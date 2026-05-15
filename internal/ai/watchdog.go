package ai

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ── Context-attached watchdog tap ───────────────────────────────────────
//
// Silent LLM calls (recheck consolidator + dismiss, runtime model
// discovery, boundary inventory, sibling clustering) pass nil as the
// onToken callback, so the per-token watchdog wiring never fires.
// One progress event per call isn't enough — the call itself can run
// silent for > IdleWatch's window, tripping the watchdog mid-call.
//
// The pattern is:
//   1. Caller attaches the watchdog tap to ctx via ContextWithTap.
//   2. Each silent call site reads it via TapFromContext.
//   3. Heartbeat (next file) periodically taps for the duration of
//      the LLM call.
//
// Using ctx.Value sidesteps adding a "tap" field to every options
// struct in the pipeline (RecheckOptions, ExecuteOptions, plus
// per-package config in project/audit). Context already flows
// everywhere; this just gives it a typed accessor.

type tapKey struct{}

// ContextWithTap attaches a watchdog tap function to ctx. Silent
// LLM call sites read it back via TapFromContext to heartbeat the
// watchdog while their underlying ChatStream is in flight.
//
// Tap is nil-safe — passing nil is the same as not attaching at all.
// TapFromContext on a context without a tap returns nil.
func ContextWithTap(ctx context.Context, tap func(string)) context.Context {
	if tap == nil {
		return ctx
	}
	return context.WithValue(ctx, tapKey{}, tap)
}

// TapFromContext returns the watchdog tap attached by ContextWithTap,
// or nil when no tap is set. Callers should nil-check before invoking.
func TapFromContext(ctx context.Context) func(string) {
	if v, ok := ctx.Value(tapKey{}).(func(string)); ok {
		return v
	}
	return nil
}

// ErrIdle is the cause attached to a context that was cancelled by an
// IdleWatch because no activity flowed within the configured window.
// Callers use errors.Is(err, ErrIdle) to distinguish idle cancellation
// from user-initiated cancellation or wall-clock deadline.
var ErrIdle = errors.New("ai client idle: no tokens or activity events")

// IdleWatch returns a derived context that cancels when no activity is
// observed for `idle` duration. "Activity" is any call to the returned
// wrapper function — typically how the agent forwards streamed tokens,
// thinking events, and tool start/done markers.
//
// Usage:
//
//	ctx, wrappedOnToken, stop := IdleWatch(parent, 240*time.Second, originalOnToken)
//	defer stop()
//	resp, err := agent.ChatStream(ctx, system, msgs, wrappedOnToken)
//
// On idle cancellation, context.Cause(ctx) returns ErrIdle. Parent
// cancellation propagates through to the derived context immediately
// and is NOT reported as idle.
//
// stop() must be called when the request completes (success, error, or
// caller-initiated cancel) to release the watchdog goroutine.
func IdleWatch(parent context.Context, idle time.Duration, onToken func(string)) (context.Context, func(string), func()) {
	ctx, cancel := context.WithCancelCause(parent)

	// reset is signaled on every activity event; the watchdog goroutine
	// waits on either reset, parent-done, or the idle timer.
	reset := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				cancel(ErrIdle)
				return
			case <-reset:
				timer.Stop()
				timer.Reset(idle)
			case <-parent.Done():
				// Parent cancellation: propagate without marking as
				// idle. The cause from the parent is preserved.
				cancel(context.Cause(parent))
				return
			case <-done:
				return
			}
		}
	}()

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			close(done)
			cancel(context.Canceled)
		})
	}

	wrapper := func(s string) {
		// Non-blocking signal: if a reset is already queued, drop this
		// one. The watchdog only needs to know "something happened
		// recently", not how many somethings.
		select {
		case reset <- struct{}{}:
		default:
		}
		if onToken != nil {
			onToken(s)
		}
	}

	return ctx, wrapper, stop
}

// pulseHeartbeatInterval is the default cadence for HeartbeatTap.
// 30s sits well below typical IdleWatch windows (240s) while keeping
// goroutine overhead minimal — a silent LLM call gets 7–8 heartbeats
// before the watchdog would otherwise trip.
const pulseHeartbeatInterval = 30 * time.Second

// HeartbeatTap starts a goroutine that calls the watchdog tap (read
// from ctx via TapFromContext) every interval until the returned
// stop function is called. Use around silent LLM calls so they don't
// trip an upstream IdleWatch when ChatStream's onToken is nil.
//
// Returns a no-op stop when no tap is attached to ctx — callers can
// always defer the returned function regardless.
//
// Usage:
//
//	stop := ai.HeartbeatTap(ctx)
//	defer stop()
//	raw, err := client.ChatStream(ctx, systemPrompt, messages, nil)
//
// The default interval is pulseHeartbeatInterval (30s). Use
// HeartbeatTapEvery to override.
func HeartbeatTap(ctx context.Context) func() {
	return HeartbeatTapEvery(ctx, pulseHeartbeatInterval)
}

// HeartbeatTapEvery is HeartbeatTap with an explicit interval.
// Exposed so tests can use a much shorter cadence to verify the
// pulse without slowing the suite down.
func HeartbeatTapEvery(ctx context.Context, interval time.Duration) func() {
	tap := TapFromContext(ctx)
	if tap == nil || interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				tap("heartbeat")
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		once.Do(func() { close(done) })
	}
}

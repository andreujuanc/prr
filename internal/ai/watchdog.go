package ai

import (
	"context"
	"errors"
	"sync"
	"time"
)

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
				if !timer.Stop() {
					// Drain a fired-but-unconsumed tick. Without this
					// the next Reset() can race against a still-pending
					// channel send and never fire.
					select {
					case <-timer.C:
					default:
					}
				}
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

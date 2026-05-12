package ai

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestIdleWatch_ResetsOnToken pins the load-bearing contract: tokens
// arriving faster than the idle threshold must NOT trigger cancellation.
// A regression here would kill legitimately-active streams.
func TestIdleWatch_ResetsOnToken(t *testing.T) {
	ctx, wrap, stop := IdleWatch(context.Background(), 100*time.Millisecond, nil)
	defer stop()

	// Send a token every 30ms for 250ms. The 100ms idle window never
	// elapses without activity, so ctx must not be cancelled.
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		wrap("tok")
		time.Sleep(30 * time.Millisecond)
	}

	if err := ctx.Err(); err != nil {
		t.Errorf("ctx cancelled despite continuous activity: %v (cause: %v)", err, context.Cause(ctx))
	}
}

// TestIdleWatch_CancelsAfterIdle is the failure-mode pin: when no
// activity flows for the full idle window, ctx must cancel with
// ErrIdle as the cause.
func TestIdleWatch_CancelsAfterIdle(t *testing.T) {
	ctx, _, stop := IdleWatch(context.Background(), 50*time.Millisecond, nil)
	defer stop()

	// Wait past the idle window with no activity.
	select {
	case <-ctx.Done():
		// Expected. Check cause.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ctx not cancelled after idle window expired")
	}

	if !errors.Is(context.Cause(ctx), ErrIdle) {
		t.Errorf("cause = %v, want ErrIdle", context.Cause(ctx))
	}
}

// TestIdleWatch_RespectsParentCancel ensures parent cancellation
// propagates immediately and is NOT mislabeled as idle. Without this,
// a user-initiated cancel would surface as "agent stalled" — confusing.
func TestIdleWatch_RespectsParentCancel(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	ctx, _, stop := IdleWatch(parent, 10*time.Second, nil)
	defer stop()

	parentCancel()

	select {
	case <-ctx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ctx did not propagate parent cancel within 200ms")
	}

	cause := context.Cause(ctx)
	if errors.Is(cause, ErrIdle) {
		t.Errorf("parent cancel mislabeled as idle: cause = %v", cause)
	}
}

// TestIdleWatch_StopReleasesGoroutine guards against goroutine leaks.
// Without proper teardown, every TUI session would accumulate one
// permanent goroutine per AI request.
func TestIdleWatch_StopReleasesGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()

	for i := 0; i < 50; i++ {
		_, _, stop := IdleWatch(context.Background(), time.Hour, nil)
		stop()
	}

	// Give goroutines a moment to wind down.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("leaked goroutines: before=%d, after=%d", before, runtime.NumGoroutine())
}

// TestIdleWatch_WrapperCallsUnderlyingOnToken makes sure that wrapping
// onToken doesn't suppress its delivery to the original caller — the
// TUI relies on receiving every token for live rendering.
func TestIdleWatch_WrapperCallsUnderlyingOnToken(t *testing.T) {
	var calls int64
	_, wrap, stop := IdleWatch(context.Background(), time.Hour, func(string) {
		atomic.AddInt64(&calls, 1)
	})
	defer stop()

	for i := 0; i < 10; i++ {
		wrap("x")
	}

	if got := atomic.LoadInt64(&calls); got != 10 {
		t.Errorf("underlying onToken called %d times, want 10", got)
	}
}

// TestIdleWatch_NilOnTokenIsSafe ensures we can pass nil for sites
// (headless) that don't need token forwarding — the watchdog still
// works.
func TestIdleWatch_NilOnTokenIsSafe(t *testing.T) {
	_, wrap, stop := IdleWatch(context.Background(), time.Hour, nil)
	defer stop()

	// Should not panic.
	wrap("anything")
}

// TestIdleWatch_DoubleStopIsSafe — defer stop() at multiple levels
// shouldn't blow up.
func TestIdleWatch_DoubleStopIsSafe(t *testing.T) {
	_, _, stop := IdleWatch(context.Background(), time.Hour, nil)
	stop()
	stop() // must not panic
}

// TestIdleWatch_ConcurrentWrapCalls pins the data-race-free contract
// when many goroutines call the wrapper simultaneously — exactly what
// happens during parallel tool execution in the agent. The race
// detector must see no flagged accesses.
func TestIdleWatch_ConcurrentWrapCalls(t *testing.T) {
	var calls int64
	_, wrap, stop := IdleWatch(context.Background(), time.Hour, func(string) {
		atomic.AddInt64(&calls, 1)
	})
	defer stop()

	const goroutines = 20
	const callsPer = 500
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < callsPer; j++ {
				wrap("tok")
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines * callsPer)
	if got := atomic.LoadInt64(&calls); got != want {
		t.Errorf("underlying onToken called %d times, want %d", got, want)
	}
}

// TestIdleWatch_ConcurrentStopAndWrap pins the race where stop() and
// many concurrent wrap() calls happen at the same time — the wrap path
// must continue to be safe after stop(), the cancel cause must
// resolve cleanly, and no panic.
func TestIdleWatch_ConcurrentStopAndWrap(t *testing.T) {
	ctx, wrap, stop := IdleWatch(context.Background(), time.Hour, nil)

	const writers = 16
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				wrap("tok")
			}
		}()
	}

	// Stop concurrently with the writers. Must not panic; must not race.
	stop()
	wg.Wait()

	// After stop, ctx is cancelled.
	if ctx.Err() == nil {
		t.Error("ctx should be cancelled after stop()")
	}
}

// TestIdleWatch_ConcurrentStopAndParentCancel — the watchdog goroutine
// might exit via parent.Done() OR via done. Both paths racing must
// produce a clean exit with no leak.
func TestIdleWatch_ConcurrentStopAndParentCancel(t *testing.T) {
	before := runtime.NumGoroutine()

	for i := 0; i < 30; i++ {
		parent, parentCancel := context.WithCancel(context.Background())
		_, _, stop := IdleWatch(parent, time.Hour, nil)
		// Race parent cancel against stop. Either order must work.
		go parentCancel()
		go stop()
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutine leak after concurrent stop+parent-cancel: before=%d, after=%d", before, runtime.NumGoroutine())
}

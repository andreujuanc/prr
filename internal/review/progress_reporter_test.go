package review

import (
	"strings"
	"testing"
)

// Pins the contract that progressReporter (the adapter that converts
// the Reporter interface into the (phase, message) string events the
// shared TUI consumes) suppresses StatusActive batch events.
//
// Why: with parallel batches the "active" messages flip the displayed
// detail line chaotically without conveying real progress. The inline
// X/Y counter + terminal-status messages (done/cached/failed) give
// users an honest read of how much is done.
//
// Watchdog behavior is preserved by WatchdogReporter, which wraps
// progressReporter and taps on every BatchProgress call regardless of
// status — that's covered by TestWatchdogReporter_ConcurrentCalls in
// batch_test.go.

func TestProgressReporter_SuppressesActiveBatch(t *testing.T) {
	var events []string
	p := &progressReporter{onProgress: func(phase, msg string) {
		events = append(events, phase+": "+msg)
	}}

	// Active first: must not emit.
	p.BatchProgress(0, StatusActive)
	if len(events) != 0 {
		t.Fatalf("StatusActive should not emit; got %v", events)
	}

	// Terminal statuses: all emit.
	p.BatchProgress(0, StatusDone)
	p.BatchProgress(1, StatusCached)
	p.BatchProgress(2, StatusFailed)
	if len(events) != 3 {
		t.Fatalf("expected 3 terminal events, got %d: %v", len(events), events)
	}
	for _, want := range []string{"Batch 1: done", "Batch 2: cached", "Batch 3: failed"} {
		found := false
		for _, e := range events {
			if strings.Contains(e, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected event containing %q, got %v", want, events)
		}
	}
}

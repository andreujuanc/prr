package review

import (
	"context"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/security"
)

// Pins the contract that progressReporter (the adapter that converts
// the Reporter interface into the (phase, message) string events the
// shared TUI consumes) suppresses StatusActive batch events.
//
// Why: with parallel batches the "active" messages flip the displayed
// detail line chaotically without conveying real progress. The inline
// X/Y counter + terminal-status messages (done/cached/failed) give
// users an honest read of how much is done.

// TestRunReviewCalls_NoDoubleCountedBatchProgress pins the contract
// that RunReviewCalls emits exactly one terminal BatchProgress per
// review call. Earlier the pipeline ALSO ran a "Mark all AOI call
// batches as done" loop after RunReviewCalls returned, double-counting
// every call (and clobbering cached/failed statuses with done). The
// shared TUI then showed nonsense like "Deep Review 16/10".
func TestRunReviewCalls_NoDoubleCountedBatchProgress(t *testing.T) {
	calls := []ReviewCall{
		{Type: "individual", AOIs: []security.AreaOfInterest{{ID: "aoi-1", File: "x.go", Line: 1}}},
		{Type: "individual", AOIs: []security.AreaOfInterest{{ID: "aoi-2", File: "y.go", Line: 2}}},
		{Type: "individual", AOIs: []security.AreaOfInterest{{ID: "aoi-3", File: "z.go", Line: 3}}},
	}

	client := &fakeAIClient{
		Responder: func(systemPrompt, _ string) string {
			return pinDeepFindingsResponse(extractAOIID(systemPrompt), "x.go", "42")
		},
	}

	// Count BatchProgress calls per status type. The pipeline's
	// OnProgress closure dispatches to BatchProgress with the real
	// terminal status — we count it here directly.
	var done, cached, failed int
	opts := ExecuteOptions{
		Mode: ModePR,
		OnProgress: func(completed, total int, fromCache bool, err error) {
			switch {
			case err != nil:
				failed++
			case fromCache:
				cached++
			default:
				done++
			}
		},
	}

	if _, err := RunReviewCalls(context.Background(), client, calls, opts); err != nil {
		t.Fatalf("RunReviewCalls: %v", err)
	}

	if got := done + cached + failed; got != len(calls) {
		t.Fatalf("OnProgress fired %d times for %d calls (%d done, %d cached, %d failed); want %d total",
			got, len(calls), done, cached, failed, len(calls))
	}
}

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

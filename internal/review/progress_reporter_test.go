package review

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/andreujuanc/prr/internal/security"
)

// Pins the contract that progressReporter (the adapter that converts
// the Reporter interface into the (phase, message) string events the
// shared TUI consumes) emits both StatusActive AND terminal statuses
// as parseable per-batch lines.
//
// Why: the Batches panel renders one row per active batch; the active
// transition is what flips a row from queued to running. Terminal
// events (done/cached/failed) flip it to the finished tail.

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

func TestProgressReporter_EmitsAllBatchStatuses(t *testing.T) {
	var events []string
	p := &progressReporter{onProgress: func(phase, msg string) {
		events = append(events, phase+": "+msg)
	}}

	p.BatchProgress(0, StatusActive)
	p.BatchProgress(0, StatusDone)
	p.BatchProgress(1, StatusCached)
	p.BatchProgress(2, StatusFailed)

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %v", len(events), events)
	}
	for _, want := range []string{"Batch 1: active", "Batch 1: done", "Batch 2: cached", "Batch 3: failed"} {
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

// TestRunReviewCalls_FiresBatchStreamForNonCachedCall pins that the
// per-batch token streaming path actually fires OnCallStream when a
// fresh (non-cached) review call produces a response. Without this
// wiring, the Batches panel's per-row bar would never update for
// real runs, only for cached ones (which is exactly backwards).
func TestRunReviewCalls_FiresBatchStreamForNonCachedCall(t *testing.T) {
	calls := []ReviewCall{
		{Type: "individual", AOIs: []security.AreaOfInterest{{ID: "aoi-1", File: "x.go", Line: 1}}},
	}

	// Response must be >=256 chars of content so the producer-side
	// throttle fires at least once.
	bigResponse := pinDeepFindingsResponse("aoi-1", "x.go", "42") +
		strings.Repeat(" ", 512) // padding pushes content over 256-byte threshold
	client := &fakeAIClient{
		Responder: func(_, _ string) string { return bigResponse },
	}

	var mu sync.Mutex
	var streamCalls []struct{ idx, bytes int }
	opts := ExecuteOptions{
		Mode: ModePR,
		OnCallStream: func(idx, bytes int) {
			mu.Lock()
			defer mu.Unlock()
			streamCalls = append(streamCalls, struct{ idx, bytes int }{idx, bytes})
		},
	}

	if _, err := RunReviewCalls(context.Background(), client, calls, opts); err != nil {
		t.Fatalf("RunReviewCalls: %v", err)
	}

	mu.Lock()
	got := streamCalls
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("expected at least one OnCallStream emit; got none")
	}
	if got[0].idx != 0 {
		t.Errorf("OnCallStream idx = %d, want 0", got[0].idx)
	}
	if got[0].bytes < 256 {
		t.Errorf("OnCallStream bytes = %d, want >=256 (first throttle threshold)", got[0].bytes)
	}
}

// Pins the format of the per-batch stream emit: `Batch K: stream
// bytes=N` with K being the 1-based batch number and N a cumulative
// byte count. The producer (RunReviewCalls / RunBatchesOnly)
// throttles to ≥256-byte deltas; progressReporter forwards verbatim.
func TestProgressReporter_BatchStreamEmitsBytesLine(t *testing.T) {
	var events []string
	p := &progressReporter{onProgress: func(phase, msg string) {
		events = append(events, phase+": "+msg)
	}}

	p.BatchStream(0, 512)
	p.BatchStream(7, 2048)

	want := []string{
		batchPhase + ": Batch 1: stream bytes=512",
		batchPhase + ": Batch 8: stream bytes=2048",
	}
	if len(events) != len(want) {
		t.Fatalf("expected %d events, got %d: %v", len(want), len(events), events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event[%d]: got %q, want %q", i, events[i], want[i])
		}
	}
}

// Pins the format of the per-batch init events emitted by InitBatches.
// The Batches panel parser reads `Batch K: init label=... files=...
// kind=...` to populate row identity. Format drift breaks the panel
// silently — the row stays "queued" forever — so we lock it here.
func TestProgressReporter_InitBatchesEmitsPerBatchIdentity(t *testing.T) {
	var events []string
	p := &progressReporter{onProgress: func(phase, msg string) {
		events = append(events, phase+": "+msg)
	}}

	p.InitBatches([]BatchInfo{
		{Label: "auth/injection [critical]", NumFiles: 2, Kind: BatchAOIDriven},
		{Label: "internal/ui", NumFiles: 4, Kind: BatchGeneral},
	})

	// First emit is the aggregate. Subsequent emits are one per batch.
	if len(events) != 3 {
		t.Fatalf("expected 1 aggregate + 2 init events, got %d: %v", len(events), events)
	}
	wantPrefixes := []string{
		batchPhase + `: Initialized 2 batches`,
		batchPhase + `: Batch 1: init label="auth/injection [critical]" files=2 kind=aoi-driven`,
		batchPhase + `: Batch 2: init label="internal/ui" files=4 kind=general`,
	}
	for i, want := range wantPrefixes {
		if !strings.HasPrefix(events[i], want) {
			t.Errorf("event[%d]: got %q, want prefix %q", i, events[i], want)
		}
	}
}

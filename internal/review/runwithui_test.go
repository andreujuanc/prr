package review

import (
	"testing"

	"github.com/andreujuanc/prr/internal/progress"
)

// Mirrors internal/audit/progress_test.go — pins the review-pipeline
// contract for the shared TUI: which message formats produce which
// counters, and how those counters drive the per-phase progress bar.

func newState() *progress.State {
	return &progress.State{Counters: make(map[string]int)}
}

// ── parseReviewEvent ────────────────────────────────────────────────────

func TestParseReviewEvent_AOIScanTotal(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "aoi", "scanning 5 file(s) for areas of interest (10 cached)")
	if s.Counters["aoi_total"] != 5 {
		t.Errorf("aoi_total = %d, want 5", s.Counters["aoi_total"])
	}
}

func TestParseReviewEvent_AOIScanProgress(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "aoi", "AOI scan 3/5 complete")
	if s.Counters["aoi_scanned"] != 3 {
		t.Errorf("aoi_scanned = %d, want 3", s.Counters["aoi_scanned"])
	}
	if s.Counters["aoi_total"] != 5 {
		t.Errorf("aoi_total = %d, want 5", s.Counters["aoi_total"])
	}
}

func TestParseReviewEvent_BatchesInitialized(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "phase1", "Initialized 39 batches")
	if s.Counters["batches_total"] != 39 {
		t.Errorf("batches_total = %d, want 39", s.Counters["batches_total"])
	}
}

func TestParseReviewEvent_BatchDoneIncrementsCounter(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "phase1", "Initialized 3 batches")
	parseReviewEvent(s, "phase1", "Batch 1: done")
	parseReviewEvent(s, "phase1", "Batch 2: cached")
	parseReviewEvent(s, "phase1", "Batch 3: failed")
	if got := s.Counters["batches_done"]; got != 3 {
		t.Errorf("batches_done = %d, want 3 (done + cached + failed)", got)
	}
}

func TestParseReviewEvent_BatchActiveDoesNotIncrement(t *testing.T) {
	// "Batch K: active" fires while a batch is in flight — must not
	// be counted as completion or progress would over-report.
	s := newState()
	parseReviewEvent(s, "phase1", "Initialized 5 batches")
	parseReviewEvent(s, "phase1", "Batch 1: active")
	parseReviewEvent(s, "phase1", "Batch 2: active")
	if s.Counters["batches_done"] != 0 {
		t.Errorf("batches_done = %d, want 0 for active-only batches", s.Counters["batches_done"])
	}
}

// ── ProgressFn ─────────────────────────────────────────────────────────

func TestAOIProgress_RatioOfCounters(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"aoi_scanned": 4, "aoi_total": 10}}
	if got := aoiProgress(s); got != 0.4 {
		t.Errorf("aoiProgress = %f, want 0.4", got)
	}
}

func TestAOIProgress_ZeroTotal(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"aoi_total": 0}}
	if got := aoiProgress(s); got != 0 {
		t.Errorf("aoiProgress with zero total = %f, want 0", got)
	}
}

func TestBatchProgress_RatioOfCounters(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"batches_done": 13, "batches_total": 39}}
	want := 13.0 / 39.0
	if got := batchProgress(s); got != want {
		t.Errorf("batchProgress = %f, want %f", got, want)
	}
}

func TestBatchProgress_ZeroTotal(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"batches_total": 0}}
	if got := batchProgress(s); got != 0 {
		t.Errorf("batchProgress with zero total = %f, want 0", got)
	}
}

// ── Recheck ────────────────────────────────────────────────────────────

func TestParseReviewEvent_RecheckProgress(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "recheck", "rechecked 50/200 findings")
	if s.Counters["recheck_done"] != 50 {
		t.Errorf("recheck_done = %d, want 50", s.Counters["recheck_done"])
	}
	if s.Counters["recheck_total"] != 200 {
		t.Errorf("recheck_total = %d, want 200", s.Counters["recheck_total"])
	}
}

func TestRecheckProgress_RatioOfCounters(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"recheck_done": 50, "recheck_total": 200}}
	if got := recheckProgress(s); got != 0.25 {
		t.Errorf("recheckProgress = %f, want 0.25", got)
	}
}

func TestRecheckProgress_ZeroTotal(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"recheck_total": 0}}
	if got := recheckProgress(s); got != 0 {
		t.Errorf("recheckProgress with zero total = %f, want 0", got)
	}
}

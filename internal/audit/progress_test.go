package audit

import (
	"testing"

	"github.com/andreujuanc/prr/internal/progress"
)

// These tests pin the audit-specific contract for the shared TUI:
// what message formats the audit pipeline emits, what counters they
// populate, and how those counters drive the per-phase progress bar.
//
// Generic phase lifecycle (waiting → active → done, warning banner,
// applyEvent dispatch) is covered in internal/progress/tui_test.go and
// not duplicated here.

func newState() *progress.State {
	return &progress.State{Counters: make(map[string]int)}
}

// ── parseAuditEvent ─────────────────────────────────────────────────────

func TestParseAuditEvent_Phase1Files(t *testing.T) {
	s := newState()
	parseAuditEvent(s, "phase1", "Phase 1 complete: 42 files to audit")
	if got := s.Counters["aoi_total"]; got != 42 {
		t.Errorf("aoi_total = %d, want 42", got)
	}
}

func TestParseAuditEvent_Phase2ScanProgress(t *testing.T) {
	s := newState()
	parseAuditEvent(s, "phase2", "AOI scan 3/5 complete")
	if s.Counters["aoi_scanned"] != 3 {
		t.Errorf("aoi_scanned = %d, want 3", s.Counters["aoi_scanned"])
	}
	if s.Counters["aoi_total"] != 5 {
		t.Errorf("aoi_total = %d, want 5", s.Counters["aoi_total"])
	}
}

func TestParseAuditEvent_Phase3ReviewTotal(t *testing.T) {
	s := newState()
	parseAuditEvent(s, "phase3", "Executing 10 review calls...")
	if s.Counters["review_total"] != 10 {
		t.Errorf("review_total = %d, want 10", s.Counters["review_total"])
	}
}

func TestParseAuditEvent_Phase3ReviewProgress(t *testing.T) {
	s := newState()
	parseAuditEvent(s, "phase3", "review 7/12")
	if s.Counters["review_done"] != 7 {
		t.Errorf("review_done = %d, want 7", s.Counters["review_done"])
	}
	if s.Counters["review_total"] != 12 {
		t.Errorf("review_total = %d, want 12", s.Counters["review_total"])
	}
}

// ── ProgressFn ─────────────────────────────────────────────────────────

func TestAOIProgress_RatioOfCounters(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"aoi_scanned": 5, "aoi_total": 10}}
	if got := aoiProgress(s); got != 0.5 {
		t.Errorf("aoiProgress = %f, want 0.5", got)
	}
}

func TestAOIProgress_ZeroTotal(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"aoi_total": 0}}
	if got := aoiProgress(s); got != 0 {
		t.Errorf("aoiProgress with zero total = %f, want 0", got)
	}
}

func TestReviewProgress_RatioOfCounters(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"review_done": 3, "review_total": 4}}
	if got := reviewProgress(s); got != 0.75 {
		t.Errorf("reviewProgress = %f, want 0.75", got)
	}
}

func TestReviewProgress_ZeroTotal(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"review_total": 0}}
	if got := reviewProgress(s); got != 0 {
		t.Errorf("reviewProgress with zero total = %f, want 0", got)
	}
}

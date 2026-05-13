package audit

import (
	"fmt"
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

// TestParseAuditEvent_PipelineEmitContracts pipes the exact format
// strings used at the audit pipeline's call sites through
// parseAuditEvent — catching any future drift between
// internal/audit/pipeline.go's fmt.Sprintf templates and the
// scanCounter format strings in parseAuditEvent. This is the contract
// that broke when the pipeline started emitting "Review X/Y complete"
// while parseAuditEvent still expected "review X/Y".
//
// If you change a fmt.Sprintf in pipeline.go that's parsed here, the
// matching case in parseAuditEvent must change too — and this test
// will fail to remind you.
func TestParseAuditEvent_PipelineEmitContracts(t *testing.T) {
	cases := []struct {
		name    string
		phase   string
		message string
		want    map[string]int
	}{
		{
			name:    "phase1 file total (pipeline.go line ~270)",
			phase:   "phase1",
			message: fmt.Sprintf("Phase 1 complete: %d files to audit", 42),
			want:    map[string]int{"aoi_total": 42},
		},
		{
			name:    "phase2 review-call total (pipeline.go line ~371)",
			phase:   "phase3",
			message: fmt.Sprintf("Executing %d review calls...", 96),
			want:    map[string]int{"review_total": 96},
		},
		{
			name:    "phase3 review done (pipeline.go line ~623)",
			phase:   "phase3",
			message: fmt.Sprintf("Review %d/%d complete", 76, 96),
			want:    map[string]int{"review_done": 76, "review_total": 96},
		},
		{
			name:    "phase3 review done — cached (pipeline.go line ~623)",
			phase:   "phase3",
			message: fmt.Sprintf("Review %d/%d complete%s", 77, 96, " (cached)"),
			want:    map[string]int{"review_done": 77, "review_total": 96},
		},
		{
			name:    "phase3 review failed (pipeline.go line ~616)",
			phase:   "phase3",
			message: fmt.Sprintf("Review %d/%d failed: %v", 78, 96, fmt.Errorf("ctx cancelled")),
			want:    map[string]int{"review_done": 78, "review_total": 96},
		},
		{
			name:    "recheck progress (RunRecheck OnProgress forwarding)",
			phase:   "recheck",
			message: fmt.Sprintf("rechecked %d/%d findings", 50, 200),
			want:    map[string]int{"recheck_done": 50, "recheck_total": 200},
		},
		{
			name:    "recheck progress at completion",
			phase:   "recheck",
			message: fmt.Sprintf("rechecked %d/%d findings", 200, 200),
			want:    map[string]int{"recheck_done": 200, "recheck_total": 200},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newState()
			parseAuditEvent(s, tc.phase, tc.message)
			for k, want := range tc.want {
				if got := s.Counters[k]; got != want {
					t.Errorf("Counters[%q] = %d, want %d (msg=%q)", k, got, want, tc.message)
				}
			}
		})
	}
}

// TestParseAuditEvent_Phase3ReviewProgress pins the message format the
// audit pipeline actually emits ("Review X/Y complete" with capital R
// and a trailing suffix). Previously this test used "review 7/12" —
// a synthetic format the pipeline never produces — and silently passed
// while the production code path showed counter "0/Y" because the
// real emit matched no branch in parseAuditEvent.
func TestParseAuditEvent_Phase3ReviewProgress(t *testing.T) {
	cases := []string{
		"Review 7/12 complete",            // standard terminal
		"Review 7/12 complete (cached)",   // cached path adds suffix
		"Review 7/12 failed: timeout",     // failed path still ticks the counter
	}
	for _, msg := range cases {
		t.Run(msg, func(t *testing.T) {
			s := newState()
			parseAuditEvent(s, "phase3", msg)
			if s.Counters["review_done"] != 7 {
				t.Errorf("review_done = %d, want 7 (msg=%q)", s.Counters["review_done"], msg)
			}
			if s.Counters["review_total"] != 12 {
				t.Errorf("review_total = %d, want 12 (msg=%q)", s.Counters["review_total"], msg)
			}
		})
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

// ── Summary functions ─────────────────────────────────────────────────

func TestFileCollectionSummary_FilledFromCounter(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"aoi_total": 42}}
	if got := fileCollectionSummary(s); got != "42 files" {
		t.Errorf("fileCollectionSummary = %q, want %q", got, "42 files")
	}
}

func TestClassifySummary_BreakdownFromCounters(t *testing.T) {
	s := &progress.State{Counters: map[string]int{
		"classify_total": 42, "classify_cached": 35, "classify_uncached": 7,
	}}
	want := "42 classified · 35 cached · 7 fresh"
	if got := classifySummary(s); got != want {
		t.Errorf("classifySummary = %q, want %q", got, want)
	}
}

func TestAOISummary_BreakdownFromCounters(t *testing.T) {
	s := &progress.State{Counters: map[string]int{
		"aoi_total": 40, "aoi_count": 32, "aoi_cached": 10,
	}}
	want := "32 AOIs across 40 files · 10 cached"
	if got := aoiSummary(s); got != want {
		t.Errorf("aoiSummary = %q, want %q", got, want)
	}
}

func TestReviewSummary_BreakdownFromCounters(t *testing.T) {
	// review_done is the cumulative position; cached/failed are
	// per-status counts. fresh = done - cached - failed.
	s := &progress.State{Counters: map[string]int{
		"review_total":  96,
		"review_done":   96,
		"review_cached": 58,
		"review_failed": 3,
	}}
	want := "35 done · 58 cached · 3 failed"
	if got := reviewSummary(s); got != want {
		t.Errorf("reviewSummary = %q, want %q", got, want)
	}
}

func TestRecheckSummary_BreakdownFromCounters(t *testing.T) {
	s := &progress.State{Counters: map[string]int{
		"recheck_kept": 11, "recheck_dismissed": 4,
		"recheck_consolidated": 2, "recheck_modified": 1,
	}}
	want := "kept 11 · dismissed 4 · consolidated 2 · modified 1"
	if got := recheckSummary(s); got != want {
		t.Errorf("recheckSummary = %q, want %q", got, want)
	}
}

func TestSummaries_EmptyBeforeAnyData(t *testing.T) {
	s := &progress.State{Counters: map[string]int{}}
	for name, fn := range map[string]func(*progress.State) string{
		"fileCollection": fileCollectionSummary,
		"classify":       classifySummary,
		"aoi":            aoiSummary,
		"review":         reviewSummary,
		"recheck":        recheckSummary,
	} {
		if got := fn(s); got != "" {
			t.Errorf("%sSummary with empty state = %q, want empty", name, got)
		}
	}
}

// ── parseAuditEvent for the new summary captures ───────────────────

func TestParseAuditEvent_ClassifyCachedCounter(t *testing.T) {
	s := newState()
	parseAuditEvent(s, "phase1b", "classifying 7 file(s) (35 cached)...")
	if got := s.Counters["classify_uncached"]; got != 7 {
		t.Errorf("classify_uncached = %d, want 7", got)
	}
	if got := s.Counters["classify_cached"]; got != 35 {
		t.Errorf("classify_cached = %d, want 35", got)
	}
	if got := s.Counters["classify_total"]; got != 42 {
		t.Errorf("classify_total = %d, want 42", got)
	}
}

func TestParseAuditEvent_AOICachedFiles(t *testing.T) {
	s := newState()
	parseAuditEvent(s, "phase2", "using cached AOI results for 10 file(s)")
	if got := s.Counters["aoi_cached"]; got != 10 {
		t.Errorf("aoi_cached = %d, want 10", got)
	}
}

func TestParseAuditEvent_AOIFoundCount(t *testing.T) {
	s := newState()
	parseAuditEvent(s, "phase2", "found 32 areas of interest")
	if got := s.Counters["aoi_count"]; got != 32 {
		t.Errorf("aoi_count = %d, want 32", got)
	}
}

func TestParseAuditEvent_ReviewCachedAndFailedCounters(t *testing.T) {
	// Cumulative position (review_done) plus per-status sub-counters
	// so the Summary can render the breakdown.
	s := newState()
	parseAuditEvent(s, "phase3", "Review 1/3 complete")
	parseAuditEvent(s, "phase3", "Review 2/3 complete (cached)")
	parseAuditEvent(s, "phase3", "Review 3/3 failed: timeout")
	if got := s.Counters["review_done"]; got != 3 {
		t.Errorf("review_done = %d, want 3", got)
	}
	if got := s.Counters["review_cached"]; got != 1 {
		t.Errorf("review_cached = %d, want 1", got)
	}
	if got := s.Counters["review_failed"]; got != 1 {
		t.Errorf("review_failed = %d, want 1", got)
	}
}

func TestParseAuditEvent_RecheckCompleteBreakdown(t *testing.T) {
	s := newState()
	parseAuditEvent(s, "recheck", "Recheck complete: kept 11, dismissed 4, consolidated 2, modified 1")
	if got := s.Counters["recheck_kept"]; got != 11 {
		t.Errorf("recheck_kept = %d, want 11", got)
	}
	if got := s.Counters["recheck_dismissed"]; got != 4 {
		t.Errorf("recheck_dismissed = %d, want 4", got)
	}
	if got := s.Counters["recheck_consolidated"]; got != 2 {
		t.Errorf("recheck_consolidated = %d, want 2", got)
	}
	if got := s.Counters["recheck_modified"]; got != 1 {
		t.Errorf("recheck_modified = %d, want 1", got)
	}
}

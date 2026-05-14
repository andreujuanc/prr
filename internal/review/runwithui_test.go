package review

import (
	"strings"
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

func TestParseReviewEvent_AOIScanProgressDoesNotOverwriteFileCount(t *testing.T) {
	// Regression: "AOI scan X/Y" emits batch counts, not file counts.
	// Previously this branch wrote to aoi_total, stomping the file
	// total set by "scanning N file(s)" and making the AOI summary
	// row read "N AOIs across <batch-count> files" mid-run.
	s := newState()
	parseReviewEvent(s, "aoi", "scanning 40 file(s) for areas of interest (0 cached)")
	parseReviewEvent(s, "aoi", "AOI scan 3/5")
	if got := s.Counters["aoi_total"]; got != 40 {
		t.Errorf("aoi_total = %d, want 40 (must not be overwritten by batch count)", got)
	}
	if got := s.Counters["aoi_batches_done"]; got != 3 {
		t.Errorf("aoi_batches_done = %d, want 3", got)
	}
	if got := s.Counters["aoi_batches_total"]; got != 5 {
		t.Errorf("aoi_batches_total = %d, want 5", got)
	}
}

func TestParseReviewEvent_BatchesInitialized(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "phase1", "Initialized 39 batches (12 AOI-driven, 27 general)")
	if s.Counters["batches_total"] != 39 {
		t.Errorf("batches_total = %d, want 39", s.Counters["batches_total"])
	}
	if s.Counters["batches_aoi_driven"] != 12 {
		t.Errorf("batches_aoi_driven = %d, want 12", s.Counters["batches_aoi_driven"])
	}
	if s.Counters["batches_general"] != 27 {
		t.Errorf("batches_general = %d, want 27", s.Counters["batches_general"])
	}
}

func TestParseReviewEvent_BatchDoneIncrementsCounter(t *testing.T) {
	// Each terminal status increments its own sub-counter so the
	// Summary row can render the breakdown. The inline X/Y is the
	// sum (see batchCounter).
	s := newState()
	parseReviewEvent(s, "phase1", "Initialized 3 batches (1 AOI-driven, 2 general)")
	parseReviewEvent(s, "phase1", "Batch 1: done")
	parseReviewEvent(s, "phase1", "Batch 2: cached")
	parseReviewEvent(s, "phase1", "Batch 3: failed")
	if got := s.Counters["batches_done"]; got != 1 {
		t.Errorf("batches_done = %d, want 1 (fresh)", got)
	}
	if got := s.Counters["batches_cached"]; got != 1 {
		t.Errorf("batches_cached = %d, want 1", got)
	}
	if got := s.Counters["batches_failed"]; got != 1 {
		t.Errorf("batches_failed = %d, want 1", got)
	}
	// Inline counter sums all three so the X/Y stays honest.
	if done, total := batchCounter(s); done != 3 || total != 3 {
		t.Errorf("batchCounter = (%d, %d), want (3, 3)", done, total)
	}
}

func TestParseReviewEvent_BatchActiveDoesNotIncrement(t *testing.T) {
	// "Batch K: active" fires while a batch is in flight — must not
	// be counted as completion or progress would over-report.
	s := newState()
	parseReviewEvent(s, "phase1", "Initialized 5 batches (2 AOI-driven, 3 general)")
	parseReviewEvent(s, "phase1", "Batch 1: active")
	parseReviewEvent(s, "phase1", "Batch 2: active")
	if done, _ := batchCounter(s); done != 0 {
		t.Errorf("batchCounter done = %d, want 0 for active-only batches", done)
	}
}

// ── ProgressFn ─────────────────────────────────────────────────────────

func TestAOIProgress_RatioOfCounters(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"aoi_batches_done": 4, "aoi_batches_total": 10}}
	if got := aoiProgress(s); got != 0.4 {
		t.Errorf("aoiProgress = %f, want 0.4", got)
	}
}

func TestAOIProgress_ZeroTotal(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"aoi_batches_total": 0}}
	if got := aoiProgress(s); got != 0 {
		t.Errorf("aoiProgress with zero total = %f, want 0", got)
	}
}

func TestBatchProgress_RatioOfCounters(t *testing.T) {
	// batches_done now means "fresh successful only". batchProgress
	// sums fresh + cached + failed for the numerator.
	s := &progress.State{Counters: map[string]int{
		"batches_done":   8,
		"batches_cached": 4,
		"batches_failed": 1,
		"batches_total":  39,
	}}
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
	parseReviewEvent(s, "recheck", "rechecked 50/200")
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

// ── Summary functions ─────────────────────────────────────────────────

func TestFetchSummary_FilledFromCounter(t *testing.T) {
	s := &progress.State{Counters: map[string]int{"fetch_files": 41}}
	if got := fetchSummary(s); got != "41 files" {
		t.Errorf("fetchSummary = %q, want %q", got, "41 files")
	}
}

func TestFetchSummary_EmptyWhenNotSet(t *testing.T) {
	s := &progress.State{Counters: map[string]int{}}
	if got := fetchSummary(s); got != "" {
		t.Errorf("fetchSummary with no counter = %q, want empty", got)
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
	want := "32 AOIs across 40 file(s) · 10 cached"
	if got := aoiSummary(s); got != want {
		t.Errorf("aoiSummary = %q, want %q", got, want)
	}
}

func TestAOISummary_ZeroAOIsMentionsGeneralReview(t *testing.T) {
	// The original "0 AOIs across N files" line was the user's main
	// complaint: it read as "nothing to review" then Deep Review fired
	// off batches anyway. The 0-AOI summary now spells out that the
	// general fallback pass will run.
	s := &progress.State{Counters: map[string]int{
		"aoi_total": 12, "aoi_count": 0, "aoi_cached": 0,
	}}
	got := aoiSummary(s)
	if !strings.Contains(got, "no security AOIs") || !strings.Contains(got, "general review") {
		t.Errorf("aoiSummary for 0 AOIs = %q, want mention of 'no security AOIs' and 'general review'", got)
	}
}

func TestDeepReviewSummary_BreakdownFromCounters(t *testing.T) {
	s := &progress.State{Counters: map[string]int{
		"batches_total":       39,
		"batches_done":        35,
		"batches_cached":      3,
		"batches_failed":      1,
		"batches_aoi_driven":  12,
		"batches_general":     27,
	}}
	want := "35 done · 3 cached · 1 failed (12 AOI-driven + 27 general)"
	if got := deepReviewSummary(s); got != want {
		t.Errorf("deepReviewSummary = %q, want %q", got, want)
	}
}

func TestDeepReviewSummary_NoBreakdownFallsBackToTallyOnly(t *testing.T) {
	// Backwards-compat: if the AOI/general counters weren't populated
	// (older emit, or a test path that bypasses the init message),
	// fall back to the plain tally without the parens.
	s := &progress.State{Counters: map[string]int{
		"batches_total":  3,
		"batches_done":   3,
		"batches_cached": 0,
		"batches_failed": 0,
	}}
	want := "3 done · 0 cached · 0 failed"
	if got := deepReviewSummary(s); got != want {
		t.Errorf("deepReviewSummary = %q, want %q", got, want)
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
		"fetch":      fetchSummary,
		"classify":   classifySummary,
		"aoi":        aoiSummary,
		"deepReview": deepReviewSummary,
		"recheck":    recheckSummary,
	} {
		if got := fn(s); got != "" {
			t.Errorf("%sSummary with empty state = %q, want empty", name, got)
		}
	}
}

// ── parseReviewEvent for the new summary captures ───────────────────

func TestParseReviewEvent_FetchFilesCounter(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "fetch", "Collected diffs for 41 files")
	if got := s.Counters["fetch_files"]; got != 41 {
		t.Errorf("fetch_files = %d, want 41", got)
	}
}

func TestParseReviewEvent_ClassifyCachedCounter(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "classify", "classifying 7 file(s) (35 cached)...")
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

func TestParseReviewEvent_AOICachedFiles(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "aoi", "using cached AOI results for 10 file(s)")
	if got := s.Counters["aoi_cached"]; got != 10 {
		t.Errorf("aoi_cached = %d, want 10", got)
	}
}

func TestParseReviewEvent_AOIFoundCount(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "aoi", "found 32 areas of interest")
	if got := s.Counters["aoi_count"]; got != 32 {
		t.Errorf("aoi_count = %d, want 32", got)
	}
}

func TestParseReviewEvent_RecheckCompleteBreakdown(t *testing.T) {
	s := newState()
	parseReviewEvent(s, "recheck", "Recheck complete: kept 11, dismissed 4, consolidated 2, modified 1")
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

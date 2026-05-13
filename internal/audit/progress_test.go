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
	parseAuditEvent(s, "phase2", "AOI scan 3/5")
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
			// Counter-only emit: just "Review N/M". Status (complete /
			// failed / cached) is a separate emit covered below.
			name:    "phase3 counter-only emit",
			phase:   "phase3",
			message: fmt.Sprintf("Review %d/%d", 76, 96),
			want:    map[string]int{"review_done": 76, "review_total": 96},
		},
		{
			// Status emit "complete (cached)" follows the counter emit
			// and ticks the cached sub-counter. Counter values are NOT
			// updated by this emit — that's the counter emit's job.
			name:    "phase3 cached status emit",
			phase:   "phase3",
			message: "complete (cached)",
			want:    map[string]int{"review_cached": 1},
		},
		{
			name:    "phase3 failed status emit",
			phase:   "phase3",
			message: "failed: ctx cancelled",
			want:    map[string]int{"review_failed": 1},
		},
		{
			name:    "recheck progress (RunRecheck OnProgress forwarding)",
			phase:   "recheck",
			message: fmt.Sprintf("rechecked %d/%d", 50, 200),
			want:    map[string]int{"recheck_done": 50, "recheck_total": 200},
		},
		{
			name:    "recheck progress at completion",
			phase:   "recheck",
			message: fmt.Sprintf("rechecked %d/%d", 200, 200),
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

// TestParseAuditEvent_Phase3ReviewCounterEmit pins the counter-only
// emit format the audit pipeline now produces per review call.
// Previously the message was "Review X/Y complete" with status baked
// in — that duplicated the counter on the TUI detail line. Now the
// counter and status are separate emits; this test pins the counter.
func TestParseAuditEvent_Phase3ReviewCounterEmit(t *testing.T) {
	s := newState()
	parseAuditEvent(s, "phase3", "Review 7/12")
	if s.Counters["review_done"] != 7 {
		t.Errorf("review_done = %d, want 7", s.Counters["review_done"])
	}
	if s.Counters["review_total"] != 12 {
		t.Errorf("review_total = %d, want 12", s.Counters["review_total"])
	}
}

// TestParseAuditEvent_Phase3StatusEmits pins the per-call status emit
// formats (cached, failed). Each is a SEPARATE emit from the counter,
// emitted second so the TUI detail line shows the status — not a
// duplicate of the counter.
func TestParseAuditEvent_Phase3StatusEmits(t *testing.T) {
	t.Run("complete (cached) increments cached sub-counter", func(t *testing.T) {
		s := newState()
		parseAuditEvent(s, "phase3", "complete (cached)")
		if s.Counters["review_cached"] != 1 {
			t.Errorf("review_cached = %d, want 1", s.Counters["review_cached"])
		}
		// Status emit must NOT touch the count — that's the counter
		// emit's job. A double-touch would inflate the bar.
		if s.Counters["review_done"] != 0 {
			t.Errorf("status emit must not advance review_done; got %d", s.Counters["review_done"])
		}
	})
	t.Run("failed: <err> increments failed sub-counter", func(t *testing.T) {
		s := newState()
		parseAuditEvent(s, "phase3", "failed: context cancelled")
		if s.Counters["review_failed"] != 1 {
			t.Errorf("review_failed = %d, want 1", s.Counters["review_failed"])
		}
	})
	t.Run("plain complete is a no-op for counters", func(t *testing.T) {
		// Plain "complete" is purely a UI detail signal — no counter
		// effect. Pin so we don't accidentally start incrementing
		// review_done from it (double-count with the counter emit).
		s := newState()
		parseAuditEvent(s, "phase3", "complete")
		if s.Counters["review_done"] != 0 || s.Counters["review_cached"] != 0 || s.Counters["review_failed"] != 0 {
			t.Errorf("plain 'complete' should not affect counters; got %+v", s.Counters)
		}
	})
}

// ── EstimateSynthesisChars ─────────────────────────────────────────────

func TestEstimateSynthesisChars(t *testing.T) {
	tests := []struct {
		findings int
		wantMin  int
		wantMax  int
	}{
		{0, 3000, 3000},     // clean audit floor
		{5, 3500, 3500},     // small audit
		{20, 5000, 5000},    // medium
		{50, 8000, 8000},    // large
		{80, 10000, 10000},  // hits the cap
		{500, 10000, 10000}, // capped at 10000
	}
	for _, tc := range tests {
		got := EstimateSynthesisChars(tc.findings)
		if got < tc.wantMin || got > tc.wantMax {
			t.Errorf("EstimateSynthesisChars(%d) = %d, want in [%d, %d]",
				tc.findings, got, tc.wantMin, tc.wantMax)
		}
	}
}

func TestEstimateSynthesisChars_MonotonicUntilCap(t *testing.T) {
	// The estimate must never decrease as findings grow — a non-monotonic
	// function would make the bar's expected size shrink mid-run if
	// future code re-derives it (it shouldn't, but pin the property).
	last := EstimateSynthesisChars(0)
	for f := 1; f <= 100; f++ {
		cur := EstimateSynthesisChars(f)
		if cur < last {
			t.Errorf("estimate dropped: f=%d gave %d, f=%d gave %d", f-1, last, f, cur)
		}
		last = cur
	}
}

// ── synthesisProgress ──────────────────────────────────────────────────

func TestSynthesisProgress_ZeroEstimateReturnsSliver(t *testing.T) {
	// Before the estimate is seeded, return a tiny non-zero value so
	// the bar reads as "starting" rather than "stuck".
	s := &progress.State{Counters: map[string]int{}}
	got := synthesisProgress(s)
	if got <= 0 || got > 0.1 {
		t.Errorf("synthesisProgress with no estimate = %g, want small non-zero (0 < x ≤ 0.1)", got)
	}
}

func TestSynthesisProgress_FillsAsCharsReceived(t *testing.T) {
	s := &progress.State{Counters: map[string]int{
		"synthesis_chars_estimated": 1000,
		"synthesis_chars_received":  500,
	}}
	got := synthesisProgress(s)
	if got != 0.5 {
		t.Errorf("synthesisProgress at half = %g, want 0.5", got)
	}
}

func TestSynthesisProgress_CapsAt95UntilFull(t *testing.T) {
	// Above 95% but under 100%, return 0.95. This prevents the bar
	// from claiming done while the LLM is still streaming the tail.
	s := &progress.State{Counters: map[string]int{
		"synthesis_chars_estimated": 1000,
		"synthesis_chars_received":  980, // 98%
	}}
	got := synthesisProgress(s)
	if got != 0.95 {
		t.Errorf("synthesisProgress at 98%% should cap at 0.95; got %g", got)
	}
}

func TestSynthesisProgress_HitsFullWhenReachedOrExceeded(t *testing.T) {
	// Once received >= estimated, the bar reads full. The pipeline
	// emits a final "synthesis received N" with the actual final
	// count to guarantee this.
	s := &progress.State{Counters: map[string]int{
		"synthesis_chars_estimated": 1000,
		"synthesis_chars_received":  1200, // exceeded estimate
	}}
	got := synthesisProgress(s)
	if got != 1.0 {
		t.Errorf("synthesisProgress at >100%% = %g, want 1.0", got)
	}
}

// ── Parser: synthesis events ───────────────────────────────────────────

func TestParseAuditEvent_SynthesisEstimateSeed(t *testing.T) {
	// The "synthesis estimate N" emit sets the estimated counter and
	// resets the received counter (covers a re-run within the same
	// session).
	s := newState()
	s.Counters["synthesis_chars_received"] = 500 // stale from a prior run
	parseAuditEvent(s, "phase4", "synthesis estimate 4000")
	if s.Counters["synthesis_chars_estimated"] != 4000 {
		t.Errorf("estimated = %d, want 4000", s.Counters["synthesis_chars_estimated"])
	}
	if s.Counters["synthesis_chars_received"] != 0 {
		t.Errorf("received should reset to 0 on new estimate; got %d", s.Counters["synthesis_chars_received"])
	}
}

func TestParseAuditEvent_SynthesisReceivedTicksBar(t *testing.T) {
	s := newState()
	parseAuditEvent(s, "phase4", "synthesis estimate 1000")
	parseAuditEvent(s, "phase4", "synthesis received 250")
	if s.Counters["synthesis_chars_received"] != 250 {
		t.Errorf("received = %d, want 250", s.Counters["synthesis_chars_received"])
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
	// Mirrors the two-emit-per-call sequence the pipeline now produces:
	// counter emit first ("Review N/M"), status emit second ("complete"
	// / "complete (cached)" / "failed: <err>"). Cumulative position
	// comes from the counter emits; per-status sub-counters come from
	// the status emits.
	s := newState()
	// Call 1: success.
	parseAuditEvent(s, "phase3", "Review 1/3")
	parseAuditEvent(s, "phase3", "complete")
	// Call 2: cache hit.
	parseAuditEvent(s, "phase3", "Review 2/3")
	parseAuditEvent(s, "phase3", "complete (cached)")
	// Call 3: failure.
	parseAuditEvent(s, "phase3", "Review 3/3")
	parseAuditEvent(s, "phase3", "failed: timeout")

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

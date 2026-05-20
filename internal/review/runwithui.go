package review

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/progress"
)

// Review-side phase list, counter parsing, and summary renderer for
// the shared progress.TUI. The audit equivalent is in
// internal/audit/progress.go — keep these symmetrical when adding new
// phases or counters so future progress-UI work flows naturally to
// both modes.

var (
	rTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	rInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))
)

// PRReviewPhases is the review pipeline's phase order. Names match the
// event keys emitted by progressReporter in internal/review/pipeline.go.
//
// Exported so the in-app TUI can render the same phase list as the
// headless `prr review` UI — the two views share this definition
// rather than maintaining parallel copies.
//
// "discovery" covers project-context + PR-brief — usually cache-hit
// fast. "aoi" is the security pre-scan — the slow part of pre-review.
// Splitting them lets the TUI show real progress instead of a single
// row that flashes past in milliseconds.
func PRReviewPhases() []progress.PhaseDef {
	return []progress.PhaseDef{
		{Name: "fetch", Label: "Fetch PR",
			Summary: fetchSummary},
		{Name: "discovery", Label: "Discovery"},
		{Name: "classify", Label: "Classification",
			Summary: classifySummary},
		{Name: "aoi", Label: "AOI Pre-scan",
			ProgressFn:  aoiProgress,
			Counter:     aoiCounter,
			CounterUnit: "batches",
			Summary:     aoiSummary},
		{Name: "phase1", Label: "Deep Review",
			ProgressFn:  batchProgress,
			Counter:     batchCounter,
			CounterUnit: "batches",
			Summary:     deepReviewSummary},
		{Name: "recheck", Label: "Recheck",
			ProgressFn:  recheckProgress,
			Counter:     recheckCounter,
			CounterUnit: "findings",
			Summary:     recheckSummary},
		{Name: "phase2", Label: "Synthesis", ProgressFn: synthesisProgress},
	}
}

// ── Summary functions ──────────────────────────────────────────────────

func fetchSummary(s *progress.State) string {
	if n := s.Counters["fetch_files"]; n > 0 {
		return fmt.Sprintf("%d files", n)
	}
	return ""
}

func classifySummary(s *progress.State) string {
	total := s.Counters["classify_total"]
	if total == 0 {
		return ""
	}
	cached := s.Counters["classify_cached"]
	uncached := s.Counters["classify_uncached"]
	return fmt.Sprintf("%d classified · %d cached · %d fresh", total, cached, uncached)
}

func aoiSummary(s *progress.State) string {
	files := s.Counters["aoi_total"]
	aois := s.Counters["aoi_count"]
	cached := s.Counters["aoi_cached"]
	if files == 0 && aois == 0 && cached == 0 {
		return ""
	}
	// The 0-AOI case is what tripped users: they'd see "0 AOIs across
	// N files" and assume Deep Review had nothing to do, then watch it
	// fire off batches anyway. Spell out that those batches are the
	// general fallback pass — independent of AOI findings.
	if aois == 0 && files > 0 {
		if cached > 0 {
			return fmt.Sprintf("no security AOIs · %d file(s) scanned · %d cached (general review will run)", files, cached)
		}
		return fmt.Sprintf("no security AOIs · %d file(s) scanned (general review will run)", files)
	}
	return fmt.Sprintf("%d AOIs across %d file(s) · %d cached", aois, files, cached)
}

func deepReviewSummary(s *progress.State) string {
	total := s.Counters["batches_total"]
	if total == 0 {
		return ""
	}
	done := s.Counters["batches_done"]
	cached := s.Counters["batches_cached"]
	failed := s.Counters["batches_failed"]
	if done == 0 && cached == 0 && failed == 0 {
		// Mid-init, before any batch has completed — still useful to
		// show the AOI/general split so the user knows what kind of
		// work is queued. The bar already shows 0/N from batchCounter.
		aoi := s.Counters["batches_aoi_driven"]
		general := s.Counters["batches_general"]
		if aoi == 0 && general == 0 {
			return ""
		}
		return fmt.Sprintf("%d AOI-driven + %d general", aoi, general)
	}
	aoi := s.Counters["batches_aoi_driven"]
	general := s.Counters["batches_general"]
	base := fmt.Sprintf("%d done · %d cached · %d failed", done, cached, failed)
	if aoi == 0 && general == 0 {
		return base
	}
	return fmt.Sprintf("%s (%d AOI-driven + %d general)", base, aoi, general)
}

func recheckSummary(s *progress.State) string {
	kept := s.Counters["recheck_kept"]
	dismissed := s.Counters["recheck_dismissed"]
	consolidated := s.Counters["recheck_consolidated"]
	modified := s.Counters["recheck_modified"]
	if kept == 0 && dismissed == 0 && consolidated == 0 && modified == 0 {
		return ""
	}
	return fmt.Sprintf("kept %d · dismissed %d · consolidated %d · modified %d",
		kept, dismissed, consolidated, modified)
}

// aoiCounter / batchCounter expose the same counters that drive the
// progress bar so the TUI can render an inline "X/Y" alongside the
// phase label.
//
// The inline counter ticks per BATCH (one tick per LLM call complete),
// not per file — that's the only thing the scanner gives us mid-run.
// The summary row uses aoi_total (files) so the "across N files" stays
// honest.
func aoiCounter(s *progress.State) (done, total int) {
	return s.Counters["aoi_batches_done"], s.Counters["aoi_batches_total"]
}

func batchCounter(s *progress.State) (done, total int) {
	// "done" for the inline X/Y is the sum of all terminal-status
	// counters (fresh done + cached + failed). The breakdown is
	// preserved in the individual counters for the Summary row.
	return s.Counters["batches_done"] +
		s.Counters["batches_cached"] +
		s.Counters["batches_failed"], s.Counters["batches_total"]
}

// recheckCounter / recheckProgress drive the Recheck row's "X/Y" and
// progress bar from "rechecked X/Y findings" emit events.
func recheckCounter(s *progress.State) (done, total int) {
	return s.Counters["recheck_done"], s.Counters["recheck_total"]
}

func recheckProgress(s *progress.State) float64 {
	total := s.Counters["recheck_total"]
	if total == 0 {
		return 0
	}
	return float64(s.Counters["recheck_done"]) / float64(total)
}

// aoiProgress reports the AOI pre-scan ratio. The bar advances per
// batch, since that's the only granularity the scanner emits (one
// "AOI scan X/Y" per batch completion).
func aoiProgress(s *progress.State) float64 {
	total := s.Counters["aoi_batches_total"]
	if total == 0 {
		return 0
	}
	return float64(s.Counters["aoi_batches_done"]) / float64(total)
}

// batchProgress reports the deep-review batch ratio. The pipeline emits
// "Initialized N batches" (total) and "Batch K: done/cached/failed"
// (each completion). The done numerator sums all three terminal-status
// sub-counters; see batchCounter for the rationale.
func batchProgress(s *progress.State) float64 {
	total := s.Counters["batches_total"]
	if total == 0 {
		return 0
	}
	done := s.Counters["batches_done"] +
		s.Counters["batches_cached"] +
		s.Counters["batches_failed"]
	return float64(done) / float64(total)
}

// synthesisProgress drives the synthesis row's bar from streamed-byte
// counters. Replaces the previous sin-wave pulse which oscillated
// 0.05↔0.95 and made the percentage visibly go DOWN — users read
// that as a UI bug. Now: monotonic fill as content streams in,
// capped at 95% until the explicit completion signal arrives.
//
// Before the estimate is seeded, returns 0.03 so the bar shows a
// sliver rather than reading as "not started".
func synthesisProgress(s *progress.State) float64 {
	estimated := s.Counters["synthesis_chars_estimated"]
	received := s.Counters["synthesis_chars_received"]
	if estimated == 0 {
		return 0.03
	}
	pct := float64(received) / float64(estimated)
	if pct >= 1.0 {
		return 1.0
	}
	if pct > 0.95 {
		return 0.95
	}
	return pct
}

// parseReviewEvent extracts review-pipeline counters from the
// (phase, message) event stream. Each branch matches a known format
// in pipeline.go and is logged via scanCounter on format drift.
//
// If pipeline message strings change, update the format strings here
// and add a test case.
func parseReviewEvent(s *progress.State, phase, message string) {
	// Per-batch lifecycle (init/active/stream/done/cached/failed)
	// populates the Batches panel state. Both phase1 (Deep Review)
	// and recheck emit these — phase transitions reset the map so
	// the rows don't bleed across.
	if (phase == "phase1" || phase == "recheck") && strings.HasPrefix(message, "Batch ") {
		progress.ParseBatchEvent(s, message)
	}
	switch {
	// aoi: AOI pre-scan progress
	case phase == "aoi" && strings.Contains(message, "scanning") && strings.Contains(message, "for areas of interest"):
		// "scanning N file(s) for areas of interest (M cached)"
		var n int
		if scanCounter(phase, message, "scanning %d file(s)", &n) {
			s.Counters["aoi_total"] = n
		}
	case phase == "aoi" && strings.HasPrefix(message, "AOI scan "):
		// Counter-only emit from internal/security/scanner.go:
		// "AOI scan X/Y" where X/Y are BATCH counts. Stored separately
		// from aoi_total (which is the file count from the "scanning N
		// file(s)" emit) — previously these collided and aoi_total
		// flipped from "files scanned" to "batches completed" mid-run,
		// making the summary line render "0 AOIs across 3 files" when
		// actually 40 files in 3 batches had been scanned.
		var done, total int
		if scanCounter(phase, message, "AOI scan %d/%d", &done, &total) {
			s.Counters["aoi_batches_done"] = done
			s.Counters["aoi_batches_total"] = total
		}

	// phase1: batch progress
	case phase == "phase1" && strings.HasPrefix(message, "Initialized ") && strings.Contains(message, "batches "):
		// "Initialized N batches (X AOI-driven, Y general)" — the
		// breakdown is what answers "if AOIs were 0, why is Deep Review
		// running anything?" for the user.
		var total, aoi, general int
		if scanCounter(phase, message,
			"Initialized %d batches (%d AOI-driven, %d general)",
			&total, &aoi, &general) {
			s.Counters["batches_total"] = total
			s.Counters["batches_aoi_driven"] = aoi
			s.Counters["batches_general"] = general
		}
	case phase == "phase1" && strings.HasPrefix(message, "Batch "):
		// "Batch K: done|cached|failed|active" — increment per-status
		// sub-counters on terminal statuses; ignore "active".
		// batches_done is fresh-successful only; the inline counter
		// callback sums all three so the "X/Y" shows total progress.
		var k int
		var status string
		if scanCounter(phase, message, "Batch %d: %s", &k, &status) {
			switch status {
			case "done":
				s.Counters["batches_done"]++
			case "cached":
				s.Counters["batches_cached"]++
			case "failed":
				s.Counters["batches_failed"]++
			}
		}

	case phase == "recheck" && strings.HasPrefix(message, "rechecked "):
		// Pipeline emits "rechecked X/Y" (counter-only). The earlier
		// "rechecked X/Y findings" form duplicated the X/Y on the TUI
		// detail line; "findings" is now implicit from the phase label.
		var done, total int
		if scanCounter(phase, message, "rechecked %d/%d", &done, &total) {
			s.Counters["recheck_done"] = done
			s.Counters["recheck_total"] = total
		}

	case phase == "recheck" && strings.HasPrefix(message, "Recheck complete:"):
		// Final terminal event with the breakdown for the Summary row.
		var kept, dismissed, consolidated, modified int
		if scanCounter(phase, message,
			"Recheck complete: kept %d, dismissed %d, consolidated %d, modified %d",
			&kept, &dismissed, &consolidated, &modified) {
			s.Counters["recheck_kept"] = kept
			s.Counters["recheck_dismissed"] = dismissed
			s.Counters["recheck_consolidated"] = consolidated
			s.Counters["recheck_modified"] = modified
		}

	case phase == "phase2" && strings.HasPrefix(message, "synthesis estimate "):
		// Seed for synthesisProgress. Sent once before the LLM call.
		var n int
		if scanCounter(phase, message, "synthesis estimate %d", &n) {
			s.Counters["synthesis_chars_estimated"] = n
			s.Counters["synthesis_chars_received"] = 0
		}
	case phase == "phase2" && strings.HasPrefix(message, "synthesis received "):
		// Throttled emit from the streaming onToken counter.
		var n int
		if scanCounter(phase, message, "synthesis received %d", &n) {
			s.Counters["synthesis_chars_received"] = n
		}

	case phase == "classify" && strings.Contains(message, "classifying") && strings.Contains(message, "cached"):
		// classify package emits "classifying N file(s) (M cached)...".
		var uncached, cached int
		if scanCounter(phase, message, "classifying %d file(s) (%d cached)...", &uncached, &cached) {
			s.Counters["classify_uncached"] = uncached
			s.Counters["classify_cached"] = cached
			s.Counters["classify_total"] = uncached + cached
		}

	case phase == "aoi" && strings.HasPrefix(message, "using cached AOI results"):
		// "using cached AOI results for N file(s)".
		var n int
		if scanCounter(phase, message, "using cached AOI results for %d file(s)", &n) {
			s.Counters["aoi_cached"] = n
		}

	case phase == "aoi" && strings.HasPrefix(message, "found ") && strings.Contains(message, "areas of interest"):
		// "found N areas of interest".
		var n int
		if scanCounter(phase, message, "found %d areas of interest", &n) {
			s.Counters["aoi_count"] = n
		}

	case phase == "fetch" && strings.HasPrefix(message, "Collected diffs for "):
		// "Collected diffs for N files".
		var n int
		if scanCounter(phase, message, "Collected diffs for %d files", &n) {
			s.Counters["fetch_files"] = n
		}
	}
}

// scanCounter wraps fmt.Sscanf with a logged warning on format mismatch.
// Kept package-local (audit has the same helper) so review-side format
// drift is surfaced separately from audit-side drift.
func scanCounter(phase, message, format string, dest ...any) bool {
	n, err := fmt.Sscanf(message, format, dest...)
	if err != nil || n != len(dest) {
		log.Printf("progress: counter parse mismatch in phase=%q (format=%q, message=%q): %v",
			phase, format, message, err)
		return false
	}
	return true
}

// renderReviewSummary builds the TUI footer shown after the review
// completes. Compact — the caller (cmd/prr/main.go) prints the full
// per-finding detail to stderr separately after the TUI exits.
func renderReviewSummary(result *PRReviewResult, elapsed time.Duration) string {
	if result == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(rTitle.Render("  Review Complete"))
	b.WriteString("\n\n")

	if result.PR != nil {
		b.WriteString(fmt.Sprintf("  PR         %s\n", rInfo.Render(fmt.Sprintf("#%d  %s", result.PR.Number, result.PR.Title))))
	}
	if result.StructuredReview != nil {
		b.WriteString(fmt.Sprintf("  Verdict    %s\n", rInfo.Render(result.StructuredReview.Verdict)))
		b.WriteString(fmt.Sprintf("  Findings   %s\n", rInfo.Render(fmt.Sprintf("%d", len(result.StructuredReview.Findings)))))
	} else if len(result.DeepFindings) > 0 {
		b.WriteString(fmt.Sprintf("  Findings   %s\n", rInfo.Render(fmt.Sprintf("%d (deep)", len(result.DeepFindings)))))
	}
	b.WriteString(fmt.Sprintf("  Files      %s\n", rInfo.Render(fmt.Sprintf("%d", result.FilesReviewed))))
	b.WriteString(fmt.Sprintf("  Time       %s\n", rInfo.Render(elapsed.Truncate(time.Second).String())))
	return b.String()
}

// ── Entry point ────────────────────────────────────────────────────────

// RunWithUI executes a headless PR review with the shared progress
// TUI. Mirrors audit.RunWithUI.
func RunWithUI(
	ctx context.Context,
	reviewClient ai.Client,
	aoiClient ai.Client,
	opts PRReviewOptions,
	headerSubtitle, headerInfo string,
) (*PRReviewResult, error) {
	// Derived cancellable ctx so the TUI's OnCancel can stop the
	// in-flight LLM call when the user Ctrl+Cs. Without this the
	// background goroutine orphans on the synthesis call until it
	// completes — wasting tokens and leaking the goroutine.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var result *PRReviewResult

	header := progress.Header{
		Title:    "  prr review",
		Subtitle: headerSubtitle,
		Info:     headerInfo,
	}

	cfg := progress.Config{
		Header:      header,
		Phases:      PRReviewPhases(),
		BatchPhases: []string{"phase1", "recheck"},
		ParseEvent:  parseReviewEvent,
		Summary: func(_ error, elapsed time.Duration) string {
			return renderReviewSummary(result, elapsed)
		},
		OnCancel: cancel,
		RunTask: func(emit func(phase, message string)) error {
			r, err := RunPRReview(runCtx, reviewClient, aoiClient, opts, emit)
			result = r
			return err
		},
	}

	if err := progress.Run(cfg); err != nil {
		return result, err
	}
	return result, nil
}

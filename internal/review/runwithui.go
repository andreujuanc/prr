package review

import (
	"context"
	"fmt"
	"log"
	"math"
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

// reviewPhases is the review pipeline's phase order. Names match the
// event keys emitted by progressReporter in internal/review/pipeline.go.
//
// "discovery" covers project-context + PR-brief — usually cache-hit
// fast. "aoi" is the security pre-scan — the slow part of pre-review.
// Splitting them lets the TUI show real progress instead of a single
// row that flashes past in milliseconds.
func reviewPhases() []progress.PhaseDef {
	return []progress.PhaseDef{
		{Name: "fetch", Label: "Fetch PR",
			Summary: fetchSummary},
		{Name: "discovery", Label: "Discovery"},
		{Name: "classify", Label: "Classification",
			Summary: classifySummary},
		{Name: "aoi", Label: "AOI Pre-scan",
			ProgressFn: aoiProgress,
			Counter:    aoiCounter,
			Summary:    aoiSummary},
		{Name: "phase1", Label: "Deep Review",
			ProgressFn: batchProgress,
			Counter:    batchCounter,
			Summary:    deepReviewSummary},
		{Name: "recheck", Label: "Recheck",
			ProgressFn: recheckProgress,
			Counter:    recheckCounter,
			Summary:    recheckSummary},
		{Name: "phase2", Label: "Synthesis", ProgressFn: synthesisPulse},
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
	if files == 0 && aois == 0 {
		return ""
	}
	return fmt.Sprintf("%d AOIs across %d files · %d cached", aois, files, cached)
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
		return ""
	}
	return fmt.Sprintf("%d done · %d cached · %d failed", done, cached, failed)
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
func aoiCounter(s *progress.State) (done, total int) {
	return s.Counters["aoi_scanned"], s.Counters["aoi_total"]
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

// aoiProgress reports the AOI pre-scan ratio. The pipeline emits
// "AOI scan X/Y complete" lines during scanning.
func aoiProgress(s *progress.State) float64 {
	total := s.Counters["aoi_total"]
	if total == 0 {
		return 0
	}
	return float64(s.Counters["aoi_scanned"]) / float64(total)
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

// synthesisPulse animates the synthesis bar — a single LLM call, no
// granular sub-steps, so the value oscillates with time to signal
// liveness rather than progress. Matches audit's pulse so both modes
// feel the same during synthesis.
func synthesisPulse(s *progress.State) float64 {
	return 0.5 + 0.45*math.Sin(s.Elapsed.Seconds()*math.Pi/2)
}

// parseReviewEvent extracts review-pipeline counters from the
// (phase, message) event stream. Each branch matches a known format
// in pipeline.go and is logged via scanCounter on format drift.
//
// If pipeline message strings change, update the format strings here
// and add a test case.
func parseReviewEvent(s *progress.State, phase, message string) {
	switch {
	// aoi: AOI pre-scan progress
	case phase == "aoi" && strings.Contains(message, "scanning") && strings.Contains(message, "for areas of interest"):
		// "scanning N file(s) for areas of interest (M cached)"
		var n int
		if scanCounter(phase, message, "scanning %d file(s)", &n) {
			s.Counters["aoi_total"] = n
		}
	case phase == "aoi" && strings.Contains(message, "AOI scan") && strings.Contains(message, "complete"):
		// "AOI scan X/Y complete"
		var done, total int
		if scanCounter(phase, message, "AOI scan %d/%d complete", &done, &total) {
			s.Counters["aoi_scanned"] = done
			s.Counters["aoi_total"] = total
		}

	// phase1: batch progress
	case phase == "phase1" && strings.Contains(message, "Initialized") && strings.Contains(message, "batches"):
		// "Initialized N batches"
		var n int
		if scanCounter(phase, message, "Initialized %d batches", &n) {
			s.Counters["batches_total"] = n
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
func scanCounter(phase, message, format string, dest ...interface{}) bool {
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
		Header:     header,
		Phases:     reviewPhases(),
		ParseEvent: parseReviewEvent,
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

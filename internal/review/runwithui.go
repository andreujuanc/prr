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
		{Name: "fetch", Label: "Fetch PR"},
		{Name: "discovery", Label: "Discovery"},
		{Name: "classify", Label: "Classification"},
		{Name: "aoi", Label: "AOI Pre-scan",
			ProgressFn: aoiProgress,
			Counter:    aoiCounter},
		{Name: "phase1", Label: "Deep Review",
			ProgressFn: batchProgress,
			Counter:    batchCounter},
		{Name: "recheck", Label: "Recheck"},
		{Name: "phase2", Label: "Synthesis", ProgressFn: synthesisPulse},
	}
}

// aoiCounter / batchCounter expose the same counters that drive the
// progress bar so the TUI can render an inline "X/Y" alongside the
// phase label.
func aoiCounter(s *progress.State) (done, total int) {
	return s.Counters["aoi_scanned"], s.Counters["aoi_total"]
}

func batchCounter(s *progress.State) (done, total int) {
	return s.Counters["batches_done"], s.Counters["batches_total"]
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
// (each completion).
func batchProgress(s *progress.State) float64 {
	total := s.Counters["batches_total"]
	if total == 0 {
		return 0
	}
	return float64(s.Counters["batches_done"]) / float64(total)
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
		// "Batch K: done|cached|failed|active" — increment done on
		// terminal statuses, ignore "active".
		var k int
		var status string
		if scanCounter(phase, message, "Batch %d: %s", &k, &status) {
			switch status {
			case "done", "cached", "failed":
				s.Counters["batches_done"]++
			}
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

package audit

import (
	"context"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/progress"
)

// ── Audit-specific styling (used in the summary footer only) ───────────

var (
	pTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	pInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))
)

// ── Phase list ─────────────────────────────────────────────────────────

// auditPhases is the audit pipeline's phase order shown in the TUI.
// Names must match the strings passed to OnProgress at runtime
// (internal/audit/pipeline.go).
func auditPhases() []progress.PhaseDef {
	return []progress.PhaseDef{
		{Name: "phase0", Label: "Project Discovery"},
		{Name: "phase1", Label: "File Collection"},
		{Name: "phase1b", Label: "Classification"},
		{Name: "phase2", Label: "AOI Pre-scan",
			ProgressFn: aoiProgress,
			Counter:    aoiCounter},
		{Name: "phase3", Label: "Deep Review",
			ProgressFn: reviewProgress,
			Counter:    reviewCounter},
		{Name: "recheck", Label: "Recheck",
			ProgressFn: recheckProgress,
			Counter:    recheckCounter},
		{Name: "phase4", Label: "Synthesis", ProgressFn: synthesisPulse},
	}
}

// aoiCounter / reviewCounter expose the same counters that drive the
// progress bar so the TUI can render an inline "X/Y" alongside the
// phase label.
func aoiCounter(s *progress.State) (done, total int) {
	return s.Counters["aoi_scanned"], s.Counters["aoi_total"]
}

func reviewCounter(s *progress.State) (done, total int) {
	return s.Counters["review_done"], s.Counters["review_total"]
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

// aoiProgress reports the AOI pre-scan progress bar value.
func aoiProgress(s *progress.State) float64 {
	total := s.Counters["aoi_total"]
	if total == 0 {
		return 0
	}
	return float64(s.Counters["aoi_scanned"]) / float64(total)
}

// reviewProgress reports the deep-review progress bar value.
func reviewProgress(s *progress.State) float64 {
	total := s.Counters["review_total"]
	if total == 0 {
		return 0
	}
	return float64(s.Counters["review_done"]) / float64(total)
}

// synthesisPulse animates a pulsing bar — synthesis is a single LLM
// call without granular sub-steps, so we show motion to signal liveness.
// Oscillates between 0.05 and 0.95 with ~4s period.
func synthesisPulse(s *progress.State) float64 {
	return 0.5 + 0.45*math.Sin(s.Elapsed.Seconds()*math.Pi/2)
}

// ── Event parsing ──────────────────────────────────────────────────────

// parseAuditEvent extracts progress-bar counters from the audit's
// (phase, message) event stream. Each branch matches a known format
// string; format drift surfaces as a log warning via scanCounter
// instead of silently zeroing the counter.
//
// If audit pipeline message formats change, update the format strings
// here. Tests in this package pin the contracts.
func parseAuditEvent(s *progress.State, phase, message string) {
	switch {
	case phase == "phase1" && strings.Contains(message, "files to audit"):
		var n int
		if scanCounter(phase, message, "Phase 1 complete: %d files to audit", &n) {
			s.Counters["aoi_total"] = n
		}
	case phase == "phase2" && strings.Contains(message, "Scanning"):
		var n int
		if scanCounter(phase, message, "Scanning %d files", &n) {
			s.Counters["aoi_total"] = n
		}
	case phase == "phase2" && strings.Contains(message, "complete"):
		var done, total int
		if scanCounter(phase, message, "AOI scan %d/%d complete", &done, &total) {
			s.Counters["aoi_scanned"] = done
			s.Counters["aoi_total"] = total
		}
	case phase == "phase3" && strings.Contains(message, "Executing"):
		var n int
		if scanCounter(phase, message, "Executing %d review calls...", &n) {
			s.Counters["review_total"] = n
		}
	case phase == "phase3" && strings.HasPrefix(message, "Review "):
		// Pipeline emits "Review X/Y complete" / "Review X/Y complete (cached)"
		// / "Review X/Y failed: <err>". All three are terminal events that
		// should tick the done counter — Sscanf with "Review %d/%d" matches
		// the prefix and ignores the trailing suffix.
		var done, total int
		if scanCounter(phase, message, "Review %d/%d", &done, &total) {
			s.Counters["review_done"] = done
			s.Counters["review_total"] = total
		}
	case phase == "recheck" && strings.HasPrefix(message, "rechecked "):
		// Pipeline emits "rechecked X/Y findings" via RunRecheck's
		// OnProgress callback forwarding.
		var done, total int
		if scanCounter(phase, message, "rechecked %d/%d", &done, &total) {
			s.Counters["recheck_done"] = done
			s.Counters["recheck_total"] = total
		}
	}
}

// scanCounter wraps fmt.Sscanf with a logged warning on format mismatch.
func scanCounter(phase, message, format string, dest ...interface{}) bool {
	n, err := fmt.Sscanf(message, format, dest...)
	if err != nil || n != len(dest) {
		log.Printf("progress: counter parse mismatch in phase=%q (format=%q, message=%q): %v",
			phase, format, message, err)
		return false
	}
	return true
}

// ── Summary footer ─────────────────────────────────────────────────────

// renderAuditSummary builds the post-run summary box. The closure passed
// to progress.Run captures the *Result so we can pull live numbers
// without smuggling them through the shared TUI.
func renderAuditSummary(result *Result, elapsed time.Duration) string {
	if result == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(pTitle.Render("  Results"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("  Files      %s\n", pInfo.Render(fmt.Sprintf("%d", result.FilesScanned))))
	b.WriteString(fmt.Sprintf("  AOIs       %s\n", pInfo.Render(fmt.Sprintf("%d", result.AOIsGenerated))))
	b.WriteString(fmt.Sprintf("  Reviews    %s\n", pInfo.Render(fmt.Sprintf("%d (%d individual, %d grouped)",
		result.ReviewCalls, result.IndividualReviews, result.GroupedReviews))))
	b.WriteString(fmt.Sprintf("  Findings   %s\n", pInfo.Render(fmt.Sprintf("%d", len(result.Findings)))))
	b.WriteString(fmt.Sprintf("  Dismissed  %s\n", pInfo.Render(fmt.Sprintf("%d", result.Dismissals))))
	b.WriteString(fmt.Sprintf("  Time       %s\n", pInfo.Render(elapsed.Truncate(time.Second).String())))
	return b.String()
}

// ── Entry point ────────────────────────────────────────────────────────

// RunWithUI executes the audit with the shared progress TUI.
// Returns the audit result and synthesis after completion.
func RunWithUI(
	ctx context.Context,
	reviewClient ai.Client,
	aoiClient ai.Client,
	opts Options,
	reviewModel, aoiModel string,
	noSynthesis bool,
) (*Result, *SynthesisResult, error) {
	// Derived cancellable ctx so the TUI's OnCancel can stop the
	// in-flight LLM call when the user Ctrl+Cs.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		result    *Result
		synthesis *SynthesisResult
	)

	header := progress.Header{
		Title:    "  prr audit",
		Subtitle: filepath.Base(opts.RepoRoot),
		Info:     fmt.Sprintf("review: %s  aoi: %s", reviewModel, aoiModel),
	}

	cfg := progress.Config{
		Header:     header,
		Phases:     auditPhases(),
		ParseEvent: parseAuditEvent,
		Summary: func(_ error, elapsed time.Duration) string {
			return renderAuditSummary(result, elapsed)
		},
		OnCancel: cancel,
		RunTask: func(emit func(phase, message string)) error {
			r, err := Run(runCtx, reviewClient, aoiClient, opts, emit)
			result = r
			if err != nil {
				return err
			}
			if r != nil && len(r.Findings) > 0 && !noSynthesis {
				emit("phase4", "Synthesizing executive summary...")
				s, synthErr := SynthesizeCached(runCtx, reviewClient, r.Findings, r.CrossCuttingObservations, r.ProjectContext, nil, opts.NoCache)
				if synthErr != nil {
					emit("phase4", "Synthesis failed: "+synthErr.Error())
					// Non-fatal — continue without synthesis
				} else {
					emit("phase4", "Synthesis complete")
					synthesis = s
				}
				r.Usage.Synth = ai.SnapshotUsage(reviewClient)
			}
			return nil
		},
	}

	if err := progress.Run(cfg); err != nil {
		return result, synthesis, err
	}
	return result, synthesis, nil
}

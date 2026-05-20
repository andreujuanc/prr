package audit

import (
	"context"
	"fmt"
	"log"
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
		{Name: "phase1", Label: "File Collection",
			Summary: fileCollectionSummary},
		{Name: "phase1b", Label: "Classification",
			Summary: classifySummary},
		{Name: "phase2", Label: "AOI Pre-scan",
			ProgressFn: aoiProgress,
			Counter:    aoiCounter,
			Summary:    aoiSummary},
		{Name: "phase3", Label: "Deep Review",
			ProgressFn: reviewProgress,
			Counter:    reviewCounter,
			Summary:    reviewSummary},
		{Name: "recheck", Label: "Recheck",
			ProgressFn: recheckProgress,
			Counter:    recheckCounter,
			Summary:    recheckSummary},
		{Name: "phase4", Label: "Synthesis", ProgressFn: synthesisProgress},
	}
}

// ── Summary functions ──────────────────────────────────────────────────
//
// Each returns a stable, structured description of what the phase
// accomplished, rendered as the row's detail line when the phase
// reaches `done` state. Empty string falls back to the live detail.

func fileCollectionSummary(s *progress.State) string {
	n := s.Counters["aoi_total"]
	if n == 0 {
		return ""
	}
	skipped := s.Counters["file_skipped_binary"] +
		s.Counters["file_skipped_large"] +
		s.Counters["file_skipped_empty"] +
		s.Counters["file_skipped_symlink"] +
		s.Counters["file_skipped_missing"] +
		s.Counters["file_skipped_errored"]
	if skipped == 0 {
		return fmt.Sprintf("%d files", n)
	}
	return fmt.Sprintf("%d files · %d skipped", n, skipped)
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
	// Audit has no fallback path — if AOIs=0 the audit ends here, so
	// say so. Deep Review row will get "No areas of interest found —
	// audit complete." separately and skip running anything.
	if aois == 0 && files > 0 {
		if cached > 0 {
			return fmt.Sprintf("no AOIs · %d file(s) scanned · %d cached", files, cached)
		}
		return fmt.Sprintf("no AOIs · %d file(s) scanned", files)
	}
	return fmt.Sprintf("%d AOIs across %d file(s) · %d cached", aois, files, cached)
}

func reviewSummary(s *progress.State) string {
	total := s.Counters["review_total"]
	done := s.Counters["review_done"]
	if total == 0 || done == 0 {
		return ""
	}
	cached := s.Counters["review_cached"]
	failed := s.Counters["review_failed"]
	fresh := max(done-cached-failed, 0)
	base := fmt.Sprintf("%d done · %d cached · %d failed", fresh, cached, failed)
	// If the routing breakdown was captured ("N AOIs → X individual +
	// Y grouped"), append it so users see what kind of calls ran
	// rather than just the run-tally.
	individual := s.Counters["review_individual"]
	grouped := s.Counters["review_grouped"]
	if individual == 0 && grouped == 0 {
		return base
	}
	return fmt.Sprintf("%s (%d individual + %d grouped)", base, individual, grouped)
}

func recheckSummary(s *progress.State) string {
	// Use the four counters captured from "Recheck complete: ..." event.
	// Only render once at least one is non-zero — otherwise the phase
	// just hadn't emitted a terminal summary yet.
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

// aoiCounter / reviewCounter expose the same counters that drive the
// progress bar so the TUI can render an inline "X/Y" alongside the
// phase label.
//
// AOI inline counter advances per batch (one tick per LLM call). The
// summary row uses aoi_total (file count from phase 1) for the
// "across N files" wording.
func aoiCounter(s *progress.State) (done, total int) {
	return s.Counters["aoi_batches_done"], s.Counters["aoi_batches_total"]
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

// aoiProgress reports the AOI pre-scan progress bar value. Advances
// per batch since that's the only mid-run granularity from the scanner.
func aoiProgress(s *progress.State) float64 {
	total := s.Counters["aoi_batches_total"]
	if total == 0 {
		return 0
	}
	return float64(s.Counters["aoi_batches_done"]) / float64(total)
}

// reviewProgress reports the deep-review progress bar value.
func reviewProgress(s *progress.State) float64 {
	total := s.Counters["review_total"]
	if total == 0 {
		return 0
	}
	return float64(s.Counters["review_done"]) / float64(total)
}

// synthesisProgress drives the synthesis row's progress bar from the
// streamed-token counters. The previous synthesisPulse oscillated
// between 0.05 and 0.95 to "show liveness" — the user saw the
// percentage going both up and down, which read as broken.
//
// Now: bar fills monotonically as content streams in from the LLM,
// capped at 95% so it never claims done before the response actually
// ends. When `complete` arrives the parser will have already pushed
// received past estimated, lifting the bar to 100%.
//
// Before the estimate is set (first emit before the LLM call), we
// return a tiny non-zero value so the bar shows SOMETHING — a hard
// zero would briefly look like the phase isn't running.
func synthesisProgress(s *progress.State) float64 {
	estimated := s.Counters["synthesis_chars_estimated"]
	received := s.Counters["synthesis_chars_received"]
	if estimated == 0 {
		// Just kicked off — show a slim bar so the row doesn't read
		// as "stuck at 0".
		return 0.03
	}
	pct := float64(received) / float64(estimated)
	if pct >= 1.0 {
		return 1.0
	}
	if pct > 0.95 {
		// Cap below 100% until the explicit "complete" arrives, so
		// the bar never claims done while the LLM is still streaming.
		return 0.95
	}
	return pct
}

// newSynthesisStreamCounter builds an onToken callback that counts
// non-control bytes received from the synthesis LLM and emits a
// throttled "synthesis received N" event so the parser can update
// the streaming progress counter. Tool / thought tokens (prefixed
// with \x00) are excluded — they're metadata, not output.
//
// emitEveryChars throttles the event rate (one emit per ~N chars)
// to avoid flooding the TUI with one event per token.
func newSynthesisStreamCounter(received, lastEmitAt *int, emitEveryChars int, emit func(phase, message string)) func(string) {
	return func(tok string) {
		if len(tok) > 0 && tok[0] == 0x00 {
			return
		}
		*received += len(tok)
		if *received-*lastEmitAt >= emitEveryChars {
			emit("phase4", fmt.Sprintf("synthesis received %d", *received))
			*lastEmitAt = *received
		}
	}
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
	// Per-batch lifecycle for the Batches panel. phase3 (Deep Review)
	// is the only phase that emits these; aggregate counters fall
	// through to the existing switch below.
	if phase == "phase3" && strings.HasPrefix(message, "Batch ") {
		progress.ParseBatchEvent(s, message)
	}
	switch {
	case phase == "phase1" && strings.Contains(message, "files to audit"):
		var n int
		if scanCounter(phase, message, "Phase 1 complete: %d files to audit", &n) {
			s.Counters["aoi_total"] = n
		}
	case phase == "phase1" && strings.HasPrefix(message, "Phase 1 skip breakdown:"):
		// Pipeline emits "Phase 1 skip breakdown: %d binary, %d large,
		// %d empty, %d symlink, %d missing, %d errored". Capture each
		// counter so fileCollectionSummary can render the skip total.
		var binary, large, empty, symlink, missing, errored int
		if scanCounter(phase, message,
			"Phase 1 skip breakdown: %d binary, %d large, %d empty, %d symlink, %d missing, %d errored",
			&binary, &large, &empty, &symlink, &missing, &errored) {
			s.Counters["file_skipped_binary"] = binary
			s.Counters["file_skipped_large"] = large
			s.Counters["file_skipped_empty"] = empty
			s.Counters["file_skipped_symlink"] = symlink
			s.Counters["file_skipped_missing"] = missing
			s.Counters["file_skipped_errored"] = errored
		}
	case phase == "phase2" && strings.Contains(message, "Scanning"):
		var n int
		if scanCounter(phase, message, "Scanning %d files", &n) {
			s.Counters["aoi_total"] = n
		}
	case phase == "phase2" && strings.HasPrefix(message, "AOI scan "):
		// Counter-only emit: "AOI scan %d/%d" where X/Y are BATCH counts
		// (from internal/security/scanner.go). Stored separately from
		// aoi_total — that one holds the file count from phase 1 ("Phase 1
		// complete: N files to audit"). Previously this branch overwrote
		// aoi_total mid-run, so the AOI summary "N AOIs across M files"
		// reported batches instead of files.
		var done, total int
		if scanCounter(phase, message, "AOI scan %d/%d", &done, &total) {
			s.Counters["aoi_batches_done"] = done
			s.Counters["aoi_batches_total"] = total
		}
	case phase == "phase3" && strings.Contains(message, "Executing"):
		var n int
		if scanCounter(phase, message, "Executing %d review calls...", &n) {
			s.Counters["review_total"] = n
		}
	case phase == "phase3" && strings.Contains(message, "AOIs →") && strings.Contains(message, "individual"):
		// routing.FormatSummary(): "N AOIs → X individual review(s) +
		// Y grouped review(s) across Z subcategorie(s) = T total call(s)"
		// Capture the X/Y so reviewSummary can show the routing split
		// alongside the run-tally.
		var aois, individual, grouped, subcats, totalCalls int
		if scanCounter(phase, message,
			"%d AOIs → %d individual review(s) + %d grouped review(s) across %d subcategorie(s) = %d total call(s)",
			&aois, &individual, &grouped, &subcats, &totalCalls) {
			s.Counters["review_individual"] = individual
			s.Counters["review_grouped"] = grouped
			// aoi_count is normally set by "found N areas of interest"
			// from the scanner; the routing line is a second source of
			// truth, useful as a fallback if the upstream emit drifts.
			if s.Counters["aoi_count"] == 0 {
				s.Counters["aoi_count"] = aois
			}
		}
	case phase == "phase3" && strings.HasPrefix(message, "Review "):
		// Counter-only emit: "Review %d/%d". The runPhase3 callback
		// now emits this and a separate status string per call so the
		// TUI detail line doesn't duplicate the counter. Cache and
		// failure sub-counters are incremented from the status emit
		// below.
		var done, total int
		if scanCounter(phase, message, "Review %d/%d", &done, &total) {
			s.Counters["review_done"] = done
			s.Counters["review_total"] = total
		}
	case phase == "phase3" && message == "complete (cached)":
		// Cache-hit status emit follows a "Review N/M" emit; tick the
		// cached sub-counter so the Summary can break down done vs
		// cached.
		s.Counters["review_cached"]++
	case phase == "phase3" && strings.HasPrefix(message, "failed:"):
		// Failure status emit — same shape, separate sub-counter.
		s.Counters["review_failed"]++
	case phase == "recheck" && strings.HasPrefix(message, "rechecked "):
		// Pipeline emits "rechecked X/Y findings" via RunRecheck's
		// OnProgress callback forwarding.
		var done, total int
		if scanCounter(phase, message, "rechecked %d/%d", &done, &total) {
			s.Counters["recheck_done"] = done
			s.Counters["recheck_total"] = total
		}
	case phase == "recheck" && strings.HasPrefix(message, "Recheck complete:"):
		// Pipeline emits "Recheck complete: kept X, dismissed Y, consolidated Z, modified W"
		// as the final terminal event. Capture the breakdown for the Summary row.
		var kept, dismissed, consolidated, modified int
		if scanCounter(phase, message,
			"Recheck complete: kept %d, dismissed %d, consolidated %d, modified %d",
			&kept, &dismissed, &consolidated, &modified) {
			s.Counters["recheck_kept"] = kept
			s.Counters["recheck_dismissed"] = dismissed
			s.Counters["recheck_consolidated"] = consolidated
			s.Counters["recheck_modified"] = modified
		}
	case phase == "phase4" && strings.HasPrefix(message, "synthesis estimate "):
		// Seeded once at synthesis start so synthesisProgress can compute
		// a ratio. Sent before the first token streams in.
		var n int
		if scanCounter(phase, message, "synthesis estimate %d", &n) {
			s.Counters["synthesis_chars_estimated"] = n
			// Reset received in case this is a re-run within the same
			// session (e.g. --no-cache replay).
			s.Counters["synthesis_chars_received"] = 0
		}
	case phase == "phase4" && strings.HasPrefix(message, "synthesis received "):
		// Throttled emit from the streaming onToken counter. The value
		// is cumulative bytes-of-content received so far.
		var n int
		if scanCounter(phase, message, "synthesis received %d", &n) {
			s.Counters["synthesis_chars_received"] = n
		}
	case phase == "phase1b" && strings.Contains(message, "classifying") && strings.Contains(message, "cached"):
		// classify package emits "classifying N file(s) (M cached)..." early in the phase.
		var uncached, cached int
		if scanCounter(phase, message, "classifying %d file(s) (%d cached)...", &uncached, &cached) {
			s.Counters["classify_uncached"] = uncached
			s.Counters["classify_cached"] = cached
			s.Counters["classify_total"] = uncached + cached
		}
	case phase == "phase2" && strings.HasPrefix(message, "using cached AOI results"):
		// security scanner emits "using cached AOI results for N file(s)".
		var n int
		if scanCounter(phase, message, "using cached AOI results for %d file(s)", &n) {
			s.Counters["aoi_cached"] = n
		}
	case phase == "phase2" && strings.HasPrefix(message, "found ") && strings.Contains(message, "areas of interest"):
		// security scanner emits "found N areas of interest" at end of phase.
		var n int
		if scanCounter(phase, message, "found %d areas of interest", &n) {
			s.Counters["aoi_count"] = n
		}
	}
}

// scanCounter wraps fmt.Sscanf with a logged warning on format mismatch.
func scanCounter(phase, message, format string, dest ...any) bool {
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
		Header:      header,
		Phases:      auditPhases(),
		BatchPhases: []string{"phase3"},
		ParseEvent:  parseAuditEvent,
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

				// Estimate output size from the finding count and seed
				// the progress bar counters BEFORE the LLM call starts.
				// Without the estimate, the first few hundred streamed
				// chars produce a wildly inaccurate percentage.
				estimated := EstimateSynthesisChars(len(r.Findings))
				emit("phase4", fmt.Sprintf("synthesis estimate %d", estimated))

				// Stream-counting onToken: every received content chunk
				// bumps a local counter and (throttled) emits a progress
				// event so the parser can update the bar. Tool/THOUGHT
				// tokens (prefixed with \x00) are excluded — they aren't
				// part of the output text the user reads.
				var received int
				var lastEmitAt int
				const emitEveryChars = 150 // ~1 emit per 30-50 tokens
				onToken := newSynthesisStreamCounter(&received, &lastEmitAt, emitEveryChars, emit)

				s, synthErr := SynthesizeCached(runCtx, reviewClient, r.Findings, r.CrossCuttingObservations, r.ProjectContext, len(r.FailedAOIIDs), onToken, opts.NoCache)
				if synthErr != nil {
					emit("phase4", "Synthesis failed: "+synthErr.Error())
					// Non-fatal — continue without synthesis
				} else {
					// Final emit lifts the bar to 100% (parser will
					// derive that from received >= estimated; emit a
					// big "received" to guarantee it).
					emit("phase4", fmt.Sprintf("synthesis received %d", received))
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

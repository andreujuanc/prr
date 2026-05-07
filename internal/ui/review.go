package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/project"
	"github.com/andreujuanc/prr/internal/review"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"

	tea "github.com/charmbracelet/bubbletea"
)

// ReviewReporter decouples review orchestration from the Bubble Tea event
// loop. Production code uses teaReporter (wraps *tea.Program); tests can
// supply a lightweight recording implementation.
type ReviewReporter interface {
	// AOIProgress reports status updates from the AOI pre-scan phase.
	AOIProgress(status string, done bool, aoiCount int)
	// InitBatches is called once at the start with the batch list.
	InitBatches(batches []AIReviewBatchInfo)
	// BatchProgress reports a status change for a single batch.
	BatchProgress(batch int, status AIReviewBatchStatus)
	// SynthesisStarted signals the transition to the synthesis phase.
	SynthesisStarted()
	// Token delivers a streaming token from the synthesis phase.
	Token(token string)
}

// teaReporter adapts ReviewReporter to *tea.Program.
type teaReporter struct{ p *tea.Program }

func (r teaReporter) AOIProgress(status string, done bool, aoiCount int) {
	r.p.Send(AIReviewAOIMsg{Status: status, Done: done, AOIs: aoiCount})
}
func (r teaReporter) InitBatches(batches []AIReviewBatchInfo) {
	r.p.Send(AIReviewInitMsg{Batches: batches})
}
func (r teaReporter) BatchProgress(batch int, status AIReviewBatchStatus) {
	r.p.Send(AIReviewProgressMsg{Batch: batch, Status: status})
}
func (r teaReporter) SynthesisStarted() {
	r.p.Send(AIReviewSynthesisMsg{})
}
func (r teaReporter) Token(token string) {
	r.p.Send(AIChatDeltaMsg{Token: token})
}

// offsetReporter wraps a ReviewReporter and adds an offset to batch indices.
// Used when fallback directory batches are displayed after AOI review calls.
type offsetReporter struct {
	rr     ReviewReporter
	offset int
}

func (o *offsetReporter) AOIProgress(status string, done bool, aoiCount int) {
	o.rr.AOIProgress(status, done, aoiCount)
}
func (o *offsetReporter) InitBatches(batches []AIReviewBatchInfo) {
	// Skip — batches were already initialized by the caller
}
func (o *offsetReporter) BatchProgress(batch int, status AIReviewBatchStatus) {
	o.rr.BatchProgress(batch+o.offset, status)
}
func (o *offsetReporter) SynthesisStarted() {
	o.rr.SynthesisStarted()
}
func (o *offsetReporter) Token(token string) {
	o.rr.Token(token)
}

// reviewBatch represents a group of related files to review together.
type reviewBatch struct {
	label string   // e.g. "internal/ui" or "root"
	files []string // file paths in this batch
	diffs string   // concatenated diffs for all files in this batch
}

// batchMaxChars is the approximate max diff size per batch.
// Sized to keep each AI call's context focused.
const batchMaxChars = 20000

// checkMark is the symbol shown next to completed batches in the AI panel.
const checkMark = "\u2713" // ✓

// buildReviewBatches groups changed files into batches by directory,
// respecting the size limit. Files in the same directory are grouped
// together when possible. Large files get their own batch.
func buildReviewBatches(rawDiffs map[string]string) []reviewBatch {
	// Group files by parent directory, skipping excluded files
	dirFiles := make(map[string][]string)
	for p := range rawDiffs {
		if config.ShouldExcludeFromReview(p) {
			continue
		}
		dir := filepath.Dir(p)
		if dir == "." {
			dir = "root"
		}
		dirFiles[dir] = append(dirFiles[dir], p)
	}

	// Sort directories for deterministic order
	dirs := make([]string, 0, len(dirFiles))
	for d := range dirFiles {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var batches []reviewBatch

	for _, dir := range dirs {
		files := dirFiles[dir]
		sort.Strings(files)

		var curFiles []string
		var curDiff strings.Builder

		for _, f := range files {
			diff := rawDiffs[f]
			entry := fmt.Sprintf("=== %s ===\n%s\n\n", f, diff)

			// If adding this file would exceed limit and we already have files, flush
			if curDiff.Len() > 0 && curDiff.Len()+len(entry) > batchMaxChars {
				batches = append(batches, reviewBatch{
					label: dir,
					files: curFiles,
					diffs: curDiff.String(),
				})
				curFiles = nil
				curDiff.Reset()
			}

			curDiff.WriteString(entry)
			curFiles = append(curFiles, f)
		}

		if len(curFiles) > 0 {
			batches = append(batches, reviewBatch{
				label: dir,
				files: curFiles,
				diffs: curDiff.String(),
			})
		}
	}

	return batches
}

// batchResult holds the outcome of a single batch review.
type batchResult struct {
	batch  reviewBatch
	result string
	cached bool
	err    error
}

// streamMultiPassReview runs a multi-pass PR review:
// Phase 0 (optional): AOI pre-scan — lightweight security triage with cheap model
// Phase 1: Review each batch of files independently (skipping batches where all files have cached findings)
//   - When parallelReviews > 1, batches are reviewed concurrently using a worker pool.
//     In parallel mode, batch tokens are not streamed (would be garbled); instead,
//     the UI shows a progress counter of completed/active/total batches.
//   - When parallelReviews == 1, batches are reviewed sequentially with token streaming.
//
// Phase 2: Synthesize all findings into a final review (always streamed)
//
// Progress and tokens are sent to the UI via program.Send().
// Returns an AIChatDoneMsg with Review data for persistence.
//
// If aoiClient is non-nil, the AOI pre-scan runs before Phase 1 and its
// security digest is injected into both batch and synthesis prompts.
// When aoiContextLines > 3, diffs are re-generated with more context for AOI.
func streamMultiPassReview(
	ctx context.Context,
	client ai.Client,
	aoiClient ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	reviewState *state.State,
	parallelReviews int,
	rr ReviewReporter,
	base, head string, // git refs for re-diffing with more context
	aoiContextLines int, // 0 or 3 = use rawDiffs as-is; >3 = re-diff for AOI
	repoRoot string, // repository root for project context discovery
) tea.Cmd {
	return func() tea.Msg {
		// ── Phase 0: Project context + AOI pre-scan (parallel) ────
		var securityDigest string
		var aoiScanResults []security.AOIScanResult // retained for AOI-driven routing

		// Run project context discovery and AOI scan concurrently
		type projectResult struct {
			context string
			err     error
		}
		projectCh := make(chan projectResult, 1)

		// Start project context discovery in background
		if repoRoot != "" {
			go func() {
				var cachedCtx, cachedHash string
				if reviewState != nil {
					cachedCtx, cachedHash = reviewState.GetProjectContext()
				}

				pctx, err := project.Discover(ctx, repoRoot, aoiClient, cachedHash, func(status string) {
					log.Printf("Project context: %s", status)
				})
				if err != nil {
					projectCh <- projectResult{err: err}
					return
				}
				if pctx.FromCache {
					projectCh <- projectResult{context: cachedCtx}
					return
				}
				// Cache the new result
				if reviewState != nil && pctx.Summary != "" {
					reviewState.SetProjectContext(pctx.Summary, pctx.InputHash)
				}
				projectCh <- projectResult{context: pctx.Summary}
			}()
		} else {
			projectCh <- projectResult{}
		}

		// AOI scan (runs concurrently with project context discovery)
		if aoiClient != nil {
			rr.AOIProgress("starting security pre-scan...", false, 0)

			// Use diffs with more context for AOI if configured.
			aoiDiffs := rawDiffs
			if aoiContextLines > 3 && base != "" && head != "" {
				rr.AOIProgress(fmt.Sprintf("re-diffing with %d context lines...", aoiContextLines), false, 0)
				aoiDiffs = make(map[string]string, len(rawDiffs))
				for filePath := range rawDiffs {
					d, err := git.GetRawDiffWithContext(base, head, filePath, aoiContextLines)
					if err != nil {
						log.Printf("AOI re-diff failed for %s (falling back to 3-line): %v", filePath, err)
						aoiDiffs[filePath] = rawDiffs[filePath]
					} else {
						aoiDiffs[filePath] = d
					}
				}
			}

			// Build AOI cache from state
			var aoiCache map[string]*security.AOIScanResult
			if reviewState != nil {
				aoiCache = make(map[string]*security.AOIScanResult)
				for filePath := range aoiDiffs {
					raw, cachedCtxLines := reviewState.GetAOIResults(filePath)
					if raw != nil && cachedCtxLines == aoiContextLines {
						var cached security.AOIScanResult
						if err := json.Unmarshal(raw, &cached); err == nil {
							aoiCache[filePath] = &cached
						}
					} else if raw != nil && cachedCtxLines != aoiContextLines {
						log.Printf("AOI cache miss for %s: context lines changed (%d -> %d)", filePath, cachedCtxLines, aoiContextLines)
					}
				}
			}

			aoiReport, err := security.ScanAreasOfInterest(ctx, aoiClient, aoiDiffs, aoiCache, func(status string) {
				rr.AOIProgress(status, false, 0)
			})
			if err != nil {
				log.Printf("AOI scan failed (non-fatal): %v", err)
				rr.AOIProgress("security pre-scan failed (continuing without)", true, 0)
			} else if aoiReport != nil {
				// Save new AOI results back to state for caching
				if reviewState != nil {
					for _, fileResult := range aoiReport.Files {
						filePath := fileResult.File
						if !reviewState.HasFile(filePath) {
							cleaned := filepath.Clean(filePath)
							if cleaned != filePath && reviewState.HasFile(cleaned) {
								log.Printf("AOI path normalized: %q -> %q", filePath, cleaned)
								filePath = cleaned
							} else {
								log.Printf("Warning: AOI returned file %q not found in state (skipping cache)", filePath)
								continue
							}
						}
						if data, err := json.Marshal(fileResult); err == nil {
							reviewState.SetAOIResults(filePath, data, aoiContextLines)
						} else {
							log.Printf("Warning: failed to marshal AOI result for %s: %v", filePath, err)
						}
					}
					if err := state.Save(reviewState); err != nil {
						log.Printf("Warning: failed to persist AOI results: %v", err)
					}
				}

				if aoiReport.TotalAOIs > 0 {
					securityDigest = aoiReport.SecurityDigest
					aoiScanResults = aoiReport.Files
					rr.AOIProgress(
						fmt.Sprintf("found %d areas of interest", aoiReport.TotalAOIs),
						true, aoiReport.TotalAOIs,
					)
				} else {
					rr.AOIProgress("no security areas of interest found", true, 0)
				}
			} else {
				rr.AOIProgress("no security areas of interest found", true, 0)
			}
		}

		// Wait for project context discovery to complete
		projResult := <-projectCh
		if projResult.err != nil {
			log.Printf("Project context discovery failed (non-fatal): %v", projResult.err)
		}

		projectContext := projResult.context

		// Build enhanced instructions: project context → security digest → user instructions
		enhancedInstructions := ""
		if projectContext != "" {
			enhancedInstructions = projectContext + "\n\n"
		}
		enhancedInstructions += customInstructions
		if securityDigest != "" {
			enhancedInstructions += "\n\n" + securityDigest
		}
		enhancedInstructions = strings.TrimSpace(enhancedInstructions)

		// ── Phase 1: AOI-driven review + fallback batches ─────────

		// Route AOIs into review calls (individual + grouped)
		routeResult := review.RouteAOIs(aoiScanResults, nil, 10)

		// Identify files covered by AOI review calls
		aoiCoveredFiles := make(map[string]bool)
		var reviewCalls []review.ReviewCall
		if routeResult != nil && routeResult.TotalAOIs > 0 {
			reviewCalls = routeResult.PrioritizedCalls(0)
			for _, call := range reviewCalls {
				for _, f := range call.Files {
					aoiCoveredFiles[f] = true
				}
			}
			log.Printf("AOI routing: %s", routeResult.FormatSummary())
		}

		// Build fallback batches for files WITHOUT AOIs
		fallbackDiffs := make(map[string]string)
		for fp, diff := range rawDiffs {
			if !aoiCoveredFiles[fp] && !config.ShouldExcludeFromReview(fp) {
				fallbackDiffs[fp] = diff
			}
		}
		fallbackBatches := buildReviewBatches(fallbackDiffs)

		// Total "batches" = AOI review calls + fallback directory batches
		totalCalls := len(reviewCalls) + len(fallbackBatches)
		if totalCalls == 0 {
			return AIChatDoneMsg{Err: fmt.Errorf("no files to review")}
		}

		// Initialize the batch list in the UI — AOI calls first, then fallback batches
		batchInfos := make([]AIReviewBatchInfo, 0, totalCalls)
		for _, call := range reviewCalls {
			label := call.Category
			if call.Subcategory != "" {
				label += "/" + call.Subcategory
			}
			if call.Type == "individual" {
				label += " [critical]"
			}
			batchInfos = append(batchInfos, AIReviewBatchInfo{
				Label:    label,
				NumFiles: len(call.Files),
			})
		}
		for _, b := range fallbackBatches {
			batchInfos = append(batchInfos, AIReviewBatchInfo{
				Label:    b.label,
				NumFiles: len(b.files),
			})
		}
		rr.InitBatches(batchInfos)

		// ── Phase 1a: Run AOI-driven review calls concurrently ────
		var allFindings strings.Builder
		allFileFindings := make(map[string]string)
		var deepFindings []state.DeepFinding

		if len(reviewCalls) > 0 {
			maxConc := parallelReviews
			if maxConc <= 0 {
				maxConc = 5
			}

			execResult, execErr := review.RunReviewCalls(ctx, client, reviewCalls, review.ExecuteOptions{
				Mode:               review.ModePR,
				ProjectContext:     projectContext,
				CustomInstructions: enhancedInstructions,
				MaxConcurrency:     maxConc,
				OnProgress: func(completed, total int, cached bool, callErr error) {
					// Map progress back to batch indices
					idx := completed - 1
					if idx < 0 || idx >= len(reviewCalls) {
						return
					}
					if callErr != nil {
						rr.BatchProgress(idx, BatchFailed)
					} else {
						rr.BatchProgress(idx, BatchDone)
					}
				},
			})
			if execErr != nil {
				return AIChatDoneMsg{Err: fmt.Errorf("AOI review: %w", execErr)}
			}

			deepFindings = execResult.Findings

			// Build synthesis input from structured findings
			for _, f := range execResult.Findings {
				allFindings.WriteString(fmt.Sprintf("### %s: %s\n", f.Severity, f.Title))
				allFindings.WriteString(fmt.Sprintf("**File:** %s:%s\n", f.File, f.Lines))
				allFindings.WriteString(fmt.Sprintf("**Category:** %s/%s\n", f.Category, f.Subcategory))
				allFindings.WriteString(fmt.Sprintf("**Description:** %s\n", f.Description))
				if f.Trigger != "" {
					allFindings.WriteString(fmt.Sprintf("**Trigger:** %s\n", f.Trigger))
				}
				if f.Suggestion != "" {
					allFindings.WriteString(fmt.Sprintf("**Suggestion:** %s\n", f.Suggestion))
				}
				allFindings.WriteString("\n---\n\n")

				// Index by file for per-file findings
				entry := fmt.Sprintf("[%s] %s: %s", f.Severity, f.Title, f.Description)
				if existing, ok := allFileFindings[f.File]; ok {
					allFileFindings[f.File] = existing + "\n\n" + entry
				} else {
					allFileFindings[f.File] = entry
				}
			}

			// Mark all AOI call batches as done (for any that didn't get progress)
			for i := range reviewCalls {
				rr.BatchProgress(i, BatchDone)
			}
		}

		aoiCallOffset := len(reviewCalls)

		// ── Phase 1b: Run fallback directory batches ──────────────
		if len(fallbackBatches) > 0 {
			// Adjust batch indices to account for AOI calls
			fbReporter := &offsetReporter{rr: rr, offset: aoiCallOffset}

			// Run batches without synthesis — synthesis happens once combining all findings
			fbResult := runBatchesOnly(ctx, client, prMeta, fallbackDiffs, enhancedInstructions, reviewState, fallbackBatches, fbReporter, false)

			// Extract findings from fallback result
			if doneMsg, ok := fbResult.(AIChatDoneMsg); ok {
				if doneMsg.Err != nil {
					return doneMsg
				}
				// Merge fallback file findings
				for f, findings := range doneMsg.FileFindings {
					allFileFindings[f] = findings
				}
				// Append fallback review findings
				if doneMsg.Review != nil {
					allFindings.WriteString(doneMsg.Review.Findings)
				}
			}
		}

		// ── Phase 1c: Recheck — deduplicate and filter findings ──
		if len(deepFindings) > 0 {
			log.Printf("Recheck: %d findings to validate", len(deepFindings))
			recheckResult, recheckErr := review.RecheckFindings(ctx, client, deepFindings, review.RecheckOptions{
				Mode:           review.ModePR,
				ProjectContext: projectContext,
			})
			if recheckErr != nil {
				log.Printf("Recheck failed (non-fatal): %v — keeping all findings", recheckErr)
			} else {
				log.Printf("Recheck: kept %d, dismissed %d, consolidated %d, modified %d",
					len(recheckResult.Findings), recheckResult.DismissedCount,
					recheckResult.ConsolidatedCount, recheckResult.ModifiedCount)
				deepFindings = recheckResult.Findings

				// Rebuild synthesis input from rechecked findings
				allFindings.Reset()
				for _, f := range deepFindings {
					allFindings.WriteString(fmt.Sprintf("### %s: %s\n", f.Severity, f.Title))
					allFindings.WriteString(fmt.Sprintf("**File:** %s:%s\n", f.File, f.Lines))
					allFindings.WriteString(fmt.Sprintf("**Category:** %s/%s\n", f.Category, f.Subcategory))
					allFindings.WriteString(fmt.Sprintf("**Description:** %s\n", f.Description))
					if f.Trigger != "" {
						allFindings.WriteString(fmt.Sprintf("**Trigger:** %s\n", f.Trigger))
					}
					if f.Suggestion != "" {
						allFindings.WriteString(fmt.Sprintf("**Suggestion:** %s\n", f.Suggestion))
					}
					allFindings.WriteString("\n---\n\n")
				}
			}
		}

		// ── Phase 2: Synthesis ────────────────────────────────────
		result := runSynthesis(ctx, client, prMeta, rawDiffs, enhancedInstructions, allFindings.String(), allFileFindings, nil, rr)
		if doneMsg, ok := result.(AIChatDoneMsg); ok {
			doneMsg.DeepFindings = deepFindings
			return doneMsg
		}
		return result
	}
}

// isBatchCached checks if all files in a batch have cached findings.
func isBatchCached(batch reviewBatch, reviewState *state.State) bool {
	if reviewState == nil {
		return false
	}
	if !reviewState.HasCachedBatch(batch.files) {
		for _, f := range batch.files {
			purpose, _ := reviewState.GetBatchFindings(f)
			if purpose == "" {
				log.Printf("Cache miss: file %q (purpose empty)", f)
				break
			}
		}
		return false
	}
	return true
}

// collectCachedFindings reassembles per-file findings from cache for synthesis input.
// Only includes files that have findings (skips clean files).
func collectCachedFindings(batch reviewBatch, reviewState *state.State) (string, map[string]string) {
	return reviewState.CollectCachedFindings(batch.files)
}

// buildBatchSystemPrompt constructs the system prompt for a batch review.
func buildBatchSystemPrompt(prMeta, customInstructions string) string {
	systemPrompt := ai.ReviewBatchPrompt + "\n\n## PR Context\n" + prMeta
	if customInstructions != "" {
		systemPrompt += "\n\n## Project-Specific Instructions\n\n" + customInstructions
	}
	return systemPrompt
}

// maxDiffLines is the approximate max number of diff lines included inline in
// the user message. Beyond this the model is instructed to use git_diff to
// paginate. Keeping the inline diff bounded stabilises the cacheable prefix
// and avoids blowing context on very large batches.
const maxDiffLines = 4000

// capDiff truncates a diff to maxDiffLines and appends a tool-use hint.
// Returns the original string unchanged if it's within the limit.
func capDiff(diff string, files []string) string {
	lines := strings.Split(diff, "\n")
	if len(lines) <= maxDiffLines {
		return diff
	}
	capped := strings.Join(lines[:maxDiffLines], "\n")
	pathList := strings.Join(files, " ")
	return capped + fmt.Sprintf(
		"\n\n... (diff truncated at %d lines — %d more lines omitted)"+
			"\nUse git_diff with paths=\"%s\" to read the remaining context.",
		maxDiffLines, len(lines)-maxDiffLines, pathList)
}

// buildBatchMessages constructs the user message for a batch review.
func buildBatchMessages(batch reviewBatch) []ai.Message {
	return []ai.Message{
		{Role: "user", Content: fmt.Sprintf(
			"Review these %d file(s): %s\n\n%s",
			len(batch.files),
			strings.Join(batch.files, ", "),
			capDiff(batch.diffs, batch.files),
		)},
	}
}

// batchFileReview is the structured output from reviewing a single file in a batch.
type batchFileReview struct {
	File     string `json:"file"`
	Purpose  string `json:"purpose"`
	Findings string `json:"findings"`
}

// parseBatchResult parses the JSON array from a batch review response.
// Handles markdown code fences (```json ... ```) that AI models commonly wrap around JSON.
// Returns nil if parsing fails.
func parseBatchResult(raw string) []batchFileReview {
	s := strings.TrimSpace(raw)

	// Strip markdown code fences
	if strings.HasPrefix(s, "```") {
		// Remove opening fence (```json or ```)
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		// Remove closing fence
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	var results []batchFileReview
	if err := json.Unmarshal([]byte(s), &results); err != nil {
		log.Printf("Warning: failed to parse batch JSON: %v", err)
		return nil
	}
	return results
}

const maxRetries = 3

// reviewBatchWithRetry calls ChatStream for a batch and retries up to maxRetries
// times if the result is empty or unparseable as structured JSON.
// onToken is called for each streamed token (can be nil to discard).
func reviewBatchWithRetry(
	ctx context.Context,
	client ai.Client,
	systemPrompt string,
	batch reviewBatch,
	onToken func(string),
) (string, error) {
	if onToken == nil {
		onToken = func(string) {}
	}

	var lastResult string
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		result, err := client.ChatStream(ctx, systemPrompt, buildBatchMessages(batch), onToken)
		if err != nil {
			return "", err
		}

		lastResult = result
		trimmed := strings.TrimSpace(result)

		// Success: non-empty and parseable as structured JSON
		if trimmed != "" && parseBatchResult(trimmed) != nil {
			return result, nil
		}

		if attempt < maxRetries {
			reason := "empty response"
			if trimmed != "" {
				reason = "unparseable response"
			}
			log.Printf("Batch %q attempt %d/%d: %s, retrying...", batch.label, attempt, maxRetries, reason)
		}
	}

	// Exhausted retries — return last result (persistBatchFindings handles fallback)
	log.Printf("Batch %q: exhausted %d retries, using last result as fallback", batch.label, maxRetries)
	return lastResult, nil
}

// synthesisWithRetry calls ChatStream for synthesis and retries up to maxRetries
// times if the result is empty.
func synthesisWithRetry(
	ctx context.Context,
	client ai.Client,
	systemPrompt string,
	messages []ai.Message,
	onToken func(string),
) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		result, err := client.ChatStream(ctx, systemPrompt, messages, onToken)
		if err != nil {
			return "", err
		}

		summary := strings.TrimSpace(result)
		if summary != "" {
			return summary, nil
		}

		lastErr = fmt.Errorf("synthesis returned empty response")
		if attempt < maxRetries {
			log.Printf("Synthesis attempt %d/%d: empty response, retrying...", attempt, maxRetries)
		}
	}

	return "", lastErr
}

// persistBatchFindings parses a batch result and saves per-file purpose+findings
// to state immediately, so they survive even if a later batch fails.
// Returns the parsed reviews for use in building synthesis input,
// and a map of file->findings for the FileFindings output.
func persistBatchFindings(reviewState *state.State, batch reviewBatch, rawResult string) ([]batchFileReview, map[string]string) {
	parsed := parseBatchResult(rawResult)
	fileFindings := make(map[string]string)

	if parsed == nil {
		// Fallback: couldn't parse JSON — store raw result on all files in batch
		log.Printf("Warning: batch %q returned unparseable result, using raw fallback", batch.label)
		if reviewState != nil {
			for _, f := range batch.files {
				reviewState.SetBatchFindings(f, "unknown (parse failed)", rawResult)
			}
			// Only include in fileFindings once (keyed to first file)
			if len(batch.files) > 0 {
				fileFindings[batch.files[0]] = rawResult
			}
		}
	} else {
		// Build a set of batch files for validation
		batchFiles := make(map[string]bool, len(batch.files))
		for _, f := range batch.files {
			batchFiles[f] = true
		}

		matchedFiles := make(map[string]bool)
		for _, entry := range parsed {
			if !batchFiles[entry.File] {
				log.Printf("Warning: AI returned file %q not in batch %v", entry.File, batch.files)
				continue // ignore files not in this batch
			}
			matchedFiles[entry.File] = true
			if reviewState != nil {
				purpose := entry.Purpose
				if purpose == "" {
					purpose = "reviewed"
				}
				reviewState.SetBatchFindings(entry.File, purpose, entry.Findings)
			}
			if entry.Findings != "" {
				fileFindings[entry.File] = entry.Findings
			}
		}

		if len(matchedFiles) < len(batch.files) {
			log.Printf("Warning: batch %q has %d files but AI only returned %d matching entries",
				batch.label, len(batch.files), len(matchedFiles))
		}

		// Mark files that weren't in the parsed output (AI omitted them)
		if reviewState != nil {
			for _, f := range batch.files {
				purpose, _ := reviewState.GetBatchFindings(f)
				if purpose == "" {
					reviewState.SetBatchFindings(f, "reviewed (no details)", "")
				}
			}
		}
	}

	if reviewState != nil {
		if err := state.Save(reviewState); err != nil {
			log.Printf("Warning: failed to persist batch findings: %v", err)
		}
	}

	return parsed, fileFindings
}

// reviewBatchesSequential reviews batches one at a time with token streaming.
// This is the original behavior when parallelReviews == 1.
func reviewBatchesSequential(
	ctx context.Context,
	client ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	reviewState *state.State,
	batches []reviewBatch,
	rr ReviewReporter,
) tea.Msg {
	return runBatchesOnly(ctx, client, prMeta, rawDiffs, customInstructions, reviewState, batches, rr, true)
}

// runBatchesOnly reviews batches one at a time with token streaming.
// If synthesize is true, runs synthesis after all batches; otherwise returns findings only.
func runBatchesOnly(
	ctx context.Context,
	client ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	reviewState *state.State,
	batches []reviewBatch,
	rr ReviewReporter,
	synthesize bool,
) tea.Msg {
	var allFindings strings.Builder
	allFileFindings := make(map[string]string)
	allBatchesCached := true

	for i, batch := range batches {
		if ctx.Err() != nil {
			return AIChatDoneMsg{Err: ctx.Err()}
		}

		if isBatchCached(batch, reviewState) {
			rr.BatchProgress(i, BatchCached)

			cached, cachedFF := collectCachedFindings(batch, reviewState)
			allFindings.WriteString(fmt.Sprintf("### Batch %d: %s\n", i+1, batch.label))
			allFindings.WriteString(fmt.Sprintf("Files: %s\n\n", strings.Join(batch.files, ", ")))
			allFindings.WriteString(cached)
			allFindings.WriteString("\n\n---\n\n")
			for f, findings := range cachedFF {
				allFileFindings[f] = findings
			}
			continue
		}

		allBatchesCached = false
		rr.BatchProgress(i, BatchActive)

		result, err := reviewBatchWithRetry(ctx, client, buildBatchSystemPrompt(prMeta, customInstructions), batch, nil)
		if err != nil {
			rr.BatchProgress(i, BatchFailed)
			return AIChatDoneMsg{Err: fmt.Errorf("batch %d/%d (%s): %w", i+1, len(batches), batch.label, err)}
		}

		rr.BatchProgress(i, BatchDone)

		parsed, batchFF := persistBatchFindings(reviewState, batch, result)
		for f, findings := range batchFF {
			allFileFindings[f] = findings
		}

		// Build synthesis input from parsed results or raw fallback
		allFindings.WriteString(fmt.Sprintf("### Batch %d: %s\n", i+1, batch.label))
		allFindings.WriteString(fmt.Sprintf("Files: %s\n\n", strings.Join(batch.files, ", ")))
		if parsed != nil {
			for _, entry := range parsed {
				if entry.Findings != "" {
					allFindings.WriteString(fmt.Sprintf("#### %s\nPurpose: %s\n%s\n\n", entry.File, entry.Purpose, entry.Findings))
				}
			}
		} else {
			allFindings.WriteString(result)
		}
		allFindings.WriteString("\n\n---\n\n")
	}

	// If ALL batches used cached findings and we have a non-stale review, skip synthesis
	if allBatchesCached && reviewState != nil && reviewState.Review != nil && !reviewState.IsReviewStale() {
		log.Printf("All %d batches cached and review is current — skipping synthesis (sequential)", len(batches))
		return AIChatDoneMsg{
			Review:       reviewState.Review,
			FileFindings: allFileFindings,
		}
	}

	if !synthesize {
		return AIChatDoneMsg{
			Review: &state.AIReview{
				Findings: allFindings.String(),
			},
			FileFindings: allFileFindings,
		}
	}

	return runSynthesis(ctx, client, prMeta, rawDiffs, customInstructions, allFindings.String(), allFileFindings, batches, rr)
}

// reviewBatchesParallel reviews batches concurrently using a worker pool.
func reviewBatchesParallel(
	ctx context.Context,
	client ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	reviewState *state.State,
	batches []reviewBatch,
	maxWorkers int,
	rr ReviewReporter,
) tea.Msg {
	results := make([]batchResult, len(batches))
	allFileFindings := make(map[string]string)

	// Separate cached vs uncached batches
	var uncachedIndices []int

	for i, batch := range batches {
		if isBatchCached(batch, reviewState) {
			cached, cachedFF := collectCachedFindings(batch, reviewState)
			results[i] = batchResult{
				batch:  batch,
				result: cached,
				cached: true,
			}

			rr.BatchProgress(i, BatchCached)
			for f, findings := range cachedFF {
				allFileFindings[f] = findings
			}
		} else {
			uncachedIndices = append(uncachedIndices, i)
		}
	}

	if len(uncachedIndices) > 0 {
		// Worker pool for uncached batches
		work := make(chan int, len(uncachedIndices))
		var wg sync.WaitGroup

		workers := maxWorkers
		if workers > len(uncachedIndices) {
			workers = len(uncachedIndices)
		}

		systemPrompt := buildBatchSystemPrompt(prMeta, customInstructions)

		// Start workers
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range work {
					if ctx.Err() != nil {
						results[idx] = batchResult{batch: batches[idx], err: ctx.Err()}
						rr.BatchProgress(idx, BatchFailed)
						continue
					}

					batch := batches[idx]
					rr.BatchProgress(idx, BatchActive)

					result, err := reviewBatchWithRetry(ctx, client, systemPrompt, batch, nil)

					results[idx] = batchResult{
						batch:  batch,
						result: result,
						err:    err,
					}

					if err == nil {
						rr.BatchProgress(idx, BatchDone)
					} else {
						rr.BatchProgress(idx, BatchFailed)
					}
				}
			}()
		}

		// Feed work
		for _, idx := range uncachedIndices {
			work <- idx
		}
		close(work)

		wg.Wait()
	}

	// Assemble findings in order (all goroutines are done, no concurrency here)
	var allFindings strings.Builder
	for i, res := range results {
		if res.err != nil {
			return AIChatDoneMsg{Err: fmt.Errorf("batch %d/%d (%s): %w", i+1, len(batches), res.batch.label, res.err)}
		}

		allFindings.WriteString(fmt.Sprintf("### Batch %d: %s\n", i+1, res.batch.label))
		allFindings.WriteString(fmt.Sprintf("Files: %s\n\n", strings.Join(res.batch.files, ", ")))

		if !res.cached {
			parsed, batchFF := persistBatchFindings(reviewState, res.batch, res.result)
			for f, findings := range batchFF {
				allFileFindings[f] = findings
			}
			if parsed != nil {
				for _, entry := range parsed {
					if entry.Findings != "" {
						allFindings.WriteString(fmt.Sprintf("#### %s\nPurpose: %s\n%s\n\n", entry.File, entry.Purpose, entry.Findings))
					}
				}
			} else {
				allFindings.WriteString(res.result)
			}
		} else {
			allFindings.WriteString(res.result)
		}
		allFindings.WriteString("\n\n---\n\n")
	}

	// If ALL batches used cached findings and we have a non-stale review, skip synthesis
	if len(uncachedIndices) == 0 && reviewState != nil && reviewState.Review != nil && !reviewState.IsReviewStale() {
		log.Printf("All %d batches cached and review is current — skipping synthesis (parallel)", len(batches))
		return AIChatDoneMsg{
			Review:       reviewState.Review,
			FileFindings: allFileFindings,
		}
	}

	return runSynthesis(ctx, client, prMeta, rawDiffs, customInstructions, allFindings.String(), allFileFindings, batches, rr)
}

// runSynthesis runs Phase 2 of the multi-pass review: synthesize all findings.
// This is shared between sequential and parallel modes.
// The synthesis prompt now requests structured JSON output (ReviewOutput).
// If parsing fails, it retries once with an error correction prompt.
// Falls back to raw text if structured parsing fails entirely.
func runSynthesis(
	ctx context.Context,
	client ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	allFindings string,
	fileFindings map[string]string,
	batches []reviewBatch,
	rr ReviewReporter,
) tea.Msg {
	if ctx.Err() != nil {
		return AIChatDoneMsg{Err: ctx.Err()}
	}

	rr.SynthesisStarted()

	// Build file listing for synthesis context
	var fileListing strings.Builder
	paths := make([]string, 0, len(rawDiffs))
	for fp := range rawDiffs {
		paths = append(paths, fp)
	}
	sort.Strings(paths)
	fileListing.WriteString(fmt.Sprintf("Files changed (%d):\n", len(paths)))
	for _, fp := range paths {
		diff := rawDiffs[fp]
		added, removed := countDiffStats(diff)
		fileListing.WriteString(fmt.Sprintf("  %-50s +%-4d -%d\n", fp, added, removed))
	}

	synthesisSystem := ai.ReviewSynthesisPrompt + "\n\n" +
		"## PR Metadata\n" + prMeta + "\n" +
		"## Changed Files\n" + fileListing.String() + "\n" +
		"## Per-batch Findings\n\n" + allFindings
	if customInstructions != "" {
		synthesisSystem += "\n\n## Project-Specific Instructions\n\n" + customInstructions
	}

	synthesisMessages := []ai.Message{
		{Role: "user", Content: "Synthesize the per-file findings into a final PR review. Use tools to verify any findings you are uncertain about. Return ONLY the JSON review object."},
	}

	summary, err := synthesisWithRetry(ctx, client, synthesisSystem, synthesisMessages, func(token string) {
		rr.Token(token)
	})
	if err != nil {
		return AIChatDoneMsg{Err: fmt.Errorf("synthesis: %w", err)}
	}

	// Try to parse as structured ReviewOutput
	structured := ai.ParseReviewOutput(summary)

	if structured == nil {
		// Retry once with error correction prompt
		log.Printf("Synthesis: initial JSON parse failed, retrying with correction prompt")
		correctionMessages := []ai.Message{
			{Role: "user", Content: "Synthesize the per-file findings into a final PR review. Return ONLY the JSON review object."},
			{Role: "assistant", Content: summary},
			{Role: "user", Content: "Your response was not valid JSON. Please return ONLY a valid JSON object matching the schema specified in the system prompt. No markdown, no prose — just the raw JSON object starting with { and ending with }."},
		}

		corrected, corrErr := synthesisWithRetry(ctx, client, synthesisSystem, correctionMessages, func(token string) {
			rr.Token(token)
		})
		if corrErr == nil {
			structured = ai.ParseReviewOutput(corrected)
			if structured != nil {
				summary = corrected
			}
		}
	}

	result := AIChatDoneMsg{
		FullResponse: summary,
		Review: &state.AIReview{
			Summary:  summary,
			Findings: allFindings,
		},
		StructuredReview: structured,
		FileFindings:     fileFindings,
	}

	return result
}

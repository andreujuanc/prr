package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"prr/internal/ai"
	"prr/internal/config"
	"prr/internal/state"

	tea "github.com/charmbracelet/bubbletea"
)

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
// Phase 1: Review each batch of files independently (skipping batches where all files have cached findings)
//   - When parallelReviews > 1, batches are reviewed concurrently using a worker pool.
//     In parallel mode, batch tokens are not streamed (would be garbled); instead,
//     the UI shows a progress counter of completed/active/total batches.
//   - When parallelReviews == 1, batches are reviewed sequentially with token streaming.
// Phase 2: Synthesize all findings into a final review (always streamed)
//
// Progress and tokens are sent to the UI via program.Send().
// Returns an AIChatDoneMsg with Review data for persistence.
func streamMultiPassReview(
	ctx context.Context,
	client ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	reviewState *state.State,
	parallelReviews int,
	p *tea.Program,
) tea.Cmd {
	return func() tea.Msg {
		batches := buildReviewBatches(rawDiffs)
		if len(batches) == 0 {
			return AIChatDoneMsg{Err: fmt.Errorf("no files to review")}
		}

		// Initialize the batch list in the UI
		batchInfos := make([]AIReviewBatchInfo, len(batches))
		for i, b := range batches {
			batchInfos[i] = AIReviewBatchInfo{Label: b.label, NumFiles: len(b.files)}
		}
		p.Send(AIReviewInitMsg{Batches: batchInfos})

		if parallelReviews <= 1 {
			return reviewBatchesSequential(ctx, client, prMeta, rawDiffs, customInstructions, reviewState, batches, p)
		}
		return reviewBatchesParallel(ctx, client, prMeta, rawDiffs, customInstructions, reviewState, batches, parallelReviews, p)
	}
}

// isBatchCached checks if all files in a batch have cached findings.
func isBatchCached(batch reviewBatch, reviewState *state.State) bool {
	if reviewState == nil {
		return false
	}
	for _, f := range batch.files {
		fs, ok := reviewState.Files[f]
		if !ok || fs.BatchFindings == "" {
			return false
		}
	}
	return true
}

// collectCachedFindings gathers cached findings for a batch from state.
func collectCachedFindings(batch reviewBatch, reviewState *state.State) string {
	var sb strings.Builder
	for _, f := range batch.files {
		sb.WriteString(reviewState.Files[f].BatchFindings)
	}
	return sb.String()
}

// buildBatchSystemPrompt constructs the system prompt for a batch review.
func buildBatchSystemPrompt(prMeta, customInstructions string) string {
	systemPrompt := ai.ReviewBatchPrompt + "\n\n## PR Context\n" + prMeta
	if customInstructions != "" {
		systemPrompt += "\n\n## Project-Specific Instructions\n\n" + customInstructions
	}
	return systemPrompt
}

// buildBatchMessages constructs the user message for a batch review.
func buildBatchMessages(batch reviewBatch) []ai.Message {
	return []ai.Message{
		{Role: "user", Content: fmt.Sprintf(
			"Review these %d file(s): %s\n\n%s",
			len(batch.files),
			strings.Join(batch.files, ", "),
			batch.diffs,
		)},
	}
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
	p *tea.Program,
) tea.Msg {
	var allFindings strings.Builder
	fileFindings := make(map[string]string)

	for i, batch := range batches {
		if ctx.Err() != nil {
			return AIChatDoneMsg{Err: ctx.Err()}
		}

		if isBatchCached(batch, reviewState) {
			p.Send(AIReviewProgressMsg{Batch: i, Status: BatchCached})

			cached := collectCachedFindings(batch, reviewState)
			allFindings.WriteString(fmt.Sprintf("### Batch %d: %s\n", i+1, batch.label))
			allFindings.WriteString(fmt.Sprintf("Files: %s\n\n", strings.Join(batch.files, ", ")))
			allFindings.WriteString(cached)
			allFindings.WriteString("\n\n---\n\n")
			for _, f := range batch.files {
				fileFindings[f] = reviewState.Files[f].BatchFindings
			}
			continue
		}

		p.Send(AIReviewProgressMsg{Batch: i, Status: BatchActive})

		// Stream the batch review silently — don't show intermediate findings
		result, err := client.ChatStream(ctx, buildBatchSystemPrompt(prMeta, customInstructions), buildBatchMessages(batch), func(token string) {
			// Suppress batch tokens from the panel — the user sees the final synthesis
		})
		if err != nil {
			p.Send(AIReviewProgressMsg{Batch: i, Status: BatchFailed})
			return AIChatDoneMsg{Err: fmt.Errorf("batch %d/%d (%s): %w", i+1, len(batches), batch.label, err)}
		}

		p.Send(AIReviewProgressMsg{Batch: i, Status: BatchDone})

		for _, f := range batch.files {
			fileFindings[f] = result
		}

		allFindings.WriteString(fmt.Sprintf("### Batch %d: %s\n", i+1, batch.label))
		allFindings.WriteString(fmt.Sprintf("Files: %s\n\n", strings.Join(batch.files, ", ")))
		allFindings.WriteString(result)
		allFindings.WriteString("\n\n---\n\n")
	}

	return runSynthesis(ctx, client, prMeta, rawDiffs, customInstructions, allFindings.String(), fileFindings, batches, p)
}

// reviewBatchesParallel reviews batches concurrently using a worker pool.
// Tokens are NOT streamed (would be garbled from multiple workers).
// Instead, progress is reported as completed/active/total counts.
func reviewBatchesParallel(
	ctx context.Context,
	client ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	reviewState *state.State,
	batches []reviewBatch,
	maxWorkers int,
	p *tea.Program,
) tea.Msg {
	results := make([]batchResult, len(batches))
	fileFindings := make(map[string]string)

	// Separate cached vs uncached batches
	var uncachedIndices []int

	for i, batch := range batches {
		if isBatchCached(batch, reviewState) {
			cached := collectCachedFindings(batch, reviewState)
			results[i] = batchResult{
				batch:  batch,
				result: cached,
				cached: true,
			}

			p.Send(AIReviewProgressMsg{Batch: i, Status: BatchCached})
			for _, f := range batch.files {
				fileFindings[f] = reviewState.Files[f].BatchFindings
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
						p.Send(AIReviewProgressMsg{Batch: idx, Status: BatchFailed})
						continue
					}

					batch := batches[idx]
					p.Send(AIReviewProgressMsg{Batch: idx, Status: BatchActive})

					// No token streaming in parallel mode — collect result silently
					result, err := client.ChatStream(ctx, systemPrompt, buildBatchMessages(batch), func(token string) {
						// Discard tokens in parallel mode — can't interleave output
					})

					results[idx] = batchResult{
						batch:  batch,
						result: result,
						err:    err,
					}

					if err == nil {
						p.Send(AIReviewProgressMsg{Batch: idx, Status: BatchDone})
					} else {
						p.Send(AIReviewProgressMsg{Batch: idx, Status: BatchFailed})
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
		allFindings.WriteString(res.result)
		allFindings.WriteString("\n\n---\n\n")

		if !res.cached {
			for _, f := range res.batch.files {
				fileFindings[f] = res.result
			}
		}
	}

	return runSynthesis(ctx, client, prMeta, rawDiffs, customInstructions, allFindings.String(), fileFindings, batches, p)
}

// runSynthesis runs Phase 2 of the multi-pass review: synthesize all findings.
// This is shared between sequential and parallel modes.
func runSynthesis(
	ctx context.Context,
	client ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	allFindings string,
	fileFindings map[string]string,
	batches []reviewBatch,
	p *tea.Program,
) tea.Msg {
	if ctx.Err() != nil {
		return AIChatDoneMsg{Err: ctx.Err()}
	}

	p.Send(AIReviewSynthesisMsg{})

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
		{Role: "user", Content: "Synthesize the per-file findings into a final PR review. Use the get_diff tool if you need to verify any findings against the actual code."},
	}

	result, err := client.ChatStream(ctx, synthesisSystem, synthesisMessages, func(token string) {
		p.Send(AIChatDeltaMsg{Token: token})
	})
	if err != nil {
		return AIChatDoneMsg{Err: fmt.Errorf("synthesis: %w", err)}
	}

	summary := strings.TrimSpace(result)
	if summary == "" {
		return AIChatDoneMsg{Err: fmt.Errorf("synthesis returned empty response")}
	}

	return AIChatDoneMsg{
		FullResponse: summary,
		Review: &state.AIReview{
			Summary:  summary,
			Findings: allFindings,
		},
		FileFindings: fileFindings,
	}
}

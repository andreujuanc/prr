package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

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

// streamMultiPassReview runs a multi-pass PR review:
// Phase 1: Review each batch of files independently
// Phase 2: Synthesize all findings into a final review
//
// Progress and tokens are sent to the UI via program.Send().
// Returns an AIChatDoneMsg with Review data for persistence.
func streamMultiPassReview(
	ctx context.Context,
	client ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	p *tea.Program,
) tea.Cmd {
	return func() tea.Msg {
		batches := buildReviewBatches(rawDiffs)
		if len(batches) == 0 {
			return AIChatDoneMsg{Err: fmt.Errorf("no files to review")}
		}

		var allFindings strings.Builder

		// Phase 1: Per-batch reviews
		for i, batch := range batches {
			if ctx.Err() != nil {
				return AIChatDoneMsg{Err: ctx.Err()}
			}

			// Send progress update to UI
			p.Send(AIReviewProgressMsg{
				Batch: i + 1,
				Total: len(batches),
				Label: batch.label,
				Phase: "batch",
			})

			// Batch header in chat (dim)
			header := fmt.Sprintf(
				"\n### Reviewing: %s (%d file(s))\n",
				batch.label, len(batch.files),
			)
			p.Send(AIChatDeltaMsg{Token: "\x00DIM:" + header})

			// File list for this batch (dim)
			for _, f := range batch.files {
				p.Send(AIChatDeltaMsg{Token: "\x00DIM:  • " + f + "\n"})
			}
			p.Send(AIChatDeltaMsg{Token: "\x00DIM:\n"})

			// System prompt is instructions-only; diffs go in the user message
			systemPrompt := ai.ReviewBatchPrompt + "\n\n## PR Context\n" + prMeta
			if customInstructions != "" {
				systemPrompt += "\n\n## Project-Specific Instructions\n\n" + customInstructions
			}

			messages := []ai.Message{
				{Role: "user", Content: fmt.Sprintf(
					"Review these %d file(s): %s\n\n%s",
					len(batch.files),
					strings.Join(batch.files, ", "),
					batch.diffs,
				)},
			}

			// Stream the batch review — tokens rendered dim
			result, err := client.ChatStream(ctx, systemPrompt, messages, func(token string) {
				p.Send(AIChatDeltaMsg{Token: "\x00DIM:" + token})
			})
			if err != nil {
				return AIChatDoneMsg{Err: fmt.Errorf("batch %d/%d (%s): %w", i+1, len(batches), batch.label, err)}
			}

			// Collect findings for synthesis (raw text, not styled)
			allFindings.WriteString(fmt.Sprintf("### Batch %d: %s\n", i+1, batch.label))
			allFindings.WriteString(fmt.Sprintf("Files: %s\n\n", strings.Join(batch.files, ", ")))
			allFindings.WriteString(result)
			allFindings.WriteString("\n\n---\n\n")
		}

		// Phase 2: Synthesis
		if ctx.Err() != nil {
			return AIChatDoneMsg{Err: ctx.Err()}
		}

		p.Send(AIReviewProgressMsg{
			Batch: len(batches),
			Total: len(batches),
			Phase: "synthesis",
		})

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

		// Synthesis prompt includes findings + file listing.
		// The AI also has get_diff, read_file, read_base_file, search_code tools
		// to verify claims from the batch reviews.
		synthesisSystem := ai.ReviewSynthesisPrompt + "\n\n" +
			"## PR Metadata\n" + prMeta + "\n" +
			"## Changed Files\n" + fileListing.String() + "\n" +
			"## Per-batch Findings\n\n" + allFindings.String()
		if customInstructions != "" {
			synthesisSystem += "\n\n## Project-Specific Instructions\n\n" + customInstructions
		}

		synthesisMessages := []ai.Message{
			{Role: "user", Content: "Synthesize the per-file findings into a final PR review. Use the get_diff tool if you need to verify any findings against the actual code."},
		}

		// Synthesis header (normal brightness)
		p.Send(AIChatDeltaMsg{Token: "\n\n---\n\n## Final Review\n\n"})

		// Stream synthesis — tokens at full brightness (no DIM marker)
		var fullResponse strings.Builder
		_, err := client.ChatStream(ctx, synthesisSystem, synthesisMessages, func(token string) {
			fullResponse.WriteString(token)
			p.Send(AIChatDeltaMsg{Token: token})
		})
		if err != nil {
			return AIChatDoneMsg{Err: fmt.Errorf("synthesis: %w", err)}
		}

		// Return both findings and synthesis for persistence
		return AIChatDoneMsg{
			FullResponse: fullResponse.String(),
			Review: &state.AIReview{
				Summary:  fullResponse.String(),
				Findings: allFindings.String(),
			},
		}
	}
}

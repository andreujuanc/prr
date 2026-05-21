package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// doFallbackBatchCall handles Type="fallback-batch" review calls. It
// reuses the existing directory-batch system prompt and message
// builders (so the LLM gets the same input it always has), parses
// the response with the existing fallback parser, then converts each
// finding into a DeepFinding so the rest of the pipeline (recheck,
// synthesis) sees a single uniform shape.
//
// Returning DeepFinding-shape findings — instead of routing through
// the old RunBatchesOnly text path — is what lets recheck process
// fallback findings alongside AOI ones.
func doFallbackBatchCall(
	ctx context.Context,
	client ai.Client,
	call ReviewCall,
	opts ExecuteOptions,
	callIndex int,
	onToken func(string),
) (*state.DeepReviewResult, error) {
	batch := Batch{
		Label: fallbackLabelFromCall(call),
		Files: call.Files,
		Diffs: assembleFallbackDiffs(call),
	}

	systemPrompt := BuildBatchSystemPrompt(opts.PRMeta, opts.CustomInstructions)
	systemPrompt = ai.ResolveToolsForClient(client, systemPrompt)

	messages := BuildBatchMessages(batch)
	raw, callErr := client.ChatStream(ctx, systemPrompt, messages, onToken)
	if callErr != nil {
		return nil, callErr
	}

	if opts.OnLLMCall != nil {
		opts.OnLLMCall(callIndex, call, systemPrompt, messages[0].Content, raw)
	}

	parsed := ParseBatchResult(raw)
	if parsed == nil {
		// Wrapping errReviewParse is intentional: runReviewCallWithRetry
		// uses errors.Is(err, errReviewParse) to short-circuit retries
		// for shape failures, and a fallback batch that returned
		// unparseable prose isn't going to fix itself on retry either.
		return nil, fmt.Errorf("%w: fallback batch %q produced no parseable findings",
			errReviewParse, batch.Label)
	}

	findings := convertFallbackToDeepFindings(parsed)
	return &state.DeepReviewResult{
		Findings: findings,
	}, nil
}

// fallbackLabelFromCall builds a stable label for a fallback batch.
// We reuse the directory of the first file as the label — this
// matches the original BuildBatches behaviour (label = dir, "root"
// when at repo root). Used in error messages and downstream logs.
func fallbackLabelFromCall(call ReviewCall) string {
	if call.Category != "" {
		return call.Category
	}
	if len(call.Files) > 0 {
		if i := strings.LastIndex(call.Files[0], "/"); i > 0 {
			return call.Files[0][:i]
		}
		return "root"
	}
	return "fallback"
}

// assembleFallbackDiffs concatenates per-file diffs into the same
// "=== path ===\n<diff>\n\n" form that BuildBatches originally
// produced. The unified executor stores per-file diffs on
// ReviewCall.FileDiffs (same field AOI calls use), so we just walk
// call.Files in order and look each up.
func assembleFallbackDiffs(call ReviewCall) string {
	var b strings.Builder
	for _, f := range call.Files {
		diff, ok := call.FileDiffs[f]
		if !ok {
			continue
		}
		b.WriteString("=== ")
		b.WriteString(f)
		b.WriteString(" ===\n")
		b.WriteString(diff)
		b.WriteString("\n\n")
	}
	return b.String()
}

// convertFallbackToDeepFindings adapts the BatchFileReview output
// (the shape the fallback-batch prompt produces) into DeepFinding so
// downstream code sees one finding type. AOI-specific fields (Trace,
// DefensesChecked, SiblingDeviation) stay empty — fallback findings
// don't have that information. EvidenceSnippet IS populated: the
// fallback prompt requires it, and recheck's "re-read ±20 lines"
// pass benefits when the snippet is present even if the in-loop
// evidence verifier is skipped for fallback calls.
//
// Lines is rendered as a string for DeepFinding.Lines because
// BatchFinding only carries a single line number; recheck and
// synthesis both already tolerate single-line strings here.
func convertFallbackToDeepFindings(parsed []BatchFileReview) []state.DeepFinding {
	var out []state.DeepFinding
	for _, entry := range parsed {
		for _, f := range entry.Findings.Items {
			lines := ""
			if f.Line > 0 {
				lines = fmt.Sprintf("%d", f.Line)
			}
			out = append(out, state.DeepFinding{
				File:                entry.File,
				Lines:               lines,
				Severity:            f.Severity,
				Category:            f.Category,
				Title:               f.Title,
				Description:         f.Detail,
				EvidenceSnippet:     f.EvidenceSnippet,
				Suggestion:          f.Suggestion,
				ConfidenceScore:     f.ConfidenceScore,
				ConfidenceReasoning: f.ConfidenceReasoning,
			})
		}
	}
	return out
}

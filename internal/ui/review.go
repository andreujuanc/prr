package ui

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/review"
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

// reviewBatch represents a group of related files to review together.
// This is a thin wrapper around review.Batch for backward compat with tests.
type reviewBatch = review.Batch

// batchFileReview is a thin alias for review.BatchFileReview.
type batchFileReview = review.BatchFileReview

// checkMark is the symbol shown next to completed batches in the AI panel.
const checkMark = "\u2713" // ✓

// batchMaxChars re-exports the constant for any local references.
const batchMaxChars = review.BatchMaxChars

// maxDiffLines re-exports the constant.
const maxDiffLines = review.MaxDiffLines

// maxRetries re-exports the constant.
const maxRetries = review.MaxRetries

// reviewReporterAdapter wraps a TUI ReviewReporter as a review.Reporter.
type reviewReporterAdapter struct {
	rr ReviewReporter
}

func (a *reviewReporterAdapter) AOIProgress(status string, done bool, aoiCount int) {
	a.rr.AOIProgress(status, done, aoiCount)
}
func (a *reviewReporterAdapter) InitBatches(batches []review.BatchInfo) {
	infos := make([]AIReviewBatchInfo, len(batches))
	for i, b := range batches {
		infos[i] = AIReviewBatchInfo{Label: b.Label, NumFiles: b.NumFiles}
	}
	a.rr.InitBatches(infos)
}
func (a *reviewReporterAdapter) BatchProgress(batch int, status review.BatchStatus) {
	a.rr.BatchProgress(batch, batchStatusFromReview(status))
}
func (a *reviewReporterAdapter) SynthesisStarted() {
	a.rr.SynthesisStarted()
}
func (a *reviewReporterAdapter) Token(token string) {
	a.rr.Token(token)
}

// batchStatusFromReview maps review.BatchStatus to AIReviewBatchStatus.
func batchStatusFromReview(s review.BatchStatus) AIReviewBatchStatus {
	switch s {
	case review.StatusActive:
		return BatchActive
	case review.StatusDone:
		return BatchDone
	case review.StatusCached:
		return BatchCached
	case review.StatusFailed:
		return BatchFailed
	default:
		return BatchPending
	}
}

// batchStatusToReview maps AIReviewBatchStatus to review.BatchStatus.
func batchStatusToReview(s AIReviewBatchStatus) review.BatchStatus {
	switch s {
	case BatchActive:
		return review.StatusActive
	case BatchDone:
		return review.StatusDone
	case BatchCached:
		return review.StatusCached
	case BatchFailed:
		return review.StatusFailed
	default:
		return review.StatusPending
	}
}

// offsetReporter wraps a ReviewReporter and adds an offset to batch indices.
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

// ── Delegating functions ────────────────────────────────────────────────
// These delegate to review.* for the shared implementation.

func buildReviewBatches(rawDiffs map[string]string) []reviewBatch {
	return review.BuildBatches(rawDiffs)
}

func capDiff(diff string, files []string) string {
	return review.CapDiff(diff, files)
}

func buildBatchSystemPrompt(prMeta, customInstructions string) string {
	return review.BuildBatchSystemPrompt(prMeta, customInstructions)
}

func buildBatchMessages(batch reviewBatch) []ai.Message {
	return review.BuildBatchMessages(batch)
}

func parseBatchResult(raw string) []batchFileReview {
	return review.ParseBatchResult(raw)
}

func reviewBatchWithRetry(
	ctx context.Context,
	client ai.Client,
	systemPrompt string,
	batch reviewBatch,
	onToken func(string),
) (string, error) {
	return review.ReviewBatchWithRetry(ctx, client, systemPrompt, batch, onToken)
}

func synthesisWithRetry(
	ctx context.Context,
	client ai.Client,
	systemPrompt string,
	messages []ai.Message,
	onToken func(string),
) (string, error) {
	return review.SynthesisWithRetry(ctx, client, systemPrompt, messages, onToken)
}

func persistBatchFindings(reviewState *state.State, batch reviewBatch, rawResult string) ([]batchFileReview, map[string]string) {
	return review.PersistBatchFindings(reviewState, batch, rawResult)
}

func isBatchCached(batch reviewBatch, reviewState *state.State) bool {
	return review.IsBatchCached(batch, reviewState)
}

func collectCachedFindings(batch reviewBatch, reviewState *state.State) (string, map[string]string) {
	return review.CollectCachedFindings(batch, reviewState)
}

// ── streamMultiPassReview ───────────────────────────────────────────────

// streamMultiPassReview runs a multi-pass PR review using the shared pipeline core.
// This is now a thin wrapper around review.RunReviewCore.
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
	base, head string,
	aoiContextLines int,
	repoRoot string,
) tea.Cmd {
	return func() tea.Msg {
		adapter := &reviewReporterAdapter{rr: rr}

		coreResult, err := review.RunReviewCore(ctx, client, aoiClient, review.CoreOptions{
			PRMeta:             prMeta,
			RawDiffs:           rawDiffs,
			CustomInstructions: customInstructions,
			ReviewState:        reviewState,
			ParallelReviews:    parallelReviews,
			Base:               base,
			Head:               head,
			AOIContextLines:    aoiContextLines,
			RepoRoot:           repoRoot,
		}, adapter)
		if err != nil {
			return AIChatDoneMsg{Err: err}
		}

		return AIChatDoneMsg{
			FullResponse:     coreResult.Review.Summary,
			Review:           coreResult.Review,
			StructuredReview: coreResult.StructuredReview,
			FileFindings:     coreResult.FileFindings,
			DeepFindings:     coreResult.DeepFindings,
		}
	}
}

// ── Batch runners (kept for test compatibility) ─────────────────────────

// batchResult holds the outcome of a single batch review.
type batchResult struct {
	batch  reviewBatch
	result string
	cached bool
	err    error
}

// reviewBatchesSequential reviews batches one at a time with token streaming.
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

// runBatchesOnly reviews batches sequentially.
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
	adapter := &reviewReporterAdapter{rr: rr}

	findings, fileFindings, err := review.RunBatchesOnly(ctx, client, prMeta, rawDiffs,
		customInstructions, reviewState, batches, adapter)
	if err != nil {
		return AIChatDoneMsg{Err: err}
	}

	// Check if all batches were cached and review is current
	allCached := true
	for _, batch := range batches {
		if !isBatchCached(batch, reviewState) {
			allCached = false
			break
		}
	}
	if allCached && reviewState != nil && reviewState.Review != nil && !reviewState.IsReviewStale() {
		log.Printf("All %d batches cached and review is current — skipping synthesis", len(batches))
		return AIChatDoneMsg{
			Review:       reviewState.Review,
			FileFindings: fileFindings,
		}
	}

	if !synthesize {
		return AIChatDoneMsg{
			Review: &state.AIReview{
				Findings: findings,
			},
			FileFindings: fileFindings,
		}
	}

	return runSynthesis(ctx, client, prMeta, rawDiffs, customInstructions, findings, fileFindings, batches, rr)
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

	var uncachedIndices []int
	for i, batch := range batches {
		if isBatchCached(batch, reviewState) {
			cached, cachedFF := collectCachedFindings(batch, reviewState)
			results[i] = batchResult{batch: batch, result: cached, cached: true}
			rr.BatchProgress(i, BatchCached)
			for f, findings := range cachedFF {
				allFileFindings[f] = findings
			}
		} else {
			uncachedIndices = append(uncachedIndices, i)
		}
	}

	if len(uncachedIndices) > 0 {
		work := make(chan int, len(uncachedIndices))
		var wg sync.WaitGroup

		workers := maxWorkers
		if workers > len(uncachedIndices) {
			workers = len(uncachedIndices)
		}

		systemPrompt := buildBatchSystemPrompt(prMeta, customInstructions)

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
					results[idx] = batchResult{batch: batch, result: result, err: err}

					if err == nil {
						rr.BatchProgress(idx, BatchDone)
					} else {
						rr.BatchProgress(idx, BatchFailed)
					}
				}
			}()
		}

		for _, idx := range uncachedIndices {
			work <- idx
		}
		close(work)
		wg.Wait()
	}

	var allFindings strings.Builder
	for i, res := range results {
		if res.err != nil {
			return AIChatDoneMsg{Err: fmt.Errorf("batch %d/%d (%s): %w", i+1, len(batches), res.batch.Label, res.err)}
		}

		allFindings.WriteString(fmt.Sprintf("### Batch %d: %s\n", i+1, res.batch.Label))
		allFindings.WriteString(fmt.Sprintf("Files: %s\n\n", strings.Join(res.batch.Files, ", ")))

		if !res.cached {
			parsed, batchFF := persistBatchFindings(reviewState, res.batch, res.result)
			for f, findings := range batchFF {
				allFileFindings[f] = findings
			}
			if parsed != nil {
				for _, entry := range parsed {
					if !entry.Findings.IsEmpty() {
						allFindings.WriteString(fmt.Sprintf("#### %s\nPurpose: %s\n%s\n\n", entry.File, entry.Purpose, entry.Findings.Text()))
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

	if len(uncachedIndices) == 0 && reviewState != nil && reviewState.Review != nil && !reviewState.IsReviewStale() {
		log.Printf("All %d batches cached and review is current — skipping synthesis (parallel)", len(batches))
		return AIChatDoneMsg{
			Review:       reviewState.Review,
			FileFindings: allFileFindings,
		}
	}

	return runSynthesis(ctx, client, prMeta, rawDiffs, customInstructions, allFindings.String(), allFileFindings, batches, rr)
}

// runSynthesis runs Phase 2: synthesize all findings.
// Delegates to review.RunSynthesis.
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
	adapter := &reviewReporterAdapter{rr: rr}

	synthResult, err := review.RunSynthesis(ctx, client, prMeta, rawDiffs, customInstructions, allFindings, fileFindings, adapter)
	if err != nil {
		return AIChatDoneMsg{Err: err}
	}

	return AIChatDoneMsg{
		FullResponse:     synthResult.Review.Summary,
		Review:           synthResult.Review,
		StructuredReview: synthResult.Structured,
		FileFindings:     fileFindings,
	}
}

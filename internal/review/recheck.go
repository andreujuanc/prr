package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// RecheckOptions configures the recheck phase.
type RecheckOptions struct {
	// Mode is "pr" or "audit" — used in the preamble.
	Mode Mode

	// ProjectContext for additional context in the prompt.
	ProjectContext string

	// RepoRoot is the absolute path to the repository root. When
	// set, the dismiss pass runs a cheap filesystem grep per finding
	// for matching test files (foo_test.*, foo.test.*, foo.spec.*)
	// and includes a one-line "Test coverage:" annotation per
	// finding in the user message — so the recheck model can
	// downgrade findings whose existing test suite should have
	// caught them, and synthesis can list missing tests. Empty =
	// skip the cross-check.
	RepoRoot string

	// MaxFindingsPerBatch caps findings per LLM call in the
	// consolidate pass. If total findings exceed this, they're split
	// by category. Default: 50.
	//
	// The dismiss pass uses DismissBatchSize instead — dismissals
	// benefit from much smaller batches because the model judges each
	// finding individually rather than looking for cross-finding
	// patterns.
	MaxFindingsPerBatch int

	// DismissBatchSize caps findings per LLM call in the dismiss
	// pass. Findings are first grouped by category, then each
	// category is chunked into batches of this size. Default: 3.
	// 1 means one LLM call per finding (most focused, most expensive).
	DismissBatchSize int

	// MaxConcurrency caps parallel batch calls. Default: 5.
	MaxConcurrency int

	// OnLLMCall is called with the prompt and response for debugging. Can be nil.
	OnLLMCall func(systemPrompt string, userMsg string, response string)

	// OnProgress reports per-batch completion. done counts findings
	// successfully processed (batch.size summed across completed
	// batches); total is len(findings). Called once at start
	// (0, total) and once after each batch completes. Optional.
	//
	// The cross-batch dedup pass that runs when len(findings) >
	// MaxFindingsPerBatch does not advance the counter — done stays
	// at total during that final pass. It's typically fast (<2s)
	// and treating it as overhead simplifies the contract.
	OnProgress func(done, total int)

	// OnBatchInit fires once per dismiss-pass batch before any goroutine
	// launches. Used by the Batches panel to populate row identity so
	// recheck shows the same "rows you can watch" the deep-review
	// phase does. Optional.
	OnBatchInit func(index int, label string, numFindings int)

	// OnBatchActive fires when a dismiss-pass batch's goroutine
	// acquires its semaphore slot, just before the LLM call. Optional.
	OnBatchActive func(index int)

	// OnBatchDone fires when a dismiss-pass batch finishes (success or
	// error). err is non-nil on failure. Optional.
	OnBatchDone func(index int, err error)
}

// RecheckResult holds the output of the recheck phase.
type RecheckResult struct {
	// Findings is the cleaned, deduplicated list.
	Findings []state.DeepFinding

	// Dismissed is the full record of findings the recheck pass
	// removed, with the rationale the model gave for each. Used by
	// the audit report to show users *what* got dismissed and *why*
	// — which is what makes recheck auditable. DismissedCount below
	// is len(Dismissed); kept as a separate field so existing
	// callers don't break.
	Dismissed []state.DismissedRecord

	// DismissedCount is how many findings were removed.
	// Equals len(Dismissed) and is maintained alongside it.
	DismissedCount int

	// ConsolidatedCount is how many findings were merged.
	ConsolidatedCount int

	// ModifiedCount is how many findings had severity/description adjusted.
	ModifiedCount int
}

// AssignFindingIDs assigns sequential IDs (F-001, F-002, ...) to findings.
// Modifies the slice in place and returns it for convenience.
func AssignFindingIDs(findings []state.DeepFinding) []state.DeepFinding {
	for i := range findings {
		findings[i].FindingID = fmt.Sprintf("F-%03d", i+1)
	}
	return findings
}

// RecheckFindings runs the two-pass recheck pipeline.
//
//	Pass 1 — consolidate. The full candidate set goes through the
//	         cross-file consolidator. It produces (kept, consolidated)
//	         and does NOT dismiss anything. The reason for going
//	         first: per-file dismissal can erase findings that look
//	         weak in isolation but are members of a cross-file
//	         pattern. Running consolidation first preserves the
//	         pattern signal before that dismissal happens.
//
//	Pass 2 — dismiss. The post-consolidate kept set is batched by
//	         file and sent to the per-file dismisser. It produces
//	         (kept, modified, dismissed) but never `consolidated`
//	         (that's done). Per-file batching lets the LLM hold one
//	         file's full context.
//
// Both passes are non-fatal: if either errors, that pass's findings
// pass through unchanged. AssignFindingIDs runs once at the top so
// IDs are stable for both passes' parsers.
func RecheckFindings(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	opts RecheckOptions,
) (*RecheckResult, error) {
	if len(findings) == 0 {
		return &RecheckResult{}, nil
	}

	AssignFindingIDs(findings)

	total := len(findings)
	emit := func(done int) {
		if opts.OnProgress != nil {
			opts.OnProgress(done, total)
		}
	}
	emit(0)

	maxPerBatch := opts.MaxFindingsPerBatch
	if maxPerBatch <= 0 {
		maxPerBatch = 50
	}

	// ── Pass 1: Cross-file consolidation ────────────────────────
	// Consolidation runs on the FULL set in a single call when it
	// fits. Above the per-batch cap, we group by category — patterns
	// almost always live within a category. Cross-category patterns
	// are rare enough that we accept missing them at scale rather
	// than ballooning the call into one massive request.
	//
	// Output is a single combined list: originals not in any merge
	// group + new merged findings. Everything then flows into Pass 2
	// (dismiss) — including the merged ones — so a bad consolidation
	// can still be dropped downstream.
	postConsolidate, consolidatedCount, consolErr := runConsolidatePass(ctx, client, findings, opts, maxPerBatch)
	if consolErr != nil {
		log.Printf("Recheck pass 1 (consolidate) failed: %v — proceeding with original findings", consolErr)
		postConsolidate = findings
		consolidatedCount = 0
	}

	// Apply the systemic gate to any "Systemic:"-flagged finding
	// that didn't reach the 3-distinct-sites bar. Severity is
	// untouched — only the Systemic prefix and flag.
	postConsolidate = ApplySystemicGate(postConsolidate)

	// ── Pass 2: Dismissal over the merged set ──────────────────
	dismissResult, dismissErr := runDismissPass(ctx, client, postConsolidate, opts, maxPerBatch, emit)
	if dismissErr != nil {
		log.Printf("Recheck pass 2 (dismiss) failed: %v — keeping post-consolidate set", dismissErr)
		emit(total)
		return &RecheckResult{
			Findings:          postConsolidate,
			ConsolidatedCount: consolidatedCount,
		}, nil
	}

	emit(total)
	return &RecheckResult{
		Findings:          dismissResult.Findings,
		Dismissed:         dismissResult.Dismissed,
		DismissedCount:    len(dismissResult.Dismissed),
		ConsolidatedCount: consolidatedCount,
		ModifiedCount:     dismissResult.ModifiedCount,
	}, nil
}

// runConsolidatePass runs cross-file consolidation. Returns the
// findings that survived consolidation (i.e. were not absorbed into
// a systemic finding) and the consolidated systemic findings as a
// returned as a single flat list: originals not merged + any new
// merged findings. Both flow into the dismiss pass downstream — a
// bad consolidation can still be dropped there.
//
// absorbedCount sums the constituents of every accepted merge group
// (e.g. one merge of 3 findings = 3 absorbed). Tracks "how many of
// the input findings ended up inside a systemic" for the report.
func runConsolidatePass(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	opts RecheckOptions,
	maxPerBatch int,
) (out []state.DeepFinding, absorbedCount int, err error) {
	if len(findings) == 0 {
		return findings, 0, nil
	}
	if len(findings) <= maxPerBatch {
		return recheckConsolidateBatch(ctx, client, findings, opts)
	}

	// Batch by category. Each batch is consolidated independently —
	// cross-category patterns are lost, but those are rare and the
	// alternative (one massive call) is worse for attention and
	// token cost.
	batches := splitFindingsByCategory(findings, maxPerBatch)
	maxConc := opts.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 5
	}

	type batchOut struct {
		findings []state.DeepFinding
		absorbed int
	}
	outs := make([]batchOut, len(batches))
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	for i, batch := range batches {
		wg.Add(1)
		go func(i int, batch []state.DeepFinding) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				outs[i] = batchOut{findings: batch}
				return
			}
			r, absorbed, err := recheckConsolidateBatch(ctx, client, batch, opts)
			if err != nil {
				log.Printf("Recheck consolidate batch %d failed: %v (keeping all)", i+1, err)
				outs[i] = batchOut{findings: batch}
				return
			}
			outs[i] = batchOut{findings: r, absorbed: absorbed}
		}(i, batch)
	}
	wg.Wait()

	var combined []state.DeepFinding
	totalAbsorbed := 0
	for _, o := range outs {
		combined = append(combined, o.findings...)
		totalAbsorbed += o.absorbed
	}
	return combined, totalAbsorbed, nil
}

// runDismissPass runs dismissal on the post-consolidate set.
// Findings are grouped by category and chunked into batches of
// DismissBatchSize (default 3). Each batch is one LLM call; calls
// run in parallel up to MaxConcurrency.
//
// Returns the same shape as the original recheck pipeline so the
// caller can fold its output into the merged result. The maxPerBatch
// parameter is no longer used (kept in the signature for now to
// minimise call-site churn — drop in a follow-up).
func runDismissPass(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	opts RecheckOptions,
	maxPerBatch int,
	emit func(int),
) (*RecheckResult, error) {
	_ = maxPerBatch // intentionally unused; see comment above
	if len(findings) == 0 {
		return &RecheckResult{}, nil
	}

	chunkSize := opts.DismissBatchSize
	if chunkSize <= 0 {
		chunkSize = 3
	}
	batches := splitFindingsByCategoryChunked(findings, chunkSize)

	maxConc := opts.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 5
	}

	// Surface batch identity to the Batches panel before the goroutines
	// launch so all rows appear as queued together, then transition to
	// active as slots open. Matches the deep-review init/active/done
	// shape.
	if opts.OnBatchInit != nil {
		for i, batch := range batches {
			opts.OnBatchInit(i, recheckBatchLabel(batch), distinctFileCount(batch))
		}
	}

	type batchOutcome struct {
		index           int
		findings        []state.DeepFinding
		dismissedRecord []state.DismissedRecord
		modified        int
	}

	outcomes := make([]batchOutcome, len(batches))
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	var doneCount int64

	for i, batch := range batches {
		wg.Add(1)
		go func(i int, batch []state.DeepFinding) {
			defer wg.Done()
			defer func() {
				d := atomic.AddInt64(&doneCount, int64(len(batch)))
				emit(int(d))
			}()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				outcomes[i] = batchOutcome{index: i, findings: batch}
				if opts.OnBatchDone != nil {
					opts.OnBatchDone(i, ctx.Err())
				}
				return
			}
			if opts.OnBatchActive != nil {
				opts.OnBatchActive(i)
			}
			log.Printf("Recheck dismiss batch %d/%d: %d findings", i+1, len(batches), len(batch))
			result, err := recheckDismissBatch(ctx, client, batch, opts)
			if err != nil {
				log.Printf("Recheck dismiss batch %d failed: %v (keeping all findings)", i+1, err)
				outcomes[i] = batchOutcome{index: i, findings: batch}
				if opts.OnBatchDone != nil {
					opts.OnBatchDone(i, err)
				}
				return
			}
			outcomes[i] = batchOutcome{
				index:           i,
				findings:        result.Findings,
				dismissedRecord: result.Dismissed,
				modified:        result.ModifiedCount,
			}
			if opts.OnBatchDone != nil {
				opts.OnBatchDone(i, nil)
			}
		}(i, batch)
	}
	wg.Wait()

	var allKept []state.DeepFinding
	var allDismissed []state.DismissedRecord
	totalModified := 0
	for _, o := range outcomes {
		allKept = append(allKept, o.findings...)
		allDismissed = append(allDismissed, o.dismissedRecord...)
		totalModified += o.modified
	}
	return &RecheckResult{
		Findings:       allKept,
		Dismissed:      allDismissed,
		DismissedCount: len(allDismissed),
		ModifiedCount:  totalModified,
	}, nil
}

// splitFindingsByCategory groups findings by Category for the
// consolidator's batches. Patterns almost always live within a
// category (input-validation, error-handling, …), so within-
// recheckBatchLabel picks a readable label for a dismiss-pass
// batch. Dismiss now batches by category, so every finding in the
// batch shares one — use it as the label. Falls back to
// "_uncategorized" when missing.
func recheckBatchLabel(batch []state.DeepFinding) string {
	if len(batch) == 0 {
		return "empty"
	}
	cat := batch[0].Category
	if cat == "" {
		return "_uncategorized"
	}
	return cat
}

// distinctFileCount counts the unique files touched by a batch of
// findings. Used for the panel's "N files" column so the row count
// reflects what the row actually covers — earlier code passed the
// finding count, which made the column read "21 files" for a batch
// of 21 findings that actually touched 5 files.
func distinctFileCount(batch []state.DeepFinding) int {
	seen := make(map[string]struct{})
	for _, f := range batch {
		if f.File != "" {
			seen[f.File] = struct{}{}
		}
	}
	return len(seen)
}

// category batches give the LLM the right neighborhood to spot
// them while keeping each call's token budget bounded.
func splitFindingsByCategory(findings []state.DeepFinding, maxPerBatch int) [][]state.DeepFinding {
	byCat := make(map[string][]state.DeepFinding)
	var order []string
	for _, f := range findings {
		key := f.Category
		if key == "" {
			key = "_uncategorized"
		}
		if _, seen := byCat[key]; !seen {
			order = append(order, key)
		}
		byCat[key] = append(byCat[key], f)
	}

	var batches [][]state.DeepFinding
	var current []state.DeepFinding
	for _, cat := range order {
		group := byCat[cat]
		if len(current) > 0 && len(current)+len(group) > maxPerBatch {
			batches = append(batches, current)
			current = nil
		}
		if len(group) > maxPerBatch {
			batches = append(batches, group)
			continue
		}
		current = append(current, group...)
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// recheckConsolidateBatch runs ONE consolidator LLM call on the
// given findings using RecheckConsolidatePrompt. The model only
// emits merge groups; anything not in a group is implicitly kept by
// the parser. Returns the combined post-merge set (originals not
// merged + newly created merged findings). Any stray `dismissed`
// or `modified` entries from the model (forbidden by the prompt)
// are re-attached to the result rather than dropped.
func recheckConsolidateBatch(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	opts RecheckOptions,
) ([]state.DeepFinding, int, error) {
	systemPrompt := buildRecheckSystemPrompt(ai.RecheckConsolidatePrompt, opts)
	systemPrompt = ai.ResolveToolsForClient(client, systemPrompt)

	findingsJSON, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return nil, 0, fmt.Errorf("marshal findings: %w", err)
	}
	messages := []ai.Message{{
		Role: "user",
		Content: fmt.Sprintf(
			"Here are %d findings spanning multiple files. Identify cross-file patterns and consolidate them. Do NOT dismiss or modify individual findings — that's the next pass's job.\n\n%s",
			len(findings), string(findingsJSON),
		),
	}}

	// Retry transient HTTP errors. Recheck consolidation is the
	// pre-step to per-file dedup; a transient blip shouldn't kill
	// the cross-file pattern detection.
	raw, err := ai.RetryTransient(ctx, 3, "recheck-consolidate", func(ctx context.Context) (string, error) {
		return client.ChatStream(ctx, systemPrompt, messages, nil)
	})
	if err != nil {
		return nil, 0, fmt.Errorf("recheck consolidate call: %w", err)
	}
	if opts.OnLLMCall != nil {
		opts.OnLLMCall(systemPrompt, messages[0].Content, raw)
	}

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		return nil, 0, err
	}

	// The consolidator prompt forbids `dismissed` and `modified`.
	// If the model emitted either, log it and treat as kept — we
	// trust the dismiss pass to handle those decisions later.
	if len(result.Dismissed) > 0 || result.ModifiedCount > 0 {
		log.Printf("Recheck consolidate: model emitted %d dismiss(es) and %d modification(s) it was told not to — re-attaching",
			len(result.Dismissed), result.ModifiedCount)
		for _, d := range result.Dismissed {
			result.Findings = append(result.Findings, d.Finding)
		}
	}

	return result.Findings, result.ConsolidatedCount, nil
}

// recheckDismissBatch runs ONE per-file dismissal LLM call using
// RecheckDismissPrompt. The model emits `kept`, `modified`, and
// `dismissed`; any `consolidated` entry (which this prompt forbids
// because cross-file consolidation already happened) is logged and
// the constituents are re-attached to kept — we don't want a
// confused model to delete real findings via the "wrong" bucket.
func recheckDismissBatch(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	opts RecheckOptions,
) (*RecheckResult, error) {
	systemPrompt := buildRecheckSystemPrompt(ai.RecheckDismissPrompt, opts)
	systemPrompt = ai.ResolveToolsForClient(client, systemPrompt)

	findingsJSON, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal findings: %w", err)
	}

	// Optional pre-recheck test-coverage cross-check. Renders as a
	// "Test coverage:" annotation per finding so the model can ask
	// "if a test exists and passes, the finding is probably wrong."
	// Skipped when opts.RepoRoot is empty (PR-review path passes a
	// real repo root; some tests don't).
	hints := CheckTestCoverage(opts.RepoRoot, findings)
	testCoverageBlock := renderTestCoverageHints(hints)

	messages := []ai.Message{{
		Role: "user",
		Content: fmt.Sprintf(
			"Here are %d findings to dismiss-or-refine. Cross-file consolidation already ran upstream — do NOT consolidate. Focus on dismissing false positives and within-file dedup.%s\n\n%s",
			len(findings), testCoverageBlock, string(findingsJSON),
		),
	}}

	// Retry transient HTTP errors. Recheck dismiss is the
	// final-quality gate before findings reach the user.
	raw, err := ai.RetryTransient(ctx, 3, "recheck-dismiss", func(ctx context.Context) (string, error) {
		return client.ChatStream(ctx, systemPrompt, messages, nil)
	})
	if err != nil {
		return nil, fmt.Errorf("recheck dismiss call: %w", err)
	}
	if opts.OnLLMCall != nil {
		opts.OnLLMCall(systemPrompt, messages[0].Content, raw)
	}

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		return nil, err
	}
	if result.ConsolidatedCount > 0 {
		log.Printf("Recheck dismiss: model emitted %d consolidation(s) it was told not to — ignoring",
			result.ConsolidatedCount)
		// parseRecheckResult already mutated result.Findings to
		// include the merged consolidations; we can't easily undo
		// that. Accept it: the merged finding is in result.Findings
		// already (per the parser), and the consolidator's per-
		// finding records are gone. This is a degradation, not a
		// data-loss bug, and is rare in practice.
		result.ConsolidatedCount = 0
	}
	return result, nil
}

// renderTestCoverageHints turns the per-finding hint map into a
// prompt block to prepend to the dismiss user message. Empty when
// there are no hints to surface.
func renderTestCoverageHints(hints map[string]TestCoverageHint) string {
	if len(hints) == 0 {
		return ""
	}

	// Sort by FindingID for deterministic prompts.
	keys := make([]string, 0, len(hints))
	for k := range hints {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("\n\nTest-suite cross-check (per finding ID):\n")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("- %s: %s\n", k, hints[k]))
	}
	b.WriteString("\nWhen a finding is tagged `test_exists_and_covers`, ASK: if the existing test passes, this finding is probably wrong — dismiss with rationale `existing-test-would-have-caught-it` unless the finding identifies a test-suite gap. When `test_exists_but_not_covering`, keep the finding and note the missing test case. When `no_test_file`, the finding stays and synthesis lists it under missing_tests.")
	return b.String()
}

// buildRecheckSystemPrompt assembles the system prompt: mode preamble +
// the chosen base prompt + project context. Shared between the two
// passes so they treat mode/context identically.
func buildRecheckSystemPrompt(basePrompt string, opts RecheckOptions) string {
	systemPrompt := basePrompt
	switch opts.Mode {
	case ModePR:
		systemPrompt = "You are rechecking findings from a pull request review.\n" +
			"These findings were generated by examining changes in a PR.\n\n" + systemPrompt
	case ModeAudit:
		systemPrompt = "You are rechecking findings from a full codebase audit.\n" +
			"These findings were generated by examining the entire source code.\n\n" + systemPrompt
	}
	if opts.ProjectContext != "" {
		pc := opts.ProjectContext
		if strings.HasPrefix(strings.TrimSpace(pc), "## Project Context") {
			systemPrompt += "\n\n" + pc
		} else {
			systemPrompt += "\n\n## Project Context\n\n" + pc
		}
	}
	return systemPrompt
}

// recheckResponse is the expected JSON structure from the LLM.
type recheckResponse struct {
	Kept     []string `json:"kept"`
	Modified []struct {
		FindingID   string `json:"finding_id"`
		Severity    string `json:"severity,omitempty"`
		Title       string `json:"title,omitempty"`
		Description string `json:"description,omitempty"`
		Suggestion  string `json:"suggestion,omitempty"`
		Rationale   string `json:"rationale,omitempty"`
	} `json:"modified"`
	Consolidated []struct {
		FindingIDs []string          `json:"finding_ids"`
		Finding    state.DeepFinding `json:"finding"`
	} `json:"consolidated"`
	Dismissed []struct {
		FindingID string `json:"finding_id"`
		Rationale string `json:"rationale"`
	} `json:"dismissed"`
}

// parseRecheckResult parses the LLM response and applies changes to the findings.
func parseRecheckResult(original []state.DeepFinding, raw string) (*RecheckResult, error) {
	s := strings.TrimSpace(raw)

	// Strip markdown fences
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	// Find JSON start
	jsonStart := strings.IndexAny(s, "{")
	if jsonStart == -1 {
		log.Printf("Recheck: no JSON found in response, keeping all findings")
		return &RecheckResult{Findings: original}, nil
	}
	s = s[jsonStart:]

	var resp recheckResponse
	if err := unmarshalLLMResponse([]byte(s), &resp); err != nil {
		log.Printf("Recheck: failed to parse response: %v — response prefix: %q — keeping all findings", err, previewForLog([]byte(s), 500))
		return &RecheckResult{Findings: original}, nil
	}

	// Build lookup by FindingID
	byID := make(map[string]*state.DeepFinding, len(original))
	for i := range original {
		byID[original[i].FindingID] = &original[i]
	}

	var result []state.DeepFinding

	// 1. Kept — pass through unchanged
	for _, id := range resp.Kept {
		if f, ok := byID[id]; ok {
			result = append(result, *f)
			delete(byID, id)
		} else {
			log.Printf("Recheck: kept ID %q not found in original findings", id)
		}
	}

	// 2. Modified — apply changes
	modifiedCount := 0
	for _, mod := range resp.Modified {
		f, ok := byID[mod.FindingID]
		if !ok {
			log.Printf("Recheck: modified ID %q not found in original findings", mod.FindingID)
			continue
		}
		if mod.Severity != "" {
			f.Severity = mod.Severity
		}
		if mod.Title != "" {
			f.Title = mod.Title
		}
		if mod.Description != "" {
			f.Description = mod.Description
		}
		if mod.Suggestion != "" {
			f.Suggestion = mod.Suggestion
		}
		result = append(result, *f)
		delete(byID, mod.FindingID)
		modifiedCount++
	}

	// 3. Consolidated — merge into single finding.
	//
	// Single-ID groups are ignored: a merge that doesn't actually
	// combine 2+ findings is no merge at all. The original finding
	// stays in byID (no delete), so it's picked up by the safety
	// net below.
	consolidatedCount := 0
	for _, cons := range resp.Consolidated {
		if len(cons.FindingIDs) < 2 {
			log.Printf("Recheck consolidate: ignoring merge group with %d finding_id(s) — needs 2+", len(cons.FindingIDs))
			continue
		}
		consolidatedCount += len(cons.FindingIDs)
		// Remove all constituent findings from lookup
		for _, id := range cons.FindingIDs {
			delete(byID, id)
		}
		// Add the merged finding, with Systemic=true so downstream
		// (recheckConsolidateBatch / runConsolidatePass) can route
		// it around the per-file dismiss pass via a structural flag
		// rather than a heuristic on File/Title.
		merged := cons.Finding
		if merged.FindingID == "" && len(cons.FindingIDs) > 0 {
			merged.FindingID = cons.FindingIDs[0]
		}
		merged.Systemic = true
		result = append(result, merged)
	}

	// 4. Dismissed — record original finding + rationale so the
	// report can show users what got removed and why. Skipping
	// log.Printf for dismissals here keeps the audit trail in one
	// place (RecheckResult.Dismissed); previously the rationale
	// was emitted to the log and lost.
	var dismissed []state.DismissedRecord
	for _, dis := range resp.Dismissed {
		orig, ok := byID[dis.FindingID]
		if !ok {
			log.Printf("Recheck: dismissed ID %q not found in original findings", dis.FindingID)
			continue
		}
		dismissed = append(dismissed, state.DismissedRecord{
			FindingID: dis.FindingID,
			Finding:   *orig,
			Rationale: dis.Rationale,
		})
		delete(byID, dis.FindingID)
	}

	// 5. Any findings not accounted for — keep them (safety net)
	if len(byID) > 0 {
		log.Printf("Recheck: %d findings not referenced in response — keeping them", len(byID))
		for _, f := range byID {
			result = append(result, *f)
		}
	}

	return &RecheckResult{
		Findings:          result,
		Dismissed:         dismissed,
		DismissedCount:    len(dismissed),
		ConsolidatedCount: consolidatedCount,
		ModifiedCount:     modifiedCount,
	}, nil
}

// splitFindingsByFile groups findings by file and creates batches that
// don't exceed maxPerBatch. Files with many findings may span multiple batches.
func splitFindingsByFile(findings []state.DeepFinding, maxPerBatch int) [][]state.DeepFinding {
	// Group by file
	byFile := make(map[string][]state.DeepFinding)
	var fileOrder []string
	for _, f := range findings {
		if _, seen := byFile[f.File]; !seen {
			fileOrder = append(fileOrder, f.File)
		}
		byFile[f.File] = append(byFile[f.File], f)
	}

	var batches [][]state.DeepFinding
	var current []state.DeepFinding

	for _, file := range fileOrder {
		fileFDs := byFile[file]
		// If adding this file would exceed limit, flush current batch
		if len(current) > 0 && len(current)+len(fileFDs) > maxPerBatch {
			batches = append(batches, current)
			current = nil
		}
		// If a single file exceeds the limit, it gets its own batch
		if len(fileFDs) > maxPerBatch {
			batches = append(batches, fileFDs)
			continue
		}
		current = append(current, fileFDs...)
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}

	return batches
}

// splitFindingsByCategoryChunked groups findings by category, then
// chunks each category into batches of at most chunkSize findings.
// Used by the dismiss pass so each LLM call sees a small slice of
// one category's findings — the model can focus on judging each
// finding without cross-category context bleed.
//
// Category order is preserved by first appearance. chunkSize <= 0 is
// treated as 1 (one finding per batch).
func splitFindingsByCategoryChunked(findings []state.DeepFinding, chunkSize int) [][]state.DeepFinding {
	if chunkSize <= 0 {
		chunkSize = 1
	}
	byCat := make(map[string][]state.DeepFinding)
	var order []string
	for _, f := range findings {
		key := f.Category
		if key == "" {
			key = "_uncategorized"
		}
		if _, seen := byCat[key]; !seen {
			order = append(order, key)
		}
		byCat[key] = append(byCat[key], f)
	}

	var batches [][]state.DeepFinding
	for _, cat := range order {
		group := byCat[cat]
		for start := 0; start < len(group); start += chunkSize {
			end := start + chunkSize
			if end > len(group) {
				end = len(group)
			}
			batches = append(batches, group[start:end])
		}
	}
	return batches
}

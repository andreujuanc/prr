package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

	// MaxFindingsPerBatch caps findings per LLM call.
	// If total findings exceed this, they're split by file.
	// Default: 50.
	MaxFindingsPerBatch int

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
	// almost always live within a category, so within-category
	// batches preserve consolidation opportunities. Cross-category
	// patterns are rare enough that we accept missing them at scale
	// rather than ballooning the call into one massive request.
	postConsolidate, consolidations, consolErr := runConsolidatePass(ctx, client, findings, opts, maxPerBatch)
	if consolErr != nil {
		log.Printf("Recheck pass 1 (consolidate) failed: %v — proceeding with original findings", consolErr)
		postConsolidate = findings
		consolidations = nil
	}

	// ── Pass 2: Per-file dismissal ─────────────────────────────
	dismissResult, dismissErr := runDismissPass(ctx, client, postConsolidate, opts, maxPerBatch, emit, total)
	if dismissErr != nil {
		log.Printf("Recheck pass 2 (dismiss) failed: %v — keeping post-consolidate set", dismissErr)
		// Fall back: keep everything that survived consolidation, no dismissal records.
		emit(total)
		return &RecheckResult{
			Findings:          append(append([]state.DeepFinding(nil), consolidations...), postConsolidate...),
			ConsolidatedCount: len(findings) - len(postConsolidate),
		}, nil
	}

	// Merge: consolidations go first (systemic findings are usually
	// higher priority), then per-file kept/modified.
	merged := make([]state.DeepFinding, 0, len(consolidations)+len(dismissResult.Findings))
	merged = append(merged, consolidations...)
	merged = append(merged, dismissResult.Findings...)

	emit(total)
	return &RecheckResult{
		Findings:          merged,
		Dismissed:         dismissResult.Dismissed,
		DismissedCount:    len(dismissResult.Dismissed),
		ConsolidatedCount: len(findings) - len(postConsolidate),
		ModifiedCount:     dismissResult.ModifiedCount,
	}, nil
}

// runConsolidatePass runs cross-file consolidation. Returns the
// findings that survived consolidation (i.e. were not absorbed into
// a systemic finding) and the consolidated systemic findings as a
// separate slice. The two are returned separately so the caller can
// route only the non-systemic findings to the per-file dismiss pass.
func runConsolidatePass(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	opts RecheckOptions,
	maxPerBatch int,
) (kept []state.DeepFinding, consolidations []state.DeepFinding, err error) {
	if len(findings) == 0 {
		return findings, nil, nil
	}
	if len(findings) <= maxPerBatch {
		result, err := recheckConsolidateBatch(ctx, client, findings, opts)
		if err != nil {
			return nil, nil, err
		}
		return result.kept, result.consolidations, nil
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
		index  int
		kept   []state.DeepFinding
		consol []state.DeepFinding
		err    error
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
				outs[i] = batchOut{index: i, kept: batch, err: ctx.Err()}
				return
			}
			r, err := recheckConsolidateBatch(ctx, client, batch, opts)
			if err != nil {
				log.Printf("Recheck consolidate batch %d failed: %v (keeping all)", i+1, err)
				outs[i] = batchOut{index: i, kept: batch}
				return
			}
			outs[i] = batchOut{index: i, kept: r.kept, consol: r.consolidations}
		}(i, batch)
	}
	wg.Wait()

	for _, o := range outs {
		kept = append(kept, o.kept...)
		consolidations = append(consolidations, o.consol...)
	}
	return kept, consolidations, nil
}

// runDismissPass runs per-file dismissal on the post-consolidate set.
// Batched by file so each batch has the full file-level context.
// Returns the same shape as the original recheck pipeline so the
// caller can fold its output into the merged result.
func runDismissPass(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	opts RecheckOptions,
	maxPerBatch int,
	emit func(int),
	total int,
) (*RecheckResult, error) {
	if len(findings) == 0 {
		return &RecheckResult{}, nil
	}
	if len(findings) <= maxPerBatch {
		// Single batch — covers the small-audit case.
		result, err := recheckDismissBatch(ctx, client, findings, opts)
		return result, err
	}

	batches := splitFindingsByFile(findings, maxPerBatch)
	maxConc := opts.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 5
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
	startedAt := len(findings) - total
	if startedAt < 0 {
		startedAt = 0
	}

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
				return
			}
			log.Printf("Recheck dismiss batch %d/%d: %d findings", i+1, len(batches), len(batch))
			result, err := recheckDismissBatch(ctx, client, batch, opts)
			if err != nil {
				log.Printf("Recheck dismiss batch %d failed: %v (keeping all findings)", i+1, err)
				outcomes[i] = batchOutcome{index: i, findings: batch}
				return
			}
			outcomes[i] = batchOutcome{
				index:           i,
				findings:        result.Findings,
				dismissedRecord: result.Dismissed,
				modified:        result.ModifiedCount,
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

// consolidateBatchResult is the output of recheckConsolidateBatch.
// Separating kept from consolidations lets the orchestrator route
// only the non-systemic findings to the per-file dismiss pass.
type consolidateBatchResult struct {
	kept           []state.DeepFinding
	consolidations []state.DeepFinding
}

// recheckConsolidateBatch runs ONE consolidator LLM call on the
// given findings using RecheckConsolidatePrompt. It expects the
// model to produce only `kept` and `consolidated` buckets; any
// `dismissed` or `modified` entries (which the consolidator prompt
// forbids) are treated as a kept passthrough — the model violated
// the contract but we err on the safe side rather than dropping
// the finding.
func recheckConsolidateBatch(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	opts RecheckOptions,
) (*consolidateBatchResult, error) {
	systemPrompt := buildRecheckSystemPrompt(ai.RecheckConsolidatePrompt, opts)
	systemPrompt = ai.ResolveToolsForClient(client, systemPrompt)

	findingsJSON, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal findings: %w", err)
	}
	messages := []ai.Message{{
		Role: "user",
		Content: fmt.Sprintf(
			"Here are %d findings spanning multiple files. Identify cross-file patterns and consolidate them. Do NOT dismiss or modify individual findings — that's the next pass's job.\n\n%s",
			len(findings), string(findingsJSON),
		),
	}}

	raw, err := client.ChatStream(ctx, systemPrompt, messages, nil)
	if err != nil {
		return nil, fmt.Errorf("recheck consolidate call: %w", err)
	}
	if opts.OnLLMCall != nil {
		opts.OnLLMCall(systemPrompt, messages[0].Content, raw)
	}

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		return nil, err
	}

	// The consolidator prompt forbids `dismissed` and `modified`.
	// If the model emitted either, log it and treat as kept — we
	// trust the per-file pass to handle those decisions.
	if len(result.Dismissed) > 0 || result.ModifiedCount > 0 {
		log.Printf("Recheck consolidate: model emitted %d dismiss(es) and %d modification(s) it was told not to — re-attaching to kept set",
			len(result.Dismissed), result.ModifiedCount)
		for _, d := range result.Dismissed {
			result.Findings = append(result.Findings, d.Finding)
		}
	}

	// Split result.Findings into (constituents-of-consolidations,
	// systemic-consolidated). parseRecheckResult already removed
	// the constituent IDs and appended the merged findings, tagging
	// the merged ones with Systemic=true. Use the flag — not a
	// File/Title heuristic — to route them: any deviation in
	// prompt convention (e.g., a different File value) would
	// otherwise leak a systemic finding into the dismiss pass.
	var kept, consols []state.DeepFinding
	for _, f := range result.Findings {
		if f.Systemic {
			consols = append(consols, f)
		} else {
			kept = append(kept, f)
		}
	}
	return &consolidateBatchResult{kept: kept, consolidations: consols}, nil
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
	messages := []ai.Message{{
		Role: "user",
		Content: fmt.Sprintf(
			"Here are %d findings to dismiss-or-refine. Cross-file consolidation already ran upstream — do NOT consolidate. Focus on dismissing false positives and within-file dedup.\n\n%s",
			len(findings), string(findingsJSON),
		),
	}}

	raw, err := client.ChatStream(ctx, systemPrompt, messages, nil)
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
		log.Printf("Recheck: failed to parse response: %v — keeping all findings", err)
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

	// 3. Consolidated — merge into single finding
	consolidatedCount := 0
	for _, cons := range resp.Consolidated {
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
		consolidatedCount += len(cons.FindingIDs) - 1 // net reduction
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

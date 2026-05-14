package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// defaultHierarchicalMaxConcurrency caps parallel per-category synthesis
// calls. SetHierarchicalSynthConcurrency overrides this for the lifetime
// of the process.
const defaultHierarchicalMaxConcurrency = 5

var hierarchicalMaxConcurrency = defaultHierarchicalMaxConcurrency

// SetHierarchicalSynthConcurrency sets the max number of per-category
// synthesis calls run in parallel. Values <= 0 reset to the default.
// Not safe to call concurrently with synthesis in flight.
func SetHierarchicalSynthConcurrency(n int) {
	if n <= 0 {
		hierarchicalMaxConcurrency = defaultHierarchicalMaxConcurrency
		return
	}
	hierarchicalMaxConcurrency = n
}

// SynthesisResult holds the output of Phase 4 synthesis.
type SynthesisResult struct {
	// ExecutiveSummary is a 2-3 paragraph overview of audit findings.
	ExecutiveSummary string `json:"executive_summary"`

	// TopRisks are the most critical issues identified, ranked.
	TopRisks []string `json:"top_risks"`

	// SystemicPatterns are recurring issues across the codebase.
	SystemicPatterns []string `json:"systemic_patterns"`

	// Recommendations are prioritized action items.
	Recommendations []string `json:"recommendations"`

	// RawOutput is the full LLM response.
	RawOutput string `json:"-"`
}

// hierarchicalThreshold is the number of findings above which we split
// synthesis into per-category passes followed by a final merge.
const hierarchicalThreshold = 50

// EstimateSynthesisChars returns the expected total output character
// count for synthesis given the finding count. Used to drive a
// truthful streaming progress bar.
//
// The estimate is bounded:
//   - 3000 chars minimum (clean audit: executive_summary + 1-2 risks)
//   - 10000 chars maximum (lots of findings hit the prompt's per-item
//     word caps, so output stops growing past a point)
//
// 100 chars per finding is the slope: each finding contributes ~1
// short risk/pattern/recommendation entry on average (≤100 words ≈
// 400 chars per entry, but only a fraction of findings generate one).
//
// The estimate intentionally errs HIGH so the streaming bar fills
// slower than reality — better to see a bar that holds at 95% for a
// moment than to claim 100% before the response actually ends.
func EstimateSynthesisChars(findingCount int) int {
	est := 3000 + 100*findingCount
	if est > 10000 {
		return 10000
	}
	return est
}

// Synthesize runs Phase 4: takes all findings and produces an executive summary.
// onToken is called for streaming output (can be nil).
//
// failedAOICount is the number of AOIs whose Phase 3 review failed
// (so they have no finding/dismissal verdict). When > 0, synthesis is
// told to mention the recall gap in the executive summary — otherwise
// the user reads a confident summary on top of degraded inputs and
// has no way to know.
func Synthesize(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	crossCutting []string,
	projectContext string,
	failedAOICount int,
	onToken func(string),
) (*SynthesisResult, error) {
	if len(findings) == 0 {
		summary := "No findings were identified during the audit."
		if failedAOICount > 0 {
			// Don't claim "clean audit" when N AOIs never got reviewed.
			// This is the most user-misleading case: an empty findings
			// list could mean "code is fine" OR "we failed to review
			// most of it". Make the difference visible.
			summary = fmt.Sprintf(
				"No findings were identified during the audit, but %d area(s) of interest had failed deep reviews — recall is degraded. See the audit log for failed AOI IDs.",
				failedAOICount)
		}
		return &SynthesisResult{ExecutiveSummary: summary}, nil
	}

	if len(findings) > hierarchicalThreshold {
		return synthesizeHierarchical(ctx, client, findings, crossCutting, projectContext, failedAOICount, onToken)
	}

	return synthesizeDirect(ctx, client, findings, crossCutting, projectContext, failedAOICount, onToken)
}

// SynthesizeCached wraps Synthesize with persistent cache lookup against the
// audit state. Cache misses run synthesis and persist the result. Cache hits
// are returned without an LLM call. Pass noCache=true to bypass.
//
// The audit state is loaded and (if updated) saved by this function — callers
// don't need to manage it.
func SynthesizeCached(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	crossCutting []string,
	projectContext string,
	failedAOICount int,
	onToken func(string),
	noCache bool,
) (*SynthesisResult, error) {
	if len(findings) == 0 {
		return Synthesize(ctx, client, findings, crossCutting, projectContext, failedAOICount, onToken)
	}

	// Include failedAOICount in the cache key. A re-run that resolves
	// the transient errors (failedAOICount drops to zero) should NOT
	// return the previous "recall degraded" synthesis from cache —
	// the new run has different coverage so the summary is different.
	key := computeSynthesisCacheKey(findings, crossCutting, projectContext, failedAOICount)

	auditState, loadErr := state.Load("audit")
	if loadErr != nil {
		// Cache disabled — run synthesis as usual.
		return Synthesize(ctx, client, findings, crossCutting, projectContext, failedAOICount, onToken)
	}

	if !noCache {
		if raw := auditState.GetSynthesisCache(key); raw != nil {
			var cached SynthesisResult
			if err := json.Unmarshal(raw, &cached); err == nil {
				return &cached, nil
			}
			// Corrupt entry — fall through and regenerate.
		}
	}

	result, err := Synthesize(ctx, client, findings, crossCutting, projectContext, failedAOICount, onToken)
	if err != nil {
		return nil, err
	}

	// Persist on success.
	if raw, marshalErr := json.Marshal(result); marshalErr == nil {
		auditState.SetSynthesisCache(key, raw)
		if saveErr := state.Save(auditState); saveErr != nil {
			// Non-fatal — we have the result, just won't be cached next time.
			_ = saveErr
		}
	}

	return result, nil
}

// errSynthesisParse marks parse-shape failures (no JSON object, bad
// JSON unmarshal). Distinguished from transport errors so retry can
// short-circuit — re-running the same prompt against a model emitting
// prose won't fix it, just doubles the token spend on the most
// expensive single call in the pipeline.
var errSynthesisParse = errors.New("synthesis: parse failure")

// synthesisRetryBackoff is the wait before the single retry of a
// transient synthesis failure. Matches deep review's 1.5s — synthesis
// uses the strong model with a large input prompt, so when it fails
// the cause is more often rate-limiting (which needs longer to
// recover) than a brief disconnect.
const synthesisRetryBackoff = 1500 * time.Millisecond

// hierarchicalPartialFloor is the minimum fraction of per-category
// syntheses that must succeed for the hierarchical merge to proceed.
// Previously a single category failure aborted the whole synthesis,
// throwing away successful work on the other N-1 categories. 50%
// strikes a balance: a tiny dip is tolerated, a structural breakdown
// still aborts.
const hierarchicalPartialFloor = 0.5

// runSynthesisChatStreamWithRetry wraps a single ChatStream call with
// one retry after synthesisRetryBackoff for transient errors. Parse
// failures (errSynthesisParse) and context cancellation short-circuit
// immediately — neither benefits from retry.
//
// Returns the raw LLM response on success. The caller still parses
// the response separately so parse-error wrapping happens close to
// the parse site (not buried under a retry helper).
func runSynthesisChatStreamWithRetry(
	ctx context.Context,
	client ai.Client,
	systemPrompt string,
	messages []ai.Message,
	onToken func(string),
	label string,
) (string, error) {
	raw, err := client.ChatStream(ctx, systemPrompt, messages, onToken)
	if err == nil {
		return raw, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}

	select {
	case <-time.After(synthesisRetryBackoff):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	log.Printf("synthesis: retrying %s after transient error: %v", label, err)
	return client.ChatStream(ctx, systemPrompt, messages, onToken)
}

// synthesizeDirect sends all findings in a single LLM call.
func synthesizeDirect(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	crossCutting []string,
	projectContext string,
	failedAOICount int,
	onToken func(string),
) (*SynthesisResult, error) {
	userMsg := BuildSynthesisUserMessage(findings, crossCutting, projectContext, failedAOICount)

	messages := []ai.Message{
		{Role: "user", Content: userMsg},
	}

	raw, err := runSynthesisChatStreamWithRetry(ctx, client, ai.AuditSynthesisPrompt, messages, onToken, "direct call")
	if err != nil {
		return nil, fmt.Errorf("synthesis LLM call: %w", err)
	}

	result, err := ParseSynthesisResult(raw)
	if err != nil {
		return nil, err
	}
	result.RawOutput = raw
	return result, nil
}

// synthesizeHierarchical splits findings by category, synthesizes each
// category separately, then merges the category summaries into a final result.
func synthesizeHierarchical(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	crossCutting []string,
	projectContext string,
	failedAOICount int,
	onToken func(string),
) (*SynthesisResult, error) {
	// Group findings by category.
	byCategory := make(map[string][]state.DeepFinding)
	for _, f := range findings {
		cat := f.Category
		if cat == "" {
			cat = "uncategorized"
		}
		byCategory[cat] = append(byCategory[cat], f)
	}

	// Synthesize each category in parallel.
	type catResult struct {
		category string
		count    int
		summary  string
		err      error
	}

	// Build a stable, sorted list of categories so the merge prompt has a
	// deterministic order regardless of map iteration randomization.
	cats := make([]string, 0, len(byCategory))
	for cat := range byCategory {
		cats = append(cats, cat)
	}
	sort.Strings(cats)

	results := make([]catResult, len(cats))
	sem := make(chan struct{}, hierarchicalMaxConcurrency)
	var wg sync.WaitGroup

	for i, cat := range cats {
		wg.Add(1)
		go func(i int, cat string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = catResult{category: cat, err: ctx.Err()}
				return
			}
			catFindings := byCategory[cat]
			// Per-category calls don't carry the recall-gap line —
			// that's a property of the audit as a whole, surfaced
			// once in the final merge below.
			//
			// Pass the SAME onToken through so streamed chars from
			// every per-category call (and the final merge) contribute
			// to the shared progress bar. Without this, the bar would
			// sit at 0 through the parallel per-category phase and
			// only move during the merge.
			r, err := synthesizeDirect(ctx, client, catFindings, nil, projectContext, 0, onToken)
			if err != nil {
				results[i] = catResult{category: cat, count: len(catFindings), err: err}
				return
			}
			results[i] = catResult{
				category: cat,
				count:    len(catFindings),
				summary:  r.ExecutiveSummary,
			}
		}(i, cat)
	}
	wg.Wait()

	// Partial-failure tolerance: previously a single category-synthesis
	// error aborted the whole hierarchical run, throwing away successful
	// work on the other N-1 categories. Now we collect successes and
	// errors separately, and only abort if too few categories survived
	// for the merge to be meaningful.
	var (
		categorySummaries []string
		failedCategories  []string
	)
	for _, r := range results {
		if r.err != nil {
			log.Printf("synthesis: category %q failed (%d findings): %v — proceeding without it",
				r.category, r.count, r.err)
			failedCategories = append(failedCategories, r.category)
			continue
		}
		categorySummaries = append(categorySummaries,
			fmt.Sprintf("## Category: %s (%d findings)\n%s", r.category, r.count, r.summary))
	}

	if len(cats) == 0 {
		// Shouldn't happen — we entered hierarchical because we had
		// >50 findings, which means at least one category — but guard
		// anyway so we don't divide by zero below.
		return nil, fmt.Errorf("hierarchical synthesis: no categories to merge")
	}
	survivalRatio := float64(len(categorySummaries)) / float64(len(cats))
	if survivalRatio < hierarchicalPartialFloor {
		return nil, fmt.Errorf("hierarchical synthesis aborted: %d/%d categories failed (>%.0f%% threshold); failed: %v",
			len(failedCategories), len(cats), (1-hierarchicalPartialFloor)*100, failedCategories)
	}

	// Final merge: use category summaries as input.
	mergeInput := fmt.Sprintf("The following are per-category summaries from a large audit with %d total findings.\n\n%s",
		len(findings), strings.Join(categorySummaries, "\n\n"))

	if len(failedCategories) > 0 {
		// Tell the merging model about the gap so the executive
		// summary can mention degraded coverage in those categories
		// rather than confidently summarizing complete results.
		mergeInput += fmt.Sprintf("\n\n## Note\n%d category synthesis call(s) failed and are NOT represented above: %s. Mention this gap in the executive summary so readers know coverage was degraded for these areas.",
			len(failedCategories), strings.Join(failedCategories, ", "))
	}

	if failedAOICount > 0 {
		// Separate gap from above: upstream Phase 3 reviews failed
		// for N AOIs, so the findings list itself is incomplete (not
		// just the per-category summaries). Surface this in the
		// merge prompt so the executive summary can mention it.
		mergeInput += fmt.Sprintf("\n\n## Audit Recall Gap\n%d area(s) of interest had failed Phase 3 reviews and produced no finding or dismissal. They are NOT in the findings above. Mention this gap explicitly in the executive summary so readers know the audit's recall was degraded.",
			failedAOICount)
	}

	if len(crossCutting) > 0 {
		mergeInput += "\n\n## Cross-Cutting Observations\n- " + strings.Join(crossCutting, "\n- ")
	}

	messages := []ai.Message{
		{Role: "user", Content: mergeInput},
	}

	raw, err := runSynthesisChatStreamWithRetry(ctx, client, ai.AuditSynthesisPrompt, messages, onToken, "hierarchical merge")
	if err != nil {
		return nil, fmt.Errorf("final synthesis merge: %w", err)
	}

	result, err := ParseSynthesisResult(raw)
	if err != nil {
		return nil, err
	}
	result.RawOutput = raw
	return result, nil
}

// BuildSynthesisUserMessage formats findings and context into the user message
// sent to the LLM for synthesis.
//
// failedAOICount > 0 appends a "## Audit Recall Gap" section asking
// the model to mention the gap in the executive summary. Synthesis
// otherwise can produce a confident-sounding summary on top of
// degraded inputs with no way for the reader to know.
func BuildSynthesisUserMessage(findings []state.DeepFinding, crossCutting []string, projectContext string, failedAOICount int) string {
	var sb strings.Builder

	if projectContext != "" {
		sb.WriteString("## Project Context\n")
		sb.WriteString(projectContext)
		sb.WriteString("\n\n")
	}

	// Group by severity for structured presentation.
	bySeverity := map[string][]state.DeepFinding{}
	severityOrder := []string{"critical", "high", "medium", "low", "nit"}
	for _, f := range findings {
		sev := f.Severity
		if sev == "" {
			sev = "low"
		}
		bySeverity[sev] = append(bySeverity[sev], f)
	}

	sb.WriteString(fmt.Sprintf("## Audit Findings (%d total)\n\n", len(findings)))

	for _, sev := range severityOrder {
		group := bySeverity[sev]
		if len(group) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("### %s (%d)\n", strings.ToUpper(sev), len(group)))
		for _, f := range group {
			sb.WriteString(fmt.Sprintf("- **%s** [%s/%s] %s:%s — %s\n",
				f.Title, f.Category, f.Subcategory, f.File, f.Lines, f.Description))
		}
		sb.WriteString("\n")
	}

	if len(crossCutting) > 0 {
		sb.WriteString("## Cross-Cutting Observations\n")
		for _, obs := range crossCutting {
			sb.WriteString("- " + obs + "\n")
		}
		sb.WriteString("\n")
	}

	if failedAOICount > 0 {
		sb.WriteString("## Audit Recall Gap\n")
		sb.WriteString(fmt.Sprintf("%d area(s) of interest had failed Phase 3 reviews and produced no finding or dismissal. They are NOT in the findings above — the audit's recall is degraded. Mention this gap explicitly in the executive summary so readers know coverage is incomplete.\n\n",
			failedAOICount))
	}

	sb.WriteString("Produce the JSON executive summary now.")
	return sb.String()
}

// ParseSynthesisResult extracts a SynthesisResult from the LLM's raw response.
// Uses ai.ExtractJSON so trailing prose, embedded fences, and multi-round
// agent output don't break parsing.
//
// Returns errSynthesisParse-wrapped errors on parse-shape failures so
// callers (and the retry wrapper) can distinguish "model output was
// malformed" from "transport error" — the former won't be fixed by
// retrying the same prompt.
func ParseSynthesisResult(raw string) (*SynthesisResult, error) {
	s := ai.ExtractJSON(raw)
	if s == "" {
		return nil, fmt.Errorf("%w: no JSON object found in synthesis response", errSynthesisParse)
	}

	var result SynthesisResult
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil, fmt.Errorf("%w: %v", errSynthesisParse, err)
	}

	return &result, nil
}

// NeedsHierarchical reports whether the given finding count exceeds the
// hierarchical synthesis threshold.
func NeedsHierarchical(findingCount int) bool {
	return findingCount > hierarchicalThreshold
}

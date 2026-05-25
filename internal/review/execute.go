package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"context"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/bugpriors"
	"github.com/andreujuanc/prr/internal/state"
)

// ExecuteOptions configures RunReviewCalls.
type ExecuteOptions struct {
	// Mode is "pr" or "audit".
	Mode Mode

	// ProjectContext is the discovered project summary.
	ProjectContext string

	// PRMeta is the PR-level metadata string (title, body, files list,
	// CI status etc.) built by BuildPRMeta. Only the fallback-batch
	// review path reads it — AOI-driven prompts get their PR context
	// via different sections of the AOI prompt builder. Empty in audit
	// mode (no PR).
	PRMeta string

	// RuntimeModel is the discovered structured codebase shape from
	// Phase 0.5. When non-nil, the runtime model is rendered into a
	// `## Runtime Model` section of every Phase 3 prompt so the
	// reviewer can ground findings in the project's actual entry
	// points, validation sites, and error discipline. Nil is fine —
	// the section is simply omitted.
	RuntimeModel *state.RuntimeModel

	// CustomInstructions from user config.
	CustomInstructions string

	// FocusCategories filters which AOIs are reviewed (nil = all).
	FocusCategories []string

	// BugPriors is the rendered bug-priors prompt section produced by
	// internal/bugpriors.Extract. When non-empty, it's spliced into
	// the deep-review prompt and folded into the cache key (so the
	// arrival of new fix commits invalidates stale entries). Empty
	// string == feature disabled.
	BugPriors string

	// MaxConcurrency caps parallel review calls (default 10).
	MaxConcurrency int

	// NoCache disables reading from cache.
	NoCache bool

	// RepoRoot is the absolute path to the repository root. The
	// in-loop evidence verifier needs it to resolve each finding's
	// File field against the on-disk source. When empty, evidence
	// verification is skipped (and findings flow through unfiltered)
	// because we have no way to check the snippet against anything.
	RepoRoot string

	// SkipEvidenceVerify disables the in-loop evidence verification
	// pass even when RepoRoot is set. Wired through for tests and
	// for situations where the source tree isn't available (e.g.
	// reviewing a PR from a fork without a checkout). Production
	// pipelines should leave this false.
	SkipEvidenceVerify bool

	// CacheGet retrieves a cached DeepReviewResult by key. Can be nil.
	CacheGet func(key string) *state.DeepReviewResult

	// CacheSet stores a DeepReviewResult by key. Can be nil.
	CacheSet func(key string, result *state.DeepReviewResult)

	// OnProgress is called with status updates. Can be nil.
	OnProgress func(completed, total int, cached bool, err error)

	// OnCallStart fires when a non-cached call enters its LLM phase
	// (i.e., after the semaphore is acquired, just before ChatStream).
	// Cached calls do not fire this — they return before semaphore
	// acquisition. The Batches panel uses it to flip a batch's status
	// from queued to active and start the elapsed-time clock.
	OnCallStart func(index int)

	// OnCallEnd fires for every call (cached or not) with its final
	// outcome. completed/total order is preserved by OnProgress, but
	// OnCallEnd carries the original call index so the Batches panel
	// can address the specific batch row. cached and err mirror
	// OnProgress. findings is the count produced by this call (0 on
	// err); the panel surfaces it per-row and the phase summary sums
	// it across calls.
	OnCallEnd func(index int, cached bool, err error, findings int)

	// OnCallStream fires (throttled) as bytes of plain content stream
	// in from the LLM for a given call. tokenBytes is cumulative bytes
	// received so far for this call. Used by the Batches panel to draw
	// a per-batch progress bar from a real signal instead of a
	// timer-only spinner.
	OnCallStream func(index int, tokenBytes int)

	// OnLLMCall is called before and after each LLM call for debugging.
	// Called with (callIndex, ReviewCall, systemPrompt, userMessage, response).
	// If nil, no debug output is produced.
	OnLLMCall func(index int, call ReviewCall, systemPrompt string, userMsg string, response string)

	// OnToolCall is called for each tool invocation during Phase 3 review.
	// Called with (callIndex, toolName, args, status, duration).
	// If nil, tool events are silently discarded.
	OnToolCall func(callIndex int, toolName string, args string, status string, duration string)
}

// ExecuteResult holds the aggregate output of RunReviewCalls.
type ExecuteResult struct {
	Findings []state.DeepFinding

	// Dismissals carries the full per-AOI dismissal record (file +
	// confidence + rationale), not just a count. Per-file coverage
	// instrumentation downstream relies on the file attribution.
	// Callers that only want the count should use DismissalCount().
	Dismissals []state.DeepDismissal

	CrossCutting []string
	// Failed is the count of review calls that errored. The caller should
	// surface this — failed calls drop their AOIs from the result.
	Failed int
	// FailedAOIIDs is the list of AOI IDs whose review call failed (and
	// therefore produced no finding or dismissal verdict). Synthesis
	// and reporting can use this to tell the user WHICH areas of the
	// codebase lost their deep review attention — previously this info
	// only existed implicitly in "N reviews failed" with no path back
	// to the affected AOIs.
	FailedAOIIDs []string
}

// DismissalCount returns the number of dismissed AOIs, replacing
// the old ExecuteResult.Dismissals int field.
func (r ExecuteResult) DismissalCount() int { return len(r.Dismissals) }

// RunReviewCalls executes all review calls concurrently with bounded concurrency.
// This is the shared pipeline for both PR review and audit modes.
func RunReviewCalls(
	ctx context.Context,
	client ai.Client,
	calls []ReviewCall,
	opts ExecuteOptions,
) (*ExecuteResult, error) {
	if len(calls) == 0 {
		return &ExecuteResult{}, nil
	}

	maxConc := opts.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 10
	}

	// Context caching is currently disabled at the call site.
	//
	// The provider plumbing (CreateContextCache / DeleteContextCache on
	// GeminiProvider, ChatRequest.CachedContent, the CacheSupport
	// interface) and the setupContextCache helper are all kept — they
	// pass their unit tests and the live verification test in
	// internal/ai/gemini_cache_test.go. The reason for keeping the call
	// turned off is that Gemini rejects requests that combine
	// cachedContent with system_instruction in the same generateContent
	// body ("CachedContent can not be used with GenerateContent request
	// setting system_instruction, tools or tool_config"). The MVP path
	// that cached only tools therefore stripped the review system prompt
	// from each call, which made the model produce prose instead of JSON
	// and crashed every parser (see plans/benchmark-results-2026-05-20.md
	// §3f for the failing run).
	//
	// To re-enable, the review prompt builders need to split into
	// (cacheable static prefix, per-AOI variable suffix) so the prefix
	// can live inside the cache as the systemInstruction and the suffix
	// can go in the user message. Tracked in
	// plans/review-cost-and-routing-tuning.md.
	_ = setupContextCache

	type callResult struct {
		index     int
		result    *state.DeepReviewResult
		err       error
		fromCache bool
	}

	resultsCh := make(chan callResult, len(calls))
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(i int, call ReviewCall) {
			defer wg.Done()

			// Check cache (individual calls only — grouped calls have unstable
			// cache keys because the group composition changes when any member
			// file is modified, orphaning the old cache entry).
			cacheKey := ComputeCacheKey(call, opts.FocusCategories, bugpriors.Hash(opts.BugPriors))
			if !opts.NoCache && opts.CacheGet != nil && call.Type == "individual" {
				if cached := opts.CacheGet(cacheKey); cached != nil {
					resultsCh <- callResult{index: i, result: cached, fromCache: true}
					return
				}
			}

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				resultsCh <- callResult{index: i, err: ctx.Err()}
				return
			}

			if opts.OnCallStart != nil {
				opts.OnCallStart(i)
			}

			result, err := runReviewCallWithRetry(ctx, client, call, opts, i)
			if err != nil {
				resultsCh <- callResult{index: i, err: err}
				return
			}
			result.CacheKey = cacheKey

			// Cache the result — individual calls only AND only on
			// parse success (i.e. err == nil above). Previously a
			// parse failure produced an empty result which was then
			// written to the cache, poisoning that AOI's slot: every
			// future run would hit "cached: no findings" until the
			// user wiped the cache or used --no-cache. With the new
			// retry+error path, only validated results land here.
			if opts.CacheSet != nil && call.Type == "individual" {
				opts.CacheSet(cacheKey, result)
			}

			resultsCh <- callResult{index: i, result: result}
		}(i, call)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	// Collect results
	execResult := &ExecuteResult{}
	completed := 0
	for cr := range resultsCh {
		completed++
		if cr.err != nil {
			log.Printf("Review call %d failed: %v", cr.index+1, cr.err)
			execResult.Failed++
			// Track which AOI IDs were in the failed call. Synthesis
			// and reports can then tell the user precisely which
			// areas of the codebase didn't get reviewed — instead of
			// just "N reviews failed" with no recovery path.
			for _, aoi := range calls[cr.index].AOIs {
				if aoi.ID != "" {
					execResult.FailedAOIIDs = append(execResult.FailedAOIIDs, aoi.ID)
				}
			}
			if opts.OnProgress != nil {
				opts.OnProgress(completed, len(calls), cr.fromCache, cr.err)
			}
			if opts.OnCallEnd != nil {
				opts.OnCallEnd(cr.index, cr.fromCache, cr.err, 0)
			}
			continue
		}
		findings := 0
		if cr.result != nil {
			findings = len(cr.result.Findings)
			execResult.Findings = append(execResult.Findings, cr.result.Findings...)
			execResult.Dismissals = append(execResult.Dismissals, cr.result.Dismissals...)
			if cr.result.CrossCutting != "" {
				execResult.CrossCutting = append(execResult.CrossCutting, cr.result.CrossCutting)
			}
		}
		if opts.OnProgress != nil {
			opts.OnProgress(completed, len(calls), cr.fromCache, nil)
		}
		if opts.OnCallEnd != nil {
			opts.OnCallEnd(cr.index, cr.fromCache, nil, findings)
		}
	}

	// Sort findings by severity (critical first)
	sort.Slice(execResult.Findings, func(i, j int) bool {
		return severityRank(execResult.Findings[i].Severity) < severityRank(execResult.Findings[j].Severity)
	})

	// All-failed: even below the 2-call floor, a single-call run that
	// fails has nothing useful to return. Keep parity with the AOI
	// scanner which treats this as an unconditional abort.
	if execResult.Failed == len(calls) && len(calls) > 0 {
		return execResult, fmt.Errorf("all %d deep review call(s) failed; %d AOI(s) had no review",
			len(calls), len(execResult.FailedAOIIDs))
	}

	// Aggregate-fail: too many calls failed for the audit's recall to
	// be trustworthy. Surface this as an error instead of silently
	// returning a partial result that looks complete.
	if shouldAggregateFailReview(execResult.Failed, len(calls)) {
		return execResult, fmt.Errorf(
			"phase 3: %d/%d review call(s) failed (>%.0f%% threshold) — aborting; %d AOI(s) had no deep review",
			execResult.Failed, len(calls), reviewAggregateFailRatio*100,
			len(execResult.FailedAOIIDs))
	}

	return execResult, nil
}

// Aggregate-fail thresholds for Phase 3 deep review.
//
// Phase 3 previously had no fail-fast at all — RunReviewCalls always
// returned success regardless of how many calls failed. An audit with
// 8 of 10 calls failing would still report "found N findings" as if
// it were complete. Same pattern as Phase 1 file load and Phase 2 AOI:
// >20% failures with a 2-call floor aborts the run.
const (
	reviewAggregateFailRatio    = 0.20
	reviewAggregateFailMinCalls = 2
)

// shouldAggregateFailReview reports whether the (failed, total) call
// counts cross the abort threshold for deep review.
func shouldAggregateFailReview(failed, total int) bool {
	if failed < reviewAggregateFailMinCalls {
		return false
	}
	if total <= 0 {
		return false
	}
	return float64(failed)/float64(total) > reviewAggregateFailRatio
}

// reviewRetryBackoff is the wait before the single retry of a transient
// deep-review failure. Larger than AOI's 1s and classify's 750ms because
// deep reviews run the strong model with tool use, generating big
// outputs over 30-60s — when they fail, the cause is more often rate-
// limiting (which needs longer to recover) than a brief disconnect.
const reviewRetryBackoff = 1500 * time.Millisecond

// runReviewCallWithRetry executes one deep review call and retries once
// on any non-cancellation error after reviewRetryBackoff. That covers
// both transient API errors (5xx, network blips) and parse-shape
// failures (errReviewParse: prose-only response, off-list category that
// failed json.Unmarshal). Sampling variance is enough that a second
// attempt often produces parseable JSON even with the same prompt.
// Context cancellation still short-circuits — a cancelled context
// can't carry the retry anyway.
//
// Why retry deep reviews at all? They're the most expensive LLM calls
// in the pipeline; losing one to a 503 after 45s of work also loses
// every finding for that AOI (individual) or up to 10 AOIs (grouped).
// One retry catches most transient API hiccups with a small extra-
// latency budget.
func runReviewCallWithRetry(
	ctx context.Context,
	client ai.Client,
	call ReviewCall,
	opts ExecuteOptions,
	callIndex int,
) (*state.DeepReviewResult, error) {
	start := time.Now()
	log.Printf("review: call %d (%s %s/%s) starting (%d AOI)",
		callIndex+1, call.Type, call.Category, call.Subcategory, len(call.AOIs))

	result, err := doReviewCall(ctx, client, call, opts, callIndex)
	if err == nil {
		log.Printf("review: call %d (%s %s/%s) completed in %v",
			callIndex+1, call.Type, call.Category, call.Subcategory,
			time.Since(start).Round(time.Millisecond))
		return result, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}

	select {
	case <-time.After(reviewRetryBackoff):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	reason := "transient error"
	if errors.Is(err, errReviewParse) {
		reason = "parse failure"
	}
	log.Printf("review: retrying call %d (%s %s/%s) after %s: %v",
		callIndex+1, call.Type, call.Category, call.Subcategory, reason, err)
	result, err = doReviewCall(ctx, client, call, opts, callIndex)
	if err == nil {
		log.Printf("review: call %d (%s %s/%s) completed in %v (retry succeeded)",
			callIndex+1, call.Type, call.Category, call.Subcategory,
			time.Since(start).Round(time.Millisecond))
	}
	return result, err
}

// doReviewCall executes one review call end-to-end: builds the prompt
// (with {{TOOLS}} resolved so the OnLLMCall debug hook sees the same
// text the model does), invokes the LLM, parses the response. Returns
// a populated DeepReviewResult on success or an error — wrapped with
// errReviewParse for response-shape failures so the caller can
// short-circuit retry and skip the cache write.
func doReviewCall(
	ctx context.Context,
	client ai.Client,
	call ReviewCall,
	opts ExecuteOptions,
	callIndex int,
) (*state.DeepReviewResult, error) {
	// Build the onToken callback up front so fallback-batch calls can
	// also feed the Batches panel's per-row byte counter.
	onToken := buildCallOnToken(opts, callIndex)

	// Fallback-batch calls use the directory-batch prompt; result is
	// adapted to DeepFinding shape so the rest of the pipeline doesn't
	// branch on origin. See internal/review/fallback.go.
	if call.Type == "fallback-batch" {
		return doFallbackBatchCall(ctx, client, call, opts, callIndex, onToken)
	}

	// Build prompt
	var systemPrompt string
	if call.Type == "individual" {
		systemPrompt = BuildIndividualPrompt(
			opts.Mode, opts.ProjectContext, opts.CustomInstructions, opts.BugPriors, opts.RuntimeModel, call,
		)
	} else {
		systemPrompt = BuildGroupedPrompt(
			opts.Mode, opts.ProjectContext, opts.CustomInstructions, opts.BugPriors, opts.RuntimeModel, call,
		)
	}

	messages := []ai.Message{
		{Role: "user", Content: userMessage(opts.Mode)},
	}

	// Resolve {{TOOLS}} before ChatStream so the debug hook sees the
	// same text the LLM sees. Agent.ChatStream resolves internally on
	// a local copy — without this, OnLLMCall would get the unresolved
	// placeholder while the model gets the resolved version.
	systemPrompt = ai.ResolveToolsForClient(client, systemPrompt)

	raw, callErr := client.ChatStream(ctx, systemPrompt, messages, onToken)
	if callErr != nil {
		return nil, callErr
	}

	if opts.OnLLMCall != nil {
		opts.OnLLMCall(callIndex, call, systemPrompt, messages[0].Content, raw)
	}

	rawResult, parseErr := parseDeepReviewRaw(call, raw)
	if parseErr != nil {
		return &state.DeepReviewResult{
			Type:        call.Type,
			Category:    call.Category,
			Subcategory: call.Subcategory,
		}, parseErr
	}

	cc := correctorContext{
		client:           client,
		call:             call,
		callIndex:        callIndex,
		systemPrompt:     systemPrompt,
		originalMessages: messages,
		originalRaw:      raw,
	}

	// Category corrector: up to maxCategoryCorrectorAttempts round
	// trips on the same chat thread for any finding whose Category
	// isn't a known slug. Findings still off-list after the loop are
	// dropped by convertRawToTyped.
	rawResult = verifyAndCorrectCategory(ctx, cc, rawResult)

	result, droppedCats := convertRawToTyped(call, rawResult)
	if len(droppedCats) > 0 {
		log.Printf("review: call %d (%s %s/%s) dropped %d finding(s) for off-list categories after corrector",
			callIndex+1, call.Type, call.Category, call.Subcategory, len(droppedCats))
	}
	validateReviewResult(call, result)

	// In-loop evidence verification: every emitted finding carries
	// a verbatim snippet (per the prompts); we match it against the
	// cited file. Mismatches get one corrector round trip in the
	// SAME chat thread, and findings that are still unmatched after
	// that get dropped. This catches the F-042/F-045/F-051 class
	// (hallucinated coordinates / paraphrased "snippets") at the
	// producer, before they hit recheck or the user.
	if !opts.SkipEvidenceVerify && opts.RepoRoot != "" {
		result = verifyAndCorrectEvidence(ctx, cc, opts.RepoRoot, result)
	}

	// Apply confidence penalties for missing required evidence (3-hop
	// trace on critical/high — commit 4 in the audit-quality plan).
	// Severity stays the model's call; only confidence moves.
	if result != nil {
		result.Findings = ApplyConfidencePenalties(result.Findings)
	}

	return result, nil
}

// rawDeepFinding is the on-the-wire form: a state.DeepFinding plus the
// LLM-only fields (Status, DismissedRationale) and a shadowed Category
// as raw string. The shadowing lets JSON unmarshal accept any string —
// validation runs later in convertRawToTyped, after the corrector has
// had its chance.
type rawDeepFinding struct {
	state.DeepFinding

	// Category shadows DeepFinding.Category for JSON parsing. The
	// embedded typed field stays at zero until convertRawToTyped runs
	// ParseCategory and assigns the validated value.
	Category string `json:"category"`

	// LLM-response-only fields not part of DeepFinding.
	Status             string `json:"status"`
	DismissedRationale string `json:"dismissed_rationale"`
}

// rawDeepReviewResult embeds state.DeepReviewResult and shadows its
// Findings field with the raw-category-tolerant rawDeepFinding type.
// Every other field (Type, Category, Subcategory, CrossCutting,
// Dismissals, RawOutput) is reused directly from the embedded struct.
type rawDeepReviewResult struct {
	state.DeepReviewResult

	// Findings shadows DeepReviewResult.Findings so JSON unmarshal
	// accepts off-list categories on individual findings.
	Findings []rawDeepFinding
}

// convertRawToTyped promotes raw category strings to state.Category.
// Findings whose category isn't a known slug after the corrector has
// run are dropped here; the returned DroppedFinding slice lets callers
// surface "N dropped" to the user instead of relying on log scraping.
func convertRawToTyped(call ReviewCall, raw *rawDeepReviewResult) (*state.DeepReviewResult, []DroppedFinding) {
	out := raw.DeepReviewResult
	out.Findings = nil
	var dropped []DroppedFinding
	for _, f := range raw.Findings {
		cat, err := state.ParseCategory(f.Category)
		if err != nil {
			log.Printf("review: %s call (%s/%s) dropping finding %q — %v",
				call.Type, call.Category, call.Subcategory, f.Title, err)
			dropped = append(dropped, DroppedFinding{
				Title:  f.Title,
				File:   f.File,
				Reason: err.Error(),
			})
			continue
		}
		typed := f.DeepFinding
		typed.Category = cat
		out.Findings = append(out.Findings, typed)
	}
	return &out, dropped
}

// buildCallOnToken constructs the ChatStream onToken callback for one
// call. ChatStream offers one slot, so this multiplexes two concerns:
// tool-event debug logging (OnToolCall) and per-batch byte counting
// for the Batches panel's progress bar (OnCallStream). Content bytes
// — anything not prefixed with the \x00 control byte — feed the
// per-batch counter. Of the \x00-prefixed control tokens, only
// TOOL_START and TOOL_DONE dispatch to OnToolCall; THOUGHT_ markers
// are intentionally dropped (no consumer wants them and surfacing
// thoughts would dilute the tool-call debug log). Returns nil when
// neither hook is configured.
//
// Extracted out so both the AOI-driven path and the fallback-batch
// path build the same callback shape with no copy/paste drift.
func buildCallOnToken(opts ExecuteOptions, callIndex int) func(string) {
	if opts.OnToolCall == nil && opts.OnCallStream == nil {
		return nil
	}
	var streamBytes int
	var lastEmit int
	return func(tok string) {
		if len(tok) == 0 {
			return
		}
		if tok[0] == 0x00 {
			if opts.OnToolCall == nil {
				return
			}
			if after, ok := strings.CutPrefix(tok, "\x00TOOL_START:"); ok {
				payload := after
				if before, after, ok := strings.Cut(payload, "("); ok {
					name := before
					args := strings.TrimSuffix(after, ")")
					opts.OnToolCall(callIndex, name, args, "start", "")
				}
			} else if after, ok := strings.CutPrefix(tok, "\x00TOOL_DONE:"); ok {
				payload := after
				parts := strings.SplitN(payload, "|", 3)
				if len(parts) == 3 {
					opts.OnToolCall(callIndex, parts[0], "", parts[1], parts[2])
				}
			}
			return
		}
		if opts.OnCallStream != nil {
			streamBytes += len(tok)
			// Throttle: one emit per >=256 bytes received. Same
			// pattern as synthesis streaming — keeps the TUI from
			// re-rendering on every token while still feeling live.
			if streamBytes-lastEmit >= 256 {
				opts.OnCallStream(callIndex, streamBytes)
				lastEmit = streamBytes
			}
		}
	}
}

// correctorContext bundles the call-identity and chat-thread fields
// shared by verifyAndCorrectCategory and verifyAndCorrectEvidence.
// ctx stays out of the struct so it can remain the first parameter
// (Go convention).
type correctorContext struct {
	client           ai.Client
	call             ReviewCall
	callIndex        int
	systemPrompt     string
	originalMessages []ai.Message
	originalRaw      string
}

// verifyAndCorrectEvidence runs the per-finding snippet check, asks
// the model for corrections on any mismatches via one follow-up
// round trip on the same chat thread, and drops findings that are
// still unmatched after the retry. The corrector message tells the
// model exactly which finding indexes failed and what's expected,
// so the response stays small.
//
// All errors are non-fatal: if the corrector call fails, the result
// is returned with the original (potentially-mismatched) findings
// preserved. The audit-trail log line is enough signal — silently
// dropping findings on infrastructure errors would be worse than
// keeping a possibly-mismatched one.
func verifyAndCorrectEvidence(
	ctx context.Context,
	cc correctorContext,
	repoRoot string,
	result *state.DeepReviewResult,
) *state.DeepReviewResult {
	if result == nil || len(result.Findings) == 0 {
		return result
	}

	verdicts := make([]evidenceVerdict, len(result.Findings))
	mismatchedIdx := make([]int, 0, len(result.Findings))
	for i, f := range result.Findings {
		verdicts[i] = verifyEvidence(repoRoot, f)
		if verdicts[i] == evidenceMismatch || verdicts[i] == evidenceFileMissing {
			mismatchedIdx = append(mismatchedIdx, i)
		}
	}
	if len(mismatchedIdx) == 0 {
		return result
	}

	log.Printf("review: call %d (%s %s/%s) has %d/%d finding(s) with unverifiable evidence — requesting corrections",
		cc.callIndex+1, cc.call.Type, cc.call.Category, cc.call.Subcategory,
		len(mismatchedIdx), len(result.Findings))

	// Build a corrector message keyed by the per-call finding index.
	// We use index (not aoi_id or finding_id) because grouped calls
	// can produce multiple findings per AOI and the model hasn't been
	// assigned global F-NNN IDs yet at this point in the pipeline.
	correctorMsg := buildEvidenceCorrectorMessage(result.Findings, verdicts)

	followup := make([]ai.Message, 0, len(cc.originalMessages)+2)
	followup = append(followup, cc.originalMessages...)
	followup = append(followup, ai.Message{Role: "assistant", Content: cc.originalRaw})
	followup = append(followup, ai.Message{Role: "user", Content: correctorMsg})

	correctorRaw, err := cc.client.ChatStream(ctx, cc.systemPrompt, followup, nil)
	if err != nil {
		log.Printf("review: evidence corrector call failed (call %d, non-fatal): %v — dropping unverifiable findings without retry",
			cc.callIndex+1, err)
		return dropFindingsAfterCorrector(result, verdicts, nil)
	}

	corrections := parseEvidenceCorrections(correctorRaw)
	withdrawn := applyEvidenceCorrections(result, corrections)

	// Re-verify the corrected findings. Withdrawn indexes are
	// dropped unconditionally; the rest must pass re-verification.
	for i := range result.Findings {
		if withdrawn[i] {
			// Withdrawn — keep verdict at whatever it was; the drop
			// logic checks `withdrawn` first.
			continue
		}
		verdicts[i] = verifyEvidence(repoRoot, result.Findings[i])
	}
	return dropFindingsAfterCorrector(result, verdicts, withdrawn)
}

// buildEvidenceCorrectorMessage formats the follow-up prompt. The
// model sees an index-by-index list of which findings failed and
// what shape the response should take.
func buildEvidenceCorrectorMessage(findings []state.DeepFinding, verdicts []evidenceVerdict) string {
	var b strings.Builder
	b.WriteString("Some of the findings you just emitted have evidence snippets that do not match the cited file.\n\n")
	b.WriteString("For each indexed finding listed below, either:\n")
	b.WriteString("  (a) re-read the file with read_file and return a CORRECTED snippet (and corrected file/lines if needed), or\n")
	b.WriteString("  (b) withdraw the finding if you cannot anchor it to real code at any location in the file.\n\n")
	b.WriteString("Findings needing correction:\n\n")
	for i, f := range findings {
		switch verdicts[i] {
		case evidenceMismatch:
			fmt.Fprintf(&b, "  - index %d: %s:%s — snippet %q not found within ±10 lines of cited range.\n",
				i, f.File, f.Lines, f.EvidenceSnippet)
		case evidenceFileMissing:
			fmt.Fprintf(&b, "  - index %d: %s — file does not exist on disk.\n", i, f.File)
		}
	}
	b.WriteString("\nReturn ONLY a JSON object with the corrections, no prose:\n\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"corrections\": [\n")
	b.WriteString("    {\"index\": 0, \"withdraw\": false, \"file\": \"path/to/file.go\", \"lines\": \"45-47\", \"evidence_snippet\": \"verbatim line from the file\"},\n")
	b.WriteString("    {\"index\": 1, \"withdraw\": true, \"reason\": \"brief explanation\"}\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")
	b.WriteString("Only include entries for the indexes listed above. Findings not listed are accepted as-is.")
	return b.String()
}

// evidenceCorrection is one entry from the corrector response. Fields
// are pointers/strings so we can tell "not provided" apart from
// "empty" (an empty new snippet means withdrawal in practice).
type evidenceCorrection struct {
	Index           int    `json:"index"`
	Withdraw        bool   `json:"withdraw"`
	File            string `json:"file,omitempty"`
	Lines           string `json:"lines,omitempty"`
	EvidenceSnippet string `json:"evidence_snippet,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// parseEvidenceCorrections extracts the corrector response. On any
// parse failure returns nil — the caller treats nil as "no
// corrections, proceed to drop unverifiable findings", which is the
// safe behavior.
func parseEvidenceCorrections(raw string) []evidenceCorrection {
	extracted, err := extractLastJSONValue([]byte(ai.StripMarkdownFences(raw)))
	if err != nil {
		log.Printf("review: evidence corrector response had no JSON; treating as no corrections")
		return nil
	}
	var resp struct {
		Corrections []evidenceCorrection `json:"corrections"`
	}
	if err := unmarshalLLMResponse(extracted, &resp); err != nil {
		log.Printf("review: failed to parse evidence corrector response: %v — response prefix: %q", err, previewForLog(extracted, 500))
		return nil
	}
	return resp.Corrections
}

// applyEvidenceCorrections mutates result.Findings in place: each
// non-withdrawn correction updates the matching finding's file /
// lines / snippet. Returns a parallel-indexed slice marking which
// findings the model withdrew — those get dropped unconditionally
// at the next step, bypassing re-verification (an explicit "I can't
// anchor this" beats any further machine inspection).
func applyEvidenceCorrections(result *state.DeepReviewResult, corrections []evidenceCorrection) []bool {
	withdrawn := make([]bool, len(result.Findings))
	for _, c := range corrections {
		if c.Index < 0 || c.Index >= len(result.Findings) {
			log.Printf("review: corrector returned out-of-range index %d (have %d findings)",
				c.Index, len(result.Findings))
			continue
		}
		if c.Withdraw {
			withdrawn[c.Index] = true
			continue
		}
		if c.File != "" {
			result.Findings[c.Index].File = c.File
		}
		if c.Lines != "" {
			result.Findings[c.Index].Lines = c.Lines
		}
		if c.EvidenceSnippet != "" {
			result.Findings[c.Index].EvidenceSnippet = c.EvidenceSnippet
		}
	}
	return withdrawn
}

// dropFindingsAfterCorrector filters out findings that were
// withdrawn or that re-verification couldn't anchor. Logs each drop
// so the audit log shows why a finding disappeared without
// surfacing these to the state-level dismissal record (they never
// reached recheck — they're producer-side drops, distinct from
// recheck dismissals which carry an LLM rationale).
func dropFindingsAfterCorrector(result *state.DeepReviewResult, verdicts []evidenceVerdict, withdrawn []bool) *state.DeepReviewResult {
	if result == nil || len(result.Findings) == 0 {
		return result
	}
	kept := result.Findings[:0]
	for i, f := range result.Findings {
		if i < len(withdrawn) && withdrawn[i] {
			log.Printf("review: finding withdrawn by corrector [aoi=%s file=%s lines=%s]",
				f.AOIID, f.File, f.Lines)
			continue
		}
		if v := verdicts[i]; v == evidenceMismatch || v == evidenceFileMissing {
			log.Printf("review: dropping finding [aoi=%s file=%s lines=%s]: evidence snippet not anchored after corrector pass (verdict=%d)",
				f.AOIID, f.File, f.Lines, v)
			continue
		}
		kept = append(kept, f)
	}
	result.Findings = kept
	return result
}

// validateReviewResult surfaces semantic issues in a parsed review
// result that would otherwise propagate silently into synthesis and
// reporting. All issues are logged; the result is NOT mutated. This
// matches the same logging-only pattern as security.validateAOIs —
// the model's actual output is preserved so prompt drift remains
// visible rather than masked.
//
// Surfaced issues:
//
//   - Grouped-call drops: a grouped call asked for N AOIs but the
//     response only addressed M. The missing AOIs end up with no
//     verdict (no finding, no dismissal). Without this log, those
//     AOIs vanish from the audit silently.
//   - Empty AOI ID in a finding: breaks the source_ids linkage
//     synthesis uses to tie findings back to AOIs.
//   - Empty file path or lines: finding cannot be located in the
//     codebase; renders to the user as a floating sentence.
//   - Severity outside the canonical set: severityRank falls through
//     to "4" (last), so an invalid severity quietly buries the
//     finding at the bottom of the sorted list.
func validateReviewResult(call ReviewCall, result *state.DeepReviewResult) {
	if result == nil {
		return
	}

	// Drop detection: grouped calls only. Individual calls have
	// exactly one input AOI by construction, so "dropped" is the
	// same as "parse failed" — already surfaced via errReviewParse.
	if call.Type == "grouped" {
		seen := make(map[string]bool, len(result.Findings)+len(result.Dismissals))
		for _, f := range result.Findings {
			if f.AOIID != "" {
				seen[f.AOIID] = true
			}
		}
		for _, d := range result.Dismissals {
			if d.AOIID != "" {
				seen[d.AOIID] = true
			}
		}
		var dropped []string
		for _, aoi := range call.AOIs {
			if aoi.ID != "" && !seen[aoi.ID] {
				dropped = append(dropped, aoi.ID)
			}
		}
		if len(dropped) > 0 {
			log.Printf("review: grouped call %s/%s dropped %d of %d AOI(s) from response: %v (no verdict — synthesis will be missing these)",
				call.Category, call.Subcategory, len(dropped), len(call.AOIs), dropped)
		}
	}

	// Per-finding field validation. Skip dismissals — they have
	// fewer required fields and a missing one is less load-bearing.
	for _, f := range result.Findings {
		if f.AOIID == "" {
			log.Printf("review: finding without aoi_id in %s/%s (file=%s lines=%s) — cannot link to AOI in synthesis",
				call.Category, call.Subcategory, f.File, f.Lines)
		}
		if f.File == "" {
			log.Printf("review: finding without file path in %s/%s [aoi_id=%s] — cannot locate",
				call.Category, call.Subcategory, f.AOIID)
		}
		if f.Lines == "" {
			log.Printf("review: finding without lines in %s/%s [aoi_id=%s file=%s]",
				call.Category, call.Subcategory, f.AOIID, f.File)
		}
		if !isValidSeverity(f.Severity) {
			log.Printf("review: finding with invalid severity %q in %s/%s [aoi_id=%s] — will sort last in priority list",
				f.Severity, call.Category, call.Subcategory, f.AOIID)
		}
	}
}

// isValidSeverity reports whether s is one of the canonical severity
// strings the reviewer is supposed to emit. Anything else is silently
// sorted to position 4 (last) by severityRank — which buries findings.
//
// The vocabulary must stay in sync with the `severity` enum listed in
// review_individual.md and review_grouped.md. "nit" is included here
// because the prompts offer it as a valid emission; without it, every
// well-formed nit finding would log as an "invalid severity" warning.
func isValidSeverity(s string) bool {
	switch s {
	case "critical", "high", "medium", "low", "nit":
		return true
	}
	return false
}

// ComputeCacheKey returns the cache key for a review call.
//
// priorsHash is sha256 of the bug-priors content for this run (empty
// when --bug-priors is off). Folding it in here means flipping the
// flag or shipping a new fix-commit yields a fresh cache key.
//
// The call's inlined code context (FileDiffs / AOISources) is also
// folded in so that changes to the surrounding diff or source code
// invalidate cached results — without it, an AOI whose line/concern
// is stable could serve stale review verdicts after nearby code
// changed.
func ComputeCacheKey(call ReviewCall, focusCategories []string, priorsHash string) string {
	codeContext := codeContextDigest(call)
	if call.Type == "individual" {
		return IndividualCacheKey(codeContext, call.AOIs[0], focusCategories, priorsHash)
	}
	return GroupedCacheKey(call.AOIs, codeContext, focusCategories, priorsHash)
}

func userMessage(mode Mode) string {
	switch mode {
	case ModePR:
		return "Investigate the area(s) of interest in this PR. Focus on whether the changes introduce new issues. Return your findings as JSON."
	default:
		return "Investigate the area(s) of interest described in the system prompt. Use tools to verify."
	}
}

// errReviewParse marks parse-shape failures (LLM emitted prose, malformed
// JSON, missing JSON delimiter). Distinguished from transport errors so:
//   - retry can short-circuit (re-running the same prompt won't fix it)
//   - the caller knows NOT to cache the result (an empty result cached
//     here would poison the cache for that AOI on every future run)
var errReviewParse = errors.New("review: parse failure")

// ParseDeepReviewResult parses the LLM's response into a DeepReviewResult.
//
// Returns errReviewParse-wrapped errors when the response cannot be
// parsed (no JSON delimiter, malformed JSON). The returned result is
// never nil — even on parse error it carries the call's Type/Category/
// Subcategory so the caller can attribute the failure. Findings and
// Dismissals are populated only on success.
//
// RawOutput is never the raw LLM string. The field's type is
// json.RawMessage, whose MarshalJSON validates bytes as JSON — assigning
// fenced/prose output (e.g. "```json\n{...}\n```") would make every
// future state.Save fail and silently drop the user's findings.
// We keep RawOutput nil until parsing succeeds, then store the cleaned
// JSON we actually unmarshaled.
func ParseDeepReviewResult(call ReviewCall, raw string) (*state.DeepReviewResult, []DroppedFinding, error) {
	rawResult, err := parseDeepReviewRaw(call, raw)
	if err != nil {
		return &state.DeepReviewResult{
			Type:        call.Type,
			Category:    call.Category,
			Subcategory: call.Subcategory,
		}, nil, err
	}
	result, dropped := convertRawToTyped(call, rawResult)
	return result, dropped, nil
}

// parseDeepReviewRaw is the JSON-only stage. Categories arrive as raw
// strings; doReviewCall feeds the result through verifyAndCorrectCategory
// before convertRawToTyped promotes them. Tests that don't exercise the
// corrector reach this via ParseDeepReviewResult.
func parseDeepReviewRaw(call ReviewCall, raw string) (*rawDeepReviewResult, error) {
	result := &rawDeepReviewResult{
		DeepReviewResult: state.DeepReviewResult{
			Type:        call.Type,
			Category:    call.Category,
			Subcategory: call.Subcategory,
		},
	}

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

	// Extract the last complete JSON value. Models running inside a
	// CLI tool harness may emit a pre-investigation draft, prose, and
	// a refined post-tools draft — we want the last complete value.
	extracted, err := extractLastJSONValue([]byte(s))
	if err != nil {
		return result, fmt.Errorf("%w: no JSON found in response for %s/%s",
			errReviewParse, call.Category, call.Subcategory)
	}
	s = string(extracted)

	if call.Type == "individual" {
		var parsed rawDeepFinding
		if err := unmarshalLLMResponse([]byte(s), &parsed); err != nil {
			return result, fmt.Errorf("%w: parse individual response: %v — response prefix: %q", errReviewParse, err, previewForLog([]byte(s), 500))
		}
		result.RawOutput = json.RawMessage(s)
		if parsed.Status == "finding" {
			result.Findings = append(result.Findings, parsed)
		} else {
			result.Dismissals = append(result.Dismissals, state.DeepDismissal{
				AOIID:               parsed.AOIID,
				File:                parsed.File,
				Evidence:            parsed.Evidence,
				Rationale:           parsed.DismissedRationale,
				ConfidenceScore:     parsed.ConfidenceScore,
				ConfidenceReasoning: parsed.ConfidenceReasoning,
			})
		}
	} else {
		var parsed struct {
			CrossCutting string           `json:"cross_cutting"`
			Results      []rawDeepFinding `json:"results"`
		}
		if err := unmarshalLLMResponse([]byte(s), &parsed); err != nil {
			return result, fmt.Errorf("%w: parse grouped response: %v — response prefix: %q", errReviewParse, err, previewForLog([]byte(s), 500))
		}
		result.RawOutput = json.RawMessage(s)
		result.CrossCutting = parsed.CrossCutting
		for _, r := range parsed.Results {
			if r.Status == "finding" {
				result.Findings = append(result.Findings, r)
			} else {
				result.Dismissals = append(result.Dismissals, state.DeepDismissal{
					AOIID:               r.AOIID,
					File:                r.File,
					Evidence:            r.Evidence,
					Rationale:           r.DismissedRationale,
					ConfidenceScore:     r.ConfidenceScore,
					ConfidenceReasoning: r.ConfidenceReasoning,
				})
			}
		}
	}

	return result, nil
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}

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
	"github.com/andreujuanc/prr/internal/state"
)

// ExecuteOptions configures RunReviewCalls.
type ExecuteOptions struct {
	// Mode is "pr" or "audit".
	Mode Mode

	// ProjectContext is the discovered project summary.
	ProjectContext string

	// CustomInstructions from user config.
	CustomInstructions string

	// FocusDimensions filters which AOIs are reviewed (nil = all).
	FocusDimensions []string

	// MaxConcurrency caps parallel review calls (default 10).
	MaxConcurrency int

	// NoCache disables reading from cache.
	NoCache bool

	// CacheGet retrieves a cached DeepReviewResult by key. Can be nil.
	CacheGet func(key string) *state.DeepReviewResult

	// CacheSet stores a DeepReviewResult by key. Can be nil.
	CacheSet func(key string, result *state.DeepReviewResult)

	// OnProgress is called with status updates. Can be nil.
	OnProgress func(completed, total int, cached bool, err error)

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
	Findings     []state.DeepFinding
	Dismissals   int
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
			cacheKey := ComputeCacheKey(call, opts.FocusDimensions)
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
			continue
		}
		if cr.result != nil {
			execResult.Findings = append(execResult.Findings, cr.result.Findings...)
			execResult.Dismissals += len(cr.result.Dismissals)
			if cr.result.CrossCutting != "" {
				execResult.CrossCutting = append(execResult.CrossCutting, cr.result.CrossCutting)
			}
		}
		if opts.OnProgress != nil {
			opts.OnProgress(completed, len(calls), cr.fromCache, nil)
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
// on transient errors after reviewRetryBackoff. Parse-shape failures
// (errReviewParse) and context cancellation short-circuit immediately —
// retrying the same prompt won't fix prose-in-response, and a cancelled
// context can't carry the retry anyway.
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
	result, err := doReviewCall(ctx, client, call, opts, callIndex)
	if err == nil {
		return result, nil
	}
	if errors.Is(err, errReviewParse) {
		return nil, err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}

	select {
	case <-time.After(reviewRetryBackoff):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	log.Printf("review: retrying call %d (%s %s/%s) after transient error: %v",
		callIndex+1, call.Type, call.Category, call.Subcategory, err)
	return doReviewCall(ctx, client, call, opts, callIndex)
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
	// Build prompt
	var systemPrompt string
	if call.Type == "individual" {
		systemPrompt = BuildIndividualPrompt(
			opts.Mode, opts.ProjectContext, opts.CustomInstructions, call.AOIs[0],
		)
	} else {
		systemPrompt = BuildGroupedPrompt(
			opts.Mode, opts.ProjectContext, opts.CustomInstructions, call,
		)
	}

	messages := []ai.Message{
		{Role: "user", Content: userMessage(opts.Mode)},
	}

	// Build onToken callback to capture tool events for debug logging.
	var onToken func(string)
	if opts.OnToolCall != nil {
		onToken = func(tok string) {
			if strings.HasPrefix(tok, "\x00TOOL_START:") {
				// Format: \x00TOOL_START:name(args)
				payload := strings.TrimPrefix(tok, "\x00TOOL_START:")
				if idx := strings.Index(payload, "("); idx >= 0 {
					name := payload[:idx]
					args := strings.TrimSuffix(payload[idx+1:], ")")
					opts.OnToolCall(callIndex, name, args, "start", "")
				}
			} else if strings.HasPrefix(tok, "\x00TOOL_DONE:") {
				// Format: \x00TOOL_DONE:name|status|duration
				payload := strings.TrimPrefix(tok, "\x00TOOL_DONE:")
				parts := strings.SplitN(payload, "|", 3)
				if len(parts) == 3 {
					opts.OnToolCall(callIndex, parts[0], "", parts[1], parts[2])
				}
			}
		}
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

	return ParseDeepReviewResult(call, raw)
}

// ComputeCacheKey returns the cache key for a review call.
func ComputeCacheKey(call ReviewCall, focusDimensions []string) string {
	if call.Type == "individual" {
		return IndividualCacheKey("", call.AOIs[0], focusDimensions)
	}
	return GroupedCacheKey(call.AOIs, focusDimensions)
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
func ParseDeepReviewResult(call ReviewCall, raw string) (*state.DeepReviewResult, error) {
	result := &state.DeepReviewResult{
		Type:        call.Type,
		Category:    call.Category,
		Subcategory: call.Subcategory,
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

	// Find JSON start
	jsonStart := strings.IndexAny(s, "{[")
	if jsonStart == -1 {
		return result, fmt.Errorf("%w: no JSON found in response for %s/%s",
			errReviewParse, call.Category, call.Subcategory)
	}
	s = s[jsonStart:]

	// Trim trailing non-JSON (e.g. markdown code fences like ```)
	if s[0] == '{' {
		if end := strings.LastIndex(s, "}"); end != -1 {
			s = s[:end+1]
		}
	} else if s[0] == '[' {
		if end := strings.LastIndex(s, "]"); end != -1 {
			s = s[:end+1]
		}
	}

	if call.Type == "individual" {
		var parsed struct {
			AOIID              string `json:"aoi_id"`
			Status             string `json:"status"`
			File               string `json:"file"`
			Lines              string `json:"lines"`
			Severity           string `json:"severity"`
			Category           string `json:"category"`
			Subcategory        string `json:"subcategory"`
			Dimension          string `json:"dimension"`
			Title              string `json:"title"`
			Description        string `json:"description"`
			Evidence           string `json:"evidence"`
			Trigger            string `json:"trigger"`
			Suggestion         string `json:"suggestion"`
			DismissedRationale string `json:"dismissed_rationale"`
		}
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			return result, fmt.Errorf("%w: parse individual response: %v", errReviewParse, err)
		}
		result.RawOutput = json.RawMessage(s)
		if parsed.Status == "finding" {
			result.Findings = append(result.Findings, state.DeepFinding{
				AOIID:       parsed.AOIID,
				File:        parsed.File,
				Lines:       parsed.Lines,
				Severity:    parsed.Severity,
				Category:    parsed.Category,
				Subcategory: parsed.Subcategory,
				Dimension:   parsed.Dimension,
				Title:       parsed.Title,
				Description: parsed.Description,
				Evidence:    parsed.Evidence,
				Trigger:     parsed.Trigger,
				Suggestion:  parsed.Suggestion,
			})
		} else {
			result.Dismissals = append(result.Dismissals, state.DeepDismissal{
				AOIID:     parsed.AOIID,
				Evidence:  parsed.Evidence,
				Rationale: parsed.DismissedRationale,
			})
		}
	} else {
		// Grouped response
		var parsed struct {
			Subcategory  string `json:"subcategory"`
			CrossCutting string `json:"cross_cutting"`
			Results      []struct {
				AOIID              string `json:"aoi_id"`
				Status             string `json:"status"`
				File               string `json:"file"`
				Lines              string `json:"lines"`
				Severity           string `json:"severity"`
				Category           string `json:"category"`
				Subcategory        string `json:"subcategory"`
				Dimension          string `json:"dimension"`
				Title              string `json:"title"`
				Description        string `json:"description"`
				Evidence           string `json:"evidence"`
				Trigger            string `json:"trigger"`
				Suggestion         string `json:"suggestion"`
				DismissedRationale string `json:"dismissed_rationale"`
			} `json:"results"`
		}
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			return result, fmt.Errorf("%w: parse grouped response: %v", errReviewParse, err)
		}
		result.RawOutput = json.RawMessage(s)
		result.CrossCutting = parsed.CrossCutting
		for _, r := range parsed.Results {
			if r.Status == "finding" {
				result.Findings = append(result.Findings, state.DeepFinding{
					AOIID:       r.AOIID,
					File:        r.File,
					Lines:       r.Lines,
					Severity:    r.Severity,
					Category:    r.Category,
					Subcategory: r.Subcategory,
					Dimension:   r.Dimension,
					Title:       r.Title,
					Description: r.Description,
					Evidence:    r.Evidence,
					Trigger:     r.Trigger,
					Suggestion:  r.Suggestion,
				})
			} else {
				result.Dismissals = append(result.Dismissals, state.DeepDismissal{
					AOIID:     r.AOIID,
					Evidence:  r.Evidence,
					Rationale: r.DismissedRationale,
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

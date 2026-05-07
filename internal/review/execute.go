package review

import (
	"encoding/json"
	"log"
	"sort"
	"strings"
	"sync"

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
							opts.OnToolCall(i, name, args, "start", "")
						}
					} else if strings.HasPrefix(tok, "\x00TOOL_DONE:") {
						// Format: \x00TOOL_DONE:name|status|duration
						payload := strings.TrimPrefix(tok, "\x00TOOL_DONE:")
						parts := strings.SplitN(payload, "|", 3)
						if len(parts) == 3 {
							opts.OnToolCall(i, parts[0], "", parts[1], parts[2])
						}
					}
				}
			}

			raw, callErr := client.ChatStream(ctx, systemPrompt, messages, onToken)
			if callErr != nil {
				resultsCh <- callResult{index: i, err: callErr}
				return
			}

			// Debug hook
			if opts.OnLLMCall != nil {
				opts.OnLLMCall(i, call, systemPrompt, messages[0].Content, raw)
			}

			result := ParseDeepReviewResult(call, raw)
			result.CacheKey = cacheKey

			// Cache the result (individual calls only)
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

	return execResult, nil
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

// ParseDeepReviewResult parses the LLM's response into a DeepReviewResult.
func ParseDeepReviewResult(call ReviewCall, raw string) *state.DeepReviewResult {
	result := &state.DeepReviewResult{
		Type:        call.Type,
		Category:    call.Category,
		Subcategory: call.Subcategory,
		RawOutput:   json.RawMessage(raw),
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
		log.Printf("Deep review: no JSON found in response for %s/%s", call.Category, call.Subcategory)
		return result
	}
	s = s[jsonStart:]

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
			log.Printf("Deep review: failed to parse individual response: %v", err)
			return result
		}
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
			log.Printf("Deep review: failed to parse grouped response: %v", err)
			return result
		}
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

	return result
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

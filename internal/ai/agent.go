package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	// defaultMaxRounds is the default maximum number of tool-calling iterations.
	defaultMaxRounds = 50

	// maxParallelTools caps concurrent tool execution.
	maxParallelTools = 5
)

// AgentOption configures an Agent.
type AgentOption func(*Agent)

// WithMaxRounds sets the maximum number of tool-calling loop iterations.
func WithMaxRounds(n int) AgentOption {
	return func(a *Agent) {
		if n > 0 {
			a.maxRounds = n
		}
	}
}

// WithDebugLogger enables debug logging to the given writer.
// All requests, responses, and tool calls are logged (with secrets redacted).
func WithDebugLogger(w io.Writer) AgentOption {
	return func(a *Agent) {
		a.debugLog = log.New(w, "[agent] ", log.Ltime|log.Lmicroseconds)
	}
}

// WithUsageTracker attaches a UsageTracker that accumulates token counts
// across all ChatStream calls. Useful for cost estimation and benchmarking.
func WithUsageTracker(tracker *UsageTracker) AgentOption {
	return func(a *Agent) {
		a.usageTracker = tracker
	}
}

// WithToolFilter restricts the agent to only the named tools.
// Tools not in the list are omitted from the API request and cannot be called.
func WithToolFilter(names []string) AgentOption {
	return func(a *Agent) {
		a.toolFilter = make(map[string]bool, len(names))
		for _, n := range names {
			a.toolFilter[n] = true
		}
	}
}

// UsageTracker accumulates token usage across multiple API calls.
// It is safe for concurrent use.
type UsageTracker struct {
	mu           sync.Mutex
	InputTokens  int
	OutputTokens int
	CacheHits    int
	Calls        int // number of API calls
}

// Add records usage from a single API call.
func (t *UsageTracker) Add(u TokenUsage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.InputTokens += u.InputTokens
	t.OutputTokens += u.OutputTokens
	t.CacheHits += u.CacheHits
	t.Calls++
}

// Snapshot returns a copy of the current accumulated usage.
func (t *UsageTracker) Snapshot() UsageTracker {
	t.mu.Lock()
	defer t.mu.Unlock()
	return UsageTracker{
		InputTokens:  t.InputTokens,
		OutputTokens: t.OutputTokens,
		CacheHits:    t.CacheHits,
		Calls:        t.Calls,
	}
}

// Reset zeroes all counters.
func (t *UsageTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.InputTokens = 0
	t.OutputTokens = 0
	t.CacheHits = 0
	t.Calls = 0
}

// Agent wraps a Provider with a tool-calling loop.
// It implements Client and ToolConfigurer for backward compatibility
// with the existing UI and review code.
type Agent struct {
	provider     Provider
	toolExecutor *ToolExecutor
	maxRounds    int
	debugLog     *log.Logger   // nil = no debug logging
	usageTracker *UsageTracker // nil = don't track usage
	toolFilter   map[string]bool // nil = all tools; non-nil = only named tools
}

// NewAgent creates an Agent that uses the given Provider for API calls
// and the ToolExecutor for handling tool calls within the iterative loop.
func NewAgent(provider Provider, toolExec *ToolExecutor, opts ...AgentOption) *Agent {
	a := &Agent{
		provider:     provider,
		toolExecutor: toolExec,
		maxRounds:    defaultMaxRounds,
		usageTracker: &UsageTracker{}, // always track usage
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Usage returns accumulated token usage. Implements UsageReporter.
func (a *Agent) Usage() TokenUsage {
	s := a.usageTracker.Snapshot()
	return TokenUsage{
		InputTokens:  s.InputTokens,
		OutputTokens: s.OutputTokens,
		CacheHits:    s.CacheHits,
	}
}

// ResetUsage zeroes the usage counters. Implements UsageReporter.
func (a *Agent) ResetUsage() {
	a.usageTracker.Reset()
}

// ChatStream implements Client. It runs the iterative tool-calling loop:
//
//  1. Build ChatRequest with system prompt, history, tools (and cache hint).
//  2. Call provider.StreamChat, emit text/thinking/tool tokens to the TUI.
//  3. Inspect Response.Content for ToolUseBlocks.
//  4. If tool calls found: execute (parallel if all read-only), build
//     ToolResultBlock per tool_use (matched by ID, in order), append to
//     history, loop.
//  5. If no tool calls: surface final text.
//  6. Cap iterations at maxRounds. On hit, surface partial result +
//     "max iterations reached" message.
//
// Quirk tolerance: if the model emits text without any tool_use blocks
// but the text looks like a preamble ("I'll now use …" pattern without
// substance), we tolerate this once and continue. On the second
// occurrence, we terminate.
func (a *Agent) ChatStream(ctx context.Context, systemPrompt string, messages []Message, onToken func(string)) (string, error) {
	// Convert simple Messages to ProviderMessages
	provMsgs := make([]ProviderMessage, 0, len(messages))
	for _, m := range messages {
		role := RoleUser
		if m.Role == "assistant" {
			role = RoleAssistant
		}
		provMsgs = append(provMsgs, ProviderMessage{
			Role:    role,
			Content: []ContentBlock{TextBlock{Text: m.Content}},
		})
	}

	// Get tool definitions if executor is configured
	var tools []ToolDef
	if a.toolExecutor != nil {
		all := CanonicalToolDefs()
		if a.toolFilter != nil {
			for _, t := range all {
				if a.toolFilter[t.Name] {
					tools = append(tools, t)
				}
			}
		} else {
			tools = all
		}
	}

	// Determine if provider supports prompt caching
	cachePrefix := a.provider.Capabilities().PromptCaching

	var full strings.Builder
	emptyTextStrikes := 0 // track "text but no tool use" quirk

	for round := 0; round < a.maxRounds; round++ {
		// Check context before each round
		if err := ctx.Err(); err != nil {
			return full.String(), err
		}

		req := ChatRequest{
			System:      systemPrompt,
			Messages:    provMsgs,
			Tools:       tools,
			CachePrefix: cachePrefix,
		}

		a.debugf("round %d: sending request (messages=%d, tools=%d)", round+1, len(provMsgs), len(tools))

		ch, err := a.provider.StreamChat(ctx, req)
		if err != nil {
			a.debugf("round %d: provider error: %v", round+1, err)
			return full.String(), err
		}

		var toolCalls []ToolUseBlock
		var respContent []ContentBlock
		var roundText strings.Builder

		for event := range ch {
			switch event.Type {
			case EventText:
				full.WriteString(event.Text)
				roundText.WriteString(event.Text)
				if onToken != nil {
					onToken(event.Text)
				}
			case EventThinking:
				if onToken != nil {
					onToken("\x00THOUGHT:" + event.Text)
				}
			case EventToolUse:
				toolCalls = append(toolCalls, *event.ToolUse)
			case EventDone:
				if event.Response != nil {
					respContent = event.Response.Content
					a.debugf("round %d: response done (blocks=%d, stop=%s, input_tokens=%d, output_tokens=%d)",
						round+1, len(respContent), event.Response.StopReason,
						event.Response.Usage.InputTokens, event.Response.Usage.OutputTokens)
					if a.usageTracker != nil {
						a.usageTracker.Add(event.Response.Usage)
					}
				}
			case EventError:
				a.debugf("round %d: stream error: %v", round+1, event.Err)
				return full.String(), event.Err
			}
		}

		// No tool calls → check for quirk or terminate
		if len(toolCalls) == 0 {
			// Quirk tolerance: if text looks like a preamble to tool use
			// ("I'll now use the tool", "Let me check…") but no tool_use
			// block was emitted, tolerate once. On repeat, stop.
			if looksLikeStallPreamble(roundText.String()) && len(tools) > 0 {
				emptyTextStrikes++
				a.debugf("round %d: stall preamble detected (strike %d)", round+1, emptyTextStrikes)
				if emptyTextStrikes >= 2 {
					a.debugf("round %d: terminating after %d stall preambles", round+1, emptyTextStrikes)
					break
				}
				// Echo it back as assistant turn and continue
				provMsgs = append(provMsgs, ProviderMessage{
					Role:    RoleAssistant,
					Content: respContent,
				})
				continue
			}
			break
		}

		// Reset stall counter when we get real tool calls
		emptyTextStrikes = 0

		// Append the assistant turn with all content blocks
		provMsgs = append(provMsgs, ProviderMessage{
			Role:    RoleAssistant,
			Content: respContent,
		})

		// Execute tools — parallel if all are read-only, sequential otherwise
		resultBlocks := a.executeTools(ctx, toolCalls, onToken)

		provMsgs = append(provMsgs, ProviderMessage{
			Role:    RoleUser,
			Content: resultBlocks,
		})

		// Continue loop — next iteration sends tool results to the provider
	}

	// Check if we exhausted maxRounds
	if countToolRounds(provMsgs) >= a.maxRounds {
		maxMsg := fmt.Sprintf("\n\n[max iterations (%d) reached — partial result returned]", a.maxRounds)
		full.WriteString(maxMsg)
		if onToken != nil {
			onToken(maxMsg)
		}
		a.debugf("max rounds (%d) reached", a.maxRounds)
	}

	return full.String(), nil
}

// countToolRounds counts the number of user messages that contain ToolResultBlocks.
func countToolRounds(msgs []ProviderMessage) int {
	count := 0
	for _, m := range msgs {
		if m.Role != RoleUser {
			continue
		}
		for _, b := range m.Content {
			if _, ok := b.(ToolResultBlock); ok {
				count++
				break
			}
		}
	}
	return count
}

// executeTools runs tool calls and returns ToolResultBlocks in order.
// If all tools are read-only, they execute concurrently (capped at maxParallelTools).
// Otherwise, they execute sequentially. Each tool's start/done is streamed to the TUI.
// Tool errors are captured gracefully as IsError=true results.
func (a *Agent) executeTools(ctx context.Context, toolCalls []ToolUseBlock, onToken func(string)) []ContentBlock {
	// Determine if all tools are read-only (safe for parallel execution)
	allReadOnly := true
	for _, tc := range toolCalls {
		if !IsToolReadOnly(tc.Name) {
			allReadOnly = false
			break
		}
	}

	results := make([]ContentBlock, len(toolCalls))

	if allReadOnly && len(toolCalls) > 1 {
		a.executeToolsParallel(ctx, toolCalls, results, onToken)
	} else {
		a.executeToolsSequential(ctx, toolCalls, results, onToken)
	}

	return results
}

// executeToolsParallel runs read-only tools concurrently with bounded parallelism.
func (a *Agent) executeToolsParallel(ctx context.Context, toolCalls []ToolUseBlock, results []ContentBlock, onToken func(string)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallelTools)

	for i, tc := range toolCalls {
		wg.Add(1)
		go func(idx int, tc ToolUseBlock) {
			defer wg.Done()

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = ToolResultBlock{
					ToolUseID: tc.ID,
					Name:      tc.Name,
					Content:   "tool execution cancelled",
					IsError:   true,
				}
				return
			}

			results[idx] = a.executeSingleTool(ctx, tc, onToken)
		}(i, tc)
	}

	wg.Wait()
}

// executeToolsSequential runs tools one at a time.
func (a *Agent) executeToolsSequential(ctx context.Context, toolCalls []ToolUseBlock, results []ContentBlock, onToken func(string)) {
	for i, tc := range toolCalls {
		if err := ctx.Err(); err != nil {
			results[i] = ToolResultBlock{
				ToolUseID: tc.ID,
				Name:      tc.Name,
				Content:   "tool execution cancelled",
				IsError:   true,
			}
			continue
		}
		results[i] = a.executeSingleTool(ctx, tc, onToken)
	}
}

// executeSingleTool runs one tool call with streaming events and graceful error handling.
func (a *Agent) executeSingleTool(ctx context.Context, tc ToolUseBlock, onToken func(string)) ToolResultBlock {
	argsStr := formatToolArgs(tc.Args)

	// Emit start event
	if onToken != nil {
		onToken(fmt.Sprintf("\x00TOOL_START:%s(%s)", tc.Name, argsStr))
	}

	a.debugf("tool call: %s(%s)", tc.Name, argsStr)
	start := time.Now()

	// Parse args and execute
	args := make(map[string]interface{})
	_ = json.Unmarshal(tc.Args, &args)

	result, isError := a.toolExecutor.ExecuteTool(tc.Name, args)
	elapsed := time.Since(start)

	// Log result (truncated for debug)
	truncResult := result
	if len(truncResult) > 500 {
		truncResult = truncResult[:500] + fmt.Sprintf("… (%d bytes total)", len(result))
	}
	a.debugf("tool result: %s → %dB in %v (error=%v): %s", tc.Name, len(result), elapsed, isError, truncResult)

	// Emit done event with duration
	if onToken != nil {
		status := "ok"
		if isError {
			status = "error"
		}
		onToken(fmt.Sprintf("\x00TOOL_DONE:%s|%s|%s", tc.Name, status, elapsed.Truncate(time.Millisecond)))
	}

	return ToolResultBlock{
		ToolUseID: tc.ID,
		Name:      tc.Name,
		Content:   result,
		IsError:   isError,
	}
}

// looksLikeStallPreamble detects text that suggests the model intended to call
// a tool but didn't actually emit a tool_use block. This is a known quirk
// with some models (especially older OpenAI variants).
func looksLikeStallPreamble(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}

	// Short text that looks like "I'll use the tool" / "Let me check" patterns
	lower := strings.ToLower(text)
	stallPhrases := []string{
		"i'll use the",
		"i will use the",
		"let me use the",
		"i'll now use",
		"i will now use",
		"let me check",
		"let me look",
		"let me read",
		"let me search",
		"i'll check",
		"i'll look at",
		"i'll read",
		"i'll search",
	}

	for _, phrase := range stallPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}

	return false
}

// ── ToolConfigurer implementation ───────────────────────────────────────

// ProviderName returns the name of the underlying LLM provider.
func (a *Agent) ProviderName() string {
	return a.provider.Name()
}

// ModelName returns the model identifier of the underlying LLM provider.
func (a *Agent) ModelName() string {
	return a.provider.ModelID()
}

// SetHeadRef configures the git ref used for file reading tools.
func (a *Agent) SetHeadRef(ref string) {
	if a.toolExecutor != nil {
		a.toolExecutor.HeadRef = ref
	}
}

// SetBaseRef configures the git ref for reading base-branch files.
func (a *Agent) SetBaseRef(ref string) {
	if a.toolExecutor != nil {
		a.toolExecutor.BaseRef = ref
	}
}

// SetRawDiffs provides the raw unified diffs for the git_diff tool.
func (a *Agent) SetRawDiffs(diffs map[string]string) {
	if a.toolExecutor != nil {
		a.toolExecutor.RawDiffs = diffs
	}
}

// SetReviewGetter provides a function that returns the latest PR review summary.
func (a *Agent) SetReviewGetter(fn func() string) {
	if a.toolExecutor != nil {
		a.toolExecutor.ReviewGetter = fn
	}
}

// SwitchModel changes the underlying model at runtime.
func (a *Agent) SwitchModel(modelID string, maxOutputTokens int, temperature float64, thinkingBudget int) error {
	if gp, ok := a.provider.(*GeminiProvider); ok {
		gp.Model = modelID
		gp.ModelConfig.MaxOutputTokens = maxOutputTokens
		gp.ModelConfig.Temperature = temperature
		gp.ModelConfig.ThinkingBudget = thinkingBudget
		return nil
	}
	return fmt.Errorf("model switching not supported for provider %s", a.provider.Name())
}

// ── Helpers ─────────────────────────────────────────────────────────────

// formatToolArgs formats json.RawMessage args for display.
func formatToolArgs(raw json.RawMessage) string {
	var args map[string]interface{}
	if err := json.Unmarshal(raw, &args); err != nil {
		return string(raw)
	}
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, ", ")
}

// debugf writes to the debug log if enabled.
func (a *Agent) debugf(format string, args ...interface{}) {
	if a.debugLog != nil {
		a.debugLog.Printf(format, args...)
	}
}

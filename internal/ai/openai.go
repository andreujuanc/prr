package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// OpenAIProvider implements Provider for the OpenAI Chat Completions API.
// It also works with any OpenAI-compatible endpoint (GitHub Copilot/Models,
// Azure OpenAI, Together, etc.) by overriding BaseURL.
type OpenAIProvider struct {
	APIKey     string
	Model      string
	BaseURL    string       // defaults to "https://api.openai.com/v1"
	HTTPClient *http.Client // optional; defaults to a shared client with no http-level timeout

	// RequestTimeout bounds a single HTTP call (POST → final SSE event).
	// Zero disables the wrapper; the production factory sets
	// DefaultRequestTimeout. Per-call timeout matches
	// googleapis/go-genai's HTTPOptions.Timeout pattern.
	RequestTimeout time.Duration

	// ProviderName overrides the name returned by Name() — useful for
	// distinguishing "github-copilot" from "openai" while sharing impl.
	ProviderLabel string

	// ExtraHeaders are additional HTTP headers sent on every request.
	// Used by GitHub Copilot for Openai-Intent, User-Agent, etc.
	ExtraHeaders map[string]string

	// ModelConfig holds per-model tuning.
	ModelConfig struct {
		MaxOutputTokens int
		Temperature     float64
		ThinkingBudget  int
	}
}

// defaultHTTPClient is shared across providers so connection pooling
// works — every doHTTPRequest call previously allocated a fresh
// http.Client which threw away the keep-alive connection. No
// http.Client.Timeout is set — total-request timeouts are the wrong
// shape for streaming completions (a legitimate generation can take
// many minutes). Per-call timeouts are applied at the provider level
// via RequestTimeout and context.WithTimeout.
var defaultHTTPClient = &http.Client{}

func (o *OpenAIProvider) httpClient() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return defaultHTTPClient
}

func (o *OpenAIProvider) Name() string {
	if o.ProviderLabel != "" {
		return o.ProviderLabel
	}
	return "openai"
}

func (o *OpenAIProvider) ModelID() string { return o.Model }

func (o *OpenAIProvider) Capabilities() Capabilities {
	return Capabilities{
		PromptCaching:     false,
		StructuredOutput:  true,
		ParallelToolCalls: true,
		MaxContextTokens:  128_000, // varies by model; conservative default
	}
}

// Chat performs a non-streaming request by collecting StreamChat events.
func (o *OpenAIProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	ch, err := o.StreamChat(ctx, req)
	if err != nil {
		return nil, err
	}
	var resp *ChatResponse
	for event := range ch {
		switch event.Type {
		case EventError:
			return nil, event.Err
		case EventDone:
			resp = event.Response
		}
	}
	if resp == nil {
		return nil, fmt.Errorf("openai: no response received")
	}
	return resp, nil
}

// StreamChat makes a streaming request to the OpenAI-compatible API.
//
// When RequestTimeout is set, ctx is wrapped with a per-call deadline that
// covers both the request send and the entire SSE read. The cancel function
// is held until the streaming goroutine completes so the deadline isn't
// released early.
func (o *OpenAIProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	cancel := func() {}
	if o.RequestTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, o.RequestTimeout)
	}

	nativeReq := o.toNativeRequest(req)

	body, err := json.Marshal(nativeReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("openai: failed to marshal request: %w", err)
	}

	resp, err := o.doHTTPRequest(ctx, body)
	if err != nil {
		cancel()
		return nil, err
	}

	ch := make(chan ChatEvent, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		defer cancel()
		o.parseSSEStream(ctx, resp.Body, ch)
	}()

	return ch, nil
}

// ── Native request types ────────────────────────────────────────────────

type oaiRequest struct {
	Model             string        `json:"model"`
	Messages          []oaiMessage  `json:"messages"`
	Stream            bool          `json:"stream"`
	Tools             []oaiTool     `json:"tools,omitempty"`
	ToolChoice        any           `json:"tool_choice,omitempty"`
	MaxCompletionToks int           `json:"max_completion_tokens,omitempty"`
	Temperature       *float64      `json:"temperature,omitempty"`
	ReasoningEffort   string        `json:"reasoning_effort,omitempty"`
	StreamOpts        *oaiStreamOpt `json:"stream_options,omitempty"`
}

type oaiStreamOpt struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content,omitempty"` // string or []oaiContentPart
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

type oaiContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function oaiToolCallFunc `json:"function"`
}

type oaiToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ── Request translation ─────────────────────────────────────────────────

func (o *OpenAIProvider) toNativeRequest(req ChatRequest) oaiRequest {
	var messages []oaiMessage

	// System message
	if req.System != "" {
		messages = append(messages, oaiMessage{
			Role:    "system",
			Content: req.System,
		})
	}

	// Conversation messages
	for _, msg := range req.Messages {
		messages = append(messages, o.translateMessage(msg)...)
	}

	// Tools
	var tools []oaiTool
	for _, t := range req.Tools {
		params := o.toolParamsToJSON(t.Parameters)
		tools = append(tools, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}

	// Tool choice
	var toolChoice any
	switch req.ToolChoice {
	case ToolChoiceAuto:
		if len(tools) > 0 {
			toolChoice = "auto"
		}
	case ToolChoiceRequired:
		toolChoice = "required"
	case ToolChoiceNone:
		toolChoice = "none"
	}

	// Max tokens
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = o.ModelConfig.MaxOutputTokens
	}
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	// Temperature
	temp := req.Temperature
	if temp <= 0 {
		temp = o.ModelConfig.Temperature
	}

	native := oaiRequest{
		Model:             o.Model,
		Messages:          messages,
		Stream:            true,
		MaxCompletionToks: maxTokens,
		StreamOpts:        &oaiStreamOpt{IncludeUsage: true},
	}
	if temp > 0 {
		native.Temperature = &temp
	}
	// NOTE: reasoning_effort is NOT sent here because the Chat Completions API
	// rejects it when tools are present ("Function tools with reasoning_effort
	// are not supported for gpt-5.4 in /v1/chat/completions"). To control
	// reasoning effort with tools, we would need to migrate to the Responses API.
	// The ThinkingBudget field is retained for future use.
	if len(tools) > 0 {
		native.Tools = tools
		native.ToolChoice = toolChoice
	}

	return native
}

// thinkingBudgetToEffort maps a token budget to an OpenAI reasoning_effort string.
// Aligns with Gemini thinking budgets: 0=none, <=1024=low, <=4096=medium, <=16384=high, >16384=xhigh.
func thinkingBudgetToEffort(budget int) string {
	switch {
	case budget <= 1024:
		return "low"
	case budget <= 4096:
		return "medium"
	case budget <= 16384:
		return "high"
	default:
		return "xhigh"
	}
}

func (o *OpenAIProvider) translateMessage(msg ProviderMessage) []oaiMessage {
	switch msg.Role {
	case RoleUser:
		return o.translateUserMessage(msg)
	case RoleAssistant:
		return o.translateAssistantMessage(msg)
	}
	return nil
}

func (o *OpenAIProvider) translateUserMessage(msg ProviderMessage) []oaiMessage {
	// Check if it contains tool results
	var toolResults []oaiMessage
	var textParts []string

	for _, block := range msg.Content {
		switch b := block.(type) {
		case TextBlock:
			textParts = append(textParts, b.Text)
		case ToolResultBlock:
			toolResults = append(toolResults, oaiMessage{
				Role:       "tool",
				Content:    b.Content,
				ToolCallID: b.ToolUseID,
			})
		}
	}

	var result []oaiMessage
	// Tool results go as separate "tool" role messages
	if len(toolResults) > 0 {
		result = append(result, toolResults...)
	}
	// Text parts go as a user message
	if len(textParts) > 0 {
		result = append(result, oaiMessage{
			Role:    "user",
			Content: strings.Join(textParts, "\n"),
		})
	}

	return result
}

func (o *OpenAIProvider) translateAssistantMessage(msg ProviderMessage) []oaiMessage {
	var textParts []string
	var toolCalls []oaiToolCall

	for _, block := range msg.Content {
		switch b := block.(type) {
		case TextBlock:
			textParts = append(textParts, b.Text)
		case ThinkingBlock:
			// Drop thinking blocks — OpenAI doesn't echo these back
		case ToolUseBlock:
			toolCalls = append(toolCalls, oaiToolCall{
				ID:   b.ID,
				Type: "function",
				Function: oaiToolCallFunc{
					Name:      b.Name,
					Arguments: string(b.Args),
				},
			})
		}
	}

	m := oaiMessage{
		Role: "assistant",
	}
	if len(textParts) > 0 {
		m.Content = strings.Join(textParts, "\n")
	}
	if len(toolCalls) > 0 {
		m.ToolCalls = toolCalls
	}

	return []oaiMessage{m}
}

func (o *OpenAIProvider) toolParamsToJSON(params ToolParams) json.RawMessage {
	schema := map[string]any{
		"type": "object",
	}
	props := make(map[string]any)
	for name, p := range params.Properties {
		prop := map[string]any{
			"type":        p.Type,
			"description": p.Description,
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		if p.Items != nil {
			prop["items"] = map[string]any{
				"type": p.Items.Type,
			}
		}
		props[name] = prop
	}
	schema["properties"] = props
	if len(params.Required) > 0 {
		schema["required"] = params.Required
	}
	raw, _ := json.Marshal(schema)
	return raw
}

// ── HTTP request with retry ─────────────────────────────────────────────

func (o *OpenAIProvider) doHTTPRequest(ctx context.Context, body []byte) (*http.Response, error) {
	base := o.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	url := base + "/chat/completions"

	maxRetries := 2
	var resp *http.Response
	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("openai: failed to create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
		for k, v := range o.ExtraHeaders {
			httpReq.Header.Set(k, v)
		}

		resp, err = o.httpClient().Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("openai: request failed: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if (resp.StatusCode == 429 || resp.StatusCode == 503) && attempt < maxRetries {
			delay := 2 * time.Second * time.Duration(attempt+1)
			log.Printf("OpenAI API rate limited (HTTP %d), retrying in %v (attempt %d/%d)",
				resp.StatusCode, delay, attempt+1, maxRetries)
			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		log.Printf("OpenAI API error (HTTP %d): %s", resp.StatusCode, string(errBody))
		return nil, fmt.Errorf("OpenAI API error (HTTP %d): %s", resp.StatusCode, string(errBody))
	}

	return nil, fmt.Errorf("openai: exhausted retries without a response")
}

// ── SSE stream parsing ──────────────────────────────────────────────────

type oaiStreamChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Delta struct {
			Content   string        `json:"content"`
			ToolCalls []oaiToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (o *OpenAIProvider) parseSSEStream(ctx context.Context, body io.Reader, ch chan<- ChatEvent) {
	var contentBlocks []ContentBlock
	var textBuf strings.Builder
	var usage TokenUsage
	var stopReason StopReason

	// Track tool calls being accumulated across deltas
	type toolCallAccum struct {
		ID   string
		Name string
		Args strings.Builder
	}
	toolCalls := make(map[int]*toolCallAccum)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			ch <- ChatEvent{Type: EventError, Err: ctx.Err()}
			return
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk oaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Usage info (comes in final chunk with stream_options)
		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		// Text content
		if delta.Content != "" {
			textBuf.WriteString(delta.Content)
			ch <- ChatEvent{Type: EventText, Text: delta.Content}
		}

		// Tool calls (accumulated across multiple deltas)
		for _, tc := range delta.ToolCalls {
			idx := 0 // OpenAI sends index in the array; use ID-based if available
			if tc.ID != "" {
				// New tool call starting
				toolCalls[len(toolCalls)] = &toolCallAccum{
					ID:   tc.ID,
					Name: tc.Function.Name,
				}
				idx = len(toolCalls) - 1
			} else {
				// Continuation of existing tool call (find last one)
				idx = len(toolCalls) - 1
			}
			if accum, ok := toolCalls[idx]; ok {
				if tc.Function.Name != "" && accum.Name == "" {
					accum.Name = tc.Function.Name
				}
				accum.Args.WriteString(tc.Function.Arguments)
			}
		}

		// Finish reason
		if choice.FinishReason != nil {
			switch *choice.FinishReason {
			case "stop":
				stopReason = StopEndTurn
			case "tool_calls":
				stopReason = StopToolUse
			case "length":
				stopReason = StopMaxTokens
			}
		}
	}

	// scanner.Scan() returned false: either we hit [DONE] (clean) or the
	// underlying body read errored (network drop, per-call ctx deadline,
	// user cancel). Surface the error so callers don't get a fake
	// EventDone with truncated content.
	if err := scanner.Err(); err != nil {
		log.Printf("OpenAI stream read error: %v", err)
		ch <- ChatEvent{Type: EventError, Err: fmt.Errorf("stream read error: %w", err)}
		return
	}
	// scanner.Err() returns nil on ctx-driven Body.Close (the http.Client
	// closes the body when ctx fires, surfacing as io.EOF which Scanner
	// hides). Check ctx directly so per-call timeouts and parent cancels
	// don't silently produce an empty Done event.
	if err := ctx.Err(); err != nil {
		ch <- ChatEvent{Type: EventError, Err: err}
		return
	}

	// Finalize text block
	if textBuf.Len() > 0 {
		contentBlocks = append(contentBlocks, TextBlock{Text: textBuf.String()})
	}

	// Finalize tool call blocks
	for i := 0; i < len(toolCalls); i++ {
		tc := toolCalls[i]
		contentBlocks = append(contentBlocks, ToolUseBlock{
			ID:   tc.ID,
			Name: tc.Name,
			Args: json.RawMessage(tc.Args.String()),
		})
		ch <- ChatEvent{Type: EventToolUse, ToolUse: &ToolUseBlock{
			ID:   tc.ID,
			Name: tc.Name,
			Args: json.RawMessage(tc.Args.String()),
		}}
	}

	// Emit done event
	ch <- ChatEvent{
		Type: EventDone,
		Response: &ChatResponse{
			Content:    contentBlocks,
			StopReason: stopReason,
			Usage:      usage,
		},
	}
}

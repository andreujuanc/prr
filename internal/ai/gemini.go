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
	"strconv"
	"strings"
	"time"
)

// GeminiProvider implements Provider for the Google Gemini API.
// It handles single request/response translation; the iterative
// tool-calling loop lives in Agent.
type GeminiProvider struct {
	APIKey     string
	Model      string
	BaseURL    string       // override for testing; empty uses the real Gemini API
	HTTPClient *http.Client // optional; defaults to a client with no timeout (context-based cancellation)

	// ModelConfig holds per-model tuning (maxOutputTokens, temperature,
	// thinkingBudget). Set by the caller from config.GetModelConfig().
	ModelConfig struct {
		MaxOutputTokens int
		Temperature     float64
		ThinkingBudget  int
	}
}

// httpClient returns the configured HTTP client or a sensible default.
// No Timeout is set because http.Client.Timeout covers the entire request
// lifecycle including reading the streaming response body — which can take
// minutes for long-thinking models. Cancellation is handled via the
// request context instead.
func (g *GeminiProvider) httpClient() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{}
}

func (g *GeminiProvider) Name() string    { return "gemini" }
func (g *GeminiProvider) ModelID() string  { return g.Model }

func (g *GeminiProvider) Capabilities() Capabilities {
	return Capabilities{
		PromptCaching:     true,
		StructuredOutput:  true,
		ParallelToolCalls: true,
		MaxContextTokens:  1_000_000,
	}
}

// Chat performs a non-streaming request by collecting StreamChat events.
func (g *GeminiProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	ch, err := g.StreamChat(ctx, req)
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
		return nil, fmt.Errorf("gemini: no response received")
	}
	return resp, nil
}

// StreamChat makes a single streaming API call and returns a channel of events.
// It translates ChatRequest to Gemini's native format, streams the SSE response,
// and emits canonical ChatEvents. The channel is closed when the response ends.
func (g *GeminiProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	nativeReq := g.toNativeRequest(req)

	body, err := json.Marshal(nativeReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: failed to marshal request: %w", err)
	}

	resp, err := g.doHTTPRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	ch := make(chan ChatEvent, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		g.parseSSEStream(ctx, resp.Body, ch)
	}()

	return ch, nil
}

// ── Gemini native types ─────────────────────────────────────────────────

type geminiRequest struct {
	SystemInstruction *geminiContent       `json:"systemInstruction,omitempty"`
	Contents          []geminiContent      `json:"contents"`
	Tools             []geminiTool         `json:"tools,omitempty"`
	GenerationConfig  *geminiGenConfig     `json:"generationConfig,omitempty"`
	ToolConfig        *geminiToolConfig    `json:"toolConfig,omitempty"`
	SafetySettings    []geminiSafety       `json:"safetySettings,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                `json:"text,omitempty"`
	Thought          *bool                 `json:"thought,omitempty"`
	ThoughtSignature string                `json:"thoughtSignature,omitempty"`
	FunctionCall     *geminiFunctionCall   `json:"functionCall,omitempty"`
	FunctionResponse *geminiFuncResponse   `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
	ID   string                 `json:"id,omitempty"`
}

type geminiFuncResponse struct {
	Name     string      `json:"name"`
	Response interface{} `json:"response"`
}

// Tool declaration types
type geminiTool struct {
	FunctionDeclarations []geminiFunction `json:"functionDeclarations"`
}

type geminiFunction struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Parameters  geminiSchema `json:"parameters"`
}

type geminiSchema struct {
	Type        string                  `json:"type"`
	Description string                  `json:"description,omitempty"`
	Properties  map[string]geminiSchema `json:"properties,omitempty"`
	Required    []string                `json:"required,omitempty"`
	Items       *geminiSchema           `json:"items,omitempty"`
	Enum        []string                `json:"enum,omitempty"`
}

// Generation config types
type geminiGenConfig struct {
	MaxOutputTokens int                  `json:"maxOutputTokens,omitempty"`
	Temperature     *float64             `json:"temperature,omitempty"`
	ThinkingConfig  *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type geminiThinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts"`
	ThinkingBudget  int  `json:"thinkingBudget,omitempty"`
}

// Tool config types
type geminiToolConfig struct {
	FunctionCallingConfig *geminiFuncCallingConfig `json:"functionCallingConfig,omitempty"`
}

type geminiFuncCallingConfig struct {
	Mode string `json:"mode"` // AUTO, ANY, NONE, VALIDATED
}

// Safety settings
type geminiSafety struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// Streaming response
type geminiStreamResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text             string              `json:"text,omitempty"`
				Thought          *bool               `json:"thought,omitempty"`
				ThoughtSignature string              `json:"thoughtSignature,omitempty"`
				FunctionCall     *geminiFunctionCall `json:"functionCall,omitempty"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata,omitempty"`
}

// ── Translation: canonical → Gemini native ──────────────────────────────

func (g *GeminiProvider) toNativeRequest(req ChatRequest) geminiRequest {
	var native geminiRequest

	if req.System != "" {
		native.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: req.System}},
		}
	}

	for _, msg := range req.Messages {
		role := "user"
		if msg.Role == RoleAssistant {
			role = "model"
		}

		var parts []geminiPart
		for _, block := range msg.Content {
			switch b := block.(type) {
			case TextBlock:
				parts = append(parts, geminiPart{Text: b.Text})
			case ThinkingBlock:
				p := geminiPart{
					Text:    b.Text,
					Thought: boolPtr(true),
				}
				if b.Signature != "" {
					p.ThoughtSignature = b.Signature
				}
				parts = append(parts, p)
			case ToolUseBlock:
				args := make(map[string]interface{})
				_ = json.Unmarshal(b.Args, &args)
				p := geminiPart{
					FunctionCall: &geminiFunctionCall{
						Name: b.Name,
						Args: args,
						ID:   b.ID,
					},
				}
				if b.Signature != "" {
					p.ThoughtSignature = b.Signature
				}
				parts = append(parts, p)
			case ToolResultBlock:
				parts = append(parts, geminiPart{
					FunctionResponse: &geminiFuncResponse{
						Name:     b.Name,
						Response: map[string]string{"result": b.Content},
					},
				})
			}
		}

		native.Contents = append(native.Contents, geminiContent{
			Role:  role,
			Parts: parts,
		})
	}

	if len(req.Tools) > 0 {
		native.Tools = convertToolDefs(req.Tools)
	}

	// Generation config: temperature, max tokens, thinking
	native.GenerationConfig = g.buildGenConfig()

	// Tool config: use VALIDATED mode for constrained decoding when tools are present
	if len(req.Tools) > 0 {
		native.ToolConfig = &geminiToolConfig{
			FunctionCallingConfig: &geminiFuncCallingConfig{Mode: "VALIDATED"},
		}
	}

	// Safety settings: disable all blocking — code content frequently
	// triggers false positives on security/profanity/violence filters
	native.SafetySettings = []geminiSafety{
		{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_NONE"},
		{Category: "HARM_CATEGORY_HATE_SPEECH", Threshold: "BLOCK_NONE"},
		{Category: "HARM_CATEGORY_SEXUALLY_EXPLICIT", Threshold: "BLOCK_NONE"},
		{Category: "HARM_CATEGORY_DANGEROUS_CONTENT", Threshold: "BLOCK_NONE"},
	}

	return native
}

// buildGenConfig constructs the generationConfig from the provider's ModelConfig.
func (g *GeminiProvider) buildGenConfig() *geminiGenConfig {
	cfg := &geminiGenConfig{}

	if g.ModelConfig.MaxOutputTokens > 0 {
		cfg.MaxOutputTokens = g.ModelConfig.MaxOutputTokens
	}

	if g.ModelConfig.Temperature > 0 {
		t := g.ModelConfig.Temperature
		cfg.Temperature = &t
	}

	if g.ModelConfig.ThinkingBudget > 0 {
		cfg.ThinkingConfig = &geminiThinkingConfig{
			IncludeThoughts: true,
			ThinkingBudget:  g.ModelConfig.ThinkingBudget,
		}
	}

	return cfg
}

// convertToolDefs translates canonical ToolDefs to Gemini's native format.
func convertToolDefs(tools []ToolDef) []geminiTool {
	var fns []geminiFunction
	for _, t := range tools {
		fns = append(fns, geminiFunction{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  convertToolParams(t.Parameters),
		})
	}
	return []geminiTool{{FunctionDeclarations: fns}}
}

func convertToolParams(p ToolParams) geminiSchema {
	schema := geminiSchema{
		Type:     strings.ToUpper(p.Type),
		Required: p.Required,
	}
	if len(p.Properties) > 0 {
		schema.Properties = make(map[string]geminiSchema, len(p.Properties))
		for name, param := range p.Properties {
			schema.Properties[name] = convertToolParam(param)
		}
	}
	return schema
}

func convertToolParam(p ToolParam) geminiSchema {
	schema := geminiSchema{
		Type:        strings.ToUpper(p.Type),
		Description: p.Description,
		Enum:        p.Enum,
	}
	if p.Items != nil {
		items := convertToolParam(*p.Items)
		schema.Items = &items
	}
	return schema
}

// ── HTTP request with retry ─────────────────────────────────────────────

func (g *GeminiProvider) doHTTPRequest(ctx context.Context, body []byte) (*http.Response, error) {
	base := g.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", base, g.Model)

	maxRetries := 2
	var resp *http.Response
	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("gemini: failed to create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-goog-api-key", g.APIKey)

		resp, err = g.httpClient().Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("gemini: request failed: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if (resp.StatusCode == 429 || resp.StatusCode == 503) && attempt < maxRetries {
			delay := parseRetryDelay(errBody)
			log.Printf("Gemini API rate limited (HTTP %d), retrying in %v (attempt %d/%d)",
				resp.StatusCode, delay, attempt+1, maxRetries)
			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		log.Printf("Gemini API error (HTTP %d): %s", resp.StatusCode, string(errBody))
		return nil, fmt.Errorf("Gemini API error (HTTP %d): %s", resp.StatusCode, string(errBody))
	}

	// Unreachable: the loop always returns from the StatusOK branch or the error branch.
	// If we somehow get here, return a clear error rather than a closed response body.
	return nil, fmt.Errorf("gemini: exhausted retries without a response")
}

// ── SSE stream parsing ──────────────────────────────────────────────────

func (g *GeminiProvider) parseSSEStream(ctx context.Context, body io.Reader, ch chan<- ChatEvent) {
	var contentBlocks []ContentBlock
	var usage TokenUsage

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		// Respect context cancellation during parsing
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
		if data == "" {
			continue
		}

		var chunk geminiStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Extract usage from the last chunk
		if chunk.UsageMetadata != nil {
			usage = TokenUsage{
				InputTokens:  chunk.UsageMetadata.PromptTokenCount,
				OutputTokens: chunk.UsageMetadata.CandidatesTokenCount,
				CacheHits:    chunk.UsageMetadata.CachedContentTokenCount,
			}
		}

		for _, candidate := range chunk.Candidates {
			for _, part := range candidate.Content.Parts {
			// Handle function calls
			if part.FunctionCall != nil {
				args, _ := json.Marshal(part.FunctionCall.Args)
				tub := ToolUseBlock{
					ID:        part.FunctionCall.ID,
					Name:      part.FunctionCall.Name,
					Args:      args,
					Signature: part.ThoughtSignature,
				}
				// If the function call part doesn't carry its own
				// thoughtSignature, inherit it from the most recent
				// ThinkingBlock. Gemini requires this on every
				// functionCall part when echoing the turn back.
				if tub.Signature == "" {
					for i := len(contentBlocks) - 1; i >= 0; i-- {
						if tb, ok := contentBlocks[i].(ThinkingBlock); ok && tb.Signature != "" {
							tub.Signature = tb.Signature
							break
						}
					}
				}
				// Generate a synthetic ID if Gemini didn't provide one
				if tub.ID == "" {
					tub.ID = fmt.Sprintf("call_%d", len(contentBlocks))
				}
				contentBlocks = append(contentBlocks, tub)
				ch <- ChatEvent{Type: EventToolUse, ToolUse: &tub}
			}

				// Handle text (regular or thinking)
				if part.Text != "" {
					if part.Thought != nil && *part.Thought {
						tb := ThinkingBlock{
							Text:      part.Text,
							Signature: part.ThoughtSignature,
						}
						contentBlocks = append(contentBlocks, tb)
						ch <- ChatEvent{Type: EventThinking, Text: part.Text}
					} else {
						contentBlocks = append(contentBlocks, TextBlock{Text: part.Text})
						ch <- ChatEvent{Type: EventText, Text: part.Text}
					}
				} else if part.Thought != nil && *part.Thought && part.ThoughtSignature != "" {
					// Gemini sometimes sends the thoughtSignature in a
					// separate part with empty text after the thinking
					// content. Attach the signature to the most recent
					// ThinkingBlock so it is echoed back correctly;
					// without it the API rejects tool-call turns with
					// "Function call is missing a thought signature".
					attached := false
					for i := len(contentBlocks) - 1; i >= 0; i-- {
						if tb, ok := contentBlocks[i].(ThinkingBlock); ok && tb.Signature == "" {
							tb.Signature = part.ThoughtSignature
							contentBlocks[i] = tb
							attached = true
							break
						}
					}
					if !attached {
						// No prior ThinkingBlock — create a minimal one
						// so the signature is preserved in the turn.
						contentBlocks = append(contentBlocks, ThinkingBlock{
							Signature: part.ThoughtSignature,
						})
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Gemini stream read error: %v", err)
		ch <- ChatEvent{Type: EventError, Err: fmt.Errorf("stream read error: %w", err)}
		return
	}

	// Determine stop reason from content
	stopReason := StopEndTurn
	for _, block := range contentBlocks {
		if _, ok := block.(ToolUseBlock); ok {
			stopReason = StopToolUse
			break
		}
	}

	ch <- ChatEvent{
		Type: EventDone,
		Response: &ChatResponse{
			Content:    contentBlocks,
			StopReason: stopReason,
			Usage:      usage,
		},
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────

func boolPtr(v bool) *bool { return &v }

// parseRetryDelay extracts the retry delay from a Gemini error response body.
// Falls back to 5 seconds if parsing fails.
func parseRetryDelay(body []byte) time.Duration {
	const fallback = 5 * time.Second

	var errResp struct {
		Error struct {
			Details []struct {
				Type       string `json:"@type"`
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		return fallback
	}
	for _, d := range errResp.Error.Details {
		if d.RetryDelay != "" {
			// Format is like "41s" or "41.5s"
			d.RetryDelay = strings.TrimSuffix(d.RetryDelay, "s")
			if secs, err := strconv.ParseFloat(d.RetryDelay, 64); err == nil {
				if secs <= 0 {
					return 100 * time.Millisecond // immediate retry with small buffer
				}
				// Cap at 60 seconds to avoid unreasonable waits
				if secs > 60 {
					secs = 60
				}
				return time.Duration(secs * float64(time.Second))
			}
		}
	}
	return fallback
}

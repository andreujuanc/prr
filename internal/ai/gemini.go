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

// GeminiClient implements Client for the Google Gemini API.
type GeminiClient struct {
	APIKey       string
	Model        string
	ToolExecutor *ToolExecutor
	BaseURL      string       // override for testing; empty uses the real Gemini API
	HTTPClient   *http.Client // optional; defaults to a client with 5-minute timeout
}

// httpClient returns the configured HTTP client or a sensible default.
func (g *GeminiClient) httpClient() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// SetHeadRef configures the git ref for file reading tools.
func (g *GeminiClient) SetHeadRef(ref string) {
	if g.ToolExecutor != nil {
		g.ToolExecutor.HeadRef = ref
	}
}

// SetRawDiffs provides the raw unified diffs for the get_diff tool.
func (g *GeminiClient) SetRawDiffs(diffs map[string]string) {
	if g.ToolExecutor != nil {
		g.ToolExecutor.RawDiffs = diffs
	}
}

// Gemini API request/response types

type geminiRequest struct {
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	Tools             []geminiTool    `json:"tools,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text               string                `json:"text,omitempty"`
	Thought            *bool                 `json:"thought,omitempty"`
	ThoughtSignature   string                `json:"thoughtSignature,omitempty"`
	FunctionCall       *geminiFunctionCall   `json:"functionCall,omitempty"`
	FunctionResponse   *geminiFuncResponse   `json:"functionResponse,omitempty"`
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

// Streaming response
type geminiStreamResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text             string              `json:"text,omitempty"`
				Thought          *bool               `json:"thought,omitempty"`
				ThoughtSignature string              `json:"thoughtSignature,omitempty"`
				FunctionCall     *geminiFunctionCall  `json:"functionCall,omitempty"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func (g *GeminiClient) ChatStream(ctx context.Context, systemPrompt string, messages []Message, onToken func(string)) (string, error) {
	// Build initial contents from messages
	var contents []geminiContent

	for _, m := range messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: m.Content}},
		})
	}

	var full strings.Builder

	// Tool call loop: keep making requests until we get a text response
	maxToolRounds := 10
	for round := 0; round < maxToolRounds; round++ {
		// Build request
		req := geminiRequest{
			Contents: contents,
		}

		if systemPrompt != "" {
			req.SystemInstruction = &geminiContent{
				Parts: []geminiPart{{Text: systemPrompt}},
			}
		}

		// Add tools if executor is configured
		if g.ToolExecutor != nil {
			req.Tools = ToolDeclarations()
		}

		// Stream the response
		textResult, toolCalls, modelParts, err := g.doStreamRequest(ctx, req, onToken, &full)
		if err != nil {
			return full.String(), err
		}

		// If no tool calls, we're done
		if len(toolCalls) == 0 {
			_ = textResult
			break
		}

		// Append the full model turn (includes thought signatures + function calls)
		contents = append(contents, geminiContent{
			Role:  "model",
			Parts: modelParts,
		})

		// Execute tools and build response parts
		var responseParts []geminiPart
		for _, tc := range toolCalls {
			log.Printf("AI tool call: %s(%v)", tc.Name, tc.Args)
			if onToken != nil {
				onToken(fmt.Sprintf("\x00TOOL:%s(%s)", tc.Name, formatArgs(tc.Args)))
			}

			result := g.ToolExecutor.ExecuteTool(tc.Name, tc.Args)

			responseParts = append(responseParts, geminiPart{
				FunctionResponse: &geminiFuncResponse{
					Name:     tc.Name,
					Response: map[string]string{"result": result},
				},
			})
		}
		contents = append(contents, geminiContent{
			Role:  "user",
			Parts: responseParts,
		})

		// Continue loop — next iteration will send the tool results
	}

	return full.String(), nil
}

// doStreamRequest makes a single streaming request and returns text output, any tool calls,
// and the full set of model parts (needed to echo thought signatures back for thinking models).
func (g *GeminiClient) doStreamRequest(ctx context.Context, req geminiRequest, onToken func(string), full *strings.Builder) (string, []geminiFunctionCall, []geminiPart, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	base := g.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", base, g.Model)

	// Retry loop for transient errors (429 rate limit, 503 unavailable)
	maxRetries := 2
	var resp *http.Response
	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-goog-api-key", g.APIKey)

		resp, err = g.httpClient().Do(httpReq)
		if err != nil {
			return "", nil, nil, fmt.Errorf("request failed: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			break // success
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
				return "", nil, nil, ctx.Err()
			}
		}

		log.Printf("Gemini API error (HTTP %d): %s", resp.StatusCode, string(errBody))
		return "", nil, nil, fmt.Errorf("Gemini API error (HTTP %d): %s", resp.StatusCode, string(errBody))
	}
	defer resp.Body.Close()

	var textResult strings.Builder
	var toolCalls []geminiFunctionCall
	var modelParts []geminiPart

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
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

		for _, candidate := range chunk.Candidates {
			for _, part := range candidate.Content.Parts {
				// Collect all parts (text, thought, functionCall) for replay
				p := geminiPart{
					Thought:          part.Thought,
					ThoughtSignature: part.ThoughtSignature,
				}
				if part.FunctionCall != nil {
					toolCalls = append(toolCalls, *part.FunctionCall)
					p.FunctionCall = &geminiFunctionCall{
						Name: part.FunctionCall.Name,
						Args: part.FunctionCall.Args,
						ID:   part.FunctionCall.ID,
					}
				}
				if part.Text != "" {
					p.Text = part.Text
					if part.Thought != nil && *part.Thought {
						// Stream thought text with a marker prefix so the UI
						// can style it differently (dim/italic).
						if onToken != nil {
							onToken("\x00THOUGHT:" + part.Text)
						}
					} else {
						textResult.WriteString(part.Text)
						full.WriteString(part.Text)
						if onToken != nil {
							onToken(part.Text)
						}
					}
				}
				// Only keep parts that carry actual data — empty parts
				// cause "required oneof field 'data'" errors on the next request.
				if p.Text != "" || p.FunctionCall != nil {
					modelParts = append(modelParts, p)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Gemini stream read error: %v", err)
		return textResult.String(), toolCalls, modelParts, fmt.Errorf("stream read error: %w", err)
	}

	return textResult.String(), toolCalls, modelParts, nil
}

// formatArgs creates a readable summary of tool call arguments.
func formatArgs(args map[string]interface{}) string {
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, ", ")
}

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

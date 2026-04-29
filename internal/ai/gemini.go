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
)

// GeminiClient implements Client for the Google Gemini API.
type GeminiClient struct {
	APIKey       string
	Model        string
	ToolExecutor *ToolExecutor
}

// SetHeadRef configures the git ref for file reading tools.
func (g *GeminiClient) SetHeadRef(ref string) {
	if g.ToolExecutor != nil {
		g.ToolExecutor.HeadRef = ref
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
	Text             string                `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall   `json:"functionCall,omitempty"`
	FunctionResponse *geminiFuncResponse   `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
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
				Text         string              `json:"text,omitempty"`
				FunctionCall *geminiFunctionCall  `json:"functionCall,omitempty"`
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
		textResult, toolCalls, err := g.doStreamRequest(ctx, req, onToken, &full)
		if err != nil {
			return full.String(), err
		}

		// If no tool calls, we're done
		if len(toolCalls) == 0 {
			_ = textResult
			break
		}

		// Append model's tool call turn
		var callParts []geminiPart
		for _, tc := range toolCalls {
			callParts = append(callParts, geminiPart{
				FunctionCall: &geminiFunctionCall{
					Name: tc.Name,
					Args: tc.Args,
				},
			})
		}
		contents = append(contents, geminiContent{
			Role:  "model",
			Parts: callParts,
		})

		// Execute tools and build response parts
		var responseParts []geminiPart
		for _, tc := range toolCalls {
			log.Printf("AI tool call: %s(%v)", tc.Name, tc.Args)
			if onToken != nil {
				onToken(fmt.Sprintf("\n🔧 *%s*(%s)\n", tc.Name, formatArgs(tc.Args)))
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

// doStreamRequest makes a single streaming request and returns text output and any tool calls.
func (g *GeminiClient) doStreamRequest(ctx context.Context, req geminiRequest, onToken func(string), full *strings.Builder) (string, []geminiFunctionCall, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent?alt=sse",
		g.Model,
	)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", g.APIKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("Gemini API error (HTTP %d): %s", resp.StatusCode, string(errBody))
	}

	var textResult strings.Builder
	var toolCalls []geminiFunctionCall

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
				if part.FunctionCall != nil {
					toolCalls = append(toolCalls, *part.FunctionCall)
				}
				if part.Text != "" {
					textResult.WriteString(part.Text)
					full.WriteString(part.Text)
					if onToken != nil {
						onToken(part.Text)
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return textResult.String(), toolCalls, fmt.Errorf("stream read error: %w", err)
	}

	return textResult.String(), toolCalls, nil
}

// formatArgs creates a readable summary of tool call arguments.
func formatArgs(args map[string]interface{}) string {
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, ", ")
}

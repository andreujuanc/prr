package ai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andreujuanc/prr/internal/config"
)

// Run with: PRR_LIVE_TESTS=1 go test ./internal/ai/ -run TestLiveCopilot -v

func skipWithoutCopilotKey(t *testing.T) (string, map[string]string) {
	t.Helper()
	if os.Getenv("PRR_LIVE_TESTS") != "1" {
		t.Skip("PRR_LIVE_TESTS=1 not set, skipping live API test")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("no valid config: %v", err)
	}
	key := cfg.APIKeyFor("github-copilot")
	if key == "" {
		t.Skip("no github-copilot API key in config, skipping")
	}
	return key, map[string]string{
		"User-Agent":    "prr",
		"Openai-Intent": "conversation-edits",
	}
}

func TestLiveCopilot_SimpleChat(t *testing.T) {
	key, headers := skipWithoutCopilotKey(t)

	model := "claude-opus-4.6"
	if m := os.Getenv("PRR_COPILOT_MODEL"); m != "" {
		model = m
	}

	provider := &OpenAIProvider{
		APIKey:       key,
		Model:        model,
		BaseURL:      CopilotBaseURL,
		ExtraHeaders: headers,
	}
	agent := NewAgent(provider, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx, "Reply in exactly one word.", []Message{
		{Role: "user", Content: "What color is the sky on a clear day?"},
	}, func(s string) {
		tokens = append(tokens, s)
	})

	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
	if len(tokens) == 0 {
		t.Error("expected at least one token callback")
	}
	t.Logf("Model: %s | Result: %q (%d tokens)", model, result, len(tokens))
}

// TestLiveCopilot_ResponsesAPIProbe checks if Copilot supports /v1/responses.
func TestLiveCopilot_ResponsesAPIProbe(t *testing.T) {
	key, headers := skipWithoutCopilotKey(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	models := []string{"gpt-4.1", "gpt-5.4", "claude-opus-4.6", "claude-sonnet-4.6", "o4-mini", "gemini-3.1-pro-preview"}
	for _, model := range models {
		reqBody := fmt.Sprintf(`{"model":%q,"input":"Say hello in one word.","stream":false}`, model)

		req, err := http.NewRequestWithContext(ctx, "POST", CopilotBaseURL+"/responses",
			strings.NewReader(reqBody))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed for %s: %v", model, err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		t.Logf("[%s] HTTP %d: %.200s", model, resp.StatusCode, string(body))
	}
}

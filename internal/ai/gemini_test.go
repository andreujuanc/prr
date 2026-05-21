package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andreujuanc/prr/internal/config"
)

// ── Test helpers ────────────────────────────────────────────────────────

// sseEvent formats a geminiStreamResponse as an SSE "data:" line.
func sseEvent(parts ...ssePartSpec) string {
	type partJSON struct {
		Text             string              `json:"text,omitempty"`
		Thought          *bool               `json:"thought,omitempty"`
		ThoughtSignature string              `json:"thoughtSignature,omitempty"`
		FunctionCall     *geminiFunctionCall `json:"functionCall,omitempty"`
	}

	var pp []partJSON
	for _, p := range parts {
		pj := partJSON{
			Text:             p.Text,
			Thought:          p.Thought,
			ThoughtSignature: p.ThoughtSignature,
			FunctionCall:     p.FunctionCall,
		}
		pp = append(pp, pj)
	}

	resp := map[string]any{
		"candidates": []map[string]any{
			{
				"content": map[string]any{
					"parts": pp,
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return "data: " + string(b) + "\n\n"
}

type ssePartSpec struct {
	Text             string
	Thought          *bool
	ThoughtSignature string
	FunctionCall     *geminiFunctionCall
}

// newTestServer creates an httptest.Server that replays a sequence of SSE responses.
// Each call to the endpoint consumes the next response in the sequence.
func newTestServer(responses []string) *httptest.Server {
	var mu sync.Mutex
	idx := 0

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("x-goog-api-key") == "" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}

		mu.Lock()
		if idx >= len(responses) {
			mu.Unlock()
			http.Error(w, "no more responses", http.StatusInternalServerError)
			return
		}
		body := responses[idx]
		idx++
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
}

// newTestProvider creates a GeminiProvider pointing at the test server.
func newTestProvider(serverURL string) *GeminiProvider {
	return &GeminiProvider{
		APIKey:  "test-key",
		Model:   "test-model",
		BaseURL: serverURL,
	}
}

// collectText drains the event channel and returns accumulated text + response.
func collectText(ch <-chan ChatEvent) (string, *ChatResponse, error) {
	var text strings.Builder
	var resp *ChatResponse
	for event := range ch {
		switch event.Type {
		case EventText:
			text.WriteString(event.Text)
		case EventDone:
			resp = event.Response
		case EventError:
			return text.String(), nil, event.Err
		}
	}
	return text.String(), resp, nil
}

// collectEvents drains the event channel and returns all events.
func collectEvents(ch <-chan ChatEvent) ([]ChatEvent, error) {
	var events []ChatEvent
	for event := range ch {
		events = append(events, event)
		if event.Type == EventError {
			return events, event.Err
		}
	}
	return events, nil
}

// readBody reads and returns the request body as a string.
func readBody(r *http.Request) (string, error) {
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	return string(b), err
}

// ── GeminiProvider tests ────────────────────────────────────────────────

func TestGeminiStreamChat_SimpleText(t *testing.T) {
	resp := sseEvent(ssePartSpec{Text: "Hello "}) +
		sseEvent(ssePartSpec{Text: "world!"})

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		System: "system",
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text, chatResp, err := collectText(ch)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}
	if text != "Hello world!" {
		t.Errorf("got text %q, want %q", text, "Hello world!")
	}
	if chatResp == nil {
		t.Fatal("expected ChatResponse in EventDone")
	}
	if chatResp.StopReason != StopEndTurn {
		t.Errorf("got stop reason %q, want %q", chatResp.StopReason, StopEndTurn)
	}
}

func TestGeminiStreamChat_Thinking(t *testing.T) {
	resp := sseEvent(ssePartSpec{Text: "let me think...", Thought: new(true)}) +
		sseEvent(ssePartSpec{Text: "The answer is 42."})

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "question"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, err := collectEvents(ch)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	// Should have: EventThinking, EventText, EventDone
	var foundThinking, foundText bool
	for _, e := range events {
		switch e.Type {
		case EventThinking:
			foundThinking = true
			if e.Text != "let me think..." {
				t.Errorf("thinking text = %q, want %q", e.Text, "let me think...")
			}
		case EventText:
			foundText = true
			if e.Text != "The answer is 42." {
				t.Errorf("text = %q, want %q", e.Text, "The answer is 42.")
			}
		}
	}
	if !foundThinking {
		t.Error("expected EventThinking")
	}
	if !foundText {
		t.Error("expected EventText")
	}
}

func TestGeminiStreamChat_ToolCall(t *testing.T) {
	resp := sseEvent(ssePartSpec{
		FunctionCall: &geminiFunctionCall{
			Name: "list_dir",
			Args: map[string]any{"path": "src"},
		},
	})

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "list files"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, err := collectEvents(ch)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	var foundToolUse, foundDone bool
	for _, e := range events {
		switch e.Type {
		case EventToolUse:
			foundToolUse = true
			if e.ToolUse.Name != "list_dir" {
				t.Errorf("tool name = %q, want %q", e.ToolUse.Name, "list_dir")
			}
			// Args should be valid JSON
			var args map[string]any
			if err := json.Unmarshal(e.ToolUse.Args, &args); err != nil {
				t.Errorf("tool args not valid JSON: %v", err)
			}
		case EventDone:
			foundDone = true
			if e.Response.StopReason != StopToolUse {
				t.Errorf("stop reason = %q, want %q", e.Response.StopReason, StopToolUse)
			}
		}
	}
	if !foundToolUse {
		t.Error("expected EventToolUse")
	}
	if !foundDone {
		t.Error("expected EventDone")
	}
}

func TestGeminiStreamChat_ThinkingWithToolCall(t *testing.T) {
	resp := sseEvent(
		ssePartSpec{Text: "I need to check the files", Thought: new(true)},
	) + sseEvent(
		ssePartSpec{
			FunctionCall: &geminiFunctionCall{
				Name: "read_file",
				Args: map[string]any{"path": "main.go"},
			},
		},
	)

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		System: "be helpful",
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "show me main.go"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, err := collectEvents(ch)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	var foundThinking, foundToolUse bool
	for _, e := range events {
		switch e.Type {
		case EventThinking:
			foundThinking = true
		case EventToolUse:
			foundToolUse = true
		}
	}
	if !foundThinking {
		t.Error("expected EventThinking")
	}
	if !foundToolUse {
		t.Error("expected EventToolUse")
	}

	// EventDone should contain all content blocks
	for _, e := range events {
		if e.Type == EventDone {
			if len(e.Response.Content) < 2 {
				t.Errorf("expected at least 2 content blocks, got %d", len(e.Response.Content))
			}
			if e.Response.StopReason != StopToolUse {
				t.Errorf("stop reason = %q, want %q", e.Response.StopReason, StopToolUse)
			}
		}
	}
}

func TestGeminiStreamChat_EmptyPartsFiltered(t *testing.T) {
	// Simulate streaming chunks where some parts have no data
	resp := sseEvent(
		ssePartSpec{}, // empty part — should be filtered
		ssePartSpec{
			FunctionCall: &geminiFunctionCall{
				Name: "list_dir",
				Args: map[string]any{"path": "."},
			},
		},
	)

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "list"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, err := collectEvents(ch)
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	// Should have EventToolUse + EventDone, no empty events
	for _, e := range events {
		if e.Type == EventDone {
			for i, block := range e.Response.Content {
				switch block.(type) {
				case TextBlock:
					if block.(TextBlock).Text == "" {
						t.Errorf("empty TextBlock at index %d", i)
					}
				}
			}
		}
	}
}

func TestGeminiStreamChat_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"bad request"}}`)
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	_, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should contain status code, got: %v", err)
	}
}

// TestGeminiStreamChat_429ReturnsTransientError pins the contract that
// a bare StreamChat call returns a *TransientError on 429 — without any
// inline retry. Callers that want retries wrap in RetryTransient.
func TestGeminiStreamChat_429ReturnsTransientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"rate limited","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7s"}]}}`)
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	_, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err == nil {
		t.Fatal("expected error on 429, got nil")
	}
	var te *TransientError
	if !errors.As(err, &te) {
		t.Fatalf("expected *TransientError, got %T: %v", err, err)
	}
	if te.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v, want 7s", te.RetryAfter)
	}
	if !IsTransientError(err, context.Background()) {
		t.Error("IsTransientError should report true for a 429 TransientError")
	}
}

// TestGeminiStreamChat_RetryTransientHonoursRetryAfter integrates the
// retry path: a server that returns 429 with retryDelay=0s then 200
// is recovered by RetryTransient. Mirrors the production wrap used by
// every call site in review/, audit/, security/, project/, etc.
func TestGeminiStreamChat_RetryTransientHonoursRetryAfter(t *testing.T) {
	var mu sync.Mutex
	attempts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()

		if n <= 1 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"rate limited","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "retried ok"}))
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	resp, err := RetryTransient(context.Background(), 3, "gemini-test", func(ctx context.Context) (*ChatResponse, error) {
		return provider.Chat(ctx, ChatRequest{
			Messages: []ProviderMessage{
				{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
			},
		})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Content) == 0 {
		t.Fatal("empty response after retry")
	}
	tb, ok := resp.Content[0].(TextBlock)
	if !ok || tb.Text != "retried ok" {
		t.Errorf("got %#v, want TextBlock{Text:\"retried ok\"}", resp.Content[0])
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestGeminiStreamChat_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		<-r.Context().Done()
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := provider.StreamChat(ctx, ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})

	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestGeminiStreamChat_SystemPrompt(t *testing.T) {
	var mu sync.Mutex
	var requestBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		mu.Lock()
		requestBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "ok"}))
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		System: "You are a code reviewer",
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "review this"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for range ch {
	} // drain

	mu.Lock()
	defer mu.Unlock()

	if !strings.Contains(requestBody, "systemInstruction") {
		t.Error("request should contain systemInstruction")
	}
	if !strings.Contains(requestBody, "You are a code reviewer") {
		t.Error("request should contain the system prompt text")
	}
}

func TestGeminiStreamChat_NoSystemPrompt(t *testing.T) {
	var mu sync.Mutex
	var requestBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		mu.Lock()
		requestBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "ok"}))
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for range ch {
	} // drain

	mu.Lock()
	defer mu.Unlock()

	if strings.Contains(requestBody, "systemInstruction") {
		t.Error("request should NOT contain systemInstruction when prompt is empty")
	}
}

func TestGeminiTranslation_ThinkingBlockEchoedBack(t *testing.T) {
	var mu sync.Mutex
	var requestBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		mu.Lock()
		requestBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "ok"}))
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	// Simulate echoing a previous turn with thinking blocks
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "question"}}},
			{Role: RoleAssistant, Content: []ContentBlock{
				ThinkingBlock{Text: "let me think...", Signature: "sig123"},
				TextBlock{Text: "answer"},
			}},
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "follow up"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for range ch {
	} // drain

	mu.Lock()
	defer mu.Unlock()

	if !strings.Contains(requestBody, `"thought":true`) {
		t.Error("request should contain thought:true")
	}
	if !strings.Contains(requestBody, "let me think...") {
		t.Error("request should echo thought text")
	}
}

func TestGeminiTranslation_ToolResultToFunctionResponse(t *testing.T) {
	var mu sync.Mutex
	var requestBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		mu.Lock()
		requestBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "ok"}))
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	// Simulate a conversation with tool results
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "list files"}}},
			{Role: RoleAssistant, Content: []ContentBlock{
				ToolUseBlock{ID: "call_1", Name: "list_dir", Args: json.RawMessage(`{"path":"src"}`)},
			}},
			{Role: RoleUser, Content: []ContentBlock{
				ToolResultBlock{ToolUseID: "call_1", Name: "list_dir", Content: "file1.go\nfile2.go"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for range ch {
	} // drain

	mu.Lock()
	defer mu.Unlock()

	if !strings.Contains(requestBody, "functionCall") {
		t.Error("request should contain functionCall")
	}
	if !strings.Contains(requestBody, "functionResponse") {
		t.Error("request should contain functionResponse")
	}
	if !strings.Contains(requestBody, "list_dir") {
		t.Error("request should reference list_dir tool")
	}
}

func TestGeminiTranslation_ToolDefs(t *testing.T) {
	var mu sync.Mutex
	var requestBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		mu.Lock()
		requestBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "ok"}))
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hello"}}},
		},
		Tools: []ToolDef{
			{
				Name:        "test_tool",
				Description: "A test tool",
				Parameters: ToolParams{
					Type: "object",
					Properties: map[string]ToolParam{
						"arg1": {Type: "string", Description: "first arg"},
					},
					Required: []string{"arg1"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for range ch {
	} // drain

	mu.Lock()
	defer mu.Unlock()

	// Verify Gemini-format tool declarations
	if !strings.Contains(requestBody, "functionDeclarations") {
		t.Error("request should contain functionDeclarations")
	}
	if !strings.Contains(requestBody, "test_tool") {
		t.Error("request should contain tool name")
	}
	// Gemini uses uppercase types
	if !strings.Contains(requestBody, "STRING") {
		t.Error("request should contain uppercase STRING type")
	}
	if !strings.Contains(requestBody, "OBJECT") {
		t.Error("request should contain uppercase OBJECT type")
	}
}

// ══════════════════════════════════════════════════════════════════════════
// Fixture-based translation tests
// ══════════════════════════════════════════════════════════════════════════

// TestGeminiTranslation_MessagesToNative verifies canonical→native message
// translation preserves roles, text, thinking blocks, tool calls, and tool
// results in the correct Gemini format.
func TestGeminiTranslation_MessagesToNative(t *testing.T) {
	provider := &GeminiProvider{APIKey: "k", Model: "m"}

	tests := []struct {
		name    string
		req     ChatRequest
		checkFn func(t *testing.T, native geminiRequest)
	}{
		{
			name: "system prompt → systemInstruction",
			req: ChatRequest{
				System: "be helpful",
				Messages: []ProviderMessage{
					{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
				},
			},
			checkFn: func(t *testing.T, n geminiRequest) {
				if n.SystemInstruction == nil {
					t.Fatal("expected systemInstruction")
				}
				if len(n.SystemInstruction.Parts) != 1 || n.SystemInstruction.Parts[0].Text != "be helpful" {
					t.Errorf("systemInstruction text = %+v", n.SystemInstruction.Parts)
				}
			},
		},
		{
			name: "no system prompt → nil systemInstruction",
			req: ChatRequest{
				Messages: []ProviderMessage{
					{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
				},
			},
			checkFn: func(t *testing.T, n geminiRequest) {
				if n.SystemInstruction != nil {
					t.Error("expected nil systemInstruction")
				}
			},
		},
		{
			name: "user role → user, assistant role → model",
			req: ChatRequest{
				Messages: []ProviderMessage{
					{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "q"}}},
					{Role: RoleAssistant, Content: []ContentBlock{TextBlock{Text: "a"}}},
				},
			},
			checkFn: func(t *testing.T, n geminiRequest) {
				if len(n.Contents) != 2 {
					t.Fatalf("expected 2 contents, got %d", len(n.Contents))
				}
				if n.Contents[0].Role != "user" {
					t.Errorf("first role = %q, want user", n.Contents[0].Role)
				}
				if n.Contents[1].Role != "model" {
					t.Errorf("second role = %q, want model", n.Contents[1].Role)
				}
			},
		},
		{
			name: "ThinkingBlock → thought:true + signature",
			req: ChatRequest{
				Messages: []ProviderMessage{
					{Role: RoleAssistant, Content: []ContentBlock{
						ThinkingBlock{Text: "reasoning...", Signature: "sig1"},
						TextBlock{Text: "answer"},
					}},
				},
			},
			checkFn: func(t *testing.T, n geminiRequest) {
				parts := n.Contents[0].Parts
				if len(parts) != 2 {
					t.Fatalf("expected 2 parts, got %d", len(parts))
				}
				if parts[0].Thought == nil || !*parts[0].Thought {
					t.Error("first part should have thought=true")
				}
				if parts[0].Text != "reasoning..." {
					t.Errorf("thought text = %q", parts[0].Text)
				}
				if parts[0].ThoughtSignature != "sig1" {
					t.Errorf("thought signature = %q", parts[0].ThoughtSignature)
				}
				if parts[1].Text != "answer" {
					t.Errorf("text = %q", parts[1].Text)
				}
			},
		},
		{
			name: "ToolUseBlock → functionCall",
			req: ChatRequest{
				Messages: []ProviderMessage{
					{Role: RoleAssistant, Content: []ContentBlock{
						ToolUseBlock{ID: "c1", Name: "read_file", Args: json.RawMessage(`{"path":"x.go"}`)},
					}},
				},
			},
			checkFn: func(t *testing.T, n geminiRequest) {
				parts := n.Contents[0].Parts
				if len(parts) != 1 {
					t.Fatalf("expected 1 part, got %d", len(parts))
				}
				fc := parts[0].FunctionCall
				if fc == nil {
					t.Fatal("expected functionCall")
				}
				if fc.Name != "read_file" {
					t.Errorf("functionCall name = %q", fc.Name)
				}
				if fc.ID != "c1" {
					t.Errorf("functionCall ID = %q", fc.ID)
				}
				if fc.Args["path"] != "x.go" {
					t.Errorf("functionCall args = %v", fc.Args)
				}
			},
		},
		{
			name: "ToolResultBlock → functionResponse",
			req: ChatRequest{
				Messages: []ProviderMessage{
					{Role: RoleUser, Content: []ContentBlock{
						ToolResultBlock{ToolUseID: "c1", Name: "read_file", Content: "file content"},
					}},
				},
			},
			checkFn: func(t *testing.T, n geminiRequest) {
				parts := n.Contents[0].Parts
				if len(parts) != 1 {
					t.Fatalf("expected 1 part, got %d", len(parts))
				}
				fr := parts[0].FunctionResponse
				if fr == nil {
					t.Fatal("expected functionResponse")
				}
				if fr.Name != "read_file" {
					t.Errorf("functionResponse name = %q", fr.Name)
				}
				resp, ok := fr.Response.(map[string]string)
				if !ok {
					t.Fatalf("response type = %T", fr.Response)
				}
				if resp["result"] != "file content" {
					t.Errorf("response = %v", resp)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			native := provider.toNativeRequest(tt.req)
			tt.checkFn(t, native)
		})
	}
}

// TestGeminiTranslation_ToolDefsToNative verifies canonical ToolDef →
// Gemini functionDeclarations translation, including type uppercasing,
// nested properties, enums, and required fields.
func TestGeminiTranslation_ToolDefsToNative(t *testing.T) {
	provider := &GeminiProvider{APIKey: "k", Model: "m"}

	tools := []ToolDef{
		{
			Name:        "grep",
			Description: "Search files",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"pattern":     {Type: "string", Description: "regex pattern"},
					"max_results": {Type: "integer", Description: "max"},
					"regex":       {Type: "string", Enum: []string{"true", "false"}},
				},
				Required: []string{"pattern"},
			},
		},
	}

	native := provider.toNativeRequest(ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
		Tools: tools,
	})

	if len(native.Tools) != 1 {
		t.Fatalf("expected 1 tool group, got %d", len(native.Tools))
	}
	fns := native.Tools[0].FunctionDeclarations
	if len(fns) != 1 {
		t.Fatalf("expected 1 function, got %d", len(fns))
	}

	fn := fns[0]
	if fn.Name != "grep" {
		t.Errorf("name = %q", fn.Name)
	}
	if fn.Parameters.Type != "OBJECT" {
		t.Errorf("params type = %q, want OBJECT", fn.Parameters.Type)
	}
	if len(fn.Parameters.Properties) != 3 {
		t.Errorf("expected 3 properties, got %d", len(fn.Parameters.Properties))
	}
	if fn.Parameters.Properties["pattern"].Type != "STRING" {
		t.Errorf("pattern type = %q, want STRING", fn.Parameters.Properties["pattern"].Type)
	}
	if fn.Parameters.Properties["max_results"].Type != "INTEGER" {
		t.Errorf("max_results type = %q, want INTEGER", fn.Parameters.Properties["max_results"].Type)
	}
	if len(fn.Parameters.Properties["regex"].Enum) != 2 {
		t.Errorf("regex enum = %v", fn.Parameters.Properties["regex"].Enum)
	}
	if len(fn.Parameters.Required) != 1 || fn.Parameters.Required[0] != "pattern" {
		t.Errorf("required = %v", fn.Parameters.Required)
	}

	// Verify toolConfig is set when tools are present
	if native.ToolConfig == nil {
		t.Fatal("expected toolConfig when tools are present")
	}
	if native.ToolConfig.FunctionCallingConfig == nil {
		t.Fatal("expected functionCallingConfig")
	}
	if native.ToolConfig.FunctionCallingConfig.Mode != "VALIDATED" {
		t.Errorf("mode = %q, want VALIDATED", native.ToolConfig.FunctionCallingConfig.Mode)
	}

	// Verify safetySettings are always set
	if len(native.SafetySettings) != 4 {
		t.Errorf("expected 4 safety settings, got %d", len(native.SafetySettings))
	}
	for _, s := range native.SafetySettings {
		if s.Threshold != "BLOCK_NONE" {
			t.Errorf("safety %s threshold = %q, want BLOCK_NONE", s.Category, s.Threshold)
		}
	}
}

// TestGeminiTranslation_GenerationConfig verifies that ModelConfig fields
// are correctly translated into the native generationConfig.
func TestGeminiTranslation_GenerationConfig(t *testing.T) {
	t.Run("with thinking enabled", func(t *testing.T) {
		provider := &GeminiProvider{APIKey: "k", Model: "gemini-3.1-pro-preview"}
		provider.ModelConfig.MaxOutputTokens = 65536
		provider.ModelConfig.Temperature = TempPtr(0.2)
		provider.ModelConfig.ThinkingBudget = 8192

		native := provider.toNativeRequest(ChatRequest{
			Messages: []ProviderMessage{
				{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
			},
		})

		gc := native.GenerationConfig
		if gc == nil {
			t.Fatal("expected generationConfig")
		}
		if gc.MaxOutputTokens != 65536 {
			t.Errorf("maxOutputTokens = %d, want 65536", gc.MaxOutputTokens)
		}
		if gc.Temperature == nil || *gc.Temperature != 0.2 {
			t.Errorf("temperature = %v, want 0.2", gc.Temperature)
		}
		if gc.ThinkingConfig == nil {
			t.Fatal("expected thinkingConfig")
		}
		if !gc.ThinkingConfig.IncludeThoughts {
			t.Error("expected includeThoughts=true")
		}
		if gc.ThinkingConfig.ThinkingBudget != 8192 {
			t.Errorf("thinkingBudget = %d, want 8192", gc.ThinkingConfig.ThinkingBudget)
		}
	})

	t.Run("explicit zero temperature is sent", func(t *testing.T) {
		// Pin the bugfix: *float64 lets a caller request greedy
		// decoding (0). The earlier `Temperature > 0` check dropped 0
		// silently as "unset".
		zero := 0.0
		provider := &GeminiProvider{APIKey: "k", Model: "m"}
		provider.ModelConfig.Temperature = &zero
		native := provider.toNativeRequest(ChatRequest{
			Messages: []ProviderMessage{
				{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
			},
		})
		gc := native.GenerationConfig
		if gc == nil || gc.Temperature == nil {
			t.Fatalf("expected temperature on the wire, got gc=%v", gc)
		}
		if *gc.Temperature != 0 {
			t.Errorf("temperature = %v, want 0 (greedy)", *gc.Temperature)
		}
	})

	t.Run("nil temperature is omitted", func(t *testing.T) {
		provider := &GeminiProvider{APIKey: "k", Model: "m"}
		// ModelConfig.Temperature left nil
		native := provider.toNativeRequest(ChatRequest{
			Messages: []ProviderMessage{
				{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
			},
		})
		gc := native.GenerationConfig
		if gc != nil && gc.Temperature != nil {
			t.Errorf("temperature should be omitted when ModelConfig.Temperature is nil, got %v", *gc.Temperature)
		}
	})

	t.Run("without thinking (legacy model)", func(t *testing.T) {
		provider := &GeminiProvider{APIKey: "k", Model: "gemini-1.5-pro"}
		provider.ModelConfig.MaxOutputTokens = 8192
		provider.ModelConfig.Temperature = TempPtr(0.2)
		provider.ModelConfig.ThinkingBudget = 0

		native := provider.toNativeRequest(ChatRequest{
			Messages: []ProviderMessage{
				{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
			},
		})

		gc := native.GenerationConfig
		if gc == nil {
			t.Fatal("expected generationConfig")
		}
		if gc.MaxOutputTokens != 8192 {
			t.Errorf("maxOutputTokens = %d, want 8192", gc.MaxOutputTokens)
		}
		if gc.ThinkingConfig != nil {
			t.Error("expected no thinkingConfig for legacy model")
		}
	})

	t.Run("no tools → no toolConfig", func(t *testing.T) {
		provider := &GeminiProvider{APIKey: "k", Model: "m"}

		native := provider.toNativeRequest(ChatRequest{
			Messages: []ProviderMessage{
				{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
			},
		})

		if native.ToolConfig != nil {
			t.Error("expected no toolConfig when no tools")
		}
	})
}

// TestGeminiTranslation_ResponseEvents verifies that SSE stream parsing
// produces the correct sequence of canonical ChatEvents for various
// Gemini response shapes.
func TestGeminiTranslation_ResponseEvents(t *testing.T) {
	tests := []struct {
		name   string
		sse    string
		checks func(t *testing.T, events []ChatEvent)
	}{
		{
			name: "text only → EventText + EventDone(EndTurn)",
			sse:  sseEvent(ssePartSpec{Text: "hello"}),
			checks: func(t *testing.T, events []ChatEvent) {
				var texts []string
				for _, e := range events {
					if e.Type == EventText {
						texts = append(texts, e.Text)
					}
					if e.Type == EventDone {
						if e.Response.StopReason != StopEndTurn {
							t.Errorf("stop = %q, want end_turn", e.Response.StopReason)
						}
					}
				}
				if len(texts) != 1 || texts[0] != "hello" {
					t.Errorf("texts = %v", texts)
				}
			},
		},
		{
			name: "tool call → EventToolUse + EventDone(ToolUse)",
			sse: sseEvent(ssePartSpec{
				FunctionCall: &geminiFunctionCall{Name: "read_file", Args: map[string]any{"path": "x"}},
			}),
			checks: func(t *testing.T, events []ChatEvent) {
				var foundTool bool
				for _, e := range events {
					if e.Type == EventToolUse {
						foundTool = true
						if e.ToolUse.Name != "read_file" {
							t.Errorf("tool name = %q", e.ToolUse.Name)
						}
					}
					if e.Type == EventDone {
						if e.Response.StopReason != StopToolUse {
							t.Errorf("stop = %q, want tool_use", e.Response.StopReason)
						}
					}
				}
				if !foundTool {
					t.Error("expected EventToolUse")
				}
			},
		},
		{
			name: "thinking + text → EventThinking + EventText",
			sse: sseEvent(ssePartSpec{Text: "think", Thought: new(true)}) +
				sseEvent(ssePartSpec{Text: "answer"}),
			checks: func(t *testing.T, events []ChatEvent) {
				var thinking, text bool
				for _, e := range events {
					if e.Type == EventThinking {
						thinking = true
					}
					if e.Type == EventText {
						text = true
					}
				}
				if !thinking {
					t.Error("expected EventThinking")
				}
				if !text {
					t.Error("expected EventText")
				}
			},
		},
		{
			name: "synthetic tool ID when Gemini omits it",
			sse: sseEvent(ssePartSpec{
				FunctionCall: &geminiFunctionCall{Name: "list_dir", Args: map[string]any{}},
			}),
			checks: func(t *testing.T, events []ChatEvent) {
				for _, e := range events {
					if e.Type == EventToolUse {
						if e.ToolUse.ID == "" {
							t.Error("expected synthetic ID, got empty")
						}
						if !strings.HasPrefix(e.ToolUse.ID, "call_") {
							t.Errorf("synthetic ID should start with 'call_', got %q", e.ToolUse.ID)
						}
					}
				}
			},
		},
		{
			name: "multiple tool calls in one response",
			sse: sseEvent(
				ssePartSpec{FunctionCall: &geminiFunctionCall{Name: "read_file", Args: map[string]any{"path": "a"}}},
				ssePartSpec{FunctionCall: &geminiFunctionCall{Name: "read_file", Args: map[string]any{"path": "b"}}},
			),
			checks: func(t *testing.T, events []ChatEvent) {
				toolCount := 0
				for _, e := range events {
					if e.Type == EventToolUse {
						toolCount++
					}
					if e.Type == EventDone {
						// Should have 2 ToolUseBlocks in content
						tuCount := 0
						for _, b := range e.Response.Content {
							if _, ok := b.(ToolUseBlock); ok {
								tuCount++
							}
						}
						if tuCount != 2 {
							t.Errorf("expected 2 ToolUseBlocks in response, got %d", tuCount)
						}
					}
				}
				if toolCount != 2 {
					t.Errorf("expected 2 EventToolUse events, got %d", toolCount)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer([]string{tt.sse})
			defer srv.Close()
			provider := newTestProvider(srv.URL)

			ch, err := provider.StreamChat(context.Background(), ChatRequest{
				Messages: []ProviderMessage{
					{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "test"}}},
				},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			events, err := collectEvents(ch)
			if err != nil {
				t.Fatalf("stream error: %v", err)
			}
			tt.checks(t, events)
		})
	}
}

// ── Thought signature tests ─────────────────────────────────────────────

func TestGeminiStreamChat_ThoughtSignatureSeparatePart(t *testing.T) {
	// Gemini sometimes sends the thoughtSignature in a separate part with
	// empty text after the thinking content. Verify the signature is captured
	// and attached to the ThinkingBlock so it can be echoed back.
	resp := sseEvent(
		ssePartSpec{Text: "Let me analyze this", Thought: new(true)},
	) + sseEvent(
		// Signature-only part: thought=true, no text, just signature
		ssePartSpec{Thought: new(true), ThoughtSignature: "opaque-sig-abc123"},
	) + sseEvent(
		ssePartSpec{
			FunctionCall: &geminiFunctionCall{
				Name: "read_base_file",
				Args: map[string]any{"path": "main.go"},
			},
		},
	)

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "read base file"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, err := collectEvents(ch)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	// Verify we got thinking + tool call events
	var foundThinking, foundTool bool
	for _, e := range events {
		switch e.Type {
		case EventThinking:
			foundThinking = true
		case EventToolUse:
			foundTool = true
		}
	}
	if !foundThinking {
		t.Error("expected EventThinking")
	}
	if !foundTool {
		t.Error("expected EventToolUse")
	}

	// Critical: verify the ThinkingBlock has the signature from the separate part
	for _, e := range events {
		if e.Type == EventDone {
			var thinkingBlocks []ThinkingBlock
			for _, block := range e.Response.Content {
				if tb, ok := block.(ThinkingBlock); ok {
					thinkingBlocks = append(thinkingBlocks, tb)
				}
			}
			if len(thinkingBlocks) == 0 {
				t.Fatal("expected at least one ThinkingBlock in response content")
			}
			// The signature should be attached to the thinking block
			foundSig := false
			for _, tb := range thinkingBlocks {
				if tb.Signature == "opaque-sig-abc123" {
					foundSig = true
				}
			}
			if !foundSig {
				t.Errorf("expected signature 'opaque-sig-abc123' on ThinkingBlock, got blocks: %+v", thinkingBlocks)
			}
		}
	}
}

func TestGeminiStreamChat_ThoughtSignatureOnSamePart(t *testing.T) {
	// When the signature is on the same part as the thinking text, it should
	// be captured directly without needing the separate-part logic.
	resp := sseEvent(
		ssePartSpec{
			Text:             "I should check the old version",
			Thought:          new(true),
			ThoughtSignature: "inline-sig-xyz",
		},
	) + sseEvent(
		ssePartSpec{
			FunctionCall: &geminiFunctionCall{
				Name: "read_base_file",
				Args: map[string]any{"path": "util.go"},
			},
		},
	)

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "compare files"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, err := collectEvents(ch)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	for _, e := range events {
		if e.Type == EventDone {
			for _, block := range e.Response.Content {
				if tb, ok := block.(ThinkingBlock); ok {
					if tb.Signature != "inline-sig-xyz" {
						t.Errorf("ThinkingBlock.Signature = %q, want %q", tb.Signature, "inline-sig-xyz")
					}
					if tb.Text != "I should check the old version" {
						t.Errorf("ThinkingBlock.Text = %q", tb.Text)
					}
					return
				}
			}
			t.Error("expected ThinkingBlock in response content")
		}
	}
}

func TestGeminiTranslation_ThoughtSignatureEchoedBackForToolCall(t *testing.T) {
	// Simulate the full round-trip: the model returns thinking with a signature
	// and a tool call. After tool execution, the agent echoes the assistant turn
	// back. Verify the Gemini native request includes the thoughtSignature.
	var mu sync.Mutex
	var requestBodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		mu.Lock()
		requestBodies = append(requestBodies, body)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		// First call: return thinking + tool call
		// Second call: return text
		mu.Lock()
		n := len(requestBodies)
		mu.Unlock()
		if n == 1 {
			fmt.Fprint(w,
				sseEvent(ssePartSpec{Text: "checking", Thought: new(true)})+
					sseEvent(ssePartSpec{Thought: new(true), ThoughtSignature: "echo-me-back"})+
					sseEvent(ssePartSpec{FunctionCall: &geminiFunctionCall{
						Name: "read_base_file",
						Args: map[string]any{"path": "go.mod"},
					}}),
			)
		} else {
			fmt.Fprint(w, sseEvent(ssePartSpec{Text: "done"}))
		}
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)
	exec := &ToolExecutor{
		HeadRef: "HEAD",
		BaseRef: "HEAD",
		gitRunner: func(args ...string) (string, error) {
			return "module test\n", nil
		},
	}
	agent := NewAgent(provider, exec)

	_, err := agent.ChatStream(context.Background(), "test", []Message{
		{Role: "user", Content: "read base file"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(requestBodies) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(requestBodies))
	}

	// The second request should echo back the thinking block with the signature
	secondReq := requestBodies[1]
	if !strings.Contains(secondReq, "echo-me-back") {
		t.Error("second request should contain thoughtSignature 'echo-me-back'")
	}
	if !strings.Contains(secondReq, `"thought":true`) {
		t.Error("second request should contain thought:true")
	}
	if !strings.Contains(secondReq, "functionResponse") {
		t.Error("second request should contain functionResponse (tool result)")
	}
}

func TestGeminiStreamChat_MultipleThinkingChunksSignatureOnLast(t *testing.T) {
	// Multiple thinking chunks where only the last one has the signature
	resp := sseEvent(ssePartSpec{Text: "First thought", Thought: new(true)}) +
		sseEvent(ssePartSpec{Text: "Second thought", Thought: new(true)}) +
		sseEvent(ssePartSpec{Thought: new(true), ThoughtSignature: "multi-sig"}) +
		sseEvent(ssePartSpec{FunctionCall: &geminiFunctionCall{
			Name: "grep",
			Args: map[string]any{"pattern": "func"},
		}})

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "search"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, err := collectEvents(ch)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	for _, e := range events {
		if e.Type != EventDone {
			continue
		}
		// The signature should be on the last ThinkingBlock (second thought)
		var lastTB *ThinkingBlock
		for _, block := range e.Response.Content {
			if tb, ok := block.(ThinkingBlock); ok {
				tbCopy := tb
				lastTB = &tbCopy
			}
		}
		if lastTB == nil {
			t.Fatal("expected at least one ThinkingBlock")
		}
		if lastTB.Signature != "multi-sig" {
			t.Errorf("last ThinkingBlock.Signature = %q, want %q", lastTB.Signature, "multi-sig")
		}
	}
}

// ── Provider interface compliance ───────────────────────────────────────

func TestGeminiProvider_ImplementsProvider(t *testing.T) {
	var _ Provider = (*GeminiProvider)(nil)
}

func TestGeminiProvider_Name(t *testing.T) {
	p := &GeminiProvider{APIKey: "k", Model: "gemini-3.1-flash-lite"}
	if p.Name() != "gemini" {
		t.Errorf("Name() = %q", p.Name())
	}
}

func TestGeminiProvider_ModelID(t *testing.T) {
	p := &GeminiProvider{APIKey: "k", Model: "gemini-3.1-flash-lite"}
	if p.ModelID() != "gemini-3.1-flash-lite" {
		t.Errorf("ModelID() = %q", p.ModelID())
	}
}

func TestGeminiProvider_Capabilities(t *testing.T) {
	p := &GeminiProvider{APIKey: "k", Model: "m"}
	caps := p.Capabilities()
	if !caps.PromptCaching {
		t.Error("expected PromptCaching=true")
	}
	if !caps.StructuredOutput {
		t.Error("expected StructuredOutput=true")
	}
	if !caps.ParallelToolCalls {
		t.Error("expected ParallelToolCalls=true")
	}
	if caps.MaxContextTokens != 1_000_000 {
		t.Errorf("MaxContextTokens = %d", caps.MaxContextTokens)
	}
}

// TestGeminiStreamChat_ResponseSchemaOnTheWire pins the structured-output
// wiring: ChatRequest.JSONSchema → generationConfig.responseMimeType +
// generationConfig.responseSchema in the outbound request body.
func TestGeminiStreamChat_ResponseSchemaOnTheWire(t *testing.T) {
	var mu sync.Mutex
	var body string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := readBody(r)
		mu.Lock()
		body = b
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "{}"}))
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	schema := json.RawMessage(`{"type":"object","properties":{"verdict":{"type":"string"}}}`)
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "score"}}},
		},
		JSONSchema: &JSONSchema{Name: "verdict", Schema: schema},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range ch {
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(body, `"responseMimeType":"application/json"`) {
		t.Errorf("request body missing responseMimeType, body=%s", body)
	}
	if !strings.Contains(body, `"responseSchema"`) {
		t.Errorf("request body missing responseSchema, body=%s", body)
	}
	if !strings.Contains(body, `"verdict"`) {
		t.Errorf("request body missing schema content, body=%s", body)
	}
}

// TestGeminiStreamChat_NoSchemaOmitsResponseFields pins that without a
// JSONSchema, no responseSchema / responseMimeType is sent (avoids
// constraining unrelated calls).
func TestGeminiStreamChat_NoSchemaOmitsResponseFields(t *testing.T) {
	var mu sync.Mutex
	var body string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := readBody(r)
		mu.Lock()
		body = b
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "ok"}))
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range ch {
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(body, "responseMimeType") {
		t.Errorf("responseMimeType should be omitted, body=%s", body)
	}
	if strings.Contains(body, "responseSchema") {
		t.Errorf("responseSchema should be omitted, body=%s", body)
	}
}

// ── Live API tests ─────────────────────────────────────────────────────
// These hit the real Gemini API. Skipped unless PRR_LIVE_TESTS=1 is set.
// Credentials are read from ~/.config/prr/config.json.
// Run with: PRR_LIVE_TESTS=1 go test ./internal/ai/ -run TestLive -v

func skipWithoutAPIKey(t *testing.T) string {
	t.Helper()
	if os.Getenv("PRR_LIVE_TESTS") != "1" {
		t.Skip("PRR_LIVE_TESTS=1 not set, skipping live API test")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("no valid config: %v", err)
	}
	key := cfg.APIKeyFor("gemini")
	if key == "" {
		t.Skip("no gemini API key in config, skipping live API test")
	}
	return key
}

func liveModel() string {
	if m := os.Getenv("PRR_MODEL"); m != "" {
		return m
	}
	return "gemini-3.1-flash-lite"
}

func TestLive_SimpleChat(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
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
	t.Logf("Result: %q (%d tokens)", result, len(tokens))
}

func TestLive_ToolCall(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You have access to tools. Use the list_dir tool to see what files exist, then describe what you found. Be brief.",
		[]Message{
			{Role: "user", Content: "What files are in the root directory?"},
		}, func(s string) {
			tokens = append(tokens, s)
		})

	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
	t.Logf("Result: %q (%d tokens)", result, len(tokens))

	joined := strings.Join(tokens, "")
	if !strings.Contains(joined, "list_dir") {
		t.Logf("Warning: expected tool call indicator in tokens, got: %s", joined)
	}
}

func TestLive_OverviewReview(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	diffContext := `PR #42: Add greeting utilities
Description:
Refactor main to use a new greet function and add a reverse utility.
Base: main → Head: feature/greet

Files changed (2):
  main.go                                            +10  -2
  util.go                                            +5   -0

=== main.go ===
@@ -1,5 +1,13 @@
 package main
+import "fmt"
 func main() {
-    println("hello")
+    fmt.Println(greet("world"))
+}
+func greet(name string) string {
+    return "Hello, " + name
 }

=== util.go ===
@@ -0,0 +1,5 @@
+package main
+func reverse(s string) string {
+    // TODO: implement
+    return s
+}`

	systemPrompt := ReviewBatchPrompt + "\n\n" + diffContext

	var tokens []string
	result, err := agent.ChatStream(ctx, systemPrompt,
		[]Message{{Role: "user", Content: "Please review the full set of changes in this PR."}},
		func(s string) {
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
	t.Logf("Result (%d tokens, %d chars): %s", len(tokens), len(result), result[:min(len(result), 200)])
}

// ── Live tool-specific tests ────────────────────────────────────────────
// These exercise individual tools through the full agent loop with the
// real Gemini API and real git commands. Each test verifies that the tool
// is actually invoked (via TOOL_START/TOOL_DONE tokens) and produces a
// meaningful result.

func TestLive_Tool_ReadFile(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the read_file tool to answer. Be brief.",
		[]Message{{Role: "user", Content: "Read the file go.mod and tell me the module name."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "read_file")
	if !strings.Contains(strings.ToLower(result), "prr") {
		t.Errorf("expected 'prr' in result, got: %s", result)
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_ReadBaseFile(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the read_base_file tool to answer. Be brief.",
		[]Message{{Role: "user", Content: "Read the base version of go.mod and tell me the module name."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "read_base_file")
	if !strings.Contains(strings.ToLower(result), "prr") {
		t.Errorf("expected 'prr' in result, got: %s", result)
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_ListDir(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the list_dir tool to answer. Be brief.",
		[]Message{{Role: "user", Content: "List the root directory and tell me the directory names you see."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "list_dir")
	if result == "" {
		t.Error("expected non-empty result")
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_Glob(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the glob tool to answer. Be brief.",
		[]Message{{Role: "user", Content: "Find all Go test files (pattern **/*_test.go) and count how many there are."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "glob")
	if result == "" {
		t.Error("expected non-empty result")
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_Grep(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the grep tool to answer. Be brief.",
		[]Message{{Role: "user", Content: "Search for the pattern 'ToolExecutor' in Go files and list the files containing it."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "grep")
	if !strings.Contains(result, "tools") {
		t.Logf("Warning: expected 'tools' mentioned in result, got: %s", truncForLog(result))
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_GitDiff(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD~1"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the git_diff tool to answer. Be brief. If no diff is found, say 'no changes'.",
		[]Message{{Role: "user", Content: "Show the diff between the base and head and summarize what changed."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "git_diff")
	if result == "" {
		t.Error("expected non-empty result")
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_GitLog(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD~3"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the git_log tool to answer. Be brief.",
		[]Message{{Role: "user", Content: "Show the recent commit log and tell me how many commits you see."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "git_log")
	if result == "" {
		t.Error("expected non-empty result")
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_GitShow(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the git_show tool to answer. Be brief.",
		[]Message{{Role: "user", Content: "Show the details of commit HEAD and tell me the commit message."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "git_show")
	if result == "" {
		t.Error("expected non-empty result")
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_GitBlame(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the git_blame tool to answer. Be brief.",
		[]Message{{Role: "user", Content: "Blame the first 5 lines of go.mod and tell me who last modified them."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "git_blame")
	if result == "" {
		t.Error("expected non-empty result")
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_GitStatus(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the git_status tool to answer. Be brief.",
		[]Message{{Role: "user", Content: "Check the git status and tell me if the working tree is clean or has changes."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "git_status")
	if result == "" {
		t.Error("expected non-empty result")
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_GetReview(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	exec := &ToolExecutor{
		HeadRef:      "HEAD",
		BaseRef:      "HEAD",
		ReviewGetter: func() string { return "LGTM - code looks good, no issues found." },
	}
	agent := NewAgent(provider, exec)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You must use the get_review tool to answer. Be brief.",
		[]Message{{Role: "user", Content: "What does the review say?"}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	assertToolUsed(t, tokens, "get_review")
	if !strings.Contains(strings.ToLower(result), "lgtm") && !strings.Contains(strings.ToLower(result), "good") {
		t.Logf("Warning: expected review content in result, got: %s", truncForLog(result))
	}
	t.Logf("Result: %s", truncForLog(result))
}

func TestLive_Tool_MultiTool(t *testing.T) {
	// Exercise multiple tools in a single conversation to test the full
	// agent loop including thinking, tool calls, and multi-round behavior.
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: liveModel()}
	agent := NewAgent(provider, &ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD"})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var tokens []string
	result, err := agent.ChatStream(ctx,
		"You have tools available. Use them to answer. Be brief (2-3 sentences max).",
		[]Message{{Role: "user", Content: "First list the root directory, then read go.mod, and tell me the module name and Go version."}},
		func(s string) { tokens = append(tokens, s) },
	)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	// Verify multiple tools were used
	toolsUsed := map[string]bool{}
	for _, tok := range tokens {
		if after, ok := strings.CutPrefix(tok, "\x00TOOL_START:"); ok {
			name := strings.SplitN(after, "(", 2)[0]
			toolsUsed[name] = true
		}
	}
	if len(toolsUsed) < 2 {
		t.Errorf("expected at least 2 tools used, got %d: %v", len(toolsUsed), toolsUsed)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
	t.Logf("Tools used: %v", toolsUsed)
	t.Logf("Result: %s", truncForLog(result))
}

// ── Live test helpers ───────────────────────────────────────────────────

func assertToolUsed(t *testing.T, tokens []string, toolName string) {
	t.Helper()
	var foundStart, foundDone bool
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "\x00TOOL_START:"+toolName) {
			foundStart = true
		}
		if strings.HasPrefix(tok, "\x00TOOL_DONE:"+toolName) {
			foundDone = true
		}
	}
	if !foundStart {
		t.Errorf("expected TOOL_START for %s in tokens", toolName)
	}
	if !foundDone {
		t.Errorf("expected TOOL_DONE for %s in tokens", toolName)
	}
}

func truncForLog(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// sseEvent formats a geminiStreamResponse as an SSE "data:" line.
func sseEvent(parts ...ssePartSpec) string {
	type partJSON struct {
		Text         string              `json:"text,omitempty"`
		Thought      *bool               `json:"thought,omitempty"`
		FunctionCall *geminiFunctionCall  `json:"functionCall,omitempty"`
	}

	var pp []partJSON
	for _, p := range parts {
		pj := partJSON{
			Text:         p.Text,
			Thought:      p.Thought,
			FunctionCall: p.FunctionCall,
		}
		pp = append(pp, pj)
	}

	resp := map[string]interface{}{
		"candidates": []map[string]interface{}{
			{
				"content": map[string]interface{}{
					"parts": pp,
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return "data: " + string(b) + "\n\n"
}

type ssePartSpec struct {
	Text         string
	Thought      *bool
	FunctionCall *geminiFunctionCall
}

func boolPtr(v bool) *bool { return &v }

// newTestServer creates an httptest.Server that replays a sequence of SSE responses.
// Each call to the endpoint consumes the next response in the sequence.
func newTestServer(responses []string) *httptest.Server {
	var mu sync.Mutex
	idx := 0

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify basic request structure
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

// newTestClient creates a GeminiClient pointing at the test server.
func newTestClient(serverURL string, toolExec *ToolExecutor) *GeminiClient {
	return &GeminiClient{
		APIKey:       "test-key",
		Model:        "test-model",
		ToolExecutor: toolExec,
		BaseURL:      serverURL,
	}
}

// --- Tests ---

func TestChatStream_SimpleText(t *testing.T) {
	resp := sseEvent(ssePartSpec{Text: "Hello "}) +
		sseEvent(ssePartSpec{Text: "world!"})

	srv := newTestServer([]string{resp})
	defer srv.Close()

	client := newTestClient(srv.URL, nil)

	var tokens []string
	result, err := client.ChatStream(context.Background(), "system", []Message{
		{Role: "user", Content: "hi"},
	}, func(s string) {
		tokens = append(tokens, s)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Hello world!" {
		t.Errorf("got result %q, want %q", result, "Hello world!")
	}
	if len(tokens) != 2 {
		t.Errorf("got %d token callbacks, want 2", len(tokens))
	}
}

func TestChatStream_ThinkingPartsNotStreamed(t *testing.T) {
	resp := sseEvent(ssePartSpec{Text: "let me think...", Thought: boolPtr(true)}) +
		sseEvent(ssePartSpec{Text: "The answer is 42."})

	srv := newTestServer([]string{resp})
	defer srv.Close()

	client := newTestClient(srv.URL, nil)

	var tokens []string
	result, err := client.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "question"},
	}, func(s string) {
		tokens = append(tokens, s)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only visible text should be in the result (thoughts excluded from result)
	if result != "The answer is 42." {
		t.Errorf("got result %q, want %q", result, "The answer is 42.")
	}
	// 2 token callbacks: thought (with marker) + visible text
	if len(tokens) != 2 {
		t.Errorf("got %d token callbacks, want 2; tokens: %v", len(tokens), tokens)
	}
	// First token should be the thought with marker prefix
	if len(tokens) > 0 && !strings.HasPrefix(tokens[0], "\x00THOUGHT:") {
		t.Errorf("expected thought marker prefix, got: %q", tokens[0])
	}
}

func TestChatStream_ToolCall(t *testing.T) {
	// Round 1: model returns a tool call
	round1 := sseEvent(ssePartSpec{
		FunctionCall: &geminiFunctionCall{
			Name: "list_files",
			Args: map[string]interface{}{"path": "src"},
		},
	})
	// Round 2: model returns text after receiving tool result
	round2 := sseEvent(ssePartSpec{Text: "I found 3 files."})

	var mu sync.Mutex
	var requestBodies []string
	callIdx := 0
	responses := []string{round1, round2}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		mu.Lock()
		requestBodies = append(requestBodies, body)
		idx := callIdx
		callIdx++
		mu.Unlock()

		if idx >= len(responses) {
			http.Error(w, "no more", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responses[idx])
	}))
	defer srv.Close()

	client := &GeminiClient{
		APIKey:       "test-key",
		Model:        "test-model",
		BaseURL:      srv.URL,
		ToolExecutor: &ToolExecutor{},
	}

	result, err := client.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "list files"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "I found 3 files." {
		t.Errorf("got result %q, want %q", result, "I found 3 files.")
	}

	// Verify round 2 request contains the function response
	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) < 2 {
		t.Fatalf("expected 2 requests, got %d", len(requestBodies))
	}
	if !strings.Contains(requestBodies[1], "functionResponse") {
		t.Errorf("round 2 request should contain functionResponse, got: %s", requestBodies[1])
	}
	if !strings.Contains(requestBodies[1], "list_files") {
		t.Errorf("round 2 request should reference list_files tool")
	}
}

func TestChatStream_ThinkingWithToolCall(t *testing.T) {
	// This is the exact scenario that was broken: thinking model + tool call.
	// Round 1: thought text + function call
	round1 := sseEvent(
		ssePartSpec{Text: "I need to check the files", Thought: boolPtr(true)},
	) + sseEvent(
		ssePartSpec{
			FunctionCall: &geminiFunctionCall{
				Name: "read_file",
				Args: map[string]interface{}{"path": "main.go"},
			},
		},
	)
	// Round 2: final answer
	round2 := sseEvent(ssePartSpec{Text: "Here is the file content."})

	var mu sync.Mutex
	var requestBodies []string
	callIdx := 0
	responses := []string{round1, round2}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		mu.Lock()
		requestBodies = append(requestBodies, body)
		idx := callIdx
		callIdx++
		mu.Unlock()

		if idx >= len(responses) {
			http.Error(w, "no more", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responses[idx])
	}))
	defer srv.Close()

	client := &GeminiClient{
		APIKey:       "test-key",
		Model:        "test-model",
		BaseURL:      srv.URL,
		ToolExecutor: &ToolExecutor{},
	}

	var tokens []string
	result, err := client.ChatStream(context.Background(), "be helpful", []Message{
		{Role: "user", Content: "show me main.go"},
	}, func(s string) {
		tokens = append(tokens, s)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Here is the file content." {
		t.Errorf("got result %q, want %q", result, "Here is the file content.")
	}

	// Thought text should appear in tokens but with the THOUGHT marker
	foundThought := false
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "\x00THOUGHT:") && strings.Contains(tok, "I need to check") {
			foundThought = true
		}
		// Raw thought text without marker should NOT appear
		if !strings.HasPrefix(tok, "\x00") && strings.Contains(tok, "I need to check") {
			t.Errorf("unmarked thought text in tokens: %q", tok)
		}
	}
	if !foundThought {
		t.Error("expected thought text with THOUGHT marker in tokens")
	}

	// Verify round 2 request echoes thought parts back
	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) < 2 {
		t.Fatalf("expected 2 requests, got %d", len(requestBodies))
	}
	req2 := requestBodies[1]

	// Must contain the thought text (echoed back for signature)
	if !strings.Contains(req2, "I need to check the files") {
		t.Errorf("round 2 request should echo thought text back, got: %s", req2)
	}
	// Must contain thought: true marker
	if !strings.Contains(req2, `"thought":true`) {
		t.Errorf("round 2 request should contain thought:true marker")
	}
	// Must contain functionResponse
	if !strings.Contains(req2, "functionResponse") {
		t.Errorf("round 2 request should contain functionResponse")
	}
}

func TestChatStream_EmptyPartsFiltered(t *testing.T) {
	// Simulate streaming chunks where some parts have no data
	// (the bug that caused "required oneof field 'data'" errors)
	round1 := sseEvent(
		ssePartSpec{}, // empty part — should be filtered
		ssePartSpec{
			FunctionCall: &geminiFunctionCall{
				Name: "list_files",
				Args: map[string]interface{}{"path": "."},
			},
		},
	)
	round2 := sseEvent(ssePartSpec{Text: "Done."})

	var mu sync.Mutex
	var requestBodies []string
	callIdx := 0
	responses := []string{round1, round2}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		mu.Lock()
		requestBodies = append(requestBodies, body)
		idx := callIdx
		callIdx++
		mu.Unlock()

		if idx >= len(responses) {
			http.Error(w, "no more", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responses[idx])
	}))
	defer srv.Close()

	client := &GeminiClient{
		APIKey:       "test-key",
		Model:        "test-model",
		BaseURL:      srv.URL,
		ToolExecutor: &ToolExecutor{},
	}

	result, err := client.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "list"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Done." {
		t.Errorf("got result %q, want %q", result, "Done.")
	}

	// The model turn in request 2 should NOT contain empty parts
	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) < 2 {
		t.Fatalf("expected 2 requests, got %d", len(requestBodies))
	}

	// Parse the second request to check parts
	var req2 geminiRequest
	if err := json.Unmarshal([]byte(requestBodies[1]), &req2); err != nil {
		t.Fatalf("failed to parse round 2 request: %v", err)
	}

	// Find the model turn
	for _, content := range req2.Contents {
		if content.Role == "model" {
			for i, part := range content.Parts {
				if part.Text == "" && part.FunctionCall == nil && part.FunctionResponse == nil {
					t.Errorf("model turn contains empty part at index %d: %+v", i, part)
				}
			}
		}
	}
}

func TestChatStream_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"bad request"}}`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, nil)

	_, err := client.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "hi"},
	}, nil)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should contain status code, got: %v", err)
	}
}

func TestChatStream_RetryOn429(t *testing.T) {
	var mu sync.Mutex
	attempts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()

		if n <= 1 {
			// First attempt: rate limited
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"rate limited","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`)
			return
		}
		// Second attempt: success
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "retried ok"}))
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, nil)

	result, err := client.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "hi"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "retried ok" {
		t.Errorf("got %q, want %q", result, "retried ok")
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestChatStream_ContextCancellation(t *testing.T) {
	// Server that blocks forever
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Block until request context is done
		<-r.Context().Done()
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := client.ChatStream(ctx, "", []Message{
		{Role: "user", Content: "hi"},
	}, nil)

	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestChatStream_SystemPrompt(t *testing.T) {
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

	client := newTestClient(srv.URL, nil)

	_, err := client.ChatStream(context.Background(), "You are a code reviewer", []Message{
		{Role: "user", Content: "review this"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !strings.Contains(requestBody, "systemInstruction") {
		t.Error("request should contain systemInstruction")
	}
	if !strings.Contains(requestBody, "You are a code reviewer") {
		t.Error("request should contain the system prompt text")
	}
}

func TestChatStream_NoSystemPrompt(t *testing.T) {
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

	client := newTestClient(srv.URL, nil)

	_, err := client.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if strings.Contains(requestBody, "systemInstruction") {
		t.Error("request should NOT contain systemInstruction when prompt is empty")
	}
}

// readBody reads and returns the request body as a string.
func readBody(r *http.Request) (string, error) {
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	return string(b), err
}

// ── Live API tests ─────────────────────────────────────────────────────
// These hit the real Gemini API. Skipped unless PRR_API_KEY is set.
// Run with: PRR_API_KEY=<key> go test ./internal/ai/ -run TestLive -v

func skipWithoutAPIKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("PRR_API_KEY")
	if key == "" {
		t.Skip("PRR_API_KEY not set, skipping live API test")
	}
	return key
}

func liveModel() string {
	if m := os.Getenv("PRR_MODEL"); m != "" {
		return m
	}
	return "gemini-2.5-flash"
}

func TestLive_SimpleChat(t *testing.T) {
	key := skipWithoutAPIKey(t)

	client := &GeminiClient{
		APIKey: key,
		Model:  liveModel(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var tokens []string
	result, err := client.ChatStream(ctx, "Reply in exactly one word.", []Message{
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

	// Use a real ToolExecutor — the tool call will fail (no git repo)
	// but the point is to verify the API accepts our request format
	// through the full tool call round-trip.
	client := &GeminiClient{
		APIKey:       key,
		Model:        liveModel(),
		ToolExecutor: &ToolExecutor{HeadRef: "HEAD"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var tokens []string
	result, err := client.ChatStream(ctx,
		"You have access to tools. Use the list_files tool to see what files exist, then describe what you found. Be brief.",
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

	// Check that tokens contain the tool call indicator
	joined := strings.Join(tokens, "")
	if !strings.Contains(joined, "list_files") {
		t.Logf("Warning: expected tool call indicator in tokens, got: %s", joined)
	}
}

func TestLive_OverviewReview(t *testing.T) {
	key := skipWithoutAPIKey(t)

	client := &GeminiClient{
		APIKey:       key,
		Model:        liveModel(),
		ToolExecutor: &ToolExecutor{HeadRef: "HEAD"},
	}

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

	systemPrompt := ReviewPRPrompt + "\n\n" + diffContext

	var tokens []string
	result, err := client.ChatStream(ctx, systemPrompt,
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

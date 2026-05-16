package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestOpenAIStreamChat_RequestTimeout_HungServer mirrors the Gemini
// timeout pin for OpenAI's parallel implementation: a hung server
// must surface as context.DeadlineExceeded after RequestTimeout
// rather than blocking indefinitely.
func TestOpenAIStreamChat_RequestTimeout_HungServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	provider := &OpenAIProvider{
		APIKey:         "test-key",
		Model:          "gpt-test",
		BaseURL:        srv.URL,
		RequestTimeout: 150 * time.Millisecond,
	}

	start := time.Now()
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("StreamChat returned non-deadline error: %v", err)
		}
		return
	}

	var gotErr error
	for event := range ch {
		if event.Type == EventError {
			gotErr = event.Err
		}
	}
	elapsed := time.Since(start)
	if gotErr == nil {
		t.Fatalf("expected EventError, channel closed cleanly after %v", elapsed)
	}
	if !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", gotErr)
	}
	if elapsed > 5*time.Second {
		t.Errorf("timeout fired too late: elapsed=%v (RequestTimeout=150ms)", elapsed)
	}
}

// TestOpenAIStreamChat_RequestTimeoutZero_NoWrapping pins the
// zero-disables contract for OpenAI (parallels the Gemini pin).
func TestOpenAIStreamChat_RequestTimeoutZero_NoWrapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Minimal valid stream — one chunk with a done marker.
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}` + "\n\n"))
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	provider := &OpenAIProvider{
		APIKey:  "test-key",
		Model:   "gpt-test",
		BaseURL: srv.URL,
		// RequestTimeout: 0 — no wrapping
	}

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Drain — we only care that no premature timeout fires.
	for range ch {
	}
}

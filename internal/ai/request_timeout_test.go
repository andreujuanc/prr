package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestChatRequest_RequestTimeoutOverridesProviderDefault pins that a
// per-call ChatRequest.RequestTimeout fires before the provider's
// default would. We set the provider default very generous (1h) and
// the request override very tight (150ms), then point at a hung
// server. The tight override must win.
func TestChatRequest_RequestTimeoutOverridesProviderDefault(t *testing.T) {
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

	provider := &GeminiProvider{
		APIKey:         "test-key",
		Model:          "test-model",
		BaseURL:        srv.URL,
		RequestTimeout: 1 * time.Hour, // generous default
	}

	start := time.Now()
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
		RequestTimeout: 150 * time.Millisecond, // per-call override
	})
	if err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("StreamChat returned non-deadline error: %v", err)
		}
		return
	}

	var gotErr error
	for ev := range ch {
		if ev.Type == EventError {
			gotErr = ev.Err
		}
	}
	elapsed := time.Since(start)

	if gotErr == nil {
		t.Fatalf("expected EventError, channel closed cleanly after %v", elapsed)
	}
	if !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", gotErr)
	}
	// The per-call deadline is 150ms; allow generous slack for CI.
	if elapsed > 3*time.Second {
		t.Errorf("override took %v — should have fired near 150ms", elapsed)
	}
}

// TestChatRequest_NoOverrideUsesProviderDefault guards the inverse:
// when ChatRequest.RequestTimeout is zero, the provider's
// RequestTimeout is what fires. We give the provider a tight 150ms
// budget, leave the request override zero, and assert deadline
// exceeded.
func TestChatRequest_NoOverrideUsesProviderDefault(t *testing.T) {
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

	provider := &GeminiProvider{
		APIKey:         "test-key",
		Model:          "test-model",
		BaseURL:        srv.URL,
		RequestTimeout: 150 * time.Millisecond,
	}

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
		// RequestTimeout: 0 — fall through to provider default
	})
	if err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("StreamChat returned non-deadline error: %v", err)
		}
		return
	}

	var gotErr error
	for ev := range ch {
		if ev.Type == EventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected EventError from provider-level deadline")
	}
	if !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", gotErr)
	}
}

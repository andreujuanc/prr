package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestStreamHeartbeat_FiresAfterSilence pins that the watchdog emits
// an EventHeartbeat when no chunk arrives within HeartbeatInterval.
// We stage a server that flushes a first chunk, pauses, then flushes
// a second chunk — the gap must produce a heartbeat in between.
func TestStreamHeartbeat_FiresAfterSilence(t *testing.T) {
	const interval = 200 * time.Millisecond
	const gap = 500 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "first"}))
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(gap)
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "second"}))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	provider := &GeminiProvider{
		APIKey:            "test-key",
		Model:             "test-model",
		BaseURL:           srv.URL,
		HeartbeatInterval: interval,
	}

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var sawHeartbeat bool
	var sawFirst, sawSecond bool
	var minSilence time.Duration
	for ev := range ch {
		switch ev.Type {
		case EventText:
			if ev.Text == "first" {
				sawFirst = true
			} else if ev.Text == "second" {
				sawSecond = true
			}
		case EventHeartbeat:
			sawHeartbeat = true
			if minSilence == 0 || ev.Silence < minSilence {
				minSilence = ev.Silence
			}
		}
	}

	if !sawFirst || !sawSecond {
		t.Errorf("text events missed: first=%v second=%v", sawFirst, sawSecond)
	}
	if !sawHeartbeat {
		t.Fatalf("expected EventHeartbeat across %v gap with %v interval", gap, interval)
	}
	if minSilence < interval {
		t.Errorf("heartbeat Silence = %v, expected >= %v", minSilence, interval)
	}
}

// TestStreamHeartbeat_DisabledWhenIntervalZero pins the opt-out: zero
// interval means no heartbeat, even across a long pause.
func TestStreamHeartbeat_DisabledWhenIntervalZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "first"}))
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "second"}))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	provider := &GeminiProvider{
		APIKey:  "test-key",
		Model:   "test-model",
		BaseURL: srv.URL,
		// HeartbeatInterval: 0 — disabled
	}

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	for ev := range ch {
		if ev.Type == EventHeartbeat {
			t.Fatalf("unexpected EventHeartbeat with interval=0: silence=%v", ev.Silence)
		}
	}
}

package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

// TestStreamHeartbeat_SilenceCapFiresCancel pins that the silence cap
// invokes the cancel callback when no tick lands within maxSilence,
// and reports silenceCapFired() = true. Heartbeat emission is
// disabled (interval = 0) to isolate the cap behavior. We wait on a
// channel that the cancel callback closes rather than time.Sleep, so
// the test does not race the watchdog goroutine on heavily loaded CI.
func TestStreamHeartbeat_SilenceCapFiresCancel(t *testing.T) {
	ch := make(chan ChatEvent, 16)
	var cancelCount int32
	cancelled := make(chan struct{})
	cancel := func() {
		if atomic.AddInt32(&cancelCount, 1) == 1 {
			close(cancelled)
		}
	}

	hb := newStreamHeartbeat(ch, 0, 100*time.Millisecond, cancel)
	defer hb.stop()

	select {
	case <-cancelled:
		// silence cap fired
	case <-time.After(2 * time.Second):
		t.Fatal("silence cap did not fire within 2s (cap=100ms)")
	}

	if got := atomic.LoadInt32(&cancelCount); got != 1 {
		t.Fatalf("cancel call count = %d, want 1", got)
	}
	if !hb.silenceCapFired() {
		t.Fatal("silenceCapFired() = false, want true")
	}
}

// TestStreamHeartbeat_SilenceCapNotFiredOnTicks pins that ticks reset
// the silence clock, so regular ticks prevent the cap from firing.
// Margin is generous (1s cap, 100ms sleep, ~10× headroom) so an 80ms
// sleep stretching to ~200ms on loaded CI does not produce a false
// positive — the cap fires only if a single sleep exceeds 1s, which
// is extreme jitter.
func TestStreamHeartbeat_SilenceCapNotFiredOnTicks(t *testing.T) {
	ch := make(chan ChatEvent, 16)
	var cancelCount int32
	cancel := func() {
		atomic.AddInt32(&cancelCount, 1)
	}

	hb := newStreamHeartbeat(ch, 0, 1*time.Second, cancel)
	stop := time.Now().Add(800 * time.Millisecond)
	for time.Now().Before(stop) {
		hb.tick()
		time.Sleep(100 * time.Millisecond)
	}
	hb.stop()

	if got := atomic.LoadInt32(&cancelCount); got != 0 {
		t.Fatalf("cancel call count = %d, want 0 (regular ticks should prevent silence cap)", got)
	}
	if hb.silenceCapFired() {
		t.Fatal("silenceCapFired() = true with regular ticks; want false")
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

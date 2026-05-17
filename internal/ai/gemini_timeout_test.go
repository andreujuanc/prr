package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// TestGeminiStreamChat_RequestTimeout_HungServer pins the load-bearing
// contract: a server that accepts the connection then never sends a
// byte must surface as context.DeadlineExceeded after RequestTimeout,
// not hang indefinitely. This is the exact failure mode that
// motivated the per-call timeout (silent Gemini calls hung forever
// because no token activity ever reset the watchdog).
func TestGeminiStreamChat_RequestTimeout_HungServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hang until the request context completes OR a generous safety
		// timer fires, so srv.Close() can't deadlock if r.Context() is
		// slow to propagate the client-side cancellation.
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

	start := time.Now()
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		// Some Go versions surface the timeout on the initial Do() call
		// if the server hangs before flushing headers; either path is OK.
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("StreamChat returned non-deadline error: %v", err)
		}
		return
	}

	// Drain the channel — the stream goroutine must terminate with
	// an EventError carrying context.DeadlineExceeded.
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

// TestGeminiStreamChat_RequestTimeout_FastResponseSucceeds pins the
// other side: when the server responds within the timeout, the call
// succeeds normally. Guards against false positives where the timeout
// fires on legit-but-slow responses.
func TestGeminiStreamChat_RequestTimeout_FastResponseSucceeds(t *testing.T) {
	resp := sseEvent(ssePartSpec{Text: "ok"})
	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := &GeminiProvider{
		APIKey:         "test-key",
		Model:          "test-model",
		BaseURL:        srv.URL,
		RequestTimeout: 5 * time.Second, // generous; server responds instantly
	}

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text, _, err := collectText(ch)
	if err != nil {
		t.Fatalf("collectText error: %v", err)
	}
	if text != "ok" {
		t.Errorf("got text %q, want %q", text, "ok")
	}
}

// TestGeminiStreamChat_RequestTimeout_ParentCancelWins pins that user
// cancellation (parent ctx) takes precedence and is NOT mislabelled as
// a deadline-exceeded error — important so retry logic doesn't retry
// user-initiated cancels.
func TestGeminiStreamChat_RequestTimeout_ParentCancelWins(t *testing.T) {
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
		RequestTimeout: 1 * time.Hour, // much longer than the parent cancel
	}

	parent, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay so the request is in-flight.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	ch, err := provider.StreamChat(parent, ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
		return
	}

	var gotErr error
	for event := range ch {
		if event.Type == EventError {
			gotErr = event.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected EventError, channel closed cleanly")
	}
	// Must be Canceled, not DeadlineExceeded — the parent cancel fired
	// long before the per-call deadline.
	if !errors.Is(gotErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", gotErr)
	}
	if errors.Is(gotErr, context.DeadlineExceeded) {
		t.Fatalf("parent cancel mislabeled as deadline: %v", gotErr)
	}
}

// TestGeminiStreamChat_RequestTimeoutZero_NoWrapping pins that the
// zero value disables the per-call timeout entirely — tests
// constructing GeminiProvider directly (without the factory) must not
// pay a 15min wall-clock cost on failure. The probe: ctx is wrapped
// only when RequestTimeout > 0, so a request with no RequestTimeout
// and an explicit parent deadline reports that exact deadline.
func TestGeminiStreamChat_RequestTimeoutZero_NoWrapping(t *testing.T) {
	var seenDeadlineSec int64 = -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("x-server-timeout"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				atomic.StoreInt64(&seenDeadlineSec, n)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sseEvent(ssePartSpec{Text: "ok"})))
	}))
	defer srv.Close()

	provider := &GeminiProvider{
		APIKey:  "test-key",
		Model:   "test-model",
		BaseURL: srv.URL,
		// RequestTimeout: 0 — no wrapping
	}

	// Parent ctx with a 9s deadline. With wrapping off, the deadline
	// the server sees in x-server-timeout should match the parent.
	parent, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()

	ch, err := provider.StreamChat(parent, ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for range ch {
	} // drain

	seen := atomic.LoadInt64(&seenDeadlineSec)
	if seen < 1 || seen > 10 {
		t.Errorf("expected x-server-timeout in [1,10]s, got %ds (parent deadline was 9s)", seen)
	}
}

// TestGeminiStreamChat_ServerTimeoutHeader pins the contract that the
// per-call deadline is forwarded to the backend via x-server-timeout
// in seconds. The header lets Gemini shed load gracefully if we give
// up on a request that's still queued server-side.
func TestGeminiStreamChat_ServerTimeoutHeader(t *testing.T) {
	var sawHeader atomic.Bool
	var seenSec int64 = -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("x-server-timeout"); v != "" {
			sawHeader.Store(true)
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				atomic.StoreInt64(&seenSec, n)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sseEvent(ssePartSpec{Text: "ok"})))
	}))
	defer srv.Close()

	provider := &GeminiProvider{
		APIKey:         "test-key",
		Model:          "test-model",
		BaseURL:        srv.URL,
		RequestTimeout: 30 * time.Second,
	}

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

	if !sawHeader.Load() {
		t.Fatal("server did not see x-server-timeout header")
	}
	seen := atomic.LoadInt64(&seenSec)
	// 30s deadline minus a tiny amount of in-flight; allow [25, 31].
	if seen < 25 || seen > 31 {
		t.Errorf("x-server-timeout = %ds, want [25, 31]", seen)
	}
}

// TestGeminiStreamChat_MaxStreamSilence_FiresMidStream pins the
// silence cap: a server that sends headers + one SSE chunk and then
// stalls must surface as a canceled stream within ~maxSilence,
// without waiting for RequestTimeout. This is the load-bearing
// safety net for the mid-stream hang investigated 2026-05-16.
func TestGeminiStreamChat_MaxStreamSilence_FiresMidStream(t *testing.T) {
	const silence = 250 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "first"}))
		if flusher != nil {
			flusher.Flush()
		}
		// Stall mid-stream — simulates Gemini going silent after
		// headers + first chunk. Bail out when r.Context() ends so
		// srv.Close() doesn't deadlock.
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()

	provider := &GeminiProvider{
		APIKey:           "test-key",
		Model:            "test-model",
		BaseURL:          srv.URL,
		RequestTimeout:   10 * time.Second, // generous — silence cap should fire first
		MaxStreamSilence: silence,
	}

	start := time.Now()
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var gotErr error
	for ev := range ch {
		if ev.Type == EventError {
			gotErr = ev.Err
		}
	}
	elapsed := time.Since(start)

	if gotErr == nil {
		t.Fatalf("expected EventError after silence cap, channel closed cleanly after %v", elapsed)
	}
	if !errors.Is(gotErr, context.Canceled) {
		t.Fatalf("expected context.Canceled from silence cap, got: %v", gotErr)
	}
	// Should fire within a small multiple of the silence threshold,
	// well below RequestTimeout. Cap at 5× silence to absorb scheduling
	// jitter while still proving the cap fired and not the deadline.
	if elapsed > 5*silence {
		t.Errorf("silence cap fired too late: elapsed=%v (silence=%v)", elapsed, silence)
	}
}

// TestGeminiStreamChat_MaxStreamSilence_DisabledByDefault pins the
// opt-out: zero MaxStreamSilence means no cancellation regardless of
// how long the stream stalls (other timers still apply).
func TestGeminiStreamChat_MaxStreamSilence_DisabledByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "first"}))
		if flusher != nil {
			flusher.Flush()
		}
		// Pause briefly then send done — should succeed end-to-end.
		time.Sleep(150 * time.Millisecond)
		fmt.Fprint(w, sseEvent(ssePartSpec{Text: "second"}))
	}))
	defer srv.Close()

	provider := &GeminiProvider{
		APIKey:         "test-key",
		Model:          "test-model",
		BaseURL:        srv.URL,
		RequestTimeout: 5 * time.Second,
		// MaxStreamSilence: 0 — disabled
	}

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, _, err := collectText(ch)
	if err != nil {
		t.Fatalf("unexpected stream error with silence cap disabled: %v", err)
	}
	if text != "firstsecond" {
		t.Errorf("got text %q, want %q", text, "firstsecond")
	}
}

// TestGeminiStreamChat_ServerTimeoutHeader_OmittedWhenNoDeadline pins
// that when ctx has no deadline AND RequestTimeout is zero, the
// header is omitted entirely (rather than sending a bogus value).
func TestGeminiStreamChat_ServerTimeoutHeader_OmittedWhenNoDeadline(t *testing.T) {
	var sawHeader atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-server-timeout") != "" {
			sawHeader.Store(true)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sseEvent(ssePartSpec{Text: "ok"})))
	}))
	defer srv.Close()

	provider := &GeminiProvider{
		APIKey:  "test-key",
		Model:   "test-model",
		BaseURL: srv.URL,
		// RequestTimeout: 0, parent ctx has no deadline → no header
	}

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for range ch {
	}

	if sawHeader.Load() {
		t.Fatal("x-server-timeout header should be omitted when ctx has no deadline")
	}
}

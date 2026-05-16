package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestIsTransientError_PerCallDeadlineWithLiveParent pins the load-
// bearing contract: when a provider's per-call timeout fires
// (DeadlineExceeded) but the parent context is still alive, retry
// is the right call. Without this, a single hung HTTP request kills
// the whole pipeline.
func TestIsTransientError_PerCallDeadlineWithLiveParent(t *testing.T) {
	parent := context.Background()
	if !IsTransientError(context.DeadlineExceeded, parent) {
		t.Error("DeadlineExceeded with live parent should be transient")
	}
	if !IsTransientError(fmt.Errorf("wrapped: %w", context.DeadlineExceeded), parent) {
		t.Error("wrapped DeadlineExceeded with live parent should be transient")
	}
}

// TestIsTransientError_DeadlineWithDeadParent pins the other side:
// when the parent context itself has expired, no amount of retry
// will help — the caller has run out of time. Treat as terminal.
func TestIsTransientError_DeadlineWithDeadParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel() // parent is now done

	if IsTransientError(context.DeadlineExceeded, parent) {
		t.Error("DeadlineExceeded with dead parent should be terminal")
	}
}

// TestIsTransientError_UserCancelIsTerminal pins that explicit
// context.Canceled is terminal even when the immediate parent looks
// alive. Canceled propagates, so if any ctx in the chain was
// cancelled the parent reflects it; checking errors.Is covers both
// direct Canceled and propagation.
func TestIsTransientError_UserCancelIsTerminal(t *testing.T) {
	parent := context.Background()
	if IsTransientError(context.Canceled, parent) {
		t.Error("Canceled should be terminal regardless of parent state")
	}
	if IsTransientError(fmt.Errorf("user cancel: %w", context.Canceled), parent) {
		t.Error("wrapped Canceled should be terminal")
	}
}

// TestIsTransientError_TransientHTTP pins the existing classifier
// behavior that was inherited from review/batch.go's
// isTransientClientError.
func TestIsTransientError_TransientHTTP(t *testing.T) {
	parent := context.Background()
	transient := []error{
		errors.New("provider returned 429 Too Many Requests"),
		errors.New("503 Service Unavailable"),
		errors.New("net/http: request canceled (Client.Timeout exceeded)"),
		errors.New("read tcp: connection reset by peer"),
		errors.New("rate limit exceeded"),
		fmt.Errorf("upstream %w", errors.New("EOF")),
		fmt.Errorf("stream closed: %w", io.EOF),
	}
	for _, err := range transient {
		if !IsTransientError(err, parent) {
			t.Errorf("IsTransientError(%q) = false, want true", err)
		}
	}
}

// TestIsTransientError_NotTransient pins terminal errors that must
// NOT be retried — bad credentials, malformed requests, etc.
func TestIsTransientError_NotTransient(t *testing.T) {
	parent := context.Background()
	terminal := []error{
		nil,
		errors.New("invalid api key"),
		errors.New("400 bad request: malformed json"),
		errors.New("404 not found"),
		errors.New("malformed: chunk #5034 missing tail"), // 503 substring inside 5034
		errors.New("endpointoflist not configured"),       // "eof" inside word
	}
	for _, err := range terminal {
		if IsTransientError(err, parent) {
			t.Errorf("IsTransientError(%v) = true (false positive), want false", err)
		}
	}
}

// TestIsTransientError_NilParentSafe pins nil-parent safety —
// callers shouldn't need a nil-check before invoking the classifier.
func TestIsTransientError_NilParentSafe(t *testing.T) {
	if IsTransientError(io.EOF, nil) != true {
		t.Error("nil parent should be treated as alive; EOF is transient")
	}
	if IsTransientError(context.Canceled, nil) != false {
		t.Error("Canceled with nil parent should still be terminal")
	}
}

// TestRetryTransient_RetriesUntilSuccess pins the success path: two
// transient errors then a clean call returns the clean result.
func TestRetryTransient_RetriesUntilSuccess(t *testing.T) {
	var calls int32
	result, err := RetryTransient(context.Background(), 5, "test", func(ctx context.Context) (string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return "", fmt.Errorf("transient %d: %w", n, io.EOF)
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

// TestRetryTransient_ExhaustsAttempts pins the failure path: when
// every attempt is transient, exhaust maxAttempts and return the
// wrapped last error.
func TestRetryTransient_ExhaustsAttempts(t *testing.T) {
	var calls int32
	_, err := RetryTransient(context.Background(), 3, "test", func(ctx context.Context) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", io.EOF
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected wrapped io.EOF, got: %v", err)
	}
}

// TestRetryTransient_NoRetryOnTerminal pins that terminal errors
// short-circuit immediately — no backoff, no extra calls.
func TestRetryTransient_NoRetryOnTerminal(t *testing.T) {
	var calls int32
	terminal := errors.New("400 bad request")
	_, err := RetryTransient(context.Background(), 5, "test", func(ctx context.Context) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", terminal
	})
	if !errors.Is(err, terminal) {
		t.Errorf("expected terminal error, got: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (terminal should not retry)", got)
	}
}

// TestRetryTransient_ParentCancelStopsRetries pins that parent
// cancellation mid-loop terminates retries promptly — a hot retry
// loop must not ignore the caller's cancel signal.
func TestRetryTransient_ParentCancelStopsRetries(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())

	var calls int32
	go func() {
		// Cancel after first attempt — second attempt's backoff
		// wait should return promptly via parent.Done.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := RetryTransient(parent, 10, "test", func(ctx context.Context) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", io.EOF
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	// Should be a small number — 1 or 2 attempts before cancel.
	if got := atomic.LoadInt32(&calls); got > 2 {
		t.Errorf("calls = %d, want ≤ 2 (cancel should short-circuit)", got)
	}
}

// TestTransientBackoff_QuadraticGrowth pins the backoff curve:
// 1s, 4s, 9s, 16s … Quadratic so the first retry is fast (most
// transient blips clear in < 2s) but later retries respect
// rate-limit windows (30-60s) instead of hammering the upstream.
func TestTransientBackoff_QuadraticGrowth(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 4 * time.Second},
		{3, 9 * time.Second},
		{4, 16 * time.Second},
	}
	for _, c := range cases {
		if got := TransientBackoff(c.attempt); got != c.want {
			t.Errorf("TransientBackoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

// TestRetryTransient_PerCallDeadlineRetries integrates the per-call
// timeout flow: fn returns DeadlineExceeded but parent is alive →
// retry → success. Mirrors the production path where a provider's
// RequestTimeout fires once but the second attempt finds the server
// responsive.
func TestRetryTransient_PerCallDeadlineRetries(t *testing.T) {
	var calls int32
	result, err := RetryTransient(context.Background(), 3, "test", func(ctx context.Context) (int, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return 0, context.DeadlineExceeded
		}
		return 42, nil
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if result != 42 {
		t.Errorf("result = %d, want 42", result)
	}
}

// TestTransientError_IsTransient pins that *TransientError is always
// classified as retryable, even when its message would not match the
// regex (e.g. a server-side 401 wrapped accidentally).
func TestTransientError_IsTransient(t *testing.T) {
	te := &TransientError{Err: errors.New("something opaque")}
	if !IsTransientError(te, context.Background()) {
		t.Error("*TransientError must always be classified transient")
	}
	wrapped := fmt.Errorf("outer: %w", te)
	if !IsTransientError(wrapped, context.Background()) {
		t.Error("wrapped *TransientError must still be classified transient")
	}
}

// TestTransientSleep_HonorsRetryAfter pins that a server-supplied
// RetryAfter overrides the quadratic curve.
func TestTransientSleep_HonorsRetryAfter(t *testing.T) {
	te := &TransientError{Err: errors.New("429"), RetryAfter: 3 * time.Second}
	got := transientSleep(te, 1)
	if got != 3*time.Second {
		t.Errorf("transientSleep with RetryAfter=3s = %v, want 3s (not the quadratic 1s)", got)
	}
}

// TestTransientSleep_NoHintFallsBackToQuadratic pins that a
// TransientError without a RetryAfter uses the existing backoff.
func TestTransientSleep_NoHintFallsBackToQuadratic(t *testing.T) {
	te := &TransientError{Err: errors.New("500")}
	got := transientSleep(te, 2)
	if got != 4*time.Second {
		t.Errorf("transientSleep without hint at attempt 2 = %v, want 4s (quadratic)", got)
	}
}

// TestTransientSleep_CapsAt60s pins that a misconfigured server
// claiming a 1h retry-after is bounded to keep the pipeline moving.
func TestTransientSleep_CapsAt60s(t *testing.T) {
	te := &TransientError{Err: errors.New("429"), RetryAfter: 1 * time.Hour}
	got := transientSleep(te, 1)
	if got != 60*time.Second {
		t.Errorf("transientSleep with huge RetryAfter = %v, want 60s cap", got)
	}
}

// TestParseGeminiRetryDelay covers Gemini's body-embedded retryDelay
// shape and the absent case (returns 0).
func TestParseGeminiRetryDelay(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration
	}{
		{"integer seconds", `{"error":{"details":[{"@type":"x","retryDelay":"41s"}]}}`, 41 * time.Second},
		{"fractional seconds", `{"error":{"details":[{"@type":"x","retryDelay":"0.5s"}]}}`, 500 * time.Millisecond},
		{"zero floor", `{"error":{"details":[{"@type":"x","retryDelay":"0s"}]}}`, 100 * time.Millisecond},
		{"absent", `{"error":{"details":[]}}`, 0},
		{"malformed", `not json`, 0},
	}
	for _, c := range cases {
		got := parseGeminiRetryDelay([]byte(c.body))
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestParseHTTPRetryAfter covers OpenAI / generic Retry-After parsing:
// plain seconds, HTTP-date, and absent.
func TestParseHTTPRetryAfter(t *testing.T) {
	t.Run("seconds", func(t *testing.T) {
		h := http.Header{"Retry-After": []string{"12"}}
		if got := parseHTTPRetryAfter(h); got != 12*time.Second {
			t.Errorf("got %v, want 12s", got)
		}
	})
	t.Run("absent", func(t *testing.T) {
		if got := parseHTTPRetryAfter(http.Header{}); got != 0 {
			t.Errorf("got %v, want 0", got)
		}
	})
	t.Run("http date in future", func(t *testing.T) {
		future := time.Now().Add(15 * time.Second).UTC().Format(http.TimeFormat)
		h := http.Header{"Retry-After": []string{future}}
		got := parseHTTPRetryAfter(h)
		// Allow ±2s slack for scheduling jitter.
		if got < 12*time.Second || got > 17*time.Second {
			t.Errorf("got %v, want ~15s", got)
		}
	})
	t.Run("http date in past floors to 100ms", func(t *testing.T) {
		past := time.Now().Add(-1 * time.Hour).UTC().Format(http.TimeFormat)
		h := http.Header{"Retry-After": []string{past}}
		if got := parseHTTPRetryAfter(h); got != 100*time.Millisecond {
			t.Errorf("got %v, want 100ms", got)
		}
	})
}

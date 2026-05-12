package review

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
)

// fakeSynthesisClient lets tests stage a sequence of responses (each
// either a return string or an error to surface). Calls beyond the
// staged slice loop back to the last entry.
type fakeSynthesisClient struct {
	results []synthesisResult
	calls   int64
}

type synthesisResult struct {
	out string
	err error
}

func (f *fakeSynthesisClient) ChatStream(_ context.Context, _ string, _ []ai.Message, onToken func(string)) (string, error) {
	idx := int(atomic.AddInt64(&f.calls, 1)) - 1
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	r := f.results[idx]
	if r.err != nil {
		return "", r.err
	}
	if onToken != nil {
		onToken(r.out)
	}
	return r.out, nil
}

// TestSynthesisWithRetry_RetriesOnTransient429 pins the recovery
// behavior: a 429-flavored error on attempt 1 must be retried, and a
// success on attempt 2 must be returned. Without this, every transient
// rate-limit kills the whole synthesis phase.
func TestSynthesisWithRetry_RetriesOnTransient429(t *testing.T) {
	client := &fakeSynthesisClient{
		results: []synthesisResult{
			{err: errors.New("provider returned 429 Too Many Requests")},
			{out: "synthesized"},
		},
	}
	// Run with a short fake backoff via temporary patching is overkill;
	// the default 1s first-attempt backoff is acceptable for test time.
	result, err := SynthesisWithRetry(context.Background(), client, "sys", nil, nil)
	if err != nil {
		t.Fatalf("SynthesisWithRetry: %v", err)
	}
	if result != "synthesized" {
		t.Errorf("result = %q, want synthesized", result)
	}
	if got := atomic.LoadInt64(&client.calls); got != 2 {
		t.Errorf("client calls = %d, want 2 (one failure + one success)", got)
	}
}

// TestSynthesisWithRetry_DoesNotRetryOnCtxCancel ensures user-initiated
// cancellation propagates immediately — without retries chewing through
// backoff time before honoring the user's stop.
func TestSynthesisWithRetry_DoesNotRetryOnCtxCancel(t *testing.T) {
	client := &fakeSynthesisClient{
		results: []synthesisResult{
			{err: context.Canceled},
			{out: "should not be reached"},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := SynthesisWithRetry(ctx, client, "sys", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := atomic.LoadInt64(&client.calls); got > 1 {
		t.Errorf("client called %d times after ctx cancel; should be at most 1", got)
	}
}

// TestSynthesisWithRetry_GivesUpAfterMaxRetries pins the bounded retry
// behavior — persistent transient failures should surface, not loop
// forever.
func TestSynthesisWithRetry_GivesUpAfterMaxRetries(t *testing.T) {
	client := &fakeSynthesisClient{
		results: []synthesisResult{
			{err: errors.New("503 service unavailable")},
		},
	}
	_, err := SynthesisWithRetry(context.Background(), client, "sys", nil, nil)
	if err == nil {
		t.Fatal("expected error after persistent transient failures")
	}
	if got := atomic.LoadInt64(&client.calls); got != int64(MaxRetries) {
		t.Errorf("client called %d times, want MaxRetries=%d", got, MaxRetries)
	}
}

// TestSynthesisWithRetry_NonTransientErrorIsReturnedImmediately:
// "invalid api key" or malformed-request errors shouldn't waste backoff
// time on retries that will never succeed.
func TestSynthesisWithRetry_NonTransientErrorIsReturnedImmediately(t *testing.T) {
	client := &fakeSynthesisClient{
		results: []synthesisResult{
			{err: errors.New("invalid api key")},
		},
	}
	_, err := SynthesisWithRetry(context.Background(), client, "sys", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("err = %v, want error containing 'invalid api key'", err)
	}
	if got := atomic.LoadInt64(&client.calls); got != 1 {
		t.Errorf("client called %d times for non-transient error, want 1", got)
	}
}

func TestIsTransientClientError_ClassifiesCorrectly(t *testing.T) {
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
		if !isTransientClientError(err) {
			t.Errorf("isTransientClientError(%q) = false, want true", err)
		}
	}

	terminal := []error{
		nil,
		errors.New("invalid api key"),
		errors.New("400 bad request: malformed json"),
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("user-cancelled: %w", context.Canceled),
	}
	for _, err := range terminal {
		if isTransientClientError(err) {
			t.Errorf("isTransientClientError(%v) = true, want false", err)
		}
	}
}

// TestIsTransientClientError_NoFalsePositives pins the boundary cases
// that motivated tightening the matcher from substring to word-bounded
// regex. A naive substring match for "500" / "eof" would have wrongly
// flagged each of these as retryable; the regex with word boundaries
// must correctly leave them as terminal.
func TestIsTransientClientError_NoFalsePositives(t *testing.T) {
	falsePositiveTraps := []error{
		errors.New("input exceeded 5000 token limit"),       // "500" inside "5000"
		errors.New("rejected: prompt over 50000 chars"),     // 50000 contains 5000 contains 500
		errors.New("request took 502ms before failing"),     // 502 inside larger number? no, 502 alone — but follows context that's NOT a status code
		errors.New("endpointoflist not configured"),         // "eof" inside word
		errors.New("invalid useofdeprecated_field setting"), // "eof" inside word
		errors.New("error: model claude-haiku-4-5 unknown"), // contains "504" - wait, does it? no. ok.
		errors.New("malformed: chunk #5034 missing tail"),   // "503" inside 5034
	}
	// Build a sanity list — we want each NOT to be classified as transient.
	for _, err := range falsePositiveTraps {
		if isTransientClientError(err) {
			t.Errorf("isTransientClientError(%q) = true (false positive), want false", err)
		}
	}
}

// countingReporter is the inner Reporter for WatchdogReporter stress
// tests — counts every method call so we can assert all of them
// arrived under concurrent access. Atomic counters avoid needing its
// own lock; methods may be invoked from many goroutines.
type countingReporter struct {
	aoi       int64
	init      int64
	batch     int64
	synthesis int64
	tokens    int64
}

func (c *countingReporter) AOIProgress(string, bool, int)  { atomic.AddInt64(&c.aoi, 1) }
func (c *countingReporter) InitBatches([]BatchInfo)        { atomic.AddInt64(&c.init, 1) }
func (c *countingReporter) BatchProgress(int, BatchStatus) { atomic.AddInt64(&c.batch, 1) }
func (c *countingReporter) SynthesisStarted()              { atomic.AddInt64(&c.synthesis, 1) }
func (c *countingReporter) Token(string)                   { atomic.AddInt64(&c.tokens, 1) }

// TestWatchdogReporter_ConcurrentCalls pins the race-free guarantee
// that matters most in production: when parallel batch goroutines fire
// reporter events at the same time (every batch's progress, every
// streamed token), neither the wrapper nor the tap leaks state across
// goroutines. The -race detector is the authority here.
func TestWatchdogReporter_ConcurrentCalls(t *testing.T) {
	inner := &countingReporter{}
	var tapCalls int64
	rr := &WatchdogReporter{
		Inner: inner,
		Tap:   func(string) { atomic.AddInt64(&tapCalls, 1) },
	}

	const goroutines = 32
	const callsPerGoroutine = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < callsPerGoroutine; j++ {
				// Mix of methods, like a real parallel review:
				// token streams + batch updates + AOI ticks.
				rr.Token("tok")
				rr.BatchProgress(id, StatusActive)
				if j%10 == 0 {
					rr.AOIProgress("progress", false, 0)
				}
			}
		}(i)
	}
	wg.Wait()

	wantPerKind := int64(goroutines * callsPerGoroutine)
	if got := atomic.LoadInt64(&inner.tokens); got != wantPerKind {
		t.Errorf("inner.tokens = %d, want %d", got, wantPerKind)
	}
	if got := atomic.LoadInt64(&inner.batch); got != wantPerKind {
		t.Errorf("inner.batch = %d, want %d", got, wantPerKind)
	}
	wantAOI := int64(goroutines * (callsPerGoroutine / 10))
	if got := atomic.LoadInt64(&inner.aoi); got != wantAOI {
		t.Errorf("inner.aoi = %d, want %d", got, wantAOI)
	}
	// tap fires on every method, so total = tokens + batch + aoi.
	wantTap := wantPerKind*2 + wantAOI
	if got := atomic.LoadInt64(&tapCalls); got != wantTap {
		t.Errorf("tap calls = %d, want %d", got, wantTap)
	}
}

// TestWatchdogReporter_NilTapIsSafe — concurrent callers must not race
// even when no tap is wired (legitimate config when only inner-side
// counting matters).
func TestWatchdogReporter_NilTapIsSafe(t *testing.T) {
	inner := &countingReporter{}
	rr := &WatchdogReporter{Inner: inner, Tap: nil}

	var wg sync.WaitGroup
	wg.Add(8)
	for i := 0; i < 8; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rr.Token("x")
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&inner.tokens); got != 800 {
		t.Errorf("inner.tokens = %d, want 800", got)
	}
}

func TestTransientBackoff_QuadraticGrowth(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 4 * time.Second},
		{3, 9 * time.Second},
	}
	for _, c := range cases {
		if got := transientBackoff(c.attempt); got != c.want {
			t.Errorf("transientBackoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestParseBatchResult(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantNil bool
		wantLen int
	}{
		{
			name:    "valid JSON array",
			input:   `[{"file":"main.go","purpose":"entrypoint","findings":"none"}]`,
			wantLen: 1,
		},
		{
			name:    "wrapped in json fence",
			input:   "```json\n[{\"file\":\"main.go\",\"purpose\":\"entrypoint\",\"findings\":\"none\"}]\n```",
			wantLen: 1,
		},
		{
			name:    "wrapped in plain fence",
			input:   "```\n[{\"file\":\"a.go\",\"purpose\":\"p\",\"findings\":\"f\"},{\"file\":\"b.go\",\"purpose\":\"p2\",\"findings\":\"\"}]\n```",
			wantLen: 2,
		},
		{
			name:    "empty array",
			input:   "[]",
			wantLen: 0,
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantNil: true,
		},
		{
			name:    "whitespace around fences",
			input:   "  ```json\n[{\"file\":\"x.go\",\"purpose\":\"y\",\"findings\":\"z\"}]\n```  ",
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseBatchResult(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if len(got) != tt.wantLen {
				t.Errorf("expected %d results, got %d", tt.wantLen, len(got))
			}
		})
	}
}

func TestParseBatchResult_Fields(t *testing.T) {
	input := `[{"file":"src/main.go","purpose":"application entry point","findings":"Missing error handling on line 42"}]`
	results := ParseBatchResult(input)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.File != "src/main.go" {
		t.Errorf("File = %q, want %q", r.File, "src/main.go")
	}
	if r.Purpose != "application entry point" {
		t.Errorf("Purpose = %q, want %q", r.Purpose, "application entry point")
	}
	if got := r.Findings.Text(); got != "Missing error handling on line 42" {
		t.Errorf("Findings.Text() = %q, want %q", got, "Missing error handling on line 42")
	}
}

func TestParseBatchResult_StructuredFindings(t *testing.T) {
	input := `[{
		"file": "src/main.go",
		"purpose": "entry point",
		"findings": [
			{
				"severity": "high",
				"confidence": "high",
				"dimension": "correctness",
				"title": "Off-by-one in expiry",
				"line": 87,
				"detail": "exp <= now accepts an expired token.",
				"suggestion": "Use exp < now."
			}
		]
	}]`
	results := ParseBatchResult(input)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if len(r.Findings.Items) != 1 {
		t.Fatalf("expected 1 finding item, got %d", len(r.Findings.Items))
	}
	got := r.Findings.Items[0]
	if got.Severity != "high" || got.Line != 87 || got.Title != "Off-by-one in expiry" {
		t.Errorf("unexpected finding: %+v", got)
	}
	if r.Findings.IsEmpty() {
		t.Error("expected non-empty findings")
	}
	if !strings.Contains(r.Findings.Text(), "Off-by-one in expiry") {
		t.Errorf("Text() should include the title; got %q", r.Findings.Text())
	}
}

func TestBatchFindings_EmptyArray(t *testing.T) {
	input := `[{"file":"a.go","purpose":"p","findings":[]}]`
	results := ParseBatchResult(input)
	if len(results) != 1 {
		t.Fatalf("expected 1 result")
	}
	if !results[0].Findings.IsEmpty() {
		t.Error("expected empty findings")
	}
	if results[0].Findings.Text() != "" {
		t.Errorf("Text() should be empty; got %q", results[0].Findings.Text())
	}
}

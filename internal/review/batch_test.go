package review

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
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

// Transient-error classification + backoff tests live in
// internal/ai/retry_test.go alongside the implementation. The old
// duplicate tests here were removed when isTransientClientError /
// transientBackoff moved into the ai package as IsTransientError /
// TransientBackoff.

// WatchdogReporter and its concurrency tests were removed when the
// watchdog ceremony was retired in favor of HTTP-layer per-call
// timeouts (provider RequestTimeout) + RetryTransient. Stall
// detection now lives at the HTTP layer where it can actually
// observe the network, not at the reporter layer where it could
// only see progress events.

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
				"category": "correctness",
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

// TestAppendDeepFindings_EmitsFindingIDs pins the contract that
// AppendDeepFindings exposes each finding's ID in the prompt text fed
// to synthesis. Without this, the synthesis model can't fill the
// `source_ids` field on its output findings.
func TestAppendDeepFindings_EmitsFindingIDs(t *testing.T) {
	var b strings.Builder
	ff := make(map[string]string)
	findings := []state.DeepFinding{
		{FindingID: "F-001", Severity: "high", Title: "Auth bypass", File: "auth.go", Lines: "42-50",
			Category: "cryptography", Subcategory: "auth", Description: "Missing check"},
		{FindingID: "F-002", Severity: "medium", Title: "N+1 query", File: "repo.go", Lines: "88",
			Category: "performance", Subcategory: "io", Description: "Queries inside loop"},
	}
	AppendDeepFindings(&b, ff, findings)
	out := b.String()
	for _, id := range []string{"F-001", "F-002"} {
		if !strings.Contains(out, id) {
			t.Errorf("synthesis input missing %s; synthesis can't echo source_ids without it.\nGot:\n%s", id, out)
		}
	}
	// Headers should also be IDable so the model can spot per-finding boundaries.
	if !strings.Contains(out, "**ID:** F-001") {
		t.Errorf("expected explicit **ID:** F-001 marker in synthesis input; got:\n%s", out)
	}
}

// captureReporter records BatchStream calls for assertions.
type captureReporter struct {
	NopReporter
	streams []struct {
		idx, bytes int
	}
}

func (c *captureReporter) BatchStream(idx, bytes int) {
	c.streams = append(c.streams, struct{ idx, bytes int }{idx, bytes})
}

// TestBatchStreamToken_ThrottlesAndSkipsControl pins the producer-side
// contract: content tokens accumulate, the BatchStream reporter is
// called once per ≥256-byte delta with the cumulative count, and
// control tokens (\x00TOOL_*, \x00THOUGHT_*) don't contribute bytes
// or trigger emits. The 256-byte throttle matches synthesis streaming
// and keeps the TUI from re-rendering on every token.
func TestBatchStreamToken_ThrottlesAndSkipsControl(t *testing.T) {
	rr := &captureReporter{}
	onToken := batchStreamToken(rr, 3)
	if onToken == nil {
		t.Fatal("expected non-nil onToken from batchStreamToken")
	}

	// Tiny tokens below the throttle threshold should not emit yet.
	onToken("hello ")
	onToken("world ")
	if len(rr.streams) != 0 {
		t.Fatalf("expected no emit below 256-byte threshold; got %v", rr.streams)
	}

	// Control tokens are ignored — neither counted nor emitted.
	onToken("\x00TOOL_START:read_file(foo.go)")
	onToken("\x00THOUGHT:thinking about it")
	if len(rr.streams) != 0 {
		t.Fatalf("control tokens should not trigger emits; got %v", rr.streams)
	}

	// Cross the 256-byte threshold — should emit once with the
	// cumulative total of *content* bytes only.
	onToken(strings.Repeat("a", 300))
	if len(rr.streams) != 1 {
		t.Fatalf("expected exactly one emit after threshold; got %v", rr.streams)
	}
	got := rr.streams[0]
	if got.idx != 3 {
		t.Errorf("emit idx = %d, want 3", got.idx)
	}
	if got.bytes != len("hello ")+len("world ")+300 {
		t.Errorf("emit bytes = %d, want %d (content only, no control tokens)",
			got.bytes, len("hello ")+len("world ")+300)
	}
}

// TestBatchStreamToken_NilReporter returns nil so callers can pass
// the result to ReviewBatchWithRetry without a guard.
func TestBatchStreamToken_NilReporter(t *testing.T) {
	if got := batchStreamToken(nil, 0); got != nil {
		t.Errorf("batchStreamToken(nil, 0) returned non-nil; want nil for nil reporter")
	}
}

package classify

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
)

// stubClient is a minimal ai.Client used by retry/drop tests. Each
// call to ChatStream pulls from the responses/errors queues by index;
// pad with empty entries to drive specific call patterns.
type stubClient struct {
	responses []string
	errors    []error
	calls     int32
}

func (s *stubClient) ChatStream(_ context.Context, _ string, _ []ai.Message, _ func(string)) (string, error) {
	i := int(atomic.AddInt32(&s.calls, 1)) - 1
	var resp string
	var err error
	if i < len(s.responses) {
		resp = s.responses[i]
	}
	if i < len(s.errors) {
		err = s.errors[i]
	}
	return resp, err
}

func (s *stubClient) CallCount() int { return int(atomic.LoadInt32(&s.calls)) }

// ── classifyBatchWithRetry ─────────────────────────────────────────────

func TestClassifyBatchWithRetry_RetriesTransient(t *testing.T) {
	// Transient error on first attempt → retry should succeed on the
	// second. Without retry, this batch would silently classify all
	// its files as unknown downstream, causing the AOI scan to use
	// all dimensions (expensive over-scan).
	client := &stubClient{
		errors: []error{
			errors.New("connection reset by peer"),
		},
		responses: []string{
			"", // first call: errored, response ignored
			`[{"file": "main.go", "type": "infrastructure"}]`,
		},
	}
	files := []File{{Path: "main.go", Content: "package main"}}

	// Override backoff to keep the test fast; restore on exit.
	saved := classifyRetryBackoff
	defer func() { _ = saved /* const, can't reassign — keep as documentation */ }()

	got, err := classifyBatchWithRetry(context.Background(), client, files)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("expected 2 LLM calls (1 fail + 1 retry); got %d", client.CallCount())
	}
	if len(got) != 1 || got[0].Type != FileTypeInfrastructure {
		t.Errorf("unexpected classifications: %+v", got)
	}
}

func TestClassifyBatchWithRetry_DoesNotRetryParseErrors(t *testing.T) {
	// Parse errors (model emitted prose or malformed JSON) are not
	// fixed by retrying the same prompt. Burning a second LLM call
	// on the same input is pure waste — and worse, it doubles the
	// chance of rate-limit issues affecting OTHER batches.
	client := &stubClient{
		responses: []string{
			"I cannot classify these files because I am not sure.",
		},
	}
	files := []File{{Path: "x.go", Content: "package x"}}

	_, err := classifyBatchWithRetry(context.Background(), client, files)
	if err == nil {
		t.Fatal("expected parse error to propagate")
	}
	if !errors.Is(err, errClassifyParse) {
		t.Errorf("expected errClassifyParse sentinel; got: %v", err)
	}
	if client.CallCount() != 1 {
		t.Errorf("parse errors must NOT trigger retry; got %d calls", client.CallCount())
	}
}

func TestClassifyBatchWithRetry_DoesNotRetryCanceled(t *testing.T) {
	// Context-canceled means the user / parent aborted. Retrying
	// would (a) succeed against a dead context anyway and (b) waste
	// a call. Return immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &stubClient{
		errors: []error{context.Canceled},
	}
	files := []File{{Path: "x.go", Content: "package x"}}

	_, err := classifyBatchWithRetry(ctx, client, files)
	if err == nil {
		t.Fatal("expected context cancellation to surface")
	}
	if client.CallCount() != 1 {
		t.Errorf("canceled context must not retry; got %d calls", client.CallCount())
	}
}

func TestClassifyBatchWithRetry_BothAttemptsFailTransiently(t *testing.T) {
	// Two transient failures in a row — surface the second error
	// to the caller. The caller (Classify) collects this in
	// batchErrs and fills affected files with unknown.
	client := &stubClient{
		errors: []error{
			errors.New("502 bad gateway"),
			errors.New("503 service unavailable"),
		},
	}
	files := []File{{Path: "x.go", Content: "package x"}}

	_, err := classifyBatchWithRetry(context.Background(), client, files)
	if err == nil {
		t.Fatal("expected error after both attempts failed")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected SECOND error to surface (the retry), got: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("expected exactly 2 calls; got %d", client.CallCount())
	}
}

// ── Drop visibility (silent LLM drops) ─────────────────────────────────

func TestClassify_WarnsOnLLMDroppedFiles(t *testing.T) {
	// LLM returns 2 of 3 input files. The missing file gets unknown
	// (existing behavior), but onProgress and the log must surface
	// the drop so the user knows something was lost.
	client := &stubClient{
		responses: []string{
			`[{"file": "a.go", "type": "handler"},
			  {"file": "b.go", "type": "repository"}]`,
		},
	}
	files := []File{
		{Path: "a.go", Content: "package a"},
		{Path: "b.go", Content: "package b"},
		{Path: "c.go", Content: "package c"}, // LLM omitted this one
	}

	var progressLog []string
	result, err := Classify(context.Background(), client, files, nil, func(s string) {
		progressLog = append(progressLog, s)
	})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}

	// c.go must end up as unknown (the existing safety net).
	if result["c.go"] != FileTypeUnknown {
		t.Errorf("dropped file should be unknown; got %q", result["c.go"])
	}

	// Progress must explicitly call out the silent drop.
	var sawDropWarning bool
	for _, p := range progressLog {
		if strings.Contains(p, "silently dropped") {
			sawDropWarning = true
			break
		}
	}
	if !sawDropWarning {
		t.Errorf("expected progress warning about silent drop; got: %v", progressLog)
	}
}

func TestClassify_NoDropWarningWhenAllReturned(t *testing.T) {
	// Happy path: all files come back. No drop warning.
	client := &stubClient{
		responses: []string{
			`[{"file": "a.go", "type": "handler"},
			  {"file": "b.go", "type": "repository"}]`,
		},
	}
	files := []File{
		{Path: "a.go", Content: "package a"},
		{Path: "b.go", Content: "package b"},
	}

	var progressLog []string
	_, err := Classify(context.Background(), client, files, nil, func(s string) {
		progressLog = append(progressLog, s)
	})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}

	for _, p := range progressLog {
		if strings.Contains(p, "silently dropped") {
			t.Errorf("no drop should be reported when LLM returned everything: %q", p)
		}
	}
}

// ── Invalid type logging ───────────────────────────────────────────────

func TestParseResult_LogsInvalidTypes(t *testing.T) {
	// When the LLM emits a type outside the schema (model drift,
	// hallucination), we coerce to unknown. Previously this was
	// silent — now it must surface in the log so the user can spot
	// "the model is confidently producing wrong types for half my
	// files" without inspecting raw responses.
	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOutput)

	raw := `[{"file": "x.go", "type": "controller"}]`
	result, err := parseResult(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(result) != 1 || result[0].Type != FileTypeUnknown {
		t.Errorf("invalid type should coerce to unknown; got %+v", result)
	}

	logged := buf.String()
	if !strings.Contains(logged, "controller") || !strings.Contains(logged, "x.go") {
		t.Errorf("log should mention the invalid type AND the file; got: %q", logged)
	}
}

// ── Sentinel error wrapping ────────────────────────────────────────────

func TestParseResult_WrapsParseErrorsWithSentinel(t *testing.T) {
	// Pin that errors.Is(err, errClassifyParse) works for both shapes
	// of parse failure (no array, bad JSON) — the retry logic depends
	// on this to short-circuit.
	cases := []struct {
		name string
		raw  string
	}{
		{"no array", "Sorry, I cannot classify these files."},
		{"bad JSON", "[{not valid json at all}]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseResult(tc.raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, errClassifyParse) {
				t.Errorf("expected errClassifyParse sentinel; got: %v", err)
			}
		})
	}
}

// Compile-time check that stubClient satisfies ai.Client (avoids
// silent drift if the interface gains required methods).
var _ ai.Client = (*stubClient)(nil)

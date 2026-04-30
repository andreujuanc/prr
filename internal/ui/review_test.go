package ui

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"prr/internal/ai"
	"prr/internal/state"
)

// mockClient implements ai.Client with configurable responses per call.
type mockClient struct {
	responses []mockResponse // consumed in order
	callCount atomic.Int32
}

type mockResponse struct {
	result string
	err    error
}

func (m *mockClient) ChatStream(_ context.Context, _ string, _ []ai.Message, onToken func(string)) (string, error) {
	idx := int(m.callCount.Add(1)) - 1
	if idx >= len(m.responses) {
		return "", fmt.Errorf("unexpected call #%d", idx+1)
	}
	resp := m.responses[idx]
	if resp.err != nil {
		return "", resp.err
	}
	if onToken != nil && resp.result != "" {
		onToken(resp.result)
	}
	return resp.result, nil
}

func (m *mockClient) calls() int {
	return int(m.callCount.Load())
}

// --- reviewBatchWithRetry tests ---

func validBatchJSON() string {
	return `[{"file":"main.go","purpose":"entry point","findings":"looks good"}]`
}

func TestReviewBatchWithRetry_SuccessFirstAttempt(t *testing.T) {
	client := &mockClient{
		responses: []mockResponse{
			{result: validBatchJSON()},
		},
	}
	batch := reviewBatch{label: "root", files: []string{"main.go"}, diffs: "diff"}

	result, err := reviewBatchWithRetry(context.Background(), client, "system", batch, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != validBatchJSON() {
		t.Fatalf("unexpected result: %s", result)
	}
	if client.calls() != 1 {
		t.Fatalf("expected 1 call, got %d", client.calls())
	}
}

func TestReviewBatchWithRetry_EmptyThenSuccess(t *testing.T) {
	client := &mockClient{
		responses: []mockResponse{
			{result: ""},                    // attempt 1: empty
			{result: "not json"},            // attempt 2: unparseable
			{result: validBatchJSON()},      // attempt 3: success
		},
	}
	batch := reviewBatch{label: "pkg", files: []string{"main.go"}, diffs: "diff"}

	result, err := reviewBatchWithRetry(context.Background(), client, "system", batch, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != validBatchJSON() {
		t.Fatalf("unexpected result: %s", result)
	}
	if client.calls() != 3 {
		t.Fatalf("expected 3 calls, got %d", client.calls())
	}
}

func TestReviewBatchWithRetry_AllRetriesExhausted(t *testing.T) {
	client := &mockClient{
		responses: []mockResponse{
			{result: "bad1"},
			{result: "bad2"},
			{result: "bad3"},
		},
	}
	batch := reviewBatch{label: "pkg", files: []string{"main.go"}, diffs: "diff"}

	result, err := reviewBatchWithRetry(context.Background(), client, "system", batch, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return last result as fallback
	if result != "bad3" {
		t.Fatalf("expected fallback to last result, got: %s", result)
	}
	if client.calls() != 3 {
		t.Fatalf("expected 3 calls, got %d", client.calls())
	}
}

func TestReviewBatchWithRetry_APIError(t *testing.T) {
	client := &mockClient{
		responses: []mockResponse{
			{err: fmt.Errorf("rate limited")},
		},
	}
	batch := reviewBatch{label: "pkg", files: []string{"main.go"}, diffs: "diff"}

	_, err := reviewBatchWithRetry(context.Background(), client, "system", batch, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "rate limited" {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.calls() != 1 {
		t.Fatalf("expected 1 call (no retry on API error), got %d", client.calls())
	}
}

func TestReviewBatchWithRetry_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &mockClient{
		responses: []mockResponse{
			{result: validBatchJSON()},
		},
	}
	batch := reviewBatch{label: "pkg", files: []string{"main.go"}, diffs: "diff"}

	_, err := reviewBatchWithRetry(ctx, client, "system", batch, nil)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if client.calls() != 0 {
		t.Fatalf("expected 0 calls with cancelled context, got %d", client.calls())
	}
}

// --- synthesisWithRetry tests ---

func TestSynthesisWithRetry_SuccessFirstAttempt(t *testing.T) {
	client := &mockClient{
		responses: []mockResponse{
			{result: "## Summary\nAll good"},
		},
	}
	messages := []ai.Message{{Role: "user", Content: "synthesize"}}

	result, err := synthesisWithRetry(context.Background(), client, "system", messages, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "## Summary\nAll good" {
		t.Fatalf("unexpected result: %s", result)
	}
	if client.calls() != 1 {
		t.Fatalf("expected 1 call, got %d", client.calls())
	}
}

func TestSynthesisWithRetry_EmptyThenSuccess(t *testing.T) {
	client := &mockClient{
		responses: []mockResponse{
			{result: ""},
			{result: "   "},                    // whitespace-only counts as empty
			{result: "## Final review"},
		},
	}
	messages := []ai.Message{{Role: "user", Content: "synthesize"}}

	result, err := synthesisWithRetry(context.Background(), client, "system", messages, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "## Final review" {
		t.Fatalf("unexpected result: %s", result)
	}
	if client.calls() != 3 {
		t.Fatalf("expected 3 calls, got %d", client.calls())
	}
}

func TestSynthesisWithRetry_AllEmpty(t *testing.T) {
	client := &mockClient{
		responses: []mockResponse{
			{result: ""},
			{result: ""},
			{result: ""},
		},
	}
	messages := []ai.Message{{Role: "user", Content: "synthesize"}}

	_, err := synthesisWithRetry(context.Background(), client, "system", messages, nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if err.Error() != "synthesis returned empty response" {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.calls() != 3 {
		t.Fatalf("expected 3 calls, got %d", client.calls())
	}
}

func TestSynthesisWithRetry_APIError(t *testing.T) {
	client := &mockClient{
		responses: []mockResponse{
			{result: ""},
			{err: fmt.Errorf("server error")},
		},
	}
	messages := []ai.Message{{Role: "user", Content: "synthesize"}}

	_, err := synthesisWithRetry(context.Background(), client, "system", messages, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "server error" {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.calls() != 2 {
		t.Fatalf("expected 2 calls, got %d", client.calls())
	}
}

// --- parseBatchResult tests ---

func TestParseBatchResult_ValidJSON(t *testing.T) {
	input := `[{"file":"a.go","purpose":"does stuff","findings":"issue found"}]`
	result := parseBatchResult(input)
	if result == nil {
		t.Fatal("expected parsed result, got nil")
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result[0].File != "a.go" || result[0].Purpose != "does stuff" || result[0].Findings != "issue found" {
		t.Fatalf("unexpected entry: %+v", result[0])
	}
}

func TestParseBatchResult_WithCodeFences(t *testing.T) {
	input := "```json\n" + `[{"file":"b.go","purpose":"helper","findings":""}]` + "\n```"
	result := parseBatchResult(input)
	if result == nil {
		t.Fatal("expected parsed result, got nil")
	}
	if len(result) != 1 || result[0].File != "b.go" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseBatchResult_InvalidJSON(t *testing.T) {
	result := parseBatchResult("this is not json")
	if result != nil {
		t.Fatalf("expected nil for invalid JSON, got: %+v", result)
	}
}

func TestParseBatchResult_Empty(t *testing.T) {
	result := parseBatchResult("")
	if result != nil {
		t.Fatalf("expected nil for empty input, got: %+v", result)
	}
}

// --- persistBatchFindings + isBatchCached tests ---

func TestPersistBatchFindings_AllFilesCached(t *testing.T) {
	rs := state.NewState("1")
	rs.Files["a.go"] = &state.FileState{Status: state.StatusUnreviewed, DiffHash: "h1"}
	rs.Files["b.go"] = &state.FileState{Status: state.StatusUnreviewed, DiffHash: "h2"}

	batch := reviewBatch{label: "root", files: []string{"a.go", "b.go"}}
	rawResult := `[{"file":"a.go","purpose":"entry","findings":"issue"},{"file":"b.go","purpose":"helper","findings":""}]`

	parsed, ff := persistBatchFindings(rs, batch, rawResult)
	if parsed == nil {
		t.Fatal("expected parsed result")
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 parsed entries, got %d", len(parsed))
	}

	// Both files should be cached
	if !isBatchCached(batch, rs) {
		t.Fatal("expected batch to be cached after persist")
	}

	// a.go has findings, b.go doesn't
	if ff["a.go"] != "issue" {
		t.Fatalf("expected findings for a.go, got %q", ff["a.go"])
	}
	if _, ok := ff["b.go"]; ok {
		t.Fatal("b.go should not have findings entry (clean file)")
	}
}

func TestPersistBatchFindings_EmptyPurposeDefaulted(t *testing.T) {
	rs := state.NewState("1")
	rs.Files["x.go"] = &state.FileState{Status: state.StatusUnreviewed, DiffHash: "h1"}

	batch := reviewBatch{label: "root", files: []string{"x.go"}}
	// AI returns empty purpose
	rawResult := `[{"file":"x.go","purpose":"","findings":"something"}]`

	persistBatchFindings(rs, batch, rawResult)

	if rs.Files["x.go"].Purpose != "reviewed" {
		t.Fatalf("expected Purpose to default to 'reviewed', got %q", rs.Files["x.go"].Purpose)
	}
	if !isBatchCached(batch, rs) {
		t.Fatal("batch should be cached even with empty AI purpose (defaulted)")
	}
}

func TestPersistBatchFindings_AIOmitsFile(t *testing.T) {
	rs := state.NewState("1")
	rs.Files["a.go"] = &state.FileState{Status: state.StatusUnreviewed, DiffHash: "h1"}
	rs.Files["b.go"] = &state.FileState{Status: state.StatusUnreviewed, DiffHash: "h2"}

	batch := reviewBatch{label: "root", files: []string{"a.go", "b.go"}}
	// AI only returns a.go, omits b.go
	rawResult := `[{"file":"a.go","purpose":"main entry","findings":""}]`

	persistBatchFindings(rs, batch, rawResult)

	// a.go should have purpose from AI
	if rs.Files["a.go"].Purpose != "main entry" {
		t.Fatalf("expected 'main entry', got %q", rs.Files["a.go"].Purpose)
	}
	// b.go should have fallback purpose
	if rs.Files["b.go"].Purpose != "reviewed (no details)" {
		t.Fatalf("expected 'reviewed (no details)', got %q", rs.Files["b.go"].Purpose)
	}
	// Batch should still be cached
	if !isBatchCached(batch, rs) {
		t.Fatal("batch should be cached even when AI omits files")
	}
}

func TestPersistBatchFindings_UnparseableFallback(t *testing.T) {
	rs := state.NewState("1")
	rs.Files["a.go"] = &state.FileState{Status: state.StatusUnreviewed, DiffHash: "h1"}
	rs.Files["b.go"] = &state.FileState{Status: state.StatusUnreviewed, DiffHash: "h2"}

	batch := reviewBatch{label: "root", files: []string{"a.go", "b.go"}}

	persistBatchFindings(rs, batch, "not json at all")

	// All files should have purpose set (fallback)
	for _, f := range batch.files {
		if rs.Files[f].Purpose == "" {
			t.Fatalf("file %q should have Purpose set in fallback mode", f)
		}
	}
	if !isBatchCached(batch, rs) {
		t.Fatal("batch should be cached even with unparseable fallback")
	}
}

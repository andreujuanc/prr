package ui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/state"
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
	batch := reviewBatch{Label: "root", Files: []string{"main.go"}, Diffs: "diff"}

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
			{result: ""},               // attempt 1: empty
			{result: "not json"},       // attempt 2: unparseable
			{result: validBatchJSON()}, // attempt 3: success
		},
	}
	batch := reviewBatch{Label: "pkg", Files: []string{"main.go"}, Diffs: "diff"}

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
	batch := reviewBatch{Label: "pkg", Files: []string{"main.go"}, Diffs: "diff"}

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

// TestReviewBatchWithRetry_TerminalAPIError pins that non-transient
// API errors (bad credentials, malformed requests) short-circuit
// without retry — retrying would just waste tokens.
func TestReviewBatchWithRetry_TerminalAPIError(t *testing.T) {
	client := &mockClient{
		responses: []mockResponse{
			{err: fmt.Errorf("invalid api key")},
		},
	}
	batch := reviewBatch{Label: "pkg", Files: []string{"main.go"}, Diffs: "diff"}

	_, err := reviewBatchWithRetry(context.Background(), client, "system", batch, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "invalid api key" {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.calls() != 1 {
		t.Fatalf("expected 1 call (no retry on terminal API error), got %d", client.calls())
	}
}

// TestReviewBatchWithRetry_TransientAPIErrorThenSuccess pins the
// retry-on-transient-error contract added when we removed the
// watchdog ceremony. A rate-limit blip on attempt 1 followed by a
// clean response on attempt 2 must succeed without surfacing an
// error — without this, a single transient hiccup would silently
// drop findings for the whole batch.
func TestReviewBatchWithRetry_TransientAPIErrorThenSuccess(t *testing.T) {
	client := &mockClient{
		responses: []mockResponse{
			{err: fmt.Errorf("rate limited")},
			{result: validBatchJSON()},
		},
	}
	batch := reviewBatch{Label: "pkg", Files: []string{"main.go"}, Diffs: "diff"}

	result, err := reviewBatchWithRetry(context.Background(), client, "system", batch, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != validBatchJSON() {
		t.Fatalf("unexpected result: %s", result)
	}
	if client.calls() != 2 {
		t.Fatalf("expected 2 calls (retry then success), got %d", client.calls())
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
	batch := reviewBatch{Label: "pkg", Files: []string{"main.go"}, Diffs: "diff"}

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
			{result: "   "}, // whitespace-only counts as empty
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
	if result[0].File != "a.go" || result[0].Purpose != "does stuff" || result[0].Findings.Text() != "issue found" {
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

	batch := reviewBatch{Label: "root", Files: []string{"a.go", "b.go"}}
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

	batch := reviewBatch{Label: "root", Files: []string{"x.go"}}
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

	batch := reviewBatch{Label: "root", Files: []string{"a.go", "b.go"}}
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

	batch := reviewBatch{Label: "root", Files: []string{"a.go", "b.go"}}

	persistBatchFindings(rs, batch, "not json at all")

	// All files should have purpose set (fallback)
	for _, f := range batch.Files {
		if rs.Files[f].Purpose == "" {
			t.Fatalf("file %q should have Purpose set in fallback mode", f)
		}
	}
	if !isBatchCached(batch, rs) {
		t.Fatal("batch should be cached even with unparseable fallback")
	}
}

// ── buildReviewBatches tests (pure function, no mocks, no API) ──────────

func TestBuildReviewBatches_SingleFile(t *testing.T) {
	diffs := map[string]string{
		"main.go": "@@ -1,3 +1,5 @@\n package main\n+import \"fmt\"\n func main() {\n-    println(\"hi\")\n+    fmt.Println(\"hi\")\n }",
	}

	batches := buildReviewBatches(diffs)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if len(batches[0].Files) != 1 || batches[0].Files[0] != "main.go" {
		t.Fatalf("expected [main.go], got %v", batches[0].Files)
	}
	if batches[0].Label != "root" {
		t.Errorf("expected label 'root' for root-level file, got %q", batches[0].Label)
	}
	if !strings.Contains(batches[0].Diffs, "=== main.go ===") {
		t.Error("batch diffs should contain the file header")
	}
}

func TestBuildReviewBatches_GroupsByDirectory(t *testing.T) {
	diffs := map[string]string{
		"internal/ui/model.go": "diff-model",
		"internal/ui/style.go": "diff-style",
		"internal/ai/agent.go": "diff-agent",
		"main.go":              "diff-main",
	}

	batches := buildReviewBatches(diffs)

	// Should produce 3 batches: internal/ai, internal/ui, root
	// (sorted alphabetically by directory)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches (ai, ui, root), got %d: %v", len(batches), batchLabels(batches))
	}

	labels := batchLabels(batches)
	sort.Strings(labels)
	expected := []string{"internal/ai", "internal/ui", "root"}
	for i, l := range labels {
		if l != expected[i] {
			t.Errorf("batch %d: expected label %q, got %q", i, expected[i], l)
		}
	}

	// internal/ui batch should have 2 files
	for _, b := range batches {
		if b.Label == "internal/ui" {
			if len(b.Files) != 2 {
				t.Errorf("internal/ui batch: expected 2 files, got %d", len(b.Files))
			}
		}
	}
}

func TestBuildReviewBatches_LargeFileSplitsBatch(t *testing.T) {
	// Create a diff larger than batchMaxChars (20KB)
	largeDiff := strings.Repeat("+added line\n", batchMaxChars/12+1)
	smallDiff := "+small change"

	diffs := map[string]string{
		"pkg/big.go":   largeDiff,
		"pkg/small.go": smallDiff,
	}

	batches := buildReviewBatches(diffs)

	// big.go and small.go are in the same directory but big.go exceeds the limit,
	// so they should be in separate batches
	if len(batches) < 2 {
		t.Fatalf("expected at least 2 batches (large file should split), got %d", len(batches))
	}

	// All files should be present across batches
	allFiles := make(map[string]bool)
	for _, b := range batches {
		for _, f := range b.Files {
			allFiles[f] = true
		}
	}
	if !allFiles["pkg/big.go"] || !allFiles["pkg/small.go"] {
		t.Errorf("expected both files across batches, got: %v", allFiles)
	}
}

func TestBuildReviewBatches_ExcludedFilesSkipped(t *testing.T) {
	diffs := map[string]string{
		"main.go":           "diff-main",
		"go.sum":            "diff-gosum",  // excluded by default
		"vendor/lib/lib.go": "diff-vendor", // excluded by vendor/** pattern
		"style.min.css":     "diff-css",    // excluded by *.min.css
	}

	batches := buildReviewBatches(diffs)

	// Only main.go should remain
	allFiles := make(map[string]bool)
	for _, b := range batches {
		for _, f := range b.Files {
			allFiles[f] = true
		}
	}
	if !allFiles["main.go"] {
		t.Error("main.go should be included")
	}
	for _, excluded := range []string{"go.sum", "vendor/lib/lib.go", "style.min.css"} {
		if allFiles[excluded] {
			t.Errorf("%s should be excluded from review batches", excluded)
		}
	}
}

func TestBuildReviewBatches_EmptyInput(t *testing.T) {
	batches := buildReviewBatches(map[string]string{})
	if len(batches) != 0 {
		t.Fatalf("expected 0 batches for empty input, got %d", len(batches))
	}
}

func TestBuildReviewBatches_AllExcluded(t *testing.T) {
	diffs := map[string]string{
		"go.sum":            "diff1",
		"package-lock.json": "diff2",
	}
	batches := buildReviewBatches(diffs)
	if len(batches) != 0 {
		t.Fatalf("expected 0 batches when all files excluded, got %d", len(batches))
	}
}

func TestBuildReviewBatches_DeterministicOrder(t *testing.T) {
	diffs := map[string]string{
		"z/z.go": "diff-z",
		"a/a.go": "diff-a",
		"m/m.go": "diff-m",
	}

	// Run multiple times to verify determinism
	var firstLabels []string
	for i := range 5 {
		batches := buildReviewBatches(diffs)
		labels := batchLabels(batches)
		if i == 0 {
			firstLabels = labels
		} else {
			for j, l := range labels {
				if l != firstLabels[j] {
					t.Fatalf("non-deterministic order on iteration %d: %v vs %v", i, labels, firstLabels)
				}
			}
		}
	}

	// Should be alphabetically sorted
	if !sort.StringsAreSorted(firstLabels) {
		t.Errorf("batches not sorted: %v", firstLabels)
	}
}

func batchLabels(batches []reviewBatch) []string {
	labels := make([]string, len(batches))
	for i, b := range batches {
		labels[i] = b.Label
	}
	return labels
}

// ── capDiff tests (pure function) ───────────────────────────────────────

func TestCapDiff_ShortDiffUnchanged(t *testing.T) {
	diff := "+line1\n+line2\n+line3"
	result := capDiff(diff, []string{"a.go"})
	if result != diff {
		t.Errorf("short diff should be returned unchanged")
	}
}

func TestCapDiff_LongDiffTruncated(t *testing.T) {
	// Create a diff with more than maxDiffLines lines
	lines := make([]string, maxDiffLines+500)
	for i := range lines {
		lines[i] = fmt.Sprintf("+line %d", i)
	}
	diff := strings.Join(lines, "\n")

	result := capDiff(diff, []string{"a.go", "b.go"})

	resultLines := strings.Split(result, "\n")
	// Should have maxDiffLines of content plus the truncation notice
	if len(resultLines) <= maxDiffLines {
		t.Errorf("expected truncation notice appended, got %d lines", len(resultLines))
	}
	if !strings.Contains(result, "diff truncated") {
		t.Error("expected truncation notice in result")
	}
	if !strings.Contains(result, "a.go b.go") {
		t.Error("expected file paths in truncation notice")
	}
}

// ── collectCachedFindings tests (pure function) ─────────────────────────

func TestCollectCachedFindings_WithFindings(t *testing.T) {
	rs := state.NewState("1")
	rs.Files["a.go"] = &state.FileState{Purpose: "entry point", BatchFindings: "issue in a.go"}
	rs.Files["b.go"] = &state.FileState{Purpose: "helper", BatchFindings: ""}
	rs.Files["c.go"] = &state.FileState{Purpose: "util", BatchFindings: "bug in c.go"}

	batch := reviewBatch{Files: []string{"a.go", "b.go", "c.go"}}
	text, ff := collectCachedFindings(batch, rs)

	// Only files with findings should be in the output
	if !strings.Contains(text, "a.go") {
		t.Error("expected a.go in cached findings text")
	}
	if !strings.Contains(text, "c.go") {
		t.Error("expected c.go in cached findings text")
	}
	if strings.Contains(text, "### b.go") {
		t.Error("b.go has no findings, should not be in text")
	}

	if ff["a.go"] != "issue in a.go" {
		t.Errorf("expected findings for a.go, got %q", ff["a.go"])
	}
	if _, ok := ff["b.go"]; ok {
		t.Error("b.go should not be in fileFindings map (no findings)")
	}
	if ff["c.go"] != "bug in c.go" {
		t.Errorf("expected findings for c.go, got %q", ff["c.go"])
	}
}

func TestCollectCachedFindings_AllClean(t *testing.T) {
	rs := state.NewState("1")
	rs.Files["a.go"] = &state.FileState{Purpose: "clean", BatchFindings: ""}

	batch := reviewBatch{Files: []string{"a.go"}}
	text, ff := collectCachedFindings(batch, rs)

	if strings.TrimSpace(text) != "" {
		t.Errorf("expected empty text for all-clean batch, got %q", text)
	}
	if len(ff) != 0 {
		t.Errorf("expected empty fileFindings, got %v", ff)
	}
}

// ── buildBatchSystemPrompt / buildBatchMessages tests (pure) ────────────

func TestBuildBatchSystemPrompt_WithCustomInstructions(t *testing.T) {
	prompt := buildBatchSystemPrompt("PR #42: Add feature", "Focus on security")
	if !strings.Contains(prompt, "PR #42: Add feature") {
		t.Error("expected PR metadata in prompt")
	}
	if !strings.Contains(prompt, "Focus on security") {
		t.Error("expected custom instructions in prompt")
	}
	if !strings.Contains(prompt, "Project-Specific Instructions") {
		t.Error("expected instructions header in prompt")
	}
}

func TestBuildBatchSystemPrompt_WithoutCustomInstructions(t *testing.T) {
	prompt := buildBatchSystemPrompt("PR #42", "")
	if strings.Contains(prompt, "Project-Specific Instructions") {
		t.Error("should not include instructions header when empty")
	}
}

func TestBuildBatchMessages_ContainsFileListAndDiffs(t *testing.T) {
	batch := reviewBatch{
		Label: "pkg",
		Files: []string{"pkg/a.go", "pkg/b.go"},
		Diffs: "=== pkg/a.go ===\n+line\n\n=== pkg/b.go ===\n+other\n",
	}
	msgs := buildBatchMessages(batch)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected user role, got %q", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "2 file(s)") {
		t.Error("expected file count in message")
	}
	if !strings.Contains(msgs[0].Content, "pkg/a.go") {
		t.Error("expected file name in message")
	}
}

// ── isBatchCached tests (pure function) ─────────────────────────────────

func TestIsBatchCached_NilState(t *testing.T) {
	batch := reviewBatch{Files: []string{"a.go"}}
	if isBatchCached(batch, nil) {
		t.Error("should return false for nil state")
	}
}

func TestIsBatchCached_MissingFile(t *testing.T) {
	rs := state.NewState("1")
	rs.Files["a.go"] = &state.FileState{Purpose: "cached"}
	// b.go not in state
	batch := reviewBatch{Files: []string{"a.go", "b.go"}}
	if isBatchCached(batch, rs) {
		t.Error("should return false when a file is missing from state")
	}
}

func TestIsBatchCached_EmptyPurpose(t *testing.T) {
	rs := state.NewState("1")
	rs.Files["a.go"] = &state.FileState{Purpose: ""}
	batch := reviewBatch{Files: []string{"a.go"}}
	if isBatchCached(batch, rs) {
		t.Error("should return false when Purpose is empty")
	}
}

// ── Live integration tests: full review orchestrators (no mocks) ────────
// Requires PRR_LIVE_TESTS=1. Credentials are read from ~/.config/prr/config.json.
// These test the actual orchestrator functions
// (reviewBatchesSequential, reviewBatchesParallel, runSynthesis,
// streamMultiPassReview) through the ReviewReporter interface.

func skipWithoutAPIKey(t *testing.T) *config.Config {
	t.Helper()
	if os.Getenv("PRR_LIVE_TESTS") != "1" {
		t.Skip("PRR_LIVE_TESTS=1 not set, skipping live API test")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("no valid config: %v", err)
	}
	return cfg
}

func newLiveAgent(t *testing.T, cfg *config.Config) *ai.Agent {
	t.Helper()
	strongRef, err := config.ParseModelRef(cfg.StrongModel)
	if err != nil {
		t.Fatalf("invalid strong_model: %v", err)
	}
	pc := cfg.ProviderConfigFor(strongRef.Provider)

	modelConfigs, err := config.LoadModels()
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	mcfg := config.GetModelConfig(modelConfigs, strongRef.ModelID)

	provider, err := ai.NewProvider(ai.ProviderConfig{
		ProviderName:    strongRef.Provider,
		ModelID:         strongRef.ModelID,
		APIKey:          pc.APIKey,
		BaseURL:         pc.BaseURL,
		MaxOutputTokens: mcfg.MaxOutputTokens,
		Temperature:     mcfg.Temperature,
		ThinkingBudget:  mcfg.ThinkingBudget.Review,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return ai.NewAgent(provider, &ai.ToolExecutor{HeadRef: "HEAD", BaseRef: "HEAD"})
}

// testReporter implements ReviewReporter and records all events for assertions.
type testReporter struct {
	mu        sync.Mutex
	batches   []AIReviewBatchInfo
	progress  []AIReviewProgressMsg
	synthesis bool
	tokens    []string
}

func (r *testReporter) PhaseProgress(phase, status string, done bool) {
	// test recorder — not checked in existing tests
}

func (r *testReporter) AOIProgress(status string, done bool, aoiCount int) {
	// test recorder — not checked in existing tests
}

func (r *testReporter) InitBatches(batches []AIReviewBatchInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = batches
}

func (r *testReporter) BatchProgress(batch int, status AIReviewBatchStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, AIReviewProgressMsg{Batch: batch, Status: status})
}

func (r *testReporter) SynthesisStarted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.synthesis = true
}

func (r *testReporter) Token(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = append(r.tokens, token)
}

func (r *testReporter) batchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.batches)
}

func (r *testReporter) hasStatus(batch int, status AIReviewBatchStatus) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.progress {
		if p.Batch == batch && p.Status == status {
			return true
		}
	}
	return false
}

// sampleDiffs returns a small Go PR with intentional issues.
func sampleDiffs() map[string]string {
	return map[string]string{
		"main.go": `@@ -1,5 +1,13 @@
 package main
+import "fmt"
 func main() {
-    println("hello")
+    fmt.Println(greet("world"))
+}
+func greet(name string) string {
+    return "Hello, " + name
 }`,
		"util.go": `@@ -0,0 +1,8 @@
+package main
+
+func reverse(s string) string {
+    // TODO: implement
+    return s
+}
+
+func unused() {}`,
		"go.sum": "h1:abc123=\nh1:def456=\n", // should be excluded
	}
}

const samplePRMeta = `PR #99: Add greeting utilities
Author: test-user
Description: Refactor main to use a new greet function and add utilities.
Base: main → Head: feature/greet`

func newReviewState(diffs map[string]string) *state.State {
	rs := state.NewState("99")
	for path := range diffs {
		rs.Files[path] = &state.FileState{
			Status:   state.StatusUnreviewed,
			DiffHash: "test-hash",
		}
	}
	return rs
}

// verifyStructuredReview checks common invariants of a structured review.
func verifyStructuredReview(t *testing.T, structured *state.ReviewOutput, reviewedFiles map[string]bool) {
	t.Helper()
	if structured == nil {
		t.Fatal("structured review is nil")
	}
	if structured.Summary == "" {
		t.Error("structured review has empty Summary")
	}
	validVerdicts := map[string]bool{"approve": true, "request_changes": true, "comment": true}
	if !validVerdicts[structured.Verdict] {
		t.Errorf("unexpected verdict %q", structured.Verdict)
	}
	if structured.Findings == nil {
		t.Error("Findings should be non-nil")
	}
	if structured.MissingTests == nil {
		t.Error("MissingTests should be non-nil")
	}
	if structured.QuestionsForAuthor == nil {
		t.Error("QuestionsForAuthor should be non-nil")
	}
	for _, f := range structured.Findings {
		if f.File != "" && !reviewedFiles[f.File] {
			t.Errorf("finding references file %q which was not in the PR", f.File)
		}
	}
	t.Logf("Verdict: %s | Findings: %d | Summary: %.100s", structured.Verdict, len(structured.Findings), structured.Summary)
}

// TestLive_ReviewSequential exercises reviewBatchesSequential through the
// full pipeline with real Gemini API. No mocks.
func TestLive_ReviewSequential(t *testing.T) {
	cfg := skipWithoutAPIKey(t)
	agent := newLiveAgent(t, cfg)
	rawDiffs := sampleDiffs()
	rs := newReviewState(rawDiffs)
	rr := &testReporter{}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	batches := buildReviewBatches(rawDiffs)
	if len(batches) == 0 {
		t.Fatal("expected at least 1 batch")
	}
	t.Logf("Batches: %d (%v)", len(batches), batchLabels(batches))

	msg := reviewBatchesSequential(ctx, agent, samplePRMeta, rawDiffs, "", rs, batches, rr)

	done, ok := msg.(AIChatDoneMsg)
	if !ok {
		t.Fatalf("expected AIChatDoneMsg, got %T", msg)
	}
	if done.Err != nil {
		t.Fatalf("review error: %v", done.Err)
	}

	// Verify reporter events
	if !rr.synthesis {
		t.Error("SynthesisStarted was never called")
	}
	for i := range batches {
		if !rr.hasStatus(i, BatchDone) && !rr.hasStatus(i, BatchCached) {
			t.Errorf("batch %d never reached Done or Cached status", i)
		}
	}

	// Verify state was populated
	for _, b := range batches {
		for _, f := range b.Files {
			if rs.Files[f].Purpose == "" {
				t.Errorf("file %q has empty Purpose", f)
			}
		}
	}

	// Verify structured output
	reviewedFiles := make(map[string]bool)
	for _, b := range batches {
		for _, f := range b.Files {
			reviewedFiles[f] = true
		}
	}
	verifyStructuredReview(t, done.StructuredReview, reviewedFiles)

	// Verify synthesis tokens were streamed
	if len(rr.tokens) == 0 {
		t.Error("expected synthesis tokens to be streamed")
	}
}

// TestLive_ReviewMultiBatch uses files in different directories to force
// multiple batches, then runs the sequential orchestrator.
func TestLive_ReviewMultiBatch(t *testing.T) {
	cfg := skipWithoutAPIKey(t)
	agent := newLiveAgent(t, cfg)

	rawDiffs := map[string]string{
		"cmd/main.go": `@@ -0,0 +1,7 @@
+package main
+
+import "fmt"
+
+func main() {
+    fmt.Println("hello")
+}`,
		"internal/greet/greet.go": `@@ -0,0 +1,6 @@
+package greet
+
+func Hello(name string) string {
+    return "Hello, " + name
+}`,
		"internal/greet/greet_test.go": `@@ -0,0 +1,10 @@
+package greet
+
+import "testing"
+
+func TestHello(t *testing.T) {
+    got := Hello("world")
+    if got != "Hello, world" {
+        t.Errorf("got %q", got)
+    }
+}`,
	}

	rs := newReviewState(rawDiffs)
	rr := &testReporter{}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	batches := buildReviewBatches(rawDiffs)
	if len(batches) < 2 {
		t.Fatalf("expected multiple batches for files in different dirs, got %d", len(batches))
	}
	t.Logf("Batches: %d (%v)", len(batches), batchLabels(batches))

	msg := reviewBatchesSequential(ctx, agent, samplePRMeta, rawDiffs, "", rs, batches, rr)

	done, ok := msg.(AIChatDoneMsg)
	if !ok {
		t.Fatalf("expected AIChatDoneMsg, got %T", msg)
	}
	if done.Err != nil {
		t.Fatalf("review error: %v", done.Err)
	}

	// All batches should be done
	for i := range batches {
		if !rr.hasStatus(i, BatchDone) {
			t.Errorf("batch %d not done", i)
		}
	}

	reviewedFiles := make(map[string]bool)
	for _, b := range batches {
		for _, f := range b.Files {
			reviewedFiles[f] = true
		}
	}
	verifyStructuredReview(t, done.StructuredReview, reviewedFiles)
}

// TestLive_ReviewCacheSkip runs the sequential orchestrator twice.
// The second run should skip all batches (cached) but still produce
// a valid synthesis result.
func TestLive_ReviewCacheSkip(t *testing.T) {
	cfg := skipWithoutAPIKey(t)
	agent := newLiveAgent(t, cfg)
	rawDiffs := sampleDiffs()
	rs := newReviewState(rawDiffs)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	batches := buildReviewBatches(rawDiffs)

	// ── First run: populate cache ──────────────────────────────────────
	rr1 := &testReporter{}
	msg1 := reviewBatchesSequential(ctx, agent, samplePRMeta, rawDiffs, "", rs, batches, rr1)
	done1, ok := msg1.(AIChatDoneMsg)
	if !ok || done1.Err != nil {
		t.Fatalf("first run failed: %v", done1.Err)
	}
	t.Log("First run completed, cache populated")

	// Verify all batches are now cached
	for _, batch := range batches {
		if !isBatchCached(batch, rs) {
			t.Fatalf("batch %q should be cached after first run", batch.Label)
		}
	}

	// ── Second run: should use cache ───────────────────────────────────
	rr2 := &testReporter{}
	msg2 := reviewBatchesSequential(ctx, agent, samplePRMeta, rawDiffs, "", rs, batches, rr2)
	done2, ok := msg2.(AIChatDoneMsg)
	if !ok || done2.Err != nil {
		t.Fatalf("second run failed: %v", done2.Err)
	}

	// All batches should report Cached status
	for i := range batches {
		if !rr2.hasStatus(i, BatchCached) {
			t.Errorf("batch %d should be Cached on second run", i)
		}
		if rr2.hasStatus(i, BatchActive) {
			t.Errorf("batch %d should NOT be Active on second run (should be cached)", i)
		}
	}

	// Should still produce a valid structured review
	reviewedFiles := make(map[string]bool)
	for _, b := range batches {
		for _, f := range b.Files {
			reviewedFiles[f] = true
		}
	}
	verifyStructuredReview(t, done2.StructuredReview, reviewedFiles)
	t.Log("Second run used cache successfully")
}

// TestLive_ReviewParallel exercises reviewBatchesParallel with multiple
// batches and a worker pool.
func TestLive_ReviewParallel(t *testing.T) {
	cfg := skipWithoutAPIKey(t)
	agent := newLiveAgent(t, cfg)

	// Use files in different directories to force multiple batches
	rawDiffs := map[string]string{
		"cmd/main.go": `@@ -0,0 +1,5 @@
+package main
+
+func main() {
+    println("hello")
+}`,
		"pkg/util.go": `@@ -0,0 +1,5 @@
+package pkg
+
+func Add(a, b int) int {
+    return a + b
+}`,
	}

	rs := newReviewState(rawDiffs)
	rr := &testReporter{}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	batches := buildReviewBatches(rawDiffs)
	if len(batches) < 2 {
		t.Fatalf("expected multiple batches, got %d", len(batches))
	}
	t.Logf("Batches: %d (%v), workers: 2", len(batches), batchLabels(batches))

	msg := reviewBatchesParallel(ctx, agent, samplePRMeta, rawDiffs, "", rs, batches, 2, rr)

	done, ok := msg.(AIChatDoneMsg)
	if !ok {
		t.Fatalf("expected AIChatDoneMsg, got %T", msg)
	}
	if done.Err != nil {
		t.Fatalf("parallel review error: %v", done.Err)
	}

	// All batches should be done
	for i := range batches {
		if !rr.hasStatus(i, BatchDone) {
			t.Errorf("batch %d not done", i)
		}
	}

	reviewedFiles := make(map[string]bool)
	for _, b := range batches {
		for _, f := range b.Files {
			reviewedFiles[f] = true
		}
	}
	verifyStructuredReview(t, done.StructuredReview, reviewedFiles)
}

// TestLive_StreamMultiPassReview exercises the top-level orchestrator
// streamMultiPassReview, which is the actual function called by the UI.
func TestLive_StreamMultiPassReview(t *testing.T) {
	cfg := skipWithoutAPIKey(t)
	agent := newLiveAgent(t, cfg)
	rawDiffs := sampleDiffs()
	rs := newReviewState(rawDiffs)
	rr := &testReporter{}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := streamMultiPassReview(ctx, agent, nil, nil, samplePRMeta, rawDiffs, "", rs, 1, rr, "", "", 3, "", nil)
	msg := cmd() // execute the tea.Cmd

	done, ok := msg.(AIChatDoneMsg)
	if !ok {
		t.Fatalf("expected AIChatDoneMsg, got %T", msg)
	}
	if done.Err != nil {
		t.Fatalf("streamMultiPassReview error: %v", done.Err)
	}

	// Verify reporter received init
	if rr.batchCount() == 0 {
		t.Error("InitBatches was never called")
	}
	if !rr.synthesis {
		t.Error("SynthesisStarted was never called")
	}

	// Verify full output
	if done.Review == nil {
		t.Fatal("Review is nil")
	}
	if done.Review.Summary == "" {
		t.Error("Review.Summary is empty")
	}
	if done.FullResponse == "" {
		t.Error("FullResponse is empty")
	}

	// Verify structured review
	reviewedFiles := make(map[string]bool)
	batches := buildReviewBatches(rawDiffs)
	for _, b := range batches {
		for _, f := range b.Files {
			reviewedFiles[f] = true
		}
	}
	verifyStructuredReview(t, done.StructuredReview, reviewedFiles)

	// Verify file findings map
	if done.FileFindings == nil {
		t.Error("FileFindings map is nil")
	}
}

// --- renderStructuredReview coverage section tests ---

func TestRenderStructuredReview_CoverageSectionPresent(t *testing.T) {
	review := &state.ReviewOutput{
		Summary: "x",
		Verdict: "approve",
		Coverage: &state.ReviewCoverage{
			Files: []state.FileCoverage{
				{File: "a.go", AOIsScanned: 3, Findings: 1, MaxFindingSeverity: "high"},
				{File: "b.go", AOIsScanned: 2, Dismissals: 2, AvgDismissConf: 82},
			},
			FilesInScope:  3,
			FilesWithAOIs: 2,
			FilesReviewed: 2,
			OrphanFiles:   []string{"c.go"},
		},
	}
	rendered, _ := renderStructuredReview(review, 100, -1, nil, false)
	for _, want := range []string{
		"COVERAGE", "a.go", "b.go", "1 finding", "2 dismissed", "avg conf 82",
		"Orphan files", "c.go",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered coverage missing %q\nfull:\n%s", want, rendered)
		}
	}
	// Singular AOI count must render as "1 AOI", not "1 AOIs".
	if strings.Contains(rendered, "1 AOIs") {
		t.Errorf("singular AOI count should render as '1 AOI', got '1 AOIs'\n%s", rendered)
	}
	if strings.Contains(rendered, "1 findings") {
		t.Errorf("singular finding count should render as '1 finding', got '1 findings'\n%s", rendered)
	}
}

func TestRenderStructuredReview_NoCoverageWhenNil(t *testing.T) {
	review := &state.ReviewOutput{Summary: "x", Verdict: "approve"}
	rendered, _ := renderStructuredReview(review, 80, -1, nil, false)
	if strings.Contains(rendered, "COVERAGE") {
		t.Errorf("coverage section must not appear when nil: %s", rendered)
	}
}

// --- renderStructuredReview stale banner tests ---

func TestRenderStructuredReview_StaleBanner(t *testing.T) {
	review := &state.ReviewOutput{
		Summary: "Looks good overall.",
		Verdict: "approve",
	}

	t.Run("no stale banner when not stale", func(t *testing.T) {
		rendered, _ := renderStructuredReview(review, 80, -1, nil, false)
		if strings.Contains(rendered, "STALE") {
			t.Error("expected no STALE banner, but found one")
		}
	})

	t.Run("stale banner shown when stale", func(t *testing.T) {
		rendered, _ := renderStructuredReview(review, 80, -1, nil, true)
		if !strings.Contains(rendered, "STALE") {
			t.Error("expected STALE banner, but not found")
		}
		if !strings.Contains(rendered, "diffs have changed") {
			t.Error("expected staleness explanation text")
		}
	})
}

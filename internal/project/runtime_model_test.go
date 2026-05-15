package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// ── Pure-logic tests (no LLM) ───────────────────────────────────────────

func TestExtractJSONObject_Bare(t *testing.T) {
	raw := `{"auth_model":"x","entry_points":[]}`
	got := extractJSONObject(raw)
	if got != raw {
		t.Errorf("bare JSON should pass through, got %q", got)
	}
}

func TestExtractJSONObject_WithPreamble(t *testing.T) {
	raw := "Here's the model:\n" + `{"auth_model":"x"}` + "\nDone."
	want := `{"auth_model":"x"}`
	if got := extractJSONObject(raw); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractJSONObject_FencedBlock(t *testing.T) {
	raw := "```json\n{\"auth_model\":\"x\"}\n```"
	want := `{"auth_model":"x"}`
	if got := extractJSONObject(raw); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractJSONObject_NestedBraces(t *testing.T) {
	raw := `{"entry_points":[{"kind":"http","retry_model":"none"}]}`
	if got := extractJSONObject(raw); got != raw {
		t.Errorf("nested braces should round-trip, got %q", got)
	}
}

func TestExtractJSONObject_BraceInString(t *testing.T) {
	// A literal `}` inside a JSON string must not close the object early.
	raw := `{"auth_model":"check } here","other":1}`
	if got := extractJSONObject(raw); got != raw {
		t.Errorf("brace-in-string should not split: got %q", got)
	}
}

func TestExtractJSONObject_NoObject(t *testing.T) {
	raw := "no braces at all"
	if got := extractJSONObject(raw); got != "" {
		t.Errorf("no JSON → empty; got %q", got)
	}
}

func TestNormalizeRuntimeModel_LowercaseEnums(t *testing.T) {
	m := &state.RuntimeModel{
		EntryPoints: []state.RuntimeEntryPoint{
			{Kind: "HTTP", ValidationAt: "Boundary"},
			{Kind: "Queue", ValidationAt: "HANDLER"},
		},
	}
	normalizeRuntimeModel(m)
	if m.EntryPoints[0].Kind != "http" || m.EntryPoints[0].ValidationAt != "boundary" {
		t.Errorf("expected lowercased, got %+v", m.EntryPoints[0])
	}
	if m.EntryPoints[1].Kind != "queue" || m.EntryPoints[1].ValidationAt != "handler" {
		t.Errorf("expected lowercased, got %+v", m.EntryPoints[1])
	}
}

func TestNormalizeRuntimeModel_DropsKindlessEntries(t *testing.T) {
	m := &state.RuntimeModel{
		EntryPoints: []state.RuntimeEntryPoint{
			{Kind: "", RetryModel: "stranded"},
			{Kind: "http"},
		},
	}
	normalizeRuntimeModel(m)
	if len(m.EntryPoints) != 1 {
		t.Errorf("kindless entries should drop, got %d entries", len(m.EntryPoints))
	}
	if m.EntryPoints[0].Kind != "http" {
		t.Errorf("wrong entry kept, got %q", m.EntryPoints[0].Kind)
	}
}

func TestNormalizeRuntimeModel_TrimsListEntries(t *testing.T) {
	m := &state.RuntimeModel{
		ValidationSites: []string{"  boundary  ", "", "handler"},
		Invariants:      []string{"  IDs are uuid v4  "},
	}
	normalizeRuntimeModel(m)
	if len(m.ValidationSites) != 2 || m.ValidationSites[0] != "boundary" {
		t.Errorf("expected trimmed list of 2; got %v", m.ValidationSites)
	}
	if m.Invariants[0] != "IDs are uuid v4" {
		t.Errorf("expected trimmed invariant; got %q", m.Invariants[0])
	}
}

func TestHashRuntimeModelInputs_StableAcrossCalls(t *testing.T) {
	inputs := &discoveredInputs{
		docs:    map[string]string{"README.md": "hello"},
		dirTree: "src/\n  main.go",
	}
	h1 := hashRuntimeModelInputs(inputs, "summary")
	h2 := hashRuntimeModelInputs(inputs, "summary")
	if h1 != h2 {
		t.Errorf("hash must be stable; got %s vs %s", h1, h2)
	}
}

func TestHashRuntimeModelInputs_SummaryChangesHash(t *testing.T) {
	inputs := &discoveredInputs{
		docs:    map[string]string{"README.md": "hello"},
		dirTree: "src/\n  main.go",
	}
	if hashRuntimeModelInputs(inputs, "A") == hashRuntimeModelInputs(inputs, "B") {
		t.Errorf("different project summaries must yield different hashes")
	}
}

func TestHashRuntimeModelInputs_InputsChangeHash(t *testing.T) {
	a := &discoveredInputs{dirTree: "src/"}
	b := &discoveredInputs{dirTree: "src/\n  main.go"}
	if hashRuntimeModelInputs(a, "x") == hashRuntimeModelInputs(b, "x") {
		t.Errorf("different inputs must yield different hashes")
	}
}

// ── Stub-client tests ───────────────────────────────────────────────────

// runtimeModelStubClient implements ai.Client by returning a fixed
// response string.
type runtimeModelStubClient struct {
	response string
	err      error
	// captured by ChatStream so tests can assert on what was sent.
	systemPrompt string
	userContent  string
}

func (c *runtimeModelStubClient) ChatStream(_ context.Context, sys string, msgs []ai.Message, _ func(string)) (string, error) {
	c.systemPrompt = sys
	if len(msgs) > 0 {
		c.userContent = msgs[0].Content
	}
	return c.response, c.err
}

func TestSummarizeRuntimeModel_ParsesStructuredJSON(t *testing.T) {
	client := &runtimeModelStubClient{
		response: `{
  "auth_model": "API Gateway authorizer + in-handler guardAdmin",
  "validation_sites": ["all HTTP handlers parse body through schema"],
  "entry_points": [
    {"kind": "HTTP", "retry_model": "no retries", "batch_model": "single", "validation_at": "boundary"}
  ],
  "result_discipline": "Result types with safeTry",
  "invariants": ["IDs are uuid v4"]
}`,
	}
	inputs := &discoveredInputs{
		manifests: map[string]string{"package.json": "{}"},
		dirTree:   "src/\n  api/",
	}

	model, err := summarizeRuntimeModel(context.Background(), client, inputs, "project briefing prose")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if model.AuthModel != "API Gateway authorizer + in-handler guardAdmin" {
		t.Errorf("AuthModel = %q", model.AuthModel)
	}
	if len(model.EntryPoints) != 1 || model.EntryPoints[0].Kind != "http" {
		t.Errorf("entry point not normalized: %+v", model.EntryPoints)
	}
	if model.EntryPoints[0].ValidationAt != "boundary" {
		t.Errorf("ValidationAt = %q", model.EntryPoints[0].ValidationAt)
	}
	if len(model.Invariants) != 1 || model.Invariants[0] != "IDs are uuid v4" {
		t.Errorf("Invariants = %+v", model.Invariants)
	}

	// The system prompt should be the embedded runtime model prompt.
	if !strings.Contains(client.systemPrompt, "structured runtime shape") {
		t.Errorf("system prompt missing expected content; got %q", firstN(client.systemPrompt, 100))
	}
	// User content should include the briefing and dir tree.
	if !strings.Contains(client.userContent, "project briefing prose") {
		t.Errorf("user message missing project briefing")
	}
	if !strings.Contains(client.userContent, "Directory Structure") {
		t.Errorf("user message missing directory structure")
	}
}

func TestSummarizeRuntimeModel_WrappedFenced(t *testing.T) {
	client := &runtimeModelStubClient{
		response: "Here's the model:\n```json\n{\"auth_model\": \"none\"}\n```\nDone.",
	}
	model, err := summarizeRuntimeModel(context.Background(), client, &discoveredInputs{}, "")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if model.AuthModel != "none" {
		t.Errorf("AuthModel = %q", model.AuthModel)
	}
}

func TestSummarizeRuntimeModel_LLMError(t *testing.T) {
	client := &runtimeModelStubClient{err: errors.New("model not found")}
	_, err := summarizeRuntimeModel(context.Background(), client, &discoveredInputs{}, "")
	if err == nil {
		t.Fatal("expected LLM error to surface, got nil")
	}
	if !strings.Contains(err.Error(), "LLM call") {
		t.Errorf("error should wrap with 'LLM call', got %v", err)
	}
}

func TestSummarizeRuntimeModel_GarbageResponse(t *testing.T) {
	client := &runtimeModelStubClient{response: "I cannot determine the runtime model from these inputs."}
	_, err := summarizeRuntimeModel(context.Background(), client, &discoveredInputs{}, "")
	if err == nil {
		t.Fatal("expected parse error on prose-only response")
	}
}

func TestDiscoverRuntimeModel_CacheHit(t *testing.T) {
	client := &runtimeModelStubClient{response: "should not be called"}
	// Pre-compute the hash that will match the next invocation.
	dir := t.TempDir()
	inputs, err := gatherInputs(dir)
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	wantHash := hashRuntimeModelInputs(inputs, "summary")

	res, err := DiscoverRuntimeModel(context.Background(), client, dir, "summary", wantHash, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !res.FromCache {
		t.Error("expected cache hit on matching hash")
	}
	if res.Model != nil {
		t.Error("expected nil model on cache hit (caller already holds it)")
	}
	if client.userContent != "" {
		t.Error("expected no LLM call on cache hit")
	}
}

func TestDiscoverRuntimeModel_NilClient(t *testing.T) {
	_, err := DiscoverRuntimeModel(context.Background(), nil, t.TempDir(), "", "", nil)
	if err == nil {
		t.Fatal("expected error on nil client")
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

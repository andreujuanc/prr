package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
)

func TestSortedKeys_Empty(t *testing.T) {
	got := sortedKeys(map[string]string{})
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestSortedKeys_Sorted(t *testing.T) {
	m := map[string]string{"c": "3", "a": "1", "b": "2"}
	got := sortedKeys(m)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("expected [a b c], got %v", got)
	}
}

func TestHashInputs_Deterministic(t *testing.T) {
	inputs := &discoveredInputs{
		docs:      map[string]string{"README.md": "hello"},
		manifests: map[string]string{"go.mod": "module foo"},
		dirTree:   "src/\n  main.go",
	}
	h1 := hashInputs(inputs)
	h2 := hashInputs(inputs)
	if h1 != h2 {
		t.Error("same inputs should produce same hash")
	}
}

func TestHashInputs_DifferentInputs(t *testing.T) {
	i1 := &discoveredInputs{
		docs:      map[string]string{"README.md": "hello"},
		manifests: map[string]string{},
	}
	i2 := &discoveredInputs{
		docs:      map[string]string{"README.md": "world"},
		manifests: map[string]string{},
	}
	if hashInputs(i1) == hashInputs(i2) {
		t.Error("different inputs should produce different hashes")
	}
}

func TestSynthesizeFromDocs_Empty(t *testing.T) {
	// synthesizeFromDocs now returns body only; the "## Project Context"
	// header is added by assembleContext in Discover. Verify an empty
	// input produces an empty body (no sections to emit).
	inputs := &discoveredInputs{
		docs:      map[string]string{},
		manifests: map[string]string{},
	}
	got := synthesizeFromDocs(inputs)
	if strings.Contains(got, "### Documentation") {
		t.Error("should not have documentation section when empty")
	}
}

// TestAssembleContext_AddsHeaderOnce verifies the assemble wrapper
// adds the `## Project Context` header exactly once and includes both
// summary and conventions when present.
func TestAssembleContext_AddsHeaderOnce(t *testing.T) {
	out := assembleContext("### Purpose\nthe project does X", "### Conventions\n- uses Y")
	if strings.Count(out, "## Project Context") != 1 {
		t.Errorf("expected header exactly once; got:\n%s", out)
	}
	if !strings.Contains(out, "### Purpose") {
		t.Errorf("missing summary section; got:\n%s", out)
	}
	if !strings.Contains(out, "### Conventions") {
		t.Errorf("missing conventions section; got:\n%s", out)
	}
}

func TestAssembleContext_OnlyConventions(t *testing.T) {
	// If summarization fell through (no LLM, no docs) but the
	// conventions extraction ran, the output should still be valid.
	out := assembleContext("", "### Conventions\n- uses Y")
	if !strings.Contains(out, "## Project Context") {
		t.Errorf("expected header; got:\n%s", out)
	}
	if !strings.Contains(out, "### Conventions") {
		t.Errorf("missing conventions; got:\n%s", out)
	}
}

func TestSynthesizeFromDocs_WithDocs(t *testing.T) {
	inputs := &discoveredInputs{
		docs:      map[string]string{"README.md": "This is a test project."},
		manifests: map[string]string{},
	}
	got := synthesizeFromDocs(inputs)
	if !strings.Contains(got, "### Documentation") {
		t.Error("expected documentation section")
	}
	if !strings.Contains(got, "#### README.md") {
		t.Error("expected README header")
	}
	if !strings.Contains(got, "This is a test project.") {
		t.Error("expected doc content")
	}
}

func TestSynthesizeFromDocs_WithManifests(t *testing.T) {
	inputs := &discoveredInputs{
		docs:      map[string]string{},
		manifests: map[string]string{"go.mod": "module example"},
	}
	got := synthesizeFromDocs(inputs)
	if !strings.Contains(got, "### Tech Stack") {
		t.Error("expected tech stack section")
	}
	if !strings.Contains(got, "module example") {
		t.Error("expected manifest content")
	}
}

func TestSynthesizeFromDocs_WithDirTree(t *testing.T) {
	inputs := &discoveredInputs{
		docs:      map[string]string{},
		manifests: map[string]string{},
		dirTree:   "cmd/\n  main.go",
	}
	got := synthesizeFromDocs(inputs)
	if !strings.Contains(got, "### Repository Structure") {
		t.Error("expected repository structure section")
	}
	if !strings.Contains(got, "cmd/") {
		t.Error("expected dir tree content")
	}
}

func TestSynthesizeFromDocs_SortedKeys(t *testing.T) {
	inputs := &discoveredInputs{
		docs:      map[string]string{"Z.md": "z", "A.md": "a"},
		manifests: map[string]string{},
	}
	got := synthesizeFromDocs(inputs)
	aIdx := strings.Index(got, "#### A.md")
	zIdx := strings.Index(got, "#### Z.md")
	if aIdx > zIdx {
		t.Error("expected docs sorted alphabetically")
	}
}

// ── Discover loud-fail contract ────────────────────────────────────
//
// When an LLM client is present and the LLM call fails, Discover
// must return an error — NOT silently fall back to a raw doc dump
// pretending to be a summary. Previously the fallback produced a
// "context" that was actually 600+ lines of unfiltered README/etc.,
// which then got prepended to every later prompt.

func TestDiscover_LLMErrorBubblesUp(t *testing.T) {
	// Need a temp repo root with enough doc content to trigger the
	// "summarize with LLM" branch (totalDocSize >= 200).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"),
		[]byte(strings.Repeat("# This project does things.\n", 20)), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	failingClient := failingAIClient{err: errLLMFailed}

	_, err := Discover(ctx, dir, failingClient, "", nil)
	if err == nil {
		t.Fatal("expected Discover to return error when LLM call fails; got nil (silent fallback would mask the underlying problem)")
	}
	if !strings.Contains(err.Error(), "project context summarization failed") {
		t.Errorf("error should mention summarization failure for diagnostic clarity; got: %v", err)
	}
}

func TestDiscover_NilClientStillSynthesizes(t *testing.T) {
	// client == nil means "user explicitly disabled LLM" (e.g. offline
	// mode, tests). That's a different intent from "LLM failed" and
	// should still fall back to raw synthesis without error.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"),
		[]byte(strings.Repeat("# This project does things.\n", 20)), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := Discover(ctx, dir, nil, "", nil)
	if err != nil {
		t.Fatalf("nil client should not produce error (no LLM was attempted); got: %v", err)
	}
	if result.Summary == "" {
		t.Errorf("nil client should fall back to raw synthesis; got empty summary")
	}
}

// failingAIClient implements ai.Client and always returns a fixed error
// from ChatStream — used to simulate "model not found" / "key expired"
// scenarios.
type failingAIClient struct {
	err error
}

func (f failingAIClient) ChatStream(_ context.Context, _ string, _ []ai.Message, _ func(string)) (string, error) {
	return "", f.err
}

var errLLMFailed = errors.New("models/gemini-3.1-flash is not found")

// ── AI-config conventions extraction ──────────────────────────────────
//
// The extraction passes AI-assistant config files through an LLM
// instructed to keep project FACTS and drop behavioral INSTRUCTIONS.
// We can't unit-test the LLM's filtering directly (that's an integration
// concern), but we can pin:
//   1. The function asks the LLM (no extraction when client is nil-equivalent).
//   2. The prompt construction includes both an examples-of-facts and an
//      examples-of-instructions block, plus the literal AGENTS.md content.
//   3. LLM errors surface (loud-fail).
//   4. Empty config input short-circuits to empty output (no LLM call).

// promptCapturingClient records the systemPrompt and userMessage passed
// to ChatStream so tests can inspect what the LLM was actually asked.
type promptCapturingClient struct {
	systemPrompt string
	userMessage  string
	response     string
	err          error
}

func (c *promptCapturingClient) ChatStream(_ context.Context, systemPrompt string, msgs []ai.Message, _ func(string)) (string, error) {
	c.systemPrompt = systemPrompt
	if len(msgs) > 0 {
		c.userMessage = msgs[0].Content
	}
	return c.response, c.err
}

func TestExtractConventionsFromAIConfigs_EmptyShortCircuits(t *testing.T) {
	client := &promptCapturingClient{response: "should not be called"}
	got, err := extractConventionsFromAIConfigs(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("empty configs should not error; got %v", err)
	}
	if got != "" {
		t.Errorf("empty configs should return empty string; got %q", got)
	}
	if client.systemPrompt != "" {
		t.Errorf("empty configs should not invoke ChatStream; systemPrompt=%q", client.systemPrompt)
	}
}

func TestExtractConventionsFromAIConfigs_PromptHasFactsAndInstructionsExamples(t *testing.T) {
	client := &promptCapturingClient{response: "### Conventions\n- Errors use fmt.Errorf"}
	configs := map[string]string{
		"AGENTS.md": "## Rule 1 — Think Before Coding\nState assumptions explicitly.",
	}
	_, err := extractConventionsFromAIConfigs(context.Background(), client, configs)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	// The prompt must EXPLICITLY contrast facts vs. instructions so the
	// LLM has the dividing line. Without this, behavioral rules from
	// AGENTS.md would leak into the conventions output and conflict
	// with the reviewer's own instructions.
	if !strings.Contains(client.userMessage, "PROJECT FACTS") {
		t.Errorf("prompt missing PROJECT FACTS guidance; user msg excerpt: %s", client.userMessage[:min(len(client.userMessage), 400)])
	}
	if !strings.Contains(client.userMessage, "BEHAVIORAL INSTRUCTIONS") {
		t.Errorf("prompt missing BEHAVIORAL INSTRUCTIONS guidance; user msg excerpt: %s", client.userMessage[:min(len(client.userMessage), 400)])
	}
	// The AI-config content must reach the model verbatim (it's the
	// input being filtered).
	if !strings.Contains(client.userMessage, "Think Before Coding") {
		t.Errorf("AGENTS.md content not passed to LLM; user msg missing rule body")
	}
}

func TestExtractConventionsFromAIConfigs_LLMErrorSurfaces(t *testing.T) {
	client := &promptCapturingClient{err: errLLMFailed}
	configs := map[string]string{"AGENTS.md": "Some content here, plenty of it"}
	_, err := extractConventionsFromAIConfigs(context.Background(), client, configs)
	if err == nil {
		t.Fatal("expected LLM error to surface; got nil")
	}
	if !strings.Contains(err.Error(), "conventions extraction") {
		t.Errorf("error should mention conventions extraction for diagnostic clarity; got: %v", err)
	}
}

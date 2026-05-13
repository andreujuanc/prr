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
	inputs := &discoveredInputs{
		docs:      map[string]string{},
		manifests: map[string]string{},
	}
	got := synthesizeFromDocs(inputs)
	if !strings.Contains(got, "## Project Context") {
		t.Error("expected project context header")
	}
	if strings.Contains(got, "### Documentation") {
		t.Error("should not have documentation section when empty")
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

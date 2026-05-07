package project

import (
	"strings"
	"testing"
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

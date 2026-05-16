package audit

import (
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

func TestHashContent(t *testing.T) {
	h1 := hashContent("hello world")
	h2 := hashContent("hello world")
	h3 := hashContent("different content")

	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
	if len(h1) != 32 {
		t.Errorf("expected 32-char hash, got %d", len(h1))
	}
}

func TestHashContent_Empty(t *testing.T) {
	h := hashContent("")
	if len(h) != 32 {
		t.Errorf("expected 32-char hash for empty string, got %d", len(h))
	}
}

func TestEnsureFileState(t *testing.T) {
	s := state.NewState("test-pr")

	ensureFileState(s, "src/main.go", "abc123")

	fs, ok := s.Files["src/main.go"]
	if !ok {
		t.Fatal("expected file state to be created")
	}
	if fs.DiffHash != "abc123" {
		t.Errorf("expected DiffHash abc123, got %q", fs.DiffHash)
	}
}

func TestEnsureFileState_UpdatesExisting(t *testing.T) {
	s := state.NewState("test-pr")

	ensureFileState(s, "src/main.go", "hash1")
	ensureFileState(s, "src/main.go", "hash2")

	fs := s.Files["src/main.go"]
	if fs.DiffHash != "hash2" {
		t.Errorf("expected DiffHash hash2, got %q", fs.DiffHash)
	}
}

func TestPhaseUsage_Total(t *testing.T) {
	usage := PhaseUsage{
		AOI:     ai.TokenUsage{InputTokens: 100, OutputTokens: 50, CacheHits: 10},
		Review:  ai.TokenUsage{InputTokens: 200, OutputTokens: 100, CacheHits: 20},
		Recheck: ai.TokenUsage{InputTokens: 30, OutputTokens: 15, CacheHits: 5},
		Synth:   ai.TokenUsage{InputTokens: 70, OutputTokens: 35, CacheHits: 0},
	}

	total := usage.Total()
	if total.InputTokens != 400 {
		t.Errorf("expected InputTokens 400, got %d", total.InputTokens)
	}
	if total.OutputTokens != 200 {
		t.Errorf("expected OutputTokens 200, got %d", total.OutputTokens)
	}
	if total.CacheHits != 35 {
		t.Errorf("expected CacheHits 35, got %d", total.CacheHits)
	}
}

func TestPhaseUsage_Total_Zero(t *testing.T) {
	usage := PhaseUsage{}
	total := usage.Total()
	if total.InputTokens != 0 || total.OutputTokens != 0 || total.CacheHits != 0 {
		t.Error("expected all zeros for empty usage")
	}
}

// ── computeRecheckCacheKey ──────────────────────────────────────────────
//
// The cache key is load-bearing: if it collides across runs that produced
// different recheck results, stale results get served as fresh ones. The
// contract these tests enforce:
//
//  1. Same (findings, projectContext, mode, prompt) → same key.
//  2. The key is independent of finding order.
//  3. Each of {findings, projectContext, mode, prompt} is part of the key —
//     changing any one of them changes the key.

func recheckTestFinding(id, file, lines, severity string) state.DeepFinding {
	return state.DeepFinding{
		FindingID: id,
		File:      file,
		Lines:     lines,
		Severity:  severity,
		Title:     "test finding " + id,
	}
}

func TestComputeRecheckCacheKey_DeterministicForSameInputs(t *testing.T) {
	findings := []state.DeepFinding{
		recheckTestFinding("F-001", "a.go", "10-20", "high"),
		recheckTestFinding("F-002", "b.go", "30-40", "medium"),
	}
	k1 := computeRecheckCacheKey(findings, "ctx", "audit", "")
	k2 := computeRecheckCacheKey(findings, "ctx", "audit", "")
	if k1 != k2 {
		t.Errorf("same inputs must produce the same key, got %q vs %q", k1, k2)
	}
	if len(k1) != 32 {
		t.Errorf("expected 32-char key, got %d (%q)", len(k1), k1)
	}
}

func TestComputeRecheckCacheKey_OrderIndependent(t *testing.T) {
	a := recheckTestFinding("F-001", "a.go", "10-20", "high")
	b := recheckTestFinding("F-002", "b.go", "30-40", "medium")
	c := recheckTestFinding("F-003", "c.go", "50-60", "low")

	k1 := computeRecheckCacheKey([]state.DeepFinding{a, b, c}, "ctx", "audit", "")
	k2 := computeRecheckCacheKey([]state.DeepFinding{c, a, b}, "ctx", "audit", "")
	if k1 != k2 {
		t.Errorf("permuting findings must not change the key, got %q vs %q", k1, k2)
	}
}

func TestComputeRecheckCacheKey_DiffersByFindings(t *testing.T) {
	base := []state.DeepFinding{recheckTestFinding("F-001", "a.go", "10-20", "high")}
	mutated := []state.DeepFinding{recheckTestFinding("F-001", "a.go", "10-20", "low")} // severity diff

	k1 := computeRecheckCacheKey(base, "ctx", "audit", "")
	k2 := computeRecheckCacheKey(mutated, "ctx", "audit", "")
	if k1 == k2 {
		t.Errorf("changing a finding field must change the key, both %q", k1)
	}
}

func TestComputeRecheckCacheKey_DiffersByProjectContext(t *testing.T) {
	findings := []state.DeepFinding{recheckTestFinding("F-001", "a.go", "10-20", "high")}

	k1 := computeRecheckCacheKey(findings, "context one", "audit", "")
	k2 := computeRecheckCacheKey(findings, "context two", "audit", "")
	if k1 == k2 {
		t.Errorf("different projectContext must change the key, both %q", k1)
	}
}

func TestComputeRecheckCacheKey_DiffersByMode(t *testing.T) {
	findings := []state.DeepFinding{recheckTestFinding("F-001", "a.go", "10-20", "high")}

	k1 := computeRecheckCacheKey(findings, "ctx", "audit", "")
	k2 := computeRecheckCacheKey(findings, "ctx", "pr", "")
	if k1 == k2 {
		t.Errorf("different mode must change the key, both %q", k1)
	}
}

// TestComputeRecheckCacheKey_DiffersByConsolidatePrompt verifies that
// iterating on the consolidator prompt invalidates the cache. After
// PR 5 the recheck pipeline runs two prompts; both must contribute
// to the key.
func TestComputeRecheckCacheKey_DiffersByConsolidatePrompt(t *testing.T) {
	findings := []state.DeepFinding{recheckTestFinding("F-001", "a.go", "10-20", "high")}

	original := ai.RecheckConsolidatePrompt
	t.Cleanup(func() { ai.RecheckConsolidatePrompt = original })

	ai.RecheckConsolidatePrompt = "CONSOLIDATE VERSION A"
	keyA := computeRecheckCacheKey(findings, "ctx", "audit", "")

	ai.RecheckConsolidatePrompt = "CONSOLIDATE VERSION B"
	keyB := computeRecheckCacheKey(findings, "ctx", "audit", "")

	if keyA == keyB {
		t.Errorf("changing RecheckConsolidatePrompt must change the key, both %q", keyA)
	}

	ai.RecheckConsolidatePrompt = "CONSOLIDATE VERSION A"
	keyARestored := computeRecheckCacheKey(findings, "ctx", "audit", "")
	if keyA != keyARestored {
		t.Errorf("same consolidate prompt text must produce same key, got %q vs %q", keyA, keyARestored)
	}
}

// TestComputeRecheckCacheKey_DiffersByPriorsHash pins the bug-priors
// hash into the cache key so a fix-commit landing between runs doesn't
// silently serve recheck reasoning from before the prior set grew.
func TestComputeRecheckCacheKey_DiffersByPriorsHash(t *testing.T) {
	findings := []state.DeepFinding{recheckTestFinding("F-001", "a.go", "10-20", "high")}

	keyA := computeRecheckCacheKey(findings, "ctx", "audit", "")
	keyB := computeRecheckCacheKey(findings, "ctx", "audit", "abc123")

	if keyA == keyB {
		t.Errorf("different priorsHash must change the key, both %q", keyA)
	}
}

// TestComputeRecheckCacheKey_DiffersByDismissPrompt is the analogous
// pin for the second prompt — both must independently change the key.
func TestComputeRecheckCacheKey_DiffersByDismissPrompt(t *testing.T) {
	findings := []state.DeepFinding{recheckTestFinding("F-001", "a.go", "10-20", "high")}

	original := ai.RecheckDismissPrompt
	t.Cleanup(func() { ai.RecheckDismissPrompt = original })

	ai.RecheckDismissPrompt = "DISMISS VERSION A"
	keyA := computeRecheckCacheKey(findings, "ctx", "audit", "")

	ai.RecheckDismissPrompt = "DISMISS VERSION B"
	keyB := computeRecheckCacheKey(findings, "ctx", "audit", "")

	if keyA == keyB {
		t.Errorf("changing RecheckDismissPrompt must change the key, both %q", keyA)
	}
}

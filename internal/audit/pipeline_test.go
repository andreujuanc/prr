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

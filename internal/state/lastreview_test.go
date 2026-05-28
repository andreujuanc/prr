package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHasReviewArtifact_LastReviewMarker(t *testing.T) {
	s := NewState("1")
	if s.HasReviewArtifact() {
		t.Fatal("fresh state should have no artifact")
	}

	s.SetLastReview(&ReviewMeta{Verdict: "approve", Summary: "clean"})

	if !s.HasReviewArtifact() {
		t.Fatal("LastReview marker alone should make HasReviewArtifact true — " +
			"this is the load-bearing fix for clean-PR display")
	}
}

func TestSetLastReview_AutoStampsCompletedAt(t *testing.T) {
	s := NewState("1")
	before := time.Now()
	s.SetLastReview(&ReviewMeta{Verdict: "comment"})
	after := time.Now()

	if s.LastReview == nil {
		t.Fatal("LastReview should be set")
	}
	if s.LastReview.CompletedAt.Before(before) || s.LastReview.CompletedAt.After(after) {
		t.Fatalf("CompletedAt %v should be auto-stamped between %v and %v",
			s.LastReview.CompletedAt, before, after)
	}
}

func TestSetLastReview_PreservesExplicitCompletedAt(t *testing.T) {
	s := NewState("1")
	custom := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	s.SetLastReview(&ReviewMeta{Verdict: "approve", CompletedAt: custom})
	if !s.LastReview.CompletedAt.Equal(custom) {
		t.Fatalf("explicit CompletedAt = %v should not be overwritten, got %v",
			custom, s.LastReview.CompletedAt)
	}
}

func TestSetLastReview_NilClears(t *testing.T) {
	s := NewState("1")
	s.SetLastReview(&ReviewMeta{Verdict: "approve"})
	s.SetLastReview(nil)
	if s.LastReview != nil {
		t.Fatal("passing nil should clear the marker")
	}
}

func TestClearAllCaches_ClearsLastReview(t *testing.T) {
	s := NewState("1")
	s.SetLastReview(&ReviewMeta{Verdict: "approve"})
	s.ClearAllCaches()
	if s.LastReview != nil {
		t.Fatal("ClearAllCaches must drop the LastReview marker so a " +
			"forced re-review starts from a clean slate")
	}
}

// TestLoad_CorruptStateReturnsTypedError ensures the TUI can detect
// state corruption via errors.As and offer a recovery path, rather
// than getting a generic %w-wrapped error it can't introspect.
func TestLoad_CorruptStateReturnsTypedError(t *testing.T) {
	// Redirect repoRootFn to a temp dir for the duration of the test.
	dir := t.TempDir()
	prev := repoRootFn
	repoRootFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { repoRootFn = prev })

	if err := os.MkdirAll(filepath.Join(dir, ".git", "pr-tui"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	corruptPath := filepath.Join(dir, ".git", "pr-tui", "42.json")
	if err := os.WriteFile(corruptPath, []byte("{not valid json"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Load("42")
	if err == nil {
		t.Fatal("expected error on corrupt JSON")
	}
	var ce *CorruptStateError
	if !errors.As(err, &ce) {
		t.Fatalf("error must be a *CorruptStateError so the TUI can route "+
			"to its delete-and-restart flow; got %T (%v)", err, err)
	}
	if ce.Path != corruptPath {
		t.Errorf("Path = %q, want %q", ce.Path, corruptPath)
	}
}

func TestDeleteStateFile_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	prev := repoRootFn
	repoRootFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { repoRootFn = prev })

	if err := os.MkdirAll(filepath.Join(dir, ".git", "pr-tui"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, ".git", "pr-tui", "42.json")
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := DeleteStateFile("42"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("state file should be gone")
	}
}

func TestDeleteStateFile_NoSuchFile_IsNoOp(t *testing.T) {
	dir := t.TempDir()
	prev := repoRootFn
	repoRootFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { repoRootFn = prev })

	if err := DeleteStateFile("does-not-exist"); err != nil {
		t.Fatalf("delete on missing file should be no-op, got %v", err)
	}
}

// TestLastReview_RoundTrip ensures the new field survives JSON
// marshal/unmarshal — guards against future field additions
// accidentally adding tag conflicts.
func TestLastReview_RoundTrip(t *testing.T) {
	s := NewState("1")
	s.SetLastReview(&ReviewMeta{
		Verdict:        "comment",
		Summary:        "Found 2",
		FindingsCount:  2,
		DismissedCount: 5,
		MinSeverity:    "high",
	})

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.LastReview == nil {
		t.Fatal("LastReview should round-trip")
	}
	if got.LastReview.FindingsCount != 2 || got.LastReview.DismissedCount != 5 {
		t.Fatalf("counters did not round-trip: %+v", got.LastReview)
	}
	if got.LastReview.MinSeverity != "high" {
		t.Fatalf("MinSeverity did not round-trip: got %q, want %q",
			got.LastReview.MinSeverity, "high")
	}
}

package state

import (
	"os"
	"reflect"
	"testing"
)

func TestStateSaveAndLoad(t *testing.T) {
	// Use a dummy PR number to avoid conflicts (must be numeric to pass validation)
	prNumber := "9999"

	// Ensure cleanup
	defer func() {
		filePath, _ := getStateFilePath(prNumber)
		os.Remove(filePath)
	}()

	state := NewState(prNumber)
	state.GlobalChat = []Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi"},
	}
	state.Files["file1.go"] = &FileState{
		Status:   StatusReviewed,
		DiffHash: "hash123",
		Chat: []Message{
			{Role: "user", Content: "Is this correct?"},
		},
	}

	err := Save(state)
	if err != nil {
		t.Fatalf("Failed to save state: %v", err)
	}

	loadedState, err := Load(prNumber)
	if err != nil {
		t.Fatalf("Failed to load state: %v", err)
	}

	if loadedState.PRNumber != state.PRNumber {
		t.Errorf("Expected PR number %s, got %s", state.PRNumber, loadedState.PRNumber)
	}

	if !reflect.DeepEqual(loadedState.GlobalChat, state.GlobalChat) {
		t.Errorf("GlobalChat mismatch. Expected %v, got %v", state.GlobalChat, loadedState.GlobalChat)
	}

	file1State, ok := loadedState.Files["file1.go"]
	if !ok {
		t.Fatalf("file1.go missing from loaded state")
	}

	if file1State.Status != StatusReviewed {
		t.Errorf("Expected status reviewed, got %s", file1State.Status)
	}

	if file1State.DiffHash != "hash123" {
		t.Errorf("Expected diff hash hash123, got %s", file1State.DiffHash)
	}

	if !reflect.DeepEqual(file1State.Chat, state.Files["file1.go"].Chat) {
		t.Errorf("File Chat mismatch")
	}
}

// TestStateSaveAndLoad_RecheckDismissalsSurviveRoundTrip pins that
// the dismissal log is persisted as part of state.Save and read back
// by state.Load. The audit pipeline's cache-hit path reads this back
// via auditState.GetRecheckDismissals() to restore the report's
// dismissal trail without re-running recheck. If JSON encoding ever
// drops the field (e.g. someone changes the tag or struct), the
// cache-hit dismissal restore breaks silently — this test prevents
// that.
func TestStateSaveAndLoad_RecheckDismissalsSurviveRoundTrip(t *testing.T) {
	prNumber := "9998"
	t.Cleanup(func() {
		p, _ := getStateFilePath(prNumber)
		os.Remove(p)
	})

	s := NewState(prNumber)
	s.SetRecheckDismissals([]DismissedRecord{
		{
			FindingID: "F-007",
			Finding: DeepFinding{
				FindingID: "F-007",
				File:      "internal/x.go",
				Lines:     "42",
				Severity:  "medium",
				Title:     "Whatever",
			},
			Rationale: "covered by upstream guard",
		},
	})

	if err := Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(prNumber)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := loaded.GetRecheckDismissals()
	if len(got) != 1 {
		t.Fatalf("expected 1 dismissal round-tripped, got %d", len(got))
	}
	if got[0].FindingID != "F-007" {
		t.Errorf("FindingID lost in round trip: %q", got[0].FindingID)
	}
	if got[0].Rationale != "covered by upstream guard" {
		t.Errorf("Rationale lost in round trip: %q", got[0].Rationale)
	}
	if got[0].Finding.File != "internal/x.go" {
		t.Errorf("Finding.File lost in round trip: %q", got[0].Finding.File)
	}
}

func TestSyncWithDiffs(t *testing.T) {
	state := NewState("test_123")
	state.GlobalChat = []Message{{Role: "user", Content: "Global"}}
	state.Files["file1.go"] = &FileState{
		Status:   StatusReviewed,
		DiffHash: "hash1",
		Chat:     []Message{{Role: "user", Content: "File1"}},
	}
	state.Files["file2.go"] = &FileState{
		Status:   StatusReviewed,
		DiffHash: "hash2",
		Chat:     []Message{{Role: "user", Content: "File2"}},
	}

	// Scenario: file1.go diff changes, file2.go remains same, file3.go is new, file2.go removed later
	currentHashes := map[string]string{
		"file1.go": "hash1_modified", // changed
		"file2.go": "hash2",          // unchanged
		"file3.go": "hash3",          // new
	}

	state.SyncWithDiffs(currentHashes, map[string]bool{"file1.go": true, "file2.go": true, "file3.go": true})

	// Check file1.go (invalidated — was reviewed, now modified)
	if state.Files["file1.go"].Status != StatusModified {
		t.Errorf("file1.go should be modified, got %s", state.Files["file1.go"].Status)
	}
	if len(state.Files["file1.go"].Chat) != 0 {
		t.Errorf("file1.go chat should be cleared")
	}
	if state.Files["file1.go"].DiffHash != "hash1_modified" {
		t.Errorf("file1.go hash should be updated")
	}

	// Check file2.go (untouched)
	if state.Files["file2.go"].Status != StatusReviewed {
		t.Errorf("file2.go should still be reviewed")
	}
	if len(state.Files["file2.go"].Chat) != 1 {
		t.Errorf("file2.go chat should be intact")
	}

	// Check file3.go (new)
	if state.Files["file3.go"].Status != StatusUnreviewed {
		t.Errorf("file3.go should be unreviewed")
	}
	if state.Files["file3.go"].DiffHash != "hash3" {
		t.Errorf("file3.go hash should be set")
	}

	// Check GlobalChat (should be cleared because something changed)
	if len(state.GlobalChat) != 0 {
		t.Errorf("GlobalChat should be cleared")
	}

	// Scenario 2: file2.go is removed
	state.GlobalChat = []Message{{Role: "user", Content: "Global again"}}
	currentHashes2 := map[string]string{
		"file1.go": "hash1_modified",
		"file3.go": "hash3",
	}

	state.SyncWithDiffs(currentHashes2, map[string]bool{"file1.go": true, "file3.go": true})

	if _, exists := state.Files["file2.go"]; exists {
		t.Errorf("file2.go should be removed")
	}
	if len(state.GlobalChat) != 0 {
		t.Errorf("GlobalChat should be cleared after removal")
	}
}

func TestLoadInvalidPRNumber(t *testing.T) {
	invalidKeys := []string{"", "12/34", "../etc", "1 2", "a b", "foo;bar", "a\nb"}
	for _, key := range invalidKeys {
		_, err := Load(key)
		if err == nil {
			t.Errorf("Load(%q) should return error for invalid state key", key)
		}
	}
}

func TestLoadValidStateKeys(t *testing.T) {
	// These should all be valid state keys (not just PR numbers)
	validKeys := []string{"42", "audit", "abc", "my-audit", "test_123"}
	for _, key := range validKeys {
		_, err := Load(key)
		if err != nil {
			t.Errorf("Load(%q) should succeed for valid state key, got: %v", key, err)
		}
	}
}

func TestSaveInvalidPRNumber(t *testing.T) {
	s := NewState("foo/bar")
	err := Save(s)
	if err == nil {
		t.Error("Save with invalid state key should return error")
	}
}

func TestSyncWithDiffsNoChanges(t *testing.T) {
	state := NewState("test_123")
	state.GlobalChat = []Message{{Role: "user", Content: "Global"}}
	state.Files["file1.go"] = &FileState{
		Status:   StatusReviewed,
		DiffHash: "hash1",
		Chat:     []Message{{Role: "user", Content: "File1"}},
	}

	currentHashes := map[string]string{
		"file1.go": "hash1",
	}

	state.SyncWithDiffs(currentHashes, map[string]bool{"file1.go": true})

	if len(state.GlobalChat) != 1 {
		t.Errorf("GlobalChat should NOT be cleared if nothing changed")
	}
	if state.Files["file1.go"].Status != StatusReviewed {
		t.Errorf("file1.go should still be reviewed")
	}
}

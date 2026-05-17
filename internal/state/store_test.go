package state

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fakeRepoRoot replaces the git.RepoRoot lookup for the duration of a
// test. Returns a cleanup func; defer-call to restore.
func fakeRepoRoot(t *testing.T, root string) {
	t.Helper()
	prev := repoRootFn
	repoRootFn = func() (string, error) { return root, nil }
	t.Cleanup(func() { repoRootFn = prev })
}

// fakeRepoRootError simulates being outside a git repo. State path
// resolution should fall back to cwd-relative behavior.
func fakeRepoRootError(t *testing.T) {
	t.Helper()
	prev := repoRootFn
	repoRootFn = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { repoRootFn = prev })
}

// chdir runs the rest of the test from a different cwd. Restores on
// cleanup so other tests aren't disturbed.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// TestStateDir_ResolvesUnderRepoRoot pins the load-bearing contract:
// regardless of where prr was launched, state files live under the
// repo root's .git/pr-tui/, not under the current working directory.
// Without this, the same PR's state can't be found across cwds and
// shows up as "no cached review."
func TestStateDir_ResolvesUnderRepoRoot(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	got := stateDir()
	want := filepath.Join(root, ".git/pr-tui")
	if got != want {
		t.Errorf("stateDir() = %q, want %q", got, want)
	}
}

// TestStateDir_FallsBackToCwdRelativeWhenNotInRepo pins the soft
// fallback: outside a git repo, we stay at cwd-relative behavior
// rather than erroring. This keeps audit-snapshot helpers usable in
// niche test contexts.
func TestStateDir_FallsBackToCwdRelativeWhenNotInRepo(t *testing.T) {
	fakeRepoRootError(t)
	if got := stateDir(); got != stateDirRel {
		t.Errorf("stateDir() = %q, want fallback %q", got, stateDirRel)
	}
}

// TestSaveLoad_RoundTripsAcrossDifferentCwds is the integration test
// that proves the original bug is fixed: write state from one cwd,
// load it from a different cwd, get the same data back. The pre-fix
// behavior failed this test silently — Load would return an empty
// State because it looked at the new cwd's .git/pr-tui.
func TestSaveLoad_RoundTripsAcrossDifferentCwds(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	// Save from one cwd.
	cwdA := t.TempDir()
	chdir(t, cwdA)
	original := NewState("42")
	original.SetProjectContext("my project", "hash-1")
	if err := Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load from a different cwd.
	cwdB := t.TempDir()
	chdir(t, cwdB)
	loaded, err := Load("42")
	if err != nil {
		t.Fatalf("Load from different cwd: %v", err)
	}
	if ctx, _ := loaded.GetProjectContext(); ctx != "my project" {
		t.Errorf("project context = %q, want 'my project' — state did not round-trip across cwds", ctx)
	}
}

// TestMigration_MovesLegacyStateFileToRepoRoot pins the user-facing
// recovery behavior: a user upgrading prr after this fix finds their
// previously-cached reviews still accessible — they're auto-migrated
// from the old cwd-relative path. Without this, every user with
// existing state would silently lose their reviews.
func TestMigration_MovesLegacyStateFileToRepoRoot(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	// Plant a legacy state file at the cwd-relative path.
	legacyCwd := t.TempDir()
	chdir(t, legacyCwd)
	legacyDir := filepath.Join(legacyCwd, ".git/pr-tui")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	legacyContents := []byte(`{"pr_number":"7","review":{"summary":"legacy review"}}`)
	if err := os.WriteFile(filepath.Join(legacyDir, "7.json"), legacyContents, 0644); err != nil {
		t.Fatal(err)
	}

	// Load — should migrate the legacy file into the repo-root path
	// and return its contents.
	loaded, err := Load("7")
	if err != nil {
		t.Fatalf("Load with legacy migration: %v", err)
	}
	if loaded.Review == nil || loaded.Review.Summary != "legacy review" {
		t.Errorf("expected migrated review summary, got: %+v", loaded.Review)
	}

	// Verify the file now lives at the new path...
	newPath := filepath.Join(root, ".git/pr-tui/7.json")
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("state not migrated to %q: %v", newPath, err)
	}
	// ...and the legacy path is gone (rename, not copy).
	if _, err := os.Stat(filepath.Join(legacyDir, "7.json")); !os.IsNotExist(err) {
		t.Errorf("legacy file still present after migration: %v", err)
	}
}

// TestMigration_NoOpWhenNewPathAlreadyExists ensures migration doesn't
// clobber the canonical state with a stale legacy file. If both exist,
// the canonical one wins — the legacy is left alone (out of caution).
func TestMigration_NoOpWhenNewPathAlreadyExists(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	// Plant the canonical (new) state file first.
	newDir := filepath.Join(root, ".git/pr-tui")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}
	canonical := []byte(`{"pr_number":"9","review":{"summary":"canonical"}}`)
	if err := os.WriteFile(filepath.Join(newDir, "9.json"), canonical, 0644); err != nil {
		t.Fatal(err)
	}

	// Plant a stale legacy file at the cwd-relative path.
	legacyCwd := t.TempDir()
	chdir(t, legacyCwd)
	legacyDir := filepath.Join(legacyCwd, ".git/pr-tui")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "9.json")
	if err := os.WriteFile(legacyPath, []byte(`{"pr_number":"9","review":{"summary":"stale"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load("9")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Review.Summary != "canonical" {
		t.Errorf("got summary %q, want 'canonical' (migration should not overwrite)", loaded.Review.Summary)
	}
	// Legacy still present — we don't delete it.
	if _, err := os.Stat(legacyPath); err != nil {
		t.Errorf("legacy file removed despite no-op migration: %v", err)
	}
}

// TestLoad_MissingFileReturnsEmptyState is the existing contract; pin
// it explicitly so the migration probe doesn't accidentally change it.
func TestLoad_MissingFileReturnsEmptyState(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)
	chdir(t, t.TempDir())

	loaded, err := Load("999")
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if loaded.PRNumber != "999" {
		t.Errorf("PRNumber = %q, want 999", loaded.PRNumber)
	}
	if loaded.Review != nil {
		t.Errorf("Review should be nil for missing-file Load, got: %+v", loaded.Review)
	}
}

// TestSave_FallbackCopyOnRenameError ensures Save still persists state when
// os.Rename (used for atomic replacement) fails. We inject a failing
// fileRename implementation to simulate cross-device rename errors and
// assert the state still round-trips via the copy fallback.
func TestSave_FallbackCopyOnRenameError(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	// Inject a fileRename that always fails to force the fallback path.
	prevRename := fileRename
	fileRename = func(oldpath, newpath string) error {
		return fmt.Errorf("simulated rename failure")
	}
	t.Cleanup(func() { fileRename = prevRename })

	s := NewState("123")
	s.SetProjectContext("ctx", "h1")
	if err := Save(s); err != nil {
		t.Fatalf("Save with rename failure: %v", err)
	}

	// Load back and ensure content matches.
	loaded, err := Load("123")
	if err != nil {
		t.Fatalf("Load after Save fallback: %v", err)
	}
	if ctx, _ := loaded.GetProjectContext(); ctx != "ctx" {
		t.Fatalf("expected project context 'ctx', got %q", ctx)
	}
}

// TestMigration_FallbackCopyOnRenameError simulates a failing rename during
// legacy migration and asserts that the copy+remove fallback still moves
// the legacy file into the canonical location.
func TestMigration_FallbackCopyOnRenameError(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	// Plant a legacy state file at the cwd-relative path.
	legacyCwd := t.TempDir()
	chdir(t, legacyCwd)
	legacyDir := filepath.Join(legacyCwd, ".git/pr-tui")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	legacyContents := []byte(`{"pr_number":"7","review":{"summary":"legacy review"}}`)
	legacyPath := filepath.Join(legacyDir, "7.json")
	if err := os.WriteFile(legacyPath, legacyContents, 0644); err != nil {
		t.Fatal(err)
	}

	// Force fileRename to fail so migrateOldCwdRelativeState does copy+remove.
	prevRename := fileRename
	fileRename = func(oldpath, newpath string) error { return fmt.Errorf("simulated rename error") }
	t.Cleanup(func() { fileRename = prevRename })

	// Load — should migrate
	loaded, err := Load("7")
	if err != nil {
		t.Fatalf("Load with fallback migration: %v", err)
	}
	if loaded.Review == nil || loaded.Review.Summary != "legacy review" {
		t.Fatalf("expected migrated review summary, got: %+v", loaded.Review)
	}
	newPath := filepath.Join(root, ".git/pr-tui/7.json")
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("state not migrated to %q: %v", newPath, err)
	}
	// legacy should be gone (we removed it after copying)
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy file still present after fallback migration: %v", err)
	}
}

// TestSave_ConcurrentSavesAreSerialised pins the saveMu contract:
// many goroutines hitting Save on a shared state must not race at the
// kernel level (two renames to the same path) and must not lose data
// from interleaved marshals. Run with -race to catch data races on
// the in-memory state; the final reload verifies the persisted JSON
// is parseable (no half-written content).
func TestSave_ConcurrentSavesAreSerialised(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	s := NewState("99")
	const n = 16
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			// Each goroutine touches a different cache key so the
			// state mutates between Saves. Without saveMu, two
			// goroutines could marshal interleaved states and the
			// older marshal could overwrite the newer.
			s.SetBatchFindings(fmt.Sprintf("file-%d.go", i), "purpose", "raw")
			errCh <- Save(s)
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("Save goroutine %d: %v", i, err)
		}
	}
	// Final reload should produce a valid parsed state, not a
	// half-written file.
	if _, err := Load("99"); err != nil {
		t.Fatalf("Load after concurrent Save: %v", err)
	}
}

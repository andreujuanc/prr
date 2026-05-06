package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupTestRepo creates a temp git repo with an "origin" remote pointing to
// a local bare repo, a base branch with an initial commit, and a head branch
// with a modification. Returns a cleanup function that restores the original
// working directory.
//
// Layout after setup:
//   - origin/main has file.go with "package main\nfunc old() {}\n"
//   - origin/feature has file.go with "package main\nfunc new() {}\n"
func setupTestRepo(t *testing.T) (cleanup func()) {
	t.Helper()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Create bare "remote" repo
	bareDir := t.TempDir()
	run(t, bareDir, "git", "init", "--bare")

	// Create working repo
	workDir := t.TempDir()
	run(t, workDir, "git", "init")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	run(t, workDir, "git", "remote", "add", "origin", bareDir)

	// Create initial commit on main
	filePath := filepath.Join(workDir, "file.go")
	os.WriteFile(filePath, []byte("package main\nfunc old() {}\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial")
	run(t, workDir, "git", "branch", "-M", "main")
	run(t, workDir, "git", "push", "origin", "main")

	// Create feature branch with a change
	run(t, workDir, "git", "checkout", "-b", "feature")
	os.WriteFile(filePath, []byte("package main\nfunc new() {}\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "change func")
	run(t, workDir, "git", "push", "origin", "feature")

	// chdir into workDir so git commands find the repo
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}

	return func() {
		os.Chdir(origDir)
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

// ── GetLocalRefHash ─────────────────────────────────────────────────────

func TestGetLocalRefHash(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	hash := GetLocalRefHash("origin/main")
	if hash == "" {
		t.Fatal("expected non-empty hash for origin/main")
	}
	if len(hash) != 40 {
		t.Errorf("hash length = %d, want 40", len(hash))
	}

	// Non-existent ref should return empty
	hash2 := GetLocalRefHash("origin/nonexistent")
	if hash2 != "" {
		t.Errorf("expected empty hash for nonexistent ref, got %q", hash2)
	}
}

// ── GetRawDiff ──────────────────────────────────────────────────────────

func TestGetRawDiff(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	diff, err := GetRawDiff("main", "feature", "file.go")
	if err != nil {
		t.Fatalf("GetRawDiff: %v", err)
	}
	if diff == "" {
		t.Fatal("expected non-empty diff")
	}
	if !strings.Contains(diff, "-func old()") {
		t.Error("diff should contain removed old function")
	}
	if !strings.Contains(diff, "+func new()") {
		t.Error("diff should contain added new function")
	}
}

func TestGetRawDiffWithContext(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	diff0, err := GetRawDiffWithContext("main", "feature", "file.go", 0)
	if err != nil {
		t.Fatalf("GetRawDiffWithContext(0): %v", err)
	}
	if diff0 == "" {
		t.Fatal("expected non-empty diff with context=0")
	}

	diff10, err := GetRawDiffWithContext("main", "feature", "file.go", 10)
	if err != nil {
		t.Fatalf("GetRawDiffWithContext(10): %v", err)
	}
	// More context should produce equal or larger output
	if len(diff10) < len(diff0) {
		t.Error("diff with more context should not be shorter")
	}
}

func TestGetRawDiff_NonexistentFile(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	diff, err := GetRawDiff("main", "feature", "nonexistent.go")
	if err != nil {
		t.Fatalf("GetRawDiff for nonexistent file should not error, got: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff for nonexistent file, got %q", diff)
	}
}

// ── BlameFile ───────────────────────────────────────────────────────────

func TestBlameFile(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()
	blame, err := BlameFile(ctx, "origin/feature", "file.go")
	if err != nil {
		t.Fatalf("BlameFile: %v", err)
	}

	// file.go on feature has 2 lines
	if len(blame) != 2 {
		t.Fatalf("expected 2 blame lines, got %d", len(blame))
	}

	// Line 1: "package main" — from initial commit
	if blame[1].Author != "Test" {
		t.Errorf("line 1 author = %q, want %q", blame[1].Author, "Test")
	}
	if blame[1].Date == "" {
		t.Error("line 1 date should not be empty")
	}

	// Line 2: "func new() {}" — from feature commit
	if blame[2].Author != "Test" {
		t.Errorf("line 2 author = %q, want %q", blame[2].Author, "Test")
	}
}

func TestBlameFile_NonexistentFile(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	ctx := context.Background()
	_, err := BlameFile(ctx, "origin/feature", "nonexistent.go")
	if err == nil {
		t.Error("BlameFile on nonexistent file should return error")
	}
}

// ── GetStyledDiff ───────────────────────────────────────────────────────

func TestGetStyledDiff(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	theme := DefaultDiffTheme()
	out, err := GetStyledDiff("main", "feature", "file.go", theme)
	if err != nil {
		t.Fatalf("GetStyledDiff: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty styled diff output")
	}
	// Should contain the changed function names somewhere in the ANSI output
	if !strings.Contains(out, "old") {
		t.Error("output should contain removed content 'old'")
	}
	if !strings.Contains(out, "new") {
		t.Error("output should contain added content 'new'")
	}
}

func TestGetStyledDiffWithContext(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	theme := DefaultDiffTheme()

	out3, err := GetStyledDiffWithContext("main", "feature", "file.go", 3, theme)
	if err != nil {
		t.Fatalf("GetStyledDiffWithContext(3): %v", err)
	}
	if out3 == "" {
		t.Fatal("expected non-empty output")
	}

	out10, err := GetStyledDiffWithContext("main", "feature", "file.go", 10, theme)
	if err != nil {
		t.Fatalf("GetStyledDiffWithContext(10): %v", err)
	}
	if len(out10) < len(out3) {
		t.Error("more context should produce equal or larger output")
	}
}

func TestGetStyledDiff_NoChanges(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	theme := DefaultDiffTheme()
	out, err := GetStyledDiff("main", "main", "file.go", theme)
	if err != nil {
		t.Fatalf("GetStyledDiff same branch: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty output for same branch, got %d bytes", len(out))
	}
}

func TestGetStyledDiff_NonexistentFile(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	theme := DefaultDiffTheme()
	out, err := GetStyledDiff("main", "feature", "nonexistent.go", theme)
	if err != nil {
		t.Fatalf("GetStyledDiff nonexistent file should not error: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty output for nonexistent file")
	}
}

// ── FetchRefs ───────────────────────────────────────────────────────────

func TestFetchRefs(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	// Should succeed — both refs exist on origin
	err := FetchRefs("main", "feature", "")
	if err != nil {
		t.Fatalf("FetchRefs: %v", err)
	}
}

func TestFetchRefs_SkipWhenUpToDate(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	// Get the actual head SHA
	headSHA := GetLocalRefHash("origin/feature")
	if headSHA == "" {
		t.Fatal("expected non-empty head SHA")
	}

	// Should skip fetch when headRefOid matches
	err := FetchRefs("main", "feature", headSHA)
	if err != nil {
		t.Fatalf("FetchRefs (skip): %v", err)
	}
}

func TestFetchRefs_NonexistentBranch(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	err := FetchRefs("main", "nonexistent-branch-xyz", "")
	if err == nil {
		t.Error("FetchRefs with nonexistent branch should return error")
	}
}

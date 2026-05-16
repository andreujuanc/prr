package bugpriors

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestFilterFixShaped(t *testing.T) {
	subjects := []string{
		"fix: nil deref in handler",
		"feat: add new widget",
		"audit: close cache-key gaps",
		"refactor: tidy imports",
		"bug(scope): wrong key on lookup",
		"chore: bump deps",
		"review: tighten Systemic gate",
		"revert: previous release",
		"docs: README typo",
		"perf: speed up sort by 10%", // no fix prefix, no keyword
		"feat(api): handle timeout properly",
	}
	got := filterFixShaped(subjects)
	want := []string{
		"fix: nil deref in handler",
		"audit: close cache-key gaps",
		"bug(scope): wrong key on lookup",
		"review: tighten Systemic gate",
		"revert: previous release",
		"feat(api): handle timeout properly",
	}
	if !slices.Equal(got, want) {
		t.Errorf("filterFixShaped mismatch\n got:  %#v\n want: %#v", got, want)
	}
}

func TestDedupe(t *testing.T) {
	subjects := []string{
		"fix: nil deref in handler",
		"fix: nil deref in handler",     // exact dup
		"FIX:  nil   deref  in handler", // whitespace + case variant
		"audit: nil deref in handler",   // prefix-only differs — should dedup
		"fix: cache-key gap",            // distinct
	}
	got := dedupe(subjects)
	want := []string{
		"fix: nil deref in handler",
		"fix: cache-key gap",
	}
	if !slices.Equal(got, want) {
		t.Errorf("dedupe mismatch\n got:  %#v\n want: %#v", got, want)
	}
}

func TestRenderContainsBulletsAndGuidance(t *testing.T) {
	out := render([]string{
		"fix: nil deref",
		"audit: cache-key gap",
	})
	if !strings.Contains(out, "## Known failure modes in this codebase") {
		t.Errorf("missing section header in render output")
	}
	if !strings.Contains(out, "- fix: nil deref") {
		t.Errorf("missing first bullet in render output")
	}
	if !strings.Contains(out, "- audit: cache-key gap") {
		t.Errorf("missing second bullet in render output")
	}
	if !strings.Contains(out, "identifier generation") {
		t.Errorf("missing guidance paragraph in render output")
	}
}

func TestExtractNonGitDirReturnsEmpty(t *testing.T) {
	// Point at a path that exists but isn't a git repo. exec will
	// succeed at starting git but git itself will fail. Extract must
	// swallow the error and return empty.
	tmp := t.TempDir()
	got, err := Extract(tmp, 30)
	if err != nil {
		t.Fatalf("expected nil error on non-git dir, got %v", err)
	}
	if got != "" {
		t.Errorf("expected empty output on non-git dir, got %q", got)
	}
}

func TestExtractZeroLookback(t *testing.T) {
	got, err := Extract(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty for zero lookback, got %q", got)
	}
}

func TestExtractFromRealRepo(t *testing.T) {
	// Seed a tiny git repo with known fix-shaped + non-fix commits
	// and assert the extractor picks up the fix-shaped ones.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		// Inherit the OS env (PATH, SystemRoot on Windows, etc.) but
		// override the git author/committer identity and disable
		// global config so the test doesn't pick up the developer's
		// gpg-sign or commit hooks.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("commit", "--allow-empty", "-m", "feat: initial scaffolding")
	run("commit", "--allow-empty", "-m", "fix: nil deref in handler")
	run("commit", "--allow-empty", "-m", "chore: bump deps")
	run("commit", "--allow-empty", "-m", "audit: close cache-key gaps")
	run("commit", "--allow-empty", "-m", "docs: rewrite README")
	run("commit", "--allow-empty", "-m", "fix: race in scheduler")

	got, err := Extract(repo, 30)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got == "" {
		t.Fatalf("expected non-empty output, got empty")
	}
	for _, want := range []string{
		"fix: nil deref in handler",
		"audit: close cache-key gaps",
		"fix: race in scheduler",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected output to contain %q\nfull:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"feat: initial scaffolding",
		"chore: bump deps",
		"docs: rewrite README",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("expected output to NOT contain %q\nfull:\n%s", unwanted, got)
		}
	}
}

func TestExtractCapEnforced(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	// Seed maxRendered+5 distinct fix-shaped commits. With 25 inputs
	// surviving filter + dedupe and a 20-bullet cap, the output must
	// contain exactly maxRendered bullets — not fewer, not more.
	const seeded = maxRendered + 5
	for i := 0; i < seeded; i++ {
		run("commit", "--allow-empty", "-m", fmt.Sprintf("fix: distinct issue %02d", i))
	}
	got, err := Extract(repo, seeded+5) // lookback wider than seeded
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got == "" {
		t.Fatalf("expected non-empty output")
	}
	bulletCount := strings.Count(got, "\n- ")
	if bulletCount != maxRendered {
		t.Errorf("bullet count: want exactly %d (the cap), got %d", maxRendered, bulletCount)
	}
}

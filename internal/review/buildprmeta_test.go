package review

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/git"
)

// TestBuildPRMeta_StableShape pins the canonical PR metadata header.
// Both `prr review` and the TUI's `a` keystroke must produce this
// exact string — divergent metadata changes prompt content, which
// changes model output, which makes the two paths non-equivalent.
func TestBuildPRMeta_StableShape(t *testing.T) {
	pr := &git.PullRequest{
		Number:      42,
		Title:       "Fix the thing",
		Body:        "Lorem ipsum.",
		BaseRefName: "main",
		HeadRefName: "feature/fix",
	}

	want := "PR #42: Fix the thing\n" +
		"Description:\nLorem ipsum.\n" +
		"Base: main → Head: feature/fix\n\n"

	if got := BuildPRMeta(pr); got != want {
		t.Fatalf("BuildPRMeta diverged.\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildPRMeta_OmitsEmptyBody(t *testing.T) {
	pr := &git.PullRequest{
		Number:      1,
		Title:       "t",
		BaseRefName: "main",
		HeadRefName: "h",
	}
	out := BuildPRMeta(pr)
	if strings.Contains(out, "Description:") {
		t.Fatalf("empty body must omit Description block, got:\n%q", out)
	}
}

func TestBuildPRMeta_NilPR_ReturnsEmpty(t *testing.T) {
	if got := BuildPRMeta(nil); got != "" {
		t.Fatalf("nil PR must return empty string, got %q", got)
	}
}

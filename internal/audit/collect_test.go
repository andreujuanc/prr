package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// partitionFiles is the pure inner function behind CollectFiles. Tests
// here pin filter precedence and stat accounting without spinning up
// a git repo — see CollectFiles for the integration shape.

func TestPartitionFiles_BasicAccounting(t *testing.T) {
	tracked := []string{
		"cmd/prr/main.go",
		"internal/audit/pipeline.go",
		"go.sum",            // ExcludedReview (lock file)
		"README.md",         // ExcludedAudit (docs)
		"dist/bundle.js",    // ExcludedAudit (build artifact)
		"vendor/lib/foo.go", // ExcludedReview (vendored)
	}
	untracked := []string{
		"prr-debug.log",   // included + transient
		"local-notes.txt", // ExcludedAudit (*.txt)
	}

	files, stats := partitionFiles(tracked, untracked, nil, nil)

	wantIncluded := []string{
		"cmd/prr/main.go",
		"internal/audit/pipeline.go",
		"prr-debug.log",
	}
	if !reflect.DeepEqual(files, wantIncluded) {
		t.Errorf("included files mismatch:\n  got:  %v\n  want: %v", files, wantIncluded)
	}

	if stats.TotalListed != 8 {
		t.Errorf("TotalListed = %d, want 8", stats.TotalListed)
	}
	if stats.Included != 3 {
		t.Errorf("Included = %d, want 3", stats.Included)
	}
	if stats.Tracked != 2 {
		t.Errorf("Tracked = %d, want 2", stats.Tracked)
	}
	if stats.Untracked != 1 {
		t.Errorf("Untracked = %d, want 1", stats.Untracked)
	}
	if stats.ExcludedReview != 2 { // go.sum + vendor/...
		t.Errorf("ExcludedReview = %d, want 2", stats.ExcludedReview)
	}
	if stats.ExcludedAudit != 3 { // README.md, dist/..., local-notes.txt
		t.Errorf("ExcludedAudit = %d, want 3", stats.ExcludedAudit)
	}
	if stats.ExcludedCustom != 0 {
		t.Errorf("ExcludedCustom = %d, want 0", stats.ExcludedCustom)
	}

	wantTransients := []string{"prr-debug.log"}
	if !reflect.DeepEqual(stats.UntrackedTransients, wantTransients) {
		t.Errorf("UntrackedTransients = %v, want %v", stats.UntrackedTransients, wantTransients)
	}
}

func TestPartitionFiles_CustomExcludePattern(t *testing.T) {
	// User-provided --exclude should count separately from built-in
	// exclusions so debug output can distinguish "your pattern dropped
	// this" from "we always drop this".
	tracked := []string{
		"internal/audit/pipeline.go",
		"internal/audit/experimental.go", // dropped by custom
	}
	files, stats := partitionFiles(tracked, nil, []string{"experimental.go"}, nil)

	if len(files) != 1 || files[0] != "internal/audit/pipeline.go" {
		t.Errorf("unexpected included files: %v", files)
	}
	if stats.ExcludedCustom != 1 {
		t.Errorf("ExcludedCustom = %d, want 1", stats.ExcludedCustom)
	}
	if stats.ExcludedAudit != 0 || stats.ExcludedReview != 0 {
		t.Errorf("custom-pattern drop must not double-count: review=%d audit=%d",
			stats.ExcludedReview, stats.ExcludedAudit)
	}
}

func TestPartitionFiles_ForceIncludeBypassesAllExclusions(t *testing.T) {
	// --include is the user's escape hatch. A file that would normally
	// be excluded must reach the audit AND be counted under
	// ForceIncluded when matched.
	//
	// Note: ShouldForceInclude uses filepath.Match (no `**` support),
	// so patterns must be glob-matchable against basename or exact path.
	tracked := []string{
		"README.md", // matches "*.md" → force-included, would otherwise be ExcludedAudit
		"normal.go",
	}
	files, stats := partitionFiles(tracked, nil, nil, []string{"*.md"})

	want := []string{"README.md", "normal.go"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("included files mismatch:\n  got:  %v\n  want: %v", files, want)
	}
	if stats.ForceIncluded != 1 {
		t.Errorf("ForceIncluded = %d, want 1", stats.ForceIncluded)
	}
	if stats.ExcludedAudit != 0 {
		t.Errorf("--include must bypass audit exclusions: ExcludedAudit=%d", stats.ExcludedAudit)
	}
}

func TestPartitionFiles_TransientsOnlyFlaggedWhenIncluded(t *testing.T) {
	// Transient detection runs on the INCLUDED set, not the listed set.
	// If an audit-exclude pattern already drops a *.log file, the user
	// has no need to be warned about gitignoring it — it isn't being
	// audited. Verify a transient-looking file that gets excluded does
	// NOT appear in UntrackedTransients.
	tracked := []string{}
	untracked := []string{
		"prr-debug.log",       // survives → flagged
		"foo-debug-trace.log", // survives → flagged
		"docs/old.log",        // matches no audit pattern, so survives; transient ⇒ flagged
	}
	// Custom pattern drops the third file — it must NOT appear in
	// transients despite matching the transient heuristic.
	files, stats := partitionFiles(tracked, untracked, []string{"old.log"}, nil)

	if len(files) != 2 {
		t.Fatalf("expected 2 included after custom exclude; got %d (%v)", len(files), files)
	}

	wantTransients := []string{"foo-debug-trace.log", "prr-debug.log"}
	if !reflect.DeepEqual(stats.UntrackedTransients, wantTransients) {
		t.Errorf("UntrackedTransients = %v, want %v (excluded files must not be flagged)",
			stats.UntrackedTransients, wantTransients)
	}
}

func TestPartitionFiles_EmptyInput(t *testing.T) {
	files, stats := partitionFiles(nil, nil, nil, nil)
	if len(files) != 0 {
		t.Errorf("expected empty result, got %v", files)
	}
	if stats.TotalListed != 0 || stats.Included != 0 {
		t.Errorf("empty input must produce zero stats; got %+v", stats)
	}
}

func TestPartitionFiles_FilterPrecedence(t *testing.T) {
	// A file matched by BOTH review and audit patterns should be counted
	// under whichever fires first (review). This pins precedence so
	// future re-ordering doesn't silently shift counters.
	//
	// go.sum is excluded by config.ShouldExcludeFromReview AND would
	// also match audit's exclusion patterns if extended. Verify it
	// lands in ExcludedReview, not ExcludedAudit.
	tracked := []string{"go.sum"}
	_, stats := partitionFiles(tracked, nil, nil, nil)
	if stats.ExcludedReview != 1 {
		t.Errorf("ExcludedReview = %d, want 1 (review pattern fires first)", stats.ExcludedReview)
	}
	if stats.ExcludedAudit != 0 {
		t.Errorf("ExcludedAudit = %d, want 0 (precedence guarantees review wins)", stats.ExcludedAudit)
	}
}

// ── Recency ordering / restriction (pure helpers) ────────────────────────

func TestReorderByRecency_BasicOrdering(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go", "d.go"}
	recent := []string{"c.go", "a.go"} // c was touched most recently, then a

	got := reorderByRecency(files, recent)
	want := []string{"c.go", "a.go", "b.go", "d.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderByRecency = %v, want %v", got, want)
	}
}

func TestReorderByRecency_EmptyRecent(t *testing.T) {
	files := []string{"a.go", "b.go"}
	got := reorderByRecency(files, nil)
	if !reflect.DeepEqual(got, files) {
		t.Errorf("empty recent should return files unchanged; got %v", got)
	}
}

func TestReorderByRecency_RecentNotInFiles(t *testing.T) {
	// Recent file isn't in the filtered set (excluded by review/audit
	// patterns). Should not be added to the result.
	files := []string{"a.go", "b.go"}
	recent := []string{"vendor/zzz.go", "a.go"}

	got := reorderByRecency(files, recent)
	want := []string{"a.go", "b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderByRecency = %v, want %v", got, want)
	}
}

func TestReorderByRecency_DuplicateRecent(t *testing.T) {
	// `git log --name-only` emits the same file once per commit
	// that touched it. The helper must dedup.
	files := []string{"a.go", "b.go", "c.go"}
	recent := []string{"a.go", "b.go", "a.go", "c.go"}

	got := reorderByRecency(files, recent)
	want := []string{"a.go", "b.go", "c.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dedup failed: got %v, want %v", got, want)
	}
}

func TestReorderByRecency_PreservesColdOrder(t *testing.T) {
	// Cold files keep their original input order.
	files := []string{"zeta.go", "alpha.go", "mu.go", "beta.go"}
	recent := []string{"mu.go"}

	got := reorderByRecency(files, recent)
	want := []string{"mu.go", "zeta.go", "alpha.go", "beta.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cold order not preserved: got %v, want %v", got, want)
	}
}

func TestRestrictToRecent_DropsColdFiles(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go", "d.go"}
	recent := []string{"c.go", "a.go"}

	got := restrictToRecent(files, recent)
	want := []string{"c.go", "a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("restrictToRecent = %v, want %v", got, want)
	}
}

func TestRestrictToRecent_EmptyRecent(t *testing.T) {
	files := []string{"a.go", "b.go"}
	got := restrictToRecent(files, nil)
	if got != nil {
		t.Errorf("empty recent should produce nil result, got %v", got)
	}
}

func TestRestrictToRecent_RecentFilesNotInFiltered(t *testing.T) {
	// Recent files all excluded by filtering — restriction is empty.
	files := []string{"src/handler.go"}
	recent := []string{"vendor/lib.go", "node_modules/x.js"}

	got := restrictToRecent(files, recent)
	if len(got) != 0 {
		t.Errorf("restrictToRecent = %v, want empty", got)
	}
}

// ── Recency integration ─────────────────────────────────────────────────
//
// gitRecentlyTouchedFiles shells out to `git log` so the simplest way
// to exercise it is to run against the actual repo. Skipped when
// `git log` is unavailable.

func TestGitRecentlyTouchedFiles_ReturnsRecentPaths(t *testing.T) {
	// "." resolves to the test's working directory, which is the
	// audit package root inside the prr repo. Walk up to the repo
	// root by looking for go.mod.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("repo root not found: %v", err)
	}

	paths, err := gitRecentlyTouchedFiles(repoRoot, 5)
	if err != nil {
		// Non-git environments (source tarball, container without git,
		// shallow checkout where `git log` rejects the request) should
		// skip rather than fail the suite.
		t.Skipf("gitRecentlyTouchedFiles: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("expected at least one path from recent commits, got 0")
	}

	// Dedup invariant.
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if _, dup := seen[p]; dup {
			t.Errorf("duplicate path in output: %q", p)
		}
		seen[p] = struct{}{}
	}
}

func findRepoRoot() (string, error) {
	// CWD is /workspace/internal/audit when go test runs. Walk up
	// until we find go.mod.
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found")
		}
		dir = parent
	}
}

package audit

import (
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

package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadAuditFile_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	body := []byte("package main\n\nfunc main() {}\n")
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}

	res := loadAuditFile(path, "main.go", MaxAuditFileBytes)
	if res.Outcome != loadedOK {
		t.Fatalf("Outcome = %v, want loadedOK", res.Outcome)
	}
	if res.File.Path != "main.go" {
		t.Errorf("File.Path = %q, want %q", res.File.Path, "main.go")
	}
	if res.File.Content != string(body) {
		t.Errorf("File.Content mismatch")
	}
	if res.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", res.Size, len(body))
	}
}

func TestLoadAuditFile_Symlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.go")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	res := loadAuditFile(link, "link.go", MaxAuditFileBytes)
	if res.Outcome != skippedSymlink {
		t.Errorf("Outcome = %v, want skippedSymlink — symlinks must not be followed silently",
			res.Outcome)
	}
}

func TestLoadAuditFile_TooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.json")
	// Write more than the cap. Cap is at maxBytes parameter, not the
	// global MaxAuditFileBytes — we pass a tiny cap to keep the test
	// fast.
	body := bytes.Repeat([]byte("x"), 1024)
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}

	res := loadAuditFile(path, "big.json", 512)
	if res.Outcome != skippedTooLarge {
		t.Fatalf("Outcome = %v, want skippedTooLarge", res.Outcome)
	}
	if res.Size != 1024 {
		t.Errorf("Size = %d, want 1024", res.Size)
	}
}

func TestLoadAuditFile_Binary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin.dat")
	// NUL byte within first 8KB triggers binary classification.
	body := []byte{'p', 'k', 0x00, 'e', 'l', 'f'}
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}

	res := loadAuditFile(path, "bin.dat", MaxAuditFileBytes)
	if res.Outcome != skippedBinary {
		t.Errorf("Outcome = %v, want skippedBinary", res.Outcome)
	}
}

func TestLoadAuditFile_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.go")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}

	res := loadAuditFile(path, "empty.go", MaxAuditFileBytes)
	if res.Outcome != skippedEmpty {
		t.Errorf("Outcome = %v, want skippedEmpty", res.Outcome)
	}
}

func TestLoadAuditFile_NotFound(t *testing.T) {
	// Path doesn't exist → ENOENT-equivalent → skippedNotFound. This is
	// the "race with `git rm` between ls-files and read" path; should
	// be benign, not an error.
	dir := t.TempDir()
	res := loadAuditFile(filepath.Join(dir, "ghost.go"), "ghost.go", MaxAuditFileBytes)
	if res.Outcome != skippedNotFound {
		t.Errorf("Outcome = %v, want skippedNotFound", res.Outcome)
	}
	if res.Err != nil {
		t.Errorf("Err should be nil for skippedNotFound (it's a benign race); got %v", res.Err)
	}
}

func TestLoadAuditFile_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission semantics differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root — chmod 0000 won't restrict reads")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "locked.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0644) })

	res := loadAuditFile(path, "locked.go", MaxAuditFileBytes)
	if res.Outcome != loadErrored {
		t.Fatalf("Outcome = %v, want loadErrored — permission denied must bubble up so aggregate-fail can decide",
			res.Outcome)
	}
	if res.Err == nil {
		t.Error("Err should be non-nil for loadErrored so the caller can log the cause")
	}
}

func TestShouldAggregateFail(t *testing.T) {
	tests := []struct {
		name      string
		errored   int
		attempted int
		want      bool
	}{
		// Below the absolute floor — never abort, regardless of ratio.
		// This protects tiny audits ("2 of 5 failed = 40%") from
		// overreacting to one or two flaky reads.
		{"below floor: 2/5 = 40% but under floor", 2, 5, false},
		{"below floor: 1/1 = 100% but under floor", 1, 1, false},

		// Above floor but under ratio — proceed with warnings.
		{"above floor but under ratio: 3/30 = 10%", 3, 30, false},
		{"exactly at ratio: 4/20 = 20%", 4, 20, false}, // strict >

		// Above floor AND above ratio — abort.
		{"above floor and ratio: 5/20 = 25%", 5, 20, true},
		{"catastrophic: 50/100 = 50%", 50, 100, true},

		// Edge: zero attempted should never panic or return true.
		{"zero attempted", 5, 0, false},
	}

	for _, tc := range tests {
		got := shouldAggregateFail(tc.errored, tc.attempted)
		if got != tc.want {
			t.Errorf("%s: shouldAggregateFail(%d, %d) = %v, want %v",
				tc.name, tc.errored, tc.attempted, got, tc.want)
		}
	}
}

// TestShouldAggregateFail_PinsThresholds documents the two constants
// the rest of the logic depends on. Bumping the ratio or floor
// without updating tests across the codebase will surface here.
func TestShouldAggregateFail_PinsThresholds(t *testing.T) {
	if aggregateFailRatio != 0.20 {
		t.Errorf("aggregateFailRatio = %g, want 0.20 (changing this changes user-visible Phase 1 behavior)",
			aggregateFailRatio)
	}
	if aggregateFailMinFailures != 3 {
		t.Errorf("aggregateFailMinFailures = %d, want 3", aggregateFailMinFailures)
	}
}

// ── parseAuditEvent contract for Phase 1 skip breakdown ─────────────────
//
// The pipeline emits skip stats as a single formatted line; the parser
// extracts them into Counters that fileCollectionSummary reads. A
// format drift between emitter and parser would silently zero the
// skip totals.

func TestParseAuditEvent_Phase1SkipBreakdown(t *testing.T) {
	// Match the exact format emitted by pipeline.go to pin the contract.
	// If you change either side, update both.
	const msg = "Phase 1 skip breakdown: 7 binary, 2 large, 5 empty, 1 symlink, 0 missing, 3 errored"

	// Re-import progress shape from the test by using parseAuditEvent
	// directly on a fresh state.
	st := newState()
	parseAuditEvent(st, "phase1", msg)

	cases := map[string]int{
		"file_skipped_binary":  7,
		"file_skipped_large":   2,
		"file_skipped_empty":   5,
		"file_skipped_symlink": 1,
		"file_skipped_missing": 0,
		"file_skipped_errored": 3,
	}
	for k, want := range cases {
		if got := st.Counters[k]; got != want {
			t.Errorf("Counters[%q] = %d, want %d (parser/emitter format drift)", k, got, want)
		}
	}
}

// TestFileCollectionSummary_WithSkips verifies the summary string
// includes the skipped count when any file was skipped — so the user
// sees "145 files · 12 skipped" instead of just "145 files".
func TestFileCollectionSummary_WithSkips(t *testing.T) {
	st := newState()
	st.Counters["aoi_total"] = 145
	st.Counters["file_skipped_binary"] = 7
	st.Counters["file_skipped_large"] = 2
	st.Counters["file_skipped_empty"] = 3

	got := fileCollectionSummary(st)
	if !strings.Contains(got, "145 files") {
		t.Errorf("summary missing file count: %q", got)
	}
	if !strings.Contains(got, "12 skipped") {
		t.Errorf("summary missing skip total (should be 7+2+3=12): %q", got)
	}
}

func TestFileCollectionSummary_NoSkips(t *testing.T) {
	st := newState()
	st.Counters["aoi_total"] = 145

	got := fileCollectionSummary(st)
	if got != "145 files" {
		t.Errorf("summary = %q, want %q (no skipped suffix when zero)", got, "145 files")
	}
}

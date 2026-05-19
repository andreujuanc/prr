package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// TestSaveReviewSnapshot_WritesUnderReviewsDir pins the path layout
// and the parseable-on-disk invariant: the file lands under
// .git/pr-tui/reviews/ with the expected name shape and is valid
// JSON when read back.
func TestSaveReviewSnapshot_WritesUnderReviewsDir(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	body := []byte(`{"verdict":"comment"}`)
	path, err := SaveReviewSnapshot("42", body)
	if err != nil {
		t.Fatalf("SaveReviewSnapshot: %v", err)
	}

	expectedDir := filepath.Join(root, ".git/pr-tui/reviews")
	if filepath.Dir(path) != expectedDir {
		t.Errorf("snapshot dir = %q, want %q", filepath.Dir(path), expectedDir)
	}
	name := filepath.Base(path)
	pattern := regexp.MustCompile(`^pr-42-review-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}Z\.json$`)
	if !pattern.MatchString(name) {
		t.Errorf("snapshot name %q does not match expected pattern", name)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back snapshot: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("content round-trip mismatch:\n got:  %q\nwant: %q", string(got), string(body))
	}
	// Parseable as JSON — defends against accidental wrapping.
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Errorf("snapshot is not valid JSON: %v", err)
	}
}

// TestSaveAuditSnapshot_WritesUnderAuditsDir mirrors the review
// test for the audit path.
func TestSaveAuditSnapshot_WritesUnderAuditsDir(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	body := []byte(`{"files_scanned":3}`)
	path, err := SaveAuditSnapshot(body)
	if err != nil {
		t.Fatalf("SaveAuditSnapshot: %v", err)
	}

	expectedDir := filepath.Join(root, ".git/pr-tui/audits")
	if filepath.Dir(path) != expectedDir {
		t.Errorf("snapshot dir = %q, want %q", filepath.Dir(path), expectedDir)
	}
	if !strings.HasPrefix(filepath.Base(path), "audit-") {
		t.Errorf("snapshot name %q should start with audit-", filepath.Base(path))
	}
	if !strings.HasSuffix(path, ".json") {
		t.Errorf("snapshot name %q should end with .json", path)
	}
}

// TestSaveReviewSnapshot_RejectsBadPRNumber pins the input validation
// — a PR number with path separators or shell metacharacters must
// be rejected, mirroring the validStateKey gate on Save().
func TestSaveReviewSnapshot_RejectsBadPRNumber(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	cases := []string{"../42", "42/sneaky", "42 with spaces", "42;rm -rf /"}
	for _, bad := range cases {
		if _, err := SaveReviewSnapshot(bad, []byte(`{}`)); err == nil {
			t.Errorf("SaveReviewSnapshot(%q) should have rejected; got nil error", bad)
		}
	}
}

// TestSaveSnapshots_Concurrent confirms snapshot writes serialise
// safely through saveMu — many goroutines writing to the same
// directory don't race on the temp+rename machinery. Run under
// -race to catch any data race on shared state.
func TestSaveSnapshots_Concurrent(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n*2)

	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := SaveReviewSnapshot("99", []byte(`{"k":"v"}`))
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := SaveAuditSnapshot([]byte(`{"k":"v"}`))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent snapshot save: %v", err)
		}
	}
}

// TestReviewsDir_CreatesOnDemand pins that ReviewsDir() creates the
// directory if missing — important because consumers (snapshot
// retention tools, the UI's review history view if we ever build
// one) may call it before any snapshot has been written.
func TestReviewsDir_CreatesOnDemand(t *testing.T) {
	root := t.TempDir()
	fakeRepoRoot(t, root)

	got, err := ReviewsDir()
	if err != nil {
		t.Fatalf("ReviewsDir: %v", err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("ReviewsDir returned %q but it doesn't exist: %v", got, err)
	}
}

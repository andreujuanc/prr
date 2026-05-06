package state

import (
	"encoding/json"
	"testing"
)

// ── SeverityRank ────────────────────────────────────────────────────────

func TestSeverityRank(t *testing.T) {
	tests := []struct {
		severity string
		want     int
	}{
		{"critical", 0},
		{"high", 1},
		{"medium", 2},
		{"low", 3},
		{"nit", 4},
		{"unknown", 5},
		{"", 5},
	}
	for _, tt := range tests {
		f := ReviewFinding{Severity: tt.severity}
		if got := f.SeverityRank(); got != tt.want {
			t.Errorf("SeverityRank(%q) = %d, want %d", tt.severity, got, tt.want)
		}
	}
}

func TestSeverityRank_Ordering(t *testing.T) {
	// Verify that the ranking maintains the correct total order:
	// critical < high < medium < low < nit < unknown
	severities := []string{"critical", "high", "medium", "low", "nit", "bogus"}
	for i := 0; i < len(severities)-1; i++ {
		a := ReviewFinding{Severity: severities[i]}
		b := ReviewFinding{Severity: severities[i+1]}
		if a.SeverityRank() >= b.SeverityRank() {
			t.Errorf("%q (rank %d) should be more severe than %q (rank %d)",
				severities[i], a.SeverityRank(), severities[i+1], b.SeverityRank())
		}
	}
}

// ── NewState ────────────────────────────────────────────────────────────

func TestNewState(t *testing.T) {
	s := NewState("123")
	if s.PRNumber != "123" {
		t.Errorf("PRNumber = %q, want %q", s.PRNumber, "123")
	}
	if s.Files == nil {
		t.Fatal("Files map should be initialized, not nil")
	}
	if len(s.Files) != 0 {
		t.Errorf("Files should be empty, got %d entries", len(s.Files))
	}
	if s.Review != nil {
		t.Error("Review should be nil on new state")
	}
}

// ── AOI Results ─────────────────────────────────────────────────────────

func TestSetGetAOIResults(t *testing.T) {
	s := NewState("1")
	data := json.RawMessage(`{"areas":["auth","crypto"]}`)

	// Set on a file that doesn't exist yet — should create FileState
	s.SetAOIResults("main.go", data, 5)

	got, ctx := s.GetAOIResults("main.go")
	if got == nil {
		t.Fatal("expected non-nil AOI results")
	}
	if string(got) != string(data) {
		t.Errorf("AOI data = %s, want %s", string(got), string(data))
	}
	if ctx != 5 {
		t.Errorf("context lines = %d, want 5", ctx)
	}
}

func TestGetAOIResults_NonexistentFile(t *testing.T) {
	s := NewState("1")
	got, ctx := s.GetAOIResults("nonexistent.go")
	if got != nil {
		t.Errorf("expected nil for nonexistent file, got %s", string(got))
	}
	if ctx != 0 {
		t.Errorf("expected 0 context lines, got %d", ctx)
	}
}

func TestSetAOIResults_OverwritesExisting(t *testing.T) {
	s := NewState("1")
	s.Files["f.go"] = &FileState{Status: StatusReviewed, DiffHash: "abc"}

	data := json.RawMessage(`{"new": true}`)
	s.SetAOIResults("f.go", data, 10)

	// Should overwrite AOI data but preserve other fields
	if s.Files["f.go"].Status != StatusReviewed {
		t.Error("SetAOIResults should not change Status")
	}
	if s.Files["f.go"].DiffHash != "abc" {
		t.Error("SetAOIResults should not change DiffHash")
	}
	got, ctx := s.GetAOIResults("f.go")
	if string(got) != `{"new": true}` {
		t.Errorf("AOI data not overwritten: %s", string(got))
	}
	if ctx != 10 {
		t.Errorf("context lines = %d, want 10", ctx)
	}
}

// ── Batch Findings ──────────────────────────────────────────────────────

func TestSetGetBatchFindings(t *testing.T) {
	s := NewState("1")

	// Set on new file
	s.SetBatchFindings("api.go", "handles HTTP routing", "found XSS in handler")

	purpose, findings := s.GetBatchFindings("api.go")
	if purpose != "handles HTTP routing" {
		t.Errorf("purpose = %q, want %q", purpose, "handles HTTP routing")
	}
	if findings != "found XSS in handler" {
		t.Errorf("findings = %q, want %q", findings, "found XSS in handler")
	}
}

func TestGetBatchFindings_NonexistentFile(t *testing.T) {
	s := NewState("1")
	purpose, findings := s.GetBatchFindings("nope.go")
	if purpose != "" || findings != "" {
		t.Errorf("expected empty strings for nonexistent file, got %q, %q", purpose, findings)
	}
}

func TestSetBatchFindings_CreatesFileState(t *testing.T) {
	s := NewState("1")
	s.SetBatchFindings("new.go", "purpose", "findings")

	if _, ok := s.Files["new.go"]; !ok {
		t.Fatal("SetBatchFindings should create FileState if not present")
	}
	if s.Files["new.go"].Status != StatusUnreviewed {
		t.Errorf("new file should default to unreviewed, got %s", s.Files["new.go"].Status)
	}
}

// ── ClearAllCaches ──────────────────────────────────────────────────────

func TestClearAllCaches(t *testing.T) {
	s := NewState("1")
	s.Files["a.go"] = &FileState{
		Status:          StatusReviewed,
		DiffHash:        "hash-a",
		Purpose:         "serves API",
		BatchFindings:   "some findings",
		AOIResults:      json.RawMessage(`{"x":1}`),
		AOIContextLines: 3,
	}
	s.Files["b.go"] = &FileState{
		Status:        StatusModified,
		DiffHash:      "hash-b",
		BatchFindings: "other findings",
	}
	s.Review = &AIReview{Summary: "looks good"}

	s.ClearAllCaches()

	// Review should be cleared
	if s.Review != nil {
		t.Error("Review should be nil after ClearAllCaches")
	}

	// Per-file cache fields cleared, but Status and DiffHash preserved
	for path, fs := range s.Files {
		if fs.BatchFindings != "" {
			t.Errorf("%s: BatchFindings should be empty", path)
		}
		if fs.Purpose != "" {
			t.Errorf("%s: Purpose should be empty", path)
		}
		if fs.AOIResults != nil {
			t.Errorf("%s: AOIResults should be nil", path)
		}
		if fs.AOIContextLines != 0 {
			t.Errorf("%s: AOIContextLines should be 0", path)
		}
	}

	// Status and DiffHash should NOT be cleared
	if s.Files["a.go"].Status != StatusReviewed {
		t.Error("ClearAllCaches should not change Status")
	}
	if s.Files["a.go"].DiffHash != "hash-a" {
		t.Error("ClearAllCaches should not change DiffHash")
	}
}

// ── HasCachedBatch ──────────────────────────────────────────────────────

func TestHasCachedBatch_AllCached(t *testing.T) {
	s := NewState("1")
	s.Files["a.go"] = &FileState{Purpose: "does thing A", BatchFindings: "ok"}
	s.Files["b.go"] = &FileState{Purpose: "does thing B", BatchFindings: "ok"}

	if !s.HasCachedBatch([]string{"a.go", "b.go"}) {
		t.Error("expected true when all files have cached Purpose")
	}
}

func TestHasCachedBatch_MissingFile(t *testing.T) {
	s := NewState("1")
	s.Files["a.go"] = &FileState{Purpose: "does thing A"}

	if s.HasCachedBatch([]string{"a.go", "b.go"}) {
		t.Error("expected false when a file is missing from state")
	}
}

func TestHasCachedBatch_EmptyPurpose(t *testing.T) {
	s := NewState("1")
	s.Files["a.go"] = &FileState{Purpose: "has purpose"}
	s.Files["b.go"] = &FileState{Purpose: ""} // no purpose = not cached

	if s.HasCachedBatch([]string{"a.go", "b.go"}) {
		t.Error("expected false when a file has empty Purpose")
	}
}

func TestHasCachedBatch_EmptyPaths(t *testing.T) {
	s := NewState("1")
	// Vacuous truth: all zero files in [] have cached findings
	if !s.HasCachedBatch([]string{}) {
		t.Error("expected true for empty paths (vacuous truth)")
	}
}

// ── CollectCachedFindings ───────────────────────────────────────────────

func TestCollectCachedFindings(t *testing.T) {
	s := NewState("1")
	s.Files["a.go"] = &FileState{Purpose: "auth logic", BatchFindings: "finding A"}
	s.Files["b.go"] = &FileState{Purpose: "db layer", BatchFindings: "finding B"}
	s.Files["c.go"] = &FileState{Purpose: "", BatchFindings: ""} // no findings

	combined, fileFindings := s.CollectCachedFindings([]string{"a.go", "b.go", "c.go"})

	// fileFindings should only contain files with actual findings
	if len(fileFindings) != 2 {
		t.Fatalf("expected 2 file findings, got %d", len(fileFindings))
	}
	if fileFindings["a.go"] != "finding A" {
		t.Errorf("a.go findings = %q, want %q", fileFindings["a.go"], "finding A")
	}
	if fileFindings["b.go"] != "finding B" {
		t.Errorf("b.go findings = %q, want %q", fileFindings["b.go"], "finding B")
	}
	if _, ok := fileFindings["c.go"]; ok {
		t.Error("c.go should not be in fileFindings (empty)")
	}

	// Combined string should contain structured markdown for each file
	if len(combined) == 0 {
		t.Fatal("combined string should not be empty")
	}
	if !contains(combined, "### a.go") {
		t.Error("combined should contain header for a.go")
	}
	if !contains(combined, "Purpose: auth logic") {
		t.Error("combined should contain purpose for a.go")
	}
	if !contains(combined, "finding A") {
		t.Error("combined should contain findings for a.go")
	}
}

func TestCollectCachedFindings_EmptyState(t *testing.T) {
	s := NewState("1")
	combined, fileFindings := s.CollectCachedFindings([]string{"x.go"})
	if combined != "" {
		t.Errorf("expected empty combined, got %q", combined)
	}
	if len(fileFindings) != 0 {
		t.Errorf("expected empty fileFindings, got %d", len(fileFindings))
	}
}

// ── HasFile ─────────────────────────────────────────────────────────────

func TestHasFile(t *testing.T) {
	s := NewState("1")
	s.Files["exists.go"] = &FileState{}

	if !s.HasFile("exists.go") {
		t.Error("expected true for existing file")
	}
	if s.HasFile("nope.go") {
		t.Error("expected false for nonexistent file")
	}
}

// ── ProjectContext ──────────────────────────────────────────────────────

func TestSetGetProjectContext(t *testing.T) {
	s := NewState("1")

	s.SetProjectContext("This is a Go CLI tool", "sha256abc")

	summary, hash := s.GetProjectContext()
	if summary != "This is a Go CLI tool" {
		t.Errorf("summary = %q, want %q", summary, "This is a Go CLI tool")
	}
	if hash != "sha256abc" {
		t.Errorf("hash = %q, want %q", hash, "sha256abc")
	}
}

func TestGetProjectContext_Empty(t *testing.T) {
	s := NewState("1")
	summary, hash := s.GetProjectContext()
	if summary != "" || hash != "" {
		t.Errorf("expected empty strings, got %q, %q", summary, hash)
	}
}

// helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

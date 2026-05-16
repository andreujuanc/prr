package state

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
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
	s.SetAOIResults("main.go", data, 5, "")

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
	s.SetAOIResults("f.go", data, 10, "")

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
	s.DeepFindings = []DeepFinding{{AOIID: "x", Severity: "high"}}
	s.RecheckDismissals = []DismissedRecord{{FindingID: "F-007", Rationale: "stale"}}

	s.ClearAllCaches()

	// Review should be cleared
	if s.Review != nil {
		t.Error("Review should be nil after ClearAllCaches")
	}

	// DeepFindings should be cleared — NoCache callers expect a clean
	// slate, and stale findings from a prior run would otherwise leak
	// through into reports.
	if s.DeepFindings != nil {
		t.Error("DeepFindings should be nil after ClearAllCaches")
	}

	// RecheckDismissals likewise — they're keyed to the prior
	// finding set, which we just discarded. Leaving them in place
	// would attach dismissal rationales to a fresh audit's findings
	// by coincidence of FindingID.
	if s.RecheckDismissals != nil {
		t.Error("RecheckDismissals should be nil after ClearAllCaches")
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

// ── PRBrief ─────────────────────────────────────────────────────────────

func TestSetGetPRBrief(t *testing.T) {
	s := NewState("42")
	s.SetPRBrief("PR #42 — auth refactor. 3 prior comments; CI passing.", "sha256xyz")

	brief, hash := s.GetPRBrief()
	if brief != "PR #42 — auth refactor. 3 prior comments; CI passing." {
		t.Errorf("brief = %q", brief)
	}
	if hash != "sha256xyz" {
		t.Errorf("hash = %q, want sha256xyz", hash)
	}
}

func TestGetPRBrief_Empty(t *testing.T) {
	s := NewState("42")
	brief, hash := s.GetPRBrief()
	if brief != "" || hash != "" {
		t.Errorf("expected empty strings, got %q, %q", brief, hash)
	}
}

// TestDeepFindings_PersistsAcrossSaveLoad is the load-bearing contract
// for the robustness fix: when pipeline writes deep findings to state,
// they must survive Save → close → Load. Without this guarantee, the
// pipeline could "succeed" yet leave the user with nothing on reopen —
// exactly the $20-loss symptom we're trying to fix.
//
// This is a state-layer test; the pipeline integration test in
// internal/review proves the pipeline actually populates this field.
func TestDeepFindings_PersistsAcrossSaveLoad(t *testing.T) {
	root := t.TempDir()
	prevRoot := repoRootFn
	repoRootFn = func() (string, error) { return root, nil }
	defer func() { repoRootFn = prevRoot }()

	s := NewState("42")
	s.SetDeepFindings([]DeepFinding{
		{FindingID: "F-001", Severity: "high", Category: "bug", File: "main.go", Lines: "42", Title: "off-by-one"},
		{FindingID: "F-002", Severity: "medium", Category: "performance", File: "util.go", Lines: "10-15", Title: "redundant copy"},
	})

	if err := Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load("42")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := loaded.GetDeepFindings()
	if len(got) != 2 {
		t.Fatalf("DeepFindings count after round-trip = %d, want 2", len(got))
	}
	if got[0].Title != "off-by-one" || got[1].Title != "redundant copy" {
		t.Errorf("DeepFindings content not preserved across save/load: %+v", got)
	}
}

// TestDeepFindings_AppendIsAtomicUnderConcurrency: the pipeline appends
// findings as each batch completes from multiple goroutines (parallel
// batch reviews). The append accessor must hold the write lock for the
// whole append, not split read-then-write — otherwise concurrent
// appends drop findings on the floor.
func TestDeepFindings_AppendIsAtomicUnderConcurrency(t *testing.T) {
	s := NewState("42")

	const writers = 8
	const perWriter = 50
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := range writers {
		go func(id int) {
			defer wg.Done()
			for j := range perWriter {
				s.AppendDeepFindings([]DeepFinding{
					{FindingID: fmt.Sprintf("w%d-f%d", id, j)},
				})
			}
		}(w)
	}
	wg.Wait()

	got := s.GetDeepFindings()
	want := writers * perWriter
	if len(got) != want {
		t.Errorf("DeepFindings count after %d concurrent appends = %d, want %d (lost findings under contention)",
			writers*perWriter, len(got), want)
	}
}

// TestCountCachedBatchFindings_RaceFree exercises the locked-iteration
// contract under concurrent writers. The race detector flags any
// access to s.Files that escapes the lock; without lock-discipline
// this test would catch the regression. Counter values are loose —
// what matters is that no race is reported.
func TestCountCachedBatchFindings_RaceFree(t *testing.T) {
	s := NewState("42")
	paths := []string{"a.go", "b.go", "c.go", "d.go"}

	// Seed everything.
	for _, p := range paths {
		s.SetBatchFindings(p, "p", "f")
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writers: keep mutating BatchFindings while readers count.
	wg.Add(2)
	for w := range 2 {
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					for _, p := range paths {
						s.SetBatchFindings(p, "p", fmt.Sprintf("w%d", id))
					}
				}
			}
		}(w)
	}

	// Readers: hammer CountCachedBatchFindings.
	wg.Add(4)
	for range 4 {
		go func() {
			defer wg.Done()
			for range 5000 {
				_ = s.CountCachedBatchFindings(paths)
			}
		}()
	}

	// Let readers finish; then signal writers to stop.
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestCountCachedBatchFindings pins basic correctness of the accessor.
// Lock behavior is verified separately by the _RaceFree test under
// -race.
func TestCountCachedBatchFindings(t *testing.T) {
	s := NewState("42")
	s.SetBatchFindings("a.go", "purpose-a", "findings-a")
	s.SetBatchFindings("b.go", "purpose-b", "findings-b")
	s.SetBatchFindings("empty.go", "purpose-c", "") // empty findings → not counted

	// Three paths: two populated, one empty, one not in state at all.
	got := s.CountCachedBatchFindings([]string{"a.go", "b.go", "empty.go", "missing.go"})
	if got != 2 {
		t.Errorf("CountCachedBatchFindings = %d, want 2", got)
	}

	// Empty input → 0.
	if got := s.CountCachedBatchFindings(nil); got != 0 {
		t.Errorf("nil paths = %d, want 0", got)
	}
}

// TestPRBrief_ClearInvalidates pins the invalidation contract: callers
// can force regeneration on next session by clearing the cached brief,
// even when input hashes would otherwise match. Important when an
// external trigger (e.g. prior AI review changes structure) should
// invalidate the brief independent of GitHub-side input changes.
func TestPRBrief_ClearInvalidates(t *testing.T) {
	s := NewState("42")
	s.SetPRBrief("cached brief", "hash1")
	s.ClearPRBrief()

	brief, hash := s.GetPRBrief()
	if brief != "" || hash != "" {
		t.Errorf("after Clear, expected empty; got brief=%q hash=%q", brief, hash)
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

// ── RecheckDismissals accessors ─────────────────────────────────────────

func TestSetRecheckDismissals_StoresCopy(t *testing.T) {
	s := NewState("1")
	input := []DismissedRecord{
		{FindingID: "F-001", Rationale: "first"},
		{FindingID: "F-002", Rationale: "second"},
	}

	s.SetRecheckDismissals(input)

	if len(s.RecheckDismissals) != 2 {
		t.Fatalf("expected 2 records persisted, got %d", len(s.RecheckDismissals))
	}

	// Mutating the input slice after the call must not affect state —
	// SetRecheckDismissals takes a defensive copy because the caller's
	// slice is shared across goroutines during recheck.
	input[0].Rationale = "mutated"
	if s.RecheckDismissals[0].Rationale != "first" {
		t.Errorf("state must hold a copy, not a reference: got %q", s.RecheckDismissals[0].Rationale)
	}
}

func TestSetRecheckDismissals_NilClears(t *testing.T) {
	s := NewState("1")
	s.SetRecheckDismissals([]DismissedRecord{{FindingID: "F-001", Rationale: "x"}})

	s.SetRecheckDismissals(nil)

	if s.RecheckDismissals != nil {
		t.Errorf("nil/empty input must clear the slice, got %d entries", len(s.RecheckDismissals))
	}
}

func TestSetRecheckDismissals_EmptyClears(t *testing.T) {
	s := NewState("1")
	s.SetRecheckDismissals([]DismissedRecord{{FindingID: "F-001", Rationale: "x"}})

	s.SetRecheckDismissals([]DismissedRecord{})

	if s.RecheckDismissals != nil {
		t.Errorf("empty input must clear the slice, got %d entries", len(s.RecheckDismissals))
	}
}

func TestGetRecheckDismissals_ReturnsCopy(t *testing.T) {
	s := NewState("1")
	s.SetRecheckDismissals([]DismissedRecord{{FindingID: "F-001", Rationale: "original"}})

	out := s.GetRecheckDismissals()
	if len(out) != 1 {
		t.Fatalf("expected 1 record, got %d", len(out))
	}

	// Mutating the returned slice must not affect state — Get returns
	// a defensive copy so concurrent UI reads can't race writers.
	out[0].Rationale = "mutated"
	if s.RecheckDismissals[0].Rationale != "original" {
		t.Errorf("Get must return a copy, mutation leaked into state: %q",
			s.RecheckDismissals[0].Rationale)
	}
}

func TestGetRecheckDismissals_EmptyReturnsNil(t *testing.T) {
	s := NewState("1")
	if got := s.GetRecheckDismissals(); got != nil {
		t.Errorf("expected nil for empty state, got %d entries", len(got))
	}
}

// ── FindingTrigger: structured + legacy-string back-compat ──────────────

func TestFindingTrigger_UnmarshalLegacyString(t *testing.T) {
	// Older cached state and the previous prompt schema both serialized
	// the trigger as a bare string. The structured type must still accept
	// it, dropping the value into Repro with Observable empty.
	raw := `"user spams the login endpoint"`
	var got FindingTrigger
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal legacy string: %v", err)
	}
	if got.Repro != "user spams the login endpoint" {
		t.Errorf("Repro = %q, want %q", got.Repro, "user spams the login endpoint")
	}
	if got.Observable != "" {
		t.Errorf("Observable = %q, want empty", got.Observable)
	}
}

func TestFindingTrigger_UnmarshalStructured(t *testing.T) {
	raw := `{"repro": "POST /admin/users body {\"role\": \"admin\"}", "observable": "200 OK"}`
	var got FindingTrigger
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal structured: %v", err)
	}
	if got.Repro != `POST /admin/users body {"role": "admin"}` {
		t.Errorf("Repro = %q", got.Repro)
	}
	if got.Observable != "200 OK" {
		t.Errorf("Observable = %q, want %q", got.Observable, "200 OK")
	}
}

func TestFindingTrigger_RoundTrip(t *testing.T) {
	orig := FindingTrigger{Repro: "trigger thing", Observable: "side effect"}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got FindingTrigger
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("round trip = %+v, want %+v", got, orig)
	}
}

func TestFindingTrigger_UnmarshalEmpty(t *testing.T) {
	cases := []string{`""`, `null`, ``, `{}`}
	for _, raw := range cases {
		var got FindingTrigger
		if err := json.Unmarshal([]byte(raw), &got); err != nil && raw != "" {
			t.Errorf("unmarshal %q: %v", raw, err)
			continue
		}
		if !got.IsZero() {
			t.Errorf("unmarshal %q: want zero, got %+v", raw, got)
		}
	}
}

func TestDeepFinding_UnmarshalLegacyStringTrigger(t *testing.T) {
	// A DeepFinding cached before commit 2 carries trigger as a string.
	// Loading it now must place the value in Trigger.Repro.
	raw := `{
		"aoi_id": "aoi-1",
		"file": "foo.go",
		"lines": "10",
		"severity": "high",
		"trigger": "user posts a malformed payload"
	}`
	var got DeepFinding
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Trigger.Repro != "user posts a malformed payload" {
		t.Errorf("Trigger.Repro = %q", got.Trigger.Repro)
	}
}

func TestDeepFinding_TraceRoundTrip(t *testing.T) {
	orig := DeepFinding{
		AOIID:    "aoi-1",
		Severity: "high",
		Trace: []TraceHop{
			{Role: "suspect", File: "a.go", Lines: "10", Evidence: "cited"},
			{Role: "caller", File: "b.go", Lines: "50", Evidence: "calls a"},
			{Role: "boundary", File: "c.go", Lines: "120", Evidence: "returns to client"},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DeepFinding
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Trace) != 3 {
		t.Fatalf("Trace length = %d, want 3", len(got.Trace))
	}
	if got.Trace[2].Role != "boundary" || got.Trace[2].File != "c.go" {
		t.Errorf("Trace[2] round-trip lost data: %+v", got.Trace[2])
	}
}

func TestDeepFinding_TraceOmittedWhenEmpty(t *testing.T) {
	// Findings at medium/low/nit don't include a trace — make sure the
	// field is omitempty and doesn't bloat the JSON.
	f := DeepFinding{AOIID: "x", Severity: "medium"}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "trace") {
		t.Errorf("empty Trace should be omitted from JSON, got %s", data)
	}
}

func TestDeepDismissal_RoundTrip(t *testing.T) {
	orig := DeepDismissal{
		AOIID:               "aoi-1",
		File:                "internal/audit/cluster.go",
		Evidence:            "checked validator at server.go:45",
		Rationale:           "guard catches this upstream",
		ConfidenceScore:     88,
		ConfidenceReasoning: "traced to middleware",
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DeepDismissal
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("round-trip mismatch\n got:  %+v\n want: %+v", got, orig)
	}
}

func TestReviewCoverage_RoundTrip(t *testing.T) {
	orig := &ReviewOutput{
		Summary: "x",
		Verdict: "approve",
		Coverage: &ReviewCoverage{
			Files: []FileCoverage{
				{
					File:               "a.go",
					AOIsScanned:        3,
					Findings:           1,
					Dismissals:         2,
					Failed:             0,
					AvgDismissConf:     85,
					MaxFindingSeverity: "high",
				},
			},
			FilesInScope:  3,
			FilesWithAOIs: 1,
			FilesReviewed: 1,
			OrphanFiles:   []string{"b.go", "c.go"},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ReviewOutput
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Coverage == nil {
		t.Fatal("coverage dropped on round-trip")
	}
	if len(got.Coverage.Files) != 1 || got.Coverage.Files[0].File != "a.go" {
		t.Errorf("files round-trip: %+v", got.Coverage.Files)
	}
	if len(got.Coverage.OrphanFiles) != 2 {
		t.Errorf("orphan files round-trip: %v", got.Coverage.OrphanFiles)
	}
}

func TestReviewCoverage_OmittedWhenNil(t *testing.T) {
	// Output with no coverage block must not emit "coverage":null —
	// otherwise downstream consumers parsing strict JSON schemas
	// would see the field present-but-null and fail.
	r := &ReviewOutput{Summary: "x", Verdict: "approve"}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "coverage") {
		t.Errorf("nil coverage should be omitted; got %s", data)
	}
}

func TestDeepDismissal_OptionalFieldsOmittedWhenEmpty(t *testing.T) {
	// Older cached state may not include File / ConfidenceScore. The
	// JSON shape must round-trip cleanly with the new fields zero.
	orig := DeepDismissal{AOIID: "aoi-1", Rationale: "not an issue"}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"file"`, `"confidence_score"`, `"confidence_reasoning"`} {
		if strings.Contains(string(data), key) {
			t.Errorf("zero-value %s should be omitted from JSON, got %s", key, data)
		}
	}
}

func TestDeepFinding_DefensesCheckedRoundTrip(t *testing.T) {
	orig := DeepFinding{
		AOIID:           "aoi-1",
		Category:        "authorization",
		Severity:        "high",
		DefensesChecked: []string{"boundary-authz", "handler-guard", "other:custom-check"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DeepFinding
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.DefensesChecked) != 3 {
		t.Fatalf("DefensesChecked length = %d, want 3", len(got.DefensesChecked))
	}
	if got.DefensesChecked[2] != "other:custom-check" {
		t.Errorf("DefensesChecked[2] = %q, want 'other:custom-check'", got.DefensesChecked[2])
	}
}

func TestDeepFinding_DefensesCheckedOmittedWhenEmpty(t *testing.T) {
	f := DeepFinding{AOIID: "x", Severity: "medium", Category: "correctness"}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "defenses_checked") {
		t.Errorf("empty DefensesChecked should be omitted, got %s", data)
	}
}

// ── ConfidenceBand: derived from score, falls back to legacy string ─────

func TestConfidenceBand(t *testing.T) {
	tests := []struct {
		score      int
		legacy     string
		wantBand   string
		wantReason string
	}{
		{score: 95, wantBand: "high"},
		{score: 80, wantBand: "high"},
		{score: 79, wantBand: "medium"},
		{score: 50, wantBand: "medium"},
		{score: 49, wantBand: "low"},
		{score: 0, legacy: "high", wantBand: "high"},     // legacy fallback (cached state)
		{score: 0, legacy: "medium", wantBand: "medium"}, // legacy fallback (cached state)
		// Penalized-to-zero finding with no legacy field falls through
		// to the switch and renders as "low" — previously this returned
		// "" and the UI blanked the confidence column.
		{score: 0, legacy: "", wantBand: "low"},
	}
	for _, tt := range tests {
		f := ReviewFinding{ConfidenceScore: tt.score, Confidence: tt.legacy}
		if got := f.ConfidenceBand(); got != tt.wantBand {
			t.Errorf("ConfidenceBand(score=%d legacy=%q) = %q, want %q",
				tt.score, tt.legacy, got, tt.wantBand)
		}
	}
}

// Regression: a finding produced by the new pipeline gets penalized
// down to ConfidenceScore=0 (e.g. a starting score of 40 with both
// missing-trace (-30) and defenses-not-checked (-25) applied). The
// legacy Confidence field is empty because the new pipeline doesn't
// populate it. Previously ConfidenceBand() returned "" here and the
// UI blanked the confidence column. Now it falls through to the
// switch and returns "low".
func TestConfidenceBand_PenalizedToZeroRendersAsLow(t *testing.T) {
	f := ReviewFinding{ConfidenceScore: 0, Confidence: ""}
	if got := f.ConfidenceBand(); got != "low" {
		t.Errorf("ConfidenceBand() = %q, want %q", got, "low")
	}
}

func TestReviewFinding_UnmarshalLegacyConfidence(t *testing.T) {
	// Older cached state carries confidence as a string band only.
	// Unmarshal must preserve it so ConfidenceBand() still renders.
	raw := `{"severity":"high","confidence":"medium","title":"t","file":"f","line":1,"category":"bug","detail":"d"}`
	var got ReviewFinding
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Confidence != "medium" {
		t.Errorf("Confidence = %q", got.Confidence)
	}
	if got.ConfidenceScore != 0 {
		t.Errorf("ConfidenceScore = %d, want 0 for legacy data", got.ConfidenceScore)
	}
	if band := got.ConfidenceBand(); band != "medium" {
		t.Errorf("ConfidenceBand() = %q, want medium (legacy fallback)", band)
	}
}

func TestReviewFinding_NewConfidenceWins(t *testing.T) {
	// When both fields are present (transitional state during the
	// migration window), ConfidenceBand prefers the structured score.
	raw := `{"severity":"high","confidence":"low","confidence_score":85,"title":"t","file":"f","line":1,"category":"bug","detail":"d"}`
	var got ReviewFinding
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ConfidenceBand() != "high" {
		t.Errorf("ConfidenceBand() = %q, want high (score-derived)", got.ConfidenceBand())
	}
}

// ── RuntimeModel: schema + render + persistence ─────────────────────────

func TestRuntimeModel_IsZero(t *testing.T) {
	var nilModel *RuntimeModel
	if !nilModel.IsZero() {
		t.Error("nil model should be zero")
	}
	if !(&RuntimeModel{}).IsZero() {
		t.Error("empty struct should be zero")
	}
	if (&RuntimeModel{AuthModel: "x"}).IsZero() {
		t.Error("model with AuthModel should not be zero")
	}
	if (&RuntimeModel{EntryPoints: []RuntimeEntryPoint{{Kind: "http"}}}).IsZero() {
		t.Error("model with entry points should not be zero")
	}
}

func TestRuntimeModel_Render_Empty(t *testing.T) {
	var m *RuntimeModel
	if got := m.Render(); got != "" {
		t.Errorf("nil render = %q, want empty", got)
	}
	if got := (&RuntimeModel{}).Render(); got != "" {
		t.Errorf("empty render = %q, want empty", got)
	}
}

func TestRuntimeModel_Render_Full(t *testing.T) {
	m := &RuntimeModel{
		AuthModel: "API Gateway authorizer validates JWT; in-handler `guardAdmin` for admin writes.",
		ValidationSites: []string{
			"All HTTP handlers parse body through a declared schema before reaching business logic",
			"Queue consumers parse each record through a declared schema",
		},
		EntryPoints: []RuntimeEntryPoint{
			{Kind: "http", RetryModel: "no retries — caller's job", BatchModel: "single-record", ValidationAt: "boundary"},
			{Kind: "queue", RetryModel: "exponential backoff per record", BatchModel: "batched, per-record isolated", ValidationAt: "handler"},
		},
		ResultDiscipline: "Result type with safeTry — all error paths propagate, no silent swallows.",
		Invariants: []string{
			"All IDs are UUID v4",
			"Amounts stored in minor units (cents)",
		},
	}

	out := m.Render()
	if !strings.HasPrefix(out, "## Runtime Model\n") {
		t.Errorf("render must start with the section header; got %q...", out[:50])
	}

	// Each field's content must appear.
	for _, want := range []string{
		"API Gateway authorizer",
		"All HTTP handlers parse body",
		"`http`",
		"validation at boundary",
		"retries: no retries",
		"`queue`",
		"batching: batched, per-record isolated",
		"Result type with safeTry",
		"UUID v4",
		"minor units",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n--- output ---\n%s", want, out)
		}
	}

	// Compact: budget is ~1KB so the section doesn't crowd the prompt.
	if len(out) > 2048 {
		t.Errorf("render size = %d bytes; budget is ~1KB (hard cap 2KB)", len(out))
	}
}

func TestRuntimeModel_Render_PartialOmitsEmptyFields(t *testing.T) {
	m := &RuntimeModel{AuthModel: "single auth check at the gateway"}
	out := m.Render()
	if !strings.Contains(out, "single auth check") {
		t.Errorf("output should contain auth content: %q", out)
	}
	for _, banned := range []string{"Validation sites", "Entry points", "Result discipline", "Invariants"} {
		if strings.Contains(out, banned) {
			t.Errorf("output should not include empty section %q\n%s", banned, out)
		}
	}
}

func TestRuntimeModel_RoundTrip(t *testing.T) {
	orig := &RuntimeModel{
		AuthModel:       "gateway authorizer",
		ValidationSites: []string{"boundary"},
		EntryPoints: []RuntimeEntryPoint{
			{Kind: "http", ValidationAt: "boundary"},
		},
		Invariants: []string{"x"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got RuntimeModel
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AuthModel != orig.AuthModel {
		t.Errorf("AuthModel mismatch")
	}
	if len(got.EntryPoints) != 1 || got.EntryPoints[0].Kind != "http" {
		t.Errorf("EntryPoints round-trip lost data: %+v", got.EntryPoints)
	}
}

func TestStateSetGetRuntimeModel(t *testing.T) {
	s := NewState("1")

	// Empty state returns nil model.
	if m, h := s.GetRuntimeModel(); m != nil || h != "" {
		t.Errorf("empty state should return nil/empty, got %+v / %q", m, h)
	}

	m := &RuntimeModel{AuthModel: "gateway authorizer"}
	s.SetRuntimeModel(m, "hash-abc")

	got, hash := s.GetRuntimeModel()
	if got == nil || got.AuthModel != "gateway authorizer" {
		t.Errorf("GetRuntimeModel = %+v, want with AuthModel set", got)
	}
	if hash != "hash-abc" {
		t.Errorf("hash = %q, want hash-abc", hash)
	}
}

func TestStateClearAllCachesAlsoClearsRuntimeModel(t *testing.T) {
	s := NewState("1")
	s.SetRuntimeModel(&RuntimeModel{AuthModel: "gateway"}, "h1")
	s.ClearAllCaches()
	if m, h := s.GetRuntimeModel(); m != nil || h != "" {
		t.Errorf("ClearAllCaches should clear the runtime model, got %+v / %q", m, h)
	}
}

// ── BoundaryInventory: schema + persistence ─────────────────────────────

func TestBoundary_RoundTrip(t *testing.T) {
	orig := []Boundary{
		{Kind: "http", File: "handler.go", Lines: "10-50", Symbol: "createUser", Description: "POST /users"},
		{Kind: "queue", File: "consumer.go", Description: "SNS payments-topic"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got []Boundary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 || got[0].Kind != "http" || got[1].Kind != "queue" {
		t.Errorf("round trip lost data: %+v", got)
	}
	if got[0].Lines != "10-50" || got[0].Symbol != "createUser" {
		t.Errorf("optional fields lost: %+v", got[0])
	}
}

func TestStateSetGetBoundaryInventory(t *testing.T) {
	s := NewState("1")

	if b, h := s.GetBoundaryInventory(); b != nil || h != "" {
		t.Errorf("empty state should return nil/empty, got %+v / %q", b, h)
	}

	inv := []Boundary{{Kind: "http", File: "h.go", Description: "x"}}
	s.SetBoundaryInventory(inv, "hash-xyz")

	got, hash := s.GetBoundaryInventory()
	if len(got) != 1 || got[0].File != "h.go" {
		t.Errorf("GetBoundaryInventory = %+v, want one entry for h.go", got)
	}
	if hash != "hash-xyz" {
		t.Errorf("hash = %q, want hash-xyz", hash)
	}
}

func TestStateClearAllCachesAlsoClearsBoundaryInventory(t *testing.T) {
	s := NewState("1")
	s.SetBoundaryInventory([]Boundary{{Kind: "http", File: "h.go", Description: "x"}}, "h1")
	s.ClearAllCaches()
	if b, h := s.GetBoundaryInventory(); b != nil || h != "" {
		t.Errorf("ClearAllCaches should clear the boundary inventory, got %+v / %q", b, h)
	}
}

// ── SiblingDeviation: schema + DeepFinding round-trip ───────────────────

func TestSiblingDeviation_RoundTrip(t *testing.T) {
	orig := SiblingDeviation{
		Pattern:    "9 of 11 handlers call guardAdmin",
		SiblingIDs: []string{"a-id", "b-id", "c-id"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SiblingDeviation
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Pattern != orig.Pattern || len(got.SiblingIDs) != 3 {
		t.Errorf("round-trip lost data: %+v", got)
	}
}

func TestDeepFinding_SiblingDeviationRoundTrip(t *testing.T) {
	orig := DeepFinding{
		AOIID:    "aoi-1",
		Severity: "high",
		SiblingDeviation: &SiblingDeviation{
			Pattern:    "9 of 11 handlers call guardAdmin",
			SiblingIDs: []string{"a", "b"},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DeepFinding
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SiblingDeviation == nil {
		t.Fatal("SiblingDeviation should round-trip")
	}
	if got.SiblingDeviation.Pattern != orig.SiblingDeviation.Pattern {
		t.Errorf("pattern lost: %q", got.SiblingDeviation.Pattern)
	}
}

func TestDeepFinding_SiblingDeviationOmittedWhenNil(t *testing.T) {
	f := DeepFinding{AOIID: "x", Severity: "medium"}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "sibling_deviation") {
		t.Errorf("nil SiblingDeviation should be omitted, got %s", data)
	}
}

// ── SiteRef + AffectedSites ─────────────────────────────────────────────

func TestDeepFinding_AffectedSitesRoundTrip(t *testing.T) {
	orig := DeepFinding{
		AOIID:    "aoi-1",
		Severity: "medium",
		Systemic: true,
		AffectedSites: []SiteRef{
			{File: "a.go", Lines: "10-20", Symbol: "createUser"},
			{File: "b.go", Lines: "55-70", Symbol: "updateUser"},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DeepFinding
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.AffectedSites) != 2 {
		t.Fatalf("AffectedSites length = %d, want 2", len(got.AffectedSites))
	}
	if got.AffectedSites[0].File != "a.go" || got.AffectedSites[0].Symbol != "createUser" {
		t.Errorf("site round-trip lost data: %+v", got.AffectedSites[0])
	}
}

func TestDeepFinding_AffectedSitesOmittedWhenEmpty(t *testing.T) {
	f := DeepFinding{AOIID: "x", Severity: "medium"}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "affected_sites") {
		t.Errorf("empty AffectedSites should be omitted, got %s", data)
	}
}

// ── SiblingClusterCache: opaque JSON + hash persistence ─────────────────

func TestStateSetGetSiblingClusterCache(t *testing.T) {
	s := NewState("1")
	if raw, h := s.GetSiblingClusterCache(); raw != nil || h != "" {
		t.Errorf("empty state should return nil/empty, got %s / %q", raw, h)
	}

	payload := json.RawMessage(`[{"id":"deviant-x","file":"h.go"}]`)
	s.SetSiblingClusterCache(payload, "hash-xyz")

	gotRaw, gotHash := s.GetSiblingClusterCache()
	if string(gotRaw) != string(payload) {
		t.Errorf("payload round-trip failed: got %s", gotRaw)
	}
	if gotHash != "hash-xyz" {
		t.Errorf("hash = %q, want hash-xyz", gotHash)
	}
}

func TestStateClearAllCachesAlsoClearsSiblingClusterCache(t *testing.T) {
	s := NewState("1")
	s.SetSiblingClusterCache(json.RawMessage(`[]`), "h1")
	s.ClearAllCaches()
	if raw, h := s.GetSiblingClusterCache(); raw != nil || h != "" {
		t.Errorf("ClearAllCaches should clear the cluster cache, got %s / %q", raw, h)
	}
}

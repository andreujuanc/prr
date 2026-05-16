package review

import (
	"os"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/state"
)

func prFiles(paths ...string) []git.PRFile {
	out := make([]git.PRFile, len(paths))
	for i, p := range paths {
		out[i] = git.PRFile{Path: p}
	}
	return out
}

func TestValidateAndNormalize_DropsEmptyTitle(t *testing.T) {
	in := &state.ReviewOutput{Findings: []state.ReviewFinding{
		{Severity: "high", Category: "bug", File: "foo.go", Line: 1, Title: ""},
	}}
	out, dropped := ValidateAndNormalize(in, prFiles("foo.go"), nil)
	if len(out.Findings) != 0 {
		t.Fatalf("expected 0 kept, got %d", len(out.Findings))
	}
	if len(dropped) != 1 || dropped[0].Reason != "empty title" {
		t.Fatalf("expected one empty-title drop, got %+v", dropped)
	}
}

func TestValidateAndNormalize_DropsHallucinatedFile(t *testing.T) {
	in := &state.ReviewOutput{Findings: []state.ReviewFinding{
		{Severity: "high", Category: "bug", File: "imagined.go", Title: "x"},
	}}
	out, dropped := ValidateAndNormalize(in, prFiles("real.go"), nil)
	if len(out.Findings) != 0 {
		t.Fatalf("expected 0 kept, got %d", len(out.Findings))
	}
	if len(dropped) != 1 || dropped[0].Reason != "file not in PR" {
		t.Fatalf("expected one file-not-in-PR drop, got %+v", dropped)
	}
}

func TestValidateAndNormalize_NormalisesUnknownSeverity(t *testing.T) {
	in := &state.ReviewOutput{Findings: []state.ReviewFinding{
		{Severity: "warn", Category: "bug", File: "foo.go", Title: "x"},
	}}
	out, dropped := ValidateAndNormalize(in, prFiles("foo.go"), nil)
	if len(dropped) != 0 {
		t.Fatalf("unexpected drops: %+v", dropped)
	}
	if got := out.Findings[0].Severity; got != "low" {
		t.Fatalf("severity = %q, want low (normalised)", got)
	}
}

func TestValidateAndNormalize_NormalisesUnknownCategory(t *testing.T) {
	in := &state.ReviewOutput{Findings: []state.ReviewFinding{
		{Severity: "high", Category: "vibes", File: "foo.go", Title: "x"},
	}}
	out, _ := ValidateAndNormalize(in, prFiles("foo.go"), nil)
	if got := out.Findings[0].Category; got != "style" {
		t.Fatalf("category = %q, want style (normalised)", got)
	}
}

func TestValidateAndNormalize_SnapsLineToNearestHunk(t *testing.T) {
	hunks := map[string][]HunkRange{
		"foo.go": {{Start: 30, End: 50}, {Start: 80, End: 100}},
	}
	in := &state.ReviewOutput{Findings: []state.ReviewFinding{
		// Out-of-hunk, closer to the second hunk.
		{Severity: "high", Category: "bug", File: "foo.go", Line: 75, Title: "x"},
	}}
	out, _ := ValidateAndNormalize(in, prFiles("foo.go"), hunks)
	if got := out.Findings[0].Line; got != 80 {
		t.Fatalf("line = %d, want 80 (snapped to nearest hunk start)", got)
	}
}

func TestValidateAndNormalize_KeepsLineInsideHunk(t *testing.T) {
	hunks := map[string][]HunkRange{
		"foo.go": {{Start: 30, End: 50}},
	}
	in := &state.ReviewOutput{Findings: []state.ReviewFinding{
		{Severity: "high", Category: "bug", File: "foo.go", Line: 42, Title: "x"},
	}}
	out, _ := ValidateAndNormalize(in, prFiles("foo.go"), hunks)
	if got := out.Findings[0].Line; got != 42 {
		t.Fatalf("line = %d, want 42 (inside hunk, unchanged)", got)
	}
}

func TestValidateAndNormalize_PreservesPRLevelFindings(t *testing.T) {
	in := &state.ReviewOutput{Findings: []state.ReviewFinding{
		{Severity: "high", Category: "architecture", File: "", Line: 0, Title: "x"},
	}}
	out, dropped := ValidateAndNormalize(in, prFiles("foo.go"), nil)
	if len(out.Findings) != 1 {
		t.Fatalf("expected PR-level finding preserved, dropped=%+v", dropped)
	}
}

func TestValidateAndNormalize_StripsLeadingDotSlash(t *testing.T) {
	in := &state.ReviewOutput{Findings: []state.ReviewFinding{
		{Severity: "high", Category: "bug", File: "./foo.go", Title: "x"},
	}}
	out, _ := ValidateAndNormalize(in, prFiles("foo.go"), nil)
	if got := out.Findings[0].File; got != "foo.go" {
		t.Fatalf("file = %q, want %q", got, "foo.go")
	}
}

func TestParseHunkRanges_StandardFormat(t *testing.T) {
	patch := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -10,5 +30,8 @@ func foo() {
 context line
+new line
@@ -50,1 +80,3 @@
 context
+new
+new
`
	got := ParseHunkRanges(patch)
	if len(got) != 2 {
		t.Fatalf("got %d hunks, want 2: %+v", len(got), got)
	}
	if got[0] != (HunkRange{Start: 30, End: 38}) {
		t.Fatalf("hunk[0] = %+v, want {30 38}", got[0])
	}
	if got[1] != (HunkRange{Start: 80, End: 83}) {
		t.Fatalf("hunk[1] = %+v, want {80 83}", got[1])
	}
}

func TestParseHunkRanges_NoCount(t *testing.T) {
	// GNU diff omits the count when it's 1.
	patch := "@@ -10 +42 @@\n+x\n"
	got := ParseHunkRanges(patch)
	if len(got) != 1 || got[0] != (HunkRange{Start: 42, End: 43}) {
		t.Fatalf("got %+v, want [{42 43}]", got)
	}
}

func TestValidateAndNormalize_NilInput(t *testing.T) {
	out, dropped := ValidateAndNormalize(nil, nil, nil)
	if out != nil || dropped != nil {
		t.Fatalf("nil input must round-trip nil/nil, got %v / %v", out, dropped)
	}
}

// ── ApplyConfidencePenalties: 3-hop trace requirement ────────────────────

func threeHopTrace() []state.TraceHop {
	return []state.TraceHop{
		{Role: "suspect", File: "a.go", Lines: "10", Evidence: "cited line"},
		{Role: "caller", File: "b.go", Lines: "50", Evidence: "calls a.go:10"},
		{Role: "boundary", File: "c.go", Lines: "120", Evidence: "HTTP response"},
	}
}

func TestApplyConfidencePenalties_HighWithoutTraceDocksConfidence(t *testing.T) {
	findings := []state.DeepFinding{
		{Severity: "high", ConfidenceScore: 85},
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 55 {
		t.Errorf("ConfidenceScore = %d, want 55 (85 - 30)", out[0].ConfidenceScore)
	}
	if out[0].ConfidenceReasoning != "missing-trace" {
		t.Errorf("ConfidenceReasoning = %q, want 'missing-trace'", out[0].ConfidenceReasoning)
	}
	if out[0].Severity != "high" {
		t.Errorf("Severity should remain unchanged, got %q", out[0].Severity)
	}
}

func TestApplyConfidencePenalties_CriticalWithoutTraceDocksConfidence(t *testing.T) {
	findings := []state.DeepFinding{
		{Severity: "critical", ConfidenceScore: 90},
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 60 {
		t.Errorf("ConfidenceScore = %d, want 60", out[0].ConfidenceScore)
	}
}

func TestApplyConfidencePenalties_HighWithTraceUntouched(t *testing.T) {
	findings := []state.DeepFinding{
		{Severity: "high", ConfidenceScore: 85, Trace: threeHopTrace()},
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 85 {
		t.Errorf("ConfidenceScore should not move when trace is present; got %d", out[0].ConfidenceScore)
	}
	if out[0].ConfidenceReasoning != "" {
		t.Errorf("ConfidenceReasoning should be empty; got %q", out[0].ConfidenceReasoning)
	}
}

func TestApplyConfidencePenalties_MediumWithoutTraceUntouched(t *testing.T) {
	findings := []state.DeepFinding{
		{Severity: "medium", ConfidenceScore: 75},
		{Severity: "low", ConfidenceScore: 50},
		{Severity: "nit", ConfidenceScore: 30},
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 75 || out[1].ConfidenceScore != 50 || out[2].ConfidenceScore != 30 {
		t.Errorf("medium/low/nit should not get the missing-trace penalty; got %+v", out)
	}
}

func TestApplyConfidencePenalties_TooFewHops(t *testing.T) {
	// Only 2 hops — the rule requires 3.
	short := []state.TraceHop{
		{Role: "suspect", File: "a.go", Lines: "10"},
		{Role: "caller", File: "b.go", Lines: "50"},
	}
	findings := []state.DeepFinding{
		{Severity: "high", ConfidenceScore: 85, Trace: short},
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 55 {
		t.Errorf("2-hop trace should not satisfy 3-hop rule; got %d", out[0].ConfidenceScore)
	}
}

func TestApplyConfidencePenalties_HopWithoutRoleDoesNotCount(t *testing.T) {
	// A hop with empty role is malformed; should not contribute.
	hops := []state.TraceHop{
		{Role: "suspect", File: "a.go", Lines: "10"},
		{Role: "", File: "b.go", Lines: "50"}, // unlabeled
		{Role: "boundary", File: "c.go", Lines: "120"},
	}
	findings := []state.DeepFinding{
		{Severity: "high", ConfidenceScore: 80, Trace: hops},
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 50 {
		t.Errorf("unlabeled hops should not count; want penalty applied, got %d", out[0].ConfidenceScore)
	}
}

func TestApplyConfidencePenalties_FloorsAtZero(t *testing.T) {
	findings := []state.DeepFinding{
		{Severity: "high", ConfidenceScore: 10}, // 10 - 30 underflows
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 0 {
		t.Errorf("ConfidenceScore should floor at 0, got %d", out[0].ConfidenceScore)
	}
}

func TestApplyConfidencePenalties_PreservesExistingReasoning(t *testing.T) {
	findings := []state.DeepFinding{
		{Severity: "high", ConfidenceScore: 80, ConfidenceReasoning: "saw the call site"},
	}
	out := ApplyConfidencePenalties(findings)
	want := "saw the call site; missing-trace"
	if out[0].ConfidenceReasoning != want {
		t.Errorf("ConfidenceReasoning = %q, want %q", out[0].ConfidenceReasoning, want)
	}
}

func TestApplyConfidencePenalties_IdempotentTag(t *testing.T) {
	// Running the validator twice should not duplicate the tag.
	findings := []state.DeepFinding{
		{Severity: "high", ConfidenceScore: 80},
	}
	out := ApplyConfidencePenalties(findings)
	out = ApplyConfidencePenalties(out)
	// First pass: 80 → 50, reasoning="missing-trace".
	// Second pass: 50 → 20, reasoning still "missing-trace" (no duplicate).
	if out[0].ConfidenceReasoning != "missing-trace" {
		t.Errorf("ConfidenceReasoning duplicated: %q", out[0].ConfidenceReasoning)
	}
}

func TestApplyConfidencePenalties_GroupedReviewIndependentAOIs(t *testing.T) {
	// Grouped review test: mixed-severity findings are judged per AOI.
	findings := []state.DeepFinding{
		{Severity: "high", ConfidenceScore: 80},                         // penalty
		{Severity: "medium", ConfidenceScore: 70},                       // no penalty
		{Severity: "high", ConfidenceScore: 80, Trace: threeHopTrace()}, // no penalty
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 50 {
		t.Errorf("[0] expected penalty: %d", out[0].ConfidenceScore)
	}
	if out[1].ConfidenceScore != 70 {
		t.Errorf("[1] medium should be untouched: %d", out[1].ConfidenceScore)
	}
	if out[2].ConfidenceScore != 80 {
		t.Errorf("[2] high+trace should be untouched: %d", out[2].ConfidenceScore)
	}
}

// ── ApplyConfidencePenalties: defenses_checked requirement ──────────────

func TestApplyConfidencePenalties_RequiredCategoryEmptyDefensesDocks(t *testing.T) {
	for _, cat := range []string{"authorization", "concurrency", "input-validation", "external-io"} {
		// medium severity + 3-hop trace not required → only the
		// defenses penalty fires.
		findings := []state.DeepFinding{
			{Category: cat, Severity: "medium", ConfidenceScore: 80},
		}
		out := ApplyConfidencePenalties(findings)
		if out[0].ConfidenceScore != 55 {
			t.Errorf("category %q: ConfidenceScore = %d, want 55 (80 - 25)", cat, out[0].ConfidenceScore)
		}
		if !strings.Contains(out[0].ConfidenceReasoning, "defenses-not-checked") {
			t.Errorf("category %q: reasoning = %q, want 'defenses-not-checked'", cat, out[0].ConfidenceReasoning)
		}
		if out[0].Severity != "medium" {
			t.Errorf("category %q: severity should remain unchanged", cat)
		}
	}
}

func TestApplyConfidencePenalties_RequiredCategoryWithDefensesUntouched(t *testing.T) {
	findings := []state.DeepFinding{
		{Category: "authorization", Severity: "medium", ConfidenceScore: 80, DefensesChecked: []string{"boundary-authz"}},
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 80 {
		t.Errorf("ConfidenceScore should not move when defenses listed; got %d", out[0].ConfidenceScore)
	}
	if out[0].ConfidenceReasoning != "" {
		t.Errorf("ConfidenceReasoning should be empty; got %q", out[0].ConfidenceReasoning)
	}
}

func TestApplyConfidencePenalties_NonRequiredCategoryEmptyDefensesUntouched(t *testing.T) {
	// Categories not in the required set don't penalize for empty
	// defenses — error-handling, correctness, performance, etc.
	for _, cat := range []string{"correctness", "error-handling", "performance", "testing"} {
		findings := []state.DeepFinding{
			{Category: cat, Severity: "medium", ConfidenceScore: 80},
		}
		out := ApplyConfidencePenalties(findings)
		if out[0].ConfidenceScore != 80 {
			t.Errorf("category %q: should not penalize; got %d", cat, out[0].ConfidenceScore)
		}
	}
}

func TestApplyConfidencePenalties_BothPenaltiesCompound(t *testing.T) {
	// A high-severity authz finding with no trace AND no defenses
	// gets BOTH penalties.
	findings := []state.DeepFinding{
		{Category: "authorization", Severity: "high", ConfidenceScore: 90},
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 35 {
		t.Errorf("ConfidenceScore = %d, want 35 (90 - 30 - 25)", out[0].ConfidenceScore)
	}
	if !strings.Contains(out[0].ConfidenceReasoning, "missing-trace") {
		t.Errorf("reasoning missing 'missing-trace': %q", out[0].ConfidenceReasoning)
	}
	if !strings.Contains(out[0].ConfidenceReasoning, "defenses-not-checked") {
		t.Errorf("reasoning missing 'defenses-not-checked': %q", out[0].ConfidenceReasoning)
	}
}

func TestApplyConfidencePenalties_CategoryCaseInsensitive(t *testing.T) {
	// LLM might emit "Authorization" or "AUTHORIZATION" — match
	// case-insensitively.
	for _, cat := range []string{"Authorization", "INPUT-VALIDATION", "External-IO"} {
		findings := []state.DeepFinding{
			{Category: cat, Severity: "medium", ConfidenceScore: 80},
		}
		out := ApplyConfidencePenalties(findings)
		if out[0].ConfidenceScore != 55 {
			t.Errorf("category %q: case-insensitive match failed; got %d", cat, out[0].ConfidenceScore)
		}
	}
}

func TestApplyConfidencePenalties_OtherTagSatisfiesDefenses(t *testing.T) {
	// `other:<tag>` is the documented escape hatch for defense
	// classes outside the canonical vocabulary. It still counts as a
	// non-empty list.
	findings := []state.DeepFinding{
		{Category: "concurrency", Severity: "medium", ConfidenceScore: 80, DefensesChecked: []string{"other:lock-free-algorithm"}},
	}
	out := ApplyConfidencePenalties(findings)
	if out[0].ConfidenceScore != 80 {
		t.Errorf("other:tag should count as non-empty; got %d", out[0].ConfidenceScore)
	}
}

// ── ApplySystemicGate: ≥3 distinct sites or strip the framing ───────────

func TestApplySystemicGate_KeepsSystemicWhenSitesSatisfy(t *testing.T) {
	findings := []state.DeepFinding{
		{
			Systemic: true,
			Title:    "Systemic: Missing input validation",
			File:     "multiple",
			AffectedSites: []state.SiteRef{
				{File: "a.go"}, {File: "b.go"}, {File: "c.go"},
			},
		},
	}
	out := ApplySystemicGate(findings)
	if !out[0].Systemic {
		t.Error("3 distinct sites should preserve the Systemic flag")
	}
	if !strings.HasPrefix(out[0].Title, "Systemic:") {
		t.Errorf("Systemic title prefix should be preserved; got %q", out[0].Title)
	}
}

func TestApplySystemicGate_DemotesBelowThreeSites(t *testing.T) {
	findings := []state.DeepFinding{
		{
			Systemic: true,
			Title:    "Systemic: Missing input validation",
			File:     "multiple",
			AffectedSites: []state.SiteRef{
				{File: "a.go"}, {File: "b.go"},
			},
		},
	}
	out := ApplySystemicGate(findings)
	if out[0].Systemic {
		t.Error("2 sites should clear the Systemic flag")
	}
	if strings.HasPrefix(out[0].Title, "Systemic:") {
		t.Errorf("Systemic prefix should be stripped; got %q", out[0].Title)
	}
	if out[0].Title != "Missing input validation" {
		t.Errorf("title after strip = %q, want 'Missing input validation'", out[0].Title)
	}
}

func TestApplySystemicGate_DemotesWhenSitesAreSameFile(t *testing.T) {
	// 3 entries but all pointing at the same file → 1 distinct file.
	findings := []state.DeepFinding{
		{
			Systemic: true,
			Title:    "Systemic: pattern",
			File:     "multiple",
			AffectedSites: []state.SiteRef{
				{File: "a.go"}, {File: "a.go"}, {File: "a.go"},
			},
		},
	}
	out := ApplySystemicGate(findings)
	if out[0].Systemic {
		t.Error("3 entries on same file = 1 distinct file, should demote")
	}
}

func TestApplySystemicGate_RewritesFilePlaceholder(t *testing.T) {
	// "multiple" placeholder file should become a real path on demotion
	// so the report doesn't render "[multiple] Missing validation".
	findings := []state.DeepFinding{
		{
			Systemic: true,
			Title:    "Systemic: pattern",
			File:     "multiple",
			Lines:    "",
			AffectedSites: []state.SiteRef{
				{File: "handler.go", Lines: "42-58"},
			},
		},
	}
	out := ApplySystemicGate(findings)
	if out[0].File != "handler.go" {
		t.Errorf("File = %q, want handler.go (taken from first site)", out[0].File)
	}
	if out[0].Lines != "42-58" {
		t.Errorf("Lines = %q, want 42-58", out[0].Lines)
	}
}

func TestApplySystemicGate_LeavesNonSystemicAlone(t *testing.T) {
	findings := []state.DeepFinding{
		{
			Systemic:      false,
			Title:         "Regular finding",
			AffectedSites: nil,
		},
	}
	out := ApplySystemicGate(findings)
	if out[0].Title != "Regular finding" {
		t.Errorf("non-systemic finding should not be modified; got %q", out[0].Title)
	}
}

// ── Test-suite coverage cross-check ──────────────────────────────────────

func TestCandidateTestPaths_GoConvention(t *testing.T) {
	got := candidateTestPaths("internal/auth/login.go")
	wantAny := []string{
		"internal/auth/login_test.go",
		"internal/auth/login.test.go",
		"internal/auth/login.spec.go",
	}
	for _, w := range wantAny {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected candidate %q in %v", w, got)
		}
	}
}

func TestCandidateTestPaths_JSExtensions(t *testing.T) {
	got := candidateTestPaths("src/auth/login.ts")
	want := map[string]bool{
		"src/auth/login_test.ts": false,
		"src/auth/login.test.ts": false,
		"src/auth/login.spec.ts": false,
	}
	for _, g := range got {
		if _, ok := want[g]; ok {
			want[g] = true
		}
	}
	for p, found := range want {
		if !found {
			t.Errorf("expected %q in candidates: %v", p, got)
		}
	}
}

func TestCitedSymbol(t *testing.T) {
	cases := []struct{ in, want string }{
		{"internal/auth/login.go", "login"},
		{"foo.ts", "foo"},
		{"a/b/c.py", "c"},
		{"noext", "noext"},
	}
	for _, c := range cases {
		if got := citedSymbol(c.in); got != c.want {
			t.Errorf("citedSymbol(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCheckTestCoverage_MissingTestFile(t *testing.T) {
	dir := t.TempDir()
	// One source file, no test file.
	if err := os.WriteFile(dir+"/login.go", []byte("package main\nfunc Login(){}"), 0o644); err != nil {
		t.Fatal(err)
	}
	hints := CheckTestCoverage(dir, []state.DeepFinding{
		{FindingID: "F-1", File: "login.go"},
	})
	if hints["F-1"] != TestCoverageMissing {
		t.Errorf("hint = %q, want %q", hints["F-1"], TestCoverageMissing)
	}
}

func TestCheckTestCoverage_TestExistsAndCovers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/login.go", []byte("package main\nfunc Login(){}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Test file mentions "login" (the cited symbol = base name).
	if err := os.WriteFile(dir+"/login_test.go",
		[]byte("package main\nimport \"testing\"\nfunc TestLogin(t *testing.T){ Login() }"), 0o644); err != nil {
		t.Fatal(err)
	}
	hints := CheckTestCoverage(dir, []state.DeepFinding{
		{FindingID: "F-1", File: "login.go"},
	})
	if hints["F-1"] != TestCoverageExistsAndCovers {
		t.Errorf("hint = %q, want %q", hints["F-1"], TestCoverageExistsAndCovers)
	}
}

func TestCheckTestCoverage_TestExistsButNotCovering(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/login.go", []byte("package main\nfunc Login(){}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Test file exists but does not reference "login".
	if err := os.WriteFile(dir+"/login_test.go",
		[]byte("package main\nimport \"testing\"\nfunc TestUnrelated(t *testing.T){}"), 0o644); err != nil {
		t.Fatal(err)
	}
	hints := CheckTestCoverage(dir, []state.DeepFinding{
		{FindingID: "F-1", File: "login.go"},
	})
	if hints["F-1"] != TestCoverageExistsButNotCovering {
		t.Errorf("hint = %q, want %q", hints["F-1"], TestCoverageExistsButNotCovering)
	}
}

func TestCheckTestCoverage_NoRepoRoot(t *testing.T) {
	hints := CheckTestCoverage("", []state.DeepFinding{{FindingID: "F-1", File: "x.go"}})
	if hints != nil {
		t.Errorf("empty repoRoot should produce nil hints; got %+v", hints)
	}
}

func TestCheckTestCoverage_SkipsFindingsWithoutID(t *testing.T) {
	dir := t.TempDir()
	hints := CheckTestCoverage(dir, []state.DeepFinding{{File: "x.go"}})
	if _, ok := hints[""]; ok {
		t.Error("finding without ID should not produce a hint entry")
	}
}

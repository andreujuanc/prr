package review

import (
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

package review

import (
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

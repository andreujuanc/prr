package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

// The verifier's contract is narrow but load-bearing: it must
//
//  1. accept findings whose snippet matches the cited file (modulo
//     whitespace) within the tolerance window,
//  2. reject findings whose snippet doesn't appear in the file at all,
//  3. accept findings with no snippet (we don't punish missing —
//     only hallucinated),
//  4. accept findings whose file is too big / unreadable (the
//     verifier shouldn't punish the model for our infrastructure),
//  5. reject findings whose file doesn't exist (those are
//     unambiguous hallucinations).
//
// These tests pin each clause.

// writeTestFile writes content to a path inside a fresh temp dir and
// returns the directory + relative path. Using a temp dir per test
// keeps the verifier honest about its repoRoot/path arithmetic.
func writeTestFile(t *testing.T, name, content string) (repoRoot, relPath string) {
	t.Helper()
	repoRoot = t.TempDir()
	full := filepath.Join(repoRoot, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return repoRoot, name
}

func TestVerifyEvidence_ExactMatch(t *testing.T) {
	root, rel := writeTestFile(t, "auth/login.go", `package auth

func Login(user, pass string) error {
	if err := validate(user); err != nil {
		return err
	}
	return nil
}
`)
	f := state.DeepFinding{
		File:            rel,
		Lines:           "4-6",
		EvidenceSnippet: `if err := validate(user); err != nil {`,
	}
	if got := verifyEvidence(root, f); got != evidenceOK {
		t.Errorf("expected evidenceOK for an exact match, got %v", got)
	}
}

func TestVerifyEvidence_WhitespaceTolerant(t *testing.T) {
	// File has a tab-indented line; model emits the same content
	// with the indent collapsed to spaces. The matcher must still
	// accept it — whitespace drift is the single most common LLM
	// snippet failure and rejecting it would produce massive FN.
	root, rel := writeTestFile(t, "x.go", "func F() {\n\tif x == nil {\n\t\treturn\n\t}\n}\n")
	f := state.DeepFinding{
		File:            rel,
		Lines:           "2",
		EvidenceSnippet: "    if x == nil {", // spaces instead of tab
	}
	if got := verifyEvidence(root, f); got != evidenceOK {
		t.Errorf("whitespace-normalized snippet must match, got %v", got)
	}
}

func TestVerifyEvidence_RejectsHallucinatedSnippet(t *testing.T) {
	root, rel := writeTestFile(t, "x.go", "func F() {\n\treturn nil\n}\n")
	f := state.DeepFinding{
		File:            rel,
		Lines:           "1-3",
		EvidenceSnippet: `panic("totally fabricated code")`,
	}
	if got := verifyEvidence(root, f); got != evidenceMismatch {
		t.Errorf("hallucinated snippet must produce evidenceMismatch, got %v", got)
	}
}

func TestVerifyEvidence_RejectsParaphrase(t *testing.T) {
	// The file is real and the bug being described is real, but the
	// "snippet" is the model's English description, not the code.
	// This is the core FP class the verifier is meant to catch.
	//
	// Note on matcher strictness: we match the snippet as a
	// SUBSTRING of the file window (whitespace-normalized). A
	// description that happens to contain the function name (e.g.
	// "ignored Close()") won't match because the full phrase isn't
	// in the window — only the identifier is, and the surrounding
	// English text isn't.
	root, rel := writeTestFile(t, "io.go", `_ = stream.Close()`+"\n")
	f := state.DeepFinding{
		File:            rel,
		Lines:           "1",
		EvidenceSnippet: "the returned error is discarded by the assignment to underscore",
	}
	if got := verifyEvidence(root, f); got != evidenceMismatch {
		t.Errorf("paraphrase masquerading as snippet must mismatch, got %v", got)
	}
}

// TestVerifyEvidence_RejectsPartialOverlap demonstrates the
// substring-match contract: a snippet that shares ONE token with the
// file but is otherwise different (so it's clearly not from the file)
// must be rejected. Without this we'd accept any description that
// happened to name a function in the file.
func TestVerifyEvidence_RejectsPartialOverlap(t *testing.T) {
	root, rel := writeTestFile(t, "io.go", "_ = stream.Close()\n")
	f := state.DeepFinding{
		File:            rel,
		Lines:           "1",
		EvidenceSnippet: "stream.Close() result captured and processed via errgroup",
	}
	if got := verifyEvidence(root, f); got != evidenceMismatch {
		t.Errorf("snippet that shares only one token must mismatch, got %v", got)
	}
}

func TestVerifyEvidence_AcceptsWithinTolerance(t *testing.T) {
	// Model cites line 20, real match is on line 25. Within ±10 → OK.
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		if i == 25 {
			b.WriteString("\tdoTheRiskyThing()\n")
		} else {
			b.WriteString("\t// filler\n")
		}
	}
	root, rel := writeTestFile(t, "x.go", b.String())
	f := state.DeepFinding{
		File:            rel,
		Lines:           "20",
		EvidenceSnippet: "doTheRiskyThing()",
	}
	if got := verifyEvidence(root, f); got != evidenceOK {
		t.Errorf("match within ±10 tolerance must accept, got %v", got)
	}
}

func TestVerifyEvidence_RejectsOutsideTolerance(t *testing.T) {
	// Model cites line 5, real match is at line 100. Way outside ±10.
	var b strings.Builder
	for i := 1; i <= 120; i++ {
		if i == 100 {
			b.WriteString("\tdoTheRiskyThing()\n")
		} else {
			b.WriteString("\t// filler\n")
		}
	}
	root, rel := writeTestFile(t, "x.go", b.String())
	f := state.DeepFinding{
		File:            rel,
		Lines:           "5",
		EvidenceSnippet: "doTheRiskyThing()",
	}
	if got := verifyEvidence(root, f); got != evidenceMismatch {
		t.Errorf("match outside tolerance must reject (this is the F-045 class), got %v", got)
	}
}

func TestVerifyEvidence_EmptySnippetAccepted(t *testing.T) {
	// No snippet at all → not a verification target. We don't
	// punish the model for omitting a snippet here; recheck handles
	// vague evidence via a separate path.
	root, rel := writeTestFile(t, "x.go", "package x\n")
	f := state.DeepFinding{File: rel, Lines: "1", EvidenceSnippet: ""}
	if got := verifyEvidence(root, f); got != evidenceOK {
		t.Errorf("missing snippet must produce evidenceOK, got %v", got)
	}
}

func TestVerifyEvidence_WhitespaceOnlySnippetAccepted(t *testing.T) {
	// Pure-whitespace snippets are functionally missing; treat them
	// the same so a model that emits `"   "` doesn't get a finding
	// dropped for what is effectively a parse artifact.
	root, rel := writeTestFile(t, "x.go", "package x\n")
	f := state.DeepFinding{File: rel, Lines: "1", EvidenceSnippet: "   \t\n  "}
	if got := verifyEvidence(root, f); got != evidenceOK {
		t.Errorf("whitespace-only snippet must produce evidenceOK, got %v", got)
	}
}

func TestVerifyEvidence_FileMissing(t *testing.T) {
	root := t.TempDir() // empty dir
	f := state.DeepFinding{
		File:            "nope/not_real.go",
		Lines:           "1-10",
		EvidenceSnippet: "anything",
	}
	if got := verifyEvidence(root, f); got != evidenceFileMissing {
		t.Errorf("missing file must produce evidenceFileMissing, got %v", got)
	}
}

func TestVerifyEvidence_EmptyFilePath(t *testing.T) {
	// A finding with an empty file path is malformed — there's
	// nothing to verify against. Treat as a file-missing case so
	// downstream handles it the same as a hallucinated path.
	f := state.DeepFinding{
		File:            "",
		Lines:           "1",
		EvidenceSnippet: "x",
	}
	if got := verifyEvidence(t.TempDir(), f); got != evidenceFileMissing {
		t.Errorf("empty file path must produce evidenceFileMissing, got %v", got)
	}
}

func TestVerifyEvidence_OversizeFileSkipped(t *testing.T) {
	// Files above the size cap are not verified — but we accept,
	// not reject. The verifier exists to catch hallucinations, not
	// to be a cost liability on generated code.
	root := t.TempDir()
	huge := make([]byte, evidenceFileSizeLimit+1024)
	for i := range huge {
		huge[i] = 'x'
	}
	path := filepath.Join(root, "huge.gen.go")
	if err := os.WriteFile(path, huge, 0o644); err != nil {
		t.Fatalf("write huge: %v", err)
	}
	f := state.DeepFinding{
		File:            "huge.gen.go",
		Lines:           "1",
		EvidenceSnippet: "anything that does not appear",
	}
	got := verifyEvidence(root, f)
	if got != evidenceFileUnreadable {
		t.Errorf("oversize file must produce evidenceFileUnreadable (accept), got %v", got)
	}
}

func TestVerifyEvidence_CitedLinePastEOF(t *testing.T) {
	root, rel := writeTestFile(t, "tiny.go", "package x\n") // 1 line
	f := state.DeepFinding{
		File:            rel,
		Lines:           "500",
		EvidenceSnippet: "package x",
	}
	if got := verifyEvidence(root, f); got != evidenceMismatch {
		t.Errorf("line cite past EOF must reject (window starts past file), got %v", got)
	}
}

func TestVerifyEvidence_NoLineRangeFallsBackToFullFile(t *testing.T) {
	// Some models emit lines:"" or omit lines for systemic findings.
	// In that case we search the whole file rather than giving up —
	// the snippet is still a meaningful claim about the file's
	// contents.
	root, rel := writeTestFile(t, "x.go", "line1\nline2\nfoundIt()\nline4\n")
	f := state.DeepFinding{
		File:            rel,
		Lines:           "",
		EvidenceSnippet: "foundIt()",
	}
	if got := verifyEvidence(root, f); got != evidenceOK {
		t.Errorf("snippet that exists anywhere in file should match when lines is empty, got %v", got)
	}
}

// ── normalizeWhitespace ─────────────────────────────────────────────────

func TestNormalizeWhitespace_CollapsesRuns(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"a\t\tb", "a b"},
		{"a\n\nb", "a b"},
		{"  leading", "leading"},
		{"trailing  ", "trailing"},
		{"mixed \t \n\rspace", "mixed space"},
		{"", ""},
		{"     ", ""},
	}
	for _, c := range cases {
		got := normalizeWhitespace(c.in)
		if got != c.want {
			t.Errorf("normalizeWhitespace(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── parseFindingLineRange ──────────────────────────────────────────────

func TestParseFindingLineRange(t *testing.T) {
	cases := []struct {
		in              string
		wantStart, wantEnd int
	}{
		{"45", 45, 45},
		{"45-62", 45, 62},
		{"L45-L62", 45, 62},
		{"l45-l62", 45, 62},
		{" 45 - 62 ", 45, 62},
		{"45,62", 45, 62},
		{"", 0, 0},
		{"abc", 0, 0},
		{"45-abc", 0, 0},
	}
	for _, c := range cases {
		gotStart, gotEnd := parseFindingLineRange(c.in)
		if gotStart != c.wantStart || gotEnd != c.wantEnd {
			t.Errorf("parseFindingLineRange(%q) = (%d,%d), want (%d,%d)",
				c.in, gotStart, gotEnd, c.wantStart, c.wantEnd)
		}
	}
}

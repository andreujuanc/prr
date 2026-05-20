package review

import (
	"context"
	"strings"
	"testing"
)

// pinBatchResponse returns a JSON response in the shape the fallback
// (directory-batch) prompt produces: an array of {file, purpose,
// findings: BatchFinding[]}.
func pinBatchResponse(file string) string {
	return `[
  {
    "file": "` + file + `",
    "purpose": "test",
    "findings": [
      {
        "severity": "high",
        "confidence_score": 80,
        "confidence_reasoning": "clear missing check",
        "dimension": "security",
        "title": "Missing input validation",
        "line": 42,
        "detail": "the handler does not validate the request body",
        "suggestion": "add validation before persisting"
      }
    ]
  }
]`
}

// TestConvertFallbackToDeepFindings pins the BatchFinding -> DeepFinding
// adapter. Field mapping is the contract recheck and synthesis depend
// on: severity/title/description/suggestion must survive, and the
// Lines string is computed from BatchFinding.Line.
func TestConvertFallbackToDeepFindings(t *testing.T) {
	parsed := ParseBatchResult(pinBatchResponse("a.go"))
	if parsed == nil {
		t.Fatal("ParseBatchResult returned nil for canned response")
	}

	out := convertFallbackToDeepFindings(parsed)
	if len(out) != 1 {
		t.Fatalf("expected 1 DeepFinding; got %d", len(out))
	}
	f := out[0]
	if f.File != "a.go" {
		t.Errorf("File = %q, want a.go", f.File)
	}
	if f.Lines != "42" {
		t.Errorf("Lines = %q, want 42", f.Lines)
	}
	if f.Severity != "high" {
		t.Errorf("Severity = %q, want high", f.Severity)
	}
	if f.Title != "Missing input validation" {
		t.Errorf("Title = %q", f.Title)
	}
	if !strings.Contains(f.Description, "does not validate") {
		t.Errorf("Description missing original detail; got %q", f.Description)
	}
	if !strings.Contains(f.Suggestion, "add validation") {
		t.Errorf("Suggestion missing original text; got %q", f.Suggestion)
	}
	if f.ConfidenceScore != 80 {
		t.Errorf("ConfidenceScore = %d, want 80", f.ConfidenceScore)
	}
	if f.Dimension != "security" {
		t.Errorf("Dimension = %q, want security", f.Dimension)
	}
}

// TestRunReviewCalls_FallbackBatchProducesDeepFindings runs a single
// fallback-batch call through the unified executor and asserts the
// returned findings are DeepFinding-shaped — the property that lets
// recheck and synthesis treat all findings the same way.
func TestRunReviewCalls_FallbackBatchProducesDeepFindings(t *testing.T) {
	call := ReviewCall{
		Type:      "fallback-batch",
		Category:  "internal/ui",
		Files:     []string{"a.go"},
		FileDiffs: map[string]string{"a.go": "@@ -1,1 +1,1 @@\n-old\n+new\n"},
	}

	client := &fakeAIClient{
		Responder: func(_, _ string) string { return pinBatchResponse("a.go") },
	}

	opts := ExecuteOptions{
		Mode:               ModePR,
		PRMeta:             "PR #1: test",
		ProjectContext:     "",
		CustomInstructions: "",
		SkipEvidenceVerify: true, // no real source tree
		// RepoRoot deliberately empty so the AOI evidence verifier
		// stays skipped — that path expects DeepFindings with
		// EvidenceSnippet, which fallback findings don't carry.
	}

	res, err := RunReviewCalls(context.Background(), client, []ReviewCall{call}, opts)
	if err != nil {
		t.Fatalf("RunReviewCalls: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(res.Findings))
	}
	if res.Findings[0].File != "a.go" {
		t.Errorf("finding File = %q", res.Findings[0].File)
	}
	if res.Findings[0].Severity != "high" {
		t.Errorf("finding Severity = %q", res.Findings[0].Severity)
	}
}

// TestAssembleFallbackDiffs pins the input shape passed to the batch
// prompt: per-file diff sections separated by "=== path ===" headers,
// matching what BuildBatches originally produced.
func TestAssembleFallbackDiffs(t *testing.T) {
	call := ReviewCall{
		Type:  "fallback-batch",
		Files: []string{"a.go", "b.go"},
		FileDiffs: map[string]string{
			"a.go": "diff for a",
			"b.go": "diff for b",
		},
	}
	got := assembleFallbackDiffs(call)
	for _, want := range []string{"=== a.go ===", "diff for a", "=== b.go ===", "diff for b"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in assembled diff; got:\n%s", want, got)
		}
	}
}

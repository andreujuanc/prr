package ai

import (
	"testing"
)

func TestParseReviewOutput_ValidJSON(t *testing.T) {
	raw := `{
		"summary": "This PR adds a greeting utility.",
		"verdict": "approve",
		"findings": [
			{
				"severity": "medium",
				"category": "testing",
				"file": "greet.go",
				"line": 10,
				"title": "Missing test for empty name",
				"detail": "The greet function doesn't handle empty strings.",
				"suggestion": "Add a test case for greet(\"\")"
			},
			{
				"severity": "critical",
				"category": "bug",
				"file": "main.go",
				"line": 5,
				"title": "Nil pointer dereference",
				"detail": "Config may be nil here.",
				"suggestion": "Add nil check"
			}
		],
		"missing_tests": ["empty name handling"],
		"questions_for_author": ["Why was the old greeting removed?"]
	}`

	result := ParseReviewOutput(raw)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Summary != "This PR adds a greeting utility." {
		t.Errorf("summary = %q", result.Summary)
	}
	if result.Verdict != "approve" {
		t.Errorf("verdict = %q", result.Verdict)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(result.Findings))
	}
	// Should be sorted: critical first, then medium
	if result.Findings[0].Severity != "critical" {
		t.Errorf("first finding should be critical, got %q", result.Findings[0].Severity)
	}
	if result.Findings[1].Severity != "medium" {
		t.Errorf("second finding should be medium, got %q", result.Findings[1].Severity)
	}
	if len(result.MissingTests) != 1 {
		t.Errorf("expected 1 missing test, got %d", len(result.MissingTests))
	}
	if len(result.QuestionsForAuthor) != 1 {
		t.Errorf("expected 1 question, got %d", len(result.QuestionsForAuthor))
	}
}

func TestParseReviewOutput_MarkdownFenced(t *testing.T) {
	raw := "```json\n" + `{
		"summary": "Clean PR.",
		"verdict": "approve",
		"findings": [],
		"missing_tests": [],
		"questions_for_author": []
	}` + "\n```"

	result := ParseReviewOutput(raw)
	if result == nil {
		t.Fatal("expected non-nil result from fenced JSON")
	}
	if result.Verdict != "approve" {
		t.Errorf("verdict = %q", result.Verdict)
	}
}

func TestParseReviewOutput_ProseWrapped(t *testing.T) {
	raw := `Here is my review:

{
	"summary": "Good changes.",
	"verdict": "comment",
	"findings": [],
	"missing_tests": [],
	"questions_for_author": []
}

That's my assessment.`

	result := ParseReviewOutput(raw)
	if result == nil {
		t.Fatal("expected non-nil result from prose-wrapped JSON")
	}
	if result.Verdict != "comment" {
		t.Errorf("verdict = %q", result.Verdict)
	}
}

func TestParseReviewOutput_VerdictNormalization(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"approve", "approve"},
		{"Approve", "approve"},
		{"APPROVE", "approve"},
		{"approve with suggestions", "approve"},
		{"request_changes", "request_changes"},
		{"Request Changes", "request_changes"},
		{"comment", "comment"},
	}

	for _, tt := range tests {
		raw := `{"summary":"test","verdict":"` + tt.input + `"}`
		result := ParseReviewOutput(raw)
		if result == nil {
			t.Fatalf("expected non-nil result for verdict %q", tt.input)
		}
		if result.Verdict != tt.expected {
			t.Errorf("verdict %q → %q, want %q", tt.input, result.Verdict, tt.expected)
		}
	}
}

func TestParseReviewOutput_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"no json", "This is just prose without any JSON."},
		{"no summary or verdict", `{"findings":[]}`},
		{"malformed json", `{summary: "bad"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseReviewOutput(tt.input)
			if result != nil {
				t.Errorf("expected nil for %q, got %+v", tt.name, result)
			}
		})
	}
}

func TestParseReviewOutput_MultiRoundText(t *testing.T) {
	// Simulates ChatStream output where the agent emits prose across
	// multiple tool-calling rounds before producing the final JSON.
	raw := `I'll verify the findings by reading the relevant files.

Let me check the error handling in config.go...

The code at line 42 does handle nil correctly, so finding #3 is a false positive.

Now let me produce the final review:

{
	"summary": "Solid PR with one correctness issue.",
	"verdict": "request_changes",
	"findings": [
		{
			"severity": "high",
			"category": "bug",
			"file": "server.go",
			"line": 88,
			"title": "Race condition on shared map",
			"detail": "The connections map is accessed without synchronization.",
			"suggestion": "Use sync.RWMutex to protect concurrent access."
		}
	],
	"missing_tests": [],
	"questions_for_author": []
}`

	result := ParseReviewOutput(raw)
	if result == nil {
		t.Fatal("expected non-nil result from multi-round text")
	}
	if result.Verdict != "request_changes" {
		t.Errorf("verdict = %q, want request_changes", result.Verdict)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result.Findings))
	}
	if result.Findings[0].Title != "Race condition on shared map" {
		t.Errorf("finding title = %q", result.Findings[0].Title)
	}
}

func TestParseReviewOutput_MultipleJSONObjects(t *testing.T) {
	// When multiple JSON objects appear (e.g. tool results + final review),
	// the parser should use the last valid ReviewOutput.
	raw := `{"status":"ok","file":"config.go","lines":50}

After verifying, here is the review:

{"summary":"Clean PR.","verdict":"approve","findings":[],"missing_tests":[],"questions_for_author":[]}`

	result := ParseReviewOutput(raw)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Verdict != "approve" {
		t.Errorf("verdict = %q, want approve", result.Verdict)
	}
}

func TestParseReviewOutput_FencedInMiddle(t *testing.T) {
	// Code fence appearing in the middle of multi-round output.
	raw := "I verified the findings. Here is my review:\n\n```json\n" +
		`{"summary":"Good changes.","verdict":"comment","findings":[],"missing_tests":[],"questions_for_author":[]}` +
		"\n```\n\nThat concludes my review."

	result := ParseReviewOutput(raw)
	if result == nil {
		t.Fatal("expected non-nil result from mid-text fenced JSON")
	}
	if result.Verdict != "comment" {
		t.Errorf("verdict = %q, want comment", result.Verdict)
	}
}

func TestParseReviewOutput_RealWorldMultiRound(t *testing.T) {
	// Real-world synthesis output: multi-round prose followed by fenced JSON.
	raw := `The Cognito filter uses string interpolation. Let me check if email is validated upstream.

Now let me finalize the review based on verified findings.

` + "```json" + `
{
  "summary": "Large PR introducing identity service.",
  "verdict": "request_changes",
  "findings": [
    {
      "severity": "high",
      "category": "security",
      "file": "authorizer.zig",
      "line": 45,
      "title": "JSON injection via unescaped header",
      "detail": "The PIN header is interpolated without escaping.",
      "suggestion": "Use proper JSON serialization."
    }
  ],
  "missing_tests": [],
  "questions_for_author": ["Is this intended for production?"]
}
` + "```"

	result := ParseReviewOutput(raw)
	if result == nil {
		t.Fatal("expected non-nil result from real-world multi-round output")
	}
	if result.Verdict != "request_changes" {
		t.Errorf("verdict = %q, want request_changes", result.Verdict)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result.Findings))
	}
}

func TestParseReviewOutput_NilArraysNormalized(t *testing.T) {
	raw := `{"summary":"test","verdict":"approve"}`
	result := ParseReviewOutput(raw)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Findings == nil {
		t.Error("Findings should be non-nil empty slice")
	}
	if result.MissingTests == nil {
		t.Error("MissingTests should be non-nil empty slice")
	}
	if result.QuestionsForAuthor == nil {
		t.Error("QuestionsForAuthor should be non-nil empty slice")
	}
}

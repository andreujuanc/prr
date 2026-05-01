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

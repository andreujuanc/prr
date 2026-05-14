package pipe

import (
	"strings"
	"testing"
)

func TestFormatAsMarkdown_Basic(t *testing.T) {
	p := Payload{
		Title:    "SQL Injection",
		Severity: "critical",
		Category: "security",
		File:     "handler.go",
		Line:     42,
		Detail:   "User input concatenated into SQL query.",
	}
	out := formatAsMarkdown(p)
	if !strings.Contains(out, "# Review Finding: SQL Injection") {
		t.Error("expected title header")
	}
	if !strings.Contains(out, "**Severity:** critical") {
		t.Error("expected severity")
	}
	if !strings.Contains(out, "**Category:** security") {
		t.Error("expected category")
	}
	if !strings.Contains(out, "**File:** handler.go:42") {
		t.Error("expected file location")
	}
	if !strings.Contains(out, "## Detail") {
		t.Error("expected detail section")
	}
	if strings.Contains(out, "## Suggestion") {
		t.Error("should not have suggestion section when empty")
	}
}

func TestFormatAsMarkdown_WithSuggestion(t *testing.T) {
	p := Payload{
		Title:      "Issue",
		Severity:   "low",
		Category:   "style",
		File:       "main.go",
		Line:       1,
		Detail:     "Details here.",
		Suggestion: "Use parameterized queries.",
	}
	out := formatAsMarkdown(p)
	if !strings.Contains(out, "## Suggestion") {
		t.Error("expected suggestion section")
	}
	if !strings.Contains(out, "Use parameterized queries.") {
		t.Error("expected suggestion content")
	}
}

func TestFormatAsPlainText_Basic(t *testing.T) {
	p := Payload{
		Title:    "Buffer Overflow",
		Severity: "high",
		Category: "memory",
		File:     "buf.c",
		Line:     99,
		Detail:   "Unbounded write.",
	}
	out := formatAsPlainText(p)
	if !strings.Contains(out, "[high/memory] Buffer Overflow") {
		t.Error("expected severity/category prefix")
	}
	if !strings.Contains(out, "File: buf.c:99") {
		t.Error("expected file location")
	}
	if !strings.Contains(out, "Unbounded write.") {
		t.Error("expected detail")
	}
	if strings.Contains(out, "Suggestion:") {
		t.Error("should not have suggestion when empty")
	}
}

func TestFormatAsPlainText_WithSuggestion(t *testing.T) {
	p := Payload{
		Title:      "Issue",
		Severity:   "low",
		Category:   "style",
		File:       "main.go",
		Line:       1,
		Detail:     "Detail.",
		Suggestion: "Fix it.",
	}
	out := formatAsPlainText(p)
	if !strings.Contains(out, "Suggestion: Fix it.") {
		t.Error("expected suggestion")
	}
}

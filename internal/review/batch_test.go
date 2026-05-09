package review

import (
	"strings"
	"testing"
)

func TestParseBatchResult(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantNil bool
		wantLen int
	}{
		{
			name:    "valid JSON array",
			input:   `[{"file":"main.go","purpose":"entrypoint","findings":"none"}]`,
			wantLen: 1,
		},
		{
			name:    "wrapped in json fence",
			input:   "```json\n[{\"file\":\"main.go\",\"purpose\":\"entrypoint\",\"findings\":\"none\"}]\n```",
			wantLen: 1,
		},
		{
			name:    "wrapped in plain fence",
			input:   "```\n[{\"file\":\"a.go\",\"purpose\":\"p\",\"findings\":\"f\"},{\"file\":\"b.go\",\"purpose\":\"p2\",\"findings\":\"\"}]\n```",
			wantLen: 2,
		},
		{
			name:    "empty array",
			input:   "[]",
			wantLen: 0,
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantNil: true,
		},
		{
			name:    "whitespace around fences",
			input:   "  ```json\n[{\"file\":\"x.go\",\"purpose\":\"y\",\"findings\":\"z\"}]\n```  ",
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseBatchResult(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if len(got) != tt.wantLen {
				t.Errorf("expected %d results, got %d", tt.wantLen, len(got))
			}
		})
	}
}

func TestParseBatchResult_Fields(t *testing.T) {
	input := `[{"file":"src/main.go","purpose":"application entry point","findings":"Missing error handling on line 42"}]`
	results := ParseBatchResult(input)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.File != "src/main.go" {
		t.Errorf("File = %q, want %q", r.File, "src/main.go")
	}
	if r.Purpose != "application entry point" {
		t.Errorf("Purpose = %q, want %q", r.Purpose, "application entry point")
	}
	if got := r.Findings.Text(); got != "Missing error handling on line 42" {
		t.Errorf("Findings.Text() = %q, want %q", got, "Missing error handling on line 42")
	}
}

func TestParseBatchResult_StructuredFindings(t *testing.T) {
	input := `[{
		"file": "src/main.go",
		"purpose": "entry point",
		"findings": [
			{
				"severity": "high",
				"confidence": "high",
				"dimension": "correctness",
				"title": "Off-by-one in expiry",
				"line": 87,
				"detail": "exp <= now accepts an expired token.",
				"suggestion": "Use exp < now."
			}
		]
	}]`
	results := ParseBatchResult(input)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if len(r.Findings.Items) != 1 {
		t.Fatalf("expected 1 finding item, got %d", len(r.Findings.Items))
	}
	got := r.Findings.Items[0]
	if got.Severity != "high" || got.Line != 87 || got.Title != "Off-by-one in expiry" {
		t.Errorf("unexpected finding: %+v", got)
	}
	if r.Findings.IsEmpty() {
		t.Error("expected non-empty findings")
	}
	if !strings.Contains(r.Findings.Text(), "Off-by-one in expiry") {
		t.Errorf("Text() should include the title; got %q", r.Findings.Text())
	}
}

func TestBatchFindings_EmptyArray(t *testing.T) {
	input := `[{"file":"a.go","purpose":"p","findings":[]}]`
	results := ParseBatchResult(input)
	if len(results) != 1 {
		t.Fatalf("expected 1 result")
	}
	if !results[0].Findings.IsEmpty() {
		t.Error("expected empty findings")
	}
	if results[0].Findings.Text() != "" {
		t.Errorf("Text() should be empty; got %q", results[0].Findings.Text())
	}
}

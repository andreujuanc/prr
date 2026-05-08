package review

import (
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
	if r.Findings != "Missing error handling on line 42" {
		t.Errorf("Findings = %q, want %q", r.Findings, "Missing error handling on line 42")
	}
}

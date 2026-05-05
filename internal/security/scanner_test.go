package security

import (
	"testing"
)

func TestParseAOIResult(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
		wantErr bool
	}{
		{
			name: "valid JSON array",
			input: `[
				{
					"file": "main.go",
					"risk_level": "high",
					"risk_summary": "Contains exec.Command with user input",
					"areas_of_interest": [
						{
							"file": "main.go",
							"line": 42,
							"end_line": 45,
							"category": "exec",
							"snippet": "exec.Command(userInput)",
							"reasoning": "Command execution with user-controlled input",
							"confidence": "high"
						}
					]
				}
			]`,
			wantLen: 1,
		},
		{
			name: "wrapped in markdown fences",
			input: "```json\n" + `[{"file":"a.go","risk_level":"none","risk_summary":"clean","areas_of_interest":[]}]` + "\n```",
			wantLen: 1,
		},
		{
			name: "with leading prose",
			input: `Here are the results:
			[{"file":"b.go","risk_level":"low","risk_summary":"minor","areas_of_interest":[]}]`,
			wantLen: 1,
		},
		{
			name:    "no JSON",
			input:   "No security issues found.",
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			input:   `[{"file": broken}]`,
			wantErr: true,
		},
		{
			name: "JSON with literal tabs in strings",
			input: "[{\"file\":\"a.go\",\"risk_level\":\"high\",\"risk_summary\":\"risky\",\"areas_of_interest\":[" +
				"{\"file\":\"a.go\",\"line\":1,\"category\":\"sql\",\"snippet\":\"db.Query(s)\tWHERE x\",\"reasoning\":\"raw\\tSQL\",\"confidence\":\"high\"}" +
				"]}]",
			wantLen: 1,
		},
		{
			name: "multiple files",
			input: `[
				{"file":"a.go","risk_level":"high","risk_summary":"risky","areas_of_interest":[
					{"file":"a.go","line":1,"category":"sql","snippet":"db.Query(s)","reasoning":"raw SQL","confidence":"high"}
				]},
				{"file":"b.go","risk_level":"none","risk_summary":"clean","areas_of_interest":[]}
			]`,
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := parseAOIResult(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(results) != tt.wantLen {
				t.Errorf("got %d results, want %d", len(results), tt.wantLen)
			}
		})
	}
}

func TestParseRevalidationResult(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
		wantErr bool
	}{
		{
			name: "valid revalidation",
			input: `[
				{
					"finding_index": 0,
					"verdict": "true-positive",
					"reasoning": "The SQL query uses string concatenation",
					"confidence": "high",
					"cwe": "CWE-89"
				},
				{
					"finding_index": 1,
					"verdict": "false-positive",
					"reasoning": "Input is validated by middleware",
					"confidence": "high",
					"cwe": ""
				}
			]`,
			wantLen: 2,
		},
		{
			name:    "no JSON",
			input:   "Nothing to report",
			wantErr: true,
		},
		{
			name: "wrapped in fences",
			input: "```json\n" + `[{"finding_index":0,"verdict":"fixed","reasoning":"patched","confidence":"medium"}]` + "\n```",
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := parseRevalidationResult(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(results) != tt.wantLen {
				t.Errorf("got %d results, want %d", len(results), tt.wantLen)
			}
		})
	}
}

func TestBuildReport(t *testing.T) {
	results := []AOIScanResult{
		{
			File:      "auth.go",
			RiskLevel: "critical",
			RiskSummary: "Authentication handler with SQL injection",
			AreasOfInterest: []AreaOfInterest{
				{File: "auth.go", Line: 10, Category: "sql", Snippet: "db.Query(q)", Reasoning: "raw SQL", Confidence: "high"},
				{File: "auth.go", Line: 20, Category: "auth", Snippet: "if admin", Reasoning: "auth check", Confidence: "medium"},
			},
		},
		{
			File:            "util.go",
			RiskLevel:       "none",
			RiskSummary:     "Utility functions, no security concerns",
			AreasOfInterest: nil,
		},
		{
			File:      "api.go",
			RiskLevel: "high",
			RiskSummary: "API endpoint with SSRF potential",
			AreasOfInterest: []AreaOfInterest{
				{File: "api.go", Line: 5, Category: "network", Snippet: "http.Get(url)", Reasoning: "SSRF", Confidence: "high"},
			},
		},
	}

	report := buildReport(results)

	if report.OverallRisk != "critical" {
		t.Errorf("overall risk = %q, want %q", report.OverallRisk, "critical")
	}
	if report.TotalAOIs != 3 {
		t.Errorf("total AOIs = %d, want %d", report.TotalAOIs, 3)
	}
	if len(report.HighRiskFiles) != 2 {
		t.Errorf("high risk files = %d, want %d", len(report.HighRiskFiles), 2)
	}
	if report.SecurityDigest == "" {
		t.Error("security digest should not be empty")
	}

	// Verify digest contains key information
	digest := report.SecurityDigest
	if !containsStr(digest, "3 Areas of Interest") {
		t.Error("digest should mention total AOIs")
	}
	if !containsStr(digest, "critical") {
		t.Error("digest should mention critical risk")
	}
	if !containsStr(digest, "auth.go") {
		t.Error("digest should mention high-risk file auth.go")
	}
}

func TestBuildReport_NoAOIs(t *testing.T) {
	results := []AOIScanResult{
		{File: "clean.go", RiskLevel: "none", AreasOfInterest: nil},
	}

	report := buildReport(results)

	if report.OverallRisk != "none" {
		t.Errorf("overall risk = %q, want %q", report.OverallRisk, "none")
	}
	if report.TotalAOIs != 0 {
		t.Errorf("total AOIs = %d, want %d", report.TotalAOIs, 0)
	}
	if report.SecurityDigest != "" {
		t.Error("security digest should be empty when no AOIs found")
	}
}

func TestBuildAOIBatches(t *testing.T) {
	rawDiffs := map[string]string{
		"internal/auth/handler.go": "diff content for handler",
		"internal/auth/middleware.go": "diff content for middleware",
		"main.go": "diff content for main",
		"go.sum":  "should be excluded",
		"vendor/lib/lib.go": "should be excluded",
	}

	batches := buildAOIBatches(rawDiffs)

	// go.sum and vendor/ should be excluded
	totalFiles := 0
	for _, b := range batches {
		totalFiles += len(b.files)
		for _, f := range b.files {
			if f == "go.sum" || f == "vendor/lib/lib.go" {
				t.Errorf("excluded file %q should not be in batches", f)
			}
		}
	}

	if totalFiles != 3 {
		t.Errorf("total files in batches = %d, want 3", totalFiles)
	}
}

func TestFormatDigest_ContainsCategories(t *testing.T) {
	results := []AOIScanResult{
		{
			File:      "a.go",
			RiskLevel: "high",
			AreasOfInterest: []AreaOfInterest{
				{File: "a.go", Line: 1, Category: "sql", Snippet: "q", Reasoning: "raw", Confidence: "high"},
				{File: "a.go", Line: 2, Category: "sql", Snippet: "q2", Reasoning: "raw2", Confidence: "medium"},
				{File: "a.go", Line: 3, Category: "auth", Snippet: "a", Reasoning: "check", Confidence: "low"},
			},
		},
	}

	report := buildReport(results)
	digest := report.SecurityDigest

	if !containsStr(digest, "sql: 2") {
		t.Error("digest should show sql category count")
	}
	if !containsStr(digest, "auth: 1") {
		t.Error("digest should show auth category count")
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

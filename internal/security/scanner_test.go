package security

import (
	"strings"
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
			name:    "wrapped in markdown fences",
			input:   "```json\n" + `[{"file":"a.go","risk_level":"none","risk_summary":"clean","areas_of_interest":[]}]` + "\n```",
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

func TestParseAOIResult_NewFormat(t *testing.T) {
	input := `[
		{
			"file": "internal/billing/charge.go",
			"risk_level": "high",
			"risk_summary": "Financial calculations with floating point",
			"areas": [
				{
					"id": "charge-go-float-currency",
					"line": 45,
					"end_line": 78,
					"category": "financial",
					"subcategory": "money-arithmetic",
					"urgency": "individual",
					"concern": "Currency conversion with floating point arithmetic",
					"context": "Multiplies amounts by exchange rates using float64",
					"dimensions": ["correctness", "financial"]
				},
				{
					"id": "charge-go-error-swallow",
					"line": 88,
					"end_line": 91,
					"category": "error-handling",
					"subcategory": "swallowed-errors",
					"urgency": "grouped",
					"concern": "Error from validateAmount() assigned to _",
					"context": "Validation error silently ignored before creating charge",
					"dimensions": ["error-handling"]
				}
			]
		}
	]`

	results, err := parseAOIResult(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	r := results[0]
	// Areas should be normalized into AreasOfInterest
	if len(r.AreasOfInterest) != 2 {
		t.Fatalf("got %d AOIs, want 2", len(r.AreasOfInterest))
	}
	if len(r.Areas) != 0 {
		t.Errorf("Areas should be nil after normalization, got %d", len(r.Areas))
	}

	aoi := r.AreasOfInterest[0]
	if aoi.ID != "charge-go-float-currency" {
		t.Errorf("got ID %q, want %q", aoi.ID, "charge-go-float-currency")
	}
	if aoi.Category != "financial" {
		t.Errorf("got Category %q, want %q", aoi.Category, "financial")
	}
	if aoi.Subcategory != "money-arithmetic" {
		t.Errorf("got Subcategory %q, want %q", aoi.Subcategory, "money-arithmetic")
	}
	if aoi.Urgency != "individual" {
		t.Errorf("got Urgency %q, want %q", aoi.Urgency, "individual")
	}
	if aoi.Concern != "Currency conversion with floating point arithmetic" {
		t.Errorf("got Concern %q", aoi.Concern)
	}
	if len(aoi.Dimensions) != 2 {
		t.Errorf("got %d dimensions, want 2", len(aoi.Dimensions))
	}
}

func TestParseAOIResult_LegacyFormatStillWorks(t *testing.T) {
	// Ensure old cached data still deserializes correctly
	input := `[{
		"file": "main.go",
		"risk_level": "high",
		"risk_summary": "risky",
		"areas_of_interest": [{
			"file": "main.go",
			"line": 42,
			"category": "sql",
			"snippet": "db.Query(s)",
			"reasoning": "raw SQL",
			"confidence": "high"
		}]
	}]`

	results, err := parseAOIResult(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results[0].AreasOfInterest) != 1 {
		t.Fatalf("got %d AOIs, want 1", len(results[0].AreasOfInterest))
	}

	aoi := results[0].AreasOfInterest[0]
	if aoi.Category != "sql" {
		t.Errorf("got Category %q, want %q", aoi.Category, "sql")
	}
	if aoi.Snippet != "db.Query(s)" {
		t.Errorf("got Snippet %q, want %q", aoi.Snippet, "db.Query(s)")
	}
	if aoi.Reasoning != "raw SQL" {
		t.Errorf("got Reasoning %q, want %q", aoi.Reasoning, "raw SQL")
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
			name:    "wrapped in fences",
			input:   "```json\n" + `[{"finding_index":0,"verdict":"fixed","reasoning":"patched","confidence":"medium"}]` + "\n```",
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
			File: "auth.go",
			AreasOfInterest: []AreaOfInterest{
				{File: "auth.go", Line: 10, Category: "sql", Snippet: "db.Query(q)", Reasoning: "raw SQL", Confidence: "high"},
				{File: "auth.go", Line: 20, Category: "auth", Snippet: "if admin", Reasoning: "auth check", Confidence: "medium"},
			},
		},
		{
			File:            "util.go",
			AreasOfInterest: nil,
		},
		{
			File: "api.go",
			AreasOfInterest: []AreaOfInterest{
				{File: "api.go", Line: 5, Category: "network", Snippet: "http.Get(url)", Reasoning: "SSRF", Confidence: "high"},
			},
		},
	}

	report := buildReport(results)

	if report.TotalAOIs != 3 {
		t.Errorf("total AOIs = %d, want %d", report.TotalAOIs, 3)
	}
	if report.SecurityDigest == "" {
		t.Error("security digest should not be empty")
	}

	// Verify digest contains key information
	digest := report.SecurityDigest
	if !containsStr(digest, "3 Areas of Interest") {
		t.Error("digest should mention total AOIs")
	}
	if !containsStr(digest, "auth.go") {
		t.Error("digest should mention file auth.go")
	}
}

func TestBuildReport_NoAOIs(t *testing.T) {
	results := []AOIScanResult{
		{File: "clean.go", AreasOfInterest: nil},
	}

	report := buildReport(results)

	if report.TotalAOIs != 0 {
		t.Errorf("total AOIs = %d, want %d", report.TotalAOIs, 0)
	}
	if report.SecurityDigest != "" {
		t.Error("security digest should be empty when no AOIs found")
	}
}

func TestBuildAOIBatches(t *testing.T) {
	rawDiffs := map[string]string{
		"internal/auth/handler.go":    "diff content for handler",
		"internal/auth/middleware.go": "diff content for middleware",
		"main.go":                     "diff content for main",
		"go.sum":                      "should be excluded",
		"vendor/lib/lib.go":           "should be excluded",
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

func TestDimensionKey(t *testing.T) {
	tests := []struct {
		name string
		dims []string
		want string
	}{
		{"nil dims", nil, "_all_"},
		{"empty dims", []string{}, "_all_"},
		{"single dim", []string{"testing"}, "testing"},
		{"multiple dims sorted", []string{"a", "b", "c"}, "a,b,c"},
		{"multiple dims unsorted", []string{"c", "a", "b"}, "a,b,c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dimensionKey(tt.dims)
			if got != tt.want {
				t.Errorf("dimensionKey(%v) = %q, want %q", tt.dims, got, tt.want)
			}
		})
	}
}

func TestBuildAOIBatchesClassified_GroupsByDimensions(t *testing.T) {
	rawDiffs := map[string]string{
		"handler.go":      "handler code",
		"handler_test.go": "test code",
		"repo.go":         "repo code",
	}

	fileDimensions := map[string][]string{
		"handler.go":      {"input-validation", "error-handling"},
		"handler_test.go": {"testing", "correctness"},
		"repo.go":         {"input-validation", "error-handling"}, // same as handler.go
	}

	batches := buildAOIBatchesClassified(rawDiffs, fileDimensions, false)

	// handler.go and repo.go share dimensions, so they should be in the same batch
	// handler_test.go has different dimensions, so it should be in a separate batch
	if len(batches) != 2 {
		t.Fatalf("got %d batches, want 2", len(batches))
	}

	// Find which batch has the test file
	var testBatch, handlerBatch *aoiBatch
	for i := range batches {
		for _, f := range batches[i].files {
			if f == "handler_test.go" {
				testBatch = &batches[i]
			}
			if f == "handler.go" {
				handlerBatch = &batches[i]
			}
		}
	}

	if testBatch == nil {
		t.Fatal("test file batch not found")
	}
	if handlerBatch == nil {
		t.Fatal("handler file batch not found")
	}

	if len(testBatch.files) != 1 {
		t.Errorf("test batch has %d files, want 1", len(testBatch.files))
	}
	if len(handlerBatch.files) != 2 {
		t.Errorf("handler batch has %d files, want 2 (handler.go + repo.go)", len(handlerBatch.files))
	}

	// Verify dimensions are attached to batches
	if len(testBatch.dimensions) != 2 {
		t.Errorf("test batch has %d dimensions, want 2", len(testBatch.dimensions))
	}
	if len(handlerBatch.dimensions) != 2 {
		t.Errorf("handler batch has %d dimensions, want 2", len(handlerBatch.dimensions))
	}
}

func TestBuildAOIBatchesClassified_NilDimensions(t *testing.T) {
	rawDiffs := map[string]string{
		"a.go": "code a",
		"b.go": "code b",
	}

	// nil fileDimensions — all files should end up in same batch (all dims)
	batches := buildAOIBatchesClassified(rawDiffs, nil, false)

	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	if len(batches[0].files) != 2 {
		t.Errorf("batch has %d files, want 2", len(batches[0].files))
	}
	if batches[0].dimensions != nil {
		t.Errorf("batch dimensions should be nil, got %v", batches[0].dimensions)
	}
}

func TestBuildAOIBatchesClassified_ExcludesFiles(t *testing.T) {
	rawDiffs := map[string]string{
		"main.go": "code",
		"go.sum":  "lock file",
	}

	batches := buildAOIBatchesClassified(rawDiffs, nil, false)

	totalFiles := 0
	for _, b := range batches {
		for _, f := range b.files {
			if f == "go.sum" {
				t.Error("go.sum should be excluded")
			}
			totalFiles++
		}
	}
	if totalFiles != 1 {
		t.Errorf("got %d files, want 1", totalFiles)
	}
}

func TestBuildAOIScanPromptWithDimensions(t *testing.T) {
	// With specific dimensions — prompt should contain those dimensions only
	prompt := buildAOIScanPromptWithDimensions(true, []string{"testing"})
	if !containsSubstring(prompt, "TESTING") {
		t.Error("prompt should contain TESTING dimension")
	}
	// Should NOT contain unrelated dimensions
	if containsSubstring(prompt, "CRYPTOGRAPHY") {
		t.Error("prompt should not contain CRYPTOGRAPHY when only testing is specified")
	}

	// With nil dimensions — prompt should contain all dimensions
	promptAll := buildAOIScanPromptWithDimensions(true, nil)
	if !containsSubstring(promptAll, "TESTING") {
		t.Error("prompt with nil dims should contain TESTING")
	}
	if !containsSubstring(promptAll, "CRYPTOGRAPHY") {
		t.Error("prompt with nil dims should contain CRYPTOGRAPHY")
	}

	// Audit mode rules
	if !containsSubstring(prompt, "full-project audit") {
		t.Error("audit mode prompt should contain audit rules")
	}

	// PR mode rules
	promptPR := buildAOIScanPromptWithDimensions(false, []string{"testing"})
	if !containsSubstring(promptPR, "DIFF") {
		t.Error("PR mode prompt should contain diff rules")
	}

	// Slug list — narrowed when dims is given.
	if !containsSubstring(prompt, "testing") {
		t.Error("prompt should list the testing slug")
	}
	if containsSubstring(prompt, "cryptography,") || containsSubstring(prompt, ", cryptography") {
		t.Error("prompt should not list cryptography when only testing is specified")
	}

	// Slug list — full list when dims is nil. Spot-check a few that should
	// be present.
	for _, slug := range []string{"correctness", "error-handling", "cryptography", "testing"} {
		if !containsSubstring(promptAll, slug) {
			t.Errorf("prompt with nil dims should list slug %q", slug)
		}
	}

	// Rule about not inventing names.
	if !containsSubstring(prompt, "Do not invent new dimension names") {
		t.Error("prompt should contain the no-invented-names rule")
	}
}

func TestFormatDigest_ContainsCategories(t *testing.T) {
	results := []AOIScanResult{
		{
			File: "a.go",
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

func TestPrefixLineNumbers(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "single line, no trailing newline",
			in:   "package main",
			want: "1: package main",
		},
		{
			name: "trailing newline is preserved",
			in:   "a\nb\n",
			want: "1: a\n2: b\n",
		},
		{
			name: "no trailing newline",
			in:   "a\nb",
			want: "1: a\n2: b",
		},
		{
			name: "blank lines keep their numbers",
			in:   "a\n\nc\n",
			want: "1: a\n2: \n3: c\n",
		},
		{
			name: "width pads when count crosses powers of ten",
			in:   strings.Repeat("x\n", 12),
			// 12 lines → width 2. First line should be " 1:", last "12:".
			want: " 1: x\n 2: x\n 3: x\n 4: x\n 5: x\n 6: x\n 7: x\n 8: x\n 9: x\n10: x\n11: x\n12: x\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prefixLineNumbers(tc.in)
			if got != tc.want {
				t.Errorf("prefixLineNumbers(%q):\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
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

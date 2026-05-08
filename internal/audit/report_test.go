package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

func sampleResult() *Result {
	return &Result{
		FilesScanned:      42,
		AOIsGenerated:     128,
		ReviewCalls:       20,
		IndividualReviews: 12,
		GroupedReviews:    8,
		Findings: []state.DeepFinding{
			{
				AOIID:       "aoi-1",
				File:        "handler.go",
				Lines:       "10-15",
				Severity:    "critical",
				Category:    "input-validation",
				Subcategory: "sql-injection",
				Dimension:   "security",
				Title:       "SQL injection in user query",
				Description: "User input concatenated into SQL",
				Trigger:     "User input concatenated directly into SQL string",
				Suggestion:  "Use parameterized queries",
			},
			{
				AOIID:       "aoi-2",
				File:        "auth.go",
				Lines:       "44-50",
				Severity:    "high",
				Category:    "authentication",
				Dimension:   "security",
				Title:       "Missing token expiry check",
				Description: "Token is never validated for expiry",
				Trigger:     "No expiry validation on JWT",
			},
			{
				AOIID:       "aoi-3",
				File:        "api.go",
				Lines:       "20-25",
				Severity:    "critical",
				Category:    "input-validation",
				Dimension:   "security",
				Title:       "Command injection via exec",
				Description: "Unsanitized input passed to exec",
				Trigger:     "os/exec call with user input",
				Suggestion:  "Sanitize input before exec",
			},
		},
		Dismissals: 113,
		CrossCuttingObservations: []string{
			"Pattern of missing input validation across HTTP handlers",
			"Inconsistent error handling in middleware",
		},
	}
}

func TestExportJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")

	if err := ExportJSON(sampleResult(), path); err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var report ReportJSON
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if report.FilesScanned != 42 {
		t.Errorf("FilesScanned = %d, want 42", report.FilesScanned)
	}
	if len(report.Findings) != 3 {
		t.Errorf("Findings = %d, want 3", len(report.Findings))
	}
	if report.Dismissals != 113 {
		t.Errorf("Dismissals = %d, want 113", report.Dismissals)
	}
	if report.ReviewCalls != 20 {
		t.Errorf("ReviewCalls = %d, want 20", report.ReviewCalls)
	}
}

func TestExportMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")

	if err := ExportMarkdown(sampleResult(), path); err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	md := string(data)

	for _, want := range []string{
		"# prr Audit Report",
		"Files scanned: 42",
		"Areas of interest: 128",
		"Review calls: 20 (12 individual, 8 grouped)",
		"Findings: 3",
		"Dismissed: 113",
		"### Critical",
		"### High",
		"[handler.go:10-15] SQL injection in user query",
		"**Category:** input-validation / sql-injection",
		"**Fix:** Use parameterized queries",
		"[auth.go:44-50] Missing token expiry check",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}

	// Critical findings should appear before High
	critIdx := strings.Index(md, "### Critical")
	highIdx := strings.Index(md, "### High")
	if critIdx > highIdx {
		t.Error("Critical should appear before High")
	}
}

func TestExportAutoDetect(t *testing.T) {
	dir := t.TempDir()
	r := sampleResult()

	// JSON
	jp := filepath.Join(dir, "out.json")
	if err := Export(r, jp); err != nil {
		t.Fatalf("Export .json: %v", err)
	}

	// MD
	mp := filepath.Join(dir, "out.md")
	if err := Export(r, mp); err != nil {
		t.Fatalf("Export .md: %v", err)
	}

	// Unsupported
	if err := Export(r, filepath.Join(dir, "out.txt")); err == nil {
		t.Error("expected error for .txt extension")
	}
}

func TestExportJSONNilFindings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")

	r := &Result{FilesScanned: 1}
	if err := ExportJSON(r, path); err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}

	data, _ := os.ReadFile(path)
	// Should have "findings": [] not "findings": null
	if !strings.Contains(string(data), `"findings": []`) {
		t.Error("nil findings should serialize as empty array")
	}
}

func TestToReportJSON_NilFindings(t *testing.T) {
	r := &Result{
		FilesScanned:  5,
		AOIsGenerated: 10,
		Findings:      nil,
	}
	rj := toReportJSON(r)
	if rj.Findings == nil {
		t.Error("expected non-nil findings slice")
	}
	if len(rj.Findings) != 0 {
		t.Errorf("expected empty findings, got %d", len(rj.Findings))
	}
}

func TestToReportJSON_FieldMapping(t *testing.T) {
	r := &Result{
		FilesScanned:             10,
		AOIsGenerated:            20,
		ReviewCalls:              5,
		IndividualReviews:        3,
		GroupedReviews:           2,
		Findings:                 []state.DeepFinding{{Title: "test"}},
		Dismissals:               4,
		CrossCuttingObservations: []string{"obs1"},
		SkippedSubcategories:     []string{"sub1"},
	}
	rj := toReportJSON(r)
	if rj.FilesScanned != 10 {
		t.Errorf("FilesScanned: want 10, got %d", rj.FilesScanned)
	}
	if rj.AOIsGenerated != 20 {
		t.Errorf("AOIsGenerated: want 20, got %d", rj.AOIsGenerated)
	}
	if rj.ReviewCalls != 5 {
		t.Errorf("ReviewCalls: want 5, got %d", rj.ReviewCalls)
	}
	if rj.IndividualReviews != 3 {
		t.Errorf("IndividualReviews: want 3, got %d", rj.IndividualReviews)
	}
	if rj.GroupedReviews != 2 {
		t.Errorf("GroupedReviews: want 2, got %d", rj.GroupedReviews)
	}
	if len(rj.Findings) != 1 || rj.Findings[0].Title != "test" {
		t.Error("Findings not mapped correctly")
	}
	if rj.Dismissals != 4 {
		t.Errorf("Dismissals: want 4, got %d", rj.Dismissals)
	}
	if len(rj.CrossCuttingObservations) != 1 {
		t.Error("CrossCuttingObservations not mapped")
	}
	if len(rj.SkippedSubcategories) != 1 {
		t.Error("SkippedSubcategories not mapped")
	}
}

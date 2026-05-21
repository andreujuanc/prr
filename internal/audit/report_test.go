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
				Title:       "SQL injection in user query",
				Description: "User input concatenated into SQL",
				Trigger:     state.FindingTrigger{Repro: "User input concatenated directly into SQL string"},
				Suggestion:  "Use parameterized queries",
			},
			{
				AOIID:       "aoi-2",
				File:        "auth.go",
				Lines:       "44-50",
				Severity:    "high",
				Category:    "authentication",
				Title:       "Missing token expiry check",
				Description: "Token is never validated for expiry",
				Trigger:     state.FindingTrigger{Repro: "No expiry validation on JWT"},
			},
			{
				AOIID:       "aoi-3",
				File:        "api.go",
				Lines:       "20-25",
				Severity:    "critical",
				Category:    "input-validation",
				Title:       "Command injection via exec",
				Description: "Unsanitized input passed to exec",
				Trigger:     state.FindingTrigger{Repro: "os/exec call with user input"},
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

	if err := ExportJSON(sampleResult(), nil, path); err != nil {
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

	if err := ExportMarkdown(sampleResult(), nil, path); err != nil {
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

	// Cross-cutting observations should be in the report
	if !strings.Contains(md, "## Cross-Cutting Observations") {
		t.Error("missing Cross-Cutting Observations section")
	}
	if !strings.Contains(md, "Pattern of missing input validation across HTTP handlers") {
		t.Error("first cross-cutting observation not embedded verbatim")
	}
}

func TestExportMarkdown_WithSynthesis(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")

	syn := &SynthesisResult{
		ExecutiveSummary: "The audit found 3 findings clustered in input validation.",
		TopRisks: []string{
			"SQL injection in user query path",
			"Command injection via exec",
		},
		SystemicPatterns: []string{
			"Missing input validation across HTTP handlers",
		},
		Recommendations: []string{
			"Add a shared input-validation middleware",
		},
	}

	if err := ExportMarkdown(sampleResult(), syn, path); err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}

	data, _ := os.ReadFile(path)
	md := string(data)

	for _, want := range []string{
		"## Executive Summary",
		"clustered in input validation",
		"## Top Risks",
		"1. SQL injection in user query path",
		"## Systemic Patterns",
		"Missing input validation across HTTP handlers",
		"## Recommendations",
		"shared input-validation middleware",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing synthesis section %q", want)
		}
	}

	// Synthesis should appear before per-finding detail
	execIdx := strings.Index(md, "## Executive Summary")
	findingsIdx := strings.Index(md, "## Findings")
	if execIdx == -1 || findingsIdx == -1 || execIdx > findingsIdx {
		t.Error("Executive Summary should appear before Findings")
	}
}

func TestExportMarkdown_NitFindingsIncluded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")

	r := &Result{
		Findings: []state.DeepFinding{
			{Severity: "nit", File: "x.go", Title: "trailing whitespace", Lines: "10"},
		},
	}
	if err := ExportMarkdown(r, nil, path); err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}
	data, _ := os.ReadFile(path)
	md := string(data)
	if !strings.Contains(md, "### Nit") {
		t.Error("nit findings should render under '### Nit' heading")
	}
	if !strings.Contains(md, "trailing whitespace") {
		t.Error("nit finding title missing")
	}
}

func TestExportMarkdown_FailedReviewsLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")

	r := sampleResult()
	r.FailedReviews = 4
	if err := ExportMarkdown(r, nil, path); err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "Failed reviews: 4") {
		t.Error("expected 'Failed reviews: 4' line in summary")
	}
}

func TestExportMarkdown_FindingsSortedByLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")

	r := &Result{
		Findings: []state.DeepFinding{
			{Severity: "high", File: "a.go", Lines: "100", Title: "later"},
			{Severity: "high", File: "a.go", Lines: "10", Title: "earlier"},
			{Severity: "high", File: "a.go", Lines: "50-60", Title: "middle"},
		},
	}
	if err := ExportMarkdown(r, nil, path); err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}
	data, _ := os.ReadFile(path)
	md := string(data)

	earlyIdx := strings.Index(md, "earlier")
	midIdx := strings.Index(md, "middle")
	lateIdx := strings.Index(md, "later")
	if !(earlyIdx < midIdx && midIdx < lateIdx) {
		t.Errorf("findings not sorted by line: earlier=%d middle=%d later=%d", earlyIdx, midIdx, lateIdx)
	}
}

func TestExportAutoDetect(t *testing.T) {
	dir := t.TempDir()
	r := sampleResult()

	// JSON
	jp := filepath.Join(dir, "out.json")
	if err := Export(r, nil, jp); err != nil {
		t.Fatalf("Export .json: %v", err)
	}

	// MD
	mp := filepath.Join(dir, "out.md")
	if err := Export(r, nil, mp); err != nil {
		t.Fatalf("Export .md: %v", err)
	}

	// Unsupported
	if err := Export(r, nil, filepath.Join(dir, "out.txt")); err == nil {
		t.Error("expected error for .txt extension")
	}
}

func TestExportJSONNilFindings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")

	r := &Result{FilesScanned: 1}
	if err := ExportJSON(r, nil, path); err != nil {
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
	rj := toReportJSON(r, nil)
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
	rj := toReportJSON(r, nil)
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

// ── Recheck dismissals rendering (PR 2) ─────────────────────────────────
//
// These tests pin the contract that the audit report surfaces *what*
// got dismissed and *why*. Counts alone (the old behavior) made
// recheck regressions invisible — over-aggressive dismissals would
// only show up as "fewer findings than expected" with no way for the
// user to spot which findings were dropped or override the decision.

func resultWithRecheckDismissals() *Result {
	r := sampleResult()
	r.RecheckDismissals = []state.DismissedRecord{
		{
			FindingID: "F-007",
			Finding: state.DeepFinding{
				FindingID: "F-007",
				File:      "internal/auth/login.go",
				Lines:     "42-58",
				Severity:  "medium",
				Title:     "Missing rate limit on login",
			},
			Rationale: "covered by upstream gateway rate limit",
		},
		{
			FindingID: "F-009",
			Finding: state.DeepFinding{
				FindingID: "F-009",
				File:      "internal/api/handler.go",
				Lines:     "12",
				Severity:  "low",
				Title:     "Magic number in retry count",
			},
			Rationale: "convention is to inline these per service",
		},
	}
	return r
}

func TestExportMarkdown_RendersRecheckDismissalsSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")

	if err := ExportMarkdown(resultWithRecheckDismissals(), nil, path); err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	md := string(data)

	if !strings.Contains(md, "## Recheck Dismissals (2)") {
		t.Errorf("report must include a Recheck Dismissals section with count, got:\n%s", md)
	}
	// Every dismissed finding's ID + location + title must appear.
	for _, want := range []string{
		"F-007",
		"internal/auth/login.go:42-58",
		"Missing rate limit on login",
		"covered by upstream gateway rate limit",
		"F-009",
		"internal/api/handler.go:12",
		"Magic number in retry count",
		"convention is to inline these per service",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("recheck dismissals section missing %q", want)
		}
	}
}

func TestExportMarkdown_NoDismissalsSectionWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")

	// sampleResult() has no RecheckDismissals — the section should be
	// suppressed entirely to keep clean audits visually clean.
	if err := ExportMarkdown(sampleResult(), nil, path); err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "Recheck Dismissals") {
		t.Error("Recheck Dismissals section must be omitted when there are none")
	}
}

func TestExportMarkdown_DismissalWithEmptyRationale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")

	r := sampleResult()
	r.RecheckDismissals = []state.DismissedRecord{
		{
			FindingID: "F-001",
			Finding:   state.DeepFinding{FindingID: "F-001", File: "x.go", Title: "X"},
			// No Rationale — the LLM omitted it or stripped it.
			Rationale: "",
		},
	}

	if err := ExportMarkdown(r, nil, path); err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}
	data, _ := os.ReadFile(path)
	md := string(data)
	if !strings.Contains(md, "(no rationale provided)") {
		t.Errorf("empty rationale must render a placeholder so users see the gap, got:\n%s", md)
	}
}

func TestExportJSON_IncludesRecheckDismissals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")

	if err := ExportJSON(resultWithRecheckDismissals(), nil, path); err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var got ReportJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.RecheckDismissals) != 2 {
		t.Fatalf("expected 2 dismissals in JSON, got %d", len(got.RecheckDismissals))
	}
	if got.RecheckDismissals[0].FindingID != "F-007" {
		t.Errorf("first dismissal id: want F-007, got %q", got.RecheckDismissals[0].FindingID)
	}
	if got.RecheckDismissals[0].Rationale != "covered by upstream gateway rate limit" {
		t.Errorf("rationale lost in JSON round trip: %q", got.RecheckDismissals[0].Rationale)
	}
}

func TestToReportJSON_RecheckDismissalsMapped(t *testing.T) {
	r := &Result{
		RecheckDismissals: []state.DismissedRecord{
			{FindingID: "F-001", Rationale: "test"},
		},
	}
	rj := toReportJSON(r, nil)
	if len(rj.RecheckDismissals) != 1 {
		t.Fatalf("expected 1, got %d", len(rj.RecheckDismissals))
	}
	if rj.RecheckDismissals[0].FindingID != "F-001" {
		t.Errorf("FindingID lost in map: %q", rj.RecheckDismissals[0].FindingID)
	}
}

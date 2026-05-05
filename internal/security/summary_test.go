package security

import (
	"testing"
)

func TestBuildSecuritySummary(t *testing.T) {
	findings := []FindingForRevalidation{
		{Index: 0, Severity: "critical", Category: "security", File: "auth.go", Line: 10, Title: "SQL injection", CWE: "CWE-89"},
		{Index: 1, Severity: "high", Category: "security", File: "api.go", Line: 20, Title: "SSRF", CWE: "CWE-918"},
		{Index: 2, Severity: "medium", Category: "bug", File: "util.go", Line: 30, Title: "Off-by-one"}, // not security
		{Index: 3, Severity: "high", Category: "security", File: "auth.go", Line: 50, Title: "Missing auth", CWE: "CWE-862"},
	}

	revalidations := []Revalidation{
		{Verdict: "true-positive", Reasoning: "confirmed", Confidence: "high", CWE: "CWE-89"},
		{Verdict: "false-positive", Reasoning: "mitigated", Confidence: "high"},
		{Verdict: "true-positive", Reasoning: "confirmed", Confidence: "medium"},
	}

	aoiReport := &AOIReport{
		TotalAOIs:     5,
		HighRiskFiles: []string{"auth.go", "api.go"},
	}

	summary := BuildSecuritySummary(findings, revalidations, aoiReport)

	if summary.TotalFindings != 3 { // only security category
		t.Errorf("total findings = %d, want 3", summary.TotalFindings)
	}
	if summary.BySeverity["critical"] != 1 {
		t.Errorf("critical count = %d, want 1", summary.BySeverity["critical"])
	}
	if summary.BySeverity["high"] != 2 {
		t.Errorf("high count = %d, want 2", summary.BySeverity["high"])
	}
	if summary.ByCWE["CWE-89"] != 1 {
		t.Errorf("CWE-89 count = %d, want 1", summary.ByCWE["CWE-89"])
	}
	if summary.RevalidatedCount != 3 {
		t.Errorf("revalidated count = %d, want 3", summary.RevalidatedCount)
	}
	if summary.TruePositives != 2 {
		t.Errorf("true positives = %d, want 2", summary.TruePositives)
	}
	if summary.FalsePositives != 1 {
		t.Errorf("false positives = %d, want 1", summary.FalsePositives)
	}
	if summary.AOIsTotal != 5 {
		t.Errorf("AOIs total = %d, want 5", summary.AOIsTotal)
	}
}

func TestBuildSecuritySummary_NoSecurityFindings(t *testing.T) {
	findings := []FindingForRevalidation{
		{Index: 0, Severity: "medium", Category: "bug", File: "util.go", Line: 30, Title: "Off-by-one"},
	}

	summary := BuildSecuritySummary(findings, nil, nil)

	if summary.TotalFindings != 0 {
		t.Errorf("total findings = %d, want 0", summary.TotalFindings)
	}
}

func TestFormatSecuritySummary(t *testing.T) {
	summary := &SecuritySummary{
		TotalFindings:    3,
		BySeverity:       map[string]int{"critical": 1, "high": 2},
		ByCWE:            map[string]int{"CWE-89": 1, "CWE-918": 1, "CWE-862": 1},
		HighRiskFiles:    []string{"auth.go", "api.go"},
		AOIsTotal:        5,
		RevalidatedCount: 2,
		TruePositives:    1,
		FalsePositives:   1,
	}

	result := FormatSecuritySummary(summary)

	if result == "" {
		t.Fatal("formatted summary should not be empty")
	}
	if !containsStr(result, "3 security finding") {
		t.Error("should mention total findings")
	}
	if !containsStr(result, "critical=1") {
		t.Error("should mention critical count")
	}
	if !containsStr(result, "CWE-89") {
		t.Error("should mention CWE IDs")
	}
	if !containsStr(result, "2 revalidated") {
		t.Error("should mention revalidation stats")
	}
}

func TestFormatSecuritySummary_Empty(t *testing.T) {
	result := FormatSecuritySummary(nil)
	if result != "" {
		t.Error("nil summary should return empty string")
	}

	result = FormatSecuritySummary(&SecuritySummary{})
	if result != "" {
		t.Error("zero-findings summary should return empty string")
	}
}

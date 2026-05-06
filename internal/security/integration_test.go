package security

import (
	"context"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
)

// mockClient implements ai.Client for testing.
type mockClient struct {
	response string
	err      error
}

func (m *mockClient) ChatStream(_ context.Context, _ string, _ []ai.Message, onToken func(string)) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if onToken != nil {
		onToken(m.response)
	}
	return m.response, nil
}

func TestAOIScanPrompt(t *testing.T) {
	p := AOIScanPrompt()
	if p == "" {
		t.Fatal("AOIScanPrompt() should not be empty")
	}
}

func TestRevalidatePrompt(t *testing.T) {
	p := RevalidatePrompt()
	if p == "" {
		t.Fatal("RevalidatePrompt() should not be empty")
	}
}

func TestCountFiles(t *testing.T) {
	batches := []aoiBatch{
		{files: []string{"a.go", "b.go"}},
		{files: []string{"c.go"}},
		{},
	}
	if got := countFiles(batches); got != 3 {
		t.Errorf("countFiles = %d, want 3", got)
	}
}

func TestCountFiles_Empty(t *testing.T) {
	if got := countFiles(nil); got != 0 {
		t.Errorf("countFiles(nil) = %d, want 0", got)
	}
}

func TestScanAreasOfInterest_AllCached(t *testing.T) {
	ctx := context.Background()
	client := &mockClient{response: "should not be called"}

	cached := map[string]*AOIScanResult{
		"a.go": {
			File:      "a.go",
			RiskLevel: "low",
		},
	}
	rawDiffs := map[string]string{"a.go": "diff content"}

	var progressMsgs []string
	report, err := ScanAreasOfInterest(ctx, client, rawDiffs, cached, func(s string) {
		progressMsgs = append(progressMsgs, s)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report == nil {
		t.Fatal("report should not be nil")
	}
	if len(progressMsgs) == 0 {
		t.Error("expected progress messages for cached results")
	}
}

func TestScanAreasOfInterest_EmptyDiffs(t *testing.T) {
	ctx := context.Background()
	client := &mockClient{}

	report, err := ScanAreasOfInterest(ctx, client, map[string]string{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.OverallRisk != "none" {
		t.Errorf("OverallRisk = %q, want %q", report.OverallRisk, "none")
	}
}

func TestScanAreasOfInterest_WithMockClient(t *testing.T) {
	ctx := context.Background()
	// Return valid AOI JSON
	response := `[{"file":"handler.go","risk_level":"high","risk_summary":"SQL injection risk","areas_of_interest":[{"file":"handler.go","line":10,"end_line":12,"category":"sql","snippet":"db.Query(userInput)","reasoning":"unsanitized input","confidence":"high"}]}]`
	client := &mockClient{response: response}

	rawDiffs := map[string]string{
		"handler.go": "diff --git a/handler.go\n+db.Query(userInput)",
	}

	report, err := ScanAreasOfInterest(ctx, client, rawDiffs, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report == nil {
		t.Fatal("report should not be nil")
	}
	if report.TotalAOIs != 1 {
		t.Errorf("TotalAOIs = %d, want 1", report.TotalAOIs)
	}
}

func TestScanAreasOfInterest_ClientError(t *testing.T) {
	ctx := context.Background()
	client := &mockClient{err: context.DeadlineExceeded}

	rawDiffs := map[string]string{
		"handler.go": "some diff",
	}

	// Should not return an error — individual batch failures are logged, not propagated
	report, err := ScanAreasOfInterest(ctx, client, rawDiffs, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Report should still be built (with 0 results from the failed batch)
	if report == nil {
		t.Fatal("report should not be nil even on batch failure")
	}
}

func TestRevalidateFindings_Empty(t *testing.T) {
	ctx := context.Background()
	client := &mockClient{}

	results, err := RevalidateFindings(ctx, client, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for empty findings, got %v", results)
	}
}

func TestRevalidateFindings_WithMockClient(t *testing.T) {
	ctx := context.Background()
	response := `[{"verdict":"true_positive","reasoning":"confirmed SQL injection","confidence":"high","cwe":"CWE-89"}]`
	client := &mockClient{response: response}

	findings := []FindingForRevalidation{
		{Index: 0, Severity: "high", Category: "sql", File: "handler.go", Line: 10, Title: "SQL injection"},
	}

	var progressMsgs []string
	results, err := RevalidateFindings(ctx, client, findings, func(s string) {
		progressMsgs = append(progressMsgs, s)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Verdict != "true_positive" {
		t.Errorf("verdict = %q, want %q", results[0].Verdict, "true_positive")
	}
	if results[0].CWE != "CWE-89" {
		t.Errorf("CWE = %q, want %q", results[0].CWE, "CWE-89")
	}
	if len(progressMsgs) == 0 {
		t.Error("expected progress messages")
	}
}

func TestRevalidateFindings_ClientError(t *testing.T) {
	ctx := context.Background()
	client := &mockClient{err: context.DeadlineExceeded}

	findings := []FindingForRevalidation{
		{Index: 0, Severity: "high", File: "x.go"},
	}

	_, err := RevalidateFindings(ctx, client, findings, nil)
	if err == nil {
		t.Fatal("expected error from client failure")
	}
}

package security

import (
	"context"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/aitesting"
)

// mockClient implements ai.Client for testing.
//
// The prompt/messages passed into ChatStream are recorded on the
// receiver so individual tests can assert that the scanner
// constructed the right request (model, system prompt, message
// list). Without that, the previous version of this mock would
// accept any input and return the same canned response, which
// silently masked prompt-construction regressions.
type mockClient struct {
	response string
	err      error

	// Captured inputs from the most recent ChatStream call.
	gotPrompt   string
	gotMessages []ai.Message
}

func (m *mockClient) ChatStream(_ context.Context, prompt string, messages []ai.Message, onToken func(string)) (string, error) {
	m.gotPrompt = prompt
	m.gotMessages = messages
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

// TestSecurityPrompts_NoToolNamesLeakIntoClaudeCode mirrors the leak
// check in internal/ai/prompt_test.go for prompts that live in this
// package. Any inline tool name that wasn't rephrased shows up here.
// The fake provider and tool-name list come from internal/aitesting so
// there is a single source of truth across leak-test sites.
func TestSecurityPrompts_NoToolNamesLeakIntoClaudeCode(t *testing.T) {
	prompts := map[string]string{
		"AOIScanPrompt":    AOIScanPrompt(),
		"AOIAuditPrompt":   AOIAuditPrompt(),
		"RevalidatePrompt": RevalidatePrompt(),
	}

	claude := aitesting.ClaudeCodeProvider{}
	for name, raw := range prompts {
		resolved := ai.ResolveTools(raw, claude)
		var leaked []string
		for _, tn := range aitesting.PrrSpecificToolNames {
			if strings.Contains(resolved, tn) {
				leaked = append(leaked, tn)
			}
		}
		if len(leaked) > 0 {
			t.Errorf("%s leaked tool names into Claude Code resolve: %v", name, leaked)
		}
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
			File: "a.go",
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
	if report.TotalAOIs != 0 {
		t.Errorf("TotalAOIs = %d, want %d", report.TotalAOIs, 0)
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

	// When all batches fail, an error should be returned
	report, err := ScanAreasOfInterest(ctx, client, rawDiffs, nil, nil)
	if err == nil {
		t.Fatal("expected error when all batches fail, got nil")
	}
	if report != nil {
		t.Fatal("report should be nil when all batches fail")
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

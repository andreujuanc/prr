package audit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// mockClient implements ai.Client for testing.
type mockSynthesisClient struct {
	response string
	err      error
	captured struct {
		systemPrompt string
		messages     []ai.Message
	}
}

func (m *mockSynthesisClient) ChatStream(_ context.Context, systemPrompt string, messages []ai.Message, onToken func(string)) (string, error) {
	m.captured.systemPrompt = systemPrompt
	m.captured.messages = messages
	if m.err != nil {
		return "", m.err
	}
	if onToken != nil {
		onToken(m.response)
	}
	return m.response, nil
}

func makeFindings(n int) []state.DeepFinding {
	findings := make([]state.DeepFinding, n)
	for i := range findings {
		sev := "low"
		if i%4 == 0 {
			sev = "critical"
		} else if i%4 == 1 {
			sev = "high"
		} else if i%4 == 2 {
			sev = "medium"
		}
		findings[i] = state.DeepFinding{
			AOIID:       fmt.Sprintf("aoi-%d", i),
			File:        fmt.Sprintf("pkg/file%d.go", i),
			Lines:       fmt.Sprintf("%d-%d", i*10, i*10+5),
			Severity:    sev,
			Category:    fmt.Sprintf("cat%d", i%3),
			Subcategory: "sub",
			Dimension:   "security",
			Title:       fmt.Sprintf("Finding %d", i),
			Description: fmt.Sprintf("Description for finding %d", i),
			Trigger:     "trigger",
			Suggestion:  "fix it",
		}
	}
	return findings
}

func TestBuildSynthesisUserMessage(t *testing.T) {
	findings := makeFindings(4)
	crossCutting := []string{"Pattern A observed", "Pattern B observed"}

	msg := BuildSynthesisUserMessage(findings, crossCutting, "Test project context", 0)

	// Check project context included
	if !strings.Contains(msg, "Test project context") {
		t.Error("expected project context in message")
	}

	// Check severity grouping
	if !strings.Contains(msg, "### CRITICAL") {
		t.Error("expected CRITICAL severity header")
	}
	if !strings.Contains(msg, "### HIGH") {
		t.Error("expected HIGH severity header")
	}

	// Check finding details
	if !strings.Contains(msg, "Finding 0") {
		t.Error("expected finding title in message")
	}
	if !strings.Contains(msg, "pkg/file0.go") {
		t.Error("expected file path in message")
	}

	// Check cross-cutting
	if !strings.Contains(msg, "Pattern A observed") {
		t.Error("expected cross-cutting observation")
	}

	// Check total count
	if !strings.Contains(msg, "4 total") {
		t.Error("expected total count in message")
	}
}

func TestBuildSynthesisUserMessageNoContext(t *testing.T) {
	findings := makeFindings(1)
	msg := BuildSynthesisUserMessage(findings, nil, "", 0)

	if strings.Contains(msg, "## Project Context") {
		t.Error("should not include project context header when empty")
	}
}

func TestParseSynthesisResult(t *testing.T) {
	raw := `{"executive_summary":"Overview here.","top_risks":["Risk 1","Risk 2"],"systemic_patterns":["Pattern 1"],"recommendations":["Fix A","Fix B"]}`

	result, err := ParseSynthesisResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExecutiveSummary != "Overview here." {
		t.Errorf("got summary %q", result.ExecutiveSummary)
	}
	if len(result.TopRisks) != 2 {
		t.Errorf("expected 2 top risks, got %d", len(result.TopRisks))
	}
	if len(result.SystemicPatterns) != 1 {
		t.Errorf("expected 1 systemic pattern, got %d", len(result.SystemicPatterns))
	}
	if len(result.Recommendations) != 2 {
		t.Errorf("expected 2 recommendations, got %d", len(result.Recommendations))
	}
}

func TestParseSynthesisResultWithFences(t *testing.T) {
	raw := "```json\n" + `{"executive_summary":"Fenced.","top_risks":[],"systemic_patterns":[],"recommendations":[]}` + "\n```"

	result, err := ParseSynthesisResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExecutiveSummary != "Fenced." {
		t.Errorf("got summary %q", result.ExecutiveSummary)
	}
}

func TestParseSynthesisResultNoJSON(t *testing.T) {
	_, err := ParseSynthesisResult("no json here")
	if err == nil {
		t.Error("expected error for non-JSON response")
	}
}

func TestNeedsHierarchical(t *testing.T) {
	if NeedsHierarchical(50) {
		t.Error("50 should not need hierarchical")
	}
	if !NeedsHierarchical(51) {
		t.Error("51 should need hierarchical")
	}
}

func TestSynthesizeEmptyFindings(t *testing.T) {
	result, err := Synthesize(context.Background(), nil, nil, nil, "", 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExecutiveSummary == "" {
		t.Error("expected non-empty summary for empty findings")
	}
}

func TestSynthesizeDirect(t *testing.T) {
	mockResp := `{"executive_summary":"All good.","top_risks":["R1"],"systemic_patterns":["P1"],"recommendations":["Do X"]}`
	client := &mockSynthesisClient{response: mockResp}

	findings := makeFindings(3)
	result, err := Synthesize(context.Background(), client, findings, []string{"obs1"}, "ctx", 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExecutiveSummary != "All good." {
		t.Errorf("got summary %q", result.ExecutiveSummary)
	}
	if result.RawOutput != mockResp {
		t.Error("expected RawOutput to be set")
	}

	// Verify system prompt was the audit synthesis prompt
	if client.captured.systemPrompt != ai.AuditSynthesisPrompt {
		t.Error("expected audit synthesis prompt as system prompt")
	}
}

func TestSynthesizeLLMError(t *testing.T) {
	client := &mockSynthesisClient{err: fmt.Errorf("api error")}
	_, err := Synthesize(context.Background(), client, makeFindings(1), nil, "", 0, nil)
	if err == nil {
		t.Error("expected error")
	}
}

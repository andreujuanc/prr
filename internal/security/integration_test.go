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

// TestAOIScanPrompt_NoUnsubstitutedPlaceholders guards the template
// composition: every {PLACEHOLDER} in the parent prompt must have a
// matching substitution at runtime. A leak here means the rendered
// prompt reaches the model with a literal "{...}" token, which would
// poison output and silently break parsing.
func TestAOIScanPrompt_NoUnsubstitutedPlaceholders(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
	}{
		{"PR mode", AOIScanPrompt()},
		{"audit mode", AOIAuditPrompt()},
	}
	for _, c := range cases {
		for _, ph := range []string{"{CATEGORIES}", "{CATEGORY_SLUGS}", "{MODE_RULES}", "{INPUT_FORMAT}"} {
			if strings.Contains(c.prompt, ph) {
				t.Errorf("%s: unsubstituted placeholder %s", c.name, ph)
			}
		}
	}
}

// TestAOIScanPrompt_HasDedupeRule pins the (file, line, category)
// dedup rule. Without it, the model can emit multiple AOIs at the
// same line for the same root issue (e.g. a TODO surface-area rule
// and a broad correctness scan both firing on the same line), which
// wastes Phase 3 LLM calls on near-duplicates. The rule lives in the
// shared parent prompt, so both modes get it.
func TestAOIScanPrompt_HasDedupeRule(t *testing.T) {
	for _, c := range []struct{ name, prompt string }{
		{"PR mode", AOIScanPrompt()},
		{"audit mode", AOIAuditPrompt()},
	} {
		if !strings.Contains(c.prompt, "One AOI per `(file, line, category)`") {
			t.Errorf("%s: missing (file, line, category) dedup rule", c.name)
		}
	}
}

// TestAOIScanPrompt_ModeContentMatchesMode pins the central reason for
// the PR/audit split: each rendered prompt must include only its own
// mode's load-bearing rules. Specifically:
//
//   - Audit mode MUST instruct the model to copy line numbers from the
//     " NN: " input prefix verbatim. This is the only correctness
//     anchor for audit-mode AOI line accuracy.
//   - PR mode MUST instruct the model to use @@ -X,Y +A,B @@ hunk
//     headers and MUST NOT instruct it to look for line-number prefixes
//     (a previous version of the prompt had a "audit-mode rule above
//     does not apply" caveat that was easy for models to mis-apply).
func TestAOIScanPrompt_ModeContentMatchesMode(t *testing.T) {
	pr := AOIScanPrompt()
	audit := AOIAuditPrompt()

	// "Every input line is prefixed with its source line number" is the
	// load-bearing audit-mode invariant — without it, the model cannot
	// produce accurate `line` / `end_line` values from prefixed input.
	if !strings.Contains(audit, "Every input line is prefixed with its source line number") {
		t.Error("audit prompt missing line-number-prefix rule (the load-bearing audit-mode invariant)")
	}
	if !strings.Contains(audit, "Scan ALL code in the file") {
		t.Error("audit prompt missing audit-mode rules")
	}

	if !strings.Contains(pr, "@@ -X,Y +A,B @@") {
		t.Error("PR prompt missing hunk-header instruction")
	}
	if !strings.Contains(pr, "ONLY flag code in the DIFF") {
		t.Error("PR prompt missing PR-mode rules")
	}
	if strings.Contains(pr, "Every input line is prefixed with its source line number") {
		t.Error("PR prompt leaked audit-mode line-prefix rule — model may fabricate line numbers from a non-existent prefix")
	}
}

// TestSecurityPrompts_NoToolNamesLeakIntoClaudeCode mirrors the leak
// check in internal/ai/prompt_test.go for prompts that live in this
// package. Any inline tool name that wasn't rephrased shows up here.
// The fake provider and tool-name list come from internal/aitesting so
// there is a single source of truth across leak-test sites.
func TestSecurityPrompts_NoToolNamesLeakIntoClaudeCode(t *testing.T) {
	prompts := map[string]string{
		"AOIScanPrompt":  AOIScanPrompt(),
		"AOIAuditPrompt": AOIAuditPrompt(),
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

package ai

import (
	"context"
	"strings"
	"testing"
)

// fakeProvider implements Provider just enough for ResolveTools.
type fakeProvider struct {
	runsOwnLoop bool
}

func (f fakeProvider) Name() string    { return "fake" }
func (f fakeProvider) ModelID() string { return "fake-1" }
func (f fakeProvider) Capabilities() Capabilities {
	return Capabilities{RunsOwnToolLoop: f.runsOwnLoop}
}
func (f fakeProvider) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	return nil, nil
}
func (f fakeProvider) StreamChat(_ context.Context, _ ChatRequest) (<-chan ChatEvent, error) {
	return nil, nil
}

// allEmbeddedPrompts returns every prompt that gets sent to a provider,
// keyed by a short name. This is the surface ResolveTools must clean.
func allEmbeddedPrompts() map[string]string {
	return map[string]string{
		"ReviewPRSystemPrompt":   ReviewPRSystemPrompt,
		"ReviewFilePrompt":       ReviewFilePrompt,
		"ReviewBatchPrompt":      ReviewBatchPrompt,
		"ReviewSynthesisPrompt":  ReviewSynthesisPrompt,
		"ChatPrompt":             ChatPrompt,
		"ReviewIndividualPrompt": ReviewIndividualPrompt,
		"ReviewGroupedPrompt":    ReviewGroupedPrompt,
		"AuditSynthesisPrompt":   AuditSynthesisPrompt,
		"RecheckPrompt":          RecheckPrompt,
		"ReviewPRPrompt":         ReviewPRPrompt,
	}
}

// TestResolveTools_NoToolNamesLeakIntoClaudeCode is the load-bearing
// guarantee of the {{TOOLS}} refactor: when a prompt is resolved for a
// provider that runs its own tool loop, no prr-specific tool name may
// remain. If this fails, a prompt either forgot the {{TOOLS}} placeholder
// or contains an inline tool-name reference that wasn't rephrased.
func TestResolveTools_NoToolNamesLeakIntoClaudeCode(t *testing.T) {
	claude := fakeProvider{runsOwnLoop: true}

	for name, raw := range allEmbeddedPrompts() {
		resolved := ResolveTools(raw, claude)
		var leaked []string
		for _, tn := range PrrSpecificToolNames {
			if strings.Contains(resolved, tn) {
				leaked = append(leaked, tn)
			}
		}
		if len(leaked) > 0 {
			t.Errorf("%s leaked tool names into Claude Code resolve: %v", name, leaked)
		}
	}
}

// TestResolveTools_PlaceholderAlwaysSubstituted ensures every prompt is
// migrated. A prompt containing a literal {{TOOLS}} after resolve means
// either the placeholder was misspelled or substitution silently failed.
func TestResolveTools_PlaceholderAlwaysSubstituted(t *testing.T) {
	for _, p := range []fakeProvider{{runsOwnLoop: false}, {runsOwnLoop: true}} {
		for name, raw := range allEmbeddedPrompts() {
			resolved := ResolveTools(raw, p)
			if strings.Contains(resolved, toolsPlaceholder) {
				t.Errorf("%s (runsOwnLoop=%v): {{TOOLS}} not substituted", name, p.runsOwnLoop)
			}
		}
	}
}

// TestResolveTools_HarnessInjectsToolBlock verifies the positive case:
// for providers that drive prr's tool loop, prompts that opted in via
// {{TOOLS}} actually receive the canonical block.
func TestResolveTools_HarnessInjectsToolBlock(t *testing.T) {
	harness := fakeProvider{runsOwnLoop: false}
	// Probe with a fixed minimal template so the test doesn't depend on
	// the content of every shipped prompt.
	resolved := ResolveTools("before\n{{TOOLS}}\nafter\n", harness)
	if !strings.Contains(resolved, "read_file") {
		t.Errorf("harness resolve missing canonical tool block; got:\n%s", resolved)
	}
	if !strings.Contains(resolved, "## Tools available") {
		t.Errorf("harness resolve missing tools heading; got:\n%s", resolved)
	}
}

// TestResolveTools_ClaudeCodeEmptySub verifies the negative case:
// {{TOOLS}} disappears cleanly for Claude Code, with no orphaned heading.
func TestResolveTools_ClaudeCodeEmptySub(t *testing.T) {
	claude := fakeProvider{runsOwnLoop: true}
	resolved := ResolveTools("before\n{{TOOLS}}\nafter\n", claude)
	if strings.Contains(resolved, "## Tools") {
		t.Errorf("claude-code resolve left orphaned heading; got:\n%s", resolved)
	}
	if strings.Contains(resolved, "read_file") {
		t.Errorf("claude-code resolve leaked tool name; got:\n%s", resolved)
	}
}

// TestRuntimeHints_NoToolNamesLeakIntoClaudeCode is the load-bearing
// guarantee for hints.go: every centralized model-facing runtime string
// must stay free of prr-specific tool names. Claude Code receives these
// hints embedded in user messages and has no read_file / git_diff /
// gh_pr_* / get_review tools to call.
//
// Why this matters (Rule 9): the leak test for prompts catches the .md
// files but cannot reach strings built at runtime in Go. By forcing
// runtime hints to live as Hint* constants registered in AllHints, we
// get CI coverage for them — the same shape as the prompt leak test.
// If you add a new Hint* constant without registering it in AllHints,
// this coverage silently degrades; that's the convention's one weak
// point, and it's why hints.go has a prominent comment about it.
func TestRuntimeHints_NoToolNamesLeakIntoClaudeCode(t *testing.T) {
	if len(AllHints) == 0 {
		t.Fatal("AllHints is empty — every Hint* constant must be registered there")
	}
	for i, hint := range AllHints {
		var leaked []string
		for _, tn := range PrrSpecificToolNames {
			if strings.Contains(hint, tn) {
				leaked = append(leaked, tn)
			}
		}
		if len(leaked) > 0 {
			t.Errorf("AllHints[%d] contains prr-specific tool names %v\n"+
				"hint: %q\n"+
				"Rephrase to neutral prose (\"fetch the diff\", \"read the file\") — "+
				"Claude Code doesn't have these tools by these names.",
				i, leaked, hint)
		}
	}
}

// TestResolveTools_NilProvider_InjectsHarnessBlock pins the defensive
// default: callers without a provider (e.g. bootstrap paths, broken
// constructors) get the harness tool block. The alternative would be
// to panic or error, but this code path runs deep inside Agent.ChatStream
// where a nil-provider crash would be much harder to diagnose. Choosing
// the safer default — inject tools and let the (likely nil) StreamChat
// call surface the real error.
func TestResolveTools_NilProvider_InjectsHarnessBlock(t *testing.T) {
	resolved := ResolveTools("before\n{{TOOLS}}\nafter\n", nil)
	if !strings.Contains(resolved, "read_file") {
		t.Errorf("nil provider should inject harness tool block; got:\n%s", resolved)
	}
	if !strings.Contains(resolved, "## Tools available") {
		t.Errorf("nil provider should inject tool heading; got:\n%s", resolved)
	}
}

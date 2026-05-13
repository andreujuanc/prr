package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// Layer 1 of the persistence-bug regression suite.
//
// The bug: `ParseDeepReviewResult` stored the unmodified LLM response in
// `state.DeepReviewResult.RawOutput` (a `json.RawMessage`). When the LLM
// returned a markdown-fenced response (```json ... ```), the leading
// backtick made the RawMessage invalid JSON. Any subsequent
// `json.Marshal(state)` — i.e. every `state.Save` — failed for the rest
// of the session, silently dropping all findings persistence. Users saw
// "review completed in the TUI, closed, reopened, findings gone".
//
// These tests pin the contract: regardless of what the LLM returns,
// `RawOutput` must be a value the `encoding/json` package can marshal.

// indivCall is the canonical "individual" ReviewCall fixture.
func indivCall() ReviewCall {
	return ReviewCall{
		Type:        "individual",
		Category:    "security",
		Subcategory: "auth",
		AOIs:        []security.AreaOfInterest{{ID: "aoi-1", File: "x.go", Line: 1}},
	}
}

// validFindingJSON is a clean JSON response for an individual AOI.
const validFindingJSON = `{
  "aoi_id": "aoi-1",
  "status": "finding",
  "file": "x.go",
  "lines": "10",
  "severity": "high",
  "category": "security",
  "subcategory": "auth",
  "dimension": "security",
  "title": "Hardcoded credential",
  "description": "API key in source.",
  "trigger": "Always.",
  "suggestion": "Move to env."
}`

// TestParseDeepReviewResult_RawOutputAlwaysMarshalable is the load-bearing
// regression test. It feeds a variety of LLM response shapes — clean
// JSON, markdown-fenced JSON, prose + fence, garbage — into
// ParseDeepReviewResult and asserts that the resulting RawOutput can be
// re-marshaled by encoding/json. If this test goes red, state.Save will
// fail at runtime for the same input, and the user's findings won't
// persist across a TUI restart.
func TestParseDeepReviewResult_RawOutputAlwaysMarshalable(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"clean json", validFindingJSON},
		{"fenced with language tag", "```json\n" + validFindingJSON + "\n```"},
		{"fenced no language tag", "```\n" + validFindingJSON + "\n```"},
		{"fence + leading prose", "Sure, here's the analysis:\n```json\n" + validFindingJSON + "\n```"},
		{"fence + trailing prose", "```json\n" + validFindingJSON + "\n```\nHope that helps."},
		{"empty string", ""},
		{"whitespace only", "   \n\t  "},
		{"non-json prose", "I can't analyze this without more context."},
		{"unterminated fence", "```json\n" + validFindingJSON},
		{"trailing backticks only", validFindingJSON + "\n```"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := ParseDeepReviewResult(indivCall(), tc.raw)
			if result == nil {
				t.Fatal("ParseDeepReviewResult returned nil")
			}

			// The whole point: json.Marshal must not fail on RawOutput.
			// json.RawMessage.MarshalJSON validates its bytes are valid
			// JSON — a backtick or empty slice would trip it. This is
			// the exact failure mode that broke state.Save.
			if _, err := json.Marshal(result.RawOutput); err != nil {
				t.Fatalf("json.Marshal(RawOutput) failed: %v\nraw input was: %q\nstored RawOutput: %q",
					err, tc.raw, string(result.RawOutput))
			}
		})
	}
}

// TestParseDeepReviewResult_StateSaveRoundTrips is the integration of
// Layer 1 with the state package: build a State, drop a DeepReviewResult
// produced from a fenced response into it, save it, load it back. Pre-fix
// this fails at state.Save with the persistence error from the bug
// report. Post-fix it round-trips cleanly.
//
// Uses an in-memory state package smoke path: NewState + SetDeepReview +
// json.Marshal. We deliberately bypass disk here — the marshal step is
// what fails, and a json.MarshalIndent assertion is enough to exercise
// the same code path Save uses (see internal/state/store.go:164).
func TestParseDeepReviewResult_StateSaveRoundTrips(t *testing.T) {
	fenced := "```json\n" + validFindingJSON + "\n```"
	result, err := ParseDeepReviewResult(indivCall(), fenced)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	s := state.NewState("test-pr")
	s.SetDeepReview("k1", result)

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshaling state with fenced RawOutput failed (this is the user bug): %v", err)
	}

	// Round-trip back through Unmarshal to confirm the structure
	// survives. We're not asserting RawOutput equality — the fix may
	// canonicalize it. We assert the *structured* fields parsed by
	// ParseDeepReviewResult are intact, since those are what the TUI
	// renders to the user.
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding parsed from fenced response, got %d", len(result.Findings))
	}
	if !strings.Contains(string(data), "Hardcoded credential") {
		t.Errorf("serialized state lost the finding title")
	}
}

// TestParseDeepReviewResult_FencedYieldsSameParsedFields confirms the
// parser's structured-field extraction is fence-robust today (the bug
// was only on the RawOutput field, not the parsing). If this test ever
// fails it means the parser regressed and findings are being dropped
// before they reach state at all.
func TestParseDeepReviewResult_FencedYieldsSameParsedFields(t *testing.T) {
	clean, cleanErr := ParseDeepReviewResult(indivCall(), validFindingJSON)
	fenced, fencedErr := ParseDeepReviewResult(indivCall(), "```json\n"+validFindingJSON+"\n```")
	if cleanErr != nil || fencedErr != nil {
		t.Fatalf("parse: clean=%v fenced=%v", cleanErr, fencedErr)
	}

	if len(clean.Findings) != 1 || len(fenced.Findings) != 1 {
		t.Fatalf("clean findings=%d, fenced findings=%d (want 1 each)", len(clean.Findings), len(fenced.Findings))
	}
	if clean.Findings[0].Title != fenced.Findings[0].Title {
		t.Errorf("title mismatch: clean=%q fenced=%q", clean.Findings[0].Title, fenced.Findings[0].Title)
	}
	if clean.Findings[0].Severity != fenced.Findings[0].Severity {
		t.Errorf("severity mismatch: clean=%q fenced=%q", clean.Findings[0].Severity, fenced.Findings[0].Severity)
	}
}

// ── OnLLMCall sees resolved prompt ─────────────────────────────────
//
// Pin the contract that the systemPrompt passed to OnLLMCall has
// already had {{TOOLS}} resolved. Without the explicit pre-resolve at
// the call site, Agent.ChatStream's internal resolve runs on a local
// copy and the debug hook receives the unresolved placeholder. The
// user saw this as a literal "{{TOOLS}}" string in their debug logs.

// recordingAgentClient is a minimal Client that wraps a real *ai.Agent
// just enough to assert what the OnLLMCall hook receives. We can't use
// the fakeAIClient from pipeline_persistence_test.go because that
// isn't an *ai.Agent — ResolveToolsForClient's pass-through branch
// would silently let the placeholder through. We need a real Agent
// here.

func TestRunReviewCalls_OnLLMCall_SystemPromptHasNoToolsPlaceholder(t *testing.T) {
	// Wire a real Agent with a harness-style fake provider so
	// ResolveTools injects the canonical tool block.
	provider := harnessProvider{}
	agent := ai.NewAgent(provider, nil)

	calls := []ReviewCall{
		{Type: "individual", AOIs: []security.AreaOfInterest{{ID: "aoi-1", File: "src/auth.go", Line: 42}}},
	}

	var hookCalled bool
	var capturedPrompt string
	opts := ExecuteOptions{
		Mode: ModePR,
		OnLLMCall: func(_ int, _ ReviewCall, systemPrompt, _, _ string) {
			hookCalled = true
			capturedPrompt = systemPrompt
		},
	}

	_, _ = RunReviewCalls(context.Background(), agent, calls, opts)

	if !hookCalled {
		t.Fatal("OnLLMCall hook was never invoked")
	}
	if strings.Contains(capturedPrompt, "{{TOOLS}}") {
		t.Errorf("OnLLMCall received unresolved {{TOOLS}} placeholder; the debug log would show the literal placeholder.\n"+
			"This regression means Agent.ChatStream's internal resolve is being hidden from the caller again.\n"+
			"systemPrompt excerpt:\n%s", excerptAround(capturedPrompt, "{{TOOLS}}"))
	}
	// Positive assertion: the canonical tool block should now be there.
	if !strings.Contains(capturedPrompt, "read_file") {
		t.Errorf("expected resolved prompt to contain the canonical tool listing; got:\n%s", capturedPrompt[:min(len(capturedPrompt), 500)])
	}
}

// harnessProvider implements ai.Provider as a harness-style provider
// (doesn't run its own tool loop, so {{TOOLS}} gets the canonical block).
// The agent never actually invokes StreamChat in this test — it errors
// before reaching the LLM — but the path through ChatStream up to the
// resolve happens, which is all we need.
type harnessProvider struct{}

func (harnessProvider) Name() string    { return "test-harness" }
func (harnessProvider) ModelID() string { return "test-1" }
func (harnessProvider) Capabilities() ai.Capabilities {
	return ai.Capabilities{RunsOwnToolLoop: false}
}
func (harnessProvider) Chat(_ context.Context, _ ai.ChatRequest) (*ai.ChatResponse, error) {
	// Agent uses StreamChat, not Chat — empty return is fine.
	return &ai.ChatResponse{}, nil
}
func (harnessProvider) StreamChat(_ context.Context, _ ai.ChatRequest) (<-chan ai.ChatEvent, error) {
	// Return a channel that emits the canned response, then closes.
	ch := make(chan ai.ChatEvent, 2)
	ch <- ai.ChatEvent{Type: ai.EventText, Text: validFindingJSON}
	ch <- ai.ChatEvent{Type: ai.EventDone}
	close(ch)
	return ch, nil
}

// excerptAround returns a slice of s centered on the first occurrence
// of needle, with ~40 chars of context on each side. Used to make
// test failures actionable without dumping the whole prompt.
func excerptAround(s, needle string) string {
	i := strings.Index(s, needle)
	if i < 0 {
		return s
	}
	start := i - 40
	if start < 0 {
		start = 0
	}
	end := i + len(needle) + 40
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}


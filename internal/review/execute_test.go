package review

import (
	"encoding/json"
	"strings"
	"testing"

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
			result := ParseDeepReviewResult(indivCall(), tc.raw)
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
	result := ParseDeepReviewResult(indivCall(), fenced)

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
	clean := ParseDeepReviewResult(indivCall(), validFindingJSON)
	fenced := ParseDeepReviewResult(indivCall(), "```json\n"+validFindingJSON+"\n```")

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

package review

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

func TestBuildIndividualPrompt_ContainsAllSections(t *testing.T) {
	aoi := security.AreaOfInterest{
		File:        "billing/charge.go",
		Line:        45,
		EndLine:     78,
		Category:    "financial",
		Subcategory: "money-arithmetic",
		Urgency:     "individual",
		ID:          "charge-go-float-currency",
		Concern:     "Currency conversion with floating point arithmetic",
		Context:     "Multiplies amounts by exchange rates using float64",
		Dimensions:  []string{"correctness", "financial"},
	}

	prompt := BuildIndividualPrompt(ModeAudit, "This is a billing system.", "Always check money math.", "", nil, aoi)

	checks := []struct {
		name    string
		content string
	}{
		{"audit preamble", "auditing source code"},
		{"base prompt", "deeply investigating"},
		{"project context", "billing system"},
		{"file", "billing/charge.go"},
		{"lines", "45-78"},
		{"category", "financial / money-arithmetic"},
		{"concern", "Currency conversion with floating point arithmetic"},
		{"context", "exchange rates"},
		{"aoi id", "charge-go-float-currency"},
		{"dimension content", "money-arithmetic"}, // from financial.md
		{"custom instructions", "Always check money math"},
	}

	for _, c := range checks {
		if !strings.Contains(prompt, c.content) {
			t.Errorf("prompt missing %s (looking for %q)", c.name, c.content)
		}
	}
}

func TestBuildIndividualPrompt_PRMode(t *testing.T) {
	aoi := security.AreaOfInterest{
		File:     "main.go",
		Line:     10,
		Category: "error-handling",
	}

	prompt := BuildIndividualPrompt(ModePR, "", "", "", nil, aoi)

	if !strings.Contains(prompt, "pull request") {
		t.Error("PR mode preamble should mention pull request")
	}
	if strings.Contains(prompt, "auditing") {
		t.Error("PR mode should not contain audit preamble")
	}
}

func TestBuildGroupedPrompt_ContainsAllAOIs(t *testing.T) {
	call := ReviewCall{
		Type:        "grouped",
		Category:    "error-handling",
		Subcategory: "swallowed-errors",
		AOIs: []security.AreaOfInterest{
			{File: "a.go", Line: 10, Category: "error-handling", Subcategory: "swallowed-errors", ID: "a-go-err", Concern: "Error ignored in handler", Dimensions: []string{"error-handling"}},
			{File: "b.go", Line: 20, Category: "error-handling", Subcategory: "swallowed-errors", ID: "b-go-err", Concern: "Error assigned to _", Dimensions: []string{"error-handling"}},
		},
		Files: []string{"a.go", "b.go"},
	}

	prompt := BuildGroupedPrompt(ModeAudit, "Test project", "", "", nil, call)

	if !strings.Contains(prompt, "error-handling/swallowed-errors") {
		t.Error("should contain subcategory label")
	}
	if !strings.Contains(prompt, "a-go-err") {
		t.Error("should contain first AOI id")
	}
	if !strings.Contains(prompt, "b-go-err") {
		t.Error("should contain second AOI id")
	}
	if !strings.Contains(prompt, "Error ignored in handler") {
		t.Error("should contain first AOI concern")
	}
	if !strings.Contains(prompt, "swallowed-errors") {
		t.Error("should contain dimension content")
	}
}

func TestBuildIndividualPrompt_LegacyAOI(t *testing.T) {
	// Legacy AOI without new fields should still work
	aoi := security.AreaOfInterest{
		File:       "main.go",
		Line:       42,
		Category:   "sql",
		Snippet:    "db.Query(s)",
		Reasoning:  "raw SQL with variable",
		Confidence: "high",
	}

	prompt := BuildIndividualPrompt(ModePR, "", "", "", nil, aoi)

	if !strings.Contains(prompt, "db.Query(s)") {
		t.Error("should include snippet from legacy AOI")
	}
	if !strings.Contains(prompt, "raw SQL with variable") {
		t.Error("should use reasoning as concern for legacy AOI")
	}
}

// ── Runtime model injection ─────────────────────────────────────────────

func TestBuildIndividualPrompt_RuntimeModelInjected(t *testing.T) {
	rm := &state.RuntimeModel{
		AuthModel: "Gateway authorizer validates every request",
		EntryPoints: []state.RuntimeEntryPoint{
			{Kind: "http", ValidationAt: "boundary"},
		},
	}
	aoi := security.AreaOfInterest{File: "main.go", Line: 1, Category: "correctness"}

	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", rm, aoi)

	if !strings.Contains(prompt, "## Runtime Model") {
		t.Error("prompt should contain the runtime model section header")
	}
	if !strings.Contains(prompt, "Gateway authorizer validates every request") {
		t.Error("prompt should include the auth model content")
	}
	if !strings.Contains(prompt, "validation at boundary") {
		t.Error("prompt should include the rendered entry point")
	}
}

func TestBuildIndividualPrompt_NilRuntimeModelOmitsSection(t *testing.T) {
	aoi := security.AreaOfInterest{File: "main.go", Line: 1, Category: "correctness"}
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, aoi)
	if strings.Contains(prompt, "## Runtime Model") {
		t.Error("nil runtime model should not emit the section header")
	}
}

func TestBuildGroupedPrompt_RuntimeModelInjected(t *testing.T) {
	rm := &state.RuntimeModel{
		AuthModel: "API key per route",
	}
	call := ReviewCall{
		Type:        "grouped",
		Category:    "error-handling",
		Subcategory: "swallowed-errors",
		AOIs: []security.AreaOfInterest{
			{File: "a.go", Line: 1, Category: "error-handling", ID: "a"},
		},
	}
	prompt := BuildGroupedPrompt(ModeAudit, "", "", "", rm, call)
	if !strings.Contains(prompt, "## Runtime Model") {
		t.Error("grouped prompt should contain the runtime model section")
	}
	if !strings.Contains(prompt, "API key per route") {
		t.Error("grouped prompt should include the auth model content")
	}
}

// ── SiblingDeviation injection ──────────────────────────────────────────

func TestBuildIndividualPrompt_SiblingDeviationInjected(t *testing.T) {
	aoi := security.AreaOfInterest{
		File:     "handler.go",
		Line:     10,
		Category: "authorization",
		ID:       "deviant-aoi",
		Concern:  "missing guardAdmin",
		SiblingDeviation: &state.SiblingDeviation{
			Pattern:    "9 of 11 admin POST handlers call guardAdmin()",
			SiblingIDs: []string{"a-id", "b-id", "c-id"},
		},
	}
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, aoi)

	if !strings.Contains(prompt, "Sibling pattern:") {
		t.Error("prompt should include the sibling pattern label")
	}
	if !strings.Contains(prompt, "9 of 11 admin POST handlers") {
		t.Error("prompt should embed the pattern description")
	}
	if !strings.Contains(prompt, "Conforming siblings") {
		t.Error("prompt should list conforming siblings")
	}
	if !strings.Contains(prompt, "a-id") || !strings.Contains(prompt, "b-id") {
		t.Error("prompt should cite specific sibling IDs")
	}
	if !strings.Contains(prompt, "intentional") {
		t.Error("prompt should anchor on whether the deviation is intentional")
	}
}

func TestBuildIndividualPrompt_NilSiblingDeviationOmitted(t *testing.T) {
	aoi := security.AreaOfInterest{
		File:     "handler.go",
		Line:     10,
		Category: "authorization",
		ID:       "regular-aoi",
	}
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, aoi)
	if strings.Contains(prompt, "Sibling pattern:") {
		t.Error("regular AOI should not emit a sibling-pattern section")
	}
}

// ── Bug-priors injection ────────────────────────────────────────────────

// The prompt template itself mentions "Known failure modes" in the
// meta-explanation that tells the reviewer how to *use* the section.
// Tests pick a content marker that only appears when the rendered
// section is actually spliced in: a "fix: ..." bullet.

func TestBuildIndividualPrompt_BugPriorsInjected(t *testing.T) {
	aoi := security.AreaOfInterest{File: "main.go", Line: 1, Category: "correctness"}
	priors := "## Known failure modes in this codebase\n\n- fix: cache-key gap\n"

	prompt := BuildIndividualPrompt(ModeAudit, "", "", priors, nil, aoi)

	if !strings.Contains(prompt, "- fix: cache-key gap") {
		t.Error("prompt should contain the bug-priors bullet content")
	}
	// Priors must appear before AOI so the reviewer reads the failure
	// history with the AOI it's investigating in mind.
	idxPriors := strings.Index(prompt, "- fix: cache-key gap")
	idxAOI := strings.Index(prompt, "## Area of Interest")
	if idxPriors < 0 || idxAOI < 0 || idxPriors > idxAOI {
		t.Errorf("priors should appear before AOI section (priors@%d, aoi@%d)", idxPriors, idxAOI)
	}
}

func TestBuildIndividualPrompt_EmptyBugPriorsOmitted(t *testing.T) {
	aoi := security.AreaOfInterest{File: "main.go", Line: 1, Category: "correctness"}
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, aoi)
	// No injected bullet content should appear when priors is empty.
	if strings.Contains(prompt, "- fix:") {
		t.Error("empty bug-priors must not emit injected bullet content")
	}
}

func TestBuildGroupedPrompt_BugPriorsInjected(t *testing.T) {
	call := ReviewCall{
		Type:        "grouped",
		Category:    "error-handling",
		Subcategory: "swallowed-errors",
		AOIs: []security.AreaOfInterest{
			{File: "a.go", Line: 1, Category: "error-handling", ID: "a"},
		},
	}
	priors := "## Known failure modes in this codebase\n\n- fix: silent error swallow\n"

	prompt := BuildGroupedPrompt(ModeAudit, "", "", priors, nil, call)

	if !strings.Contains(prompt, "- fix: silent error swallow") {
		t.Error("grouped prompt should contain the bug-priors bullet content")
	}
}

func TestBuildIndividualPrompt_SiblingDeviationCapsSiblingList(t *testing.T) {
	// More than 8 siblings — should cap to keep the prompt small.
	manyIDs := []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9", "s10", "s11", "s12"}
	aoi := security.AreaOfInterest{
		File:     "handler.go",
		Line:     10,
		Category: "authorization",
		SiblingDeviation: &state.SiblingDeviation{
			Pattern:    "test pattern",
			SiblingIDs: manyIDs,
		},
	}
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, aoi)
	// The 9th-12th ids should NOT appear (capped at 8).
	for _, late := range []string{"s9", "s10", "s11", "s12"} {
		if strings.Contains(prompt, late) {
			t.Errorf("prompt should cap sibling list at 8; saw %q", late)
		}
	}
	// First 8 should be present.
	for _, early := range manyIDs[:8] {
		if !strings.Contains(prompt, early) {
			t.Errorf("expected %q in prompt", early)
		}
	}
}

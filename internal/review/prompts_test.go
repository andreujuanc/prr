package review

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// individualCall wraps a single AOI into a ReviewCall suitable for
// BuildIndividualPrompt, keeping the tests concise.
func individualCall(aoi security.AreaOfInterest) ReviewCall {
	return ReviewCall{
		Type:        "individual",
		Category:    aoi.Category,
		Subcategory: aoi.Subcategory,
		AOIs:        []security.AreaOfInterest{aoi},
		Files:       []string{aoi.File},
	}
}

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
		Categories:  []string{"correctness", "financial"},
	}

	prompt := BuildIndividualPrompt(ModeAudit, "This is a billing system.", "Always check money math.", "", nil, individualCall(aoi))

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
		{"category content", "money-arithmetic"}, // from financial.md
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

	prompt := BuildIndividualPrompt(ModePR, "", "", "", nil, individualCall(aoi))

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
			{File: "a.go", Line: 10, Category: "error-handling", Subcategory: "swallowed-errors", ID: "a-go-err", Concern: "Error ignored in handler", Categories: []string{"error-handling"}},
			{File: "b.go", Line: 20, Category: "error-handling", Subcategory: "swallowed-errors", ID: "b-go-err", Concern: "Error assigned to _", Categories: []string{"error-handling"}},
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
		t.Error("should contain category content")
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

	prompt := BuildIndividualPrompt(ModePR, "", "", "", nil, individualCall(aoi))

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

	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", rm, individualCall(aoi))

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
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, individualCall(aoi))
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
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, individualCall(aoi))

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
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, individualCall(aoi))
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

	prompt := BuildIndividualPrompt(ModeAudit, "", "", priors, nil, individualCall(aoi))

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
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, individualCall(aoi))
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

// ── Code-context section ────────────────────────────────────────────────

func TestBuildIndividualPrompt_PRDiffSection(t *testing.T) {
	aoi := security.AreaOfInterest{File: "a.go", Line: 1, Category: "correctness"}
	call := ReviewCall{
		Type:  "individual",
		AOIs:  []security.AreaOfInterest{aoi},
		Files: []string{"a.go"},
		FileDiffs: map[string]string{
			"a.go": "@@ -1,1 +1,1 @@\n-old\n+new\n",
		},
	}
	prompt := BuildIndividualPrompt(ModePR, "", "", "", nil, call)

	if !strings.Contains(prompt, "## Changes in This File\n\n```diff") {
		t.Error("PR mode should render the Changes in This File section with a diff fence")
	}
	if !strings.Contains(prompt, "+new") {
		t.Error("PR mode should inline the diff content")
	}
	if strings.Contains(prompt, "## Source Around This AOI\n\n```") {
		t.Error("PR mode should not render the audit-mode source section")
	}
}

func TestBuildIndividualPrompt_AuditSourceSection(t *testing.T) {
	aoi := security.AreaOfInterest{File: "a.go", Line: 5, Category: "correctness"}
	call := ReviewCall{
		Type:       "individual",
		AOIs:       []security.AreaOfInterest{aoi},
		Files:      []string{"a.go"},
		AOISources: []string{"  5  the line of interest\n"},
	}
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, call)

	if !strings.Contains(prompt, "## Source Around This AOI\n\n```") {
		t.Error("audit mode should render the Source Around This AOI section")
	}
	if !strings.Contains(prompt, "the line of interest") {
		t.Error("audit mode should inline the source slice")
	}
	if strings.Contains(prompt, "## Changes in This File\n\n```") {
		t.Error("audit mode should not render the PR-mode diff section")
	}
}

func TestBuildIndividualPrompt_NoContextOmitsSection(t *testing.T) {
	aoi := security.AreaOfInterest{File: "a.go", Line: 1, Category: "correctness"}
	call := individualCall(aoi)
	prompt := BuildIndividualPrompt(ModePR, "", "", "", nil, call)

	if strings.Contains(prompt, "## Changes in This File\n\n```") {
		t.Error("empty FileDiffs should not render the Changes section")
	}
	if strings.Contains(prompt, "## Source Around This AOI\n\n```") {
		t.Error("empty AOISources should not render the Source section")
	}
}

func TestBuildGroupedPrompt_PRDiffsSection(t *testing.T) {
	call := ReviewCall{
		Type:        "grouped",
		Category:    "error-handling",
		Subcategory: "swallowed-errors",
		AOIs: []security.AreaOfInterest{
			{File: "a.go", Line: 10, Category: "error-handling"},
			{File: "b.go", Line: 20, Category: "error-handling"},
		},
		Files: []string{"a.go", "b.go"},
		FileDiffs: map[string]string{
			"a.go": "@@ -10 +10 @@\n-x\n+y",
			"b.go": "@@ -20 +20 @@\n-p\n+q",
		},
	}
	prompt := BuildGroupedPrompt(ModePR, "", "", "", nil, call)

	if !strings.Contains(prompt, "## Changes Under Review\n\n###") {
		t.Error("grouped PR prompt should render Changes Under Review with per-file blocks")
	}
	if !strings.Contains(prompt, "### a.go") || !strings.Contains(prompt, "### b.go") {
		t.Error("grouped PR prompt should list each file under its own header")
	}
	if !strings.Contains(prompt, "+y") || !strings.Contains(prompt, "+q") {
		t.Error("grouped PR prompt should inline both file diffs")
	}
}

func TestBuildGroupedPrompt_AuditInlineSource(t *testing.T) {
	call := ReviewCall{
		Type:        "grouped",
		Category:    "error-handling",
		Subcategory: "swallowed-errors",
		AOIs: []security.AreaOfInterest{
			{File: "a.go", Line: 10, Category: "error-handling"},
			{File: "b.go", Line: 20, Category: "error-handling"},
		},
		Files:      []string{"a.go", "b.go"},
		AOISources: []string{"src around a\n", "src around b\n"},
	}
	prompt := BuildGroupedPrompt(ModeAudit, "", "", "", nil, call)

	if !strings.Contains(prompt, "Source around this AOI") {
		t.Error("grouped audit prompt should include per-AOI source markers")
	}
	if !strings.Contains(prompt, "src around a") || !strings.Contains(prompt, "src around b") {
		t.Error("grouped audit prompt should inline each AOI's source slice")
	}
	if strings.Contains(prompt, "## Changes Under Review\n\n###") {
		t.Error("grouped audit prompt should not render the PR diffs section")
	}
}

func TestRenderCodeContext_TruncatesLongPRDiff(t *testing.T) {
	// Build a diff with more lines than the cap, each line distinct so
	// we can assert exactly which lines survived the truncation.
	const overflow = 50
	lines := make([]string, maxDiffLinesPerFile+overflow)
	for i := range lines {
		lines[i] = fmt.Sprintf("+line%d", i)
	}
	bigDiff := strings.Join(lines, "\n")

	call := ReviewCall{
		Type:      "individual",
		AOIs:      []security.AreaOfInterest{{File: "a.go", Line: 1}},
		Files:     []string{"a.go"},
		FileDiffs: map[string]string{"a.go": bigDiff},
	}
	section := renderCodeContext(ModePR, call)
	if !strings.Contains(section, "truncated") {
		t.Errorf("expected truncation hint in section; got:\n%s", section)
	}
	if !strings.Contains(section, "git_diff") {
		t.Errorf("expected pointer to git_diff tool in truncation hint")
	}
	// Content guarantee: the first line must survive, but any line at
	// or past the cap must be dropped. Without these checks a broken
	// capDiffLines that appends the hint but keeps all lines would
	// still pass the substring checks above.
	if !strings.Contains(section, "+line0") {
		t.Errorf("expected first line to survive truncation; got:\n%s", section)
	}
	for _, dropped := range []int{maxDiffLinesPerFile, maxDiffLinesPerFile + overflow - 1} {
		marker := fmt.Sprintf("+line%d", dropped)
		if strings.Contains(section, marker) {
			t.Errorf("expected line %d to be truncated, but it survived; got:\n%s", dropped, section)
		}
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
	prompt := BuildIndividualPrompt(ModeAudit, "", "", "", nil, individualCall(aoi))
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

package review

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/security"
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

	prompt := BuildIndividualPrompt(ModeAudit, "This is a billing system.", "Always check money math.", aoi)

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

	prompt := BuildIndividualPrompt(ModePR, "", "", aoi)

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

	prompt := BuildGroupedPrompt(ModeAudit, "Test project", "", call)

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

	prompt := BuildIndividualPrompt(ModePR, "", "", aoi)

	if !strings.Contains(prompt, "db.Query(s)") {
		t.Error("should include snippet from legacy AOI")
	}
	if !strings.Contains(prompt, "raw SQL with variable") {
		t.Error("should use reasoning as concern for legacy AOI")
	}
}

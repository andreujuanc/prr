package review

import (
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// Mode distinguishes between PR review and audit mode for prompt framing.
type Mode string

const (
	ModePR    Mode = "pr"
	ModeAudit Mode = "audit"
)

// BuildIndividualPrompt composes the system prompt for reviewing a single AOI.
// runtimeModel may be nil — when present, a `## Runtime Model` section is
// appended after the project context.
func BuildIndividualPrompt(mode Mode, projectContext, customInstructions, bugPriors string, runtimeModel *state.RuntimeModel, aoi security.AreaOfInterest) string {
	var sb strings.Builder

	// Mode-specific preamble
	switch mode {
	case ModePR:
		sb.WriteString("You are reviewing a specific concern flagged in a pull request.\n")
		sb.WriteString("Focus on whether this change introduces a new issue.\n\n")
	case ModeAudit:
		sb.WriteString("You are auditing source code for latent issues.\n")
		sb.WriteString("This is not a PR review — the code may have existed for a long time.\n")
		sb.WriteString("Focus on whether the concern is a real, current problem.\n\n")
	}

	// Base prompt
	sb.WriteString(ai.ReviewIndividualPrompt)

	// Project context
	if projectContext != "" {
		appendProjectContext(&sb, projectContext)
	}

	// Runtime model — appended after project context so the reviewer
	// sees the structured shape after the prose briefing.
	if rendered := runtimeModel.Render(); rendered != "" {
		sb.WriteString("\n\n")
		sb.WriteString(rendered)
	}

	// Bug-priors — codebase-specific failure history mined from
	// fix-shaped commits. Appended after runtime model so the reviewer
	// sees what this codebase has actually shipped right before the
	// AOI it's about to investigate. Empty string skips the section.
	if bugPriors != "" {
		sb.WriteString("\n\n")
		sb.WriteString(bugPriors)
	}

	// AOI details
	sb.WriteString("\n\n## Area of Interest\n\n")
	sb.WriteString(formatAOI(aoi))

	// Relevant dimension criteria
	dims := relevantDimensions(aoi)
	if len(dims) > 0 {
		sb.WriteString("\n\n## Evaluation Criteria\n\n")
		sb.WriteString(ai.GetDimensions(dims))
	}

	// Custom instructions
	if customInstructions != "" {
		sb.WriteString("\n\n## Project-Specific Instructions\n\n")
		sb.WriteString(customInstructions)
	}

	return sb.String()
}

// BuildGroupedPrompt composes the system prompt for reviewing a subcategory group.
// runtimeModel may be nil — when present, a `## Runtime Model` section is
// appended after the project context.
func BuildGroupedPrompt(mode Mode, projectContext, customInstructions, bugPriors string, runtimeModel *state.RuntimeModel, call ReviewCall) string {
	var sb strings.Builder

	// Mode-specific preamble
	switch mode {
	case ModePR:
		sb.WriteString("You are reviewing related concerns flagged in a pull request.\n")
		sb.WriteString("Focus on whether these changes introduce new issues.\n\n")
	case ModeAudit:
		sb.WriteString("You are auditing source code for latent issues.\n")
		sb.WriteString("This is not a PR review — the code may have existed for a long time.\n")
		sb.WriteString("Focus on whether these concerns are real, current problems.\n\n")
	}

	// Base prompt
	sb.WriteString(ai.ReviewGroupedPrompt)

	// Project context
	if projectContext != "" {
		appendProjectContext(&sb, projectContext)
	}

	// Runtime model — appended after project context.
	if rendered := runtimeModel.Render(); rendered != "" {
		sb.WriteString("\n\n")
		sb.WriteString(rendered)
	}

	// Bug-priors — codebase-specific failure history mined from
	// fix-shaped commits. Appended after runtime model, before AOIs.
	if bugPriors != "" {
		sb.WriteString("\n\n")
		sb.WriteString(bugPriors)
	}

	// AOI list
	subcatLabel := call.Category
	if call.Subcategory != "" {
		subcatLabel = call.Category + "/" + call.Subcategory
	}
	sb.WriteString(fmt.Sprintf("\n\n## Areas of Interest (%s)\n\n", subcatLabel))
	for i, aoi := range call.AOIs {
		sb.WriteString(fmt.Sprintf("%d. %s\n\n", i+1, formatAOI(aoi)))
	}

	// Relevant dimension criteria — collect from all AOIs in the group
	dims := relevantDimensionsFromGroup(call.AOIs)
	if len(dims) > 0 {
		sb.WriteString("\n\n## Evaluation Criteria\n\n")
		sb.WriteString(ai.GetDimensions(dims))
	}

	// Custom instructions
	if customInstructions != "" {
		sb.WriteString("\n\n## Project-Specific Instructions\n\n")
		sb.WriteString(customInstructions)
	}

	return sb.String()
}

// formatAOI formats a single AOI for inclusion in a prompt.
func formatAOI(aoi security.AreaOfInterest) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("**File:** %s\n", aoi.File))

	if aoi.EndLine > 0 && aoi.EndLine != aoi.Line {
		sb.WriteString(fmt.Sprintf("**Lines:** %d-%d\n", aoi.Line, aoi.EndLine))
	} else {
		sb.WriteString(fmt.Sprintf("**Line:** %d\n", aoi.Line))
	}

	if aoi.Category != "" {
		cat := aoi.Category
		if aoi.Subcategory != "" {
			cat += " / " + aoi.Subcategory
		}
		sb.WriteString(fmt.Sprintf("**Category:** %s\n", cat))
	}

	if aoi.ID != "" {
		sb.WriteString(fmt.Sprintf("**ID:** %s\n", aoi.ID))
	}

	// Use new-format fields, fall back to legacy
	concern := aoi.Concern
	if concern == "" {
		concern = aoi.Reasoning
	}
	if concern != "" {
		sb.WriteString(fmt.Sprintf("**Concern:** %s\n", concern))
	}

	if aoi.Context != "" {
		sb.WriteString(fmt.Sprintf("**Context:** %s\n", aoi.Context))
	}

	if aoi.Snippet != "" {
		sb.WriteString(fmt.Sprintf("**Code:** `%s`\n", aoi.Snippet))
	}

	// Sibling deviation: when this AOI was synthesized by Phase 2.5
	// clustering, surface the conforming pattern and the sibling
	// references so the reviewer can frame the investigation around
	// "is this deviation intentional or a bug?" rather than judging
	// the line in isolation.
	if aoi.SiblingDeviation != nil {
		sb.WriteString("\n**Sibling pattern:** ")
		sb.WriteString(strings.TrimSpace(aoi.SiblingDeviation.Pattern))
		sb.WriteString("\n")
		if ids := aoi.SiblingDeviation.SiblingIDs; len(ids) > 0 {
			capped := ids
			if len(capped) > 8 {
				capped = capped[:8]
			}
			sb.WriteString(fmt.Sprintf("**Conforming siblings (compare against):** %s\n", strings.Join(capped, ", ")))
		}
		sb.WriteString("**Anchor the investigation on:** Is this deviation intentional (note in nearby code, different invariant, deliberate exception) or is it a bug? Read both this code and at least one conforming sibling before concluding.\n")
	}

	return sb.String()
}

// relevantDimensions returns the dimension slugs to include for a single AOI.
// Uses the AOI's dimensions field, falling back to the category itself.
func relevantDimensions(aoi security.AreaOfInterest) []string {
	if len(aoi.Dimensions) > 0 {
		// Deduplicate and filter to valid dimensions
		seen := make(map[string]bool)
		var result []string
		for _, d := range aoi.Dimensions {
			if !seen[d] && ai.DimensionExists(d) {
				seen[d] = true
				result = append(result, d)
			}
		}
		return result
	}

	// Fallback: use the category as dimension if it exists
	if ai.DimensionExists(aoi.Category) {
		return []string{aoi.Category}
	}
	return nil
}

// relevantDimensionsFromGroup collects all relevant dimensions across a group of AOIs.
func relevantDimensionsFromGroup(aois []security.AreaOfInterest) []string {
	seen := make(map[string]bool)
	var result []string
	for _, aoi := range aois {
		for _, d := range relevantDimensions(aoi) {
			if !seen[d] {
				seen[d] = true
				result = append(result, d)
			}
		}
	}
	return result
}

// appendProjectContext writes the project context section, avoiding a
// duplicate "## Project Context" header if the value already includes one.
func appendProjectContext(sb *strings.Builder, pc string) {
	if strings.HasPrefix(strings.TrimSpace(pc), "## Project Context") {
		sb.WriteString("\n\n")
		sb.WriteString(pc)
	} else {
		sb.WriteString("\n\n## Project Context\n\n")
		sb.WriteString(pc)
	}
}

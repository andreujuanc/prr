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
//
// The call carries the single AOI under call.AOIs[0] plus the code
// context attached upstream by AttachFileDiffs (PR mode) or
// AttachAOISources (audit mode). Either may be empty; the prompt
// falls back to "use tools to read the code" in that case.
func BuildIndividualPrompt(mode Mode, projectContext, customInstructions, bugPriors string, runtimeModel *state.RuntimeModel, call ReviewCall) string {
	// Contract: individual calls always carry exactly one AOI. RouteAOIs
	// guarantees this. A panic here surfaces a routing bug in tests
	// rather than burying it as a wasted LLM call with an empty prompt.
	aoi := call.AOIs[0]
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

	// Base prompt — {{REVIEW_COMMON}} is substituted with the shared
	// defenses/trace/severity sections so individual and grouped prompts
	// stay in lockstep.
	sb.WriteString(strings.Replace(ai.ReviewIndividualPrompt, "{{REVIEW_COMMON}}", ai.ReviewCommonPrompt, 1))

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

	// Code context — diff for PR, source slice for audit. Gives the
	// model the actual changed lines / surrounding source without a
	// mandatory tool call.
	if section := renderCodeContext(mode, call); section != "" {
		sb.WriteString("\n\n")
		sb.WriteString(section)
	}

	// Relevant category criteria
	cats := relevantCategories(aoi)
	if len(cats) > 0 {
		sb.WriteString("\n\n## Evaluation Criteria\n\n")
		sb.WriteString(ai.GetCategories(cats))
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

	// Base prompt — {{REVIEW_COMMON}} is substituted with the shared
	// defenses/trace/severity sections so individual and grouped prompts
	// stay in lockstep.
	sb.WriteString(strings.Replace(ai.ReviewGroupedPrompt, "{{REVIEW_COMMON}}", ai.ReviewCommonPrompt, 1))

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

	// AOI list. In audit mode, attach per-AOI source context inline
	// with each AOI so the model sees the code right next to the
	// concern. In PR mode, render the file diffs once as a separate
	// section below (one diff per file in the group, not per AOI).
	subcatLabel := call.Category
	if call.Subcategory != "" {
		subcatLabel = call.Category + "/" + call.Subcategory
	}
	sb.WriteString(fmt.Sprintf("\n\n## Areas of Interest (%s)\n\n", subcatLabel))
	for i, aoi := range call.AOIs {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, formatAOI(aoi)))
		if mode == ModeAudit && i < len(call.AOISources) && call.AOISources[i] != "" {
			sb.WriteString("\n**Source around this AOI:**\n\n```\n")
			sb.WriteString(call.AOISources[i])
			sb.WriteString("```\n")
		}
		sb.WriteString("\n")
	}

	// PR mode: render the file diffs in a single section below the
	// AOI list (deduped across AOIs in the group).
	if mode == ModePR && len(call.FileDiffs) > 0 {
		sb.WriteString("\n\n")
		sb.WriteString(renderPRDiffsSection(call))
	}

	// Relevant category criteria — collect from all AOIs in the group
	cats := relevantCategoriesFromGroup(call.AOIs)
	if len(cats) > 0 {
		sb.WriteString("\n\n## Evaluation Criteria\n\n")
		sb.WriteString(ai.GetCategories(cats))
	}

	// Custom instructions
	if customInstructions != "" {
		sb.WriteString("\n\n## Project-Specific Instructions\n\n")
		sb.WriteString(customInstructions)
	}

	return sb.String()
}

// maxDiffLinesPerFile caps how many diff lines per file are inlined
// into a deep review prompt. Beyond this the tail is truncated and a
// hint points the model at git_diff for the rest. Picked generously —
// most file diffs are well under this, and the hint covers the rest.
const maxDiffLinesPerFile = 800

// renderCodeContext returns the code-context section for an individual
// review call. PR mode renders the diff for the AOI's file; audit
// mode renders the AOI's source slice. Returns empty string when no
// context is available (caller skips the section entirely).
func renderCodeContext(mode Mode, call ReviewCall) string {
	if len(call.AOIs) == 0 {
		return ""
	}
	aoi := call.AOIs[0]

	switch mode {
	case ModePR:
		diff, ok := call.FileDiffs[aoi.File]
		if !ok || diff == "" {
			return ""
		}
		var sb strings.Builder
		sb.WriteString("## Changes in This File\n\n")
		sb.WriteString("```diff\n")
		sb.WriteString(capDiffLines(diff, maxDiffLinesPerFile, aoi.File))
		if !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("```\n")
		return sb.String()
	case ModeAudit:
		if len(call.AOISources) == 0 || call.AOISources[0] == "" {
			return ""
		}
		var sb strings.Builder
		sb.WriteString("## Source Around This AOI\n\n")
		sb.WriteString("```\n")
		sb.WriteString(call.AOISources[0])
		if !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("```\n")
		return sb.String()
	}
	return ""
}

// renderPRDiffsSection renders one diff block per file in a grouped
// PR-mode call. Deduplicates by file path.
func renderPRDiffsSection(call ReviewCall) string {
	if len(call.FileDiffs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Changes Under Review\n\n")
	// Iterate in Files order for deterministic output. Skip files
	// missing from the diff map (e.g. cap'd by AttachFileDiffs).
	for _, f := range call.Files {
		diff, ok := call.FileDiffs[f]
		if !ok || diff == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("### %s\n\n", f))
		sb.WriteString("```diff\n")
		sb.WriteString(capDiffLines(diff, maxDiffLinesPerFile, f))
		if !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("```\n\n")
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

// capDiffLines truncates a diff to a max line count, appending a hint
// pointing the model at git_diff for the remainder. Mirrors the
// CapDiff pattern in internal/review/batch.go but emits a per-file
// hint rather than a per-batch one.
func capDiffLines(diff string, maxLines int, file string) string {
	if maxLines <= 0 {
		return diff
	}
	lines := strings.Split(diff, "\n")
	if len(lines) <= maxLines {
		return diff
	}
	dropped := len(lines) - maxLines
	return strings.Join(lines[:maxLines], "\n") +
		fmt.Sprintf("\n... [%d more lines truncated — use the git_diff tool with path=%q to see the rest]\n",
			dropped, file)
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

// relevantCategories returns the category slugs to include for a single AOI.
// One AOI = one category — see the AOI scan prompt's "One AOI = one
// category" rule. Returns the AOI's category as a single-element slice
// when it's in the canonical taxonomy, or nil otherwise.
func relevantCategories(aoi security.AreaOfInterest) []string {
	if ai.CategoryExists(aoi.Category) {
		return []string{aoi.Category}
	}
	return nil
}

// relevantCategoriesFromGroup collects all relevant categories across a group of AOIs.
func relevantCategoriesFromGroup(aois []security.AreaOfInterest) []string {
	seen := make(map[string]bool)
	var result []string
	for _, aoi := range aois {
		for _, c := range relevantCategories(aoi) {
			if !seen[c] {
				seen[c] = true
				result = append(result, c)
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

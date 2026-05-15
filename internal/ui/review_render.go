package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/andreujuanc/prr/internal/state"

	"github.com/charmbracelet/lipgloss"
)

// buildSyntheticReviewFromDeepFindings produces a ReviewOutput from
// persisted Phase 1+1c deep findings, so the Review tab can render them
// via the existing renderStructuredReview path without needing
// synthesis to have run.
//
// Mapping (lossy where DeepFinding has richer info than ReviewFinding):
//   - Severity, Category, File, Title, Suggestion: direct.
//   - Lines (string, e.g. "45-62") → Line (int): parsed first integer.
//   - Description → Detail.
//   - Other DeepFinding fields (Evidence, Trigger, Subcategory,
//     Dimension, AOIID, FindingID) are dropped — the UI doesn't render
//     them yet and the navigable Review tab only needs the basics.
func buildSyntheticReviewFromDeepFindings(deep []state.DeepFinding) *state.ReviewOutput {
	out := &state.ReviewOutput{
		Verdict:  "comment",
		Findings: make([]state.ReviewFinding, 0, len(deep)),
	}
	for _, d := range deep {
		out.Findings = append(out.Findings, state.ReviewFinding{
			Severity:            d.Severity,
			Category:            d.Category,
			File:                d.File,
			Line:                firstInt(d.Lines),
			Title:               d.Title,
			Detail:              d.Description,
			Suggestion:          d.Suggestion,
			ConfidenceScore:     d.ConfidenceScore,
			ConfidenceReasoning: d.ConfidenceReasoning,
		})
	}
	return out
}

// firstInt parses the leading integer from a string like "45" or
// "45-62". Returns 0 if no digits start the string.
//
// strconv.Atoi guards against integer overflow — without it, a
// pathological "9999999999999999999" from a malformed LLM response
// would silently wrap to a negative line number.
func firstInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

// renderStructuredReview renders a ReviewOutput as styled, grouped text
// for display in the AI panel's Review tab.
//
// cursor is the index of the currently selected finding (-1 = none).
// expanded maps finding index → true for findings whose body (file:line,
// detail, suggestion) should be rendered. Nil/missing entries render as
// collapsed (header only). Resolved findings are always single-line
// regardless of the expansion map.
// stale indicates the review was generated against a different set of diffs.
// Returns the rendered string and a flat ordered list of findings matching
// the render order (for navigable finding selection).
func renderStructuredReview(review *state.ReviewOutput, width int, cursor int, expanded map[int]bool, stale bool) (string, []state.ReviewFinding) {
	if review == nil {
		return "", nil
	}

	if width < 20 {
		width = 20
	}

	var b strings.Builder
	var orderedFindings []state.ReviewFinding

	// ── Stale banner ────────────────────────────────────────
	if stale {
		banner := styleStaleReview.Render("STALE — diffs have changed since this review was generated")
		hint := styleTextMuted.Render("  press a to re-review, or A to force re-review (no cache)")
		b.WriteString(banner)
		b.WriteString("\n")
		b.WriteString(hint)
		b.WriteString("\n\n")
	}

	// ── Header: verdict + summary ────────────────────────────
	verdictLabel, verdictStyle := formatVerdict(review.Verdict)
	b.WriteString(verdictStyle.Render(verdictLabel))
	b.WriteString("\n\n")

	// Summary paragraph
	if review.Summary != "" {
		b.WriteString(wrapStyled(styleTextSecondary, review.Summary, width))
		b.WriteString("\n\n")
	}

	// ── Findings grouped by severity ─────────────────────────
	if len(review.Findings) > 0 {
		b.WriteString(styleTextSubtle.Render(strings.Repeat("─", width)))
		b.WriteString("\n")

		// Group findings by severity
		groups := groupBySeverity(review.Findings)

		severityOrder := []string{"critical", "high", "medium", "low", "nit"}
		findingIdx := 0
		for _, sev := range severityOrder {
			findings, ok := groups[sev]
			if !ok || len(findings) == 0 {
				continue
			}

			// Section header with count
			sevStyle := severityStyle(sev)
			sevLabel := strings.ToUpper(sev)
			header := fmt.Sprintf("%s (%d)", sevLabel, len(findings))
			b.WriteString("\n")
			b.WriteString(sevStyle.Render(header))
			b.WriteString("\n\n")

			for _, f := range findings {
				isSelected := findingIdx == cursor
				isExpanded := expanded[findingIdx]
				renderFindingHeader(&b, f, width, isSelected, isExpanded)
				if isExpanded {
					renderFindingBody(&b, f, width)
				}
				// Trailing blank line between findings, matches the
				// previous (non-collapsible) renderer's rhythm so the
				// visual density when everything is expanded is
				// unchanged.
				b.WriteString("\n")
				orderedFindings = append(orderedFindings, f)
				findingIdx++
			}
		}
	} else {
		b.WriteString("\n")
		b.WriteString(styleAccentGreen.Render("No findings — PR looks clean."))
		b.WriteString("\n")
	}

	// ── Missing tests ────────────────────────────────────────
	if len(review.MissingTests) > 0 {
		b.WriteString("\n")
		b.WriteString(styleTextSubtle.Render(strings.Repeat("─", width)))
		b.WriteString("\n\n")
		b.WriteString(styleAccentYellowBold.Render("MISSING TESTS"))
		b.WriteString("\n\n")
		for _, mt := range review.MissingTests {
			b.WriteString(styleTextMuted.Render("  - "))
			b.WriteString(wrapStyled(styleTextSecondary, mt, width-4))
			b.WriteString("\n")
		}
	}

	// ── Questions for author ─────────────────────────────────
	if len(review.QuestionsForAuthor) > 0 {
		b.WriteString("\n")
		b.WriteString(styleTextSubtle.Render(strings.Repeat("─", width)))
		b.WriteString("\n\n")
		b.WriteString(styleAccentMauveBold.Render("QUESTIONS FOR AUTHOR"))
		b.WriteString("\n\n")
		for _, q := range review.QuestionsForAuthor {
			b.WriteString(styleTextMuted.Render("  ? "))
			b.WriteString(wrapStyled(styleTextSecondary, q, width-4))
			b.WriteString("\n")
		}
	}

	return b.String(), orderedFindings
}

// renderFindingHeader renders the always-visible part of a finding:
// the meta line (cursor marker, resolved-✓, severity badge, category
// tag, expand/collapse indicator) followed by the full title wrapped
// across as many lines as needed.
//
// The earlier version composed marker+badge+cat+title into a single
// string and truncated to viewport width — long titles got chopped
// off-screen. lipgloss' Width().Render() handles word wrapping while
// preserving style across continuation lines, so we now wrap cleanly
// with the continuation indented under the title.
func renderFindingHeader(b *strings.Builder, f state.ReviewFinding, width int, isSelected, expanded bool) {
	sevStyle := severityStyle(f.Severity)
	catStyle := categoryStyle(f.Category)

	// Resolved prefix
	resolvedPrefix := ""
	if f.Resolved {
		resolvedPrefix = styleAccentGreen.Render("✓ ")
	}

	// Expand/collapse indicator — only shown for non-resolved findings
	// (resolved are always single-line). This chevron now signals only
	// expand state; row selection is conveyed by SelectableRow's left bar
	// and background fill, so the previous double-meaning of "▸" is gone.
	expandIndicator := ""
	if !f.Resolved {
		if expanded {
			expandIndicator = styleTextMuted.Render("▾ ")
		} else {
			expandIndicator = styleTextMuted.Render("▸ ")
		}
	}

	badge := sevStyle.Render(fmt.Sprintf("[%s]", f.Severity))
	cat := catStyle.Render(fmt.Sprintf("[%s]", f.Category))
	titleStyle := styleTextPrimary.Bold(true)
	if f.Resolved {
		// Dim everything for resolved findings
		badge = styleTextMuted.Render(fmt.Sprintf("[%s]", f.Severity))
		cat = styleTextMuted.Render(fmt.Sprintf("[%s]", f.Category))
		titleStyle = styleTextMuted
	}

	// Meta prefix: resolved + expand + badge + cat. SelectableRow provides
	// the cursor affordance (left bar + background), so no marker glyph.
	prefix := fmt.Sprintf("%s%s%s %s", resolvedPrefix, expandIndicator, badge, cat)

	// SelectableRow consumes 2 outer cells (bar + gap). The content we
	// hand it must fit in width-2.
	contentW := max(width-2, 20)
	prefixVisible := lipgloss.Width(prefix)
	titleAvailable := max(
		// -1 for the space separator
		contentW-prefixVisible-1, 10)

	var rawLines []string
	titleVisible := lipgloss.Width(f.Title)
	if titleVisible <= titleAvailable {
		rawLines = append(rawLines, prefix+" "+titleStyle.Render(f.Title))
	} else {
		rawLines = append(rawLines, prefix)
		indent := strings.Repeat(" ", lipgloss.Width(resolvedPrefix))
		wrapWidth := max(contentW-lipgloss.Width(indent), 10)
		wrapped := titleStyle.Width(wrapWidth).Render(f.Title)
		for line := range strings.SplitSeq(wrapped, "\n") {
			rawLines = append(rawLines, indent+line)
		}
	}

	for _, line := range rawLines {
		b.WriteString(SelectableRow(line, width, isSelected) + "\n")
	}
}

// renderFindingBody renders the expand-only portion of a finding:
// file:line reference, detail prose, and suggestion. Skipped entirely
// for resolved findings (they're always single-line).
func renderFindingBody(b *strings.Builder, f state.ReviewFinding, width int) {
	if f.Resolved {
		return
	}

	// File:line reference
	if f.File != "" {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		b.WriteString("  " + styleFileLine.Render(loc) + "\n")
	}

	// Detail — wrapped
	if f.Detail != "" {
		b.WriteString(wrapStyled(styleTextSecondary, "  "+f.Detail, width-2))
		b.WriteString("\n")
	}

	// Suggestion — visually distinct
	if f.Suggestion != "" {
		b.WriteString(styleAccentGreen.Render("  > "))
		b.WriteString(wrapStyled(styleTextSecondary, f.Suggestion, width-4))
		b.WriteString("\n")
	}
}

// groupBySeverity groups findings by their severity level.
func groupBySeverity(findings []state.ReviewFinding) map[string][]state.ReviewFinding {
	groups := make(map[string][]state.ReviewFinding)
	for _, f := range findings {
		groups[f.Severity] = append(groups[f.Severity], f)
	}
	return groups
}

// severityStyle returns the lipgloss style for a severity level.
func severityStyle(sev string) lipgloss.Style {
	switch sev {
	case "critical":
		return styleSeverityCritical
	case "high":
		return styleSeverityHigh
	case "medium":
		return styleSeverityMedium
	case "low":
		return styleSeverityLow
	case "nit":
		return styleSeverityNit
	default:
		return styleTextMuted
	}
}

// categoryStyle returns a lipgloss style for a finding category.
func categoryStyle(cat string) lipgloss.Style {
	switch cat {
	case "bug":
		return lipgloss.NewStyle().Foreground(accentRed)
	case "security":
		return lipgloss.NewStyle().Foreground(accentPeach)
	case "performance":
		return lipgloss.NewStyle().Foreground(accentYellow)
	case "testing":
		return lipgloss.NewStyle().Foreground(accentMauve)
	case "style":
		return lipgloss.NewStyle().Foreground(textMuted)
	case "architecture":
		return lipgloss.NewStyle().Foreground(accentBlue)
	case "docs":
		return lipgloss.NewStyle().Foreground(textSecondary)
	default:
		return styleTextMuted
	}
}

// formatVerdict returns the display label and style for a verdict string.
func formatVerdict(verdict string) (string, lipgloss.Style) {
	switch verdict {
	case "approve":
		return "APPROVED", styleVerdictApprove
	case "request_changes":
		return "CHANGES REQUESTED", styleVerdictChanges
	case "comment":
		return "COMMENT", styleVerdictComment
	default:
		return strings.ToUpper(verdict), styleTextMuted
	}
}

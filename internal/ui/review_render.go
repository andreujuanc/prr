package ui

import (
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/state"

	"github.com/charmbracelet/lipgloss"
)

// renderStructuredReview renders a ReviewOutput as styled, grouped text
// for display in the AI panel's Review tab.
//
// cursor is the index of the currently selected finding (-1 = none).
// stale indicates the review was generated against a different set of diffs.
// Returns the rendered string and a flat ordered list of findings matching
// the render order (for navigable finding selection).
func renderStructuredReview(review *state.ReviewOutput, width int, cursor int, stale bool) (string, []state.ReviewFinding) {
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
				renderFinding(&b, f, width, isSelected)
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

// renderFinding renders a single ReviewFinding as styled text.
// When isSelected is true, the finding is highlighted with a cursor indicator.
func renderFinding(b *strings.Builder, f state.ReviewFinding, width int, isSelected bool) {
	sevStyle := severityStyle(f.Severity)
	catStyle := categoryStyle(f.Category)

	// Cursor indicator
	marker := "  "
	if isSelected {
		marker = styleAccentBlueBold.Render("▸ ")
	}

	// Severity badge + category tag + title
	badge := sevStyle.Render(fmt.Sprintf("[%s]", f.Severity))
	cat := catStyle.Render(fmt.Sprintf("[%s]", f.Category))
	title := styleTextPrimary.Bold(true).Render(f.Title)

	line := fmt.Sprintf("%s%s %s %s", marker, badge, cat, title)
	// Truncate to prevent wrapping inside bordered panel (Golden Rule 2)
	if width > 0 {
		line = truncateToWidth(line, width)
	}
	b.WriteString(line + "\n")

	// File:line reference
	if f.File != "" {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fileLine := "  " + styleFileLine.Render(loc)
		if width > 0 {
			fileLine = truncateToWidth(fileLine, width)
		}
		b.WriteString(fileLine + "\n")
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

	b.WriteString("\n")
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

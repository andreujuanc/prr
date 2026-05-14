package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/state"
	"github.com/charmbracelet/lipgloss"
)

// ── Severity colors ─────────────────────────────────────────────────────

// findingSeverityColor returns the border color for a finding's severity.
func findingSeverityColor(severity string) lipgloss.Color {
	switch severity {
	case "critical":
		return accentRed
	case "high":
		return accentPeach
	case "medium":
		return accentYellow
	case "low":
		return accentBlue
	case "nit":
		return textMuted
	default:
		return textSubtle
	}
}

// ── Inline finding box ──────────────────────────────────────────────────

// findingStyles holds pre-computed styles for rendering inline finding boxes.
// Computed once per injectFindings call to avoid per-finding allocations.
type findingStyles struct {
	body    lipgloss.Style
	suggest lipgloss.Style
}

// renderInlineFinding renders a single finding as a styled box for injection
// into the diff. Returns the lines to insert (without trailing newline).
//
// The box is closed on the right (Box invariant); a 2-cell left indent
// matches the inline comment block style.
func renderInlineFinding(f state.ReviewFinding, width int, fs findingStyles) []string {
	borderColor := findingSeverityColor(f.Severity)
	titleSt := lipgloss.NewStyle().Foreground(borderColor).Bold(true)

	// Outer box width = viewport width minus the 2-cell left indent.
	boxW := max(width-2, 20)
	innerW := max(
		// rails(2) + Padding L+R (2)
		boxW-4, 10)

	// Title: "[severity/category] Title [CWE] verdict-marker"
	title := fmt.Sprintf("[%s/%s] %s", f.Severity, f.Category, f.Title)
	if f.CWE != "" {
		title += fmt.Sprintf(" [%s]", f.CWE)
	}
	if f.Revalidation != nil {
		switch f.Revalidation.Verdict {
		case "true-positive":
			title += " ✘TP"
		case "false-positive":
			title += " ~FP"
		case "fixed":
			title += " ✓Fixed"
		case "uncertain":
			title += " ??"
		}
	}

	var contentLines []string
	if f.Detail != "" {
		for _, w := range wrapText(f.Detail, innerW) {
			contentLines = append(contentLines, fs.body.Render(w))
		}
	}
	if f.Suggestion != "" {
		if len(contentLines) > 0 {
			contentLines = append(contentLines, "")
		}
		for _, w := range wrapText(f.Suggestion, innerW-2) {
			contentLines = append(contentLines, fs.suggest.Render("> "+w))
		}
	}

	box := Box{
		Width:       boxW,
		Title:       title,
		BorderColor: borderColor,
		TitleStyle:  &titleSt,
		Padding:     Padding{Left: 1, Right: 1},
	}
	rendered := box.Render(strings.Join(contentLines, "\n"))
	rawLines := strings.Split(rendered, "\n")
	out := make([]string, len(rawLines))
	for i, l := range rawLines {
		out[i] = "  " + l
	}
	return out
}

// ── Injection into diff ─────────────────────────────────────────────────

// injectFindings inserts styled finding boxes after their target lines in the diff.
// This mirrors the injectComments pattern exactly.
func (m *Model) injectFindings(styledDiff, filePath string) string {
	if !m.showInlineFindings || len(m.reviewFindings) == 0 {
		return styledDiff
	}

	// Build map of line -> findings for this file, sorted by severity (most severe first)
	findingsByLine := make(map[int][]state.ReviewFinding)
	for _, f := range m.reviewFindings {
		if f.File == filePath {
			findingsByLine[f.Line] = append(findingsByLine[f.Line], f)
		}
	}
	if len(findingsByLine) == 0 {
		return styledDiff
	}

	// Sort each line's findings by severity (most severe first)
	for line := range findingsByLine {
		sort.Slice(findingsByLine[line], func(i, j int) bool {
			return findingsByLine[line][i].SeverityRank() < findingsByLine[line][j].SeverityRank()
		})
	}

	// Use the diff pane's pre-computed width budget. inner == viewport
	// width by construction; using the budget keeps the source-of-truth
	// in one place (syncLayout) instead of here. Fall back to the
	// viewport directly when the budget isn't populated (tests).
	w := m.diffWidths.inner
	if w <= 0 {
		w = m.diffViewport.Width
	}
	if w < 40 {
		w = 80
	}

	fs := findingStyles{
		body:    styleTextPrimary,
		suggest: styleAccentGreen,
	}

	diffLines := strings.Split(styledDiff, "\n")
	result := make([]string, 0, len(diffLines)+len(findingsByLine)*6) // estimate ~6 lines per finding box

	// Handle file-level findings (line == 0) — show before the first hunk
	if fileFindings, ok := findingsByLine[0]; ok {
		for _, f := range fileFindings {
			result = append(result, renderInlineFinding(f, w, fs)...)
		}
	}

	for _, line := range diffLines {
		result = append(result, line)

		info := parseDiffLine(line)
		if info.line == 0 {
			continue
		}

		// Check right side (additions/context) first, then left side (deletions)
		if findings, ok := findingsByLine[info.rightLine]; ok && info.rightLine > 0 {
			for _, f := range findings {
				result = append(result, renderInlineFinding(f, w, fs)...)
			}
			// Remove so we don't double-inject (in case left and right are same line)
			delete(findingsByLine, info.rightLine)
		} else if findings, ok := findingsByLine[info.leftLine]; ok && info.leftLine > 0 {
			for _, f := range findings {
				result = append(result, renderInlineFinding(f, w, fs)...)
			}
			delete(findingsByLine, info.leftLine)
		}
	}

	return strings.Join(result, "\n")
}

// fileFindingsCount returns the number of findings for a specific file.
func (m *Model) fileFindingsCount(filePath string) int {
	count := 0
	for _, f := range m.reviewFindings {
		if f.File == filePath {
			count++
		}
	}
	return count
}

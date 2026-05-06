package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/state"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
func renderInlineFinding(f state.ReviewFinding, width int, fs findingStyles) []string {
	borderColor := findingSeverityColor(f.Severity)
	borderStyle := lipgloss.NewStyle().Foreground(borderColor)
	titleStyle := lipgloss.NewStyle().Foreground(borderColor).Bold(true)

	maxW := width - 8 // account for "  │  " prefix + margin
	if maxW < 20 {
		maxW = 20
	}

	var lines []string

	// Top border with severity/category tag and title
	header := fmt.Sprintf("[%s/%s] %s", f.Severity, f.Category, f.Title)
	if f.CWE != "" {
		header += fmt.Sprintf(" [%s]", f.CWE)
	}
	if f.Revalidation != nil {
		switch f.Revalidation.Verdict {
		case "true-positive":
			header += " \u2718TP"
		case "false-positive":
			header += " ~FP"
		case "fixed":
			header += " \u2713Fixed"
		case "uncertain":
			header += " ??"
		}
	}
	truncatedHeader := truncateToWidth(header, maxW-2)
	headerRendered := titleStyle.Render(truncatedHeader)

	// Fill remaining width with dashes: total visual width should not exceed 'width'.
	// Prefix "  ┌──── " = 8, then header, " ", then dashes.
	usedW := 8 + ansi.StringWidth(truncatedHeader) + 1
	topPad := width - usedW - 1 // -1 for safety margin
	if topPad < 1 {
		topPad = 1
	}
	topLine := borderStyle.Render("  ┌──── ") + headerRendered + " " + borderStyle.Render(strings.Repeat("─", topPad))
	lines = append(lines, topLine)

	// Body: detail text, word-wrapped
	if f.Detail != "" {
		for _, wrapped := range wrapText(f.Detail, maxW) {
			lines = append(lines, borderStyle.Render("  │  ")+fs.body.Render(wrapped))
		}
	}

	// Suggestion (if present), prefixed with ">"
	if f.Suggestion != "" {
		lines = append(lines, borderStyle.Render("  │"))
		for _, wrapped := range wrapText(f.Suggestion, maxW-2) {
			lines = append(lines, borderStyle.Render("  │  ")+fs.suggest.Render("> "+wrapped))
		}
	}

	// Bottom border: "  └" is 3 chars; fill to just under the pane width.
	bottomW := width - 4 // 3 (prefix) + bottomW dashes ≤ width - 1
	if bottomW < 6 {
		bottomW = 6
	}
	lines = append(lines, borderStyle.Render("  └"+strings.Repeat("─", bottomW)))

	return lines
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

	w := m.diffViewport.Width
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

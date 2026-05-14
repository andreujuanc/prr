package ui

import (
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/pipe"
	"github.com/andreujuanc/prr/internal/state"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ── Confirm action types ────────────────────────────────────────────────

// confirmAction identifies the type of action being confirmed.
type confirmAction int

const (
	confirmPublishComment     confirmAction = iota // single finding → line comment
	confirmBatchReview                             // all findings → GitHub review
	confirmPRComment                               // single finding → general PR comment
	confirmPipe                                    // pipe finding to external process
	confirmFixWithOpenCode                         // fix finding with OpenCode agent
	confirmDeleteCorruptState                      // state JSON unparseable → delete and start fresh
)

// confirmModal holds the state of a confirmation overlay.
type confirmModal struct {
	action   confirmAction
	finding  *state.ReviewFinding  // non-nil for single-finding actions
	findings []state.ReviewFinding // for batch review
	target   *pipe.Target          // for pipe actions
	// For confirmDeleteCorruptState: the absolute path of the
	// unparseable file and the error explaining why we couldn't load
	// it. Kept on the overlay so the modal can show both to the user
	// before they decide whether to delete.
	statePath string
	stateErr  error
}

// ── Action menu ─────────────────────────────────────────────────────────

// actionMenu holds the state of the "actions for finding" overlay.
type actionMenu struct {
	cursor  int
	finding state.ReviewFinding
	items   []actionMenuItem
}

type actionMenuItem struct {
	key   string // display key
	label string // description
}

// buildActionMenuItems returns the menu items for the current finding.
// Includes built-in actions + configured pipe targets.
func (m *Model) buildActionMenuItems() []actionMenuItem {
	items := []actionMenuItem{
		{key: "f", label: "Fix with OpenCode"},
		{key: "c", label: "Post as line comment on GitHub"},
		{key: "C", label: "Post ALL findings as GitHub Review"},
		{key: "g", label: "Post as PR comment"},
	}
	if len(m.pipeTargets) > 0 {
		for i, t := range m.pipeTargets {
			items = append(items, actionMenuItem{
				key:   fmt.Sprintf("%d", i+1),
				label: t.Name,
			})
		}
	}
	return items
}

// ── Rendering ───────────────────────────────────────────────────────────

// renderConfirmModal renders the confirmation modal content.
//
// Returns (content, ok). ok=false when the overlay state is missing or
// malformed (no action), so the View() dispatch can fall through.
func (m *Model) renderConfirmModal() (string, bool) {
	if m.confirmOverlay == nil {
		return "", false
	}

	var b strings.Builder
	modal := m.confirmOverlay

	switch modal.action {
	case confirmPublishComment:
		f := modal.finding
		b.WriteString(styleAccentBlueBold.Render("Post as line comment?") + "\n\n")
		b.WriteString(findingSummaryLine(f) + "\n")
		b.WriteString(styleTextMuted.Render(fmt.Sprintf("  %s:%d", f.File, f.Line)) + "\n\n")
		b.WriteString(styleTextMuted.Render("[Enter] Confirm   [Esc] Cancel"))

	case confirmBatchReview:
		findings := modal.findings
		counts := severityCounts(findings)
		files := uniqueFiles(findings)
		b.WriteString(styleAccentBlueBold.Render("Submit GitHub Review?") + "\n\n")
		b.WriteString(fmt.Sprintf("  %d inline comments: %s\n", len(findings), counts))
		b.WriteString(fmt.Sprintf("  %d files affected\n\n", len(files)))
		b.WriteString(styleTextMuted.Render("[Enter] Submit   [Esc] Cancel"))

	case confirmPRComment:
		f := modal.finding
		b.WriteString(styleAccentBlueBold.Render("Post as PR comment?") + "\n\n")
		b.WriteString(findingSummaryLine(f) + "\n")
		b.WriteString(styleTextMuted.Render(fmt.Sprintf("  %s:%d", f.File, f.Line)) + "\n\n")
		b.WriteString(styleTextMuted.Render("[Enter] Confirm   [Esc] Cancel"))

	case confirmPipe:
		f := modal.finding
		t := modal.target
		b.WriteString(styleAccentBlueBold.Render(fmt.Sprintf("Pipe to \"%s\"?", t.Name)) + "\n\n")
		// Show the full command so users can verify what will be executed
		cmdLine := t.Command
		if len(t.Args) > 0 {
			cmdLine += " " + strings.Join(t.Args, " ")
		}
		b.WriteString(styleTextMuted.Render(fmt.Sprintf("  Command: %s", cmdLine)) + "\n")
		// Warn if the command is an absolute path or looks unusual
		if strings.Contains(t.Command, "/") || strings.Contains(t.Command, "\\") {
			b.WriteString(styleAccentYellow.Render("  \u26a0 Command uses an absolute path — verify it is trusted") + "\n")
		}
		b.WriteString(findingSummaryLine(f) + "\n\n")
		b.WriteString(styleTextMuted.Render("[Enter] Run   [Esc] Cancel"))

	case confirmFixWithOpenCode:
		f := modal.finding
		b.WriteString(styleAccentBlueBold.Render("Fix with OpenCode?") + "\n\n")
		b.WriteString(findingSummaryLine(f) + "\n")
		b.WriteString(styleTextMuted.Render(fmt.Sprintf("  %s:%d", f.File, f.Line)) + "\n\n")
		b.WriteString(styleTextMuted.Render("[Enter] Run   [Esc] Cancel"))

	case confirmDeleteCorruptState:
		b.WriteString(styleAccentRed.Bold(true).Render("⚠ State file is corrupt") + "\n\n")
		b.WriteString(styleTextSecondary.Render("  "+modal.statePath) + "\n")
		if modal.stateErr != nil {
			b.WriteString(styleTextMuted.Render(fmt.Sprintf("  %v", modal.stateErr)) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(styleTextSecondary.Render("  Delete this file and start fresh?") + "\n")
		b.WriteString(styleTextMuted.Render("  Cached findings, briefs, and reviews for this PR will be lost.") + "\n\n")
		b.WriteString(styleTextMuted.Render("[Enter] Delete   [Esc] Quit"))
	}

	if b.Len() == 0 {
		return "", false
	}
	return b.String(), true
}

// renderActionMenu renders the action menu overlay content.
func (m *Model) renderActionMenu() (string, bool) {
	if m.actionMenuOverlay == nil || len(m.actionMenuOverlay.items) == 0 {
		return "", false
	}

	var b strings.Builder
	menu := m.actionMenuOverlay

	b.WriteString(styleAccentBlueBold.Render("Actions for finding") + "\n\n")

	const actionMenuW = 40
	for i, item := range menu.items {
		isSelected := i == menu.cursor
		keyStyle := styleAccentBlueBold
		labelStyle := styleTextSecondary
		if isSelected {
			labelStyle = styleTextPrimary
		}
		// Separator before pipe targets
		if i == 4 && len(menu.items) > 4 {
			b.WriteString(styleTextSubtle.Render("  ─────────────────────────────────") + "\n")
		}
		row := fmt.Sprintf("%s  %s",
			keyStyle.Render(item.key),
			labelStyle.Render(item.label),
		)
		b.WriteString(SelectableRow(row, actionMenuW, isSelected) + "\n")
	}

	b.WriteString("\n" + styleTextMuted.Render("  [Enter] Select   [Esc] Cancel"))

	return b.String(), true
}

// ── Bottom-anchored overlay compositor ──────────────────────────────────

// bottomOverlay composites a small bordered box at the bottom of the base view,
// without dimming or obscuring the content behind it. The modal floats over the
// last few lines of the active pane.
func bottomOverlay(base, content string, screenWidth, screenHeight int) string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderFocus).
		Padding(0, 1)

	box := boxStyle.Render(content)
	boxLines := strings.Split(box, "\n")

	// Measure box width from rendered output
	boxW := 0
	for _, l := range boxLines {
		if w := ansi.StringWidth(l); w > boxW {
			boxW = w
		}
	}

	baseLines := strings.Split(base, "\n")
	// Pad base to fill screen height if needed
	for len(baseLines) < screenHeight {
		baseLines = append(baseLines, "")
	}

	// Position: bottom of screen with a small margin, horizontally centered
	startRow := max(screenHeight-len(boxLines)-2, 0)
	startCol := max((screenWidth-boxW)/2, 0)

	// Composite box lines onto base
	for i, bline := range boxLines {
		row := startRow + i
		if row >= len(baseLines) {
			break
		}
		baseLine := baseLines[row]
		baseW := ansi.StringWidth(baseLine)

		var composed string
		if startCol <= baseW {
			composed = ansi.Truncate(baseLine, startCol, "") + bline
		} else {
			composed = baseLine +
				strings.Repeat(" ", startCol-baseW) + bline
		}
		baseLines[row] = composed
	}

	// Trim to screen height
	if len(baseLines) > screenHeight {
		baseLines = baseLines[:screenHeight]
	}

	return strings.Join(baseLines, "\n")
}

// ── Helpers ─────────────────────────────────────────────────────────────

// findingSummaryLine returns a one-line styled summary of a finding.
func findingSummaryLine(f *state.ReviewFinding) string {
	sevStyle := findingSeverityColor(f.Severity)
	tag := lipgloss.NewStyle().Foreground(sevStyle).Bold(true).
		Render(fmt.Sprintf("[%s/%s]", f.Severity, f.Category))
	return "  " + tag + " " + styleTextPrimary.Render(f.Title)
}

// severityCounts returns a string like "2 critical · 3 high · 1 med".
func severityCounts(findings []state.ReviewFinding) string {
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low", "nit"} {
		if n, ok := counts[sev]; ok {
			label := sev
			if sev == "medium" {
				label = "med"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	return strings.Join(parts, " · ")
}

// uniqueFiles returns deduplicated file paths from findings.
func uniqueFiles(findings []state.ReviewFinding) []string {
	seen := make(map[string]bool)
	var files []string
	for _, f := range findings {
		if f.File != "" && !seen[f.File] {
			seen[f.File] = true
			files = append(files, f.File)
		}
	}
	return files
}

// formatFindingAsMarkdown formats a finding for posting as a GitHub comment.
func formatFindingAsMarkdown(f state.ReviewFinding) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**[%s/%s] %s**\n\n", f.Severity, f.Category, f.Title))
	b.WriteString(f.Detail + "\n")
	if f.Suggestion != "" {
		b.WriteString(fmt.Sprintf("\n> **Suggestion:** %s\n", f.Suggestion))
	}
	b.WriteString("\n---\n_Posted by [prr](https://github.com/andreujuanc/prr) AI review_")
	return b.String()
}

// formatBatchReviewBody formats the summary body for a batch review submission.
func formatBatchReviewBody(findings []state.ReviewFinding) string {
	var b strings.Builder
	b.WriteString("## AI Review Summary\n\n")
	counts := severityCounts(findings)
	files := uniqueFiles(findings)
	b.WriteString(fmt.Sprintf("**%d findings** across %d files: %s\n\n", len(findings), len(files), counts))
	b.WriteString("---\n_Posted by [prr](https://github.com/andreujuanc/prr) AI review_")
	return b.String()
}

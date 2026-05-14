package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// SelectableRow renders a row in a selectable list with a consistent visual
// convention across the TUI (findings, file tree, theme/model pickers,
// action menu, etc.).
//
//   - selected   → left bar in accentBlue + accent-tinted full-row background
//     so the active row is unmissable even when content has no
//     glyph anchor.
//   - unselected → plain content with a 2-cell left margin matching the bar
//     width, so the columns line up between selected and
//     unselected rows.
//
// width is the *outer* row width (including the left bar). Content is
// truncated or padded to (width - 2) so the row is always exactly width
// cells wide.
func SelectableRow(content string, width int, selected bool) string {
	if width < 3 {
		return ansi.Truncate(content, width, "")
	}

	contentW := width - 2 // 1 for the bar, 1 for the trailing pad cell
	if contentW < 1 {
		contentW = 1
	}

	// Truncate / pad content to exact width.
	vis := ansi.StringWidth(content)
	body := content
	if vis > contentW {
		body = ansi.Truncate(body, contentW, "")
		vis = ansi.StringWidth(body)
	}
	if vis < contentW {
		body = body + strings.Repeat(" ", contentW-vis)
	}

	if selected {
		// Solid accent-blue bar + 1-space gap, then content + trailing
		// space rendered against a tinted background.
		bar := lipgloss.NewStyle().Foreground(accentBlue).Render("█")
		rowBg := lipgloss.NewStyle().Background(overlayBg)
		return bar + rowBg.Render(" "+body)
	}

	// Unselected: 2-space left margin so columns align with selected rows.
	return "  " + body
}

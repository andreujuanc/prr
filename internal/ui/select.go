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
//   - selected   → solid accent-blue left bar ("█") + 1-cell gap, then
//     content padded to fill the remaining width.
//   - unselected → 2-cell blank left margin matching the bar width, then
//     content padded to fill the remaining width, so columns
//     line up between selected and unselected rows.
//
// width is the *outer* row width (including the left bar). Content is
// truncated or padded to (width - 2) so the row is always exactly width
// cells wide.
//
// A full-row background fill is intentionally NOT applied: the content
// strings passed in by call sites already contain embedded ANSI resets
// (\x1b[0m) from their own styled segments, and those resets cancel any
// outer Background(...) lipgloss has emitted. The bar alone is enough
// of a selection affordance, and avoids the bg-bleed regression in
// content-rich rows like the file tree.
func SelectableRow(content string, width int, selected bool) string {
	if width < 3 {
		return ansi.Truncate(content, width, "")
	}

	contentW := width - 2 // 1 for the bar, 1 for the gap after it
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
		bar := lipgloss.NewStyle().Foreground(accentBlue).Render("█")
		return bar + " " + body
	}
	return "  " + body
}

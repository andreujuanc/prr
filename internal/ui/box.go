package ui

import (
	"image/color"

	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Padding controls the inner spacing of a Box.
type Padding struct {
	Top, Right, Bottom, Left int
}

// widthBudget is the shared, pre-computed set of widths for a pane.
// It is populated once per layout pass (syncLayout) and read everywhere
// else, replacing the scattered "viewport.Width - N" expressions that
// previously had to be kept in sync at every render site.
//
//	pane     — outer pane width (passed to Box.Width for the pane border)
//	inner    — content area inside the pane borders == pane - 2
//	boxOuter — width of inline boxes drawn inside the pane (== inner)
//	boxInner — content area inside an inline box's own borders, after
//	           padding == boxOuter - 2 - paddingL - paddingR
//	bodyWrap — recommended wrap width for prose inside an inline box;
//	           tracks boxInner today, kept as a separate name so future
//	           tweaks (e.g. for suggestion-marker reservation) can adjust
//	           it without touching every call site.
type widthBudget struct {
	pane     int
	inner    int
	boxOuter int
	boxInner int
	bodyWrap int
}

// budgetFromPane derives a widthBudget from an outer pane width using
// the inline-box convention (1-cell padding on each side of the inline
// box). Returns a zero budget if pane is too small to render anything
// useful.
func budgetFromPane(pane int) widthBudget {
	if pane < 4 {
		return widthBudget{pane: pane}
	}
	inner := pane - 2
	// Inline boxes get a 2-cell left indent inside the pane, so their
	// outer width is inner - 2. The Box itself eats 2 cells for rails
	// and a further 2 for Padding{L:1, R:1}.
	boxOuter := max(inner-2, 4)
	boxInner := max(boxOuter-4, 1)
	return widthBudget{
		pane:     pane,
		inner:    inner,
		boxOuter: boxOuter,
		boxInner: boxInner,
		bodyWrap: boxInner,
	}
}

// Box is a single source of truth for bordered rectangles in the TUI.
// It replaces ad-hoc border-drawing logic across renderPane, inline finding
// boxes, comment boxes, etc., so that width math and right-rail invariants
// live in exactly one place.
//
// Width is the outer width (including both side rails).
// Height is the outer height; when Height == 0, the box is sized to fit the
// content (top + content lines + bottom).
//
// Inner content width is Width - 2 (left/right rails) - Padding.Left -
// Padding.Right. Content lines longer than the inner width are truncated
// ansi-aware; shorter lines are right-padded with spaces so every output
// row is exactly Width cells wide.
type Box struct {
	Width, Height int
	Title         string
	Border        lipgloss.Border
	BorderColor   color.Color
	// TitleStyle overrides the default focused/unfocused title style.
	// Nil means use the theme default.
	TitleStyle *lipgloss.Style
	Focused    bool
	Padding    Padding
}

// Render returns the box as a single string with embedded newlines.
//
// Invariants:
//   - Every output line is exactly Width cells wide (ANSI-aware).
//   - Body rows always have both a left and a right rail.
//   - Inset title is truncated with "…" if it does not fit.
//   - If Width < 4 the box is unrenderable; returns "".
//   - If Height > 0, output is always exactly Height lines.
func (b Box) Render(content string) string {
	if b.Width < 4 {
		return ""
	}

	bdr := b.Border
	if bdr == (lipgloss.Border{}) {
		bdr = lipgloss.RoundedBorder()
	}

	// Resolve border + title styles. When Focused is true and no explicit
	// BorderColor was set we fall back to the focused theme color.
	var borderSt lipgloss.Style
	if b.BorderColor != nil {
		borderSt = lipgloss.NewStyle().Foreground(b.BorderColor)
	} else if b.Focused {
		borderSt = borderStyleFocused
	} else {
		borderSt = borderStyleUnfocused
	}

	var titleSt lipgloss.Style
	if b.TitleStyle != nil {
		titleSt = *b.TitleStyle
	} else if b.Focused {
		titleSt = titleFocusedStyle
	} else {
		titleSt = titleStyle
	}

	// Inner content width (between rails, inside padding).
	innerW := max(b.Width-2-b.Padding.Left-b.Padding.Right, 0)

	// ── Top border ─────────────────────────────────────────────────
	topLeft := borderSt.Render(bdr.TopLeft)
	topRight := borderSt.Render(bdr.TopRight)
	topBarWidth := b.Width - 2 // chars between the two corners

	var topLine string
	if b.Title != "" && topBarWidth >= 4 {
		// Inset title rendered as " <title> " with a 2-char bar before it.
		// Some title styles add padding/margin, so always measure the
		// rendered label and re-truncate if it still overflows.
		labelText := " " + b.Title + " "
		labelRendered := titleSt.Render(labelText)
		labelW := ansi.StringWidth(labelRendered)
		maxLabelW := topBarWidth - 2 // reserve 2 bar cells before the title

		if labelW > maxLabelW {
			overhead := labelW - ansi.StringWidth(labelText) // style padding contribution
			maxTitleW := max(
				// -2 for surrounding spaces
				maxLabelW-2-overhead, 1)
			truncated := ansi.Truncate(b.Title, maxTitleW, "…")
			labelText = " " + truncated + " "
			labelRendered = titleSt.Render(labelText)
			labelW = ansi.StringWidth(labelRendered)
			// Safety net for unexpected style overhead.
			if labelW > maxLabelW {
				labelRendered = ansi.Truncate(labelRendered, maxLabelW, "")
				labelW = ansi.StringWidth(labelRendered)
			}
		}

		barBefore := borderSt.Render(strings.Repeat(bdr.Top, 2))
		remaining := max(topBarWidth-2-labelW, 0)
		barAfter := borderSt.Render(strings.Repeat(bdr.Top, remaining))
		topLine = topLeft + barBefore + labelRendered + barAfter + topRight
	} else {
		bar := borderSt.Render(strings.Repeat(bdr.Top, topBarWidth))
		topLine = topLeft + bar + topRight
	}

	// ── Body lines ─────────────────────────────────────────────────
	left := borderSt.Render(bdr.Left)
	right := borderSt.Render(bdr.Right)
	leftPad := strings.Repeat(" ", b.Padding.Left)
	rightPad := strings.Repeat(" ", b.Padding.Right)

	rawLines := strings.Split(content, "\n")
	if content == "" {
		rawLines = nil
	}

	// Determine how many body rows to emit.
	bodyCount := len(rawLines) + b.Padding.Top + b.Padding.Bottom
	if b.Height > 0 {
		bodyCount = max(
			// subtract top + bottom border rows
			b.Height-2, 0)
	}

	var body strings.Builder
	body.Grow(bodyCount * (b.Width + 8))

	renderRow := func(s string) {
		vis := ansi.StringWidth(s)
		if vis > innerW {
			s = ansi.Truncate(s, innerW, "")
			vis = ansi.StringWidth(s)
		}
		if vis < innerW {
			s = s + strings.Repeat(" ", innerW-vis)
		}
		body.WriteString(left + leftPad + s + rightPad + right)
	}

	for i := 0; i < bodyCount; i++ {
		// Top padding rows, then content rows, then bottom padding rows.
		var line string
		switch {
		case i < b.Padding.Top:
			line = ""
		case i < b.Padding.Top+len(rawLines):
			line = rawLines[i-b.Padding.Top]
		default:
			line = ""
		}
		renderRow(line)
		body.WriteByte('\n')
	}

	// ── Bottom border ──────────────────────────────────────────────
	bottomLeft := borderSt.Render(bdr.BottomLeft)
	bottomRight := borderSt.Render(bdr.BottomRight)
	bottomBar := borderSt.Render(strings.Repeat(bdr.Bottom, b.Width-2))
	bottomLine := bottomLeft + bottomBar + bottomRight

	return topLine + "\n" + body.String() + bottomLine
}

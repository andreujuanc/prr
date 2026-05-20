package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// ── Color Palette ──────────────────────────────────────────────────────
// Colors are populated from the active theme via rebuildStyles().

var (
	// Base backgrounds
	baseBg    lipgloss.Color
	surfaceBg lipgloss.Color
	overlayBg lipgloss.Color

	// Text hierarchy
	textPrimary   lipgloss.Color
	textSecondary lipgloss.Color
	textMuted     lipgloss.Color
	textSubtle    lipgloss.Color

	// Accent colors
	accentBlue   lipgloss.Color
	accentMauve  lipgloss.Color
	accentGreen  lipgloss.Color
	accentRed    lipgloss.Color
	accentYellow lipgloss.Color
	accentPeach  lipgloss.Color

	// Semantic
	headerBg    lipgloss.Color
	borderClr   lipgloss.Color
	borderFocus lipgloss.Color
)

// ── Reusable Styles ────────────────────────────────────────────────────

var (
	// Pane border styles
	paneStyle        lipgloss.Style
	paneFocusedStyle lipgloss.Style

	// Pane title styles
	titleStyle        lipgloss.Style
	titleFocusedStyle lipgloss.Style

	// Header
	headerStyle lipgloss.Style

	// Footer key styles
	footerKeyStyle  lipgloss.Style
	footerDescStyle lipgloss.Style
	footerSepStyle  lipgloss.Style

	// Pre-computed styles for hot render paths (avoid allocations in View)
	styleTextMuted      lipgloss.Style
	styleTextSubtle     lipgloss.Style
	styleTextPrimary    lipgloss.Style
	styleTextSecondary  lipgloss.Style
	styleAccentBlue     lipgloss.Style
	styleAccentBlueBold lipgloss.Style
	styleAccentGreen    lipgloss.Style

	// AI thought text — dim italic, ~65% opacity feel
	styleThought          lipgloss.Style
	styleToolCall         lipgloss.Style
	styleBatchOutput      lipgloss.Style
	styleProgressBar      lipgloss.Style
	styleProgressBg       lipgloss.Style
	styleAccentMauveBold  lipgloss.Style
	styleAccentRed        lipgloss.Style
	styleAccentYellow     lipgloss.Style
	styleAccentYellowBold lipgloss.Style

	// Diff cursor highlight styles
	styleHighlightCommentable lipgloss.Style
	styleHighlightNormal      lipgloss.Style

	// Structured review styles
	styleSeverityCritical lipgloss.Style
	styleSeverityHigh     lipgloss.Style
	styleSeverityMedium   lipgloss.Style
	styleSeverityLow      lipgloss.Style
	styleSeverityNit      lipgloss.Style
	styleVerdictApprove   lipgloss.Style
	styleVerdictChanges   lipgloss.Style
	styleVerdictComment   lipgloss.Style
	styleAccentPeach      lipgloss.Style
	styleFileLine         lipgloss.Style
	styleStaleReview      lipgloss.Style

	// Border styles for pane rendering (focused/unfocused)
	borderStyleFocused   lipgloss.Style
	borderStyleUnfocused lipgloss.Style

	// File tree pre-computed styles
	ftDirNameStyle     lipgloss.Style
	ftDimDirName       lipgloss.Style
	ftAddClr           lipgloss.Style
	ftDelClr           lipgloss.Style
	ftIconReviewedSt   lipgloss.Style
	ftIconModifiedSt   lipgloss.Style
	ftIconUnreviewSt   lipgloss.Style
	ftSelectedMarkerSt lipgloss.Style
)

func init() {
	rebuildStyles()
}

// rebuildStyles recomputes all lipgloss styles from the active theme.
// Called once at init and again whenever the theme is changed.
func rebuildStyles() {
	t := currentTheme

	// Copy palette from theme
	baseBg = t.BaseBg
	surfaceBg = t.SurfaceBg
	overlayBg = t.OverlayBg
	textPrimary = t.TextPrimary
	textSecondary = t.TextSecondary
	textMuted = t.TextMuted
	textSubtle = t.TextSubtle
	accentBlue = t.AccentBlue
	accentMauve = t.AccentMauve
	accentGreen = t.AccentGreen
	accentRed = t.AccentRed
	accentYellow = t.AccentYellow
	accentPeach = t.AccentPeach
	headerBg = t.HeaderBg
	borderClr = t.BorderClr
	borderFocus = t.BorderFocus

	// Pane border styles
	paneStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderClr)

	paneFocusedStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderFocus)

	// Pane title styles
	titleStyle = lipgloss.NewStyle().
		Foreground(textMuted).
		Bold(true).
		PaddingLeft(1)

	titleFocusedStyle = lipgloss.NewStyle().
		Foreground(accentBlue).
		Bold(true).
		PaddingLeft(1)

	// Header
	headerStyle = lipgloss.NewStyle().
		Foreground(textPrimary).
		Bold(true)

	// Footer key styles
	footerKeyStyle = lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
	footerDescStyle = lipgloss.NewStyle().Foreground(textMuted)
	footerSepStyle = lipgloss.NewStyle().Foreground(textSubtle)

	// Pre-computed text styles
	styleTextMuted = lipgloss.NewStyle().Foreground(textMuted)
	styleTextSubtle = lipgloss.NewStyle().Foreground(textSubtle)
	styleTextPrimary = lipgloss.NewStyle().Foreground(textPrimary)
	styleTextSecondary = lipgloss.NewStyle().Foreground(textSecondary)
	styleAccentBlue = lipgloss.NewStyle().Foreground(accentBlue)
	styleAccentBlueBold = lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
	styleAccentGreen = lipgloss.NewStyle().Foreground(accentGreen)

	// AI text styles
	styleThought = lipgloss.NewStyle().Foreground(textSubtle).Italic(true)
	styleToolCall = lipgloss.NewStyle().Foreground(overlayBg)
	styleBatchOutput = lipgloss.NewStyle().Foreground(textMuted)
	styleProgressBar = lipgloss.NewStyle().Foreground(accentBlue)
	styleProgressBg = lipgloss.NewStyle().Foreground(surfaceBg)
	styleAccentMauveBold = lipgloss.NewStyle().Foreground(accentMauve).Bold(true)
	styleAccentRed = lipgloss.NewStyle().Foreground(accentRed)
	styleAccentYellow = lipgloss.NewStyle().Foreground(accentYellow)
	styleAccentYellowBold = lipgloss.NewStyle().Foreground(accentYellow).Bold(true)

	// Diff cursor highlight styles
	styleHighlightCommentable = lipgloss.NewStyle().Background(overlayBg)
	styleHighlightNormal = lipgloss.NewStyle().Background(surfaceBg)

	// Structured review styles
	styleSeverityCritical = lipgloss.NewStyle().Foreground(accentRed).Bold(true)
	styleSeverityHigh = lipgloss.NewStyle().Foreground(accentPeach).Bold(true)
	styleSeverityMedium = lipgloss.NewStyle().Foreground(accentYellow)
	styleSeverityLow = lipgloss.NewStyle().Foreground(textSecondary)
	styleSeverityNit = lipgloss.NewStyle().Foreground(textMuted).Italic(true)
	// Verdict pills — high-contrast badges so the headline review
	// outcome (APPROVED / CHANGES REQUESTED / COMMENT) reads at a
	// glance. Background fill + dark foreground + padding gives the
	// pill shape; the original text-only colours blended too far into
	// the surrounding prose for a "this is the verdict" feel.
	styleVerdictApprove = lipgloss.NewStyle().Foreground(baseBg).Background(accentGreen).Bold(true).Padding(0, 1)
	styleVerdictChanges = lipgloss.NewStyle().Foreground(baseBg).Background(accentRed).Bold(true).Padding(0, 1)
	styleVerdictComment = lipgloss.NewStyle().Foreground(baseBg).Background(accentYellow).Bold(true).Padding(0, 1)
	styleAccentPeach = lipgloss.NewStyle().Foreground(accentPeach)
	styleFileLine = lipgloss.NewStyle().Foreground(accentBlue).Underline(true)
	styleStaleReview = lipgloss.NewStyle().Foreground(accentYellow).Bold(true)

	// Border styles for pane rendering (focused/unfocused)
	borderStyleFocused = lipgloss.NewStyle().Foreground(borderFocus)
	borderStyleUnfocused = lipgloss.NewStyle().Foreground(borderClr)

	// File tree pre-computed styles
	ftDirNameStyle = lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
	ftDimDirName = lipgloss.NewStyle().Foreground(textMuted).Bold(true)
	ftAddClr = lipgloss.NewStyle().Foreground(accentGreen)
	ftDelClr = lipgloss.NewStyle().Foreground(accentRed)
	ftIconReviewedSt = lipgloss.NewStyle().Foreground(accentGreen)
	ftIconModifiedSt = lipgloss.NewStyle().Foreground(accentYellow)
	ftIconUnreviewSt = lipgloss.NewStyle().Foreground(accentYellow)
	ftSelectedMarkerSt = lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
}

// renderMarkdown renders a markdown string for display in the TUI.
// Falls back to plain text if rendering fails.
// Thread-safe: uses an LRU cache so it can be called from
// both the main goroutine and async tea.Cmd goroutines.
var mdCache = newLRUCache(128)

// wrapStyled applies a lipgloss style to text while handling word wrapping.
// This ensures style (color, bold, etc.) is preserved across wrapped lines.
func wrapStyled(s lipgloss.Style, text string, width int) string {
	if width < 10 {
		width = 10
	}
	return s.Width(width).Render(text)
}

func renderMarkdown(content string, width int) string {
	if width < 20 {
		width = 20
	}

	// Check cache first
	cacheKey := fmt.Sprintf("%d:%s", width, content)
	if cached, ok := mdCache.get(cacheKey); ok {
		return cached
	}

	// Create a renderer
	r, err := glamour.NewTermRenderer(
		glamour.WithStylePath("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return content
	}
	out, err := r.Render(content)
	if err != nil {
		return content
	}
	result := strings.TrimRight(out, "\n")
	mdCache.set(cacheKey, result)
	return result
}

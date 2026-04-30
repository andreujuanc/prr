package ui

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// ── Color Palette ──────────────────────────────────────────────────────
// A refined dark theme inspired by modern editors (Catppuccin/Mocha-ish).

var (
	// Base backgrounds
	baseBg     = lipgloss.Color("#1E1E2E") // Main background
	surfaceBg  = lipgloss.Color("#313244") // Elevated surfaces (pane bg)
	overlayBg  = lipgloss.Color("#45475A") // Overlay / selected row bg

	// Text hierarchy
	textPrimary   = lipgloss.Color("#CDD6F4") // Primary text
	textSecondary = lipgloss.Color("#A6ADC8") // Secondary text
	textMuted     = lipgloss.Color("#6C7086") // Muted / placeholder text
	textSubtle    = lipgloss.Color("#585B70") // Subtle separators

	// Accent colors
	accentBlue   = lipgloss.Color("#89B4FA") // Primary accent / focus
	accentMauve  = lipgloss.Color("#CBA6F7") // Secondary accent
	accentGreen  = lipgloss.Color("#A6E3A1") // Success / additions
	accentRed    = lipgloss.Color("#F38BA8") // Error / deletions
	accentYellow = lipgloss.Color("#F9E2AF") // Warnings / in-progress
	accentPeach  = lipgloss.Color("#FAB387") // Highlights

	// Semantic
	headerBg   = lipgloss.Color("#1E1E2E")
	borderClr  = lipgloss.Color("#313244") // Borders (darker)
	borderFocus = lipgloss.Color("#585B70") // Focused border (subtle)
)

// ── Reusable Styles ────────────────────────────────────────────────────

var (
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
	footerKeyStyle  = lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
	footerDescStyle = lipgloss.NewStyle().Foreground(textMuted)
	footerSepStyle  = lipgloss.NewStyle().Foreground(textSubtle)

	// Pre-computed styles for hot render paths (avoid allocations in View)
	styleTextMuted       = lipgloss.NewStyle().Foreground(textMuted)
	styleTextSubtle      = lipgloss.NewStyle().Foreground(textSubtle)
	styleTextPrimary     = lipgloss.NewStyle().Foreground(textPrimary)
	styleTextSecondary   = lipgloss.NewStyle().Foreground(textSecondary)
	styleAccentBlue      = lipgloss.NewStyle().Foreground(accentBlue)
	styleAccentBlueBold  = lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
	styleAccentGreen     = lipgloss.NewStyle().Foreground(accentGreen)

	// AI thought text — dim italic, ~65% opacity feel
	styleThought     = lipgloss.NewStyle().Foreground(lipgloss.Color("#585B70")).Italic(true)
	styleToolCall    = lipgloss.NewStyle().Foreground(lipgloss.Color("#45475A"))                  // very subtle tool call indicators
	styleBatchOutput = lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))                  // muted batch review output (intermediate)
	styleProgressBar = lipgloss.NewStyle().Foreground(accentBlue)
	styleProgressBg  = lipgloss.NewStyle().Foreground(lipgloss.Color("#313244"))
	styleAccentMauveBold = lipgloss.NewStyle().Foreground(accentMauve).Bold(true)
	styleAccentRed       = lipgloss.NewStyle().Foreground(accentRed)
	styleAccentYellow    = lipgloss.NewStyle().Foreground(accentYellow)
	styleAccentYellowBold = lipgloss.NewStyle().Foreground(accentYellow).Bold(true)

	// Diff cursor highlight styles
	styleHighlightCommentable = lipgloss.NewStyle().Background(overlayBg)
	styleHighlightNormal      = lipgloss.NewStyle().Background(surfaceBg)

	// Border styles for pane rendering (focused/unfocused)
	borderStyleFocused   = lipgloss.NewStyle().Foreground(borderFocus)
	borderStyleUnfocused = lipgloss.NewStyle().Foreground(borderClr)

	// File tree pre-computed styles
	ftDirNameStyle     = lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
	ftDimDirName       = lipgloss.NewStyle().Foreground(textMuted).Bold(true)
	ftAddClr           = lipgloss.NewStyle().Foreground(accentGreen)
	ftDelClr           = lipgloss.NewStyle().Foreground(accentRed)
	ftIconReviewedSt   = lipgloss.NewStyle().Foreground(accentGreen)
	ftIconModifiedSt   = lipgloss.NewStyle().Foreground(accentYellow)
	ftIconUnreviewSt   = lipgloss.NewStyle().Foreground(accentYellow)
	ftSelectedMarkerSt = lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
)

// renderMarkdown renders a markdown string for display in the TUI.
// Falls back to plain text if rendering fails.
// Thread-safe: uses sync.Map for caching so it can be called from
// both the main goroutine and async tea.Cmd goroutines.
// Cache is evicted when it exceeds maxMDCacheEntries to bound memory.
var mdCache sync.Map // key: "width:content" -> value: rendered string
var mdCacheCount int32

const maxMDCacheEntries = 128

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

	// Check output cache first
	cacheKey := fmt.Sprintf("%d:%s", width, content)
	if cached, ok := mdCache.Load(cacheKey); ok {
		return cached.(string)
	}

	// Evict entire cache if it's grown too large
	if atomic.LoadInt32(&mdCacheCount) >= maxMDCacheEntries {
		mdCache.Range(func(key, _ any) bool {
			mdCache.Delete(key)
			return true
		})
		atomic.StoreInt32(&mdCacheCount, 0)
	}

	// Create a renderer (cheap compared to rendering itself)
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
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
	mdCache.Store(cacheKey, result)
	atomic.AddInt32(&mdCacheCount, 1)
	return result
}

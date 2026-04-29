package ui

import "github.com/charmbracelet/lipgloss"

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

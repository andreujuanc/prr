package ui

import (
	"github.com/andreujuanc/prr/internal/git"
	"github.com/charmbracelet/lipgloss"
)

// Theme defines the complete color palette for the TUI.
// Each theme provides colors for backgrounds, text, accents, diff highlighting,
// and the delta syntax theme name (used when delta is the diff renderer).
type Theme struct {
	Name string // human-readable display name
	ID   string // stable identifier for config persistence

	// Base backgrounds
	BaseBg    lipgloss.Color
	SurfaceBg lipgloss.Color
	OverlayBg lipgloss.Color

	// Text hierarchy
	TextPrimary   lipgloss.Color
	TextSecondary lipgloss.Color
	TextMuted     lipgloss.Color
	TextSubtle    lipgloss.Color

	// Accent colors
	AccentBlue   lipgloss.Color
	AccentMauve  lipgloss.Color
	AccentGreen  lipgloss.Color
	AccentRed    lipgloss.Color
	AccentYellow lipgloss.Color
	AccentPeach  lipgloss.Color

	// Semantic
	HeaderBg    lipgloss.Color
	BorderClr   lipgloss.Color
	BorderFocus lipgloss.Color

	// Diff backgrounds (for delta and chroma renderers)
	DiffAddedBg       string // e.g. "#122f1c"
	DiffAddedEmphBg   string
	DiffRemovedBg     string
	DiffRemovedEmphBg string

	// Delta syntax theme name (e.g. "Nord", "Dracula", "gruvbox-dark")
	DeltaSyntaxTheme string

	// Chroma syntax style name (e.g. "monokai", "dracula", "gruvbox")
	ChromaSyntaxStyle string
}

// ── Built-in Themes ────────────────────────────────────────────────────

var builtinThemes = []Theme{
	themeCatppuccinMocha(),
	themeDracula(),
	themeGruvboxDark(),
	themeTokyoNight(),
	themeNord(),
	themeSolarizedDark(),
	themeOneDark(),
	themeRosePine(),
}

// currentTheme holds the active theme. Defaults to Catppuccin Mocha.
var currentTheme = themeCatppuccinMocha()

// CurrentTheme returns the active theme.
func CurrentTheme() Theme { return currentTheme }

// SetTheme switches the active theme and rebuilds all lipgloss styles.
func SetTheme(t Theme) {
	currentTheme = t
	rebuildStyles()
}

// DiffThemeFromCurrent returns a DiffTheme snapshot from the active theme.
// This is captured at call time and passed to diff rendering goroutines,
// avoiding data races on mutable global state.
func DiffThemeFromCurrent() git.DiffTheme {
	t := currentTheme
	return git.DiffTheme{
		SyntaxTheme:       t.DeltaSyntaxTheme,
		ChromaSyntaxStyle: t.ChromaSyntaxStyle,
		AddedBg:           t.DiffAddedBg,
		AddedEmphBg:       t.DiffAddedEmphBg,
		RemovedBg:         t.DiffRemovedBg,
		RemovedEmphBg:     t.DiffRemovedEmphBg,
		AccentMauve:       string(t.AccentMauve),
		AccentBlue:        string(t.AccentBlue),
		AccentGreen:       string(t.AccentGreen),
		AccentRed:         string(t.AccentRed),
		AccentPeach:       string(t.AccentPeach),
		SurfaceColor:      string(t.OverlayBg),
		SubtleColor:       string(t.TextSubtle),
	}
}

// ThemeByID looks up a built-in theme by its ID.
// Returns the default theme if not found.
func ThemeByID(id string) Theme {
	for _, t := range builtinThemes {
		if t.ID == id {
			return t
		}
	}
	return builtinThemes[0]
}

// BuiltinThemes returns a copy of all available themes.
func BuiltinThemes() []Theme {
	out := make([]Theme, len(builtinThemes))
	copy(out, builtinThemes)
	return out
}

// ── Theme Definitions ──────────────────────────────────────────────────

func themeCatppuccinMocha() Theme {
	return Theme{
		Name:              "Catppuccin Mocha",
		ID:                "catppuccin-mocha",
		BaseBg:            "#1E1E2E",
		SurfaceBg:         "#313244",
		OverlayBg:         "#45475A",
		TextPrimary:       "#CDD6F4",
		TextSecondary:     "#A6ADC8",
		TextMuted:         "#6C7086",
		TextSubtle:        "#585B70",
		AccentBlue:        "#89B4FA",
		AccentMauve:       "#CBA6F7",
		AccentGreen:       "#A6E3A1",
		AccentRed:         "#F38BA8",
		AccentYellow:      "#F9E2AF",
		AccentPeach:       "#FAB387",
		HeaderBg:          "#1E1E2E",
		BorderClr:         "#313244",
		BorderFocus:       "#585B70",
		DiffAddedBg:       "#122f1c",
		DiffAddedEmphBg:   "#1a4028",
		DiffRemovedBg:     "#361420",
		DiffRemovedEmphBg: "#4d1a2a",
		DeltaSyntaxTheme:  "Nord",
		ChromaSyntaxStyle: "catppuccin-mocha",
	}
}

func themeDracula() Theme {
	return Theme{
		Name:              "Dracula",
		ID:                "dracula",
		BaseBg:            "#282A36",
		SurfaceBg:         "#44475A",
		OverlayBg:         "#6272A4",
		TextPrimary:       "#F8F8F2",
		TextSecondary:     "#BFBFBF",
		TextMuted:         "#6272A4",
		TextSubtle:        "#44475A",
		AccentBlue:        "#8BE9FD",
		AccentMauve:       "#BD93F9",
		AccentGreen:       "#50FA7B",
		AccentRed:         "#FF5555",
		AccentYellow:      "#F1FA8C",
		AccentPeach:       "#FFB86C",
		HeaderBg:          "#282A36",
		BorderClr:         "#44475A",
		BorderFocus:       "#6272A4",
		DiffAddedBg:       "#1a3a2a",
		DiffAddedEmphBg:   "#224d34",
		DiffRemovedBg:     "#3a1a1a",
		DiffRemovedEmphBg: "#4d2222",
		DeltaSyntaxTheme:  "Dracula",
		ChromaSyntaxStyle: "dracula",
	}
}

func themeGruvboxDark() Theme {
	return Theme{
		Name:              "Gruvbox Dark",
		ID:                "gruvbox-dark",
		BaseBg:            "#282828",
		SurfaceBg:         "#3C3836",
		OverlayBg:         "#504945",
		TextPrimary:       "#EBDBB2",
		TextSecondary:     "#D5C4A1",
		TextMuted:         "#928374",
		TextSubtle:        "#665C54",
		AccentBlue:        "#83A598",
		AccentMauve:       "#D3869B",
		AccentGreen:       "#B8BB26",
		AccentRed:         "#FB4934",
		AccentYellow:      "#FABD2F",
		AccentPeach:       "#FE8019",
		HeaderBg:          "#282828",
		BorderClr:         "#3C3836",
		BorderFocus:       "#665C54",
		DiffAddedBg:       "#1d2a1d",
		DiffAddedEmphBg:   "#2a3d2a",
		DiffRemovedBg:     "#2a1d1d",
		DiffRemovedEmphBg: "#3d2a2a",
		DeltaSyntaxTheme:  "gruvbox-dark",
		ChromaSyntaxStyle: "gruvbox",
	}
}

func themeTokyoNight() Theme {
	return Theme{
		Name:              "Tokyo Night",
		ID:                "tokyo-night",
		BaseBg:            "#1A1B26",
		SurfaceBg:         "#24283B",
		OverlayBg:         "#414868",
		TextPrimary:       "#C0CAF5",
		TextSecondary:     "#A9B1D6",
		TextMuted:         "#565F89",
		TextSubtle:        "#3B4261",
		AccentBlue:        "#7AA2F7",
		AccentMauve:       "#BB9AF7",
		AccentGreen:       "#9ECE6A",
		AccentRed:         "#F7768E",
		AccentYellow:      "#E0AF68",
		AccentPeach:       "#FF9E64",
		HeaderBg:          "#1A1B26",
		BorderClr:         "#24283B",
		BorderFocus:       "#3B4261",
		DiffAddedBg:       "#132a1f",
		DiffAddedEmphBg:   "#1a3d28",
		DiffRemovedBg:     "#31142a",
		DiffRemovedEmphBg: "#451a3a",
		DeltaSyntaxTheme:  "Nord",
		ChromaSyntaxStyle: "monokai",
	}
}

func themeNord() Theme {
	return Theme{
		Name:              "Nord",
		ID:                "nord",
		BaseBg:            "#2E3440",
		SurfaceBg:         "#3B4252",
		OverlayBg:         "#434C5E",
		TextPrimary:       "#ECEFF4",
		TextSecondary:     "#D8DEE9",
		TextMuted:         "#4C566A",
		TextSubtle:        "#434C5E",
		AccentBlue:        "#88C0D0",
		AccentMauve:       "#B48EAD",
		AccentGreen:       "#A3BE8C",
		AccentRed:         "#BF616A",
		AccentYellow:      "#EBCB8B",
		AccentPeach:       "#D08770",
		HeaderBg:          "#2E3440",
		BorderClr:         "#3B4252",
		BorderFocus:       "#4C566A",
		DiffAddedBg:       "#1f2d24",
		DiffAddedEmphBg:   "#2a3d2e",
		DiffRemovedBg:     "#2d1f21",
		DiffRemovedEmphBg: "#3d2a2c",
		DeltaSyntaxTheme:  "Nord",
		ChromaSyntaxStyle: "nord",
	}
}

func themeSolarizedDark() Theme {
	return Theme{
		Name:              "Solarized Dark",
		ID:                "solarized-dark",
		BaseBg:            "#002B36",
		SurfaceBg:         "#073642",
		OverlayBg:         "#586E75",
		TextPrimary:       "#FDF6E3",
		TextSecondary:     "#EEE8D5",
		TextMuted:         "#657B83",
		TextSubtle:        "#586E75",
		AccentBlue:        "#268BD2",
		AccentMauve:       "#6C71C4",
		AccentGreen:       "#859900",
		AccentRed:         "#DC322F",
		AccentYellow:      "#B58900",
		AccentPeach:       "#CB4B16",
		HeaderBg:          "#002B36",
		BorderClr:         "#073642",
		BorderFocus:       "#586E75",
		DiffAddedBg:       "#003a1a",
		DiffAddedEmphBg:   "#004d24",
		DiffRemovedBg:     "#3a0a0a",
		DiffRemovedEmphBg: "#4d1414",
		DeltaSyntaxTheme:  "Solarized (dark)",
		ChromaSyntaxStyle: "solarized-dark256",
	}
}

func themeOneDark() Theme {
	return Theme{
		Name:              "One Dark",
		ID:                "one-dark",
		BaseBg:            "#282C34",
		SurfaceBg:         "#353B45",
		OverlayBg:         "#3E4451",
		TextPrimary:       "#ABB2BF",
		TextSecondary:     "#9DA5B4",
		TextMuted:         "#5C6370",
		TextSubtle:        "#4B5263",
		AccentBlue:        "#61AFEF",
		AccentMauve:       "#C678DD",
		AccentGreen:       "#98C379",
		AccentRed:         "#E06C75",
		AccentYellow:      "#E5C07B",
		AccentPeach:       "#D19A66",
		HeaderBg:          "#282C34",
		BorderClr:         "#353B45",
		BorderFocus:       "#4B5263",
		DiffAddedBg:       "#1a2e1a",
		DiffAddedEmphBg:   "#244024",
		DiffRemovedBg:     "#2e1a1a",
		DiffRemovedEmphBg: "#401f1f",
		DeltaSyntaxTheme:  "OneHalfDark",
		ChromaSyntaxStyle: "onedark",
	}
}

func themeRosePine() Theme {
	return Theme{
		Name:              "Rose Pine",
		ID:                "rose-pine",
		BaseBg:            "#191724",
		SurfaceBg:         "#1F1D2E",
		OverlayBg:         "#26233A",
		TextPrimary:       "#E0DEF4",
		TextSecondary:     "#908CAA",
		TextMuted:         "#6E6A86",
		TextSubtle:        "#524F67",
		AccentBlue:        "#9CCFD8",
		AccentMauve:       "#C4A7E7",
		AccentGreen:       "#3E8FB0", // Rose Pine "pine" — closest semantic green
		AccentRed:         "#EB6F92",
		AccentYellow:      "#F6C177",
		AccentPeach:       "#EBBCBA",
		HeaderBg:          "#191724",
		BorderClr:         "#1F1D2E",
		BorderFocus:       "#524F67",
		DiffAddedBg:       "#152030",
		DiffAddedEmphBg:   "#1a2d3d",
		DiffRemovedBg:     "#301525",
		DiffRemovedEmphBg: "#3d1a30",
		DeltaSyntaxTheme:  "Nord",
		ChromaSyntaxStyle: "monokai",
	}
}

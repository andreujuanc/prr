package ui

import (
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/andreujuanc/prr/internal/git"
)

// Theme defines the complete color palette for the TUI.
// Each theme provides colors for backgrounds, text, accents, diff highlighting,
// and the delta syntax theme name (used when delta is the diff renderer).
type Theme struct {
	Name string // human-readable display name
	ID   string // stable identifier for config persistence

	// Base backgrounds
	BaseBg    color.Color
	SurfaceBg color.Color
	OverlayBg color.Color

	// Text hierarchy
	TextPrimary   color.Color
	TextSecondary color.Color
	TextMuted     color.Color
	TextSubtle    color.Color

	// Accent colors
	AccentBlue   color.Color
	AccentMauve  color.Color
	AccentGreen  color.Color
	AccentRed    color.Color
	AccentYellow color.Color
	AccentPeach  color.Color

	// Semantic
	HeaderBg    color.Color
	BorderClr   color.Color
	BorderFocus color.Color

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

// hexOf renders a theme color as "#RRGGBB" for consumers that take hex
// strings (the delta/chroma diff renderers). lipgloss v2 colors are
// image/color values, so the source literal is no longer recoverable
// with a plain string conversion.
func hexOf(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02X%02X%02X", uint8(r>>8), uint8(g>>8), uint8(b>>8))
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
		AccentMauve:       hexOf(t.AccentMauve),
		AccentBlue:        hexOf(t.AccentBlue),
		AccentGreen:       hexOf(t.AccentGreen),
		AccentRed:         hexOf(t.AccentRed),
		AccentPeach:       hexOf(t.AccentPeach),
		SurfaceColor:      hexOf(t.OverlayBg),
		SubtleColor:       hexOf(t.TextSubtle),
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
		BaseBg:            lipgloss.Color("#1E1E2E"),
		SurfaceBg:         lipgloss.Color("#313244"),
		OverlayBg:         lipgloss.Color("#45475A"),
		TextPrimary:       lipgloss.Color("#CDD6F4"),
		TextSecondary:     lipgloss.Color("#A6ADC8"),
		TextMuted:         lipgloss.Color("#6C7086"),
		TextSubtle:        lipgloss.Color("#585B70"),
		AccentBlue:        lipgloss.Color("#89B4FA"),
		AccentMauve:       lipgloss.Color("#CBA6F7"),
		AccentGreen:       lipgloss.Color("#A6E3A1"),
		AccentRed:         lipgloss.Color("#F38BA8"),
		AccentYellow:      lipgloss.Color("#F9E2AF"),
		AccentPeach:       lipgloss.Color("#FAB387"),
		HeaderBg:          lipgloss.Color("#1E1E2E"),
		BorderClr:         lipgloss.Color("#313244"),
		BorderFocus:       lipgloss.Color("#585B70"),
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
		BaseBg:            lipgloss.Color("#282A36"),
		SurfaceBg:         lipgloss.Color("#44475A"),
		OverlayBg:         lipgloss.Color("#6272A4"),
		TextPrimary:       lipgloss.Color("#F8F8F2"),
		TextSecondary:     lipgloss.Color("#BFBFBF"),
		TextMuted:         lipgloss.Color("#6272A4"),
		TextSubtle:        lipgloss.Color("#44475A"),
		AccentBlue:        lipgloss.Color("#8BE9FD"),
		AccentMauve:       lipgloss.Color("#BD93F9"),
		AccentGreen:       lipgloss.Color("#50FA7B"),
		AccentRed:         lipgloss.Color("#FF5555"),
		AccentYellow:      lipgloss.Color("#F1FA8C"),
		AccentPeach:       lipgloss.Color("#FFB86C"),
		HeaderBg:          lipgloss.Color("#282A36"),
		BorderClr:         lipgloss.Color("#44475A"),
		BorderFocus:       lipgloss.Color("#6272A4"),
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
		BaseBg:            lipgloss.Color("#282828"),
		SurfaceBg:         lipgloss.Color("#3C3836"),
		OverlayBg:         lipgloss.Color("#504945"),
		TextPrimary:       lipgloss.Color("#EBDBB2"),
		TextSecondary:     lipgloss.Color("#D5C4A1"),
		TextMuted:         lipgloss.Color("#928374"),
		TextSubtle:        lipgloss.Color("#665C54"),
		AccentBlue:        lipgloss.Color("#83A598"),
		AccentMauve:       lipgloss.Color("#D3869B"),
		AccentGreen:       lipgloss.Color("#B8BB26"),
		AccentRed:         lipgloss.Color("#FB4934"),
		AccentYellow:      lipgloss.Color("#FABD2F"),
		AccentPeach:       lipgloss.Color("#FE8019"),
		HeaderBg:          lipgloss.Color("#282828"),
		BorderClr:         lipgloss.Color("#3C3836"),
		BorderFocus:       lipgloss.Color("#665C54"),
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
		BaseBg:            lipgloss.Color("#1A1B26"),
		SurfaceBg:         lipgloss.Color("#24283B"),
		OverlayBg:         lipgloss.Color("#414868"),
		TextPrimary:       lipgloss.Color("#C0CAF5"),
		TextSecondary:     lipgloss.Color("#A9B1D6"),
		TextMuted:         lipgloss.Color("#565F89"),
		TextSubtle:        lipgloss.Color("#3B4261"),
		AccentBlue:        lipgloss.Color("#7AA2F7"),
		AccentMauve:       lipgloss.Color("#BB9AF7"),
		AccentGreen:       lipgloss.Color("#9ECE6A"),
		AccentRed:         lipgloss.Color("#F7768E"),
		AccentYellow:      lipgloss.Color("#E0AF68"),
		AccentPeach:       lipgloss.Color("#FF9E64"),
		HeaderBg:          lipgloss.Color("#1A1B26"),
		BorderClr:         lipgloss.Color("#24283B"),
		BorderFocus:       lipgloss.Color("#3B4261"),
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
		BaseBg:            lipgloss.Color("#2E3440"),
		SurfaceBg:         lipgloss.Color("#3B4252"),
		OverlayBg:         lipgloss.Color("#434C5E"),
		TextPrimary:       lipgloss.Color("#ECEFF4"),
		TextSecondary:     lipgloss.Color("#D8DEE9"),
		TextMuted:         lipgloss.Color("#4C566A"),
		TextSubtle:        lipgloss.Color("#434C5E"),
		AccentBlue:        lipgloss.Color("#88C0D0"),
		AccentMauve:       lipgloss.Color("#B48EAD"),
		AccentGreen:       lipgloss.Color("#A3BE8C"),
		AccentRed:         lipgloss.Color("#BF616A"),
		AccentYellow:      lipgloss.Color("#EBCB8B"),
		AccentPeach:       lipgloss.Color("#D08770"),
		HeaderBg:          lipgloss.Color("#2E3440"),
		BorderClr:         lipgloss.Color("#3B4252"),
		BorderFocus:       lipgloss.Color("#4C566A"),
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
		BaseBg:            lipgloss.Color("#002B36"),
		SurfaceBg:         lipgloss.Color("#073642"),
		OverlayBg:         lipgloss.Color("#586E75"),
		TextPrimary:       lipgloss.Color("#FDF6E3"),
		TextSecondary:     lipgloss.Color("#EEE8D5"),
		TextMuted:         lipgloss.Color("#657B83"),
		TextSubtle:        lipgloss.Color("#586E75"),
		AccentBlue:        lipgloss.Color("#268BD2"),
		AccentMauve:       lipgloss.Color("#6C71C4"),
		AccentGreen:       lipgloss.Color("#859900"),
		AccentRed:         lipgloss.Color("#DC322F"),
		AccentYellow:      lipgloss.Color("#B58900"),
		AccentPeach:       lipgloss.Color("#CB4B16"),
		HeaderBg:          lipgloss.Color("#002B36"),
		BorderClr:         lipgloss.Color("#073642"),
		BorderFocus:       lipgloss.Color("#586E75"),
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
		BaseBg:            lipgloss.Color("#282C34"),
		SurfaceBg:         lipgloss.Color("#353B45"),
		OverlayBg:         lipgloss.Color("#3E4451"),
		TextPrimary:       lipgloss.Color("#ABB2BF"),
		TextSecondary:     lipgloss.Color("#9DA5B4"),
		TextMuted:         lipgloss.Color("#5C6370"),
		TextSubtle:        lipgloss.Color("#4B5263"),
		AccentBlue:        lipgloss.Color("#61AFEF"),
		AccentMauve:       lipgloss.Color("#C678DD"),
		AccentGreen:       lipgloss.Color("#98C379"),
		AccentRed:         lipgloss.Color("#E06C75"),
		AccentYellow:      lipgloss.Color("#E5C07B"),
		AccentPeach:       lipgloss.Color("#D19A66"),
		HeaderBg:          lipgloss.Color("#282C34"),
		BorderClr:         lipgloss.Color("#353B45"),
		BorderFocus:       lipgloss.Color("#4B5263"),
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
		BaseBg:            lipgloss.Color("#191724"),
		SurfaceBg:         lipgloss.Color("#1F1D2E"),
		OverlayBg:         lipgloss.Color("#26233A"),
		TextPrimary:       lipgloss.Color("#E0DEF4"),
		TextSecondary:     lipgloss.Color("#908CAA"),
		TextMuted:         lipgloss.Color("#6E6A86"),
		TextSubtle:        lipgloss.Color("#524F67"),
		AccentBlue:        lipgloss.Color("#9CCFD8"),
		AccentMauve:       lipgloss.Color("#C4A7E7"),
		AccentGreen:       lipgloss.Color("#3E8FB0"), // Rose Pine "pine" — closest semantic green
		AccentRed:         lipgloss.Color("#EB6F92"),
		AccentYellow:      lipgloss.Color("#F6C177"),
		AccentPeach:       lipgloss.Color("#EBBCBA"),
		HeaderBg:          lipgloss.Color("#191724"),
		BorderClr:         lipgloss.Color("#1F1D2E"),
		BorderFocus:       lipgloss.Color("#524F67"),
		DiffAddedBg:       "#152030",
		DiffAddedEmphBg:   "#1a2d3d",
		DiffRemovedBg:     "#301525",
		DiffRemovedEmphBg: "#3d1a30",
		DeltaSyntaxTheme:  "Nord",
		ChromaSyntaxStyle: "monokai",
	}
}

package ui

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
)

// hexOf is the only path turning theme colors back into the hex strings
// delta and chroma expect, so a silent change here degrades every diff.
func TestHexOf(t *testing.T) {
	cases := []struct {
		name string
		in   color.Color
		want string
	}{
		{"hex round-trips", lipgloss.Color("#1E1E2E"), "#1E1E2E"},
		{"lowercase input normalizes to upper", lipgloss.Color("#a6e3a1"), "#A6E3A1"},
		{"black", lipgloss.Color("#000000"), "#000000"},
		{"white", lipgloss.Color("#FFFFFF"), "#FFFFFF"},
		{"nil is empty, not a panic", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hexOf(tc.in); got != tc.want {
				t.Errorf("hexOf(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// DiffThemeFromCurrent feeds the diff renderers; every color field must
// come out as a usable hex string rather than an empty one.
func TestDiffThemeFromCurrent_ColorsAreHex(t *testing.T) {
	SetTheme(ThemeByID("catppuccin-mocha"))
	dt := DiffThemeFromCurrent()

	for name, got := range map[string]string{
		"AccentMauve":  dt.AccentMauve,
		"AccentBlue":   dt.AccentBlue,
		"AccentGreen":  dt.AccentGreen,
		"AccentRed":    dt.AccentRed,
		"AccentPeach":  dt.AccentPeach,
		"SurfaceColor": dt.SurfaceColor,
		"SubtleColor":  dt.SubtleColor,
	} {
		if len(got) != 7 || got[0] != '#' {
			t.Errorf("%s = %q, want a #RRGGBB string", name, got)
		}
	}
}

package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestSelectableRow_ExactWidthRegardlessOfContent(t *testing.T) {
	cases := []string{"", "x", "short", strings.Repeat("a", 100)}
	for _, c := range cases {
		for _, sel := range []bool{false, true} {
			got := SelectableRow(c, 30, sel)
			if w := ansi.StringWidth(got); w != 30 {
				t.Fatalf("content=%q selected=%v width=%d, want 30", c, sel, w)
			}
		}
	}
}

func TestSelectableRow_SelectedAndUnselectedSameWidth(t *testing.T) {
	a := SelectableRow("hello", 40, true)
	b := SelectableRow("hello", 40, false)
	if ansi.StringWidth(a) != ansi.StringWidth(b) {
		t.Fatalf("selected width=%d, unselected width=%d", ansi.StringWidth(a), ansi.StringWidth(b))
	}
}

func TestSelectableRow_ANSIPreserved(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff00ff")).Render("magenta")
	got := SelectableRow(styled, 20, false)
	if !strings.Contains(stripANSI(got), "magenta") {
		t.Fatalf("expected 'magenta' substring in %q", stripANSI(got))
	}
	if ansi.StringWidth(got) != 20 {
		t.Fatalf("width=%d, want 20", ansi.StringWidth(got))
	}
}

func TestSelectableRow_TinyWidth_TruncatesGracefully(t *testing.T) {
	got := SelectableRow("hello world", 2, true)
	if w := ansi.StringWidth(got); w > 2 {
		t.Fatalf("width=%d, want <=2", w)
	}
}

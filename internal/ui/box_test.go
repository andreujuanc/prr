package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// lineWidths returns the visible width of every line in s.
func lineWidths(s string) []int {
	lines := strings.Split(s, "\n")
	widths := make([]int, len(lines))
	for i, l := range lines {
		widths[i] = ansi.StringWidth(l)
	}
	return widths
}

func assertUniformWidth(t *testing.T, out string, want int) {
	t.Helper()
	for i, w := range lineWidths(out) {
		if w != want {
			t.Fatalf("line %d width = %d, want %d. line: %q", i, w, want, strings.Split(out, "\n")[i])
		}
	}
}

func TestBox_ExactFitContent(t *testing.T) {
	b := Box{Width: 10, Title: "hi"}
	out := b.Render("hello")
	assertUniformWidth(t, out, 10)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
}

func TestBox_OversizedContent_TruncatesAndKeepsRightRail(t *testing.T) {
	b := Box{Width: 10}
	out := b.Render("this is way too long to fit")
	assertUniformWidth(t, out, 10)
	bdr := lipgloss.RoundedBorder()
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.HasSuffix(stripANSI(line), bdr.Right) &&
			!strings.HasSuffix(stripANSI(line), bdr.TopRight) &&
			!strings.HasSuffix(stripANSI(line), bdr.BottomRight) {
			t.Fatalf("line %q missing right rail", line)
		}
	}
}

func TestBox_UndersizedContent_PadsToWidth(t *testing.T) {
	b := Box{Width: 20}
	out := b.Render("hi")
	assertUniformWidth(t, out, 20)
}

func TestBox_TitleLongerThanInnerWidth_Truncated(t *testing.T) {
	b := Box{Width: 12, Title: "this title is far too long"}
	out := b.Render("body")
	assertUniformWidth(t, out, 12)
	top := strings.Split(out, "\n")[0]
	if !strings.Contains(stripANSI(top), "…") {
		t.Fatalf("expected ellipsis in truncated title, got %q", stripANSI(top))
	}
}

func TestBox_ContentWithANSI_VisualWidthRespected(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render("redtext")
	b := Box{Width: 15}
	out := b.Render(styled)
	assertUniformWidth(t, out, 15)
}

func TestBox_FocusedAndUnfocused_SameDimensions(t *testing.T) {
	// Color differences are stripped when stdout is not a TTY, so the only
	// thing we can assert in tests is that both renderings have identical
	// dimensions and cell widths.
	b1 := Box{Width: 10, Focused: true}
	b2 := Box{Width: 10, Focused: false}
	o1 := b1.Render("x")
	o2 := b2.Render("x")
	w1 := lineWidths(o1)
	w2 := lineWidths(o2)
	if len(w1) != len(w2) {
		t.Fatalf("line count mismatch: focused=%d unfocused=%d", len(w1), len(w2))
	}
	for i := range w1 {
		if w1[i] != w2[i] {
			t.Fatalf("line %d width mismatch: focused=%d unfocused=%d", i, w1[i], w2[i])
		}
	}
}

func TestBox_WidthTooSmall_Empty(t *testing.T) {
	if got := (Box{Width: 3}.Render("x")); got != "" {
		t.Fatalf("Width<4 should return empty, got %q", got)
	}
}

func TestBox_FixedHeight_ExactLineCount(t *testing.T) {
	b := Box{Width: 10, Height: 5}
	out := b.Render("a\nb")
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5", len(lines))
	}
	assertUniformWidth(t, out, 10)
}

func TestBox_Padding_ReducesInnerWidth(t *testing.T) {
	b := Box{Width: 20, Padding: Padding{Left: 2, Right: 2}}
	out := b.Render("x")
	assertUniformWidth(t, out, 20)
	// Body row should have at least 2 spaces after the left rail and before
	// the right rail (the padding).
	bodyLine := strings.Split(out, "\n")[1]
	plain := stripANSI(bodyLine)
	bdr := lipgloss.RoundedBorder()
	if !strings.HasPrefix(plain, bdr.Left+"  ") {
		t.Fatalf("expected 2-space left padding, got %q", plain)
	}
	if !strings.HasSuffix(plain, "  "+bdr.Right) {
		t.Fatalf("expected 2-space right padding, got %q", plain)
	}
}

func TestBox_NoTitle_TopAllBorder(t *testing.T) {
	b := Box{Width: 10}
	out := b.Render("body")
	top := stripANSI(strings.Split(out, "\n")[0])
	bdr := lipgloss.RoundedBorder()
	want := bdr.TopLeft + strings.Repeat(bdr.Top, 8) + bdr.TopRight
	if top != want {
		t.Fatalf("top border = %q, want %q", top, want)
	}
}

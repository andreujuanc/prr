package ui

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

func sampleReview(titles ...string) *state.ReviewOutput {
	out := &state.ReviewOutput{
		Summary: "Sample review for testing.",
		Verdict: "comment",
	}
	for i, title := range titles {
		out.Findings = append(out.Findings, state.ReviewFinding{
			Severity:   "high",
			Category:   "bug",
			File:       "internal/auth/token.go",
			Line:       42 + i,
			Title:      title,
			Detail:     "Detailed explanation that should only render when expanded.",
			Suggestion: "Concrete fix suggestion that should only render when expanded.",
		})
	}
	return out
}

// TestRenderStructuredReview_CollapsedByDefault pins the new default
// behavior: when expanded map is nil/empty, all findings render in
// compact form — title only, no detail/suggestion/file:line body.
func TestRenderStructuredReview_CollapsedByDefault(t *testing.T) {
	review := sampleReview("Token expiry check uses <= instead of <")

	rendered, findings := renderStructuredReview(review, 80, -1, nil, false, "")
	if len(findings) != 1 {
		t.Fatalf("findings len = %d, want 1", len(findings))
	}
	if !strings.Contains(rendered, "Token expiry") {
		t.Errorf("expected title in output, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "Detailed explanation") {
		t.Error("collapsed finding rendered body content; should be header-only")
	}
	if strings.Contains(rendered, "Concrete fix") {
		t.Error("collapsed finding rendered suggestion; should be header-only")
	}
}

// TestRenderStructuredReview_ExpandedShowsBody pins the expansion
// contract: when index N is in the expanded map, finding N renders its
// body (file:line + detail + suggestion).
func TestRenderStructuredReview_ExpandedShowsBody(t *testing.T) {
	review := sampleReview("First finding", "Second finding")
	expanded := map[int]bool{0: true} // only first finding expanded

	rendered, _ := renderStructuredReview(review, 80, -1, expanded, false, "")

	if !strings.Contains(rendered, "Detailed explanation") {
		t.Error("expanded finding 0 missing detail in output")
	}
	if !strings.Contains(rendered, "Concrete fix") {
		t.Error("expanded finding 0 missing suggestion in output")
	}
	// Both titles should be present (one expanded, one collapsed).
	if !strings.Contains(rendered, "First finding") {
		t.Error("missing first title")
	}
	if !strings.Contains(rendered, "Second finding") {
		t.Error("missing second title")
	}
	// Body should appear once (only finding 0 expanded). Count by
	// substring occurrence — they're unique strings in sampleReview.
	if got := strings.Count(rendered, "Detailed explanation"); got != 1 {
		t.Errorf("detail substring count = %d, want 1 (only one finding expanded)", got)
	}
}

// TestRenderFindingHeader_LongTitleWrapsCleanly is the load-bearing
// guarantee from the bug report: titles longer than panel width must
// wrap to multiple styled lines, not get truncated.
//
// Strip ANSI when checking text content so style codes don't perturb
// substring matches.
func TestRenderFindingHeader_LongTitleWrapsCleanly(t *testing.T) {
	longTitle := "Token expiry check uses <= so a token expiring at exactly now is still accepted creating a one-second window of vulnerability"
	review := sampleReview(longTitle)

	rendered, _ := renderStructuredReview(review, 60, -1, nil, false, "")
	plain := stripANSI(rendered)

	// The full title text must appear somewhere — assembled across
	// multiple lines for the long case. Strip newlines for the check.
	flat := strings.Join(strings.Fields(plain), " ")
	if !strings.Contains(flat, longTitle) {
		t.Errorf("long title was truncated; want full text in output\nflat: %q\nfull rendered:\n%s", flat, plain)
	}
	// And it must actually span multiple lines (otherwise wrapping
	// didn't happen — which would also pass the above check
	// trivially).
	titleLines := 0
	for line := range strings.SplitSeq(plain, "\n") {
		if strings.Contains(line, "Token") || strings.Contains(line, "expiring") || strings.Contains(line, "vulnerability") {
			titleLines++
		}
	}
	if titleLines < 2 {
		t.Errorf("expected title to wrap across multiple lines at width=60; got %d title-bearing lines\n%s", titleLines, plain)
	}
}

// TestRenderFindingHeader_ShortTitleSingleLine pins the fast-path: when
// the title fits within the available width, it renders on the same
// line as the badges. Multi-line rendering would be ugly noise here.
func TestRenderFindingHeader_ShortTitleSingleLine(t *testing.T) {
	review := sampleReview("Short title")
	rendered, _ := renderStructuredReview(review, 80, -1, nil, false, "")

	// Find the line containing the title; it must also contain the
	// severity badge — meaning they're on the same line.
	for line := range strings.SplitSeq(stripANSI(rendered), "\n") {
		if strings.Contains(line, "Short title") {
			if !strings.Contains(line, "[high]") {
				t.Errorf("short title rendered separately from badge:\n%s\nfull: %s", line, stripANSI(rendered))
			}
			return
		}
	}
	t.Errorf("title not found in output:\n%s", stripANSI(rendered))
}

// TestRenderStructuredReview_ResolvedAlwaysCompact pins that resolved
// findings ignore the expanded map — they always render as a single
// line. The body is suppressed regardless of expansion.
func TestRenderStructuredReview_ResolvedAlwaysCompact(t *testing.T) {
	review := sampleReview("Resolved finding")
	review.Findings[0].Resolved = true
	expanded := map[int]bool{0: true} // try to expand it

	rendered, _ := renderStructuredReview(review, 80, -1, expanded, false, "")

	if strings.Contains(rendered, "Detailed explanation") {
		t.Error("resolved finding rendered body despite resolution; should be compact")
	}
	if strings.Contains(rendered, "Concrete fix") {
		t.Error("resolved finding rendered suggestion despite resolution")
	}
}

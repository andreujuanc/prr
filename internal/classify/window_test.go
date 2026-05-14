package classify

import (
	"fmt"
	"strings"
	"testing"
)

// makeNumberedLines builds a deterministic multi-line string of the
// form "line 0\nline 1\n...". Used by the windowing tests so we can
// assert which exact lines were preserved.
func makeNumberedLines(n int) string {
	parts := make([]string, n)
	for i := range n {
		parts[i] = fmt.Sprintf("line %d", i)
	}
	return strings.Join(parts, "\n")
}

func TestWindowForClassify_ShortFile_ReturnedWhole(t *testing.T) {
	// Files at or below the threshold are sent in full — there's no
	// signal lost by sending the whole thing.
	content := makeNumberedLines(50)
	got := windowForClassify(content)
	if got != content {
		t.Errorf("short file should be returned unchanged; got:\n%s", got)
	}
}

func TestWindowForClassify_AtThreshold_ReturnedWhole(t *testing.T) {
	// Exact threshold (100 lines): still sent whole. Pins the
	// boundary so an off-by-one in the comparison surfaces.
	content := makeNumberedLines(100)
	got := windowForClassify(content)
	if got != content {
		t.Errorf("at-threshold file should be returned unchanged")
	}
}

func TestWindowForClassify_JustOverThreshold_NoOverlap(t *testing.T) {
	// 110 lines: top = 0..49, middle = 50..99 (shifted because the
	// natural center would land at 30..80, which would overlap top).
	// The two windows must be contiguous and NOT include a marker
	// (no gap to mark).
	content := makeNumberedLines(110)
	got := windowForClassify(content)

	if strings.Contains(got, "lines omitted") {
		t.Errorf("contiguous windows must not emit omitted-middle marker; got:\n%s", got)
	}
	// Must contain top boundary and middle boundary, but NOT tail.
	if !strings.Contains(got, "line 0") {
		t.Errorf("missing top line; got:\n%s", got)
	}
	if !strings.Contains(got, "line 99") {
		t.Errorf("missing last line of middle window; got:\n%s", got)
	}
	if strings.Contains(got, "line 109") {
		t.Errorf("tail must be dropped; got:\n%s", got)
	}
}

func TestWindowForClassify_LargeFile_HasGapAndMarker(t *testing.T) {
	// 1000 lines: top = 0..49, middle = 475..524 (centered on 500).
	// Gap = 425 lines, marker must surface that exact count.
	content := makeNumberedLines(1000)
	got := windowForClassify(content)

	if !strings.Contains(got, "line 0") {
		t.Error("missing top window")
	}
	if !strings.Contains(got, "line 49") {
		t.Error("missing top boundary (line 49)")
	}
	if strings.Contains(got, "line 100") {
		t.Error("line 100 is in the gap and should be omitted")
	}
	if !strings.Contains(got, "line 475") {
		t.Error("missing middle window start (line 475)")
	}
	if !strings.Contains(got, "line 524") {
		t.Error("missing middle window end (line 524)")
	}
	if strings.Contains(got, "line 600") {
		t.Error("tail past middle window must be dropped")
	}

	// Marker must report the actual omitted count.
	wantMarker := "[425 lines omitted]"
	if !strings.Contains(got, wantMarker) {
		t.Errorf("expected marker %q in output; got:\n%s", wantMarker, got)
	}
}

func TestWindowForClassify_MediumFile_CenteredMiddle(t *testing.T) {
	// 200 lines: top = 0..49, middle should be centered on line 100
	// → 75..124. Gap = 75-50 = 25 lines.
	content := makeNumberedLines(200)
	got := windowForClassify(content)

	if !strings.Contains(got, "line 75") {
		t.Error("middle should start at line 75 for a 200-line file")
	}
	if !strings.Contains(got, "line 124") {
		t.Error("middle should end at line 124 for a 200-line file")
	}
	if strings.Contains(got, "line 150") {
		t.Error("post-middle content must be dropped")
	}
	if !strings.Contains(got, "[25 lines omitted]") {
		t.Errorf("expected gap marker; got:\n%s", got)
	}
}

func TestWindowForClassify_PreservesLineOrder(t *testing.T) {
	// The output must read top-then-middle so the LLM sees imports
	// before bodies. A reordering bug would yield a confused model.
	content := makeNumberedLines(500)
	got := windowForClassify(content)

	topIdx := strings.Index(got, "line 0")
	midIdx := strings.Index(got, "line 225") // middle starts at 500/2 - 25 = 225
	if topIdx < 0 || midIdx < 0 {
		t.Fatalf("expected both top and middle markers in output:\n%s", got)
	}
	if topIdx >= midIdx {
		t.Errorf("top must come before middle (top=%d, middle=%d)", topIdx, midIdx)
	}
}

func TestWindowForClassify_EmptyContent(t *testing.T) {
	// Defensive: a zero-line file shouldn't panic or emit a marker.
	got := windowForClassify("")
	if got != "" {
		t.Errorf("empty content should return empty; got %q", got)
	}
}

func TestWindowForClassify_PinsWindowSizes(t *testing.T) {
	// Pin the constants so future tuning surfaces here.
	if classifyTopWindow != 50 {
		t.Errorf("classifyTopWindow = %d, want 50", classifyTopWindow)
	}
	if classifyMidWindow != 50 {
		t.Errorf("classifyMidWindow = %d, want 50", classifyMidWindow)
	}
	if classifyWindowThreshold != 100 {
		t.Errorf("classifyWindowThreshold = %d, want 100", classifyWindowThreshold)
	}
}

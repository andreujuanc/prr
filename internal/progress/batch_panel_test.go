package progress

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// stripANSI removes lipgloss color codes so the assertions don't have
// to know about terminal escape sequences. The panel's content shape
// (labels, counts, icons, layout) is what we're pinning, not its
// color palette.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// fixedNow gives deterministic elapsed timers for snapshot stability.
var fixedNow = time.Date(2026, 5, 20, 12, 0, 30, 0, time.UTC)

// TestRenderBatchesPanel_EmptyState returns "" when no batches have
// been seen yet — the panel should disappear cleanly, not render an
// empty header.
func TestRenderBatchesPanel_EmptyState(t *testing.T) {
	if got := RenderBatchesPanel(&State{}, BatchPanelOptions{}); got != "" {
		t.Errorf("empty state: got %q, want empty", got)
	}
	if got := RenderBatchesPanel(nil, BatchPanelOptions{}); got != "" {
		t.Errorf("nil state: got %q, want empty", got)
	}
}

// TestRenderBatchesPanel_SmallShape exercises three batches: one
// active streaming, one cached (tail), one done (tail). Pins the
// header line, the active row's components, and the tail icons.
func TestRenderBatchesPanel_SmallShape(t *testing.T) {
	s := &State{Batches: map[int]*BatchState{
		0: {
			Index: 0, Label: "auth/injection [critical]", Files: 2, Kind: "aoi-driven",
			Status: BatchActive, StartedAt: fixedNow.Add(-7 * time.Second), Bytes: 1100,
		},
		1: {
			Index: 1, Label: "internal/git", Files: 5, Kind: "general",
			Status: BatchDone,
			StartedAt: fixedNow.Add(-20 * time.Second), EndedAt: fixedNow.Add(-6 * time.Second),
		},
		2: {
			Index: 2, Label: "internal/config", Files: 1, Kind: "general",
			Status: BatchCached,
			StartedAt: fixedNow.Add(-20 * time.Second), EndedAt: fixedNow.Add(-12 * time.Second),
		},
	}}

	out := stripANSI(RenderBatchesPanel(s, BatchPanelOptions{Now: fixedNow}))

	// Header counts.
	wantHeader := "Batches  active 1 · done 1 · cached 1 · queued 0"
	if !strings.Contains(out, wantHeader) {
		t.Errorf("header missing %q in:\n%s", wantHeader, out)
	}

	// Active row: label, files, elapsed, percent.
	for _, want := range []string{
		"▶",
		"auth/injection [critical]",
		"2 files",
		"0:07",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("active row missing %q in:\n%s", want, out)
		}
	}
	// Bytes 1100 of estimate 4000 = 27.5%, rendered as "27%".
	if !strings.Contains(out, "27%") {
		t.Errorf("active row missing 27%% (1100/4000); got:\n%s", out)
	}

	// Tail rows.
	if !strings.Contains(out, "✓") {
		t.Errorf("tail missing ✓ icon; got:\n%s", out)
	}
	if !strings.Contains(out, "internal/git") {
		t.Errorf("tail missing internal/git; got:\n%s", out)
	}
	if !strings.Contains(out, "cached") {
		t.Errorf("tail missing cached suffix; got:\n%s", out)
	}
	// No overflow line for a 1-active panel.
	if strings.Contains(out, "more active") {
		t.Errorf("did not expect overflow line; got:\n%s", out)
	}
}

// TestRenderBatchesPanel_OverflowAt11Active pins the overflow rule:
// 11 active batches with MaxActiveRows=10 should render exactly 10
// detail rows and one "+1 more active" line.
func TestRenderBatchesPanel_OverflowAt11Active(t *testing.T) {
	batches := make(map[int]*BatchState, 11)
	for i := 0; i < 11; i++ {
		batches[i] = &BatchState{
			Index: i, Label: "dir/" + string(rune('a'+i)), Files: 1, Kind: "general",
			Status: BatchActive, StartedAt: fixedNow.Add(-time.Duration(i+1) * time.Second),
		}
	}
	out := stripANSI(RenderBatchesPanel(&State{Batches: batches}, BatchPanelOptions{
		MaxActiveRows: 10, Now: fixedNow,
	}))

	rowCount := strings.Count(out, "▶")
	if rowCount != 10 {
		t.Errorf("active row count = %d, want 10 (MaxActiveRows cap)", rowCount)
	}
	if !strings.Contains(out, "+1 more active") {
		t.Errorf("missing overflow line; got:\n%s", out)
	}
}

// TestRenderBatchesPanel_OverflowAt30Active is the user-requested
// shape: 30 batches all active, only 10 shown with the overflow
// summary. Pin both that the header reflects the real total and that
// no row leaks past the cap.
func TestRenderBatchesPanel_OverflowAt30Active(t *testing.T) {
	batches := make(map[int]*BatchState, 30)
	for i := 0; i < 30; i++ {
		batches[i] = &BatchState{
			Index: i, Label: "batch-" + string(rune('a'+i%26)), Files: 1, Kind: "general",
			Status: BatchActive, StartedAt: fixedNow.Add(-time.Duration(i+1) * time.Second),
		}
	}
	out := stripANSI(RenderBatchesPanel(&State{Batches: batches}, BatchPanelOptions{
		MaxActiveRows: 10, Now: fixedNow,
	}))

	if !strings.Contains(out, "active 30") {
		t.Errorf("header missing 'active 30'; got:\n%s", out)
	}
	if rowCount := strings.Count(out, "▶"); rowCount != 10 {
		t.Errorf("active row count = %d, want 10", rowCount)
	}
	if !strings.Contains(out, "+20 more active") {
		t.Errorf("missing overflow line '+20 more active'; got:\n%s", out)
	}
}

// TestRenderBatchesPanel_IndeterminateBarWhenNoBytes covers the
// fallback path: an active batch with Bytes=0 must render the dotted
// strip and the "streaming" status, not a 0% bar.
func TestRenderBatchesPanel_IndeterminateBarWhenNoBytes(t *testing.T) {
	s := &State{Batches: map[int]*BatchState{
		0: {
			Index: 0, Label: "no-stream-yet", Files: 1,
			Status: BatchActive, StartedAt: fixedNow.Add(-2 * time.Second),
		},
	}}
	out := stripANSI(RenderBatchesPanel(s, BatchPanelOptions{Now: fixedNow}))
	if !strings.Contains(out, "streaming") {
		t.Errorf("expected 'streaming' status for Bytes=0; got:\n%s", out)
	}
	if strings.Contains(out, "0%") {
		t.Errorf("Bytes=0 should not render a 0%% bar; got:\n%s", out)
	}
}

// TestRenderBatchesPanel_RecentTailCap ensures only the last
// RecentTail completions surface (most-recent first).
func TestRenderBatchesPanel_RecentTailCap(t *testing.T) {
	mk := func(idx int, when time.Duration) *BatchState {
		return &BatchState{
			Index: idx, Label: "f" + string(rune('a'+idx)), Files: 1, Kind: "general",
			Status: BatchDone,
			StartedAt: fixedNow.Add(when - 5*time.Second),
			EndedAt:   fixedNow.Add(when),
		}
	}
	s := &State{Batches: map[int]*BatchState{
		0: mk(0, -10*time.Second),
		1: mk(1, -8*time.Second),
		2: mk(2, -6*time.Second),
		3: mk(3, -4*time.Second),
		4: mk(4, -2*time.Second),
	}}
	out := stripANSI(RenderBatchesPanel(s, BatchPanelOptions{
		RecentTail: 3, Now: fixedNow,
	}))

	// Only fc, fd, fe (the three most-recent ends) should appear.
	for _, want := range []string{"fc", "fd", "fe"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in tail; got:\n%s", want, out)
		}
	}
	for _, dontWant := range []string{"fa", "fb"} {
		if strings.Contains(out, dontWant) {
			t.Errorf("did not expect older finish %q in tail; got:\n%s", dontWant, out)
		}
	}
}

// TestRenderBatchesPanel_FinishedOverflow pins the symmetric overflow
// line on the finished tail: when more batches finish than RecentTail
// allows, a "+N more finished" indicator surfaces so the count
// reconciles with the header line. Before this, the header could say
// "done 12" while the tail showed only 3 rows with no hint that more
// existed.
func TestRenderBatchesPanel_FinishedOverflow(t *testing.T) {
	batches := make(map[int]*BatchState, 12)
	for i := 0; i < 12; i++ {
		batches[i] = &BatchState{
			Index: i, Label: "done-" + string(rune('a'+i)), Files: 1, Kind: "general",
			Status:    BatchDone,
			StartedAt: fixedNow.Add(-time.Duration(20-i) * time.Second),
			EndedAt:   fixedNow.Add(-time.Duration(12-i) * time.Second),
		}
	}
	out := stripANSI(RenderBatchesPanel(&State{Batches: batches}, BatchPanelOptions{
		RecentTail: 10, Now: fixedNow,
	}))

	if !strings.Contains(out, "done 12") {
		t.Errorf("header missing 'done 12'; got:\n%s", out)
	}
	if !strings.Contains(out, "+2 more finished") {
		t.Errorf("missing finished overflow line; got:\n%s", out)
	}
}

// TestBatchPanelActive_Gating pins the BatchPhases allowlist: panel
// only renders when one of the listed phases is active.
func TestBatchPanelActive_Gating(t *testing.T) {
	phases := []phaseInfo{
		{Def: PhaseDef{Name: "p1"}, Status: PhaseDone},
		{Def: PhaseDef{Name: "p2"}, Status: PhaseActive},
		{Def: PhaseDef{Name: "p3"}, Status: PhaseWaiting},
	}

	cfg := Config{BatchPhases: []string{"p2"}}
	if !cfg.BatchPanelActive(phases) {
		t.Error("p2 active and listed in BatchPhases: expected panel active")
	}

	cfg = Config{BatchPhases: []string{"p1"}}
	if cfg.BatchPanelActive(phases) {
		t.Error("p1 in BatchPhases but done: expected panel inactive")
	}

	cfg = Config{} // empty allowlist
	if cfg.BatchPanelActive(phases) {
		t.Error("empty BatchPhases: expected panel inactive")
	}
}

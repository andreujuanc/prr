package progress

import (
	"testing"
)

// ── truncate ────────────────────────────────────────────────────────────

func TestTruncate_Short(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTruncate_Exact(t *testing.T) {
	if got := truncate("hello", 5); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTruncate_Long(t *testing.T) {
	got := truncate("hello world", 6)
	if got != "hello…" {
		t.Errorf("expected 'hello…', got %q", got)
	}
}

func TestTruncate_Empty(t *testing.T) {
	if got := truncate("", 5); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// ── Phase lifecycle ─────────────────────────────────────────────────────
//
// applyEvent transitions waiting → active on first message, then any
// later phase activating marks earlier active phases done. This is the
// load-bearing logic for "show me which phase the pipeline is in".

func newTestModel(names ...string) *model {
	phases := make([]PhaseDef, len(names))
	for i, n := range names {
		phases[i] = PhaseDef{Name: n, Label: n}
	}
	return newUI(Config{Phases: phases})
}

func TestApplyEvent_ActivatesPhase(t *testing.T) {
	m := newTestModel("p1", "p2", "p3")
	m.applyEvent("p1", "starting")

	if m.phases[0].Status != PhaseActive {
		t.Errorf("expected p1 active, got %q", m.phases[0].Status)
	}
	if m.phases[0].Detail != "starting" {
		t.Errorf("expected detail 'starting', got %q", m.phases[0].Detail)
	}
}

func TestApplyEvent_MarksPreviousDone(t *testing.T) {
	m := newTestModel("p1", "p2", "p3")
	m.applyEvent("p1", "go")
	m.applyEvent("p2", "scanning")

	if m.phases[0].Status != PhaseDone {
		t.Errorf("expected p1 done, got %q", m.phases[0].Status)
	}
	if m.phases[1].Status != PhaseActive {
		t.Errorf("expected p2 active, got %q", m.phases[1].Status)
	}
}

func TestApplyEvent_UnknownPhaseIgnored(t *testing.T) {
	// An emit for a phase that wasn't declared in Config.Phases must
	// not alter known phase states. Pipelines change message strings;
	// drift shouldn't crash the UI.
	m := newTestModel("p1", "p2")
	m.applyEvent("p1", "go")
	m.applyEvent("bogus", "should not exist")

	if m.phases[0].Status != PhaseActive {
		t.Errorf("p1 should still be active after unknown-phase emit, got %q", m.phases[0].Status)
	}
	if m.phases[1].Status != PhaseWaiting {
		t.Errorf("p2 should still be waiting, got %q", m.phases[1].Status)
	}
}

func TestApplyEvent_WarningSetsBanner(t *testing.T) {
	m := newTestModel("p1")
	m.applyEvent("warning", "Disk space low")

	if m.warning != "Disk space low" {
		t.Errorf("expected warning 'Disk space low', got %q", m.warning)
	}
	// Warning must not advance a phase.
	if m.phases[0].Status != PhaseWaiting {
		t.Errorf("warning should not activate phases, got %q", m.phases[0].Status)
	}
}

func TestApplyEvent_DispatchesToParseEvent(t *testing.T) {
	called := false
	cfg := Config{
		Phases: []PhaseDef{{Name: "p1", Label: "p1"}},
		ParseEvent: func(s *State, phase, message string) {
			called = true
			s.Counters["seen"] = 1
		},
	}
	m := newUI(cfg)
	m.applyEvent("p1", "hello")

	if !called {
		t.Error("ParseEvent should have been called")
	}
	if m.state.Counters["seen"] != 1 {
		t.Errorf("expected counter set by ParseEvent, got %v", m.state.Counters)
	}
}

// ── activeProgress ──────────────────────────────────────────────────────

func TestActiveProgress_NoActivePhase(t *testing.T) {
	m := newTestModel("p1", "p2")
	if got := m.activeProgress(); got != 0 {
		t.Errorf("expected 0, got %f", got)
	}
}

func TestActiveProgress_UsesActivePhaseFn(t *testing.T) {
	cfg := Config{
		Phases: []PhaseDef{
			{Name: "p1", Label: "p1"},
			{Name: "p2", Label: "p2", ProgressFn: func(s *State) float64 {
				return float64(s.Counters["done"]) / float64(s.Counters["total"])
			}},
		},
	}
	m := newUI(cfg)
	m.phases[1].Status = PhaseActive
	m.state.Counters["done"] = 3
	m.state.Counters["total"] = 4

	if got := m.activeProgress(); got != 0.75 {
		t.Errorf("expected 0.75, got %f", got)
	}
}

func TestActiveProgress_NilProgressFn(t *testing.T) {
	// Phases without a ProgressFn don't drive the bar.
	m := newTestModel("p1")
	m.phases[0].Status = PhaseActive

	if got := m.activeProgress(); got != 0 {
		t.Errorf("expected 0 for phase with no ProgressFn, got %f", got)
	}
}

// ── Inline counter rendering ─────────────────────────────────────────
//
// Pin that the "X/Y" counter shows up in the rendered View while a
// phase is active or done, but not while waiting (no point showing
// 0/0) and not when no Counter is configured.

func renderWithCounter(t *testing.T, status PhaseStatus, total int) string {
	t.Helper()
	cfg := Config{
		Phases: []PhaseDef{{
			Name:  "p1",
			Label: "P1",
			Counter: func(s *State) (int, int) {
				return s.Counters["done"], s.Counters["total"]
			},
		}},
	}
	m := newUI(cfg)
	m.phases[0].Status = status
	m.state.Counters["done"] = 7
	m.state.Counters["total"] = total
	return m.View()
}

func TestView_CounterShownWhileActive(t *testing.T) {
	out := renderWithCounter(t, PhaseActive, 40)
	if !contains(out, "7/40") {
		t.Errorf("active phase should render 7/40 counter; output was:\n%s", out)
	}
}

func TestView_CounterShownWhenDone(t *testing.T) {
	out := renderWithCounter(t, PhaseDone, 40)
	if !contains(out, "7/40") {
		t.Errorf("done phase should still render the counter; output was:\n%s", out)
	}
}

func TestView_CounterHiddenWhileWaiting(t *testing.T) {
	// Phase hasn't started — showing 7/40 here would be misleading
	// (the 7 is from a future state never reached).
	out := renderWithCounter(t, PhaseWaiting, 40)
	if contains(out, "7/40") {
		t.Errorf("waiting phase should not render the counter; output was:\n%s", out)
	}
}

func TestView_CounterHiddenWhenTotalZero(t *testing.T) {
	// Total not yet known — a "5/0" or "0/0" would be confusing.
	out := renderWithCounter(t, PhaseActive, 0)
	if contains(out, "/0") {
		t.Errorf("zero-total phase should hide the counter; output was:\n%s", out)
	}
}

// ── Summary rendering on done state ─────────────────────────────────
//
// Pin that PhaseDef.Summary replaces the live detail line when the
// phase reaches done — and that it falls back to the live detail
// when Summary returns "" or is unset.

func renderWithSummary(t *testing.T, status PhaseStatus, summary func(*State) string, liveDetail string) string {
	t.Helper()
	cfg := Config{
		Phases: []PhaseDef{{
			Name:    "p1",
			Label:   "P1",
			Summary: summary,
		}},
	}
	m := newUI(cfg)
	m.phases[0].Status = status
	m.phases[0].Detail = liveDetail
	return m.View()
}

func TestView_SummaryReplacesDetailWhenDone(t *testing.T) {
	out := renderWithSummary(t, PhaseDone,
		func(*State) string { return "kept 11 · dismissed 4" },
		"Recheck complete: kept 11, dismissed 4, consolidated 2, modified 1")
	if !contains(out, "kept 11 · dismissed 4") {
		t.Errorf("done phase should render Summary instead of live detail; got:\n%s", out)
	}
	// Live detail should not also appear — Summary replaces it.
	if contains(out, "Recheck complete: kept 11, dismissed 4, consolidated 2, modified 1") {
		t.Errorf("live detail should be suppressed when Summary is non-empty; got:\n%s", out)
	}
}

func TestView_SummaryNotShownWhileActive(t *testing.T) {
	// Active phase keeps the live, last-write-wins detail so users see
	// liveness. Summary is only for the done state.
	out := renderWithSummary(t, PhaseActive,
		func(*State) string { return "kept 11 · dismissed 4" },
		"Rechecking 18 findings...")
	if !contains(out, "Rechecking 18 findings...") {
		t.Errorf("active phase should still render live detail; got:\n%s", out)
	}
	if contains(out, "kept 11 · dismissed 4") {
		t.Errorf("active phase should not render Summary; got:\n%s", out)
	}
}

func TestView_SummaryEmptyFallsBackToDetail(t *testing.T) {
	// When Summary returns "" (e.g. no data captured yet), the row
	// keeps showing whatever the live detail was at done time.
	out := renderWithSummary(t, PhaseDone,
		func(*State) string { return "" },
		"Project context ready")
	if !contains(out, "Project context ready") {
		t.Errorf("empty Summary should fall back to live detail; got:\n%s", out)
	}
}

func TestView_NoSummaryFnFallsBackToDetail(t *testing.T) {
	// When PhaseDef.Summary is nil, current behavior preserved.
	cfg := Config{Phases: []PhaseDef{{Name: "p1", Label: "P1"}}}
	m := newUI(cfg)
	m.phases[0].Status = PhaseDone
	m.phases[0].Detail = "Phase 1 complete: 42 files to audit"
	if !contains(m.View(), "Phase 1 complete: 42 files to audit") {
		t.Errorf("nil Summary should preserve current detail rendering")
	}
}

// contains is a strings.Contains shim that keeps imports tight.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

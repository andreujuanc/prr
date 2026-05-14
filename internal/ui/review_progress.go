package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/andreujuanc/prr/internal/progress"
	"github.com/andreujuanc/prr/internal/review"
)

// reviewPhaseTracker holds the model-side phase state for the in-progress
// review view. It is the only source of truth for "what phase is running,
// how far along, and what's it doing right now".
//
// The phase definitions and the per-row rendering come from
// internal/progress + internal/review — the same code that drives the
// headless `prr review` UI. The tracker is just a passive container,
// so the two views can't drift apart.
type reviewPhaseTracker struct {
	phases  []progress.PhaseInfo
	state   *progress.State
	startAt time.Time
	active  bool
}

// IsActive reports whether a review run is currently being tracked.
// renderReviewProgressView consults this to decide whether to render
// the phase list or fall through to its non-progress branch.
func (t *reviewPhaseTracker) IsActive() bool {
	return t != nil && t.active
}

// Start initialises (or re-initialises) the tracker. All phases begin
// in PhaseWaiting; the state is zeroed; the elapsed clock resets.
func (t *reviewPhaseTracker) Start(specs []progress.PhaseDef) {
	t.phases = make([]progress.PhaseInfo, len(specs))
	for i, s := range specs {
		t.phases[i] = progress.PhaseInfo{Def: s, Status: progress.PhaseWaiting}
	}
	t.state = &progress.State{Counters: make(map[string]int)}
	t.startAt = time.Now()
	t.active = true
}

// Activate marks the named phase active and rolls all earlier phases
// to done. Out-of-order activations (e.g. AOI arriving before the
// classify "done" signal) are tolerated: any phase preceding the new
// active one is implicitly marked done. Idempotent.
func (t *reviewPhaseTracker) Activate(name string) {
	if !t.active {
		return
	}
	idx := t.phaseIndex(name)
	if idx < 0 {
		return
	}
	for i := range t.phases {
		switch {
		case i < idx && t.phases[i].Status != progress.PhaseDone &&
			t.phases[i].Status != progress.PhaseError:
			t.phases[i].Status = progress.PhaseDone
		case i == idx:
			if t.phases[i].Status == progress.PhaseWaiting ||
				t.phases[i].Status == progress.PhaseActive {
				t.phases[i].Status = progress.PhaseActive
			}
		}
	}
}

// Complete marks the named phase done.
func (t *reviewPhaseTracker) Complete(name string) {
	if idx := t.phaseIndex(name); idx >= 0 {
		t.phases[idx].Status = progress.PhaseDone
		t.phases[idx].Detail = ""
	}
}

// Fail marks the named phase failed (terminal).
func (t *reviewPhaseTracker) Fail(name string) {
	if idx := t.phaseIndex(name); idx >= 0 {
		t.phases[idx].Status = progress.PhaseError
	}
}

// SetCounter sets a named counter used by the phase-row counter
// callback in the shared phase definition. Counter keys match what
// internal/review/runwithui.go's parseReviewEvent populates, so the
// "X/Y" annotations line up with what `prr review` shows.
func (t *reviewPhaseTracker) SetCounter(name string, value int) {
	if t.state == nil {
		t.state = &progress.State{Counters: make(map[string]int)}
	}
	t.state.Counters[name] = value
}

// IncCounter increments a named counter by one.
func (t *reviewPhaseTracker) IncCounter(name string) {
	if t.state == nil {
		t.state = &progress.State{Counters: make(map[string]int)}
	}
	t.state.Counters[name]++
}

// SetDetail sets the live detail string for the named phase. Detail is
// only displayed while the phase is active or done.
func (t *reviewPhaseTracker) SetDetail(name, text string) {
	if idx := t.phaseIndex(name); idx >= 0 {
		t.phases[idx].Detail = text
	}
}

// Reset clears all tracker state. Called at end-of-run so the next
// review starts from a clean slate.
func (t *reviewPhaseTracker) Reset() {
	t.phases = nil
	t.state = nil
	t.startAt = time.Time{}
	t.active = false
}

func (t *reviewPhaseTracker) phaseIndex(name string) int {
	for i, p := range t.phases {
		if p.Def.Name == name {
			return i
		}
	}
	return -1
}

// defaultReviewPhases is a thin wrapper around review.PRReviewPhases()
// so the bubbletea handlers don't need to import internal/review just
// for this one identifier.
func defaultReviewPhases() []progress.PhaseDef {
	return review.PRReviewPhases()
}

// renderReviewProgressView returns the bounded phase list shown in the
// review viewport while a run is in progress. Returns "" when no run
// is active so the caller can fall through to its non-progress branch.
//
// The phase rows themselves come from progress.RenderPhaseList — the
// same renderer the headless `prr review` UI uses, so the two views
// stay visually identical without parallel code.
//
// maxWidth is the viewport content width; detail strings are
// truncated to leave room for icon/step/label/counter.
func (m Model) renderReviewProgressView(maxWidth int) string {
	t := &m.reviewProgress
	if !t.IsActive() {
		return ""
	}

	var b strings.Builder

	// Header: title + quantized 1.0s-precision elapsed (smooth in the
	// way `prr review` is not — that one uses 100ms precision because
	// it's full-screen with no other moving parts; ours sits next to
	// the diff and benefits from the slower clock).
	elapsedSec := time.Since(t.startAt).Truncate(time.Second).Seconds()
	header := fmt.Sprintf("prr review — %.1fs elapsed", elapsedSec)
	b.WriteString(styleAccentBlueBold.Render(header))
	b.WriteString("\n\n")

	// Pick a detail-truncation budget that fits inside the AI pane;
	// 60 (the headless default) is too wide for narrow side panels.
	detailW := maxWidth - 24 // icon + step + label + 2× spacing ≈ 24
	if detailW < 12 {
		detailW = 12
	}
	b.WriteString(progress.RenderPhaseList(t.phases, t.state, m.spinner.View(), detailW))

	return b.String()
}

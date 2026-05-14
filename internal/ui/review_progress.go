package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// reviewPhaseStatus is the lifecycle of a phase row in the in-progress
// review view.
type reviewPhaseStatus int

const (
	phasePending reviewPhaseStatus = iota
	phaseActive
	phaseDone
	phaseFailed
)

// reviewPhase is a single row in the in-progress review view. It mirrors
// progress.PhaseDef + progress.phaseInfo so the TUI can render the same
// bounded list `prr review` shows headlessly without importing the
// internal/progress runtime.
type reviewPhase struct {
	Name   string // pipeline key: "fetch", "discovery", "classify", "aoi", "phase1", "recheck", "phase2"
	Label  string // display label, e.g. "Deep Review"
	Status reviewPhaseStatus
	Detail string // last-write-wins live detail for the active phase
}

// counterFn extracts (done, total) from a counter map for the inline
// "12/40" annotation next to a phase row. Returns (0, 0) when the
// counter is not applicable to the phase.
type counterFn func(counters map[string]int) (done, total int)

// phaseSpec ties a phase's render shape to its data sources.
type phaseSpec struct {
	Name    string
	Label   string
	Counter counterFn
}

// defaultReviewPhases is the canonical list rendered while a PR review
// runs. It mirrors review.reviewPhases() in internal/review/runwithui.go;
// the two lists must stay in sync.
//
// The pipeline emits seven distinct phases. The previous TUI code only
// surfaced three of them ("aoi", "batch", "synthesis") by collapsing
// discovery/classify/recheck/aoi-prescan into a single status stream.
// This list lets the bubbletea view render all seven independently.
func defaultReviewPhases() []phaseSpec {
	return []phaseSpec{
		{Name: "fetch", Label: "Fetch PR", Counter: counterFetch},
		{Name: "discovery", Label: "Discovery"},
		{Name: "classify", Label: "Classification", Counter: counterClassify},
		{Name: "aoi", Label: "AOI Pre-scan", Counter: counterAOI},
		{Name: "phase1", Label: "Deep Review", Counter: counterPhase1},
		{Name: "recheck", Label: "Recheck", Counter: counterRecheck},
		{Name: "phase2", Label: "Synthesis"},
	}
}

func counterFetch(c map[string]int) (int, int) {
	return c["fetch_files"], c["fetch_files"]
}

func counterClassify(c map[string]int) (int, int) {
	return c["classify_total"], c["classify_total"]
}

func counterAOI(c map[string]int) (int, int) {
	return c["aoi_scanned"], c["aoi_total"]
}

func counterPhase1(c map[string]int) (int, int) {
	return c["batches_done"], c["batches_total"]
}

func counterRecheck(c map[string]int) (int, int) {
	return c["recheck_done"], c["recheck_total"]
}

// reviewPhaseTracker is the model-side state for the in-progress review
// view. It is the only source of truth for "what phase is running, how
// far along, and what's it doing right now".
//
// Updates flow in from the Update() handlers for AIReviewInit/Phase/
// Progress/Synthesis messages; reads flow out to renderReviewProgressView.
type reviewPhaseTracker struct {
	phases   []reviewPhase
	counters map[string]int
	startAt  time.Time
	active   bool
}

// Start initialises (or re-initialises) the tracker with the given
// phase set. All phases start pending.
func (t *reviewPhaseTracker) Start(specs []phaseSpec) {
	t.phases = make([]reviewPhase, len(specs))
	for i, s := range specs {
		t.phases[i] = reviewPhase{Name: s.Name, Label: s.Label, Status: phasePending}
	}
	t.counters = make(map[string]int)
	t.startAt = time.Now()
	t.active = true
}

// IsActive reports whether a review run is currently being tracked.
// View() consults this to decide whether to render the phase list or
// fall through to the post-review renderer.
func (t *reviewPhaseTracker) IsActive() bool {
	return t != nil && t.active
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
		case i < idx && t.phases[i].Status == phasePending:
			t.phases[i].Status = phaseDone
		case i < idx && t.phases[i].Status == phaseActive:
			t.phases[i].Status = phaseDone
		case i == idx:
			if t.phases[i].Status == phasePending || t.phases[i].Status == phaseActive {
				t.phases[i].Status = phaseActive
			}
		}
	}
}

// Complete marks the named phase done.
func (t *reviewPhaseTracker) Complete(name string) {
	if idx := t.phaseIndex(name); idx >= 0 {
		t.phases[idx].Status = phaseDone
		t.phases[idx].Detail = ""
	}
}

// Fail marks the named phase failed (terminal).
func (t *reviewPhaseTracker) Fail(name string) {
	if idx := t.phaseIndex(name); idx >= 0 {
		t.phases[idx].Status = phaseFailed
	}
}

// SetCounter sets a named counter used by the phase-row counter
// callback. Missing counters render as no annotation.
func (t *reviewPhaseTracker) SetCounter(name string, value int) {
	if t.counters == nil {
		t.counters = make(map[string]int)
	}
	t.counters[name] = value
}

// IncCounter increments a named counter by one.
func (t *reviewPhaseTracker) IncCounter(name string) {
	if t.counters == nil {
		t.counters = make(map[string]int)
	}
	t.counters[name]++
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
	t.counters = nil
	t.startAt = time.Time{}
	t.active = false
}

func (t *reviewPhaseTracker) phaseIndex(name string) int {
	for i, p := range t.phases {
		if p.Name == name {
			return i
		}
	}
	return -1
}

// activeIndex returns the index of the first active phase, or -1.
func (t *reviewPhaseTracker) activeIndex() int {
	for i, p := range t.phases {
		if p.Status == phaseActive {
			return i
		}
	}
	return -1
}

// progressFraction returns the overall completion fraction in [0, 1].
// Used by renderReviewProgressView's progress bar.
func (t *reviewPhaseTracker) progressFraction() float64 {
	if len(t.phases) == 0 {
		return 0
	}
	done := 0
	for _, p := range t.phases {
		if p.Status == phaseDone || p.Status == phaseFailed {
			done++
		}
	}
	return float64(done) / float64(len(t.phases))
}

// renderReviewProgressView returns the bounded phase list shown in the
// review viewport while a run is in progress. Returns "" when no run
// is active so the caller can fall through to its non-progress branch.
//
// maxWidth is the viewport content width; rows are truncated to fit
// (the runtime Box renderer in renderPane pads/clips again as needed).
func (m Model) renderReviewProgressView(maxWidth int) string {
	t := &m.reviewProgress
	if !t.IsActive() {
		return ""
	}

	specs := defaultReviewPhases()
	specByName := make(map[string]phaseSpec, len(specs))
	for _, s := range specs {
		specByName[s.Name] = s
	}

	var b strings.Builder

	// Header: title + elapsed
	elapsed := time.Since(t.startAt).Truncate(100 * time.Millisecond)
	header := fmt.Sprintf("prr review — %s elapsed", elapsed)
	b.WriteString(styleAccentBlueBold.Render(header))
	b.WriteString("\n\n")

	// Phase rows
	total := len(t.phases)
	for i, p := range t.phases {
		icon := phaseIcon(p.Status, m.spinner.View())
		step := styleTextSubtle.Render(fmt.Sprintf("%d/%d", i+1, total))

		row := fmt.Sprintf("  %s %s ", icon, step)
		row += phaseLabelStyled(p)

		// Inline counter while active or done.
		if spec, ok := specByName[p.Name]; ok && spec.Counter != nil &&
			(p.Status == phaseActive || p.Status == phaseDone) {
			done, tot := spec.Counter(t.counters)
			if tot > 0 {
				row += styleTextSubtle.Render(fmt.Sprintf("  %d/%d", done, tot))
			}
		}

		// Detail string when active or done.
		if p.Detail != "" && (p.Status == phaseActive || p.Status == phaseDone) {
			detail := truncateForRow(p.Detail, maxWidth-ansi.StringWidth(row)-4)
			if detail != "" {
				row += styleTextSubtle.Render("  " + detail)
			}
		}

		b.WriteString(row)
		b.WriteString("\n")
	}

	// Active-phase progress bar (cosmetic at run level — phase-internal
	// progress is already shown via the counter).
	if t.activeIndex() >= 0 {
		b.WriteString("\n")
		b.WriteString("  " + renderProgressBar(t.progressFraction(), 20))
		b.WriteString("\n")
	}

	return b.String()
}

func phaseIcon(s reviewPhaseStatus, spinner string) string {
	switch s {
	case phaseDone:
		return styleAccentGreen.Render(checkMark)
	case phaseActive:
		return spinner
	case phaseFailed:
		return styleAccentRed.Render("✗")
	default:
		return styleTextSubtle.Render("○")
	}
}

func phaseLabelStyled(p reviewPhase) string {
	switch p.Status {
	case phaseActive:
		return styleTextPrimary.Bold(true).Render(p.Label)
	case phaseDone:
		return styleTextSecondary.Render(p.Label)
	case phaseFailed:
		return styleAccentRed.Render(p.Label)
	default:
		return styleTextSubtle.Render(p.Label)
	}
}

func truncateForRow(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= maxW {
		return s
	}
	if maxW <= 1 {
		return "…"
	}
	return ansi.Truncate(s, maxW-1, "") + "…"
}

func renderProgressBar(frac float64, width int) string {
	if width < 1 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled
	return styleProgressBar.Render(strings.Repeat("█", filled)) +
		styleProgressBg.Render(strings.Repeat("░", empty))
}

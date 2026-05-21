// Package progress provides a shared Bubble Tea progress UI for the
// `prr audit` and `prr review` commands.
//
// Both commands emit (phase, message) events as their pipelines execute.
// This package renders those events as a phase list with spinner /
// per-phase detail / active-phase progress bar / final summary box.
//
// # Design notes
//
// Mode-specific concerns (the phase list, the result type, how to render
// the summary, how to extract progress-bar counters from message strings)
// are passed in via Config so the same TUI serves both modes. Future UI
// improvements (token rate, ETA, sparklines) only need to be written here
// and both modes get them.
//
// The background task runs in a goroutine and emits events via the
// `emit` closure given to RunTask. emit forwards events through the
// Bubble Tea program's Send channel so all model mutation happens on
// the main thread — the goroutine never touches the model directly.
package progress

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Styles ──────────────────────────────────────────────────────────────

var (
	sTitle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	sSubtle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))
	sWarn      = lipgloss.NewStyle().Foreground(lipgloss.Color("#F9E2AF"))
	sPhaseOn   = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	sPhaseDone = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
	sPhaseWait = lipgloss.NewStyle().Foreground(lipgloss.Color("#7F849C"))
)

// ── Public API ──────────────────────────────────────────────────────────

// PhaseDef describes one phase shown in the TUI.
type PhaseDef struct {
	// Name is the event key the pipeline emits on (e.g. "phase2", "fetch").
	// Must be unique within a Config.Phases slice.
	Name string

	// Label is the human-readable label shown next to the spinner/check.
	Label string

	// ProgressFn computes the active-phase progress bar value (0..1)
	// from the current State. Called only while this phase is active.
	// Return 0 (or leave nil) to skip the progress bar for this phase.
	ProgressFn func(s *State) float64

	// Counter returns (done, total) for inline "X/Y" display next to
	// the phase label. Rendered while the phase is active *and* after
	// it completes (so users can see the final tally per phase).
	// Skipped when total <= 0. Optional.
	Counter func(s *State) (done, total int)

	// CounterUnit is the unit shown after the X/Y, e.g. "batches",
	// "findings", "files". Empty omits the unit (legacy behavior).
	// Surfacing this is what tells the user whether 0/3 means three
	// files, three batches, or three findings — the absence of a
	// unit reads as ambiguous noise.
	CounterUnit string

	// Summary returns a stable description of what this phase
	// accomplished. Rendered as the row's detail line when the phase
	// reaches `done` state, replacing the last-write-wins live detail.
	// Falls back to live detail when this returns "" or is nil.
	//
	// Use this to surface structured information that would otherwise
	// be lost as the live detail line flips between transient status
	// messages — e.g., "kept 11 · dismissed 4" for Recheck or
	// "35 done · 58 cached · 3 failed" for Deep Review.
	Summary func(s *State) string
}

// State exposes live counters and metadata to ProgressFn / ParseEvent
// closures. Counters is mode-defined — pick any string keys; the TUI
// doesn't interpret them.
type State struct {
	Counters map[string]int
	Elapsed  time.Duration

	// Batches is populated by ParseEvent for modes whose pipelines
	// emit per-batch lifecycle events (Batch K: init/active/stream/
	// done/cached/failed). The Batches panel renders these as one
	// row per active batch plus a recent-completions tail. Nil for
	// modes that don't emit those events.
	Batches map[int]*BatchState
}

// BatchStatus lifecycle for one batch row in the Batches panel.
type BatchStatus string

const (
	BatchQueued BatchStatus = "queued"
	BatchActive BatchStatus = "active"
	BatchDone   BatchStatus = "done"
	BatchCached BatchStatus = "cached"
	BatchFailed BatchStatus = "failed"
)

// BatchState is one row in the Batches panel.
type BatchState struct {
	// Index is the call's original 0-based index (1-based on the
	// wire as "Batch K"). Stable across the run.
	Index int
	// Label is the human-readable description: directory ("internal/ui")
	// for general batches, "category/subcategory" (with " [critical]"
	// suffix for individual calls) for AOI-driven ones.
	Label string
	// Files is the count rendered in the panel's middle column. The
	// semantic depends on Unit: "files" for deep-review batches,
	// "findings" for recheck batches. Naming is legacy — kept as
	// "Files" so existing snapshot tests don't churn.
	Files int
	// Unit is the noun shown after Files in the panel ("files",
	// "findings"). Empty defaults to "files" so deep-review wire
	// shape (kind=aoi-driven|general, no unit token) keeps working.
	Unit string
	// Kind is "aoi-driven" or "general". Drives the row's accent color.
	Kind string
	// Status is the lifecycle stage.
	Status BatchStatus
	// StartedAt is set when the batch transitions to active. Zero
	// while queued. Used to compute the elapsed timer on the row.
	StartedAt time.Time
	// EndedAt is set when the batch transitions to done/cached/failed.
	// Zero while queued or active.
	EndedAt time.Time
	// Bytes is the cumulative content-byte count received from the
	// LLM stream for this batch. Drives the per-row progress bar
	// when non-zero; the row falls back to an indeterminate spinner
	// when zero.
	Bytes int
	// Findings is the number of findings this batch produced. Set
	// from the optional `findings=N` token on terminal events
	// (done/cached). Rendered next to the elapsed time on finished
	// rows so the user can see what work each call delivered.
	Findings int
}

// Header is the title block shown at the top of the TUI.
type Header struct {
	Title    string // e.g. "  prr audit", "  prr review"
	Subtitle string // e.g. repo name, PR number
	Info     string // e.g. "review: model-x  aoi: model-y"
}

// Config configures a TUI run.
type Config struct {
	Header Header
	Phases []PhaseDef

	// RunTask is the background work. emit forwards (phase, message)
	// events into the model on the Bubble Tea main thread. The
	// returned error becomes the run's error (passed to Summary).
	RunTask func(emit func(phase, message string)) error

	// ParseEvent updates counters in s for a (phase, message) pair.
	// Optional. The TUI also recognizes the special phase "warning"
	// as a banner regardless of ParseEvent.
	ParseEvent func(s *State, phase, message string)

	// Summary renders the final box after the run completes.
	// Optional. err is the RunTask return value; elapsed is wall time.
	Summary func(err error, elapsed time.Duration) string

	// OnCancel is called when the user exits the TUI (Ctrl+C / q)
	// before the background task signals done. The task's goroutine
	// is still in flight at this point; the canonical use is for the
	// caller to cancel the context it passed to RunTask so the
	// in-flight LLM call returns quickly instead of orphaning. Optional.
	OnCancel func()

	// BatchPhases lists the phase Names whose active state should
	// trigger the Batches panel. The panel renders below the phase
	// list while one of these is the active phase. Empty disables
	// the panel — modes whose pipelines don't emit per-batch
	// lifecycle events leave this nil.
	BatchPhases []string
}

// ErrCancelled is returned from Run when the user exits the TUI before
// the background task signals done.
var ErrCancelled = errors.New("cancelled by user")

// Run executes the TUI to completion. The returned error is the task's
// error, not the TUI's — a TUI startup failure surfaces wrapped as
// "progress UI error: ...". User-cancelled (Ctrl+C before doneMsg
// arrived) returns ErrCancelled.
func Run(cfg Config) error {
	ui := newUI(cfg)
	p := tea.NewProgram(ui, tea.WithAltScreen())
	// Set send before p.Run() so the background goroutine never sees
	// a nil callback. p.Send is safe to call concurrently.
	ui.send = p.Send

	final, err := p.Run()
	if err != nil {
		return fmt.Errorf("progress UI error: %w", err)
	}
	m := final.(*model)
	if !m.done {
		// User exited the TUI before the task signaled done — the
		// background goroutine is still in flight. Notify the caller
		// so it can cancel the context driving the LLM call, and
		// surface a sentinel error so the caller doesn't try to use
		// a half-built result (which would nil-deref on result.PR.Title
		// or similar).
		if cfg.OnCancel != nil {
			cfg.OnCancel()
		}
		return ErrCancelled
	}
	return m.err
}

// ── Internal model ──────────────────────────────────────────────────────

type progressMsg struct {
	phase   string
	message string
}

type doneMsg struct {
	err error
}

type tickMsg time.Time

// PhaseStatus is the lifecycle of a phase row.
type PhaseStatus string

const (
	PhaseWaiting PhaseStatus = "waiting"
	PhaseActive  PhaseStatus = "active"
	PhaseDone    PhaseStatus = "done"
	PhaseError   PhaseStatus = "error"
)

// PhaseInfo pairs a phase definition with its current status and the
// last-write-wins detail string. The bubbletea program in this package
// owns one of these per phase, and external consumers (e.g. the in-app
// TUI) build their own slice for RenderPhaseList.
type PhaseInfo struct {
	Def    PhaseDef
	Status PhaseStatus
	Detail string
}

// phaseInfo is the internal alias used by the bubbletea model.
type phaseInfo = PhaseInfo

type model struct {
	cfg Config

	phases   []phaseInfo
	state    *State
	spinner  spinner.Model
	progress progress.Model
	startAt  time.Time
	warning  string

	done bool
	err  error

	// send dispatches events from RunTask's goroutine into the model
	// via the Bubble Tea program. Wired by Run before p.Run() starts.
	send func(tea.Msg)
}

func newUI(cfg Config) *model {
	sp := spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA"))),
	)
	// Gradient matches the rest of the TUI's palette: blue (active
	// accent, same as the spinner) → green (done). Reads as
	// "progressing toward complete" instead of the bubbles default
	// magenta→purple which fights with everything else on screen.
	//
	// WithGradient (not WithScaledGradient) anchors the colors to the
	// bar's full width so the transition is visible as the fill
	// grows: at 25% you see blue, at 75% the fill has crossed into
	// teal and green. The scaled variant squeezed the whole gradient
	// into the filled portion, so 5% and 95% looked the same colorwise.
	pb := progress.New(
		progress.WithGradient("#89B4FA", "#A6E3A1"),
		progress.WithWidth(40),
	)

	phases := make([]phaseInfo, len(cfg.Phases))
	for i, p := range cfg.Phases {
		phases[i] = phaseInfo{Def: p, Status: PhaseWaiting}
	}

	return &model{
		cfg:    cfg,
		phases: phases,
		state: &State{
			Counters: make(map[string]int),
			Batches:  make(map[int]*BatchState),
		},
		spinner:  sp,
		progress: pb,
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.runTask(),
		m.tickTimer(),
	)
}

func (m *model) tickTimer() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// runTask launches the user's RunTask in a goroutine and forwards
// the eventual error back through the Bubble Tea event loop.
func (m *model) runTask() tea.Cmd {
	return func() tea.Msg {
		emit := func(phase, message string) {
			if m.send != nil {
				m.send(progressMsg{phase: phase, message: message})
			}
		}
		var err error
		if m.cfg.RunTask != nil {
			err = m.cfg.RunTask(emit)
		}
		return doneMsg{err: err}
	}
}

// applyEvent records a (phase, message) into the model on the main
// goroutine. Sequencing: first non-waiting transition activates the
// phase and marks earlier active phases done; the message becomes the
// phase detail unless it's a silent parser-only ping.
func (m *model) applyEvent(phase, message string) {
	if phase == "warning" {
		m.warning = message
		return
	}

	silent := isSilentMessage(message)
	for i := range m.phases {
		if m.phases[i].Def.Name != phase {
			continue
		}
		if m.phases[i].Status == PhaseWaiting {
			m.phases[i].Status = PhaseActive
			for j := range i {
				if m.phases[j].Status == PhaseActive {
					m.phases[j].Status = PhaseDone
				}
			}
			// When a new BatchPhase activates, clear the Batches map
			// so the panel switches contexts. Without this, the
			// previous phase's batches stay around as a stale
			// recent-completions tail or active rows that no longer
			// reflect reality. Phases not in BatchPhases keep the map
			// untouched (their events would never populate it anyway).
			if m.cfg.isBatchPhase(phase) {
				m.state.Batches = make(map[int]*BatchState)
			}
		}
		if !silent {
			m.phases[i].Detail = message
		}
		break
	}

	if m.cfg.ParseEvent != nil {
		m.cfg.ParseEvent(m.state, phase, message)
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}

	case tickMsg:
		if !m.done {
			m.state.Elapsed = time.Since(m.startAt)
			return m, m.tickTimer()
		}

	case spinner.TickMsg:
		if !m.done {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case progressMsg:
		m.applyEvent(msg.phase, msg.message)
		return m, nil

	case doneMsg:
		m.done = true
		m.err = msg.err
		m.state.Elapsed = time.Since(m.startAt)
		for i := range m.phases {
			if m.phases[i].Status == PhaseActive {
				m.phases[i].Status = PhaseDone
			}
		}
		return m, tea.Quit
	}

	return m, nil
}

func (m *model) View() string {
	if m.startAt.IsZero() {
		m.startAt = time.Now()
	}

	var b strings.Builder
	b.WriteString("\n")

	// Header
	b.WriteString(sTitle.Render(m.cfg.Header.Title))
	if m.cfg.Header.Subtitle != "" {
		b.WriteString(sSubtle.Render("  " + m.cfg.Header.Subtitle))
	}
	b.WriteString(sSubtle.Render(fmt.Sprintf("  %s", m.state.Elapsed.Truncate(100*time.Millisecond))))
	b.WriteString("\n")

	if m.cfg.Header.Info != "" {
		b.WriteString(sSubtle.Render("  " + m.cfg.Header.Info))
		b.WriteString("\n")
	}

	if m.warning != "" {
		b.WriteString("\n")
		b.WriteString(sWarn.Render("  "+m.warning) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(RenderPhaseList(m.phases, m.state, m.spinner.View(), 60))

	// Active-phase progress bar
	if !m.done {
		if pct := m.activeProgress(); pct > 0 {
			b.WriteString("\n")
			b.WriteString("  " + m.progress.ViewAs(pct) + "\n")
		}
	}

	// Batches panel — rendered while one of the configured
	// BatchPhases is active. Animation tick comes from tickMsg so
	// the spinner-fallback bar advances even when no stream events
	// land between frames.
	if !m.done && m.cfg.BatchPanelActive(m.phases) {
		panel := RenderBatchesPanel(m.state, BatchPanelOptions{
			MaxActiveRows: 10,
			RecentTail:    10,
			Animation:     int(m.state.Elapsed / (100 * time.Millisecond)),
		})
		if panel != "" {
			b.WriteString("\n")
			b.WriteString(panel)
		}
	}

	// Summary
	if m.done {
		b.WriteString("\n")
		if m.err != nil {
			b.WriteString(sWarn.Render(fmt.Sprintf("  Error: %v", m.err)))
			b.WriteString("\n")
		} else if m.cfg.Summary != nil {
			b.WriteString(m.cfg.Summary(m.err, m.state.Elapsed))
		}
	}

	b.WriteString("\n")
	return b.String()
}

// activeProgress returns the progress fraction for whichever phase is
// currently active, or 0 if none has a ProgressFn.
func (m *model) activeProgress() float64 {
	for _, p := range m.phases {
		if p.Status != PhaseActive || p.Def.ProgressFn == nil {
			continue
		}
		return p.Def.ProgressFn(m.state)
	}
	return 0
}

// RenderPhaseList renders the phase rows (no header, no progress bar,
// no summary) as a pure string. Used both by the bubbletea program in
// this package and by external callers (the in-app TUI) that want the
// same rendered shape inside their own layout.
//
// activeSpinner is the glyph drawn for the active phase row; callers
// without a spinner can pass "" and the active row will use a plain
// "●". maxDetailWidth caps the truncation of each row's detail string;
// pass 0 to disable truncation.
func RenderPhaseList(phases []PhaseInfo, state *State, activeSpinner string, maxDetailWidth int) string {
	if activeSpinner == "" {
		activeSpinner = sPhaseOn.Render("●")
	}
	total := len(phases)
	var b strings.Builder
	for i, p := range phases {
		icon := phaseIcon(p, activeSpinner)
		label := phaseLabel(p)
		step := sSubtle.Render(fmt.Sprintf("%d/%d", i+1, total))

		b.WriteString(fmt.Sprintf("  %s %s %s", icon, step, label))

		if p.Def.Counter != nil && (p.Status == PhaseActive || p.Status == PhaseDone) {
			if done, tot := p.Def.Counter(state); tot > 0 {
				suffix := ""
				if p.Def.CounterUnit != "" {
					suffix = " " + p.Def.CounterUnit
				}
				b.WriteString(sSubtle.Render(fmt.Sprintf("  %d/%d%s", done, tot, suffix)))
			}
		}

		detail := p.Detail
		if p.Status == PhaseDone && p.Def.Summary != nil {
			if s := p.Def.Summary(state); s != "" {
				detail = s
			}
		}
		if detail != "" && (p.Status == PhaseActive || p.Status == PhaseDone) {
			b.WriteString(sSubtle.Render("  " + truncate(detail, maxDetailWidth)))
		}

		b.WriteString("\n")
	}
	return b.String()
}

func phaseIcon(p PhaseInfo, activeSpinner string) string {
	switch p.Status {
	case PhaseDone:
		return sPhaseDone.Render("✓")
	case PhaseActive:
		return activeSpinner
	case PhaseError:
		return sWarn.Render("✗")
	default:
		return sPhaseWait.Render("○")
	}
}

func phaseLabel(p PhaseInfo) string {
	switch p.Status {
	case PhaseDone:
		return sPhaseDone.Render(p.Def.Label)
	case PhaseActive:
		return sPhaseOn.Render(p.Def.Label)
	default:
		return sPhaseWait.Render(p.Def.Label)
	}
}

// isSilentMessage reports whether a (phase, message) emit is a
// parser-only counter ping that should NOT appear on the phase's
// detail line. These messages exist purely to feed ParseEvent
// counters; rendering them as detail produces noise like
// "synthesis estimate 6000" and a churning byte counter the user
// didn't ask to see.
//
// Per-batch lifecycle events that the Batches panel already
// renders (init / active / stream) are also silenced here so the
// Deep Review row doesn't flicker between "Batch 5: active" and
// "Batch 12: stream bytes=1024" while the panel below shows the
// real state. Terminal events (done / cached / failed) stay
// visible — they're useful as transient detail.
func isSilentMessage(message string) bool {
	if strings.HasPrefix(message, "synthesis estimate ") ||
		strings.HasPrefix(message, "synthesis received ") {
		return true
	}
	if !strings.HasPrefix(message, "Batch ") {
		return false
	}
	return strings.Contains(message, ": init ") ||
		strings.HasSuffix(message, ": active") ||
		strings.Contains(message, ": stream bytes=")
}

// truncate trims s to at most n runes, appending ellipsis when cut.
// Operates on runes so multi-byte characters aren't sliced mid-codepoint.
// n <= 0 disables truncation.
func truncate(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

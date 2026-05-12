// Package progress provides a shared Bubble Tea progress UI for the
// `prr audit` and `prr review` commands.
//
// Both commands emit (phase, message) events as their pipelines execute.
// This package renders those events as a phase list with spinner /
// per-phase detail / active-phase progress bar / final summary box.
//
// Design notes
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
}

// State exposes live counters and metadata to ProgressFn / ParseEvent
// closures. Counters is mode-defined — pick any string keys; the TUI
// doesn't interpret them.
type State struct {
	Counters map[string]int
	Elapsed  time.Duration
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

type phaseInfo struct {
	def    PhaseDef
	status string // "waiting", "active", "done", "error"
	detail string
}

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
	pb := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(40),
	)

	phases := make([]phaseInfo, len(cfg.Phases))
	for i, p := range cfg.Phases {
		phases[i] = phaseInfo{def: p, status: "waiting"}
	}

	return &model{
		cfg:      cfg,
		phases:   phases,
		state:    &State{Counters: make(map[string]int)},
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
// phase detail.
func (m *model) applyEvent(phase, message string) {
	if phase == "warning" {
		m.warning = message
		return
	}

	for i := range m.phases {
		if m.phases[i].def.Name != phase {
			continue
		}
		if m.phases[i].status == "waiting" {
			m.phases[i].status = "active"
			for j := 0; j < i; j++ {
				if m.phases[j].status == "active" {
					m.phases[j].status = "done"
				}
			}
		}
		m.phases[i].detail = message
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
			if m.phases[i].status == "active" {
				m.phases[i].status = "done"
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

	// Phase list
	total := len(m.phases)
	for i, p := range m.phases {
		icon := m.phaseIcon(p)
		label := m.phaseLabel(p)
		step := sSubtle.Render(fmt.Sprintf("%d/%d", i+1, total))

		b.WriteString(fmt.Sprintf("  %s %s %s", icon, step, label))

		// Inline counter ("12/40") when the phase exposes one and the
		// total is known. Shown while active and after done so users
		// see the final tally per phase.
		if p.def.Counter != nil && (p.status == "active" || p.status == "done") {
			if done, tot := p.def.Counter(m.state); tot > 0 {
				b.WriteString(sSubtle.Render(fmt.Sprintf("  %d/%d", done, tot)))
			}
		}

		if p.detail != "" && (p.status == "active" || p.status == "done") {
			b.WriteString(sSubtle.Render("  " + truncate(p.detail, 60)))
		}

		b.WriteString("\n")
	}

	// Active-phase progress bar
	if !m.done {
		if pct := m.activeProgress(); pct > 0 {
			b.WriteString("\n")
			b.WriteString("  " + m.progress.ViewAs(pct) + "\n")
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

func (m *model) phaseIcon(p phaseInfo) string {
	switch p.status {
	case "done":
		return sPhaseDone.Render("✓")
	case "active":
		return m.spinner.View()
	case "error":
		return sWarn.Render("✗")
	default:
		return sPhaseWait.Render("○")
	}
}

func (m *model) phaseLabel(p phaseInfo) string {
	switch p.status {
	case "done":
		return sPhaseDone.Render(p.def.Label)
	case "active":
		return sPhaseOn.Render(p.def.Label)
	default:
		return sPhaseWait.Render(p.def.Label)
	}
}

// activeProgress returns the progress fraction for whichever phase is
// currently active, or 0 if none has a ProgressFn.
func (m *model) activeProgress() float64 {
	for _, p := range m.phases {
		if p.status != "active" || p.def.ProgressFn == nil {
			continue
		}
		return p.def.ProgressFn(m.state)
	}
	return 0
}

// truncate trims s to at most n runes, appending ellipsis when cut.
// Operates on runes so multi-byte characters aren't sliced mid-codepoint.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

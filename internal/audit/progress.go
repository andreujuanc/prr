package audit

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andreujuanc/prr/internal/ai"
)

// ── Styles ──────────────────────────────────────────────────────────────

var (
	pTitle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	pSubtle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))
	pInfo      = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))
	pSuccess   = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
	pWarn      = lipgloss.NewStyle().Foreground(lipgloss.Color("#F9E2AF"))
	pPhaseOn   = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	pPhaseDone = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
	pPhaseWait = lipgloss.NewStyle().Foreground(lipgloss.Color("#7F849C"))
	pBox       = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#45475A")).
			Padding(1, 2)
)

// ── Messages ────────────────────────────────────────────────────────────

type progressMsg struct {
	phase   string
	message string
}

type phaseCompleteMsg struct {
	phase string
}

type auditDoneMsg struct {
	result    *Result
	synthesis *SynthesisResult
	err       error
}

type tickMsg time.Time

// ── Phase tracking ──────────────────────────────────────────────────────

type phaseInfo struct {
	name   string
	label  string
	status string // "waiting", "active", "done", "error"
	detail string
}

// ── Model ───────────────────────────────────────────────────────────────

// ProgressUI is a bubbletea model that displays audit progress.
type ProgressUI struct {
	// Config
	reviewModel string
	aoiModel    string

	// Audit execution
	ctx          context.Context
	reviewClient ai.Client
	aoiClient    ai.Client
	opts         Options

	// UI state
	phases   []phaseInfo
	spinner  spinner.Model
	progress progress.Model
	elapsed  time.Duration
	startAt  time.Time
	done     bool
	err      error

	// Result
	result    *Result
	synthesis *SynthesisResult

	// Config
	noSynthesis bool

	// Progress tracking
	totalFiles    int
	scannedFiles  int
	totalReviews  int
	doneReviews   int
	findingsCount int

	// Warning message (e.g. large file count)
	warning string
}

// NewProgressUI creates a new progress UI for an audit run.
func NewProgressUI(
	ctx context.Context,
	reviewClient ai.Client,
	aoiClient ai.Client,
	opts Options,
	reviewModel, aoiModel string,
) *ProgressUI {
	s := spinner.New(spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA"))))
	p := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(40),
		progress.WithoutPercentage(),
	)

	return &ProgressUI{
		ctx:          ctx,
		reviewClient: reviewClient,
		aoiClient:    aoiClient,
		opts:         opts,
		reviewModel:  reviewModel,
		aoiModel:     aoiModel,
		spinner:      s,
		progress:     p,
		phases: []phaseInfo{
			{name: "phase0", label: "Project Discovery", status: "waiting"},
			{name: "phase1", label: "File Collection", status: "waiting"},
			{name: "phase2", label: "AOI Pre-scan", status: "waiting"},
			{name: "phase3", label: "Deep Review", status: "waiting"},
			{name: "recheck", label: "Recheck", status: "waiting"},
			{name: "phase4", label: "Synthesis", status: "waiting"},
		},
	}
}

func (m *ProgressUI) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.runAudit(),
		m.tickTimer(),
	)
}

func (m *ProgressUI) tickTimer() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m *ProgressUI) runAudit() tea.Cmd {
	return func() tea.Msg {
		result, err := Run(m.ctx, m.reviewClient, m.aoiClient, m.opts, func(phase, message string) {
			m.updateProgress(phase, message)
		})
		if err != nil {
			return auditDoneMsg{result: result, err: err}
		}

		// Phase 4: Synthesis
		var synth *SynthesisResult
		if len(result.Findings) > 0 && !m.noSynthesis {
			m.updateProgress("phase4", "Synthesizing executive summary...")
			synth, err = Synthesize(m.ctx, m.reviewClient, result.Findings, result.CrossCuttingObservations, "", nil)
			if err != nil {
				m.updateProgress("phase4", "Synthesis failed: "+err.Error())
				// Non-fatal — continue without synthesis
				err = nil
			} else {
				m.updateProgress("phase4", "Synthesis complete")
			}
			// Track synthesis usage
			if ur, ok := m.reviewClient.(ai.UsageReporter); ok {
				result.Usage.Synth = ur.Usage()
				ur.ResetUsage()
			}
		}

		return auditDoneMsg{result: result, synthesis: synth, err: err}
	}
}

func (m *ProgressUI) updateProgress(phase, message string) {
	// Handle warning messages (not tied to a phase)
	if phase == "warning" {
		m.warning = message
		return
	}

	for i := range m.phases {
		if m.phases[i].name == phase {
			if m.phases[i].status == "waiting" {
				m.phases[i].status = "active"
				// Mark previous phases as done
				for j := 0; j < i; j++ {
					if m.phases[j].status == "active" {
						m.phases[j].status = "done"
					}
				}
			}
			m.phases[i].detail = message
		}
	}

	// Parse counters from progress messages
	switch {
	case phase == "phase1" && strings.Contains(message, "files to audit"):
		fmt.Sscanf(message, "Phase 1 complete: %d files to audit", &m.totalFiles)
	case phase == "phase2" && strings.Contains(message, "Scanning"):
		fmt.Sscanf(message, "Scanning %d files", &m.scannedFiles)
	case phase == "phase2" && strings.Contains(message, "complete"):
		// "AOI scan 3/5 complete"
		var done, total int
		if _, err := fmt.Sscanf(message, "AOI scan %d/%d complete", &done, &total); err == nil {
			m.scannedFiles = done
			m.totalFiles = total
		}
	case phase == "phase3" && strings.Contains(message, "Executing"):
		fmt.Sscanf(message, "Executing %d review calls...", &m.totalReviews)
	case phase == "phase3" && strings.Contains(message, "review") && strings.Contains(message, "/"):
		var done, total int
		if _, err := fmt.Sscanf(message, "review %d/%d", &done, &total); err == nil {
			m.doneReviews = done
			m.totalReviews = total
		}
	}
}

func (m *ProgressUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}

	case tickMsg:
		if !m.done {
			m.elapsed = time.Since(m.startAt)
			return m, m.tickTimer()
		}

	case spinner.TickMsg:
		if !m.done {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case auditDoneMsg:
		m.done = true
		m.result = msg.result
		m.synthesis = msg.synthesis
		m.err = msg.err
		m.elapsed = time.Since(m.startAt)

		// Mark all phases as done
		for i := range m.phases {
			if m.phases[i].status == "active" {
				m.phases[i].status = "done"
			}
		}

		return m, tea.Quit
	}

	return m, nil
}

func (m *ProgressUI) View() string {
	if m.startAt.IsZero() {
		m.startAt = time.Now()
	}

	var b strings.Builder

	// Header
	b.WriteString("\n")
	b.WriteString(pTitle.Render("  prr audit"))
	repoName := filepath.Base(m.opts.RepoRoot)
	if repoName != "" && repoName != "." {
		b.WriteString(pSubtle.Render("  " + repoName))
	}
	b.WriteString(pSubtle.Render(fmt.Sprintf("  %s", m.elapsed.Truncate(100*time.Millisecond))))
	b.WriteString("\n")

	// Model info
	b.WriteString(pSubtle.Render(fmt.Sprintf("  review: %s  aoi: %s", m.reviewModel, m.aoiModel)))
	b.WriteString("\n")

	// Warning banner
	if m.warning != "" {
		b.WriteString("\n")
		b.WriteString(pWarn.Render("  "+m.warning) + "\n")
	}

	b.WriteString("\n")

	// Phase list
	for i, phase := range m.phases {
		icon := m.phaseIcon(phase)
		label := m.phaseLabel(phase)
		step := pSubtle.Render(fmt.Sprintf("%d/5", i+1))

		b.WriteString(fmt.Sprintf("  %s %s %s", icon, step, label))

		if phase.detail != "" && (phase.status == "active" || phase.status == "done") {
			b.WriteString(pSubtle.Render("  " + truncate(phase.detail, 60)))
		}

		b.WriteString("\n")
	}

	// Progress bar for active phase
	if !m.done {
		b.WriteString("\n")
		pct := m.activeProgress()
		if pct > 0 {
			b.WriteString("  " + m.progress.ViewAs(pct) + "\n")
		}
	}

	// Summary when done
	if m.done {
		b.WriteString("\n")
		if m.err != nil {
			b.WriteString(pWarn.Render(fmt.Sprintf("  Error: %v", m.err)))
			b.WriteString("\n")
		} else if m.result != nil {
			b.WriteString(m.renderSummary())
		}
	}

	b.WriteString("\n")
	return b.String()
}

func (m *ProgressUI) phaseIcon(p phaseInfo) string {
	switch p.status {
	case "done":
		return pPhaseDone.Render("✓")
	case "active":
		return m.spinner.View()
	case "error":
		return pWarn.Render("✗")
	default:
		return pPhaseWait.Render("○")
	}
}

func (m *ProgressUI) phaseLabel(p phaseInfo) string {
	switch p.status {
	case "done":
		return pPhaseDone.Render(p.label)
	case "active":
		return pPhaseOn.Render(p.label)
	default:
		return pPhaseWait.Render(p.label)
	}
}

func (m *ProgressUI) activeProgress() float64 {
	for _, p := range m.phases {
		if p.status != "active" {
			continue
		}
		switch p.name {
		case "phase2":
			if m.totalFiles > 0 {
				return float64(m.scannedFiles) / float64(m.totalFiles)
			}
		case "phase3":
			if m.totalReviews > 0 {
				return float64(m.doneReviews) / float64(m.totalReviews)
			}
		}
	}
	return 0
}

func (m *ProgressUI) renderSummary() string {
	r := m.result
	var b strings.Builder

	b.WriteString(pTitle.Render("  Results"))
	b.WriteString("\n\n")

	b.WriteString(fmt.Sprintf("  Files      %s\n", pInfo.Render(fmt.Sprintf("%d", r.FilesScanned))))
	b.WriteString(fmt.Sprintf("  AOIs       %s\n", pInfo.Render(fmt.Sprintf("%d", r.AOIsGenerated))))
	b.WriteString(fmt.Sprintf("  Reviews    %s\n", pInfo.Render(fmt.Sprintf("%d (%d individual, %d grouped)", r.ReviewCalls, r.IndividualReviews, r.GroupedReviews))))
	b.WriteString(fmt.Sprintf("  Findings   %s\n", pInfo.Render(fmt.Sprintf("%d", len(r.Findings)))))
	b.WriteString(fmt.Sprintf("  Dismissed  %s\n", pInfo.Render(fmt.Sprintf("%d", r.Dismissals))))
	b.WriteString(fmt.Sprintf("  Time       %s\n", pInfo.Render(m.elapsed.Truncate(time.Second).String())))

	return b.String()
}

// Results returns the audit result and synthesis after the UI finishes.
func (m *ProgressUI) Results() (*Result, *SynthesisResult, error) {
	return m.result, m.synthesis, m.err
}

// RunWithUI executes the audit with the interactive progress UI.
// Returns the audit result and synthesis after completion.
func RunWithUI(
	ctx context.Context,
	reviewClient ai.Client,
	aoiClient ai.Client,
	opts Options,
	reviewModel, aoiModel string,
	noSynthesis bool,
) (*Result, *SynthesisResult, error) {
	ui := NewProgressUI(ctx, reviewClient, aoiClient, opts, reviewModel, aoiModel)
	ui.noSynthesis = noSynthesis
	p := tea.NewProgram(ui, tea.WithAltScreen())

	finalModel, err := p.Run()
	if err != nil {
		return nil, nil, fmt.Errorf("progress UI error: %w", err)
	}

	final := finalModel.(*ProgressUI)
	return final.Results()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

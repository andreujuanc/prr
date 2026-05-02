package ui

import (
	"context"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/state"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ── Pane focus ──────────────────────────────────────────────────────────

type Pane int

const (
	PaneFileList Pane = iota
	PaneDiff
	PaneChat
)

// ── Async messages ──────────────────────────────────────────────────────

// PRFetchedMsg is sent when PR metadata has been loaded from gh CLI.
type PRFetchedMsg struct {
	PR  *git.PullRequest
	Err error
}

// RefsFetchedMsg is sent when git refs have been fetched.
type RefsFetchedMsg struct {
	Err error
}

// DiffHashedMsg is sent when all diff hashes have been computed and state synced.
type DiffHashedMsg struct {
	State        *state.State
	RawDiffs     map[string]string         // filePath -> raw diff content
	SkippedFiles map[string]git.SkipReason // filePath -> reason skipped from AI
	Err          error
}

// StyledDiffMsg is sent when a styled diff for a file is ready.
type StyledDiffMsg struct {
	FilePath string
	Content  string
	Err      error
	Reload   bool // true when reloading same file (preserve scroll position)
}

// AIChatDeltaMsg carries a streamed token chunk from the AI.
type AIChatDeltaMsg struct {
	Token string
}

// AIChatDoneMsg signals the AI response is complete.
type AIChatDoneMsg struct {
	FullResponse string
	Err          error
	// Review data — set by multi-pass review and file review for persistence
	Review *state.AIReview
	// StructuredReview — set when the review was parsed as structured JSON
	StructuredReview *state.ReviewOutput
	// FileFindings maps file paths to their batch findings for caching.
	// Set by multi-pass review so individual file findings can be persisted.
	FileFindings map[string]string
}

// aiStreamTickMsg triggers a batched render of accumulated AI tokens.
type aiStreamTickMsg struct{}

// previewTickMsg fires after a debounce delay to trigger file preview.
// seq is compared with m.previewSeq to discard stale ticks.
type previewTickMsg struct{ seq int }

// AIReviewProgressMsg tracks multi-pass review progress.
// AIReviewBatchInfo describes a single batch for the in-place progress list.
type AIReviewBatchInfo struct {
	Label    string // e.g. "internal/ui"
	NumFiles int    // number of files in this batch
}

// AIReviewBatchStatus tracks the state of a batch during review.
type AIReviewBatchStatus int

const (
	BatchPending AIReviewBatchStatus = iota
	BatchActive                      // spinner
	BatchDone                        // green ✓
	BatchCached                      // green ✓ (cached)
	BatchFailed                      // red ✗
)

// AIReviewInitMsg is sent once at the start of a review to initialize the batch list.
type AIReviewInitMsg struct {
	Batches []AIReviewBatchInfo
}

// AIReviewProgressMsg updates the status of a single batch.
type AIReviewProgressMsg struct {
	Batch  int                 // batch index (0-based)
	Status AIReviewBatchStatus // new status
}

// AIReviewSynthesisMsg signals the transition to synthesis phase.
type AIReviewSynthesisMsg struct{}

// CommentsFetchedMsg is sent when PR review comments have been loaded.
type CommentsFetchedMsg struct {
	Comments []git.ReviewComment
	Err      error
}

// CommentCreatedMsg is sent when a new comment has been posted.
type CommentCreatedMsg struct {
	Comment *git.ReviewComment
	Err     error
}

// BlameFetchedMsg is sent when git blame data for a file is ready.
type BlameFetchedMsg struct {
	FilePath string
	Blame    map[int]git.BlameLine
	Err      error
}

// ChatRenderedMsg is sent when chat/review markdown rendering completes.
type ChatRenderedMsg struct {
	FilePath string // which file this render is for ("" = overview)
	Content  string // fully rendered content
	Tab      int    // 0 = review, 1 = chat
}

// ReviewRenderedMsg is sent when a structured review render completes,
// carrying both the rendered content and the ordered findings list.
type ReviewRenderedMsg struct {
	Content  string                // rendered review text
	Findings []state.ReviewFinding // ordered findings for navigation
}

// PRListFetchedMsg is sent when the list of open PRs has been fetched.
type PRListFetchedMsg struct {
	PRs []git.PRListItem
	Err error
}

// ActionsFetchedMsg is sent when GitHub Actions workflow runs have been loaded.
type ActionsFetchedMsg struct {
	Runs []git.WorkflowRun
	Err  error
}

// ActionsJobsFetchedMsg is sent when jobs for a workflow run have been loaded.
type ActionsJobsFetchedMsg struct {
	RunID int
	Jobs  []git.WorkflowJob
	Err   error
}

// actionsTickMsg triggers a re-fetch of workflow runs for polling.
type actionsTickMsg struct{}

// logoTickMsg drives the logo color animation during loading.
type logoTickMsg struct{}

// logo is the ASCII art displayed on the loading screen.
var logoLines = [2]string{
	"█▀█ █▀█ █▀█",
	"█▀▀ █▀▄ █▀▄",
}

// ── Model ───────────────────────────────────────────────────────────────

// viewMode tracks what the diff viewport is currently displaying.
type viewMode int

const (
	viewModeOverview viewMode = iota // PR overview (default)
	viewModeFile                     // file diff
	viewModeActions                  // GitHub Actions status
)

type Model struct {
	fileTree     fileTree
	diffViewport viewport.Model
	chatViewport viewport.Model
	chatInput    textarea.Model
	spinner      spinner.Model

	focusedPane Pane
	prNumber    string
	width       int
	height      int
	ready       bool

	// Real data
	pr           *git.PullRequest
	reviewState  *state.State
	loading      bool
	loadingMsg   string
	selectedFile string            // currently selected file path (meaningful only when viewMode == viewModeFile)
	viewMode     viewMode          // what the diff pane is currently showing
	rawDiffs     map[string]string // filePath -> raw diff (for AI context)

	// Refresh tracking: when non-empty, PRFetchedMsg compares OIDs to skip no-op refreshes
	refreshOldOid string

	// Diff context
	contextLines int // number of context lines for git diff (-U<n>)

	// Files skipped from AI review (binary, generated, large)
	skippedFiles map[string]git.SkipReason

	// AI
	aiClient            ai.Client
	aiModelName         string // model identifier for display (e.g. "gemini-2.5-pro")
	aiStreaming         bool   // true while AI is generating
	aiStreamBuffer      string // accumulated streamed response
	aiStreamDirty       bool   // true when buffer has unflushed tokens
	aiCancelFn          context.CancelFunc
	aiChatHistoryCache  string                // pre-rendered markdown of completed messages (for streaming perf)
	aiReviewBatches     []AIReviewBatchInfo   // batch list for in-place rendering
	aiReviewStatuses    []AIReviewBatchStatus // per-batch status
	aiReviewPhase       string                // "batch" or "synthesis"
	aiPanelTab          int                   // 0 = Review, 1 = Chat
	aiReviewRendered    string                // cached rendered review markdown
	aiReviewRenderWidth int                   // width used for cached render

	// Custom review instructions loaded from .prr/instructions.md
	customInstructions string

	// Parallel review concurrency
	parallelReviews int

	// Debounce file preview (avoids expensive renders during rapid j/k navigation)
	previewSeq int // incremented on each cursor move; debounced tick checks this

	// Comments
	comments     map[string][]git.ReviewComment // filePath -> comments
	commenting   bool                           // true when comment input is active
	commentInput textarea.Model                 // input for new comment
	commentLine  int                            // line number for new comment
	commentSide  string                         // "LEFT" or "RIGHT"
	diffCursor   int                            // cursor position within visible diff lines (for line selection)

	// Navigable review findings
	reviewFindings    []state.ReviewFinding // flat ordered list of findings (severity-sorted, matching render order)
	reviewCursor      int                   // currently highlighted finding index (-1 = none)
	pendingScrollLine int                   // line to scroll to after diff loads (0 = none)
	cameFromFinding   bool                  // true when diff was opened via finding jump (Esc returns to review)
	diffContent       string                // cached diff content for line scanning (set on StyledDiffMsg)

	// Panel visibility
	showFilePanel bool
	showAIPanel   bool

	// Modal overlays
	showHelp           bool   // help modal visible
	showModelPicker    bool   // model picker visible
	modelPickerCursor  int    // selected index in model picker
	showSubmitReview   bool   // submit review confirmation visible
	submitReviewCursor int    // 0 = Submit, 1 = Cancel
	showThemePicker    bool   // theme picker visible
	themePickerCursor  int    // selected index in theme picker
	themeBeforePicker  string // theme ID before opening picker (for revert on Esc)
	errorMsg           string // transient error shown as modal overlay

	// PR picker modal (shown when no PR number given on startup)
	showPRPicker    bool             // PR picker visible
	prPickerItems   []git.PRListItem // fetched open PRs
	prPickerCursor  int              // selected index
	prPickerLoading bool             // true while fetching PR list
	prPickerError   string           // error message if fetch failed

	// Loading animation
	logoFrame int // color animation frame counter

	// Experimental: use chroma for syntax highlighting instead of delta
	useChroma bool

	// Git blame toggle
	blameEnabled bool                             // whether blame overlay is active
	blameCache   map[string]map[int]git.BlameLine // filePath -> lineNum -> blame info

	// GitHub Actions
	actionsRuns     []git.WorkflowRun          // workflow runs for the PR
	actionsLoading  bool                       // true while fetching runs
	actionsPolling  bool                       // true when auto-polling active runs
	actionsExpanded map[int][]git.WorkflowJob  // runID -> expanded jobs (nil = collapsed)
	actionsCursor   int                        // cursor position in actions view
}

// ── Constructor ─────────────────────────────────────────────────────────

func NewModel(prNumber string, aiClient ai.Client, parallelReviews int, useChroma bool) Model {
	diffVp := viewport.New(0, 0)
	diffVp.Style = lipgloss.NewStyle().Foreground(textPrimary)

	chatVp := viewport.New(0, 0)

	ta := textarea.New()
	ta.Placeholder = "Ask about this code..."
	ta.Prompt = ""
	ta.CharLimit = 500
	ta.SetWidth(30)
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.FocusedStyle.Base = lipgloss.NewStyle().Background(surfaceBg)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(surfaceBg)
	ta.FocusedStyle.Prompt = lipgloss.NewStyle().Background(surfaceBg)
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(textMuted).Background(surfaceBg)
	ta.FocusedStyle.Text = lipgloss.NewStyle().Foreground(textPrimary).Background(surfaceBg)
	ta.BlurredStyle.Base = lipgloss.NewStyle().Background(lipgloss.Color("#252535"))
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.Color("#252535"))
	ta.BlurredStyle.Prompt = lipgloss.NewStyle().Background(lipgloss.Color("#252535"))
	ta.BlurredStyle.Placeholder = lipgloss.NewStyle().Foreground(textSubtle).Background(lipgloss.Color("#252535"))
	ta.BlurredStyle.Text = lipgloss.NewStyle().Foreground(textSecondary).Background(lipgloss.Color("#252535"))
	ta.Blur()

	commentTa := textarea.New()
	commentTa.Placeholder = "Write a comment..."
	commentTa.Prompt = "› "
	commentTa.CharLimit = 2000
	commentTa.SetWidth(40)
	commentTa.SetHeight(3)
	commentTa.ShowLineNumbers = false
	commentTa.FocusedStyle.Base = lipgloss.NewStyle().Background(surfaceBg)
	commentTa.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(surfaceBg)
	commentTa.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(accentYellow).Background(surfaceBg).Bold(true)
	commentTa.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(textMuted).Background(surfaceBg)
	commentTa.FocusedStyle.Text = lipgloss.NewStyle().Foreground(textPrimary).Background(surfaceBg)
	commentTa.Blur()

	s := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(accentBlue)),
	)

	// Extract model name from AI client if it supports ModelInfo
	var modelName string
	if mi, ok := aiClient.(ai.ModelInfo); ok {
		modelName = mi.ModelName()
	}

	m := Model{
		fileTree:           newFileTree(nil),
		diffViewport:       diffVp,
		chatViewport:       chatVp,
		chatInput:          ta,
		spinner:            s,
		focusedPane:        PaneFileList,
		prNumber:           prNumber,
		loading:            true,
		loadingMsg:         "Fetching PR data...",
		aiClient:           aiClient,
		aiModelName:        modelName,
		contextLines:       3,
		comments:           make(map[string][]git.ReviewComment),
		commentInput:       commentTa,
		showFilePanel:      true,
		showAIPanel:        true,
		customInstructions: config.LoadCustomInstructions(),
		parallelReviews:    parallelReviews,
		reviewCursor:       -1,
		useChroma:          useChroma,
		blameCache:         make(map[string]map[int]git.BlameLine),
	}

	// PR picker mode: no PR number given — show modal instead of loading
	if prNumber == "" {
		m.loading = false
		m.loadingMsg = ""
		m.showPRPicker = true
		m.prPickerLoading = true
	}

	return m
}

func (m Model) Init() tea.Cmd {
	if m.prNumber == "" {
		// PR picker mode — fetch list of open PRs
		return tea.Batch(m.spinner.Tick, fetchPRList())
	}
	return tea.Batch(textarea.Blink, m.spinner.Tick, fetchPR(m.prNumber), tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return logoTickMsg{}
	}))
}

// ── Async commands ──────────────────────────────────────────────────────

func fetchPR(prNumber string) tea.Cmd {
	return func() tea.Msg {
		pr, err := git.FetchPR(prNumber)
		return PRFetchedMsg{PR: pr, Err: err}
	}
}

func fetchPRList() tea.Cmd {
	return func() tea.Msg {
		prs, err := git.ListPRs()
		return PRListFetchedMsg{PRs: prs, Err: err}
	}
}

func fetchRefs(base, head, headRefOid string) tea.Cmd {
	return func() tea.Msg {
		err := git.FetchRefs(base, head, headRefOid)
		return RefsFetchedMsg{Err: err}
	}
}

func computeHashes(prNumber, base, head string, files []git.PRFile) tea.Cmd {
	return func() tea.Msg {
		// Load existing state
		st, err := state.Load(prNumber)
		if err != nil {
			return DiffHashedMsg{Err: fmt.Errorf("failed to load state: %w", err)}
		}

		// Compute diff hashes for all files and store raw diffs
		hashes := make(map[string]string, len(files))
		rawDiffs := make(map[string]string, len(files))
		skippedFiles := make(map[string]git.SkipReason)
		prFiles := make(map[string]bool, len(files))
		for _, f := range files {
			prFiles[f.Path] = true
			rawDiff, err := git.GetRawDiff(base, head, f.Path)
			if err != nil {
				log.Printf("Warning: failed to get raw diff for %s: %v", f.Path, err)
				continue
			}
			hashes[f.Path] = git.HashDiff(rawDiff)

			// Filter out binary/generated/large files from AI review
			if skip, reason := git.ShouldSkipForAI(f.Path, rawDiff); skip {
				skippedFiles[f.Path] = reason
				log.Printf("Skipping %s from AI review: %s", f.Path, reason)
				continue
			}
			rawDiffs[f.Path] = rawDiff
		}

		// Sync state with current diffs
		st.SyncWithDiffs(hashes, prFiles)

		// Save updated state
		if err := state.Save(st); err != nil {
			log.Printf("Warning: failed to save state: %v", err)
		}

		return DiffHashedMsg{State: st, RawDiffs: rawDiffs, SkippedFiles: skippedFiles, Err: nil}
	}
}

func fetchStyledDiff(base, head, filePath string, contextLines int, reload bool, useChroma bool, width int) tea.Cmd {
	// Snapshot the theme now so the goroutine uses a consistent copy,
	// avoiding a data race if the theme is changed mid-render.
	theme := DiffThemeFromCurrent()
	return func() tea.Msg {
		var content string
		var err error
		if useChroma {
			content, err = git.GetChromaDiffWithContext(base, head, filePath, contextLines, theme, width)
		} else {
			content, err = git.GetStyledDiffWithContext(base, head, filePath, contextLines, theme)
		}
		return StyledDiffMsg{FilePath: filePath, Content: content, Err: err, Reload: reload}
	}
}

func fetchComments(prNumber string) tea.Cmd {
	return func() tea.Msg {
		comments, err := git.FetchReviewComments(prNumber)
		return CommentsFetchedMsg{Comments: comments, Err: err}
	}
}

func (m *Model) fetchBlame(filePath string) tea.Cmd {
	ref := "origin/" + m.pr.HeadRefName
	return func() tea.Msg {
		blame, err := git.BlameFile(context.Background(), ref, filePath)
		return BlameFetchedMsg{FilePath: filePath, Blame: blame, Err: err}
	}
}

func createComment(prNumber, commitSHA, path, body string, line int, side string) tea.Cmd {
	return func() tea.Msg {
		comment, err := git.CreateReviewComment(prNumber, commitSHA, path, body, line, side)
		return CommentCreatedMsg{Comment: comment, Err: err}
	}
}

// ── GitHub Actions commands ─────────────────────────────────────────────

func fetchActions(headSHA string) tea.Cmd {
	return func() tea.Msg {
		runs, err := git.FetchWorkflowRuns(headSHA)
		return ActionsFetchedMsg{Runs: runs, Err: err}
	}
}

func fetchActionsJobs(runID int) tea.Cmd {
	return func() tea.Msg {
		jobs, err := git.FetchWorkflowJobs(runID)
		return ActionsJobsFetchedMsg{RunID: runID, Jobs: jobs, Err: err}
	}
}

const actionsPollInterval = 15 * time.Second

func pollActionsTick() tea.Cmd {
	return tea.Tick(actionsPollInterval, func(_ time.Time) tea.Msg {
		return actionsTickMsg{}
	})
}

// streamAIChat sends the conversation to the AI and streams tokens back via tea.Msg.
func streamAIChat(client ai.Client, ctx context.Context, systemPrompt string, messages []ai.Message, p *tea.Program) tea.Cmd {
	return func() tea.Msg {
		fullResponse, err := client.ChatStream(ctx, systemPrompt, messages, func(token string) {
			p.Send(AIChatDeltaMsg{Token: token})
		})
		return AIChatDoneMsg{FullResponse: fullResponse, Err: err}
	}
}

// ── Update ──────────────────────────────────────────────────────────────

// SetProgram stores the tea.Program reference needed for streaming.
// Must be called after tea.NewProgram but before Run.
var program *tea.Program

func SetProgram(p *tea.Program) {
	program = p
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case spinner.TickMsg:
		// Only tick spinner when something is loading/streaming
		if m.loading || m.aiStreaming || m.prPickerLoading {
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case logoTickMsg:
		if m.loading {
			m.logoFrame++
			return m, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
				return logoTickMsg{}
			})
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.syncLayout()
		return m, nil

	case PRFetchedMsg:
		if msg.Err != nil {
			m.loading = false
			m.loadingMsg = ""
			m.refreshOldOid = ""
			m.setDiffContent(
				styleAccentRed.Render(
					fmt.Sprintf("Error fetching PR: %v", msg.Err)))
			return m, nil
		}

		// If this is a refresh, check whether the PR actually changed
		if m.refreshOldOid != "" {
			if msg.PR.HeadRefOid == m.refreshOldOid {
				m.loading = false
				m.loadingMsg = ""
				m.refreshOldOid = ""
				log.Printf("Refresh: PR is up to date (head=%s)", msg.PR.HeadRefOid[:8])
				// Brief flash message via diff title — just update PR metadata
				m.pr = msg.PR
				m.populateFileList(m.reviewState)
				return m, nil
			}
			log.Printf("Refresh: PR has new commits (old=%s new=%s)",
				m.refreshOldOid[:8], msg.PR.HeadRefOid[:8])
			m.refreshOldOid = ""
			m.actionsResetPolling()
		}

		m.pr = msg.PR
		// Configure AI tools with the PR head and base refs
		if tc, ok := m.aiClient.(ai.ToolConfigurer); ok {
			tc.SetHeadRef(fmt.Sprintf("origin/%s", m.pr.HeadRefName))
			tc.SetBaseRef(fmt.Sprintf("origin/%s", m.pr.BaseRefName))
		}
		m.loadingMsg = "Fetching git refs..."
		return m, fetchRefs(m.pr.BaseRefName, m.pr.HeadRefName, m.pr.HeadRefOid)

	case PRListFetchedMsg:
		m.prPickerLoading = false
		if msg.Err != nil {
			m.prPickerError = msg.Err.Error()
		} else if len(msg.PRs) == 0 {
			m.prPickerError = "No open pull requests found"
		} else {
			m.prPickerItems = msg.PRs
		}
		return m, nil

	case RefsFetchedMsg:
		if msg.Err != nil {
			m.loading = false
			m.loadingMsg = ""
			m.setDiffContent(
				styleAccentRed.Render(
					fmt.Sprintf("Error fetching refs: %v", msg.Err)))
			m.populateFileList(nil)
			return m, nil
		}
		m.loadingMsg = "Computing diff hashes..."
		return m, computeHashes(m.prNumber, m.pr.BaseRefName, m.pr.HeadRefName, m.pr.Files)

	case DiffHashedMsg:
		m.loading = false
		m.loadingMsg = ""
		if msg.Err != nil {
			m.setDiffContent(
				styleAccentRed.Render(
					fmt.Sprintf("Error syncing state: %v", msg.Err)))
			m.populateFileList(nil)
			return m, nil
		}
		m.reviewState = msg.State
		m.rawDiffs = msg.RawDiffs
		m.skippedFiles = msg.SkippedFiles
		// Clear blame cache — diffs may have changed
		m.blameCache = make(map[string]map[int]git.BlameLine)
		// Provide diffs to AI tool executor for the git_diff tool
		if tc, ok := m.aiClient.(ai.ToolConfigurer); ok {
			tc.SetRawDiffs(msg.RawDiffs)
			tc.SetReviewGetter(func() string {
				if m.reviewState != nil && m.reviewState.Review != nil {
					return m.reviewState.Review.Summary
				}
				return ""
			})
		}
		m.populateFileList(m.reviewState)
		m.selectedFile = ""
		m.viewMode = viewModeOverview
		m.setDiffContent(m.renderOverview())
		m.diffViewport.GotoTop()
		chatCmd := m.renderActiveAIView()
		var actionsCmd tea.Cmd
		if m.pr != nil && m.pr.HeadRefOid != "" {
			m.actionsResetPolling()
			m.actionsLoading = true
			actionsCmd = fetchActions(m.pr.HeadRefOid)
		}
		return m, tea.Batch(fetchComments(m.prNumber), chatCmd, actionsCmd)

	case CommentsFetchedMsg:
		if msg.Err != nil {
			log.Printf("Warning: failed to fetch comments: %v", msg.Err)
		} else {
			// Group comments by file path
			m.comments = make(map[string][]git.ReviewComment)
			for _, c := range msg.Comments {
				m.comments[c.Path] = append(m.comments[c.Path], c)
			}
		}
		return m, nil

	case ActionsFetchedMsg:
		m.actionsLoading = false
		if msg.Err != nil {
			log.Printf("Warning: failed to fetch actions: %v", msg.Err)
			// On error, continue polling if we were polling (retry on next tick)
			if m.actionsPolling {
				return m, pollActionsTick()
			}
			return m, nil
		}
		m.actionsRuns = msg.Runs
		m.fileTree.actionStatus = git.AggregateActionStatus(msg.Runs)
		// Clamp cursor to new bounds
		total := m.actionsRowCount()
		if total == 0 {
			m.actionsCursor = 0
		} else if m.actionsCursor >= total {
			m.actionsCursor = total - 1
		}
		// Prune expanded jobs for runs that no longer exist
		if m.actionsExpanded != nil {
			runIDs := make(map[int]bool, len(msg.Runs))
			for _, r := range msg.Runs {
				runIDs[r.ID] = true
			}
			for id := range m.actionsExpanded {
				if !runIDs[id] {
					delete(m.actionsExpanded, id)
				}
			}
		}
		// Re-render if currently viewing actions
		if m.viewMode == viewModeActions {
			m.setDiffContent(m.renderActionsView())
		}
		// Schedule next tick if any runs are still active
		if git.HasActiveRuns(msg.Runs) {
			m.actionsPolling = true
			return m, pollActionsTick()
		}
		m.actionsPolling = false
		return m, nil

	case ActionsJobsFetchedMsg:
		if msg.Err != nil {
			log.Printf("Warning: failed to fetch jobs for run %d: %v", msg.RunID, msg.Err)
			return m, nil
		}
		if m.actionsExpanded == nil {
			m.actionsExpanded = make(map[int][]git.WorkflowJob)
		}
		m.actionsExpanded[msg.RunID] = msg.Jobs
		// Re-render if currently viewing actions
		if m.viewMode == viewModeActions {
			m.setDiffContent(m.renderActionsView())
		}
		return m, nil

	case actionsTickMsg:
		if !m.actionsPolling || m.pr == nil {
			return m, nil
		}
		// Only fetch — the ActionsFetchedMsg handler decides whether to schedule the next tick
		return m, fetchActions(m.pr.HeadRefOid)

	case BlameFetchedMsg:
		if msg.Err != nil {
			log.Printf("Warning: failed to fetch blame for %s: %v", msg.FilePath, msg.Err)
		} else {
			m.blameCache[msg.FilePath] = msg.Blame
		}
		return m, nil

	case CommentCreatedMsg:
		if msg.Err != nil {
			m.setDiffContent(
				styleAccentRed.Render(
					fmt.Sprintf("Error posting comment: %v", msg.Err)))
		} else if msg.Comment != nil {
			// Add to local state
			m.comments[msg.Comment.Path] = append(m.comments[msg.Comment.Path], *msg.Comment)
			// Re-render the diff with the new comment
			if m.hasFileSelected() && m.selectedFile == msg.Comment.Path {
				cmd = m.reloadDiff()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		return m, tea.Batch(cmds...)

	case SubmitReviewMsg:
		if msg.Err != nil {
			m.errorMsg = fmt.Sprintf("Error submitting review:\n\n%v", msg.Err)
		} else {
			m.errorMsg = ""
			m.chatViewport.SetContent(
				styleAccentGreen.Render("Review submitted to GitHub successfully."))
		}
		return m, nil

	case ChatRenderedMsg:
		// For Review tab (0): cache the rendered content and always apply — content is global, not per-file
		// For Chat tab (1): only apply if still on the same file
		if msg.Tab == 0 {
			m.aiReviewRendered = msg.Content
			m.aiReviewRenderWidth = m.chatViewport.Width - 2
		}
		if msg.Tab == m.aiPanelTab && (msg.Tab == 0 || msg.FilePath == m.selectedFile) {
			m.chatViewport.SetContent(msg.Content)
			m.chatViewport.GotoTop()
		}
		return m, nil

	case ReviewRenderedMsg:
		// Structured review rendered — store findings and cache content
		m.reviewFindings = msg.Findings
		m.aiReviewRendered = msg.Content
		m.aiReviewRenderWidth = m.chatViewport.Width - 2
		if m.aiPanelTab == 0 {
			m.chatViewport.SetContent(msg.Content)
			m.chatViewport.GotoTop()
		}
		// Initialize cursor to first finding and re-render with the
		// indicator visible, so the user sees the cursor immediately.
		if m.reviewCursor < 0 && len(m.reviewFindings) > 0 {
			m.reviewCursor = 0
			return m, m.rerenderReviewWithCursor()
		}
		return m, nil

	case StyledDiffMsg:
		if msg.Err != nil {
			m.setDiffContent(
				styleAccentRed.Render(
					fmt.Sprintf("Error loading diff: %v", msg.Err)))
		} else {
			if msg.FilePath == m.selectedFile {
				content := m.injectComments(msg.Content, msg.FilePath)
				// Prepend skip banner if file is excluded from AI review
				if reason, ok := m.skippedFiles[msg.FilePath]; ok {
					banner := styleTextMuted.Render(
						fmt.Sprintf("  ── skipped from AI review (%s) ──", reason))
					content = banner + "\n\n" + content
				}
				m.diffContent = content // cache for line scanning
				savedOffset := m.diffViewport.YOffset
				savedCursor := m.diffCursor
				m.setDiffContent(content)
				if msg.Reload {
					m.diffViewport.SetYOffset(savedOffset)
					m.diffCursor = savedCursor
				} else if m.pendingScrollLine > 0 {
					// Jump to the line requested by a finding navigation
					m.scrollDiffToLine(m.pendingScrollLine)
					m.pendingScrollLine = 0
				} else {
					m.diffViewport.GotoTop()
					m.diffCursor = 0
				}
			}
		}
		return m, nil

	case AIReviewInitMsg:
		m.aiReviewBatches = msg.Batches
		m.aiReviewStatuses = make([]AIReviewBatchStatus, len(msg.Batches))
		m.aiReviewPhase = "batch"
		m.updateChatViewWithStream()
		return m, nil

	case AIReviewProgressMsg:
		if msg.Batch >= 0 && msg.Batch < len(m.aiReviewStatuses) {
			m.aiReviewStatuses[msg.Batch] = msg.Status
		}
		m.updateChatViewWithStream()
		return m, nil

	case AIReviewSynthesisMsg:
		m.aiReviewPhase = "synthesis"
		m.updateChatViewWithStream()
		return m, nil

	case AIChatDeltaMsg:
		if m.aiStreaming {
			token := msg.Token
			if strings.HasPrefix(token, "\x00THOUGHT:") {
				// Thought text — render each line individually to prevent
				// viewport word-wrapping from breaking ANSI escape codes
				thought := strings.TrimPrefix(token, "\x00THOUGHT:")
				for _, line := range strings.Split(thought, "\n") {
					m.aiStreamBuffer += styleThought.Render(line) + "\n"
				}
			} else if strings.HasPrefix(token, "\x00TOOL_START:") {
				// Tool execution starting — show name and args
				tool := strings.TrimPrefix(token, "\x00TOOL_START:")
				prefix := "  ▸ "
				maxLen := m.width - 6
				if maxLen < 20 {
					maxLen = 20
				}
				if len(tool) > maxLen {
					tool = "…" + tool[len(tool)-(maxLen-1):]
				}
				m.aiStreamBuffer += "\n" + styleToolCall.Render(prefix+tool+" …") + "\n"
			} else if strings.HasPrefix(token, "\x00TOOL_DONE:") {
				// Tool execution finished — show status and duration
				// Format: name|status|duration
				parts := strings.SplitN(strings.TrimPrefix(token, "\x00TOOL_DONE:"), "|", 3)
				if len(parts) == 3 {
					name, status, dur := parts[0], parts[1], parts[2]
					indicator := "  ✓ "
					if status == "error" {
						indicator = "  ✗ "
					}
					line := fmt.Sprintf("%s%s (%s)", indicator, name, dur)
					maxLen := m.width - 6
					if maxLen < 20 {
						maxLen = 20
					}
					if len(line) > maxLen {
						line = line[:maxLen-1] + "…"
					}
					m.aiStreamBuffer += styleToolCall.Render(line) + "\n"
				}
			} else if strings.HasPrefix(token, "\x00TOOL:") {
				// Legacy tool call indicator (backward compat)
				tool := strings.TrimPrefix(token, "\x00TOOL:")
				prefix := "  ▸ "
				maxLen := m.width - 6
				if maxLen < 20 {
					maxLen = 20
				}
				if len(tool) > maxLen {
					tool = "…" + tool[len(tool)-(maxLen-1):]
				}
				m.aiStreamBuffer += "\n" + styleToolCall.Render(prefix+tool) + "\n"
			} else {
				m.aiStreamBuffer += token
			}
			if !m.aiStreamDirty {
				m.aiStreamDirty = true
				return m, tea.Tick(33*time.Millisecond, func(time.Time) tea.Msg {
					return aiStreamTickMsg{}
				})
			}
		}
		return m, nil

	case aiStreamTickMsg:
		if m.aiStreamDirty {
			m.aiStreamDirty = false
			m.updateChatViewWithStream()
		}
		return m, nil

	case previewTickMsg:
		// Only act if no newer cursor move has occurred since this tick was scheduled
		if msg.seq == m.previewSeq {
			cmd = m.previewCurrentFile()
			if cmd != nil {
				return m, cmd
			}
		}
		return m, nil

	case AIChatDoneMsg:
		m.aiStreaming = false
		m.aiCancelFn = nil
		m.aiStreamDirty = false // prevent stale tick from rendering after completion
		m.aiReviewBatches = nil
		m.aiReviewStatuses = nil
		m.aiReviewPhase = ""
		if msg.Err != nil {
			log.Printf("AI chat error: %v", msg.Err)
			// Append error to chat view
			m.aiStreamBuffer += "\n\n" + styleAccentRed.Render(
				fmt.Sprintf("[error: %v]", msg.Err))
			m.updateChatViewWithStream()
		} else {
			// Save the review if present
			if msg.Review != nil {
				// If we have a structured review, attach it
				if msg.StructuredReview != nil {
					msg.Review.Structured = msg.StructuredReview
				}
				m.saveReview(msg.Review)
			}
			// Save per-file batch findings for caching
			if msg.FileFindings != nil && m.reviewState != nil {
				for path, findings := range msg.FileFindings {
					fs, ok := m.reviewState.Files[path]
					if !ok {
						fs = &state.FileState{Status: state.StatusUnreviewed}
						m.reviewState.Files[path] = fs
					}
					fs.BatchFindings = findings
				}
				if err := state.Save(m.reviewState); err != nil {
					log.Printf("Warning: failed to save file findings: %v", err)
				}
			}
			if msg.Review != nil {
				// Review results live in Review tab only — don't pollute Chat
				m.aiStreamBuffer = ""
				m.aiChatHistoryCache = ""
				m.aiPanelTab = 0
				cmd = m.renderActiveAIView()
			} else {
				// Regular chat — save response to chat history
				cmd = m.saveAIResponse(msg.FullResponse)
				m.aiStreamBuffer = ""
				m.aiChatHistoryCache = ""
			}
		}
		return m, cmd

	case tea.KeyMsg:
		// ── Modal overlays intercept all keys when visible ──────
		if m.showPRPicker {
			if m.prPickerLoading {
				// Still loading — only allow quit
				switch msg.String() {
				case "esc", "q", "ctrl+c":
					return m, tea.Quit
				}
				return m, nil
			}
			if m.prPickerError != "" {
				// Error state — allow quit
				switch msg.String() {
				case "esc", "q", "ctrl+c", "enter":
					return m, tea.Quit
				}
				return m, nil
			}
			// Normal picker interaction
			switch msg.String() {
			case "esc", "q", "ctrl+c":
				return m, tea.Quit
			case "j", "down":
				if m.prPickerCursor < len(m.prPickerItems)-1 {
					m.prPickerCursor++
				}
			case "k", "up":
				if m.prPickerCursor > 0 {
					m.prPickerCursor--
				}
			case "enter":
				if len(m.prPickerItems) > 0 {
					selected := m.prPickerItems[m.prPickerCursor]
					m.prNumber = strconv.Itoa(selected.Number)
					m.showPRPicker = false
					m.loading = true
					m.loadingMsg = "Fetching PR data..."
					m.actionsResetPolling()
					return m, tea.Batch(
						fetchPR(m.prNumber),
						m.spinner.Tick,
						textarea.Blink,
						tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
							return logoTickMsg{}
						}),
					)
				}
			}
			return m, nil
		}
		if m.showHelp {
			switch msg.String() {
			case "?", "esc", "q":
				m.showHelp = false
			}
			return m, nil
		}
		if m.errorMsg != "" {
			// Any key dismisses the error modal
			m.errorMsg = ""
			return m, nil
		}
		if m.showModelPicker {
			models := availableModels()
			switch msg.String() {
			case "esc", "q":
				m.showModelPicker = false
			case "j", "down":
				if m.modelPickerCursor < len(models)-1 {
					m.modelPickerCursor++
				}
			case "k", "up":
				if m.modelPickerCursor > 0 {
					m.modelPickerCursor--
				}
			case "enter":
				selected := models[m.modelPickerCursor]
				m.switchModel(selected.id)
				m.showModelPicker = false
			}
			return m, nil
		}
		if m.showSubmitReview {
			switch msg.String() {
			case "esc", "q":
				m.showSubmitReview = false
			case "j", "down":
				if m.submitReviewCursor < 1 {
					m.submitReviewCursor++
				}
			case "k", "up":
				if m.submitReviewCursor > 0 {
					m.submitReviewCursor--
				}
			case "enter":
				if m.submitReviewCursor == 0 {
					// Submit
					m.showSubmitReview = false
					cmd := m.submitReviewToGitHub()
					if cmd != nil {
						return m, cmd
					}
				} else {
					m.showSubmitReview = false
				}
			}
			return m, nil
		}
		if m.showThemePicker {
			themes := BuiltinThemes()
			switch msg.String() {
			case "esc", "q":
				// Revert to original theme
				m.showThemePicker = false
				for _, t := range themes {
					if t.ID == m.themeBeforePicker {
						SetTheme(t)
						mdCache = newLRUCache(128)
						if m.hasFileSelected() && m.pr != nil {
							cmd = m.reloadDiff()
							if cmd != nil {
								cmds = append(cmds, cmd)
							}
						}
						break
					}
				}
				return m, tea.Batch(cmds...)
			case "j", "down":
				if m.themePickerCursor < len(themes)-1 {
					m.themePickerCursor++
					m.applyThemePreview(themes[m.themePickerCursor])
					cmds = append(cmds, m.reloadDiff())
				}
				return m, tea.Batch(cmds...)
			case "k", "up":
				if m.themePickerCursor > 0 {
					m.themePickerCursor--
					m.applyThemePreview(themes[m.themePickerCursor])
					cmds = append(cmds, m.reloadDiff())
				}
				return m, tea.Batch(cmds...)
			case "enter":
				selected := themes[m.themePickerCursor]
				SetTheme(selected)
				m.showThemePicker = false
				// Persist to config
				if cfg, err := config.Load(); err == nil {
					cfg.Theme = selected.ID
					if err := config.Save(cfg); err != nil {
						log.Printf("Warning: failed to persist theme: %v", err)
					}
				}
				// Diff already reloaded by preview; clear markdown cache
				mdCache = newLRUCache(128)
				return m, tea.Batch(cmds...)
			}
			return m, nil
		}

		// While AI is streaming, allow navigation but block chat input
		if m.aiStreaming {
			if msg.String() == "esc" || msg.String() == "ctrl+c" {
				if m.aiCancelFn != nil {
					m.aiCancelFn()
				}
				m.aiStreaming = false
				m.aiStreamBuffer += "\n" + styleAccentYellow.Render("[cancelled]")
				m.updateChatViewWithStream()
				return m, nil
			}
			// Block chat send (enter in chat pane) during streaming, but allow everything else
			if m.focusedPane == PaneChat && msg.String() == "enter" {
				return m, nil
			}
		}

		// Comment input mode
		if m.commenting {
			switch msg.String() {
			case "esc":
				m.commenting = false
				m.commentInput.Reset()
				m.commentInput.Blur()
				m.syncLayout()
				return m, nil
			case "ctrl+s":
				// Submit comment
				body := strings.TrimSpace(m.commentInput.Value())
				m.commenting = false
				m.commentInput.Blur()
				m.commentInput.Reset()
				m.syncLayout()
				if body != "" && m.pr != nil && m.hasFileSelected() {
					cmd = createComment(
						m.prNumber,
						m.pr.HeadRefOid,
						m.selectedFile,
						body,
						m.commentLine,
						m.commentSide,
					)
					cmds = append(cmds, cmd)
				}
				return m, tea.Batch(cmds...)
			default:
				m.commentInput, cmd = m.commentInput.Update(msg)
				cmds = append(cmds, cmd)
				return m, tea.Batch(cmds...)
			}
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			if m.focusedPane != PaneChat {
				return m, tea.Quit
			}
		case "tab":
			m.cyclePane(1)
			cmd = m.syncFocus()
			cmds = append(cmds, cmd)
		case "shift+tab":
			m.cyclePane(-1)
			cmd = m.syncFocus()
			cmds = append(cmds, cmd)
		case "enter":
			if m.focusedPane == PaneFileList {
				if m.fileTree.selectedIsDir() {
					m.fileTree.toggle()
				} else {
					// Preview already loaded the diff on cursor move;
					// Enter just moves focus to the diff pane.
					m.focusedPane = PaneDiff
					m.syncFocus()
				}
			} else if m.focusedPane == PaneChat && m.aiPanelTab == 1 {
				// Send chat message only on Chat tab; on Review tab Enter is
				// handled by the pane-specific finding navigation below.
				cmd = m.sendChatMessage()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
				return m, cmd
			}
		case "+", "=":
			// Increase diff context
			if m.focusedPane == PaneDiff && m.hasFileSelected() {
				m.contextLines += 3
				if m.contextLines > 100 {
					m.contextLines = 100
				}
				cmd = m.reloadDiff()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case "-", "_":
			// Decrease diff context
			if m.focusedPane == PaneDiff && m.hasFileSelected() {
				m.contextLines -= 3
				if m.contextLines < 0 {
					m.contextLines = 0
				}
				cmd = m.reloadDiff()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case "a":
			// Trigger one-shot AI review for selected file
			if m.focusedPane != PaneChat && !m.aiStreaming {
				cmd = m.triggerAIReview()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case "o":
			// Refresh PR from origin (re-fetch metadata, refs, diffs)
			if m.focusedPane == PaneFileList && !m.loading && !m.aiStreaming && m.pr != nil {
				m.refreshOldOid = m.pr.HeadRefOid
				m.loading = true
				m.loadingMsg = "Refreshing PR from origin..."
				return m, tea.Batch(m.spinner.Tick, fetchPR(m.prNumber))
			}
		case "n":
			// Jump to next unreviewed file
			if m.focusedPane == PaneFileList || m.focusedPane == PaneDiff {
				m.jumpToUnreviewed(1)
				cmds = append(cmds, m.schedulePreview())
			}
		case "p":
			// Jump to previous unreviewed file
			if m.focusedPane == PaneFileList || m.focusedPane == PaneDiff {
				m.jumpToUnreviewed(-1)
				cmds = append(cmds, m.schedulePreview())
			}
		case "ctrl+a":
			m.showAIPanel = !m.showAIPanel
			if !m.showAIPanel && m.focusedPane == PaneChat {
				m.focusedPane = PaneDiff
				cmd = m.syncFocus()
				cmds = append(cmds, cmd)
			}
			m.syncLayout()
		case "ctrl+b":
			m.showFilePanel = !m.showFilePanel
			if !m.showFilePanel && m.focusedPane == PaneFileList {
				m.focusedPane = PaneDiff
				cmd = m.syncFocus()
				cmds = append(cmds, cmd)
			}
			m.syncLayout()
		case "?":
			if m.focusedPane != PaneChat || m.aiPanelTab != 1 {
				m.showHelp = !m.showHelp
			}
		case "m":
			if m.focusedPane != PaneChat && !m.aiStreaming {
				m.showModelPicker = true
				// Pre-select current model
				models := availableModels()
				m.modelPickerCursor = 0
				for i, mod := range models {
					if mod.id == m.aiModelName {
						m.modelPickerCursor = i
						break
					}
				}
			}
		case "T":
			if m.focusedPane != PaneChat && !m.aiStreaming {
				m.showThemePicker = true
				m.themeBeforePicker = currentTheme.ID
				// Pre-select current theme
				themes := BuiltinThemes()
				m.themePickerCursor = 0
				for i, t := range themes {
					if t.ID == currentTheme.ID {
						m.themePickerCursor = i
						break
					}
				}
			}
		case "b":
			if m.focusedPane == PaneDiff && m.hasFileSelected() {
				m.blameEnabled = !m.blameEnabled
				// Pre-fetch blame data for the current file if not cached
				if m.blameEnabled && m.pr != nil {
					if _, ok := m.blameCache[m.selectedFile]; !ok {
						cmds = append(cmds, m.fetchBlame(m.selectedFile))
					}
				}
			}
		case "A":
			// Force re-review: clear all caches and re-run
			if m.focusedPane != PaneChat && !m.aiStreaming {
				cmd = m.forceReReview()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case "ctrl+s":
			// Submit review to GitHub (from any pane, as long as review exists)
			if !m.aiStreaming && m.hasReview() {
				m.showSubmitReview = true
				m.submitReviewCursor = 0
			}
		}
	}

	switch m.focusedPane {
	case PaneFileList:
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "j", "down":
				m.fileTree.moveDown()
				cmds = append(cmds, m.schedulePreview())
			case "k", "up":
				m.fileTree.moveUp()
				cmds = append(cmds, m.schedulePreview())
			case "l", "right":
				// Expand dir or select file
				if m.fileTree.selectedIsDir() {
					entry := m.fileTree.flat[m.fileTree.cursor]
					if !entry.node.expanded {
						m.fileTree.toggle()
					}
				} else {
					cmd = m.selectCurrentFile()
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			case "h", "left":
				if m.fileTree.selectedIsDir() {
					entry := m.fileTree.flat[m.fileTree.cursor]
					if entry.node.expanded {
						// Collapse expanded directory
						m.fileTree.toggle()
					} else {
						// Already collapsed — go to parent directory
						m.fileTree.moveToParent()
					}
				} else {
					// File — go to parent directory
					m.fileTree.moveToParent()
				}
			case " ":
				// Toggle dir expand/collapse, or toggle file review status
				if m.fileTree.selectedIsDir() {
					m.fileTree.toggle()
				} else {
					cmd = m.toggleReviewStatus()
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			case "r":
				m.fileTree.toggleHideReviewed()
				m.syncLayout()
			}
		}
	case PaneDiff:
		if km, ok := msg.(tea.KeyMsg); ok {
			// Actions view has its own keybindings
			if m.viewMode == viewModeActions {
				switch km.String() {
				case "j", "down":
					m.moveActionsCursor(1)
					m.setDiffContent(m.renderActionsView())
					return m, nil
				case "k", "up":
					m.moveActionsCursor(-1)
					m.setDiffContent(m.renderActionsView())
					return m, nil
				case "enter":
					cmd = m.toggleActionsExpand()
					return m, cmd
				case "r":
					if m.pr != nil && m.pr.HeadRefOid != "" {
						m.actionsLoading = true
						m.setDiffContent(m.renderActionsView())
						return m, fetchActions(m.pr.HeadRefOid)
					}
					return m, nil
				}
			}
			switch km.String() {
			case "esc":
				// If we came from a finding jump, return to the review panel
				if m.cameFromFinding && m.showAIPanel {
					m.cameFromFinding = false
					m.focusedPane = PaneChat
					m.aiPanelTab = 0
					cmds = append(cmds, m.syncFocus())
					cmds = append(cmds, m.renderActiveAIView())
					return m, tea.Batch(cmds...)
				}
			case "c":
				if m.hasFileSelected() && !m.commenting {
					info := m.getDiffCursorInfo()
					if info.line > 0 {
						m.commentLine = info.line
						m.commentSide = info.side
						m.commenting = true
						m.commentInput.Reset()
						m.commentInput.Focus()
						m.syncLayout()
						return m, textarea.Blink
					}
				}
			case " ":
				// Toggle reviewed status for current file
				if m.hasFileSelected() {
					cmd = m.toggleReviewStatus()
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			case "j", "down":
				m.moveDiffCursor(1)
				return m, nil
			case "k", "up":
				m.moveDiffCursor(-1)
				return m, nil
			case "G", "end":
				// Jump to bottom of diff
				total := m.diffViewport.TotalLineCount()
				vpH := m.diffViewport.Height
				maxOffset := total - vpH
				if maxOffset < 0 {
					maxOffset = 0
				}
				m.diffViewport.SetYOffset(maxOffset)
				visible := m.diffViewport.VisibleLineCount()
				if visible > 0 {
					m.diffCursor = visible - 1
				}
				return m, nil
			case "g", "home":
				// Jump to top of diff (gg in vim)
				m.diffViewport.GotoTop()
				m.diffCursor = 0
				return m, nil
			case "ctrl+d":
				// Half-page down
				half := m.diffViewport.Height / 2
				if half < 1 {
					half = 1
				}
				m.diffViewport.LineDown(half)
				m.clampDiffCursor()
				return m, nil
			case "ctrl+u":
				// Half-page up
				half := m.diffViewport.Height / 2
				if half < 1 {
					half = 1
				}
				m.diffViewport.LineUp(half)
				m.clampDiffCursor()
				return m, nil
			case "pgdown", "ctrl+f":
				m.diffViewport.LineDown(m.diffViewport.Height)
				m.clampDiffCursor()
				return m, nil
			case "pgup", "ctrl+b":
				m.diffViewport.LineUp(m.diffViewport.Height)
				m.clampDiffCursor()
				return m, nil
			default:
				prev := m.diffViewport.YOffset
				m.diffViewport, cmd = m.diffViewport.Update(msg)
				if m.diffViewport.YOffset != prev {
					m.clampDiffCursor()
				}
				cmds = append(cmds, cmd)
			}
		} else {
			m.diffViewport, cmd = m.diffViewport.Update(msg)
			cmds = append(cmds, cmd)
		}
	case PaneChat:
		if km, ok := msg.(tea.KeyMsg); ok {
			// Review tab finding navigation (j/k/Enter when findings exist)
			if m.aiPanelTab == 0 && len(m.reviewFindings) > 0 {
				switch km.String() {
				case "j", "down":
					if m.reviewCursor < len(m.reviewFindings)-1 {
						m.reviewCursor++
						cmds = append(cmds, m.rerenderReviewWithCursor())
					}
					return m, tea.Batch(cmds...)
				case "k", "up":
					if m.reviewCursor > 0 {
						m.reviewCursor--
						cmds = append(cmds, m.rerenderReviewWithCursor())
					}
					return m, tea.Batch(cmds...)
				case "enter":
					if m.reviewCursor >= 0 && m.reviewCursor < len(m.reviewFindings) {
						cmd = m.jumpToFinding(m.reviewCursor)
						if cmd != nil {
							cmds = append(cmds, cmd)
						}
					}
					return m, tea.Batch(cmds...)
				}
			}

			switch km.String() {
			case "pgup", "pgdown":
				m.chatViewport, cmd = m.chatViewport.Update(msg)
				cmds = append(cmds, cmd)
			case "ctrl+k":
				m.clearChat()
			case "ctrl+tab":
				// Toggle between Review and Chat sub-tabs (only if review exists)
				if m.hasReview() {
					m.aiPanelTab = 1 - m.aiPanelTab
					cmds = append(cmds, m.renderActiveAIView())
				}
			default:
				// Only allow text input on the Chat tab, and not while AI is streaming
				if m.aiPanelTab == 1 && !m.aiStreaming {
					m.chatInput, cmd = m.chatInput.Update(msg)
					cmds = append(cmds, cmd)
				}
			}
		} else {
			// Pass mouse/resize events to viewport for scroll support
			m.chatViewport, cmd = m.chatViewport.Update(msg)
			cmds = append(cmds, cmd)
			m.chatInput, cmd = m.chatInput.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	return m, tea.Batch(cmds...)
}

// ── AI Chat helpers ────────────────────────────────────────────────────

func (m *Model) sendChatMessage() tea.Cmd {
	text := strings.TrimSpace(m.chatInput.Value())
	if text == "" || m.aiClient == nil || program == nil {
		return nil
	}

	// Auto-switch to Chat tab when sending a message
	m.aiPanelTab = 1

	// Clear input
	m.chatInput.Reset()

	// Add user message to state
	userMsg := state.Message{Role: "user", Content: text}
	m.appendMessageToState(userMsg)

	// Build conversation history for AI
	messages := m.buildAIMessages()

	// Determine system prompt — keep instructions-only, diff context is in messages
	systemPrompt := m.getSystemPrompt()

	// If this is the first message in a file chat, inject the diff as context
	// in the first user message position so the AI has it.
	aiMessages := m.buildAIMessagesWithContext(messages)

	// Start streaming
	m.aiStreaming = true
	m.aiStreamBuffer = ""
	m.aiChatHistoryCache = "" // invalidate so it's rebuilt on first tick

	// Render chat with the new user message and streaming indicator
	m.updateChatViewWithStream()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	m.aiCancelFn = cancel

	return tea.Batch(m.spinner.Tick, streamAIChat(m.aiClient, ctx, systemPrompt, aiMessages, program))
}

func (m *Model) appendMessageToState(msg state.Message) {
	if m.reviewState == nil {
		return
	}

	if m.viewMode != viewModeFile {
		// Global chat
		m.reviewState.GlobalChat = append(m.reviewState.GlobalChat, msg)
	} else {
		fs, ok := m.reviewState.Files[m.selectedFile]
		if !ok {
			fs = &state.FileState{Status: state.StatusUnreviewed}
			m.reviewState.Files[m.selectedFile] = fs
		}
		fs.Chat = append(fs.Chat, msg)
	}
}

func (m *Model) buildAIMessages() []state.Message {
	if m.reviewState == nil {
		return nil
	}

	if m.viewMode != viewModeFile {
		return m.reviewState.GlobalChat
	}

	if fs, ok := m.reviewState.Files[m.selectedFile]; ok {
		return fs.Chat
	}
	return nil
}

func (m *Model) getAIContext() (string, string) {
	// Build PR metadata header
	var meta strings.Builder
	if m.pr != nil {
		meta.WriteString(fmt.Sprintf("PR #%d: %s\n", m.pr.Number, m.pr.Title))
		if m.pr.Body != "" {
			meta.WriteString(fmt.Sprintf("Description:\n%s\n", m.pr.Body))
		}
		meta.WriteString(fmt.Sprintf("Base: %s → Head: %s\n\n", m.pr.BaseRefName, m.pr.HeadRefName))
	}

	if m.viewMode != viewModeFile {
		// PR overview: file listing with stats — diffs are fetched via git_diff tool
		paths := make([]string, 0, len(m.rawDiffs))
		for p := range m.rawDiffs {
			paths = append(paths, p)
		}
		sort.Strings(paths)

		var allDiffs strings.Builder
		allDiffs.WriteString(meta.String())
		allDiffs.WriteString(fmt.Sprintf("Files changed (%d):\n", len(paths)))
		for _, p := range paths {
			diff := m.rawDiffs[p]
			added, removed := countDiffStats(diff)
			allDiffs.WriteString(fmt.Sprintf("  %-50s +%-4d -%d\n", p, added, removed))
		}
		allDiffs.WriteString("\nUse the git_diff tool to read the actual diffs.\n")

		return m.withInstructions(ai.ReviewPRPrompt), allDiffs.String()
	}

	// Single file — return metadata + diff
	diff := m.rawDiffs[m.selectedFile]
	return m.withInstructions(ai.ReviewFilePrompt), meta.String() + "File: " + m.selectedFile + "\n```diff\n" + diff + "\n```"
}

// withInstructions appends custom review instructions to a system prompt.
func (m *Model) withInstructions(prompt string) string {
	if m.customInstructions == "" {
		return prompt
	}
	return prompt + "\n\n## Project-Specific Instructions\n\n" + m.customInstructions
}

// getSystemPrompt returns the appropriate system prompt for the current context.
// System prompts contain only instructions, never data.
func (m *Model) getSystemPrompt() string {
	if m.viewMode != viewModeFile {
		return m.withInstructions(ai.ChatPrompt)
	}
	// Use ReviewFilePrompt for file-level chats — keeps the reviewer persona
	// consistent across initial review and follow-up questions.
	return m.withInstructions(ai.ReviewFilePrompt)
}

// buildAIMessagesWithContext converts state messages to AI messages,
// injecting diff context as the first message if not already present.
func (m *Model) buildAIMessagesWithContext(messages []state.Message) []ai.Message {
	aiMessages := make([]ai.Message, 0, len(messages)+1)

	// For file-level chats, check if diff context is already in the first message
	// (from triggerAIReview). If not, inject it as a system-context user message.
	needsContext := m.hasFileSelected() && len(messages) > 0
	if needsContext {
		first := messages[0]
		if first.Role == "user" && !strings.Contains(first.Content, "```diff") {
			// Inject diff context before the conversation
			diff := m.rawDiffs[m.selectedFile]
			if diff != "" {
				var meta strings.Builder
				if m.pr != nil {
					meta.WriteString(fmt.Sprintf("PR #%d: %s\n", m.pr.Number, m.pr.Title))
					meta.WriteString(fmt.Sprintf("Base: %s → Head: %s\n\n", m.pr.BaseRefName, m.pr.HeadRefName))
				}
				contextMsg := fmt.Sprintf("%sHere is the diff for `%s`:\n\n```diff\n%s\n```\n\nI'll be asking questions about this file.",
					meta.String(), m.selectedFile, diff)
				aiMessages = append(aiMessages, ai.Message{Role: "user", Content: contextMsg})
				aiMessages = append(aiMessages, ai.Message{Role: "assistant", Content: "I've reviewed the diff. What would you like to know?"})
			}
		}
	}

	for _, msg := range messages {
		aiMessages = append(aiMessages, ai.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	return aiMessages
}

func (m *Model) saveAIResponse(response string) tea.Cmd {
	if m.reviewState == nil {
		return nil
	}

	assistantMsg := state.Message{Role: "assistant", Content: response}
	m.appendMessageToState(assistantMsg)

	// Persist to disk
	if err := state.Save(m.reviewState); err != nil {
		log.Printf("Warning: failed to save chat state: %v", err)
	}

	// Re-render the active view
	return m.renderActiveAIView()
}

// saveReview persists an AI review to the state for the current file/PR.
func (m *Model) saveReview(review *state.AIReview) {
	if m.reviewState == nil {
		return
	}

	// Stamp the diff snapshot so we can detect staleness later.
	review.DiffSnapshot = m.reviewState.DiffSnapshotFromFiles()

	m.reviewState.Review = review
	m.aiReviewRendered = "" // invalidate cached render
	m.reviewFindings = nil  // reset findings for re-population
	m.reviewCursor = -1     // reset cursor

	if err := state.Save(m.reviewState); err != nil {
		log.Printf("Warning: failed to save review state: %v", err)
	}
}

func (m *Model) clearChat() {
	if m.reviewState == nil || m.aiStreaming {
		return
	}

	if m.viewMode != viewModeFile {
		m.reviewState.GlobalChat = nil
	} else {
		if fs, ok := m.reviewState.Files[m.selectedFile]; ok {
			fs.Chat = nil
			m.reviewState.Files[m.selectedFile] = fs
		}
	}

	if err := state.Save(m.reviewState); err != nil {
		log.Printf("Warning: failed to save state after clearing chat: %v", err)
	}

	m.chatViewport.SetContent(styleTextMuted.Render("Chat cleared"))
	m.chatViewport.GotoTop()
}

func (m *Model) updateChatViewWithStream() {
	// If a PR review is streaming but the user has navigated to a file,
	// don't overwrite the file's chat panel with review stream content.
	if m.aiReviewPhase != "" && m.hasFileSelected() {
		return
	}

	var b strings.Builder

	// Use cached rendered prefix of completed messages (built once when streaming starts)
	// instead of re-rendering markdown on every 33ms tick.
	if m.aiChatHistoryCache == "" {
		// Build and cache the rendered history
		messages := m.buildAIMessages()
		var hb strings.Builder
		for _, msg := range messages {
			switch msg.Role {
			case "user":
				hb.WriteString(styleAccentBlueBold.Render("You") + "\n")
				hb.WriteString(wrapStyled(styleTextSecondary, msg.Content, m.chatViewport.Width-2) + "\n\n")
			case "assistant":
				hb.WriteString(styleAccentMauveBold.Render("AI") + "\n")
				hb.WriteString(renderMarkdown(msg.Content, m.chatViewport.Width-2) + "\n\n")
			}
		}
		m.aiChatHistoryCache = hb.String()
	}
	b.WriteString(m.aiChatHistoryCache)

	// Render streaming AI response
	if m.aiStreaming || m.aiStreamBuffer != "" {
		b.WriteString(styleAccentMauveBold.Render("AI") + "\n")

		// During review, show the in-place batch list
		if len(m.aiReviewBatches) > 0 {
			b.WriteString(m.renderBatchList())
		}

		if m.aiReviewPhase == "synthesis" || len(m.aiReviewBatches) == 0 {
			// Show synthesis stream or regular chat stream
			if m.aiStreamBuffer == "" && len(m.aiReviewBatches) == 0 {
				b.WriteString(styleTextMuted.Render("thinking...") + "\n")
			} else if m.aiStreamBuffer != "" {
				if m.aiReviewPhase == "synthesis" {
					// During synthesis the model outputs raw JSON — hide it
					// and show a friendlier progress message instead.
					b.WriteString(styleTextMuted.Render("Synthesizing final review...") + "\n")
				} else {
					b.WriteString(m.aiStreamBuffer)
				}
				if m.aiStreaming {
					b.WriteString(styleAccentBlue.Render("▊"))
				}
				b.WriteString("\n")
			}
		}
	}

	// Only auto-scroll if the user was already at the bottom.
	// This lets users scroll up to read earlier messages during streaming.
	wasAtBottom := m.chatViewport.AtBottom()
	m.chatViewport.SetContent(b.String())
	if wasAtBottom {
		m.chatViewport.GotoBottom()
	}
}

// renderChatInputLabel renders a styled separator with a prompt indicator
// above the chat input textarea.
func (m Model) renderChatInputLabel(width int) string {
	if m.focusedPane == PaneChat {
		prompt := styleAccentBlueBold.Render(" › ")
		label := styleTextMuted.Render("Enter to send")
		pw := ansi.StringWidth(prompt)
		lw := ansi.StringWidth(label)
		fill := width - pw - lw
		if fill < 1 {
			fill = 1
		}
		return prompt + strings.Repeat(" ", fill) + label
	}
	// Blurred: subtle separator
	return styleTextSubtle.Render(strings.Repeat("─", width))
}

// hasReview returns true if a PR-level review exists.
func (m Model) hasReview() bool {
	return m.reviewState != nil && m.reviewState.Review != nil
}

// renderAIPanelTitle builds the AI panel title with tab indicators and streaming status.
func (m Model) renderAIPanelTitle(maxWidth int) string {
	// During streaming, show progress or spinner instead of tabs
	if m.aiStreaming {
		if len(m.aiReviewBatches) > 0 {
			return m.renderReviewProgress(maxWidth)
		}
		return "CHAT " + m.spinner.View()
	}

	// If no review exists, just show "Chat" — no tabs needed
	if !m.hasReview() {
		return styleAccentBlueBold.Render("Chat")
	}

	// Tab indicators: [Review] [Chat] — active tab is highlighted
	reviewTab := "Review"
	chatTab := "Chat"
	if m.aiPanelTab == 0 {
		reviewTab = styleAccentBlueBold.Render("Review")
		chatTab = styleTextMuted.Render("Chat")
	} else {
		reviewTab = styleTextMuted.Render("Review")
		chatTab = styleAccentBlueBold.Render("Chat")
	}
	return reviewTab + styleTextSubtle.Render(" │ ") + chatTab
}

// renderReviewProgress renders the AI panel title with a progress bar
// during multi-pass review.
func (m Model) renderReviewProgress(maxWidth int) string {
	spin := m.spinner.View()
	total := len(m.aiReviewBatches)

	var completed, active int
	for _, s := range m.aiReviewStatuses {
		switch s {
		case BatchDone, BatchCached, BatchFailed:
			completed++
		case BatchActive:
			active++
		}
	}

	var label string
	if m.aiReviewPhase == "synthesis" {
		label = "Synthesizing final review"
	} else if active > 1 {
		label = fmt.Sprintf("Reviewing  %d/%d done, %d running", completed, total, active)
	} else if active == 1 {
		// Find the active batch label
		for i, s := range m.aiReviewStatuses {
			if s == BatchActive {
				label = fmt.Sprintf("Reviewing %s  %d/%d", m.aiReviewBatches[i].Label, completed+1, total)
				break
			}
		}
	} else {
		label = fmt.Sprintf("Reviewing  %d/%d done", completed, total)
	}

	// Build progress bar: ████░░░░
	barWidth := 10
	if total > 0 && m.aiReviewPhase != "synthesis" {
		filled := (completed * barWidth) / total
		if filled > barWidth {
			filled = barWidth
		}
		empty := barWidth - filled
		bar := styleProgressBar.Render(strings.Repeat("█", filled)) +
			styleProgressBg.Render(strings.Repeat("░", empty))
		return spin + " " + bar + " " + styleTextMuted.Render(label)
	}

	// Synthesis phase — full bar
	bar := styleProgressBar.Render(strings.Repeat("█", barWidth))
	return spin + " " + bar + " " + styleTextMuted.Render(label)
}

// renderBatchList renders the in-place batch progress list shown during review.
// Each batch is a single line: status icon + label + file count.
func (m Model) renderBatchList() string {
	if len(m.aiReviewBatches) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n")

	for i, batch := range m.aiReviewBatches {
		status := m.aiReviewStatuses[i]

		var icon string
		switch status {
		case BatchPending:
			icon = styleTextSubtle.Render("  ○")
		case BatchActive:
			icon = styleAccentBlue.Render("  ●")
		case BatchDone:
			icon = styleAccentGreen.Render("  " + checkMark)
		case BatchCached:
			icon = styleAccentGreen.Render("  " + checkMark)
		case BatchFailed:
			icon = styleAccentRed.Render("  ✗")
		}

		suffix := ""
		if status == BatchCached {
			suffix = " (cached)"
		}

		fileWord := "files"
		if batch.NumFiles == 1 {
			fileWord = "file"
		}

		// Truncate long paths from the left, keeping the deepest part visible.
		const maxLabelLen = 24
		displayLabel := batch.Label
		if len(displayLabel) > maxLabelLen {
			displayLabel = "…" + displayLabel[len(displayLabel)-(maxLabelLen-1):]
		}

		label := fmt.Sprintf(" %-24s %d %s%s",
			displayLabel, batch.NumFiles, fileWord, suffix)

		if status == BatchPending {
			b.WriteString(icon + styleTextSubtle.Render(label))
		} else if status == BatchActive {
			b.WriteString(icon + styleBatchOutput.Render(label))
		} else {
			b.WriteString(icon + styleBatchOutput.Render(label))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// ── Comment helpers ─────────────────────────────────────────────────────

// reDiffLineNums extracts left and right line numbers from delta's line number column.
// Delta formats lines as: "  OLD│NEW│ code" — either side can be blank.
var reDiffLineNums = regexp.MustCompile(`^\s*(\d+)?\s*│\s*(\d+)?\s*│`)

// diffLineInfo holds parsed line information from a diff row.
type diffLineInfo struct {
	leftLine  int    // old file line number (0 if not present)
	rightLine int    // new file line number (0 if not present)
	side      string // "LEFT" for deletions, "RIGHT" for additions/context
	line      int    // the line number to send to the API
}

// parseDiffLine extracts line info from a rendered diff row.
func parseDiffLine(rendered string) diffLineInfo {
	clean := stripANSI(rendered)
	matches := reDiffLineNums.FindStringSubmatch(clean)
	if len(matches) < 3 {
		return diffLineInfo{}
	}

	var info diffLineInfo
	if matches[1] != "" {
		info.leftLine, _ = strconv.Atoi(matches[1])
	}
	if matches[2] != "" {
		info.rightLine, _ = strconv.Atoi(matches[2])
	}

	if info.rightLine == 0 && info.leftLine > 0 {
		// Deletion: only left number present
		info.side = "LEFT"
		info.line = info.leftLine
	} else if info.rightLine > 0 {
		// Addition or context: right number present
		info.side = "RIGHT"
		info.line = info.rightLine
	}

	return info
}

// injectComments inserts styled comment blocks after their target lines in the diff.
func (m *Model) injectComments(styledDiff, filePath string) string {
	fileComments, ok := m.comments[filePath]
	if !ok || len(fileComments) == 0 {
		return styledDiff
	}

	// Build map of "side:line" -> comments
	type commentKey struct {
		side string
		line int
	}
	commentsByKey := make(map[commentKey][]git.ReviewComment)
	for _, c := range fileComments {
		key := commentKey{side: c.Side, line: c.Line}
		commentsByKey[key] = append(commentsByKey[key], c)
	}

	commentStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#F9E2AF")).
		Bold(true)
	bodyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#CDD6F4"))
	borderStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#585B70"))

	lines := strings.Split(styledDiff, "\n")
	var result []string

	for _, line := range lines {
		result = append(result, line)

		info := parseDiffLine(line)
		if info.line == 0 {
			continue
		}

		key := commentKey{side: info.side, line: info.line}
		if comments, ok := commentsByKey[key]; ok {
			for _, c := range comments {
				border := borderStyle.Render("  ┌─ ")
				author := commentStyle.Render(c.Author)
				result = append(result, border+author)
				for _, bodyLine := range strings.Split(c.Body, "\n") {
					prefix := borderStyle.Render("  │ ")
					result = append(result, prefix+bodyStyle.Render(bodyLine))
				}
				result = append(result, borderStyle.Render("  └───"))
			}
		}
	}

	return strings.Join(result, "\n")
}

// stripANSI removes all ANSI escape sequences from a string for parsing.
var reANSI = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return reANSI.ReplaceAllString(s, "")
}

// countDiffStats counts added and removed lines in a unified diff.
func countDiffStats(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+':
			if !strings.HasPrefix(line, "+++") {
				added++
			}
		case '-':
			if !strings.HasPrefix(line, "---") {
				removed++
			}
		}
	}
	return
}

// truncateToWidth truncates a string (potentially containing ANSI codes) to
// a given visible width, preserving escape sequences and appending a reset.
func truncateToWidth(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	var b strings.Builder
	visW := 0
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			b.WriteRune(r)
			continue
		}
		if inEsc {
			b.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		rw := 1
		if r > 0x7F {
			// East Asian wide chars, emoji, etc — approximate
			rw = ansi.StringWidth(string(r))
		}
		if visW+rw > maxW {
			break
		}
		b.WriteRune(r)
		visW += rw
	}
	b.WriteString("\x1b[0m") // reset to avoid style bleeding
	return b.String()
}

// clampDiffCursor ensures diffCursor is within valid bounds after a viewport
// scroll that didn't go through moveDiffCursor (e.g. page down, G).
func (m *Model) clampDiffCursor() {
	visible := m.diffViewport.VisibleLineCount()
	if m.diffCursor >= visible {
		m.diffCursor = visible - 1
	}
	if m.diffCursor < 0 {
		m.diffCursor = 0
	}
}

// setDiffContent sets viewport content after truncating lines to viewport width.
// This prevents lipgloss wrapping inside the viewport's View(), which would
// cause a mismatch between logical line count (used for scrolling) and visual
// line count (used for rendering), making the bottom of long diffs unreachable.
func (m *Model) setDiffContent(content string) {
	w := m.diffViewport.Width
	if w > 0 {
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if ansi.StringWidth(line) > w {
				lines[i] = ansi.Truncate(line, w, "")
			}
		}
		content = strings.Join(lines, "\n")
	}
	m.diffViewport.SetContent(content)
}

// getDiffCursorLine returns the file line number at the current diff cursor position
// by parsing it directly from the rendered viewport output.
func (m *Model) getDiffCursorLine() int {
	info := m.getDiffCursorInfo()
	return info.line
}

// getDiffCursorInfo returns full line info at the current diff cursor position.
func (m *Model) getDiffCursorInfo() diffLineInfo {
	lines := strings.Split(m.diffViewport.View(), "\n")
	if m.diffCursor < 0 || m.diffCursor >= len(lines) {
		return diffLineInfo{}
	}
	return parseDiffLine(lines[m.diffCursor])
}

// moveDiffCursor moves the cursor up or down within the diff viewport,
// scrolling the viewport when the cursor reaches the edges.
func (m *Model) moveDiffCursor(dir int) {
	newCursor := m.diffCursor + dir
	vpHeight := m.diffViewport.Height

	if newCursor < 0 {
		if m.diffViewport.YOffset > 0 {
			m.diffViewport.LineUp(1)
		}
		newCursor = 0
	} else if newCursor >= vpHeight {
		m.diffViewport.LineDown(1)
		newCursor = vpHeight - 1
	}

	// Clamp: check we're not past the end of content
	totalVisible := m.diffViewport.VisibleLineCount()
	if newCursor >= totalVisible {
		return
	}

	m.diffCursor = newCursor
}

// ── Data helpers ────────────────────────────────────────────────────────

// ── Actions view helpers ────────────────────────────────────────────────

// actionsRowCount returns the total number of navigable rows in the actions view.
func (m *Model) actionsRowCount() int {
	count := len(m.actionsRuns)
	for _, run := range m.actionsRuns {
		if jobs, ok := m.actionsExpanded[run.ID]; ok {
			count += len(jobs)
		}
	}
	return count
}

// actionsResetPolling clears all GitHub Actions state. Call this at every
// PR transition point (picker, refresh) so stale ticks are discarded.
func (m *Model) actionsResetPolling() {
	m.actionsPolling = false
	m.actionsRuns = nil
	m.actionsLoading = false
	m.actionsExpanded = nil
	m.actionsCursor = 0
}

func (m *Model) moveActionsCursor(dir int) {
	total := m.actionsRowCount()
	if total == 0 {
		return
	}
	m.actionsCursor += dir
	if m.actionsCursor < 0 {
		m.actionsCursor = 0
	}
	if m.actionsCursor >= total {
		m.actionsCursor = total - 1
	}
}

// toggleActionsExpand expands or collapses the run at the current actions cursor.
func (m *Model) toggleActionsExpand() tea.Cmd {
	if len(m.actionsRuns) == 0 {
		return nil
	}

	// Map flat cursor to a run index
	row := 0
	for _, run := range m.actionsRuns {
		if row == m.actionsCursor {
			// This is a run row — toggle expansion
			if m.actionsExpanded == nil {
				m.actionsExpanded = make(map[int][]git.WorkflowJob)
			}
			if _, expanded := m.actionsExpanded[run.ID]; expanded {
				delete(m.actionsExpanded, run.ID)
				m.setDiffContent(m.renderActionsView())
				return nil
			}
			// Fetch jobs for this run
			return fetchActionsJobs(run.ID)
		}
		row++
		if jobs, ok := m.actionsExpanded[run.ID]; ok {
			row += len(jobs)
		}
	}
	return nil
}

func (m *Model) triggerAIReview() tea.Cmd {
	if m.aiClient == nil || program == nil || m.pr == nil {
		return nil
	}

	// Auto-show the AI panel
	if !m.showAIPanel {
		m.showAIPanel = true
		m.syncLayout()
	}

	// Determine which file to review
	path := m.selectedFile
	if path == "" {
		// If no file selected yet, use tree cursor
		path = m.fileTree.selectedPath()
	}

	if path == "" {
		// Overview mode — multi-pass PR review
		_, prMeta := m.getAIContext()

		// Clear the previous review so the streaming view takes precedence
		if m.reviewState != nil {
			m.reviewState.Review = nil
		}

		// Start streaming
		m.aiStreaming = true
		m.aiPanelTab = 1 // show Chat tab (batch list + synthesis stream)
		m.aiStreamBuffer = ""
		m.aiChatHistoryCache = ""
		m.updateChatViewWithStream()

		// Longer timeout for multi-pass (multiple sequential AI calls)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		m.aiCancelFn = cancel

		return tea.Batch(m.spinner.Tick, streamMultiPassReview(ctx, m.aiClient, prMeta, m.rawDiffs, m.customInstructions, m.reviewState, m.parallelReviews, teaReporter{p: program}))
	}

	// Single file mode
	if m.selectedFile != path {
		m.selectedFile = path
	}

	// Skip files excluded by content filter (binary, generated, large)
	if reason, ok := m.skippedFiles[path]; ok {
		m.aiPanelTab = 1
		m.chatViewport.SetContent(
			styleTextMuted.Render(fmt.Sprintf("This file is excluded from AI review (%s).", reason)))
		return nil
	}

	diff := m.rawDiffs[path]
	if diff == "" {
		return nil
	}

	// Skip files that are excluded from review (lock files, generated code, etc.)
	if config.ShouldExcludeFromReview(path) {
		m.aiPanelTab = 1 // show message in Chat tab
		m.chatViewport.SetContent(
			styleTextMuted.Render("This file is excluded from AI review (lock file, generated code, or vendored dependency)."))
		return nil
	}

	// System prompt is instructions-only; diff goes in the user message
	systemPrompt := m.withInstructions(ai.ReviewFilePrompt)

	// For large diffs, instruct the AI to use the git_diff tool instead of
	// pasting the full diff inline. This keeps the cacheable prefix stable
	// and avoids blowing context limits.
	const largeDiffLines = 4000
	diffLines := strings.Count(diff, "\n")
	var userContent string
	if diffLines > largeDiffLines {
		userContent = fmt.Sprintf(
			"Please review the changes to `%s`. The diff is large (%d lines), so use the git_diff tool with paths=\"%s\" to read it, using pagination if needed.",
			path, diffLines, path,
		)
	} else {
		userContent = fmt.Sprintf("Please review the changes to `%s`.\n\n```diff\n%s\n```", path, diff)
	}

	// Add to state
	userStateMsg := state.Message{Role: "user", Content: userContent}
	m.appendMessageToState(userStateMsg)

	// Build full conversation history so the AI sees prior messages
	messages := m.buildAIMessages()
	aiMessages := m.buildAIMessagesWithContext(messages)

	// Start streaming on the Chat tab
	m.aiStreaming = true
	m.aiPanelTab = 1
	m.aiStreamBuffer = ""
	m.aiChatHistoryCache = "" // invalidate so it's rebuilt on first tick
	m.updateChatViewWithStream()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	m.aiCancelFn = cancel

	return tea.Batch(m.spinner.Tick, streamAIChat(m.aiClient, ctx, systemPrompt, aiMessages, program))
}

// forceReReview clears all cached batch findings and triggers a fresh PR review.
func (m *Model) forceReReview() tea.Cmd {
	if m.reviewState == nil {
		return m.triggerAIReview()
	}

	// Clear per-file batch findings so nothing is cached
	for _, fs := range m.reviewState.Files {
		fs.BatchFindings = ""
		fs.Purpose = ""
	}
	// Clear the PR-level review
	m.reviewState.Review = nil
	m.aiReviewRendered = ""
	m.reviewFindings = nil
	m.reviewCursor = -1

	// Persist cleared state
	if err := state.Save(m.reviewState); err != nil {
		log.Printf("Warning: failed to save cleared state: %v", err)
	}

	// Now trigger a normal review — with caches cleared it will re-run everything
	return m.triggerAIReview()
}

// SubmitReviewMsg is sent when a GitHub review submission completes.
type SubmitReviewMsg struct {
	Err error
}

// submitReviewToGitHub submits the current AI review as a formal GitHub review.
func (m *Model) submitReviewToGitHub() tea.Cmd {
	if m.reviewState == nil || m.reviewState.Review == nil || m.reviewState.Review.Structured == nil {
		return nil
	}

	verdict := m.reviewState.Review.Structured.Verdict
	body := m.reviewState.Review.Structured.Summary
	prNumber := m.prNumber

	// Map AI verdict to GitHub review event
	var ghVerdict string
	switch verdict {
	case "approve":
		ghVerdict = "APPROVE"
	case "request_changes":
		ghVerdict = "REQUEST_CHANGES"
	default:
		ghVerdict = "COMMENT"
	}

	return func() tea.Msg {
		err := git.SubmitReview(prNumber, ghVerdict, body)
		return SubmitReviewMsg{Err: err}
	}
}

func (m *Model) jumpToUnreviewed(direction int) {
	if len(m.fileTree.flat) == 0 || m.reviewState == nil {
		return
	}

	start := m.fileTree.cursor
	n := len(m.fileTree.flat)

	for i := 1; i < n; i++ {
		idx := (start + i*direction + n) % n
		if idx < 0 || idx >= n {
			continue
		}
		entry := m.fileTree.flat[idx]
		if entry.node.isDir || entry.node.isOverview || entry.node.isActions {
			continue
		}
		// Check if unreviewed
		if entry.node.status != state.StatusReviewed {
			m.fileTree.cursor = idx
			m.fileTree.ensureVisible()
			return
		}
	}
}

// scrollDiffToLine scans the cached diff content and scrolls the viewport
// so that the line matching targetLine (right-side line number) is visible.
func (m *Model) scrollDiffToLine(targetLine int) {
	if targetLine <= 0 || m.diffContent == "" {
		return
	}

	allLines := strings.Split(m.diffContent, "\n")

	bestOffset := -1
	bestDist := math.MaxInt

	for i, line := range allLines {
		info := parseDiffLine(line)
		if info.rightLine > 0 {
			dist := info.rightLine - targetLine
			if dist < 0 {
				dist = -dist
			}
			if dist < bestDist {
				bestDist = dist
				bestOffset = i
			}
			if info.rightLine == targetLine {
				break // exact match
			}
		}
	}

	if bestOffset >= 0 {
		// Center the target line in the viewport
		vpHeight := m.diffViewport.Height
		offset := bestOffset - vpHeight/2
		if offset < 0 {
			offset = 0
		}
		// Clamp to max scroll position to keep cursor calculation consistent
		maxOffset := m.diffViewport.TotalLineCount() - vpHeight
		if maxOffset < 0 {
			maxOffset = 0
		}
		if offset > maxOffset {
			offset = maxOffset
		}
		m.diffViewport.SetYOffset(offset)

		// Set diff cursor to the target line within the visible area
		m.diffCursor = bestOffset - offset
		if m.diffCursor < 0 {
			m.diffCursor = 0
		}
		if m.diffCursor >= vpHeight {
			m.diffCursor = vpHeight - 1
		}
	}
}

// jumpToFinding navigates to the file:line referenced by the finding at
// the given index. Selects the file in the tree, loads the diff, and
// sets up pending scroll to the target line.
func (m *Model) jumpToFinding(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.reviewFindings) || m.pr == nil {
		return nil
	}

	finding := m.reviewFindings[idx]
	if finding.File == "" {
		return nil
	}

	// Select the file in the tree
	m.fileTree.selectByPath(finding.File)

	// Set up pending scroll target
	m.selectedFile = finding.File
	m.pendingScrollLine = finding.Line
	m.cameFromFinding = true

	// Switch focus to diff pane
	m.focusedPane = PaneDiff
	m.chatInput.Blur()

	// Load the diff
	m.setDiffContent(styleTextMuted.Render("Loading diff..."))
	diffCmd := fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, finding.File, m.contextLines, false, m.useChroma, m.diffViewport.Width)
	return diffCmd
}

func (m *Model) toggleReviewStatus() tea.Cmd {
	path := m.fileTree.selectedPath()
	if path == "" || m.reviewState == nil {
		return nil
	}

	fs, ok := m.reviewState.Files[path]
	if !ok {
		fs = &state.FileState{Status: state.StatusUnreviewed}
		m.reviewState.Files[path] = fs
	}

	// Toggle: unreviewed/modified -> reviewed, reviewed -> unreviewed
	if fs.Status == state.StatusReviewed {
		fs.Status = state.StatusUnreviewed
	} else {
		fs.Status = state.StatusReviewed
	}

	// Update the tree node
	if m.fileTree.cursor >= 0 && m.fileTree.cursor < len(m.fileTree.flat) {
		m.fileTree.flat[m.fileTree.cursor].node.status = fs.Status
	}

	// If hideReviewed is active, rebuild the flat list so the file disappears/appears
	if m.fileTree.hideReviewed {
		m.fileTree.flatten()
		if m.fileTree.cursor >= len(m.fileTree.flat) {
			m.fileTree.cursor = len(m.fileTree.flat) - 1
		}
		if m.fileTree.cursor < 0 {
			m.fileTree.cursor = 0
		}
		m.fileTree.ensureVisible()
	}

	// Save state
	if err := state.Save(m.reviewState); err != nil {
		log.Printf("Warning: failed to save review state: %v", err)
	}

	// If hideReviewed caused the cursor to move, preview the new file
	if m.fileTree.hideReviewed {
		return m.previewCurrentFile()
	}
	return nil
}

// hasFileSelected returns true when the diff pane is showing an actual file diff.
func (m *Model) hasFileSelected() bool {
	return m.viewMode == viewModeFile && m.selectedFile != ""
}

func (m *Model) reloadDiff() tea.Cmd {
	if m.viewMode != viewModeFile || m.selectedFile == "" || m.pr == nil {
		return nil
	}
	// Don't replace content with "Loading..." — keep current diff visible to avoid flicker
	return fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, m.selectedFile, m.contextLines, true, m.useChroma, m.diffViewport.Width)
}

// applyThemePreview switches the active theme for live preview without persisting.
func (m *Model) applyThemePreview(t Theme) {
	SetTheme(t)
	mdCache = newLRUCache(128)
}

func (m *Model) populateFileList(st *state.State) {
	if m.pr == nil {
		return
	}

	var files []fileInfo
	for _, f := range m.pr.Files {
		status := state.StatusUnreviewed
		if st != nil {
			if fs, ok := st.Files[f.Path]; ok {
				status = fs.Status
			}
		}
		var skipReason string
		if reason, ok := m.skippedFiles[f.Path]; ok {
			skipReason = string(reason)
		}
		files = append(files, fileInfo{
			path:       f.Path,
			additions:  f.Additions,
			deletions:  f.Deletions,
			status:     status,
			skipReason: skipReason,
		})
	}

	m.fileTree = newFileTree(files)
	m.fileTree.height = m.contentHeight() - 2 // account for border
}

// schedulePreview increments the debounce sequence and schedules a preview
// after a short delay. If the user moves again before the tick fires,
// the sequence won't match and the stale tick is discarded.
func (m *Model) schedulePreview() tea.Cmd {
	m.previewSeq++
	seq := m.previewSeq
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return previewTickMsg{seq: seq}
	})
}

// previewCurrentFile loads the diff for the currently highlighted file
// without changing pane focus. Skips dirs and avoids reloading the same file.
func (m *Model) previewCurrentFile() tea.Cmd {
	if m.pr == nil || m.fileTree.selectedIsDir() {
		return nil
	}

	if m.fileTree.selectedIsOverview() {
		if m.viewMode == viewModeOverview {
			return nil // already showing overview
		}
		m.selectedFile = ""
		m.viewMode = viewModeOverview
		m.setDiffContent(m.renderOverview())
		m.diffViewport.GotoTop()
		m.diffCursor = 0
		return m.renderActiveAIView()
	}

	if m.fileTree.selectedIsActions() {
		m.selectedFile = ""
		m.viewMode = viewModeActions
		m.setDiffContent(m.renderActionsView())
		m.diffViewport.GotoTop()
		m.diffCursor = 0
		return nil
	}

	path := m.fileTree.selectedPath()
	if path == "" || path == m.selectedFile {
		return nil
	}

	m.selectedFile = path
	m.viewMode = viewModeFile
	m.setDiffContent(
		styleTextMuted.Render("Loading diff..."))
	chatCmd := m.renderActiveAIView()
	diffCmd := fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, path, m.contextLines, false, m.useChroma, m.diffViewport.Width)
	return tea.Batch(chatCmd, diffCmd)
}

func (m *Model) selectCurrentFile() tea.Cmd {
	if m.pr == nil {
		return nil
	}
	if m.fileTree.selectedIsDir() {
		m.fileTree.toggle()
		return nil
	}

	if m.fileTree.selectedIsOverview() {
		m.selectedFile = ""
		m.viewMode = viewModeOverview
		m.setDiffContent(m.renderOverview())
		m.diffViewport.GotoTop()
		m.diffCursor = 0
		return m.renderActiveAIView()
	}

	if m.fileTree.selectedIsActions() {
		m.selectedFile = ""
		m.viewMode = viewModeActions
		m.setDiffContent(m.renderActionsView())
		m.diffViewport.GotoTop()
		m.diffCursor = 0
		return nil
	}

	path := m.fileTree.selectedPath()
	if path == "" {
		return nil
	}

	m.selectedFile = path
	m.viewMode = viewModeFile
	m.setDiffContent(
		styleTextMuted.Render("Loading diff..."))
	chatCmd := m.renderActiveAIView()
	diffCmd := fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, path, m.contextLines, false, m.useChroma, m.diffViewport.Width)
	return tea.Batch(chatCmd, diffCmd)
}

// renderActiveAIView renders the currently selected AI panel tab for the current file.
func (m *Model) renderActiveAIView() tea.Cmd {
	// If a PR review is currently streaming and we're on the overview,
	// show the live stream instead of static content.
	if m.aiReviewPhase != "" && m.viewMode != viewModeFile {
		m.updateChatViewWithStream()
		return nil
	}
	if m.aiPanelTab == 0 && m.hasReview() {
		return m.renderReviewForFile(m.selectedFile)
	}
	// No review or Chat tab selected — show chat
	m.aiPanelTab = 1
	return m.renderChatForFile(m.selectedFile)
}

// renderReviewForFile renders the PR-level AI review in the Review tab.
// If a structured ReviewOutput is available, it renders that with severity
// grouping and color coding. Otherwise falls back to markdown rendering.
func (m *Model) renderReviewForFile(filePath string) tea.Cmd {
	if m.reviewState == nil || m.reviewState.Review == nil {
		return nil
	}

	width := m.chatViewport.Width - 2
	cursor := m.reviewCursor

	// If we already rendered at this width, reuse the cached content.
	// (Cache is invalidated when a new review arrives or cursor changes
	// via rerenderReviewWithCursor.)
	if m.aiReviewRendered != "" && m.aiReviewRenderWidth == width {
		m.chatViewport.SetContent(m.aiReviewRendered)
		return nil
	}

	review := m.reviewState.Review

	// Show placeholder, render async
	m.chatViewport.SetContent(styleTextMuted.Render("Rendering review..."))

	// Check if we have structured review data
	if review.Structured != nil {
		structured := review.Structured
		stale := m.reviewState.IsReviewStale()
		return func() tea.Msg {
			rendered, findings := renderStructuredReview(structured, width, cursor, stale)
			return ReviewRenderedMsg{Content: rendered, Findings: findings}
		}
	}

	// Fallback: render raw summary as markdown
	fp := filePath // capture for closure
	summary := review.Summary
	return func() tea.Msg {
		rendered := renderMarkdown(summary, width)
		return ChatRenderedMsg{FilePath: fp, Content: rendered, Tab: 0}
	}
}

// rerenderReviewWithCursor re-renders the structured review synchronously
// with the current cursor position and updates the viewport. This is called
// when the user moves the finding cursor (j/k) so the indicator updates
// immediately without async rendering delay.
func (m *Model) rerenderReviewWithCursor() tea.Cmd {
	if m.reviewState == nil || m.reviewState.Review == nil || m.reviewState.Review.Structured == nil {
		return nil
	}

	width := m.chatViewport.Width - 2
	stale := m.reviewState.IsReviewStale()
	rendered, _ := renderStructuredReview(m.reviewState.Review.Structured, width, m.reviewCursor, stale)
	m.aiReviewRendered = rendered
	m.aiReviewRenderWidth = width
	m.chatViewport.SetContent(rendered)

	// Scroll the review viewport to keep the selected finding visible.
	m.scrollReviewToFinding(m.reviewCursor)

	return nil
}

// scrollReviewToFinding scrolls the chat viewport so the selected finding
// is visible. Scans the rendered content for the "▸" cursor marker.
func (m *Model) scrollReviewToFinding(idx int) {
	if idx < 0 {
		return
	}

	content := m.aiReviewRendered
	if content == "" {
		return
	}

	lines := strings.Split(content, "\n")
	targetLine := -1
	for i, line := range lines {
		if strings.Contains(stripANSI(line), "▸") {
			targetLine = i
			break
		}
	}

	if targetLine < 0 {
		return
	}

	vpHeight := m.chatViewport.Height
	offset := targetLine - vpHeight/3 // show marker in upper third
	if offset < 0 {
		offset = 0
	}
	maxOffset := m.chatViewport.TotalLineCount() - vpHeight
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	m.chatViewport.SetYOffset(offset)
}

func (m *Model) renderChatForFile(filePath string) tea.Cmd {
	if m.reviewState == nil {
		m.chatViewport.SetContent(
			styleTextMuted.Render("No chat history"))
		return nil
	}

	var messages []state.Message

	if filePath == "" {
		messages = m.reviewState.GlobalChat
	} else {
		if fs, ok := m.reviewState.Files[filePath]; ok {
			messages = fs.Chat
		}
	}

	if len(messages) == 0 {
		hint := "Type a message to ask questions"
		if filePath != "" {
			hint += " about this file."
		} else {
			hint += " about this PR."
		}
		m.chatViewport.SetContent(styleTextMuted.Render(hint))
		m.chatViewport.GotoTop()
		return nil
	}

	// Show placeholder immediately, render markdown async
	m.chatViewport.SetContent(
		styleTextMuted.Render("Loading chat..."))

	// Copy what we need for the goroutine
	width := m.chatViewport.Width - 2
	msgsCopy := make([]state.Message, len(messages))
	copy(msgsCopy, messages)
	fp := filePath

	return func() tea.Msg {
		var b strings.Builder
		for _, msg := range msgsCopy {
			switch msg.Role {
			case "user":
				b.WriteString(styleAccentBlueBold.Render("You") + "\n")
				b.WriteString(wrapStyled(styleTextSecondary, msg.Content, width) + "\n\n")
			case "assistant":
				b.WriteString(styleAccentMauveBold.Render("AI") + "\n")
				b.WriteString(renderMarkdown(msg.Content, width) + "\n\n")
			}
		}
		return ChatRenderedMsg{FilePath: fp, Content: b.String(), Tab: 1}
	}
}

func (m Model) renderOverview() string {
	if m.pr == nil {
		return ""
	}

	w := m.diffViewport.Width
	if w < 10 {
		w = 40
	}
	inner := w - 2 // usable width with some margin

	var b strings.Builder

	// ── Title ───────────────────────────────────────────────
	prTitle := fmt.Sprintf("PR #%d: %s", m.pr.Number, m.pr.Title)
	b.WriteString(styleAccentBlueBold.Render(truncateToWidth(prTitle, inner)) + "\n")
	b.WriteString(styleTextSubtle.Render(strings.Repeat("─", inner)) + "\n\n")

	// ── Metadata ────────────────────────────────────────────
	writeField := func(lbl, val string) {
		b.WriteString(styleTextMuted.Render(lbl) + styleTextPrimary.Render(val) + "\n")
	}
	writeField("Base  ", m.pr.BaseRefName)
	writeField("Head  ", m.pr.HeadRefName)
	sha := m.pr.HeadRefOid
	if len(sha) > 8 {
		sha = sha[:8]
	}
	writeField("SHA   ", sha)
	b.WriteString("\n")

	// ── Stats ───────────────────────────────────────────────
	totalAdd, totalDel := 0, 0
	for _, f := range m.pr.Files {
		totalAdd += f.Additions
		totalDel += f.Deletions
	}
	b.WriteString(styleTextMuted.Render(fmt.Sprintf("Files changed: %d   ", len(m.pr.Files))))
	b.WriteString(ftAddClr.Render(fmt.Sprintf("+%d", totalAdd)) + " ")
	b.WriteString(ftDelClr.Render(fmt.Sprintf("-%d", totalDel)) + "\n")

	// ── Description ─────────────────────────────────────────
	if m.pr.Body != "" {
		b.WriteString("\n" + styleTextSubtle.Render(strings.Repeat("─", inner)) + "\n")
		b.WriteString(styleAccentMauveBold.Render("Description") + "\n\n")
		for _, line := range strings.Split(m.pr.Body, "\n") {
			wrapped := wrapText(line, inner)
			for _, wl := range wrapped {
				b.WriteString(styleTextSecondary.Render(wl) + "\n")
			}
		}
	}

	// ── Review comments ─────────────────────────────────────
	totalComments := 0
	for _, comments := range m.comments {
		totalComments += len(comments)
	}
	if totalComments > 0 {
		b.WriteString("\n" + styleTextSubtle.Render(strings.Repeat("─", inner)) + "\n")
		b.WriteString(styleAccentMauveBold.Render(
			fmt.Sprintf("Review Comments (%d)", totalComments)) + "\n\n")

		paths := make([]string, 0, len(m.comments))
		for path := range m.comments {
			paths = append(paths, path)
		}
		sort.Strings(paths)

		for _, path := range paths {
			for _, c := range m.comments[path] {
				header := fmt.Sprintf("%s  %s:%d", c.Author, path, c.Line)
				b.WriteString(styleAccentYellowBold.Render(truncateToWidth(header, inner)) + "\n")
				for _, wl := range wrapText(c.Body, inner-2) {
					b.WriteString("  " + styleTextSecondary.Render(wl) + "\n")
				}
				b.WriteString("\n")
			}
		}
	}

	return b.String()
}

// wrapText splits a string into lines that fit within maxWidth visible columns.
// It wraps at word boundaries when possible.
func wrapText(s string, maxWidth int) []string {
	if maxWidth < 1 {
		maxWidth = 1
	}
	if s == "" {
		return []string{""}
	}

	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}

	var lines []string
	cur := words[0]
	curW := ansi.StringWidth(cur)

	for _, word := range words[1:] {
		ww := ansi.StringWidth(word)
		if curW+1+ww <= maxWidth {
			cur += " " + word
			curW += 1 + ww
		} else {
			lines = append(lines, cur)
			cur = word
			curW = ww
		}
	}
	lines = append(lines, cur)
	return lines
}

func (m Model) reviewedCount() (int, int) {
	if m.reviewState == nil || m.pr == nil {
		return 0, 0
	}
	total := len(m.pr.Files)
	reviewed := 0
	for _, fs := range m.reviewState.Files {
		if fs.Status == state.StatusReviewed {
			reviewed++
		}
	}
	return reviewed, total
}

// ── Layout ──────────────────────────────────────────────────────────────

func (m *Model) syncLayout() {
	if !m.ready {
		return
	}
	cols := m.columns()
	ih := m.contentHeight()

	if cols[0] > 2 {
		m.fileTree.width = cols[0] - 2
		m.fileTree.height = ih - 2
	}

	m.diffViewport.Width = cols[1] - 2
	diffH := ih - 2
	if m.commenting {
		// Make room for comment separator(1) + label(1) + textarea(3) + newlines(2)
		diffH -= 5
		if diffH < 1 {
			diffH = 1
		}
	}
	m.diffViewport.Height = diffH

	if cols[2] > 2 {
		cw := cols[2] - 2
		chatInputH := 3
		// Chat body = viewport(chatVpH) + "\n" + separator(1) + "\n" + input(chatInputH)
		// renderPane clips to contentH = ih - 2 (borders)
		// So chatVpH + 1 + chatInputH = ih - 2 => chatVpH = ih - chatInputH - 3
		chatVpH := ih - chatInputH - 3
		if chatVpH < 1 {
			chatVpH = 1
		}
		m.chatViewport.Width = cw
		m.chatViewport.Height = chatVpH
		m.chatInput.SetWidth(cw)
		m.chatInput.SetHeight(chatInputH)
	}

	// Comment input width matches diff pane
	m.commentInput.SetWidth(cols[1] - 4)
}

func (m *Model) syncFocus() tea.Cmd {
	if m.focusedPane == PaneChat {
		return m.chatInput.Focus()
	}
	m.chatInput.Blur()
	return nil
}

// cyclePane moves focus to the next/previous visible pane.
func (m *Model) cyclePane(dir int) {
	panes := []Pane{PaneDiff} // diff is always visible
	if m.showFilePanel {
		panes = append([]Pane{PaneFileList}, panes...)
	}
	if m.showAIPanel {
		panes = append(panes, PaneChat)
	}
	if len(panes) == 1 {
		m.focusedPane = panes[0]
		return
	}
	cur := 0
	for i, p := range panes {
		if p == m.focusedPane {
			cur = i
			break
		}
	}
	cur = (cur + dir + len(panes)) % len(panes)
	m.focusedPane = panes[cur]
}

func (m Model) columns() [3]int {
	// Count separators (1 space between each visible pane)
	numPanes := 1 // diff always visible
	if m.showFilePanel {
		numPanes++
	}
	if m.showAIPanel {
		numPanes++
	}
	separators := numPanes - 1
	avail := m.width - separators
	if avail < 20 {
		avail = 20
	}

	var l, mid, r int

	switch {
	case !m.showFilePanel && !m.showAIPanel:
		l, r = 0, 0
		mid = avail
	case !m.showFilePanel:
		l = 0
		r = max(22, avail*25/100)
		mid = avail - r
	case !m.showAIPanel:
		need := m.fileTree.maxContentWidth() + 4
		maxL := avail * 40 / 100
		l = max(16, min(need, maxL))
		r = 0
		mid = avail - l
	default:
		need := m.fileTree.maxContentWidth() + 4
		maxL := avail * 30 / 100
		l = max(16, min(need, maxL))
		r = max(22, avail*25/100)
		mid = avail - l - r
	}

	if mid < 12 {
		mid = 12
		// Shrink other panels to compensate
		total := l + mid + r
		if total > avail && r > 0 {
			r = max(12, avail-l-mid)
		}
		total = l + mid + r
		if total > avail && l > 0 {
			l = max(12, avail-mid-r)
		}
	}
	return [3]int{l, mid, r}
}

func (m Model) contentHeight() int {
	// View() output rows: header(2) + panes(h) + footer(2) = h + 4
	// The "\n" joiners between them are separators, not extra rows.
	// For h + 4 = m.height: h = m.height - 4
	h := m.height - 4
	if h < 5 {
		return 5
	}
	return h
}

// renderDiffWithCursor renders the diff viewport with a cursor highlight
// when the diff pane is focused. Also returns the current cursor line number
// to avoid a redundant View()+Split call in the main View().
func (m Model) renderDiffWithCursor() (string, int) {
	raw := m.diffViewport.View()
	if m.focusedPane != PaneDiff || !m.hasFileSelected() {
		return raw, 0
	}

	lines := strings.Split(raw, "\n")

	// Get line number from cursor position
	cursorLineNum := 0
	if m.diffCursor >= 0 && m.diffCursor < len(lines) {
		info := parseDiffLine(lines[m.diffCursor])
		cursorLineNum = info.line
	}

	if m.diffCursor >= 0 && m.diffCursor < len(lines) {
		// Check if this line has a line number (commentable)
		hasLineNum := cursorLineNum > 0

		var highlight lipgloss.Style
		if hasLineNum {
			highlight = styleHighlightCommentable
		} else {
			highlight = styleHighlightNormal
		}
		// Wrap the entire line content with a background style
		line := lines[m.diffCursor]
		w := m.diffViewport.Width

		// Append blame info if blame mode is enabled
		if m.blameEnabled && hasLineNum {
			blameStr := m.formatBlameForLine(cursorLineNum)
			if blameStr != "" {
				blameWidth := ansi.StringWidth(blameStr)
				// Reserve space: 2 char gap + blame text
				maxCode := w - blameWidth - 2
				if maxCode < 20 {
					maxCode = 20 // don't crush the code too much
				}
				// Truncate code portion to make room for blame
				if ansi.StringWidth(line) > maxCode {
					line = ansi.Truncate(line, maxCode, "")
				}
				visible := ansi.StringWidth(line)
				gap := w - visible - blameWidth
				if gap < 2 {
					gap = 2
				}
				line = line + strings.Repeat(" ", gap) + blameStr
			}
		}

		// Pad line to full width so the highlight spans the row
		visible := ansi.StringWidth(line)
		if visible < w {
			line = line + strings.Repeat(" ", w-visible)
		}
		lines[m.diffCursor] = highlight.Render(line)
	}

	return strings.Join(lines, "\n"), cursorLineNum
}

// formatBlameForLine returns a styled blame annotation for a line number,
// e.g. "Juan Potato  2024-01-01". Returns "" if blame data isn't available.
func (m Model) formatBlameForLine(lineNum int) string {
	blame, ok := m.blameCache[m.selectedFile]
	if !ok || blame == nil {
		return styleTextMuted.Render("loading blame...")
	}

	bl, ok := blame[lineNum]
	if !ok {
		return ""
	}

	author := styleAccentPeach.Render(bl.Author)
	date := styleTextMuted.Render(bl.Date)
	return author + "  " + date
}

// ── View ────────────────────────────────────────────────────────────────

func (m Model) View() string {
	if !m.ready {
		return "\n  Loading..."
	}

	if m.loading {
		// Gradient colors for the logo sweep animation (Catppuccin palette)
		logoColors := [5]lipgloss.Color{
			lipgloss.Color("#585B70"), // subtle
			lipgloss.Color("#6C7086"), // muted
			lipgloss.Color("#89B4FA"), // blue (highlight)
			lipgloss.Color("#CBA6F7"), // mauve
			lipgloss.Color("#585B70"), // subtle
		}

		// Render logo with gradient sweep
		// Each rune column gets a color based on (column + frame) mod len(colors)
		var logoRendered []string
		for _, line := range logoLines {
			var b strings.Builder
			runes := []rune(line)
			for i, r := range runes {
				if r == ' ' {
					b.WriteRune(' ')
					continue
				}
				ci := (i/2 + m.logoFrame) % len(logoColors)
				style := lipgloss.NewStyle().Foreground(logoColors[ci])
				b.WriteString(style.Render(string(r)))
			}
			logoRendered = append(logoRendered, b.String())
		}

		spin := m.spinner.View()
		msg := styleTextSecondary.Render(" " + m.loadingMsg)
		statusLine := spin + msg

		// Measure widths for centering
		logoVisualWidth := ansi.StringWidth(logoLines[0])
		statusWidth := ansi.StringWidth(statusLine)
		maxWidth := logoVisualWidth
		if statusWidth > maxWidth {
			maxWidth = statusWidth
		}

		// Build centered content block
		totalLines := len(logoRendered) + 1 + 1 // logo + blank + status
		padTop := 0
		if m.height > totalLines {
			padTop = (m.height - totalLines) / 2
		}

		var content strings.Builder
		content.WriteString(strings.Repeat("\n", padTop))
		for _, line := range logoRendered {
			pad := 0
			if m.width > logoVisualWidth {
				pad = (m.width - logoVisualWidth) / 2
			}
			content.WriteString(strings.Repeat(" ", pad) + line + "\n")
		}
		// Blank line between logo and status
		content.WriteString("\n")
		pad := 0
		if m.width > statusWidth {
			pad = (m.width - statusWidth) / 2
		}
		content.WriteString(strings.Repeat(" ", pad) + statusLine)

		// Pad to full terminal height to prevent flicker on transition
		result := content.String()
		lines := strings.Count(result, "\n") + 1
		if lines < m.height {
			result += strings.Repeat("\n", m.height-lines)
		}
		return result
	}

	// PR picker modal — rendered before panes to avoid nil PR panics
	if m.showPRPicker {
		overlay := centerOverlay(m.renderPRPicker(), m.width, m.height)
		return overlay
	}

	cols := m.columns()
	ih := m.contentHeight()

	header := m.viewHeader()

	diffTitle := "OVERVIEW"
	diffBody, cursorLineNum := m.renderDiffWithCursor()
	if m.viewMode == viewModeActions {
		diffTitle = "ACTIONS"
	} else if m.hasFileSelected() {
		if m.focusedPane == PaneDiff && cursorLineNum > 0 {
			diffTitle = fmt.Sprintf("DIFF (±%d) L%d", m.contextLines, cursorLineNum)
		} else {
			diffTitle = fmt.Sprintf("DIFF (±%d)", m.contextLines)
		}
	}
	if m.commenting {
		diffTitle = fmt.Sprintf("DIFF – Comment on line %d", m.commentLine)
		commentSep := styleTextSubtle.Render(strings.Repeat("─", cols[1]-2))
		commentLabel := styleAccentYellowBold.Render("  New comment (Ctrl+S to submit, Esc to cancel)")
		diffBody = diffBody + "\n" + commentSep + "\n" + commentLabel + "\n" + m.commentInput.View()
	}
	middle := m.renderPane(diffTitle, diffBody, cols[1], ih, m.focusedPane == PaneDiff)

	var paneList []string

	if m.showFilePanel {
		left := m.renderPane("FILES", m.fileTree.View(), cols[0], ih, m.focusedPane == PaneFileList)
		paneList = append(paneList, left, " ")
	}

	paneList = append(paneList, middle)

	if m.showAIPanel {
		cw := cols[2] - 2 // content width inside borders

		// Build chat body — show input only on Chat tab
		var chatBody string
		if m.aiPanelTab == 1 {
			inputLabel := m.renderChatInputLabel(cw)
			chatBody = m.chatViewport.View() + "\n" + inputLabel + "\n" + m.chatInput.View()
		} else {
			chatBody = m.chatViewport.View()
		}

		// Build title with tab indicators
		chatTitle := m.renderAIPanelTitle(cw)
		right := m.renderPane(chatTitle, chatBody, cols[2], ih, m.focusedPane == PaneChat)
		paneList = append(paneList, " ", right)
	}

	panes := joinPanesHorizontal(paneList...)

	footer := m.viewFooter()

	base := header + "\n" + panes + "\n" + footer

	// ── Modal overlays ──────────────────────────────────────────
	if m.showHelp {
		overlay := centerOverlay(m.renderHelpModal(), m.width, m.height)
		return overlay
	}
	if m.showModelPicker {
		overlay := centerOverlay(m.renderModelPicker(), m.width, m.height)
		return overlay
	}
	if m.showSubmitReview {
		overlay := centerOverlay(m.renderSubmitReviewModal(), m.width, m.height)
		return overlay
	}
	if m.showThemePicker {
		return floatOverlay(base, m.renderThemePicker(), m.width, m.height)
	}
	if m.errorMsg != "" {
		overlay := centerOverlay(m.renderErrorModal(), m.width, m.height)
		return overlay
	}

	return base
}

// ── Bordered pane rendering ─────────────────────────────────────────────

// joinPanesHorizontal joins pre-rendered, fixed-width pane strings side by side.
// Unlike lipgloss.JoinHorizontal, this skips most width measurement since
// renderPane already guarantees each line is padded to exact width.
// Blocks shorter than the tallest are padded with spaces to prevent misalignment.
func joinPanesHorizontal(panes ...string) string {
	if len(panes) == 0 {
		return ""
	}
	if len(panes) == 1 {
		return panes[0]
	}

	blocks := make([][]string, len(panes))
	widths := make([]int, len(panes))
	maxH := 0
	for i, p := range panes {
		blocks[i] = strings.Split(p, "\n")
		if len(blocks[i]) > maxH {
			maxH = len(blocks[i])
		}
		// Measure width from first line; renderPane guarantees uniform width.
		if len(blocks[i]) > 0 {
			widths[i] = ansi.StringWidth(blocks[i][0])
		}
	}

	var b strings.Builder
	b.Grow(maxH * 200) // rough estimate
	for row := 0; row < maxH; row++ {
		for i, block := range blocks {
			if row < len(block) {
				b.WriteString(block[row])
			} else if widths[i] > 0 {
				// Pad missing rows to maintain alignment
				b.WriteString(strings.Repeat(" ", widths[i]))
			}
		}
		if row < maxH-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m Model) renderPane(title, content string, width, height int, focused bool) string {
	if width < 4 {
		return ""
	}

	var borderSt lipgloss.Style
	var tStyle lipgloss.Style

	if focused {
		borderSt = borderStyleFocused
		tStyle = titleFocusedStyle
	} else {
		borderSt = borderStyleUnfocused
		tStyle = titleStyle
	}

	bdr := lipgloss.RoundedBorder()

	// Build top border with inset title
	titleLabel := tStyle.Render(" " + title + " ")
	titleW := ansi.StringWidth(titleLabel)
	topLeft := borderSt.Render(bdr.TopLeft)
	topRight := borderSt.Render(bdr.TopRight)
	// 2 chars for corners, 1 char gap before title
	barBefore := borderSt.Render(strings.Repeat(bdr.Top, 2))
	remaining := width - 2 - 2 - titleW // corners(2) + barBefore(2) + title
	if remaining < 0 {
		remaining = 0
	}
	barAfter := borderSt.Render(strings.Repeat(bdr.Top, remaining))
	topLine := topLeft + barBefore + titleLabel + barAfter + topRight

	// Build content area with side borders
	contentW := width - 2 // subtract left + right border chars
	if contentW < 0 {
		contentW = 0
	}
	contentH := height - 2 // subtract top + bottom border lines
	if contentH < 0 {
		contentH = 0
	}

	// Render content lines padded/clipped to contentW
	contentLines := strings.Split(content, "\n")
	left := borderSt.Render(bdr.Left)
	right := borderSt.Render(bdr.Right)

	var body strings.Builder
	body.Grow(contentH * (contentW + 20)) // pre-allocate to reduce allocations
	for i := 0; i < contentH; i++ {
		line := ""
		if i < len(contentLines) {
			line = contentLines[i]
		}
		vis := ansi.StringWidth(line)
		if vis > contentW {
			// Truncate wide lines to fit
			line = truncateToWidth(line, contentW)
			vis = ansi.StringWidth(line)
		}
		if vis < contentW {
			line = line + strings.Repeat(" ", contentW-vis)
		}
		body.WriteString(left + line + right + "\n")
	}

	// Build bottom border
	bottomLeft := borderSt.Render(bdr.BottomLeft)
	bottomRight := borderSt.Render(bdr.BottomRight)
	bottomBar := borderSt.Render(strings.Repeat(bdr.Bottom, width-2))
	bottomLine := bottomLeft + bottomBar + bottomRight

	return topLine + "\n" + body.String() + bottomLine
}

// ── Header ──────────────────────────────────────────────────────────────

func (m Model) viewHeader() string {
	logo := styleAccentBlueBold.Render("prr")

	// Repository name right after logo (owner/repo, skip github.com)
	repoLabel := ""
	if m.pr != nil && m.pr.HeadRepository.Owner.Login != "" && m.pr.HeadRepository.Name != "" {
		repo := m.pr.HeadRepository.Owner.Login + "/" + m.pr.HeadRepository.Name
		repoLabel = styleTextSubtle.Render(" · ") + styleTextSecondary.Render(repo)
	}

	prInfo := styleTextPrimary.Render(fmt.Sprintf(" · PR #%s", m.prNumber))

	// PR metadata: author, state
	var meta string
	if m.pr != nil {
		var parts []string

		// Author
		if m.pr.Author.Login != "" {
			parts = append(parts, styleTextMuted.Render("by ")+styleTextSecondary.Render(m.pr.Author.Login))
		}

		// State badge with color
		if m.pr.State != "" {
			state := strings.ToUpper(m.pr.State)
			var stateStyle lipgloss.Style
			switch strings.ToLower(m.pr.State) {
			case "open":
				stateStyle = styleAccentGreen
			case "merged":
				stateStyle = lipgloss.NewStyle().Foreground(accentMauve)
			case "closed":
				stateStyle = styleAccentRed
			default:
				stateStyle = styleTextMuted
			}
			parts = append(parts, stateStyle.Render(state))
		}

		// Review decision
		if m.pr.ReviewDecision != "" {
			var decisionStyle lipgloss.Style
			var label string
			switch strings.ToUpper(m.pr.ReviewDecision) {
			case "APPROVED":
				decisionStyle = styleAccentGreen
				label = "✓ Approved"
			case "CHANGES_REQUESTED":
				decisionStyle = styleAccentRed
				label = "✗ Changes requested"
			case "REVIEW_REQUIRED":
				decisionStyle = styleAccentYellow
				label = "● Review required"
			default:
				decisionStyle = styleTextMuted
				label = m.pr.ReviewDecision
			}
			parts = append(parts, decisionStyle.Render(label))
		}

		if len(parts) > 0 {
			sep := styleTextSubtle.Render(" · ")
			meta = " " + sep + strings.Join(parts, sep)
		}
	}

	reviewed, total := m.reviewedCount()
	var reviewBadge string
	if total > 0 {
		badgeStyle := styleAccentYellow
		if reviewed == total {
			badgeStyle = styleAccentGreen
		}
		reviewBadge = badgeStyle.Render(fmt.Sprintf("● %d/%d reviewed", reviewed, total))
	}

	// Model name badge
	var modelBadge string
	if m.aiModelName != "" {
		modelBadge = styleTextMuted.Render(m.aiModelName)
	}

	// Calculate how much room we have for the PR title
	var rightParts []string
	if reviewBadge != "" {
		rightParts = append(rightParts, reviewBadge)
	}
	if modelBadge != "" {
		rightParts = append(rightParts, modelBadge)
	}
	right := strings.Join(rightParts, styleTextSubtle.Render(" · ")) + " "
	fixedW := ansi.StringWidth(" "+logo+repoLabel+prInfo+meta) + ansi.StringWidth(right) + 2 // 2 for min gap
	maxTitleW := m.width - fixedW

	prTitle := ""
	if m.pr != nil && maxTitleW > 4 {
		t := m.pr.Title
		titleRunes := []rune(t)
		if len(titleRunes) > maxTitleW-3 { // -3 for " · " prefix
			titleRunes = titleRunes[:maxTitleW-6]
			t = string(titleRunes) + "..."
		}
		prTitle = styleTextSecondary.Render(fmt.Sprintf(" · %s", t))
	}

	left := " " + logo + repoLabel + prInfo + prTitle + meta
	gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		gap = 1
	}

	separator := styleTextSubtle.Render(strings.Repeat("─", m.width))

	headerLine := left + strings.Repeat(" ", gap) + right
	// Truncate to terminal width to prevent wrapping
	if m.width > 0 && ansi.StringWidth(headerLine) > m.width {
		headerLine = truncateToWidth(headerLine, m.width)
	}

	return headerLine + "\n" + separator
}

// ── Footer ──────────────────────────────────────────────────────────────

func (m Model) viewFooter() string {
	separator := styleTextSubtle.Render(strings.Repeat("─", m.width))

	// Minimal footer — context-sensitive essentials + ? for full help
	bindings := []struct{ key, desc string }{}

	switch m.focusedPane {
	case PaneFileList:
		bindings = append(bindings,
			struct{ key, desc string }{"j/k", "navigate"},
			struct{ key, desc string }{"Enter", "select"},
			struct{ key, desc string }{"Space", "reviewed"},
		)
	case PaneDiff:
		bindings = append(bindings,
			struct{ key, desc string }{"j/k", "select line"},
			struct{ key, desc string }{"c", "comment"},
		)
	case PaneChat:
		if m.aiPanelTab == 0 {
			bindings = append(bindings,
				struct{ key, desc string }{"j/k", "findings"},
				struct{ key, desc string }{"Enter", "jump"},
			)
		} else {
			bindings = append(bindings,
				struct{ key, desc string }{"Enter", "send"},
			)
		}
	}

	if m.aiStreaming {
		bindings = append(bindings,
			struct{ key, desc string }{"Esc", "cancel AI"},
		)
	} else {
		bindings = append(bindings,
			struct{ key, desc string }{"a", "review"},
		)
	}

	bindings = append(bindings,
		struct{ key, desc string }{"?", "help"},
		struct{ key, desc string }{"q", "quit"},
	)

	var parts []string
	for _, b := range bindings {
		k := footerKeyStyle.Render(b.key)
		d := footerDescStyle.Render(" " + b.desc)
		parts = append(parts, k+d)
	}
	sep := footerSepStyle.Render("  │  ")
	line := " " + strings.Join(parts, sep)

	// Truncate to terminal width to prevent wrapping (which adds lines and causes flicker)
	if m.width > 0 && ansi.StringWidth(line) > m.width {
		line = truncateToWidth(line, m.width)
	}

	return separator + "\n" + line
}

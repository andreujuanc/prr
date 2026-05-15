package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/opencode"
	"github.com/andreujuanc/prr/internal/pipe"
	"github.com/andreujuanc/prr/internal/review"
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
	// DeepFindings from AOI-driven review calls (structured findings with severity, category, etc.)
	DeepFindings []state.DeepFinding
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

// AIReviewAOIMsg signals the AOI pre-scan phase with status updates.
type AIReviewAOIMsg struct {
	Status string // status text to display
	Done   bool   // true when AOI scan is complete
	AOIs   int    // number of AOIs found (set when Done=true)
}

// AIReviewPhaseMsg signals a status update for one of the pre-batch or
// post-batch phases that don't have a dedicated message type. The Phase
// field carries the pipeline phase key ("discovery", "classify",
// "recheck") so the bubbletea handler can route to the right phase row
// in reviewProgress.
//
// Done=true means the phase has finished; Done=false means it's the
// currently active status (last-write-wins for the detail line).
type AIReviewPhaseMsg struct {
	Phase  string // pipeline phase key: "discovery", "classify", "recheck"
	Status string // status text to display as the active detail
	Done   bool
}

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

// flashDismissMsg is sent after the flash message timeout expires.
type flashDismissMsg struct{}

// FindingPublishedMsg is sent after a single finding is posted as a GitHub line comment.
type FindingPublishedMsg struct {
	Finding state.ReviewFinding
	Comment *git.ReviewComment
	Err     error
}

// FindingsBatchPublishedMsg is sent after all findings are posted as a GitHub Review.
type FindingsBatchPublishedMsg struct {
	Count int
	Err   error
}

// PRCommentPostedMsg is sent after a finding is posted as a general PR comment.
type PRCommentPostedMsg struct {
	Finding state.ReviewFinding
	Err     error
}

// opencodeReadyMsg is sent after the OpenCode server has started,
// so the task can be spawned.
type opencodeReadyMsg struct {
	finding state.ReviewFinding
}

// PipeResultMsg is sent when an external pipe process completes.
type PipeResultMsg struct {
	Finding state.ReviewFinding
	Target  pipe.Target
	Output  []byte
	Err     error
}

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
	viewModeOverview   viewMode = iota // PR overview (default)
	viewModeFile                       // file diff
	viewModeActions                    // GitHub Actions status
	viewModeTaskOutput                 // background task output
)

// AI panel tab indices. The panel cycles through these via Tab / Shift+Tab.
// Review and Chat live in separate viewports — see reviewViewport and
// chatViewport. Tasks renders inline (no viewport).
const (
	tabReview = 0
	tabTasks  = 1
	tabChat   = 2
)

type Model struct {
	fileTree fileTree
	// diffViewport: middle pane (diff display).
	diffViewport viewport.Model
	// reviewViewport: AI panel, Review tab — holds streamed review
	// output during a run and the rendered final review after.
	reviewViewport viewport.Model
	// chatViewport: AI panel, Chat tab — holds chat transcript and
	// chat-stream content. Kept separate from reviewViewport so chat
	// history isn't clobbered by a review stream and vice versa.
	chatViewport viewport.Model
	chatInput    textarea.Model
	spinner      spinner.Model

	focusedPane Pane
	prNumber    string
	width       int
	height      int
	ready       bool

	// Width budgets per pane — computed once in syncLayout. Every call
	// site that previously did its own "viewport.Width - N" math should
	// read from these instead. See widthBudget and computeLayoutWidths.
	filesWidths widthBudget
	diffWidths  widthBudget
	aiWidths    widthBudget

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
	contextLines    int // number of context lines for git diff (-U<n>)
	aoiContextLines int // context lines for AOI security pre-scan diffs (default 10)

	// Files skipped from AI review (binary, generated, large)
	skippedFiles map[string]git.SkipReason

	// AI
	aiClient           ai.Client
	aoiClient          ai.Client // optional: lightweight model for AOI security pre-scan
	aiModelName        string    // model identifier for display (e.g. "gemini-3.1-pro-preview")
	aoiModelName       string    // AOI model identifier for display
	aiStreaming        bool      // true while AI is generating
	aiStreamBuffer     string    // accumulated streamed response
	aiStreamDirty      bool      // true when buffer has unflushed tokens
	aiCancelFn         context.CancelFunc
	aiChatHistoryCache string                // pre-rendered markdown of completed messages (for streaming perf)
	aiReviewBatches    []AIReviewBatchInfo   // batch list for in-place rendering
	aiReviewStatuses   []AIReviewBatchStatus // per-batch status
	aiReviewPhase      string                // "batch" or "synthesis"

	// reviewProgress is the single source of truth for the in-progress
	// phase view. Replaces the unbounded batch list that previously
	// dominated the Review tab during a run.
	reviewProgress      reviewPhaseTracker
	deepFindings        []state.DeepFinding // structured findings from AOI-driven review
	aiPanelTab          int                 // 0 = Review, 1 = Tasks, 2 = Chat
	aiReviewRendered    string              // cached rendered review markdown
	aiReviewRenderWidth int                 // width used for cached render

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
	reviewFindings     []state.ReviewFinding // flat ordered list of findings (severity-sorted, matching render order)
	reviewCursor       int                   // currently highlighted finding index (-1 = none)
	findingsExpanded   map[int]bool          // per-finding expansion state, keyed by index into reviewFindings; nil/missing = collapsed
	pendingScrollLine  int                   // line to scroll to after diff loads (0 = none)
	cameFromFinding    bool                  // true when diff was opened via finding jump (Esc returns to review)
	diffContent        string                // cached diff content for line scanning (set on StyledDiffMsg)
	rawDiffContent     string                // styled diff before comment/finding injection (for toggle re-render)
	showInlineFindings bool                  // when true, findings are shown inline in the diff

	// Panel visibility
	showFilePanel bool
	showAIPanel   bool

	// Modal overlays
	showHelp           bool   // help modal visible
	showModelPicker    bool   // model picker visible
	modelPickerCursor  int    // selected index in model picker
	modelPickerSection int    // 0 = review models, 1 = AOI models
	showSubmitReview   bool   // submit review confirmation visible
	submitReviewCursor int    // 0 = Submit, 1 = Cancel
	showThemePicker    bool   // theme picker visible
	themePickerCursor  int    // selected index in theme picker
	themeBeforePicker  string // theme ID before opening picker (for revert on Esc)
	errorMsg           string // transient error shown as modal overlay

	// Finding publish/pipe overlays (no background dimming)
	confirmOverlay    *confirmModal // non-nil when confirm modal is active
	actionMenuOverlay *actionMenu   // non-nil when action menu is active
	pipeTargets       []pipe.Target // loaded from config
	repoRoot          string        // git repo root (resolved once at init)
	flashMsg          string        // brief status flash (auto-clears on next key)

	// Background tasks (Fix with OpenCode)
	tasks         []*Task           // all tasks (running, completed, failed, cancelled)
	taskCursor    int               // selected task in Tasks tab
	taskNextID    int               // auto-incrementing task ID
	viewingTaskID int               // task ID currently shown in diff pane (-1 = none)
	opencodeMgr   *opencode.Manager // manages OpenCode server + SSE stream

	// Permission/question overlays (from background tasks)
	permissionOverlay *permissionModal // non-nil when permission approval needed
	questionOverlay   *questionModal   // non-nil when question needs answering

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
	actionsRuns     []git.WorkflowRun         // workflow runs for the PR
	actionsLoading  bool                      // true while fetching runs
	actionsPolling  bool                      // true when auto-polling active runs
	actionsExpanded map[int][]git.WorkflowJob // runID -> expanded jobs (nil = collapsed)
	actionsCursor   int                       // cursor position in actions view
}

// ── Constructor ─────────────────────────────────────────────────────────

func NewModel(prNumber string, aiClient ai.Client, aoiClient ai.Client, parallelReviews int, aoiContextLines int, useChroma bool) Model {
	diffVp := viewport.New(0, 0)
	diffVp.Style = lipgloss.NewStyle().Foreground(textPrimary)

	chatVp := viewport.New(0, 0)
	reviewVp := viewport.New(0, 0)

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

	var aoiModelDisplayName string
	if aoiClient != nil {
		if mi, ok := aoiClient.(ai.ModelInfo); ok {
			aoiModelDisplayName = mi.ModelName()
		}
	}

	repoRoot := resolveRepoRoot()

	m := Model{
		fileTree:           newFileTree(nil),
		diffViewport:       diffVp,
		reviewViewport:     reviewVp,
		chatViewport:       chatVp,
		chatInput:          ta,
		spinner:            s,
		focusedPane:        PaneFileList,
		prNumber:           prNumber,
		loading:            true,
		loadingMsg:         "Fetching PR data...",
		aiClient:           aiClient,
		aoiClient:          aoiClient,
		aiModelName:        modelName,
		aoiModelName:       aoiModelDisplayName,
		contextLines:       3,
		aoiContextLines:    aoiContextLines,
		comments:           make(map[string][]git.ReviewComment),
		commentInput:       commentTa,
		showFilePanel:      true,
		showAIPanel:        true,
		customInstructions: config.LoadCustomInstructions(),
		pipeTargets:        config.LoadPipeTargets(),
		repoRoot:           repoRoot,
		opencodeMgr:        opencode.NewManager(repoRoot),
		viewingTaskID:      -1,
		parallelReviews:    parallelReviews,
		reviewCursor:       -1,
		showInlineFindings: true,
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

	// Store manager ref for shutdown cleanup
	opencodeMgrLock.Lock()
	opencodeMgrRef = m.opencodeMgr
	opencodeMgrLock.Unlock()

	return m
}

// resolveRepoRoot returns the git repo root, falling back to "." on error.
func resolveRepoRoot() string {
	root, err := git.RepoRoot()
	if err != nil {
		return "."
	}
	return root
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

// ── Finding publish/pipe commands ───────────────────────────────────────

func publishFindingAsComment(prNumber, commitSHA string, f state.ReviewFinding) tea.Cmd {
	return func() tea.Msg {
		body := formatFindingAsMarkdown(f)
		comment, err := git.CreateReviewComment(prNumber, commitSHA, f.File, body, f.Line, "RIGHT")
		return FindingPublishedMsg{Finding: f, Comment: comment, Err: err}
	}
}

func publishBatchReview(prNumber, commitSHA string, findings []state.ReviewFinding) tea.Cmd {
	return func() tea.Msg {
		comments := make([]git.ReviewFindingComment, 0, len(findings))
		for _, f := range findings {
			if f.File != "" && f.Line > 0 {
				comments = append(comments, git.ReviewFindingComment{
					Path: f.File,
					Line: f.Line,
					Body: formatFindingAsMarkdown(f),
					Side: "RIGHT",
				})
			}
		}
		body := formatBatchReviewBody(findings)
		err := git.SubmitReviewWithFindings(prNumber, commitSHA, body, comments)
		return FindingsBatchPublishedMsg{Count: len(comments), Err: err}
	}
}

func postFindingAsPRComment(prNumber string, f state.ReviewFinding) tea.Cmd {
	return func() tea.Msg {
		body := formatFindingAsMarkdown(f)
		err := git.PostPRComment(prNumber, body)
		return PRCommentPostedMsg{Finding: f, Err: err}
	}
}

func executePipe(target pipe.Target, f state.ReviewFinding, repoRoot string) tea.Cmd {
	return func() tea.Msg {
		payload := pipe.Payload{
			File:       f.File,
			Line:       f.Line,
			Severity:   f.Severity,
			Category:   f.Category,
			Title:      f.Title,
			Detail:     f.Detail,
			Suggestion: f.Suggestion,
			RepoRoot:   repoRoot,
		}
		output, err := pipe.Execute(target, payload)
		return PipeResultMsg{Finding: f, Target: target, Output: output, Err: err}
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

// streamAIChat runs a single ChatStream call as a tea.Cmd, streaming
// tokens back via tea.Msg. watchdogTap (nullable) is called on every
// streamed token so an ai.IdleWatch associated with ctx can detect
// stalls. stopWatchdog (nullable) is invoked when the stream returns to
// release the watchdog goroutine.
func streamAIChat(client ai.Client, ctx context.Context, systemPrompt string, messages []ai.Message, p *tea.Program, watchdogTap func(string), stopWatchdog func()) tea.Cmd {
	return func() tea.Msg {
		if stopWatchdog != nil {
			defer stopWatchdog()
		}
		fullResponse, err := client.ChatStream(ctx, systemPrompt, messages, func(token string) {
			if watchdogTap != nil {
				watchdogTap(token)
			}
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

// opencodeMgrRef holds a reference to the active manager for shutdown.
var (
	opencodeMgrRef  *opencode.Manager
	opencodeMgrLock sync.Mutex
)

// Shutdown cleans up resources (stops the OpenCode server, etc.).
// Call after the bubbletea program exits.
func Shutdown() {
	opencodeMgrLock.Lock()
	mgr := opencodeMgrRef
	opencodeMgrRef = nil
	opencodeMgrLock.Unlock()

	if mgr != nil {
		mgr.Stop()
	}
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
			// Refresh the review-progress view on every tick so the
			// active phase's spinner glyph and the "elapsed" timer
			// advance smoothly between AIReview* events (which may be
			// seconds apart during e.g. AOI pre-scan).
			if m.reviewProgress.IsActive() && !m.hasFileSelected() {
				m.updateChatViewWithStream()
			}
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
		cmds = append(cmds, m.syncLayoutWithRerender())
		return m, tea.Batch(cmds...)

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
			// State file corruption is a recoverable user-facing
			// problem: route to a confirm modal instead of leaving
			// the user with a generic "Error syncing state" string
			// they can't act on.
			var ce *state.CorruptStateError
			if errors.As(msg.Err, &ce) {
				m.confirmOverlay = &confirmModal{
					action:    confirmDeleteCorruptState,
					statePath: ce.Path,
					stateErr:  ce.Cause,
				}
				return m, nil
			}
			m.setDiffContent(
				styleAccentRed.Render(
					fmt.Sprintf("Error syncing state: %v", msg.Err)))
			m.populateFileList(nil)
			return m, nil
		}
		m.reviewState = msg.State
		m.rawDiffs = msg.RawDiffs
		m.skippedFiles = msg.SkippedFiles
		// Visibility for cache hits: when state arrives with a cached
		// review, log a summary line. Helps the user confirm that
		// re-opening prr restored their previous review instead of
		// silently starting fresh — which would imply they need to
		// pay for another review.
		if r := msg.State.Review; r != nil {
			verdict := "comment"
			findingCount := 0
			if r.Structured != nil {
				if r.Structured.Verdict != "" {
					verdict = r.Structured.Verdict
				}
				findingCount = len(r.Structured.Findings)
			}
			log.Printf("Loaded cached review: verdict=%s, findings=%d", verdict, findingCount)
		}
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
		// If the loaded state already has a review (cached from a
		// previous session — synthesized Review or persisted
		// DeepFindings), open the Review tab so the user sees the
		// previous findings on PR reopen instead of an empty Chat tab.
		if m.hasReview() {
			m.aiPanelTab = tabReview
		}
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

	case FindingPublishedMsg:
		if msg.Err != nil {
			cmds = append(cmds, m.setFlash(fmt.Sprintf("Error: %v", msg.Err)))
		} else {
			cmds = append(cmds, m.setFlash(fmt.Sprintf("Comment posted on %s:%d", msg.Finding.File, msg.Finding.Line)))
			// Add to local comments state
			if msg.Comment != nil {
				m.comments[msg.Comment.Path] = append(m.comments[msg.Comment.Path], *msg.Comment)
				if m.hasFileSelected() && m.selectedFile == msg.Comment.Path {
					cmd = m.reloadDiff()
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			}
		}
		return m, tea.Batch(cmds...)

	case FindingsBatchPublishedMsg:
		if msg.Err != nil {
			return m, m.setFlash(fmt.Sprintf("Error submitting review: %v", msg.Err))
		}
		return m, m.setFlash(fmt.Sprintf("Review submitted with %d comments", msg.Count))

	case PRCommentPostedMsg:
		if msg.Err != nil {
			return m, m.setFlash(fmt.Sprintf("Error posting comment: %v", msg.Err))
		}
		return m, m.setFlash(fmt.Sprintf("PR comment posted for %s:%d", msg.Finding.File, msg.Finding.Line))

	case PipeResultMsg:
		if msg.Err != nil {
			return m, m.setFlash(fmt.Sprintf("Pipe \"%s\" failed: %v", msg.Target.Name, msg.Err))
		}
		output := strings.TrimSpace(string(msg.Output))
		if output == "" {
			return m, m.setFlash(fmt.Sprintf("Pipe \"%s\" completed", msg.Target.Name))
		} else if len(output) < 200 {
			return m, m.setFlash(fmt.Sprintf("Pipe \"%s\": %s", msg.Target.Name, output))
		}
		// Show output in error modal (scrollable)
		m.errorMsg = fmt.Sprintf("Output from \"%s\":\n\n%s", msg.Target.Name, output)
		return m, nil

	case flashDismissMsg:
		m.flashMsg = ""
		return m, nil

	case opencodeReadyMsg:
		// Server is now running — spawn the task
		return m, m.spawnFixTask(msg.finding)

	case TaskSpawnedMsg:
		// Task started — nothing extra to do (already tracked in m.tasks)
		return m, nil

	case TaskPermissionMsg:
		// A task needs permission approval — show the modal
		m.permissionOverlay = &permissionModal{
			taskID:     msg.ID,
			permission: msg.Permission,
			cursor:     0, // default to Approve
		}
		return m, nil

	case TaskQuestionMsg:
		// A task needs the user to answer a question — show the modal
		m.questionOverlay = &questionModal{
			taskID:   msg.ID,
			question: msg.Question,
			cursor:   0,
		}
		return m, nil

	case TaskOutputMsg:
		// Streaming output from a background task
		// If we're currently viewing this task's output, refresh the diff pane
		if m.viewMode == viewModeTaskOutput && m.viewingTaskID == msg.ID {
			content := m.renderTaskOutput(msg.ID)
			m.setDiffContent(content)
			// Auto-scroll to bottom
			total := m.diffViewport.TotalLineCount()
			vpH := m.diffViewport.Height
			if total > vpH {
				m.diffViewport.SetYOffset(total - vpH)
			}
		}
		return m, nil

	case TaskDoneMsg:
		// Task finished — find it and update
		for i, t := range m.tasks {
			if t.ID == msg.ID {
				// Auto-resolve the finding if task completed successfully
				if t.GetStatus() == TaskCompleted {
					if t.FindingIdx >= 0 && t.FindingIdx < len(m.reviewFindings) {
						m.reviewFindings[t.FindingIdx].Resolved = true
						// Re-render review if on Review tab
						if m.aiPanelTab == tabReview {
							cmds = append(cmds, m.rerenderReviewWithCursor())
						}
					}
				}
				// Refresh diff pane if viewing this task
				if m.viewMode == viewModeTaskOutput && m.viewingTaskID == msg.ID {
					content := m.renderTaskOutput(msg.ID)
					m.setDiffContent(content)
				}
				_ = i
				break
			}
		}
		// Stop the OpenCode server if no tasks are still running
		if !hasAnyRunningTask(m.tasks) && m.opencodeMgr != nil {
			mgr := m.opencodeMgr
			go func() {
				mgr.Stop()
			}()
		}
		return m, tea.Batch(cmds...)

	case ChatRenderedMsg:
		// Review tab (0) content is global (not per-file) and lands in
		// reviewViewport. Chat tab (2) content is per-file and lands in
		// chatViewport — only apply if still on the same file.
		if msg.Tab == tabReview {
			m.aiReviewRendered = msg.Content
			m.aiReviewRenderWidth = m.reviewViewport.Width - 2
			if m.aiPanelTab == tabReview {
				m.reviewViewport.SetContent(msg.Content)
				m.reviewViewport.GotoTop()
			}
		} else if msg.Tab == m.aiPanelTab && msg.FilePath == m.selectedFile {
			m.chatViewport.SetContent(msg.Content)
			m.chatViewport.GotoTop()
		}
		return m, nil

	case ReviewRenderedMsg:
		// Structured review rendered — store findings and cache content
		// against reviewViewport's width. A fresh review means the
		// expansion state for the previous one is irrelevant (indices
		// likely don't correspond to the same finding anymore).
		m.reviewFindings = msg.Findings
		m.findingsExpanded = nil
		m.aiReviewRendered = msg.Content
		m.aiReviewRenderWidth = m.reviewViewport.Width - 2
		if m.aiPanelTab == tabReview {
			m.reviewViewport.SetContent(msg.Content)
			m.reviewViewport.GotoTop()
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
				// Cache pre-findings content for toggle re-render
				m.rawDiffContent = content
				content = m.injectFindings(content, msg.FilePath)
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
		// Ensure tracker is initialised. InitBatches can arrive before
		// (or after) the first PhaseProgress; Start is idempotent if
		// we guard on IsActive.
		if !m.reviewProgress.IsActive() {
			m.reviewProgress.Start(defaultReviewPhases())
		}
		m.reviewProgress.SetCounter("batches_total", len(msg.Batches))
		m.reviewProgress.Activate("phase1")
		m.updateChatViewWithStream()
		return m, nil

	case AIReviewPhaseMsg:
		if !m.reviewProgress.IsActive() {
			m.reviewProgress.Start(defaultReviewPhases())
		}
		m.reviewProgress.Activate(msg.Phase)
		if msg.Done {
			m.reviewProgress.Complete(msg.Phase)
		} else if msg.Status != "" {
			m.reviewProgress.SetDetail(msg.Phase, msg.Status)
		}
		m.updateChatViewWithStream()
		return m, nil

	case AIReviewAOIMsg:
		m.aiReviewPhase = "aoi"
		if !m.reviewProgress.IsActive() {
			m.reviewProgress.Start(defaultReviewPhases())
		}
		m.reviewProgress.Activate("aoi")
		if msg.Done {
			m.reviewProgress.SetCounter("aoi_total", msg.AOIs)
			m.reviewProgress.SetCounter("aoi_scanned", msg.AOIs)
			m.reviewProgress.Complete("aoi")
			if msg.AOIs > 0 {
				m.aiStreamBuffer += fmt.Sprintf("\n%s %s\n",
					checkMark, msg.Status)
			} else {
				m.aiStreamBuffer += fmt.Sprintf("\n%s\n", msg.Status)
			}
		} else {
			m.reviewProgress.SetDetail("aoi", msg.Status)
			// Update in-progress status (legacy buffer kept for tests
			// that read it directly; the tracker drives the new view).
			m.aiStreamBuffer = msg.Status + "\n"
		}
		m.updateChatViewWithStream()
		return m, nil

	case AIReviewProgressMsg:
		if msg.Batch >= 0 && msg.Batch < len(m.aiReviewStatuses) {
			m.aiReviewStatuses[msg.Batch] = msg.Status
		}
		if m.reviewProgress.IsActive() {
			done := 0
			for _, s := range m.aiReviewStatuses {
				if s == BatchDone || s == BatchCached || s == BatchFailed {
					done++
				}
			}
			m.reviewProgress.SetCounter("batches_done", done)
			// The active-phase detail surfaces the currently running
			// batch label, collapsing the old N-line batch list into
			// one cell.
			if msg.Status == BatchActive && msg.Batch >= 0 &&
				msg.Batch < len(m.aiReviewBatches) {
				m.reviewProgress.SetDetail("phase1",
					m.aiReviewBatches[msg.Batch].Label)
			}
		}
		m.updateChatViewWithStream()
		return m, nil

	case AIReviewSynthesisMsg:
		m.aiReviewPhase = "synthesis"
		if m.reviewProgress.IsActive() {
			m.reviewProgress.Activate("phase2")
		}
		m.updateChatViewWithStream()
		return m, nil

	case AIChatDeltaMsg:
		if m.aiStreaming {
			token := msg.Token
			if after, ok := strings.CutPrefix(token, "\x00THOUGHT:"); ok {
				// Thought text — render each line individually to prevent
				// viewport word-wrapping from breaking ANSI escape codes
				thought := after
				for line := range strings.SplitSeq(thought, "\n") {
					m.aiStreamBuffer += styleThought.Render(line) + "\n"
				}
			} else if after, ok := strings.CutPrefix(token, "\x00TOOL_START:"); ok {
				// Tool execution starting — show name and args
				tool := after
				prefix := "  ▸ "
				maxLen := max(m.width-6, 20)
				if len(tool) > maxLen {
					tool = "…" + tool[len(tool)-(maxLen-1):]
				}
				m.aiStreamBuffer += "\n" + styleToolCall.Render(prefix+tool+" …") + "\n"
			} else if after, ok := strings.CutPrefix(token, "\x00TOOL_DONE:"); ok {
				// Tool execution finished — show status and duration
				// Format: name|status|duration
				parts := strings.SplitN(after, "|", 3)
				if len(parts) == 3 {
					name, status, dur := parts[0], parts[1], parts[2]
					indicator := "  ✓ "
					if status == "error" {
						indicator = "  ✗ "
					}
					line := fmt.Sprintf("%s%s (%s)", indicator, name, dur)
					maxLen := max(m.width-6, 20)
					if len(line) > maxLen {
						line = line[:maxLen-1] + "…"
					}
					m.aiStreamBuffer += styleToolCall.Render(line) + "\n"
				}
			} else if after, ok := strings.CutPrefix(token, "\x00TOOL:"); ok {
				// Legacy tool call indicator (backward compat)
				tool := after
				prefix := "  ▸ "
				maxLen := max(m.width-6, 20)
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
		// AIChatDoneMsg is shared between chat completion and review
		// completion. We disambiguate via reviewProgress.IsActive(),
		// which is set true by triggerAIReview's Start call at the
		// beginning of a PR review and stays true through every event
		// until this handler runs. The tracker is the single source of
		// truth for "was this run a review run".
		wasReview := m.reviewProgress.IsActive()

		m.aiStreaming = false
		m.aiCancelFn = nil
		m.aiStreamDirty = false // prevent stale tick from rendering after completion
		m.aiReviewBatches = nil
		m.aiReviewStatuses = nil
		m.aiReviewPhase = ""

		// ── Error path ──────────────────────────────────────────
		if msg.Err != nil {
			log.Printf("AI run error: %v", msg.Err)
			if wasReview {
				// Stamp the error onto state.LastReview so it survives
				// the TUI exit — reopening the PR will show "review
				// attempted, failed" with the message, instead of the
				// misleading "no review yet" placeholder.
				if m.reviewState != nil {
					m.reviewState.SetLastReview(&state.ReviewMeta{
						FindingsCount: len(m.reviewState.GetDeepFindings()),
						Error:         msg.Err.Error(),
						Summary:       "Review failed mid-flight.",
					})
					if err := state.Save(m.reviewState); err != nil {
						log.Printf("Warning: failed to persist failure LastReview: %v", err)
					}
				}
				// Surface the failure on the Review tab (where the
				// user is looking) by marking the active phase failed
				// with the error as its detail. Render once, THEN
				// reset, so the frozen frame the user sees is the
				// error-decorated phase list, not a blank pane.
				m.reviewProgress.FailActive(fmt.Sprintf("%v", msg.Err))
				m.updateChatViewWithStream()
				m.reviewProgress.Reset()
				// Always re-render the Review tab so the error block
				// (and any persisted partial findings) is visible.
				m.aiReviewRendered = ""
				m.reviewFindings = nil
				m.reviewCursor = -1
				m.aiPanelTab = tabReview
				m.syncLayout()
				cmd = m.renderActiveAIView()
			} else {
				m.aiStreamBuffer += "\n\n" + styleAccentRed.Render(
					fmt.Sprintf("[error: %v]", msg.Err))
				m.updateChatViewWithStream()
			}
			return m, cmd
		}

		// ── Success path ────────────────────────────────────────
		m.reviewProgress.Reset()

		// Persist a synthesized review if one came back.
		if msg.Review != nil {
			if msg.StructuredReview != nil {
				msg.Review.Structured = msg.StructuredReview
			}
			m.saveReview(msg.Review)
		}

		// Persist per-file batch findings for cache reuse.
		if msg.FileFindings != nil && m.reviewState != nil {
			for path, findings := range msg.FileFindings {
				m.reviewState.SetBatchFindings(path, "reviewed", findings)
			}
			if err := state.Save(m.reviewState); err != nil {
				log.Printf("Warning: failed to save file findings: %v", err)
			}
		}

		// Persist deep findings independently of Review. With
		// SkipSynthesis (TUI default), this is the entire payload —
		// keeping them as a first-class state field means findings
		// survive close+reopen even when no synthesised Review exists.
		if len(msg.DeepFindings) > 0 {
			m.deepFindings = msg.DeepFindings
			if msg.Review != nil {
				msg.Review.DeepFindings = msg.DeepFindings
			}
			if m.reviewState != nil {
				m.reviewState.SetDeepFindings(msg.DeepFindings)
				if err := state.Save(m.reviewState); err != nil {
					log.Printf("Warning: failed to persist deep findings: %v", err)
				}
			}
			// Invalidate the rendered cache so renderActiveAIView
			// picks up the new findings.
			m.aiReviewRendered = ""
			m.reviewFindings = nil
			m.reviewCursor = -1
		}

		// Pressing `a` is a request for a review; the result must land
		// on the Review tab even when the PR is clean (no findings) so
		// the user sees an explicit "looks clean" instead of a frozen
		// phase-tracker frame or a stray chat message.
		if wasReview {
			m.aiStreamBuffer = ""
			m.aiChatHistoryCache = ""
			m.aiPanelTab = tabReview
			m.syncLayout()
			cmd = m.renderActiveAIView()
		} else if msg.Review != nil || len(msg.DeepFindings) > 0 {
			// Non-review path that nonetheless produced review data
			// (e.g. a single-file review that returned structured
			// findings). Still route to the Review tab.
			m.aiStreamBuffer = ""
			m.aiChatHistoryCache = ""
			m.aiPanelTab = tabReview
			m.syncLayout()
			cmd = m.renderActiveAIView()
		} else {
			// Genuine chat completion — save the assistant message
			// and clear the streaming buffers.
			cmd = m.saveAIResponse(msg.FullResponse)
			m.aiStreamBuffer = ""
			m.aiChatHistoryCache = ""
		}
		return m, cmd

	case tea.KeyMsg:
		// ── Modal overlays intercept all keys when visible ──────
		// Clear flash message on any key press
		if m.flashMsg != "" {
			m.flashMsg = ""
		}
		// Permission overlay intercepts all keys
		if m.permissionOverlay != nil {
			switch msg.String() {
			case "esc", "n":
				cmd = m.denyPermission()
				m.permissionOverlay = nil
				if cmd != nil {
					return m, cmd
				}
			case "y":
				cmd = m.approvePermission()
				m.permissionOverlay = nil
				if cmd != nil {
					return m, cmd
				}
			case "enter":
				if m.permissionOverlay.cursor == 0 {
					cmd = m.approvePermission()
				} else {
					cmd = m.denyPermission()
				}
				m.permissionOverlay = nil
				if cmd != nil {
					return m, cmd
				}
			case "h", "left":
				m.permissionOverlay.cursor = 0
			case "l", "right":
				m.permissionOverlay.cursor = 1
			case "tab":
				m.permissionOverlay.cursor = (m.permissionOverlay.cursor + 1) % 2
			}
			return m, nil
		}
		// Question overlay intercepts all keys
		if m.questionOverlay != nil {
			switch msg.String() {
			case "esc":
				cmd = m.dismissQuestion()
				m.questionOverlay = nil
				if cmd != nil {
					return m, cmd
				}
			case "enter":
				cmd = m.selectQuestionOption()
				m.questionOverlay = nil
				if cmd != nil {
					return m, cmd
				}
			case "j", "down":
				if m.questionOverlay.cursor < m.questionOverlay.optionCount()-1 {
					m.questionOverlay.cursor++
				}
			case "k", "up":
				if m.questionOverlay.cursor > 0 {
					m.questionOverlay.cursor--
				}
			}
			return m, nil
		}
		// Confirm modal intercepts all keys
		if m.confirmOverlay != nil {
			switch msg.String() {
			case "esc", "q":
				m.confirmOverlay = nil
			case "enter":
				cmd = m.executeConfirmAction()
				m.confirmOverlay = nil
				if cmd != nil {
					return m, cmd
				}
			}
			return m, nil
		}
		// Action menu intercepts all keys
		if m.actionMenuOverlay != nil {
			switch msg.String() {
			case "esc", "q":
				m.actionMenuOverlay = nil
			case "j", "down":
				if m.actionMenuOverlay.cursor < len(m.actionMenuOverlay.items)-1 {
					m.actionMenuOverlay.cursor++
				}
			case "k", "up":
				if m.actionMenuOverlay.cursor > 0 {
					m.actionMenuOverlay.cursor--
				}
			case "enter":
				m.executeActionMenuItem(m.actionMenuOverlay.cursor)
				m.actionMenuOverlay = nil
			default:
				// Direct key shortcuts (c, C, g, 1, 2, 3...)
				if m.executeActionMenuByKey(msg.String()) {
					m.actionMenuOverlay = nil
				}
			}
			return m, nil
		}
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
			sections := m.modelPickerSections()
			total := modelPickerTotalItems(sections)
			switch msg.String() {
			case "esc", "q":
				m.showModelPicker = false
			case "j", "down":
				if m.modelPickerCursor < total-1 {
					m.modelPickerCursor++
				}
			case "k", "up":
				if m.modelPickerCursor > 0 {
					m.modelPickerCursor--
				}
			case "enter":
				si, ii := modelPickerItemAt(sections, m.modelPickerCursor)
				selected := sections[si].items[ii]
				if si == 0 {
					m.switchModel(selected.modelRef())
				} else {
					m.switchAOIModel(selected.modelRef())
				}
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
			} else if m.focusedPane == PaneChat && m.aiPanelTab == tabChat {
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
			cmds = append(cmds, m.syncLayoutWithRerender())
		case "ctrl+b":
			m.showFilePanel = !m.showFilePanel
			if !m.showFilePanel && m.focusedPane == PaneFileList {
				m.focusedPane = PaneDiff
				cmd = m.syncFocus()
				cmds = append(cmds, cmd)
			}
			cmds = append(cmds, m.syncLayoutWithRerender())
		case "?":
			if m.focusedPane != PaneChat || m.aiPanelTab != tabChat {
				m.showHelp = !m.showHelp
			}
		case "m":
			if m.focusedPane != PaneChat && !m.aiStreaming {
				m.showModelPicker = true
				// Pre-select current model
				sections := m.modelPickerSections()
				m.modelPickerCursor = 0
				idx := 0
				for _, section := range sections {
					for _, mod := range section.items {
						if mod.id == m.aiModelName || mod.id == m.aoiModelName {
							m.modelPickerCursor = idx
						}
						idx++
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
					m.aiPanelTab = tabReview
					m.syncLayout()
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
			case "F":
				// Toggle inline findings display (only in file diff view)
				if m.viewMode == viewModeFile && m.hasFileSelected() && m.rawDiffContent != "" {
					m.showInlineFindings = !m.showInlineFindings
					content := m.rawDiffContent
					if m.showInlineFindings {
						content = m.injectFindings(content, m.selectedFile)
					}
					m.diffContent = content
					savedOffset := m.diffViewport.YOffset
					savedCursor := m.diffCursor
					m.setDiffContent(content)
					m.diffViewport.SetYOffset(savedOffset)
					m.diffCursor = savedCursor
					return m, nil
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
				maxOffset := max(total-vpH, 0)
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
				half := max(m.diffViewport.Height/2, 1)
				m.diffViewport.LineDown(half)
				m.clampDiffCursor()
				return m, nil
			case "ctrl+u":
				// Half-page up
				half := max(m.diffViewport.Height/2, 1)
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
			if m.aiPanelTab == tabReview && len(m.reviewFindings) > 0 {
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
				case "l", "right":
					// Expand the current finding (file tree-style:
					// l/right opens, h/left closes).
					if m.reviewCursor >= 0 && m.reviewCursor < len(m.reviewFindings) {
						if m.findingsExpanded == nil {
							m.findingsExpanded = make(map[int]bool)
						}
						if !m.findingsExpanded[m.reviewCursor] {
							m.findingsExpanded[m.reviewCursor] = true
							cmds = append(cmds, m.rerenderReviewWithCursor())
						}
					}
					return m, tea.Batch(cmds...)
				case "h", "left":
					// Collapse the current finding.
					if m.reviewCursor >= 0 && m.findingsExpanded[m.reviewCursor] {
						delete(m.findingsExpanded, m.reviewCursor)
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
				case "c":
					// Publish selected finding as GitHub line comment
					if m.reviewCursor >= 0 && m.reviewCursor < len(m.reviewFindings) {
						f := m.reviewFindings[m.reviewCursor]
						m.confirmOverlay = &confirmModal{
							action:  confirmPublishComment,
							finding: &f,
						}
					}
					return m, nil
				case "C":
					// Publish ALL findings as a GitHub Review (batch)
					m.confirmOverlay = &confirmModal{
						action:   confirmBatchReview,
						findings: append([]state.ReviewFinding{}, m.reviewFindings...),
					}
					return m, nil
				case "g":
					// Post selected finding as general PR comment
					if m.reviewCursor >= 0 && m.reviewCursor < len(m.reviewFindings) {
						f := m.reviewFindings[m.reviewCursor]
						m.confirmOverlay = &confirmModal{
							action:  confirmPRComment,
							finding: &f,
						}
					}
					return m, nil
				case "|":
					// Pipe selected finding to external process
					if m.reviewCursor >= 0 && m.reviewCursor < len(m.reviewFindings) && len(m.pipeTargets) > 0 {
						f := m.reviewFindings[m.reviewCursor]
						m.confirmOverlay = &confirmModal{
							action:  confirmPipe,
							finding: &f,
							target:  &m.pipeTargets[0], // default to first pipe target
						}
					}
					return m, nil
				case "x":
					// Open action menu for selected finding
					if m.reviewCursor >= 0 && m.reviewCursor < len(m.reviewFindings) {
						f := m.reviewFindings[m.reviewCursor]
						m.actionMenuOverlay = &actionMenu{
							cursor:  0,
							finding: f,
							items:   m.buildActionMenuItems(),
						}
					}
					return m, nil
				case " ":
					// Toggle resolved status for selected finding
					if m.reviewCursor >= 0 && m.reviewCursor < len(m.reviewFindings) {
						m.reviewFindings[m.reviewCursor].Resolved = !m.reviewFindings[m.reviewCursor].Resolved
						cmds = append(cmds, m.rerenderReviewWithCursor())
					}
					return m, tea.Batch(cmds...)
				case "f":
					// Fix with OpenCode
					if m.reviewCursor >= 0 && m.reviewCursor < len(m.reviewFindings) {
						if hasRunningTaskForFinding(m.tasks, m.reviewCursor) {
							cmds = append(cmds, m.setFlash("Already running for this finding"))
							return m, tea.Batch(cmds...)
						}
						f := m.reviewFindings[m.reviewCursor]
						m.confirmOverlay = &confirmModal{
							action:  confirmFixWithOpenCode,
							finding: &f,
						}
					}
					return m, tea.Batch(cmds...)
				}
			}

			// Tasks tab navigation (j/k/Enter/d/x when tasks exist)
			if m.aiPanelTab == tabTasks && len(m.tasks) > 0 {
				switch km.String() {
				case "j", "down":
					if m.taskCursor < len(m.tasks)-1 {
						m.taskCursor++
					}
					return m, nil
				case "k", "up":
					if m.taskCursor > 0 {
						m.taskCursor--
					}
					return m, nil
				case "enter":
					// View task output in diff pane
					if m.taskCursor >= 0 && m.taskCursor < len(m.tasks) {
						m.viewTaskOutput(m.tasks[m.taskCursor].ID)
					}
					return m, nil
				case "d":
					// Cancel running task
					if m.taskCursor >= 0 && m.taskCursor < len(m.tasks) {
						t := m.tasks[m.taskCursor]
						if t.GetStatus() == TaskRunning {
							cancelTask(t, m.opencodeMgr)
							cmds = append(cmds, m.setFlash("Task cancelled"))
						}
					}
					return m, tea.Batch(cmds...)
				case "x":
					// Remove completed/failed/cancelled task
					if m.taskCursor >= 0 && m.taskCursor < len(m.tasks) {
						t := m.tasks[m.taskCursor]
						if t.GetStatus() != TaskRunning {
							m.tasks = append(m.tasks[:m.taskCursor], m.tasks[m.taskCursor+1:]...)
							if m.taskCursor >= len(m.tasks) && m.taskCursor > 0 {
								m.taskCursor--
							}
							// If we were viewing this task, go back to overview
							if m.viewMode == viewModeTaskOutput && m.viewingTaskID == t.ID {
								m.viewMode = viewModeOverview
								m.viewingTaskID = -1
								m.setDiffContent(m.renderOverview())
								m.diffViewport.GotoTop()
							}
						}
					}
					return m, nil
				}
			}

			switch km.String() {
			case "pgup", "pgdown":
				// Route scroll to the viewport that's actually visible
				// for the active tab. Tab 1 (Tasks) renders inline and
				// has no viewport — keystroke is a no-op there.
				if m.aiPanelTab == tabReview {
					m.reviewViewport, cmd = m.reviewViewport.Update(msg)
				} else if m.aiPanelTab == tabChat {
					m.chatViewport, cmd = m.chatViewport.Update(msg)
				}
				cmds = append(cmds, cmd)
			case "ctrl+k":
				m.clearChat()
			case "]":
				// Cycle forward between Review, Tasks, and Chat sub-tabs
				// (only when not typing in the chat input)
				if m.aiPanelTab == tabChat && !m.aiStreaming {
					m.chatInput, cmd = m.chatInput.Update(msg)
					cmds = append(cmds, cmd)
				} else if m.hasReview() || len(m.tasks) > 0 {
					maxTab := tabChat // Review(0), Tasks(1), Chat(2)
					m.aiPanelTab = (m.aiPanelTab + 1) % (maxTab + 1)
					m.syncLayout()
					cmds = append(cmds, m.renderActiveAIView())
				}
			case "[":
				// Cycle backward between Review, Tasks, and Chat sub-tabs
				if m.aiPanelTab == tabChat && !m.aiStreaming {
					m.chatInput, cmd = m.chatInput.Update(msg)
					cmds = append(cmds, cmd)
				} else if m.hasReview() || len(m.tasks) > 0 {
					maxTab := tabChat
					m.aiPanelTab = (m.aiPanelTab - 1 + maxTab + 1) % (maxTab + 1)
					m.syncLayout()
					cmds = append(cmds, m.renderActiveAIView())
				}
			default:
				// Only allow text input on the Chat tab, and not while AI is streaming
				if m.aiPanelTab == tabChat && !m.aiStreaming {
					m.chatInput, cmd = m.chatInput.Update(msg)
					cmds = append(cmds, cmd)
				}
			}
		} else {
			// Mouse/resize events: route scroll to the visible viewport.
			if m.aiPanelTab == tabReview {
				m.reviewViewport, cmd = m.reviewViewport.Update(msg)
			} else if m.aiPanelTab == tabChat {
				m.chatViewport, cmd = m.chatViewport.Update(msg)
			}
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
	m.aiPanelTab = tabChat
	m.syncLayout()

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

	// User-cancellable parent + idle watchdog. 240s of total silence
	// (no tokens, no thinking, no tool events) is treated as a stall;
	// active streams run to completion. The aiCancelFn hook still
	// gives the user Esc-cancel via the parent.
	parentCtx, parentCancel := context.WithCancel(context.Background())
	m.aiCancelFn = parentCancel
	ctx, watchdogTap, stopWatchdog := ai.IdleWatch(parentCtx, 240*time.Second, nil)
	ctx = ai.ContextWithTap(ctx, watchdogTap)

	// Use chat thinking budget (lower than review for responsiveness)
	if tbs, ok := m.aiClient.(ai.ThinkingBudgetSetter); ok {
		models, _ := config.LoadModels()
		if mi, ok2 := m.aiClient.(ai.ModelInfo); ok2 {
			mcfg := config.GetModelConfig(models, mi.ModelName())
			tbs.SetThinkingBudget(mcfg.ThinkingBudget.Chat)
		}
	}

	return tea.Batch(m.spinner.Tick, streamAIChat(m.aiClient, ctx, systemPrompt, aiMessages, program, watchdogTap, stopWatchdog))
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
		allDiffs.WriteString(ai.HintPROverview)

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

// updateChatViewWithStream is the dispatcher for streaming AI panel
// updates. It routes review-phase content to reviewViewport and chat
// content to chatViewport so the two viewports stay independent —
// chat history isn't clobbered by a review stream and a streaming
// review doesn't disturb chat state.
//
// The function name is kept (rather than renamed to a more neutral
// "updateAIStream") so the dozen-plus call sites stay surgical; the
// in-function dispatch keeps the routing logic in one place.
func (m *Model) updateChatViewWithStream() {
	// Either signal can mark the current stream as a review: the legacy
	// aiReviewPhase string (set by AIReviewInit/AOI/Synthesis msgs) or
	// the new phase tracker (which starts as soon as any phase event
	// arrives, including discovery/classify before AOI/batch fire).
	// Without the tracker check, the brief Discovery window before
	// AOI/batch starts routes review traffic into chatViewport.
	if m.aiReviewPhase != "" || m.reviewProgress.IsActive() {
		m.updateReviewViewWithStream()
		return
	}
	m.updateChatViewOnly()
}

// updateChatViewOnly renders the chat transcript + active chat stream
// into chatViewport. Used when no review phase is active.
func (m *Model) updateChatViewOnly() {
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

	// Render streaming AI response (chat reply)
	if m.aiStreaming || m.aiStreamBuffer != "" {
		b.WriteString(styleAccentMauveBold.Render("AI") + "\n")
		if m.aiStreamBuffer == "" {
			b.WriteString(styleTextMuted.Render("thinking...") + "\n")
		} else {
			b.WriteString(m.aiStreamBuffer)
			if m.aiStreaming {
				b.WriteString(styleAccentBlue.Render("▊"))
			}
			b.WriteString("\n")
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

// updateReviewViewWithStream renders the active review into
// reviewViewport. No chat history — the review tab is dedicated to
// the run in progress.
//
// While a review is tracked (reviewProgress.IsActive), the view shows
// the bounded phase list from renderReviewProgressView — fixed ~10
// rows regardless of batch count. The legacy "thinking..." placeholder
// is kept for the brief pre-init window before the first phase event
// arrives, so the pane is never blank during cold starts.
//
// If the user has navigated to a file mid-review, this is a no-op:
// the per-file review state lives elsewhere and shouldn't be clobbered
// by the PR-level stream.
func (m *Model) updateReviewViewWithStream() {
	if m.hasFileSelected() {
		return
	}

	var body string
	switch {
	case m.reviewProgress.IsActive():
		body = m.renderReviewProgressView(m.reviewViewport.Width)
	case m.aiStreaming:
		body = styleTextMuted.Render("thinking...") + "\n"
	}

	wasAtBottom := m.reviewViewport.AtBottom()
	m.reviewViewport.SetContent(body)
	if wasAtBottom {
		m.reviewViewport.GotoBottom()
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
		fill := max(width-pw-lw, 1)
		return prompt + strings.Repeat(" ", fill) + label
	}
	// Blurred: subtle separator
	return styleTextSubtle.Render(strings.Repeat("─", width))
}

// hasReview returns true if any reviewable content exists for the
// current PR. Three sources count as "a review exists":
//
//   - state.Review        — synthesized Review (headless / legacy path)
//   - state.DeepFindings  — persisted findings (TUI default; pipeline
//     streams these incrementally so a partial run
//     still leaves something to display)
//   - state.LastReview    — proof-of-run marker stamped by the pipeline
//     at end of every successful run, even when zero
//     findings remained (clean PR or recheck-dismissed-
//     everything). Without this, "ran and found nothing"
//     was indistinguishable from "never ran."
func (m Model) hasReview() bool {
	if m.reviewState == nil {
		return false
	}
	return m.reviewState.HasReviewArtifact()
}

// renderAIPanelTitle builds the AI panel title with tab indicators and streaming status.
func (m Model) renderAIPanelTitle(maxWidth int) string {
	// During streaming, show review progress or a chat spinner instead
	// of the tab indicators. The tracker is the authoritative "is this
	// a review run" signal — it's set in triggerAIReview before any
	// pipeline event arrives, so we route correctly even during the
	// pre-batch phases (discovery / classify / AOI) when
	// aiReviewBatches is still empty.
	if m.aiStreaming {
		if m.reviewProgress.IsActive() || len(m.aiReviewBatches) > 0 {
			if len(m.aiReviewBatches) > 0 {
				return m.renderReviewProgress(maxWidth)
			}
			return styleAccentBlueBold.Render("Reviewing ") + m.spinner.View()
		}
		return styleAccentBlueBold.Render("Chat ") + m.spinner.View()
	}

	// If no review exists and no tasks, just show "Chat" — no tabs needed
	if !m.hasReview() && len(m.tasks) == 0 {
		return styleAccentBlueBold.Render("Chat")
	}

	// Tab indicators: [Review] [Tasks] [Chat] — active tab is highlighted
	tabs := []struct {
		label string
		idx   int
	}{
		{"Review", 0},
		{"Tasks", 1},
		{"Chat", 2},
	}

	var parts []string
	for _, t := range tabs {
		// Skip Tasks tab if no tasks and no review (unlikely but safe)
		if t.idx == 1 && len(m.tasks) == 0 && !m.hasReview() {
			continue
		}
		label := t.label
		// Add running task count indicator + server status
		if t.idx == 1 {
			running := 0
			for _, task := range m.tasks {
				if task.GetStatus() == TaskRunning {
					running++
				}
			}
			if running > 0 {
				label = fmt.Sprintf("Tasks(%d)", running)
			}
			// Server status dot
			if m.opencodeMgr != nil {
				switch m.opencodeMgr.Status() {
				case opencode.ServerConnected:
					label += " " + styleAccentGreen.Render("●")
				case opencode.ServerConnecting:
					label += " " + styleAccentYellow.Render("●")
				}
			}
		}
		if t.idx == m.aiPanelTab {
			parts = append(parts, styleAccentBlueBold.Render(label))
		} else {
			parts = append(parts, styleTextMuted.Render(label))
		}
	}
	return strings.Join(parts, styleTextSubtle.Render(" │ "))
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
		filled := min((completed*barWidth)/total, barWidth)
		empty := barWidth - filled
		bar := styleProgressBar.Render(strings.Repeat("█", filled)) +
			styleProgressBg.Render(strings.Repeat("░", empty))
		return spin + " " + bar + " " + styleTextMuted.Render(label)
	}

	// Synthesis phase — pulsing bar to show activity
	secs := float64(time.Now().UnixMilli()) / 1000.0
	pct := 0.5 + 0.45*math.Sin(secs*math.Pi/2)
	filled := max(int(pct*float64(barWidth)), 1)
	if filled > barWidth {
		filled = barWidth
	}
	empty := barWidth - filled
	bar := styleProgressBar.Render(strings.Repeat("█", filled)) +
		styleProgressBg.Render(strings.Repeat("░", empty))
	return spin + " " + bar + " " + styleTextMuted.Render(label)
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
// Comments at the same position are rendered as a single threaded block.
func (m *Model) injectComments(styledDiff, filePath string) string {
	fileComments, ok := m.comments[filePath]
	if !ok || len(fileComments) == 0 {
		return styledDiff
	}

	// Build map of "side:line" -> comments (ordered by creation time)
	type commentKey struct {
		side string
		line int
	}
	commentsByKey := make(map[commentKey][]git.ReviewComment)
	for _, c := range fileComments {
		if c.Line == 0 {
			continue // skip comments without a resolved position
		}
		key := commentKey{side: c.Side, line: c.Line}
		commentsByKey[key] = append(commentsByKey[key], c)
	}

	commentTitleSt := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#F9E2AF")).
		Bold(true)
	replyHeaderSt := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A6ADC8")).
		Bold(true)
	bodyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#CDD6F4"))
	borderColor := lipgloss.Color("#585B70")

	// Inline boxes use the diff pane's width budget when available; we
	// fall back to deriving from the viewport directly when the budget
	// hasn't been populated yet (e.g. in tests that bypass syncLayout).
	boxWidth := m.diffWidths.boxOuter
	if boxWidth <= 0 {
		boxWidth = m.diffViewport.Width - 2
	}
	if boxWidth < 20 {
		boxWidth = 20
	}
	innerW := m.diffWidths.boxInner
	if innerW <= 0 {
		innerW = boxWidth - 4
	}
	if innerW < 10 {
		innerW = 10
	}

	lines := strings.Split(styledDiff, "\n")
	var result []string

	wrapBody := func(body string) []string {
		var out []string
		for raw := range strings.SplitSeq(body, "\n") {
			for _, w := range wrapText(raw, innerW) {
				out = append(out, bodyStyle.Render(w))
			}
		}
		return out
	}

	// renderCommentBlock appends a threaded comment block to result.
	renderCommentBlock := func(comments []git.ReviewComment) {
		if len(comments) == 0 {
			return
		}
		var contentLines []string
		contentLines = append(contentLines, wrapBody(comments[0].Body)...)
		for _, c := range comments[1:] {
			contentLines = append(contentLines, "")
			contentLines = append(contentLines, replyHeaderSt.Render("↳ "+c.Author))
			contentLines = append(contentLines, wrapBody(c.Body)...)
		}
		box := Box{
			Width:       boxWidth,
			Title:       comments[0].Author,
			BorderColor: borderColor,
			TitleStyle:  &commentTitleSt,
			Padding:     Padding{Left: 1, Right: 1},
		}
		rendered := box.Render(strings.Join(contentLines, "\n"))
		for l := range strings.SplitSeq(rendered, "\n") {
			result = append(result, "  "+l)
		}
	}

	for _, line := range lines {
		result = append(result, line)

		info := parseDiffLine(line)
		if info.line == 0 {
			continue
		}

		// Check the primary side for this line
		key := commentKey{side: info.side, line: info.line}
		if comments, ok := commentsByKey[key]; ok {
			renderCommentBlock(comments)
		}

		// For context lines (both old and new numbers present), also check
		// the LEFT side — a reviewer may comment on the old-file view of an
		// unchanged line.
		if info.leftLine > 0 && info.rightLine > 0 {
			leftKey := commentKey{side: "LEFT", line: info.leftLine}
			if comments, ok := commentsByKey[leftKey]; ok {
				renderCommentBlock(comments)
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
	for line := range strings.SplitSeq(diff, "\n") {
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
	if w > 1 {
		// Truncate to w-1 to leave a 1-cell safety margin. This prevents
		// characters with ambiguous terminal width (emoji, CJK, etc.) from
		// visually overflowing the pane border.
		maxW := w - 1
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if ansi.StringWidth(line) > maxW {
				lines[i] = ansi.Truncate(line, maxW, "")
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

// ── Finding publish/pipe execution ──────────────────────────────────────

const flashDismissDelay = 3 * time.Second

// setFlash sets the flash message and returns a command that auto-dismisses it.
func (m *Model) setFlash(msg string) tea.Cmd {
	m.flashMsg = msg
	return tea.Tick(flashDismissDelay, func(time.Time) tea.Msg {
		return flashDismissMsg{}
	})
}

// executeConfirmAction runs the confirmed action and returns a tea.Cmd.
//
// Most actions require a loaded PR (modal.finding lives on a PR); the
// confirmDeleteCorruptState branch is the exception — it fires before
// the PR is loaded, since state corruption is detected during the
// initial load.
func (m *Model) executeConfirmAction() tea.Cmd {
	if m.confirmOverlay == nil {
		return nil
	}
	modal := m.confirmOverlay

	if modal.action == confirmDeleteCorruptState {
		if err := state.DeleteStateFile(m.prNumber); err != nil {
			log.Printf("Failed to delete corrupt state file: %v", err)
			m.errorMsg = fmt.Sprintf("Failed to delete state file:\n%v", err)
			return nil
		}
		log.Printf("Deleted corrupt state file at %s; restarting load", modal.statePath)
		// Restart the load chain. fetchPR will trigger PR fetch →
		// refs → diffs → state.Load (which will now create a fresh
		// state since we just deleted the file).
		m.loading = true
		m.loadingMsg = "Reloading after state reset..."
		return fetchPR(m.prNumber)
	}

	if m.pr == nil {
		return nil
	}

	switch modal.action {
	case confirmPublishComment:
		if modal.finding != nil {
			return publishFindingAsComment(m.prNumber, m.pr.HeadRefOid, *modal.finding)
		}
	case confirmBatchReview:
		if len(modal.findings) > 0 {
			return publishBatchReview(m.prNumber, m.pr.HeadRefOid, modal.findings)
		}
	case confirmPRComment:
		if modal.finding != nil {
			return postFindingAsPRComment(m.prNumber, *modal.finding)
		}
	case confirmPipe:
		if modal.finding != nil && modal.target != nil {
			return executePipe(*modal.target, *modal.finding, m.repoRoot)
		}
	case confirmFixWithOpenCode:
		if modal.finding != nil {
			return m.spawnFixTask(*modal.finding)
		}
	}
	return nil
}

// executeActionMenuItem triggers the action at the given menu index.
func (m *Model) executeActionMenuItem(idx int) {
	if m.actionMenuOverlay == nil {
		return
	}
	menu := m.actionMenuOverlay
	f := menu.finding

	switch {
	case idx == 0: // Fix with OpenCode
		m.confirmOverlay = &confirmModal{action: confirmFixWithOpenCode, finding: &f}
	case idx == 1: // Post as line comment
		m.confirmOverlay = &confirmModal{action: confirmPublishComment, finding: &f}
	case idx == 2: // Post ALL as review
		m.confirmOverlay = &confirmModal{action: confirmBatchReview, findings: append([]state.ReviewFinding{}, m.reviewFindings...)}
	case idx == 3: // Post as PR comment
		m.confirmOverlay = &confirmModal{action: confirmPRComment, finding: &f}
	default:
		// Pipe targets (idx-4 maps to pipeTargets index)
		pipeIdx := idx - 4
		if pipeIdx >= 0 && pipeIdx < len(m.pipeTargets) {
			m.confirmOverlay = &confirmModal{
				action:  confirmPipe,
				finding: &f,
				target:  &m.pipeTargets[pipeIdx],
			}
		}
	}
}

// executeActionMenuByKey handles direct key shortcuts in the action menu.
// Returns true if the key matched an action.
func (m *Model) executeActionMenuByKey(key string) bool {
	if m.actionMenuOverlay == nil {
		return false
	}
	menu := m.actionMenuOverlay
	f := menu.finding

	switch key {
	case "f":
		m.confirmOverlay = &confirmModal{action: confirmFixWithOpenCode, finding: &f}
	case "c":
		m.confirmOverlay = &confirmModal{action: confirmPublishComment, finding: &f}
	case "C":
		m.confirmOverlay = &confirmModal{action: confirmBatchReview, findings: append([]state.ReviewFinding{}, m.reviewFindings...)}
	case "g":
		m.confirmOverlay = &confirmModal{action: confirmPRComment, finding: &f}
	default:
		// Check numeric keys for pipe targets (1-indexed)
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			idx := int(key[0] - '1')
			if idx < len(m.pipeTargets) {
				m.confirmOverlay = &confirmModal{
					action:  confirmPipe,
					finding: &f,
					target:  &m.pipeTargets[idx],
				}
				return true
			}
		}
		return false
	}
	return true
}

// ── Task management ─────────────────────────────────────────────────────

// maxConcurrentTasks limits the number of simultaneously running tasks
// to prevent unbounded goroutine spawning.
const maxConcurrentTasks = 5

// countRunningTasks returns the number of currently running tasks.
func countRunningTasks(tasks []*Task) int {
	n := 0
	for _, t := range tasks {
		if t.GetStatus() == TaskRunning {
			n++
		}
	}
	return n
}

// spawnFixTask creates a new task for the given finding and launches it.
// Returns a tea.Cmd that sets the flash message (actual spawning is async via program.Send).
func (m *Model) spawnFixTask(f state.ReviewFinding) tea.Cmd {
	if program == nil {
		return m.setFlash("Error: program not initialized")
	}
	if m.opencodeMgr == nil {
		return m.setFlash("Error: OpenCode manager not initialized")
	}
	if countRunningTasks(m.tasks) >= maxConcurrentTasks {
		return m.setFlash(fmt.Sprintf("Too many running tasks (max %d) — wait for one to finish", maxConcurrentTasks))
	}

	// Ensure the server is started (lazy init on first task)
	if m.opencodeMgr.Status() != opencode.ServerConnected {
		mgr := m.opencodeMgr
		go func() {
			if err := mgr.Start(context.Background()); err != nil {
				program.Send(TaskDoneMsg{ID: -1, Err: fmt.Errorf("server start: %v", err)})
				return
			}
			// Retry the spawn now that server is up
			program.Send(opencodeReadyMsg{finding: f})
		}()
		return m.setFlash("Starting OpenCode server...")
	}

	task := &Task{
		ID:         m.taskNextID,
		Title:      taskTitle(f),
		FindingIdx: m.reviewCursor,
		Finding:    f,
		StartedAt:  time.Now(),
	}
	task.setStatus(TaskRunning, "")
	m.taskNextID++
	m.tasks = append(m.tasks, task)

	// Auto-switch to Tasks tab
	m.aiPanelTab = tabTasks
	m.syncLayout()
	m.taskCursor = len(m.tasks) - 1

	// Launch the background task via HTTP API
	go spawnOpenCodeTask(task, m.opencodeMgr, program)

	return m.setFlash("Task started: " + task.Title)
}

// viewTaskOutput switches the diff pane to show a task's output.
func (m *Model) viewTaskOutput(taskID int) {
	m.viewingTaskID = taskID
	m.viewMode = viewModeTaskOutput
	content := m.renderTaskOutput(taskID)
	m.setDiffContent(content)
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
		// Overview mode — multi-pass PR review.
		//
		// Use review.BuildPRMeta so the prompt header is byte-for-byte
		// identical to what `prr review` (headless) sends. Previously
		// the TUI built its own header here via getAIContext() which
		// also appended a "Files changed" listing + an overview hint
		// prompt — that made the two paths feed the model different
		// inputs and produce different outputs for the same PR.
		prMeta := review.BuildPRMeta(m.pr)

		// Clear the previous review so the streaming view takes precedence
		if m.reviewState != nil {
			// Do NOT clear the review, otherwise subsequent reviews will fail to show
			// the cached review data because the review is immediately set to nil
			// m.reviewState.Review = nil
		}

		// Start streaming on the Review tab. The user just pressed `a`
		// expecting a review — render it where they're looking.
		m.aiStreaming = true
		m.aiPanelTab = tabReview
		m.syncLayout()
		m.aiStreamBuffer = ""
		m.aiChatHistoryCache = ""
		// Pre-Start the phase tracker so the very first View() after
		// `a` shows the bounded phase list (all rows pending) instead
		// of falling through to "thinking..." or the chat history.
		// Otherwise the first ~hundred ms feel like the review tab is
		// stuck on the previous chat content.
		m.reviewProgress.Start(defaultReviewPhases())
		m.updateChatViewWithStream()

		// Idle watchdog instead of wall-clock timeout. 240s of zero
		// activity (no tokens, no batch progress, no AOI updates)
		// cancels the run; slow-but-active multi-pass reviews finish.
		parentCtx, parentCancel := context.WithCancel(context.Background())
		m.aiCancelFn = parentCancel
		ctx, watchdogTap, stopWatchdog := ai.IdleWatch(parentCtx, 240*time.Second, nil)
		ctx = ai.ContextWithTap(ctx, watchdogTap)

		// Ensure review thinking budget is active (chat may have lowered it)
		if tbs, ok := m.aiClient.(ai.ThinkingBudgetSetter); ok {
			models, _ := config.LoadModels()
			if mi, ok2 := m.aiClient.(ai.ModelInfo); ok2 {
				mcfg := config.GetModelConfig(models, mi.ModelName())
				tbs.SetThinkingBudget(mcfg.ThinkingBudget.Review)
			}
		}

		return tea.Batch(m.spinner.Tick, streamMultiPassReview(ctx, m.aiClient, m.aoiClient, m.pr, prMeta, m.rawDiffs, m.customInstructions, m.reviewState, m.parallelReviews, teaReporter{p: program}, m.pr.BaseRefName, m.pr.HeadRefName, m.aoiContextLines, m.repoRoot, watchdogTap, stopWatchdog))
	}

	// Single file mode
	if m.selectedFile != path {
		m.selectedFile = path
	}

	// Skip files excluded by content filter (binary, generated, large)
	if reason, ok := m.skippedFiles[path]; ok {
		m.aiPanelTab = tabChat
		m.syncLayout()
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
		m.aiPanelTab = tabChat // show info notice in Chat tab
		m.syncLayout()
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
		userContent = fmt.Sprintf(ai.HintLargeFileReview, path, diffLines)
	} else {
		userContent = fmt.Sprintf("Please review the changes to `%s`.\n\n```diff\n%s\n```", path, diff)
	}

	// Add to state
	userStateMsg := state.Message{Role: "user", Content: userContent}
	m.appendMessageToState(userStateMsg)

	// Build full conversation history so the AI sees prior messages
	messages := m.buildAIMessages()
	aiMessages := m.buildAIMessagesWithContext(messages)

	// Single-file review streams into the per-file Chat history, so
	// route to the Chat tab — distinct from PR-level multi-pass review.
	m.aiStreaming = true
	m.aiPanelTab = tabChat
	m.syncLayout()
	m.aiStreamBuffer = ""
	m.aiChatHistoryCache = "" // invalidate so it's rebuilt on first tick
	m.updateChatViewWithStream()

	// Idle watchdog: 240s of zero activity cancels the call. Esc still
	// works as a user cancel via parentCancel.
	parentCtx, parentCancel := context.WithCancel(context.Background())
	m.aiCancelFn = parentCancel
	ctx, watchdogTap, stopWatchdog := ai.IdleWatch(parentCtx, 240*time.Second, nil)
	ctx = ai.ContextWithTap(ctx, watchdogTap)

	// Use review thinking budget (same depth as batch review)
	if tbs, ok := m.aiClient.(ai.ThinkingBudgetSetter); ok {
		models, _ := config.LoadModels()
		if mi, ok2 := m.aiClient.(ai.ModelInfo); ok2 {
			mcfg := config.GetModelConfig(models, mi.ModelName())
			tbs.SetThinkingBudget(mcfg.ThinkingBudget.Review)
		}
	}

	return tea.Batch(m.spinner.Tick, streamAIChat(m.aiClient, ctx, systemPrompt, aiMessages, program, watchdogTap, stopWatchdog))
}

// forceReReview clears all cached batch findings and triggers a fresh PR review.
func (m *Model) forceReReview() tea.Cmd {
	if m.reviewState == nil {
		return m.triggerAIReview()
	}

	// Clear all per-file caches and PR-level review (thread-safe)
	m.reviewState.ClearAllCaches()
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
		offset := max(bestOffset-vpHeight/2, 0)
		// Clamp to max scroll position to keep cursor calculation consistent
		maxOffset := max(m.diffViewport.TotalLineCount()-vpHeight, 0)
		if offset > maxOffset {
			offset = maxOffset
		}
		m.diffViewport.SetYOffset(offset)

		// Set diff cursor to the target line within the visible area
		m.diffCursor = max(bestOffset-offset, 0)
		if m.diffCursor >= vpHeight {
			m.diffCursor = vpHeight - 1
		}
	}
}

// jumpToFinding navigates to the file:line referenced by the finding at
// the given index. Selects the file in the tree, loads the diff, and
// sets up pending scroll to the target line.
//
// Three-branch dispatch for finding shape:
//   - PR-level (File == ""): no file to open; surface a flash message
//     and leave the current view in place. Previously this silently
//     no-op'd, which the user perceived as "Enter goes to the list of
//     issues" because the view stayed where it was.
//   - File-level (File != "", Line <= 0): open the file at line 1.
//   - Locatable (File != "", Line > 0): open the file at the line.
func (m *Model) jumpToFinding(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.reviewFindings) || m.pr == nil {
		return nil
	}
	finding := m.reviewFindings[idx]

	switch {
	case finding.File == "":
		m.flashMsg = "PR-level finding — no file to navigate to"
		return nil
	case finding.Line <= 0:
		return m.openFileAtLine(finding.File, 1)
	default:
		return m.openFileAtLine(finding.File, finding.Line)
	}
}

// openFileAtLine selects the file in the tree, switches focus to the
// diff pane, and queues a styled-diff fetch that will scroll to line.
// Shared by jumpToFinding's branches so the file-open behavior is
// consistent regardless of which shape the finding had.
func (m *Model) openFileAtLine(file string, line int) tea.Cmd {
	if file == "" || m.pr == nil {
		return nil
	}
	m.fileTree.selectByPath(file)
	m.selectedFile = file
	m.viewMode = viewModeFile
	m.pendingScrollLine = line
	m.cameFromFinding = true
	m.focusedPane = PaneDiff
	m.chatInput.Blur()
	m.setDiffContent(styleTextMuted.Render("Loading diff..."))
	return fetchStyledDiff(
		m.pr.BaseRefName, m.pr.HeadRefName, file, m.contextLines,
		false, m.useChroma, m.diffViewport.Width,
	)
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
	// newFileTree leaves width at zero; let syncLayout populate it from
	// the current column budget so the very first render after PR load
	// doesn't see ft.width == 0 (which makes SelectableRow truncate to
	// ~1 cell per row and the file panel look empty).
	m.syncLayout()
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
		m.rawDiffContent = ""
		m.viewMode = viewModeOverview
		m.setDiffContent(m.renderOverview())
		m.diffViewport.GotoTop()
		m.diffCursor = 0
		return m.renderActiveAIView()
	}

	if m.fileTree.selectedIsActions() {
		m.selectedFile = ""
		m.rawDiffContent = ""
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
		m.rawDiffContent = ""
		m.viewMode = viewModeOverview
		m.setDiffContent(m.renderOverview())
		m.diffViewport.GotoTop()
		m.diffCursor = 0
		return m.renderActiveAIView()
	}

	if m.fileTree.selectedIsActions() {
		m.selectedFile = ""
		m.rawDiffContent = ""
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
	switch m.aiPanelTab {
	case tabReview:
		if m.hasReview() {
			return m.renderReviewForFile(m.selectedFile)
		}
		// No review yet — show placeholder in the review viewport.
		placeholder := styleTextMuted.Render("  No review yet — press R to start a review")
		m.reviewViewport.SetContent(placeholder)
		m.reviewViewport.GotoTop()
		return nil
	case tabTasks:
		// Rendered directly in View(), nothing to prepare.
		return nil
	default: // tabChat
		return m.renderChatForFile(m.selectedFile)
	}
}

// renderCleanReviewPlaceholder writes the Review tab content shown
// when hasReview() is true but no findings survived. Two shapes:
//
//   - success / clean PR (Error == ""): green checkmark + verdict +
//     dismissed-count context + re-review hint.
//   - failed run (Error != ""): red header + the error message +
//     "press a to retry" hint, so the user can see what went wrong
//     and recover instead of being told "no review yet."
//
// Both shapes pull from state.LastReview so the context persists
// across TUI restarts — closing prr and reopening still shows what
// happened the last time.
func (m *Model) renderCleanReviewPlaceholder() {
	lr := (*state.ReviewMeta)(nil)
	if m.reviewState != nil {
		lr = m.reviewState.LastReview
	}

	var b strings.Builder

	if lr != nil && lr.Error != "" {
		b.WriteString(styleAccentRed.Bold(true).Render("  ✗ Review failed"))
		b.WriteString("\n\n")
		// Wrap the error so long messages don't get truncated by the
		// viewport's horizontal clip.
		w := max(m.reviewViewport.Width-4, 30)
		b.WriteString(wrapStyled(styleTextSecondary, "  "+lr.Error, w))
		b.WriteString("\n\n")
		when := lr.CompletedAt.Format("2006-01-02 15:04")
		b.WriteString(styleTextMuted.Render(fmt.Sprintf("  Last attempt: %s", when)))
		b.WriteString("\n")
		if lr.FindingsCount > 0 {
			b.WriteString(styleTextMuted.Render(
				fmt.Sprintf("  %d finding(s) were persisted before the failure (open the Files panel).",
					lr.FindingsCount)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(styleTextMuted.Render("  Press a to retry, A to force a fresh run."))
		m.reviewViewport.SetContent(b.String())
		m.reviewViewport.GotoTop()
		return
	}

	b.WriteString(styleAccentGreen.Render("  ✓ No findings — PR looks clean."))
	b.WriteString("\n\n")

	if lr != nil {
		when := lr.CompletedAt.Format("2006-01-02 15:04")
		verdict := lr.Verdict
		if verdict == "" {
			verdict = "approve"
		}
		b.WriteString(styleTextMuted.Render(
			fmt.Sprintf("  Reviewed %s · verdict %s", when, verdict)))
		b.WriteString("\n")
		if lr.DismissedCount > 0 {
			b.WriteString(styleTextMuted.Render(
				fmt.Sprintf("  Recheck dismissed %d finding(s) as false-positive.",
					lr.DismissedCount)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(styleTextMuted.Render("  Press a to re-review, A to force re-review."))
	m.reviewViewport.SetContent(b.String())
	m.reviewViewport.GotoTop()
}

// validateReviewForTUI applies the same structural validation the
// headless pipeline runs to a ReviewOutput before the TUI renders it.
// This guards against hallucinated file paths, empty titles, and out-
// of-hunk line numbers that would otherwise make Enter-on-finding
// silently no-op (the original symptom of the "Enter goes to a list
// of issues" regression).
//
// Mutates the review in place. Logs dropped-finding counts so the
// user can see if findings disappear silently.
func (m *Model) validateReviewForTUI(r *state.ReviewOutput) {
	if r == nil || m.pr == nil {
		return
	}
	hunks := make(map[string][]review.HunkRange, len(m.rawDiffs))
	for path, patch := range m.rawDiffs {
		hunks[path] = review.ParseHunkRanges(patch)
	}
	_, dropped := review.ValidateAndNormalize(r, m.pr.Files, hunks)
	if len(dropped) > 0 {
		log.Printf("review validation: dropped %d malformed finding(s)", len(dropped))
		for _, d := range dropped {
			log.Printf("  - %q (%s): %s", d.Title, d.File, d.Reason)
		}
	}
}

// renderReviewForFile renders the PR-level AI review in the Review tab.
// If a structured ReviewOutput is available, it renders that with severity
// grouping and color coding. Otherwise falls back to markdown rendering.
//
// Output goes to reviewViewport (Tab 0). The cached-render width tracks
// reviewViewport's width so cache invalidation fires when the Review tab
// resizes.
func (m *Model) renderReviewForFile(filePath string) tea.Cmd {
	if m.reviewState == nil {
		return nil
	}

	width := m.reviewViewport.Width - 2
	cursor := m.reviewCursor

	// If we already rendered at this width, reuse the cached content.
	// (Cache is invalidated when a new review arrives or cursor changes
	// via rerenderReviewWithCursor.)
	if m.aiReviewRendered != "" && m.aiReviewRenderWidth == width {
		m.reviewViewport.SetContent(m.aiReviewRendered)
		return nil
	}

	// SkipSynthesis path: no synthesized Review. Render the persisted
	// DeepFindings directly via a synthetic ReviewOutput. This is the
	// TUI default — synthesis is no longer required for the Review tab
	// to populate, which means a closed-then-reopened prr session
	// shows the findings instead of "no reviews yet."
	if m.reviewState.Review == nil {
		deep := m.reviewState.GetDeepFindings()
		if len(deep) == 0 {
			// Reaching here means hasReview() returned true (so a
			// review has been attempted) but produced no surviving
			// findings. Render a positive "looks clean" message using
			// the LastReview marker for context (dismissed count etc.)
			// instead of leaving the pane silent.
			m.renderCleanReviewPlaceholder()
			return nil
		}
		synthetic := buildSyntheticReviewFromDeepFindings(deep)
		m.validateReviewForTUI(synthetic)
		stale := m.reviewState.IsReviewStale()
		expanded := m.findingsExpanded
		return func() tea.Msg {
			rendered, findings := renderStructuredReview(synthetic, width, cursor, expanded, stale)
			return ReviewRenderedMsg{Content: rendered, Findings: findings}
		}
	}

	review := m.reviewState.Review

	// Show placeholder, render async
	m.reviewViewport.SetContent(styleTextMuted.Render("Rendering review..."))

	expanded := m.findingsExpanded

	// Check if we have structured review data
	if review.Structured != nil {
		m.validateReviewForTUI(review.Structured)
		structured := review.Structured
		stale := m.reviewState.IsReviewStale()
		return func() tea.Msg {
			rendered, findings := renderStructuredReview(structured, width, cursor, expanded, stale)
			return ReviewRenderedMsg{Content: rendered, Findings: findings}
		}
	}

	// Attempt to recover structured data from the raw summary.
	// This handles reviews saved by older versions where ParseReviewOutput
	// failed due to multi-round prose mixed with JSON.
	if parsed := ai.ParseReviewOutput(review.Summary); parsed != nil {
		review.Structured = parsed
		// Persist the recovered structured data so we don't re-parse next time.
		if m.reviewState != nil {
			if err := state.Save(m.reviewState); err != nil {
				log.Printf("Warning: failed to persist recovered structured review: %v", err)
			}
		}
		structured := parsed
		stale := m.reviewState.IsReviewStale()
		return func() tea.Msg {
			rendered, findings := renderStructuredReview(structured, width, cursor, expanded, stale)
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

	width := m.reviewViewport.Width - 2
	stale := m.reviewState.IsReviewStale()
	rendered, _ := renderStructuredReview(m.reviewState.Review.Structured, width, m.reviewCursor, m.findingsExpanded, stale)
	m.aiReviewRendered = rendered
	m.aiReviewRenderWidth = width
	m.reviewViewport.SetContent(rendered)

	// Scroll the review viewport to keep the selected finding visible.
	m.scrollReviewToFinding(m.reviewCursor)

	return nil
}

// scrollReviewToFinding scrolls the review viewport so the selected
// finding is visible. Scans the rendered content for the "▸" cursor
// marker.
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

	vpHeight := m.reviewViewport.Height
	offset := max(
		// show marker in upper third
		targetLine-vpHeight/3, 0)
	maxOffset := max(m.reviewViewport.TotalLineCount()-vpHeight, 0)
	if offset > maxOffset {
		offset = maxOffset
	}
	m.reviewViewport.SetYOffset(offset)
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
		return ChatRenderedMsg{FilePath: fp, Content: b.String(), Tab: 2}
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
		for line := range strings.SplitSeq(m.pr.Body, "\n") {
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

	// Compute per-pane width budgets once. Everywhere else reads from
	// these instead of doing its own "viewport.Width - N" arithmetic.
	m.filesWidths = budgetFromPane(cols[0])
	m.diffWidths = budgetFromPane(cols[1])
	m.aiWidths = budgetFromPane(cols[2])

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
		// Chat input is visible only on Tab 2 (Chat) when not streaming.
		// On any other tab, or during AI streaming, the input is hidden
		// and the viewport gets the full content height.
		inputVisible := m.aiPanelTab == tabChat && !m.aiStreaming
		var chatVpH int
		if inputVisible {
			// renderPane clips to contentH = ih - 2 (borders)
			// chatVpH + 1 (separator) + 1 (newline) + chatInputH = ih - 2
			chatVpH = ih - chatInputH - 3
		} else {
			chatVpH = ih - 2
		}
		if chatVpH < 1 {
			chatVpH = 1
		}
		m.chatViewport.Width = cw
		m.chatViewport.Height = chatVpH
		m.chatInput.SetWidth(cw)
		m.chatInput.SetHeight(chatInputH)

		// Review viewport always gets the full content area — Tab 0 has
		// no input. Sized identically to chatViewport's "no input" mode
		// so layout is consistent across tab switches.
		reviewVpH := max(ih-2, 1)
		m.reviewViewport.Width = cw
		m.reviewViewport.Height = reviewVpH
	}

	// Comment input width matches diff pane
	m.commentInput.SetWidth(cols[1] - 4)
}

// syncLayoutWithRerender calls syncLayout and returns commands to re-render
// content if viewport widths changed (e.g. after toggling side panels).
func (m *Model) syncLayoutWithRerender() tea.Cmd {
	prevDiffW := m.diffViewport.Width
	prevChatW := m.chatViewport.Width
	prevReviewW := m.reviewViewport.Width
	m.syncLayout()

	var cmds []tea.Cmd

	// Re-render diff/overview content if diff viewport width changed
	if m.diffViewport.Width != prevDiffW {
		switch m.viewMode {
		case viewModeOverview:
			m.setDiffContent(m.renderOverview())
		case viewModeActions:
			m.setDiffContent(m.renderActionsView())
		case viewModeTaskOutput:
			content := m.renderTaskOutput(m.viewingTaskID)
			m.setDiffContent(content)
		case viewModeFile:
			if m.selectedFile != "" && m.pr != nil {
				// Immediately re-truncate existing content to the new width
				// so the current frame looks correct while the async
				// re-fetch is in flight.
				if m.diffContent != "" {
					m.setDiffContent(m.diffContent)
				}
				cmds = append(cmds, fetchStyledDiff(
					m.pr.BaseRefName, m.pr.HeadRefName,
					m.selectedFile, m.contextLines, true,
					m.useChroma, m.diffViewport.Width))
			}
		}
	}

	// Re-render AI panel content if either viewport width changed.
	// Review-cache invalidation is tied to reviewViewport since that's
	// where rendered review content lives now.
	if m.reviewViewport.Width != prevReviewW {
		m.aiReviewRendered = ""
		m.aiReviewRenderWidth = 0
	}
	if m.chatViewport.Width != prevChatW || m.reviewViewport.Width != prevReviewW {
		cmds = append(cmds, m.renderActiveAIView())
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
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
	avail := max(m.width-separators, 20)

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

	// Final enforcement: ensure total never exceeds available width.
	// Shrink mid (the diff pane) as a last resort.
	if total := l + mid + r; total > avail {
		mid = max(avail-l-r, 1)
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
				maxCode := max(w-blameWidth-2,
					// don't crush the code too much
					20)
				// Truncate code portion to make room for blame
				if ansi.StringWidth(line) > maxCode {
					line = ansi.Truncate(line, maxCode, "")
				}
				visible := ansi.StringWidth(line)
				gap := max(w-visible-blameWidth, 2)
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
		if content, ok := m.renderPRPicker(); ok {
			return centerOverlay(content, m.width, m.height)
		}
	}

	cols := m.columns()
	ih := m.contentHeight()

	header := m.viewHeader()

	diffTitle := "OVERVIEW"
	diffBody, cursorLineNum := m.renderDiffWithCursor()
	if m.viewMode == viewModeActions {
		diffTitle = "ACTIONS"
	} else if m.viewMode == viewModeTaskOutput {
		diffTitle = "TASK OUTPUT"
	} else if m.hasFileSelected() {
		findingsCount := m.fileFindingsCount(m.selectedFile)
		findingsSuffix := ""
		if findingsCount > 0 {
			if m.showInlineFindings {
				findingsSuffix = fmt.Sprintf(" [%dF]", findingsCount)
			} else {
				findingsSuffix = fmt.Sprintf(" [%dF hidden]", findingsCount)
			}
		}
		if m.focusedPane == PaneDiff && cursorLineNum > 0 {
			diffTitle = fmt.Sprintf("DIFF (±%d) L%d%s", m.contextLines, cursorLineNum, findingsSuffix)
		} else {
			diffTitle = fmt.Sprintf("DIFF (±%d)%s", m.contextLines, findingsSuffix)
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

		// Build chat body — depends on active tab
		var chatBody string
		switch m.aiPanelTab {
		case tabReview:
			chatBody = m.reviewViewport.View()
		case tabTasks:
			chatBody = m.renderTasksTab(cw)
		case tabChat:
			// Hide the chat input while an AI stream is running. Input
			// handling already gates on !aiStreaming, but the field
			// would otherwise be visible-but-dead, which is confusing.
			if m.aiStreaming {
				chatBody = m.chatViewport.View()
			} else {
				inputLabel := m.renderChatInputLabel(cw)
				chatBody = m.chatViewport.View() + "\n" + inputLabel + "\n" + m.chatInput.View()
			}
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
	//
	// The modal contract: each renderer returns (content, ok). ok=false
	// means there is nothing meaningful to display, so we fall through
	// to the base view instead of drawing an empty bordered box. (Closing
	// the modal flag itself happens in Update() where the state actually
	// lives — View() stays pure.)
	if m.showHelp {
		if content, ok := m.renderHelpModal(); ok {
			return centerOverlay(content, m.width, m.height)
		}
	}
	if m.showModelPicker {
		if content, ok := m.renderModelPicker(); ok {
			return centerOverlay(content, m.width, m.height)
		}
	}
	if m.showSubmitReview {
		if content, ok := m.renderSubmitReviewModal(); ok {
			return centerOverlay(content, m.width, m.height)
		}
	}
	if m.showThemePicker {
		if content, ok := m.renderThemePicker(); ok {
			return floatOverlay(base, content, m.width, m.height)
		}
	}
	if m.errorMsg != "" {
		if content, ok := m.renderErrorModal(); ok {
			return centerOverlay(content, m.width, m.height)
		}
	}
	// Confirm/action overlays float at bottom (no background dimming)
	// Permission/question overlays take priority
	if m.permissionOverlay != nil {
		if content, ok := m.renderPermissionModal(); ok {
			return bottomOverlay(base, content, m.width, m.height)
		}
	}
	if m.questionOverlay != nil {
		if content, ok := m.renderQuestionModal(); ok {
			return bottomOverlay(base, content, m.width, m.height)
		}
	}
	if m.confirmOverlay != nil {
		if content, ok := m.renderConfirmModal(); ok {
			return bottomOverlay(base, content, m.width, m.height)
		}
	}
	if m.actionMenuOverlay != nil {
		if content, ok := m.renderActionMenu(); ok {
			return bottomOverlay(base, content, m.width, m.height)
		}
	}
	// Flash message: show briefly in the footer area
	if m.flashMsg != "" {
		return bottomOverlay(base, styleAccentGreen.Render(m.flashMsg), m.width, m.height)
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
	return Box{
		Width:   width,
		Height:  height,
		Title:   title,
		Focused: focused,
	}.Render(content)
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
	gap := max(m.width-ansi.StringWidth(left)-ansi.StringWidth(right), 1)

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
		if m.aiPanelTab == tabReview {
			bindings = append(bindings,
				struct{ key, desc string }{"j/k", "findings"},
				struct{ key, desc string }{"Enter", "jump"},
			)
		} else if m.aiPanelTab == tabChat {
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
			struct{ key, desc string }{"a", "ai review"},
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

package ui

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"prr/internal/ai"
	"prr/internal/config"
	"prr/internal/git"
	"prr/internal/state"

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
	State    *state.State
	RawDiffs map[string]string // filePath -> raw diff content
	Err      error
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
}

// aiStreamTickMsg triggers a batched render of accumulated AI tokens.
type aiStreamTickMsg struct{}

// AIReviewProgressMsg tracks multi-pass review progress.
type AIReviewProgressMsg struct {
	Batch int    // current batch (1-indexed)
	Total int    // total batches
	Label string // e.g. "internal/ui"
	Phase string // "batch" or "synthesis"
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

// ChatRenderedMsg is sent when chat/review markdown rendering completes.
type ChatRenderedMsg struct {
	FilePath string // which file this render is for ("" = overview)
	Content  string // fully rendered content
	Tab      int    // 0 = review, 1 = chat
}

// logoTickMsg drives the logo color animation during loading.
type logoTickMsg struct{}

// logo is the ASCII art displayed on the loading screen.
var logoLines = [2]string{
	"█▀█ █▀█ █▀█",
	"█▀▀ █▀▄ █▀▄",
}

// ── Model ───────────────────────────────────────────────────────────────

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
	selectedFile string // currently selected file path ("" = overview)
	rawDiffs     map[string]string // filePath -> raw diff (for AI context)

	// Diff context
	contextLines int // number of context lines for git diff (-U<n>)

	// AI
	aiClient           ai.Client
	aiStreaming         bool   // true while AI is generating
	aiStreamBuffer     string // accumulated streamed response
	aiStreamDirty      bool   // true when buffer has unflushed tokens
	aiCancelFn         context.CancelFunc
	aiChatHistoryCache string // pre-rendered markdown of completed messages (for streaming perf)
	aiReviewBatch      int    // current batch during multi-pass review (0 = not in multi-pass)
	aiReviewTotal      int    // total batches
	aiReviewLabel      string // current batch label (e.g. "internal/ui")
	aiReviewPhase      string // "batch" or "synthesis"
	aiPanelTab         int    // 0 = Review, 1 = Chat

	// Custom review instructions loaded from .prr/instructions.md
	customInstructions string

	// Comments
	comments       map[string][]git.ReviewComment // filePath -> comments
	commenting     bool                           // true when comment input is active
	commentInput   textarea.Model                 // input for new comment
	commentLine    int                            // line number for new comment
	commentSide    string                         // "LEFT" or "RIGHT"
	diffCursor     int                            // cursor position within visible diff lines (for line selection)

	// Panel visibility
	showFilePanel bool
	showAIPanel   bool

	// Loading animation
	logoFrame int // color animation frame counter
}

// ── Constructor ─────────────────────────────────────────────────────────

func NewModel(prNumber string, aiClient ai.Client) Model {
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

	return Model{
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
		contextLines:       3,
		comments:           make(map[string][]git.ReviewComment),
		commentInput:       commentTa,
		showFilePanel:      true,
		showAIPanel:        true,
		customInstructions: config.LoadCustomInstructions(),
	}
}

func (m Model) Init() tea.Cmd {
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
		for _, f := range files {
			rawDiff, err := git.GetRawDiff(base, head, f.Path)
			if err != nil {
				log.Printf("Warning: failed to get raw diff for %s: %v", f.Path, err)
				continue
			}
			hashes[f.Path] = git.HashDiff(rawDiff)
			rawDiffs[f.Path] = rawDiff
		}

		// Sync state with current diffs
		st.SyncWithDiffs(hashes)

		// Save updated state
		if err := state.Save(st); err != nil {
			log.Printf("Warning: failed to save state: %v", err)
		}

		return DiffHashedMsg{State: st, RawDiffs: rawDiffs, Err: nil}
	}
}

func fetchStyledDiff(base, head, filePath string, contextLines int, reload bool) tea.Cmd {
	return func() tea.Msg {
		content, err := git.GetStyledDiffWithContext(base, head, filePath, contextLines)
		return StyledDiffMsg{FilePath: filePath, Content: content, Err: err, Reload: reload}
	}
}

func fetchComments(prNumber string) tea.Cmd {
	return func() tea.Msg {
		comments, err := git.FetchReviewComments(prNumber)
		return CommentsFetchedMsg{Comments: comments, Err: err}
	}
}

func createComment(prNumber, commitSHA, path, body string, line int, side string) tea.Cmd {
	return func() tea.Msg {
		comment, err := git.CreateReviewComment(prNumber, commitSHA, path, body, line, side)
		return CommentCreatedMsg{Comment: comment, Err: err}
	}
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

// streamAIReview is like streamAIChat but also produces an AIReview for persistence.
func streamAIReview(client ai.Client, ctx context.Context, systemPrompt string, messages []ai.Message, p *tea.Program) tea.Cmd {
	return func() tea.Msg {
		fullResponse, err := client.ChatStream(ctx, systemPrompt, messages, func(token string) {
			p.Send(AIChatDeltaMsg{Token: token})
		})
		if err != nil {
			return AIChatDoneMsg{FullResponse: fullResponse, Err: err}
		}
		return AIChatDoneMsg{
			FullResponse: fullResponse,
			Review: &state.AIReview{
				Summary: fullResponse,
			},
		}
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
		if m.loading || m.aiStreaming {
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
			m.diffViewport.SetContent(
				styleAccentRed.Render(
					fmt.Sprintf("Error fetching PR: %v", msg.Err)))
			return m, nil
		}
		m.pr = msg.PR
		// Configure AI tools with the PR head and base refs
		if tc, ok := m.aiClient.(ai.ToolConfigurer); ok {
			tc.SetHeadRef(fmt.Sprintf("origin/%s", m.pr.HeadRefName))
			tc.SetBaseRef(fmt.Sprintf("origin/%s", m.pr.BaseRefName))
		}
		m.loadingMsg = "Fetching git refs..."
		return m, fetchRefs(m.pr.BaseRefName, m.pr.HeadRefName, m.pr.HeadRefOid)

	case RefsFetchedMsg:
		if msg.Err != nil {
			m.loading = false
			m.loadingMsg = ""
			m.diffViewport.SetContent(
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
			m.diffViewport.SetContent(
				styleAccentRed.Render(
					fmt.Sprintf("Error syncing state: %v", msg.Err)))
			m.populateFileList(nil)
			return m, nil
		}
		m.reviewState = msg.State
		m.rawDiffs = msg.RawDiffs
		// Provide diffs to AI tool executor for the get_diff tool
		if tc, ok := m.aiClient.(ai.ToolConfigurer); ok {
			tc.SetRawDiffs(msg.RawDiffs)
		}
		m.populateFileList(m.reviewState)
		m.selectedFile = ""
		m.diffViewport.SetContent(m.renderOverview())
		m.diffViewport.GotoTop()
		chatCmd := m.renderActiveAIView()
		return m, tea.Batch(fetchComments(m.prNumber), chatCmd)

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

	case CommentCreatedMsg:
		if msg.Err != nil {
			m.diffViewport.SetContent(
				styleAccentRed.Render(
					fmt.Sprintf("Error posting comment: %v", msg.Err)))
		} else if msg.Comment != nil {
			// Add to local state
			m.comments[msg.Comment.Path] = append(m.comments[msg.Comment.Path], *msg.Comment)
			// Re-render the diff with the new comment
			if m.selectedFile == msg.Comment.Path {
				cmd = m.reloadDiff()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		return m, tea.Batch(cmds...)

	case ChatRenderedMsg:
		// Only apply if we're still looking at the same file and tab
		if msg.FilePath == m.selectedFile && msg.Tab == m.aiPanelTab {
			m.chatViewport.SetContent(msg.Content)
			m.chatViewport.GotoTop()
		}
		return m, nil

	case StyledDiffMsg:
		if msg.Err != nil {
			m.diffViewport.SetContent(
				styleAccentRed.Render(
					fmt.Sprintf("Error loading diff: %v", msg.Err)))
		} else {
			if msg.FilePath == m.selectedFile {
				content := m.injectComments(msg.Content, msg.FilePath)
				savedOffset := m.diffViewport.YOffset
				savedCursor := m.diffCursor
				m.diffViewport.SetContent(content)
				if msg.Reload {
					m.diffViewport.SetYOffset(savedOffset)
					m.diffCursor = savedCursor
				} else {
					m.diffViewport.GotoTop()
					m.diffCursor = 0
				}
			}
		}
		return m, nil

	case AIReviewProgressMsg:
		m.aiReviewBatch = msg.Batch
		m.aiReviewTotal = msg.Total
		m.aiReviewLabel = msg.Label
		m.aiReviewPhase = msg.Phase
		return m, nil

	case AIChatDeltaMsg:
		if m.aiStreaming {
			token := msg.Token
			if strings.HasPrefix(token, "\x00THOUGHT:") {
				// Thought text — render with dim/italic style
				thought := strings.TrimPrefix(token, "\x00THOUGHT:")
				m.aiStreamBuffer += styleThought.Render(thought)
			} else if strings.HasPrefix(token, "\x00TOOL:") {
				// Tool call — render as a subtle indicator
				tool := strings.TrimPrefix(token, "\x00TOOL:")
				m.aiStreamBuffer += "\n" + styleToolCall.Render("  ▸ " + tool) + "\n"
			} else if strings.HasPrefix(token, "\x00DIM:") {
				// Batch review output — muted to show it's intermediate
				dimText := strings.TrimPrefix(token, "\x00DIM:")
				m.aiStreamBuffer += styleBatchOutput.Render(dimText)
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

	case AIChatDoneMsg:
		m.aiStreaming = false
		m.aiCancelFn = nil
		m.aiStreamDirty = false // prevent stale tick from rendering after completion
		m.aiReviewBatch = 0
		m.aiReviewTotal = 0
		m.aiReviewLabel = ""
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
				m.saveReview(msg.Review)
			}
			// Save the completed response to state and re-render async with markdown
			cmd = m.saveAIResponse(msg.FullResponse)
			// Clear stream buffer so the raw text doesn't linger while markdown renders
			m.aiStreamBuffer = ""
			m.aiChatHistoryCache = ""
			// If a review was produced, auto-switch to the Review tab
			if msg.Review != nil {
				m.aiPanelTab = 0
				cmd = m.renderActiveAIView()
			}
		}
		return m, cmd

	case tea.KeyMsg:
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
				return m, nil
			case "ctrl+s":
				// Submit comment
				body := strings.TrimSpace(m.commentInput.Value())
				m.commenting = false
				m.commentInput.Blur()
				m.commentInput.Reset()
				if body != "" && m.pr != nil && m.selectedFile != "" {
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
			} else if m.focusedPane == PaneChat {
				cmd = m.sendChatMessage()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
				return m, cmd
			}
		case "+", "=":
			// Increase diff context
			if m.focusedPane == PaneDiff && m.selectedFile != "" {
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
			if m.focusedPane == PaneDiff && m.selectedFile != "" {
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
		case "n":
			// Jump to next unreviewed file
			if m.focusedPane == PaneFileList {
				m.jumpToUnreviewed(1)
			}
		case "p":
			// Jump to previous unreviewed file
			if m.focusedPane == PaneFileList {
				m.jumpToUnreviewed(-1)
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
		}
	}

	switch m.focusedPane {
	case PaneFileList:
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "j", "down":
				m.fileTree.moveDown()
				cmd = m.previewCurrentFile()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case "k", "up":
				m.fileTree.moveUp()
				cmd = m.previewCurrentFile()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
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
				// Collapse dir
				if m.fileTree.selectedIsDir() {
					entry := m.fileTree.flat[m.fileTree.cursor]
					if entry.node.expanded {
						m.fileTree.toggle()
					}
				}
			case " ":
				// Toggle dir expand/collapse, or toggle file review status
				if m.fileTree.selectedIsDir() {
					m.fileTree.toggle()
				} else {
					m.toggleReviewStatus()
				}
			case "r":
				m.fileTree.toggleHideReviewed()
				m.syncLayout()
			}
		}
	case PaneDiff:
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "c":
				if m.selectedFile != "" && !m.commenting {
					info := m.getDiffCursorInfo()
					if info.line > 0 {
						m.commentLine = info.line
						m.commentSide = info.side
						m.commenting = true
						m.commentInput.Reset()
						m.commentInput.Focus()
						return m, textarea.Blink
					}
				}
			case "j", "down":
				m.moveDiffCursor(1)
				return m, nil
			case "k", "up":
				m.moveDiffCursor(-1)
				return m, nil
			default:
				m.diffViewport, cmd = m.diffViewport.Update(msg)
				cmds = append(cmds, cmd)
			}
		} else {
			m.diffViewport, cmd = m.diffViewport.Update(msg)
			cmds = append(cmds, cmd)
		}
	case PaneChat:
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "pgup", "pgdown":
				m.chatViewport, cmd = m.chatViewport.Update(msg)
				cmds = append(cmds, cmd)
			case "ctrl+k":
				m.clearChat()
			case "tab":
				// Toggle between Review and Chat tabs
				m.aiPanelTab = 1 - m.aiPanelTab
				cmds = append(cmds, m.renderActiveAIView())
			default:
				// Only allow text input on the Chat tab
				if m.aiPanelTab == 1 {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	m.aiCancelFn = cancel

	return tea.Batch(m.spinner.Tick, streamAIChat(m.aiClient, ctx, systemPrompt, aiMessages, program))
}

func (m *Model) appendMessageToState(msg state.Message) {
	if m.reviewState == nil {
		return
	}

	if m.selectedFile == "" {
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

	if m.selectedFile == "" {
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

	if m.selectedFile == "" {
		// PR overview: file listing with stats — diffs are fetched via get_diff tool
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
		allDiffs.WriteString("\nUse the get_diff tool to read the actual diffs (paginated by file).\n")

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
	if m.selectedFile == "" {
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
	needsContext := m.selectedFile != "" && len(messages) > 0
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

	if m.selectedFile == "" {
		m.reviewState.Review = review
	} else {
		fs, ok := m.reviewState.Files[m.selectedFile]
		if !ok {
			fs = &state.FileState{Status: state.StatusUnreviewed}
			m.reviewState.Files[m.selectedFile] = fs
		}
		fs.Review = review
	}

	if err := state.Save(m.reviewState); err != nil {
		log.Printf("Warning: failed to save review state: %v", err)
	}
}

func (m *Model) clearChat() {
	if m.reviewState == nil || m.aiStreaming {
		return
	}

	if m.selectedFile == "" {
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
				hb.WriteString(styleTextSecondary.Render(msg.Content) + "\n\n")
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

// renderAIPanelTitle builds the AI panel title with tab indicators and streaming status.
func (m Model) renderAIPanelTitle(maxWidth int) string {
	// During streaming, show progress or spinner instead of tabs
	if m.aiStreaming {
		if m.aiReviewTotal > 0 {
			return m.renderReviewProgress(maxWidth)
		}
		tabLabel := "CHAT"
		if m.aiPanelTab == 0 {
			tabLabel = "REVIEW"
		}
		return tabLabel + " " + m.spinner.View()
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

// renderReviewProgress renders the AI CHAT panel title with a progress bar
// during multi-pass review.
func (m Model) renderReviewProgress(maxWidth int) string {
	spin := m.spinner.View()

	var label string
	if m.aiReviewPhase == "synthesis" {
		label = "Synthesizing"
	} else {
		label = fmt.Sprintf("%d/%d %s", m.aiReviewBatch, m.aiReviewTotal, m.aiReviewLabel)
	}

	// Build progress bar: ████░░░░
	barWidth := 10
	if m.aiReviewTotal > 0 && m.aiReviewPhase != "synthesis" {
		filled := (m.aiReviewBatch * barWidth) / m.aiReviewTotal
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
		userContent := "Please review the full set of changes in this PR."

		// Add to state
		userStateMsg := state.Message{Role: "user", Content: userContent}
		m.appendMessageToState(userStateMsg)

		// Start streaming
		m.aiStreaming = true
		m.aiStreamBuffer = ""
		m.aiChatHistoryCache = ""
		m.updateChatViewWithStream()

		// Longer timeout for multi-pass (multiple sequential AI calls)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		m.aiCancelFn = cancel

		return tea.Batch(m.spinner.Tick, streamMultiPassReview(ctx, m.aiClient, prMeta, m.rawDiffs, m.customInstructions, program))
	}

	// Single file mode
	if m.selectedFile != path {
		m.selectedFile = path
	}
	diff := m.rawDiffs[path]
	if diff == "" {
		return nil
	}

	// Skip files that are excluded from review (lock files, generated code, etc.)
	if config.ShouldExcludeFromReview(path) {
		m.aiPanelTab = 0 // switch to Review tab so user sees the message
		m.chatViewport.SetContent(
			styleTextMuted.Render("This file is excluded from AI review (lock file, generated code, or vendored dependency)."))
		return nil
	}

	// System prompt is instructions-only; diff goes in the user message
	systemPrompt := m.withInstructions(ai.ReviewFilePrompt)

	// For large diffs, instruct the AI to use the get_diff tool instead of
	// pasting the full diff inline. This avoids hitting context limits.
	const largeDiffThreshold = 5000
	var userContent string
	if len(diff) > largeDiffThreshold {
		userContent = fmt.Sprintf(
			"Please review the changes to `%s`. The diff is large (%d chars), so use the get_diff tool to read it.",
			path, len(diff),
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

	// Start streaming
	m.aiStreaming = true
	m.aiStreamBuffer = ""
	m.aiChatHistoryCache = "" // invalidate so it's rebuilt on first tick
	m.updateChatViewWithStream()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	m.aiCancelFn = cancel

	return tea.Batch(m.spinner.Tick, streamAIReview(m.aiClient, ctx, systemPrompt, aiMessages, program))
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
		if entry.node.isDir {
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

func (m *Model) toggleReviewStatus() {
	path := m.fileTree.selectedPath()
	if path == "" || m.reviewState == nil {
		return
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

	// Save state
	if err := state.Save(m.reviewState); err != nil {
		log.Printf("Warning: failed to save review state: %v", err)
	}
}

func (m *Model) reloadDiff() tea.Cmd {
	if m.selectedFile == "" || m.pr == nil {
		return nil
	}
	// Don't replace content with "Loading..." — keep current diff visible to avoid flicker
	return fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, m.selectedFile, m.contextLines, true)
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
		files = append(files, fileInfo{
			path:      f.Path,
			additions: f.Additions,
			deletions: f.Deletions,
			status:    status,
		})
	}

	m.fileTree = newFileTree(files)
	m.fileTree.height = m.contentHeight() - 2 // account for border
}

// previewCurrentFile loads the diff for the currently highlighted file
// without changing pane focus. Skips dirs and avoids reloading the same file.
func (m *Model) previewCurrentFile() tea.Cmd {
	if m.pr == nil || m.fileTree.selectedIsDir() {
		return nil
	}

	if m.fileTree.selectedIsOverview() {
		if m.selectedFile == "" {
			return nil // already showing overview
		}
		m.selectedFile = ""
		m.diffViewport.SetContent(m.renderOverview())
		m.diffViewport.GotoTop()
		m.diffCursor = 0
		return m.renderActiveAIView()
	}

	path := m.fileTree.selectedPath()
	if path == "" || path == m.selectedFile {
		return nil
	}

	m.selectedFile = path
	m.diffViewport.SetContent(
		styleTextMuted.Render("Loading diff..."))
	chatCmd := m.renderActiveAIView()
	diffCmd := fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, path, m.contextLines, false)
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
		m.diffViewport.SetContent(m.renderOverview())
		m.diffViewport.GotoTop()
		m.diffCursor = 0
		return m.renderActiveAIView()
	}

	path := m.fileTree.selectedPath()
	if path == "" {
		return nil
	}

	m.selectedFile = path
	m.diffViewport.SetContent(
		styleTextMuted.Render("Loading diff..."))
	chatCmd := m.renderActiveAIView()
	diffCmd := fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, path, m.contextLines, false)
	return tea.Batch(chatCmd, diffCmd)
}

// renderActiveAIView renders the currently selected AI panel tab for the current file.
func (m *Model) renderActiveAIView() tea.Cmd {
	if m.aiPanelTab == 0 {
		return m.renderReviewForFile(m.selectedFile)
	}
	return m.renderChatForFile(m.selectedFile)
}

// renderReviewForFile renders the AI review content for a file (or PR overview).
func (m *Model) renderReviewForFile(filePath string) tea.Cmd {
	if m.reviewState == nil {
		m.chatViewport.SetContent(
			styleTextMuted.Render("No AI review yet. Press 'a' to start a review."))
		return nil
	}

	var review *state.AIReview

	if filePath == "" {
		review = m.reviewState.Review
	} else {
		if fs, ok := m.reviewState.Files[filePath]; ok {
			review = fs.Review
		}
	}

	if review == nil {
		hint := "Press 'a' to start an AI review"
		if filePath == "" {
			hint += " of this PR."
		} else {
			hint += " of this file."
		}
		m.chatViewport.SetContent(styleTextMuted.Render(hint))
		return nil
	}

	// Show placeholder, render markdown async
	m.chatViewport.SetContent(styleTextMuted.Render("Rendering review..."))

	width := m.chatViewport.Width - 2
	summary := review.Summary
	findings := review.Findings
	fp := filePath

	return func() tea.Msg {
		var b strings.Builder

		// Render findings if present (PR-level multi-pass)
		if findings != "" {
			b.WriteString(styleTextMuted.Render("─── Per-file Findings ───") + "\n\n")
			b.WriteString(renderMarkdown(findings, width) + "\n\n")
			b.WriteString(styleTextMuted.Render("─── Final Review ───") + "\n\n")
		}

		b.WriteString(renderMarkdown(summary, width))
		return ChatRenderedMsg{FilePath: fp, Content: b.String(), Tab: 0}
	}
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
				b.WriteString(styleTextSecondary.Render(msg.Content) + "\n\n")
			case "assistant":
				b.WriteString(styleAccentMauveBold.Render("AI") + "\n")
				b.WriteString(renderMarkdown(msg.Content, width) + "\n\n")
			default:
				b.WriteString(styleTextMuted.Render(msg.Role) + "\n")
				b.WriteString(styleTextSecondary.Render(msg.Content) + "\n\n")
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
	m.diffViewport.Height = ih - 2

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
	if m.focusedPane != PaneDiff || m.selectedFile == "" {
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
		// Pad line to full width so the highlight spans the row
		visible := ansi.StringWidth(line)
		if visible < w {
			line = line + strings.Repeat(" ", w-visible)
		}
		lines[m.diffCursor] = highlight.Render(line)
	}

	return strings.Join(lines, "\n"), cursorLineNum
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

	cols := m.columns()
	ih := m.contentHeight()

	header := m.viewHeader()

	diffTitle := "OVERVIEW"
	diffBody, cursorLineNum := m.renderDiffWithCursor()
	if m.selectedFile != "" {
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

	return header + "\n" + panes + "\n" + footer
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

	prInfo := styleTextPrimary.Render(fmt.Sprintf(" · PR #%s", m.prNumber))

	// PR metadata: repo, author, state
	var meta string
	if m.pr != nil {
		var parts []string

		// Repository name (owner/repo)
		if m.pr.HeadRepository.Owner.Login != "" && m.pr.HeadRepository.Name != "" {
			repo := m.pr.HeadRepository.Owner.Login + "/" + m.pr.HeadRepository.Name
			parts = append(parts, styleTextMuted.Render(repo))
		}

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

	// Calculate how much room we have for the PR title
	right := reviewBadge + " "
	fixedW := ansi.StringWidth(" "+logo+prInfo+meta) + ansi.StringWidth(right) + 2 // 2 for min gap
	maxTitleW := m.width - fixedW

	prTitle := ""
	if m.pr != nil && maxTitleW > 4 {
		t := m.pr.Title
		if len(t) > maxTitleW-3 { // -3 for " · " prefix
			t = t[:maxTitleW-6] + "..."
		}
		prTitle = styleTextSecondary.Render(fmt.Sprintf(" · %s", t))
	}

	left := " " + logo + prInfo + prTitle + meta
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

	// Common bindings
	bindings := []struct{ key, desc string }{
		{"S-Tab", "prev pane"},
	}

	// Pane-specific bindings
	switch m.focusedPane {
	case PaneFileList:
		// Tab cycles panes from file list
		bindings = append([]struct{ key, desc string }{{"Tab", "next pane"}}, bindings...)
		aiLabel := "AI review file"
		if m.selectedFile == "" {
			aiLabel = "AI review PR"
		}
		rLabel := "hide reviewed"
		if m.fileTree.hideReviewed {
			rLabel = "show all"
		}
		bindings = append(bindings,
			struct{ key, desc string }{"j/k", "navigate"},
			struct{ key, desc string }{"Enter", "select"},
			struct{ key, desc string }{"Space", "review"},
			struct{ key, desc string }{"h/l", "collapse/expand"},
			struct{ key, desc string }{"r", rLabel},
			struct{ key, desc string }{"n/p", "next/prev unreviewed"},
			struct{ key, desc string }{"a", aiLabel},
		)
	case PaneDiff:
		// Tab cycles panes from diff
		bindings = append([]struct{ key, desc string }{{"Tab", "next pane"}}, bindings...)
		aiLabel := "AI review file"
		if m.selectedFile == "" {
			aiLabel = "AI review PR"
		}
		bindings = append(bindings,
			struct{ key, desc string }{"j/k", "select line"},
			struct{ key, desc string }{"+/−", "context"},
			struct{ key, desc string }{"c", "comment"},
			struct{ key, desc string }{"a", aiLabel},
		)
	case PaneChat:
		// Tab toggles Review/Chat tabs in the AI panel
		bindings = append([]struct{ key, desc string }{{"Tab", "review/chat"}}, bindings...)
		if m.aiPanelTab == 1 {
			bindings = append(bindings,
				struct{ key, desc string }{"Enter", "send"},
			)
		}
		if m.aiStreaming {
			bindings = append(bindings,
				struct{ key, desc string }{"Esc", "cancel AI"},
			)
		}
	}

	bindings = append(bindings,
		struct{ key, desc string }{"^B", "files"},
		struct{ key, desc string }{"^A", "AI"},
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

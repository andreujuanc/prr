package ui

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"prr/internal/ai"
	"prr/internal/git"
	"prr/internal/state"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// ── Model ───────────────────────────────────────────────────────────────

type Model struct {
	fileTree     fileTree
	diffViewport viewport.Model
	chatViewport viewport.Model
	chatInput    textarea.Model

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
	aiClient       ai.Client
	aiStreaming     bool   // true while AI is generating
	aiStreamBuffer string // accumulated streamed response
	aiCancelFn     context.CancelFunc

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
}

// ── Constructor ─────────────────────────────────────────────────────────

func NewModel(prNumber string, aiClient ai.Client) Model {
	diffVp := viewport.New(0, 0)
	diffVp.Style = lipgloss.NewStyle().Foreground(textPrimary)

	chatVp := viewport.New(0, 0)

	ta := textarea.New()
	ta.Placeholder = "Ask about this code..."
	ta.Prompt = "› "
	ta.CharLimit = 500
	ta.SetWidth(30)
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.FocusedStyle.Base = lipgloss.NewStyle().Background(surfaceBg)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(surfaceBg)
	ta.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(accentBlue).Background(surfaceBg).Bold(true)
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(textMuted).Background(surfaceBg)
	ta.FocusedStyle.Text = lipgloss.NewStyle().Foreground(textPrimary).Background(surfaceBg)
	ta.BlurredStyle.Base = lipgloss.NewStyle().Background(lipgloss.Color("#252535"))
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.Color("#252535"))
	ta.BlurredStyle.Prompt = lipgloss.NewStyle().Foreground(textMuted).Background(lipgloss.Color("#252535"))
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

	return Model{
		fileTree:     newFileTree(nil),
		diffViewport: diffVp,
		chatViewport: chatVp,
		chatInput:    ta,
		focusedPane:  PaneFileList,
		prNumber:     prNumber,
		loading:      true,
		loadingMsg:   "Fetching PR data...",
		aiClient:     aiClient,
		contextLines: 3,
		comments:     make(map[string][]git.ReviewComment),
		commentInput: commentTa,
		showFilePanel: true,
		showAIPanel:   true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, fetchPR(m.prNumber))
}

// ── Async commands ──────────────────────────────────────────────────────

func fetchPR(prNumber string) tea.Cmd {
	return func() tea.Msg {
		pr, err := git.FetchPR(prNumber)
		return PRFetchedMsg{PR: pr, Err: err}
	}
}

func fetchRefs(base, head string) tea.Cmd {
	return func() tea.Msg {
		err := git.FetchRefs(base, head)
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
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.syncLayout()

	case PRFetchedMsg:
		if msg.Err != nil {
			m.loading = false
			m.loadingMsg = ""
			m.diffViewport.SetContent(
				lipgloss.NewStyle().Foreground(accentRed).Render(
					fmt.Sprintf("Error fetching PR: %v", msg.Err)))
			return m, nil
		}
		m.pr = msg.PR
		// Configure AI tools with the PR head ref
		if tc, ok := m.aiClient.(ai.ToolConfigurer); ok {
			tc.SetHeadRef(fmt.Sprintf("origin/%s", m.pr.HeadRefName))
		}
		m.loadingMsg = "Fetching git refs..."
		return m, fetchRefs(m.pr.BaseRefName, m.pr.HeadRefName)

	case RefsFetchedMsg:
		if msg.Err != nil {
			m.loading = false
			m.loadingMsg = ""
			m.diffViewport.SetContent(
				lipgloss.NewStyle().Foreground(accentRed).Render(
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
				lipgloss.NewStyle().Foreground(accentRed).Render(
					fmt.Sprintf("Error syncing state: %v", msg.Err)))
			m.populateFileList(nil)
			return m, nil
		}
		m.reviewState = msg.State
		m.rawDiffs = msg.RawDiffs
		m.populateFileList(m.reviewState)
		m.selectedFile = ""
		m.diffViewport.SetContent(
			lipgloss.NewStyle().Foreground(textMuted).Render(
				"Select a file to view its diff"))
		m.renderChatForFile("")
		return m, fetchComments(m.prNumber)

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
				lipgloss.NewStyle().Foreground(accentRed).Render(
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

	case StyledDiffMsg:
		if msg.Err != nil {
			m.diffViewport.SetContent(
				lipgloss.NewStyle().Foreground(accentRed).Render(
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

	case AIChatDeltaMsg:
		if m.aiStreaming {
			m.aiStreamBuffer += msg.Token
			m.updateChatViewWithStream()
		}
		return m, nil

	case AIChatDoneMsg:
		m.aiStreaming = false
		m.aiCancelFn = nil
		if msg.Err != nil {
			// Append error to chat view
			m.aiStreamBuffer += "\n\n" + lipgloss.NewStyle().Foreground(accentRed).Render(
				fmt.Sprintf("[error: %v]", msg.Err))
			m.updateChatViewWithStream()
		} else {
			// Save the completed response to state
			m.saveAIResponse(msg.FullResponse)
		}
		return m, nil

	case tea.KeyMsg:
		// While AI is streaming, allow navigation but block chat input
		if m.aiStreaming {
			if msg.String() == "esc" || msg.String() == "ctrl+c" {
				if m.aiCancelFn != nil {
					m.aiCancelFn()
				}
				m.aiStreaming = false
				m.aiStreamBuffer += "\n" + lipgloss.NewStyle().Foreground(accentYellow).Render("[cancelled]")
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
					cmd = m.selectCurrentFile()
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
					// Move focus to diff pane
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
			default:
				m.chatInput, cmd = m.chatInput.Update(msg)
				cmds = append(cmds, cmd)
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

	// Clear input
	m.chatInput.Reset()

	// Add user message to state
	userMsg := state.Message{Role: "user", Content: text}
	m.appendMessageToState(userMsg)

	// Build conversation history for AI
	messages := m.buildAIMessages()

	// Determine system prompt and diff context
	systemPrompt, diffContext := m.getAIContext()

	// Prepend diff context to the system prompt
	if diffContext != "" {
		systemPrompt = systemPrompt + "\n\nHere is the code diff for context:\n```\n" + diffContext + "\n```"
	}

	// Convert state messages to AI messages
	var aiMessages []ai.Message
	for _, msg := range messages {
		aiMessages = append(aiMessages, ai.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// Start streaming
	m.aiStreaming = true
	m.aiStreamBuffer = ""

	// Render chat with the new user message and streaming indicator
	m.updateChatViewWithStream()

	ctx, cancel := context.WithCancel(context.Background())
	m.aiCancelFn = cancel

	return streamAIChat(m.aiClient, ctx, systemPrompt, aiMessages, program)
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
		// PR overview: use all diffs (truncated)
		var allDiffs strings.Builder
		allDiffs.WriteString(meta.String())
		for path, diff := range m.rawDiffs {
			allDiffs.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", path, diff))
			// Truncate at ~50k chars to avoid token limits
			if allDiffs.Len() > 50000 {
				allDiffs.WriteString("... (remaining files truncated)")
				break
			}
		}
		return ai.ReviewPRPrompt, allDiffs.String()
	}

	// Single file
	diff := m.rawDiffs[m.selectedFile]
	return ai.ChatPrompt, meta.String() + diff
}

func (m *Model) saveAIResponse(response string) {
	if m.reviewState == nil {
		return
	}

	assistantMsg := state.Message{Role: "assistant", Content: response}
	m.appendMessageToState(assistantMsg)

	// Persist to disk
	if err := state.Save(m.reviewState); err != nil {
		log.Printf("Warning: failed to save chat state: %v", err)
	}

	// Re-render chat from state (clean render without streaming artifacts)
	m.renderChatForFile(m.selectedFile)
}

func (m *Model) updateChatViewWithStream() {
	// Render existing messages + streaming response
	var b strings.Builder

	you := lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
	aiStyle := lipgloss.NewStyle().Foreground(accentMauve).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(textSecondary)

	// Render existing completed messages from state
	messages := m.buildAIMessages()
	// Show all messages except the streaming one (last user message is already in state)
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			b.WriteString(you.Render("You") + "\n")
		case "assistant":
			b.WriteString(aiStyle.Render("AI") + "\n")
		}
		b.WriteString(msgStyle.Render(msg.Content) + "\n\n")
	}

	// Render streaming AI response
	if m.aiStreaming || m.aiStreamBuffer != "" {
		b.WriteString(aiStyle.Render("AI") + "\n")
		if m.aiStreamBuffer == "" {
			b.WriteString(lipgloss.NewStyle().Foreground(textMuted).Render("thinking...") + "\n")
		} else {
			b.WriteString(msgStyle.Render(m.aiStreamBuffer))
			if m.aiStreaming {
				b.WriteString(lipgloss.NewStyle().Foreground(accentBlue).Render("▊"))
			}
			b.WriteString("\n")
		}
	}

	m.chatViewport.SetContent(b.String())
	m.chatViewport.GotoBottom()
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
			rw = lipgloss.Width(string(r))
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
	totalVisible := len(strings.Split(m.diffViewport.View(), "\n"))
	if newCursor >= totalVisible {
		return
	}

	m.diffCursor = newCursor
}

// ── Data helpers ────────────────────────────────────────────────────────

func (m *Model) triggerAIReview() tea.Cmd {
	// Determine which file to review
	path := m.selectedFile
	if path == "" {
		// If no file selected yet, use tree cursor
		path = m.fileTree.selectedPath()
	}
	if path == "" || m.aiClient == nil || program == nil || m.pr == nil {
		return nil
	}

	// Select the file if not already selected
	if m.selectedFile != path {
		m.selectedFile = path
	}

	// Build a one-shot review message
	diff := m.rawDiffs[path]
	if diff == "" {
		return nil
	}

	systemPrompt := ai.ReviewFilePrompt + "\n\nHere is the code diff for the file `" + path + "`:\n```\n" + diff + "\n```"

	reviewMsg := ai.Message{
		Role:    "user",
		Content: "Please review this file's changes.",
	}

	// Add to state
	userStateMsg := state.Message{Role: "user", Content: "Please review this file's changes."}
	m.appendMessageToState(userStateMsg)

	// Start streaming
	m.aiStreaming = true
	m.aiStreamBuffer = ""
	m.updateChatViewWithStream()

	ctx, cancel := context.WithCancel(context.Background())
	m.aiCancelFn = cancel

	return streamAIChat(m.aiClient, ctx, systemPrompt, []ai.Message{reviewMsg}, program)
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
	if m.fileTree.selectedIsDir() {
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
		m.renderChatForFile("")
		return nil
	}

	path := m.fileTree.selectedPath()
	if path == "" || path == m.selectedFile {
		return nil
	}

	m.selectedFile = path
	m.diffViewport.SetContent(
		lipgloss.NewStyle().Foreground(textMuted).Render("Loading diff..."))
	m.renderChatForFile(path)
	return fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, path, m.contextLines, false)
}

func (m *Model) selectCurrentFile() tea.Cmd {
	if m.fileTree.selectedIsDir() {
		m.fileTree.toggle()
		return nil
	}

	if m.fileTree.selectedIsOverview() {
		m.selectedFile = ""
		m.diffViewport.SetContent(m.renderOverview())
		m.diffViewport.GotoTop()
		m.diffCursor = 0
		m.renderChatForFile("")
		return nil
	}

	path := m.fileTree.selectedPath()
	if path == "" {
		return nil
	}

	m.selectedFile = path
	m.diffViewport.SetContent(
		lipgloss.NewStyle().Foreground(textMuted).Render("Loading diff..."))
	m.renderChatForFile(path)
	return fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, path, m.contextLines, false)
}

func (m *Model) renderChatForFile(filePath string) {
	if m.reviewState == nil {
		m.chatViewport.SetContent(
			lipgloss.NewStyle().Foreground(textMuted).Render("No chat history"))
		return
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
		m.chatViewport.SetContent(
			lipgloss.NewStyle().Foreground(textMuted).Render("No chat history for this file"))
		m.chatViewport.GotoTop()
		return
	}

	you := lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
	aiStyle := lipgloss.NewStyle().Foreground(accentMauve).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(textSecondary)

	var b strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			b.WriteString(you.Render("You") + "\n")
		case "assistant":
			b.WriteString(aiStyle.Render("AI") + "\n")
		default:
			b.WriteString(lipgloss.NewStyle().Foreground(textMuted).Render(msg.Role) + "\n")
		}
		b.WriteString(msgStyle.Render(msg.Content) + "\n\n")
	}

	m.chatViewport.SetContent(b.String())
	m.chatViewport.GotoTop()
}

func (m Model) renderOverview() string {
	if m.pr == nil {
		return ""
	}

	w := m.diffViewport.Width
	if w < 10 {
		w = 40
	}

	title := lipgloss.NewStyle().Foreground(accentBlue).Bold(true).Width(w)
	label := lipgloss.NewStyle().Foreground(textMuted)
	value := lipgloss.NewStyle().Foreground(textPrimary)

	var b strings.Builder
	b.WriteString(title.Render(fmt.Sprintf("PR #%d: %s", m.pr.Number, m.pr.Title)) + "\n\n")
	b.WriteString(label.Render("Base: ") + value.Render(m.pr.BaseRefName) + "\n")
	b.WriteString(label.Render("Head: ") + value.Render(m.pr.HeadRefName) + "\n")
	sha := m.pr.HeadRefOid
	if len(sha) > 8 {
		sha = sha[:8]
	}
	b.WriteString(label.Render("SHA:  ") + lipgloss.NewStyle().Foreground(textMuted).Render(sha) + "\n\n")

	b.WriteString(label.Render(fmt.Sprintf("Files changed: %d", len(m.pr.Files))) + "\n")

	totalAdd, totalDel := 0, 0
	for _, f := range m.pr.Files {
		totalAdd += f.Additions
		totalDel += f.Deletions
	}

	addClr := lipgloss.NewStyle().Foreground(accentGreen)
	delClr := lipgloss.NewStyle().Foreground(accentRed)
	b.WriteString(label.Render("Total: ") +
		addClr.Render(fmt.Sprintf("+%d", totalAdd)) + " " +
		delClr.Render(fmt.Sprintf("-%d", totalDel)) + "\n")

	separator := lipgloss.NewStyle().Foreground(textSubtle).Render(strings.Repeat("-", w-2))

	// PR description
	if m.pr.Body != "" {
		sectionTitle := lipgloss.NewStyle().Foreground(accentMauve).Bold(true)
		bodyStyle := lipgloss.NewStyle().Foreground(textSecondary)

		b.WriteString("\n" + separator + "\n")
		b.WriteString(sectionTitle.Render("Description") + "\n\n")
		for _, line := range strings.Split(m.pr.Body, "\n") {
			if len(line) > w-2 {
				// Hard-wrap long lines
				for len(line) > 0 {
					end := w - 2
					if end > len(line) {
						end = len(line)
					}
					b.WriteString(bodyStyle.Render(line[:end]) + "\n")
					line = line[end:]
				}
			} else {
				b.WriteString(bodyStyle.Render(line) + "\n")
			}
		}
	}

	// Review comments summary
	totalComments := 0
	for _, comments := range m.comments {
		totalComments += len(comments)
	}
	if totalComments > 0 {
		sectionTitle := lipgloss.NewStyle().Foreground(accentMauve).Bold(true)
		commentBody := lipgloss.NewStyle().Foreground(textSecondary).Width(w - 4)

		b.WriteString("\n" + separator + "\n")
		b.WriteString(sectionTitle.Render(fmt.Sprintf("Review Comments (%d)", totalComments)) + "\n\n")

		// Sort paths for deterministic ordering
		paths := make([]string, 0, len(m.comments))
		for path := range m.comments {
			paths = append(paths, path)
		}
		sort.Strings(paths)

		for _, path := range paths {
			for _, c := range m.comments[path] {
				header := c.Author + " " + path + fmt.Sprintf(":%d", c.Line)
				headerStyle := lipgloss.NewStyle().Foreground(accentYellow).Bold(true).Width(w)
				b.WriteString(headerStyle.Render(header) + "\n")
				b.WriteString("  " + commentBody.Render(c.Body) + "\n\n")
			}
		}
	}

	return b.String()
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
		chatVpH := ih - chatInputH - 4
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
	h := m.height - 5
	if h < 5 {
		return 5
	}
	return h
}

// renderDiffWithCursor renders the diff viewport with a cursor highlight
// when the diff pane is focused.
func (m Model) renderDiffWithCursor() string {
	raw := m.diffViewport.View()
	if m.focusedPane != PaneDiff || m.selectedFile == "" {
		return raw
	}

	lines := strings.Split(raw, "\n")
	if m.diffCursor >= 0 && m.diffCursor < len(lines) {
		// Check if this line has a line number (commentable)
		hasLineNum := m.getDiffCursorLine() > 0

		var bg lipgloss.Color
		if hasLineNum {
			bg = lipgloss.Color("#45475A") // overlayBg — commentable line
		} else {
			bg = surfaceBg // non-commentable
		}
		// Wrap the entire line content with a background style
		// First strip any trailing spaces, apply bg, then pad to viewport width
		line := lines[m.diffCursor]
		highlight := lipgloss.NewStyle().Background(bg)
		w := m.diffViewport.Width
		// Pad line to full width so the highlight spans the row
		visible := lipgloss.Width(line)
		if visible < w {
			line = line + strings.Repeat(" ", w-visible)
		}
		lines[m.diffCursor] = highlight.Render(line)
	}

	return strings.Join(lines, "\n")
}

// ── View ────────────────────────────────────────────────────────────────

func (m Model) View() string {
	if !m.ready {
		return "\n  Loading..."
	}

	if m.loading {
		spinner := lipgloss.NewStyle().Foreground(accentBlue).Bold(true).Render("⠋")
		msg := lipgloss.NewStyle().Foreground(textSecondary).Render(" " + m.loadingMsg)
		return "\n  " + spinner + msg
	}

	cols := m.columns()
	ih := m.contentHeight()

	header := m.viewHeader()

	diffTitle := "OVERVIEW"
	if m.selectedFile != "" {
		lineNum := m.getDiffCursorLine()
		if m.focusedPane == PaneDiff && lineNum > 0 {
			diffTitle = fmt.Sprintf("DIFF (±%d) L%d", m.contextLines, lineNum)
		} else {
			diffTitle = fmt.Sprintf("DIFF (±%d)", m.contextLines)
		}
	}
	diffBody := m.renderDiffWithCursor()
	if m.commenting {
		diffTitle = fmt.Sprintf("DIFF – Comment on line %d", m.commentLine)
		commentSep := lipgloss.NewStyle().Foreground(textSubtle).Render(strings.Repeat("─", cols[1]-2))
		commentLabel := lipgloss.NewStyle().Foreground(accentYellow).Bold(true).Render("  New comment (Ctrl+S to submit, Esc to cancel)")
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
		sep := lipgloss.NewStyle().Foreground(textSubtle).Render(strings.Repeat("─", cols[2]-2))
		chatBody := m.chatViewport.View() + "\n" + sep + "\n" + m.chatInput.View()
		chatTitle := "AI CHAT"
		if m.aiStreaming {
			chatTitle = "AI CHAT " + lipgloss.NewStyle().Foreground(accentYellow).Render("●")
		}
		right := m.renderPane(chatTitle, chatBody, cols[2], ih, m.focusedPane == PaneChat)
		paneList = append(paneList, " ", right)
	}

	panes := lipgloss.JoinHorizontal(lipgloss.Top, paneList...)

	footer := m.viewFooter()

	return header + "\n" + panes + "\n" + footer
}

// ── Bordered pane rendering ─────────────────────────────────────────────

func (m Model) renderPane(title, content string, width, height int, focused bool) string {
	if width < 4 {
		return ""
	}

	var borderFg lipgloss.Color
	var tStyle lipgloss.Style

	if focused {
		borderFg = borderFocus
		tStyle = titleFocusedStyle
	} else {
		borderFg = borderClr
		tStyle = titleStyle
	}

	bdr := lipgloss.RoundedBorder()
	borderStyle := lipgloss.NewStyle().Foreground(borderFg)

	// Build top border with inset title
	titleLabel := tStyle.Render(" " + title + " ")
	titleW := lipgloss.Width(titleLabel)
	topLeft := borderStyle.Render(bdr.TopLeft)
	topRight := borderStyle.Render(bdr.TopRight)
	// 2 chars for corners, 1 char gap before title
	barBefore := borderStyle.Render(strings.Repeat(bdr.Top, 2))
	remaining := width - 2 - 2 - titleW // corners(2) + barBefore(2) + title
	if remaining < 0 {
		remaining = 0
	}
	barAfter := borderStyle.Render(strings.Repeat(bdr.Top, remaining))
	topLine := topLeft + barBefore + titleLabel + barAfter + topRight

	// Build content area with side borders
	contentW := width - 2
	if contentW < 0 {
		contentW = 0
	}
	contentH := height - 2
	if contentH < 0 {
		contentH = 0
	}

	// Render content lines padded/clipped to contentW
	contentLines := strings.Split(content, "\n")
	left := borderStyle.Render(bdr.Left)
	right := borderStyle.Render(bdr.Right)

	var body strings.Builder
	for i := 0; i < contentH; i++ {
		line := ""
		if i < len(contentLines) {
			line = contentLines[i]
		}
		vis := lipgloss.Width(line)
		if vis > contentW {
			// Truncate wide lines to fit
			line = truncateToWidth(line, contentW)
			vis = lipgloss.Width(line)
		}
		if vis < contentW {
			line = line + strings.Repeat(" ", contentW-vis)
		}
		body.WriteString(left + line + right + "\n")
	}

	// Build bottom border
	bottomLeft := borderStyle.Render(bdr.BottomLeft)
	bottomRight := borderStyle.Render(bdr.BottomRight)
	bottomBar := borderStyle.Render(strings.Repeat(bdr.Bottom, width-2))
	bottomLine := bottomLeft + bottomBar + bottomRight

	return topLine + "\n" + body.String() + bottomLine
}

// ── Header ──────────────────────────────────────────────────────────────

func (m Model) viewHeader() string {
	logo := lipgloss.NewStyle().
		Foreground(accentBlue).
		Bold(true).
		Render("prr")

	prInfo := lipgloss.NewStyle().
		Foreground(textPrimary).
		Render(fmt.Sprintf(" · PR #%s", m.prNumber))

	reviewed, total := m.reviewedCount()
	var reviewBadge string
	if total > 0 {
		clr := accentYellow
		if reviewed == total {
			clr = accentGreen
		}
		reviewBadge = lipgloss.NewStyle().
			Foreground(clr).
			Render(fmt.Sprintf("● %d/%d reviewed", reviewed, total))
	}

	// Calculate how much room we have for the PR title
	right := reviewBadge + " "
	fixedW := lipgloss.Width(" "+logo+prInfo) + lipgloss.Width(right) + 2 // 2 for min gap
	maxTitleW := m.width - fixedW

	prTitle := ""
	if m.pr != nil && maxTitleW > 4 {
		t := m.pr.Title
		if len(t) > maxTitleW-3 { // -3 for " · " prefix
			t = t[:maxTitleW-6] + "..."
		}
		prTitle = lipgloss.NewStyle().
			Foreground(textSecondary).
			Render(fmt.Sprintf(" · %s", t))
	}

	left := " " + logo + prInfo + prTitle
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}

	separator := lipgloss.NewStyle().
		Foreground(textSubtle).
		Render(strings.Repeat("─", m.width))

	return left + strings.Repeat(" ", gap) + right + "\n" + separator
}

// ── Footer ──────────────────────────────────────────────────────────────

func (m Model) viewFooter() string {
	separator := lipgloss.NewStyle().
		Foreground(textSubtle).
		Render(strings.Repeat("─", m.width))

	// Common bindings
	bindings := []struct{ key, desc string }{
		{"Tab", "next pane"},
		{"S-Tab", "prev pane"},
	}

	// Pane-specific bindings
	switch m.focusedPane {
	case PaneFileList:
		hideLabel := "hide reviewed"
		if m.fileTree.hideReviewed {
			hideLabel = "show reviewed"
		}
		bindings = append(bindings,
			struct{ key, desc string }{"j/k", "navigate"},
			struct{ key, desc string }{"Enter", "select"},
			struct{ key, desc string }{"Space", "review"},
			struct{ key, desc string }{"h/l", "collapse/expand"},
			struct{ key, desc string }{"r", hideLabel},
			struct{ key, desc string }{"n/p", "next/prev unreviewed"},
			struct{ key, desc string }{"a", "AI review"},
		)
	case PaneDiff:
		bindings = append(bindings,
			struct{ key, desc string }{"j/k", "select line"},
			struct{ key, desc string }{"+/−", "context"},
			struct{ key, desc string }{"c", "comment"},
			struct{ key, desc string }{"a", "AI review"},
		)
	case PaneChat:
		bindings = append(bindings,
			struct{ key, desc string }{"Enter", "send"},
		)
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

	return separator + "\n" + line
}

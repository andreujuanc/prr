package ui

import (
	"context"
	"fmt"
	"log"
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

func fetchStyledDiff(base, head, filePath string, contextLines int) tea.Cmd {
	return func() tea.Msg {
		content, err := git.GetStyledDiffWithContext(base, head, filePath, contextLines)
		return StyledDiffMsg{FilePath: filePath, Content: content, Err: err}
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
		return m, nil

	case StyledDiffMsg:
		if msg.Err != nil {
			m.diffViewport.SetContent(
				lipgloss.NewStyle().Foreground(accentRed).Render(
					fmt.Sprintf("Error loading diff: %v", msg.Err)))
		} else {
			if msg.FilePath == m.selectedFile {
				m.diffViewport.SetContent(msg.Content)
				m.diffViewport.GotoTop()
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
		// Don't process keys while AI is streaming (except cancel)
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
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			if m.focusedPane != PaneChat {
				return m, tea.Quit
			}
		case "tab":
			m.focusedPane = (m.focusedPane + 1) % 3
			cmd = m.syncFocus()
			cmds = append(cmds, cmd)
		case "shift+tab":
			m.focusedPane--
			if m.focusedPane < 0 {
				m.focusedPane = PaneChat
			}
			cmd = m.syncFocus()
			cmds = append(cmds, cmd)
		case "enter":
			if m.focusedPane == PaneFileList {
				cmd = m.selectCurrentFile()
				if cmd != nil {
					cmds = append(cmds, cmd)
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
		}
	}

	switch m.focusedPane {
	case PaneFileList:
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "j", "down":
				m.fileTree.moveDown()
			case "k", "up":
				m.fileTree.moveUp()
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
			}
		}
	case PaneDiff:
		m.diffViewport, cmd = m.diffViewport.Update(msg)
		cmds = append(cmds, cmd)
	case PaneChat:
		m.chatInput, cmd = m.chatInput.Update(msg)
		cmds = append(cmds, cmd)
		if km, ok := msg.(tea.KeyMsg); ok {
			if km.String() == "pgup" || km.String() == "pgdown" {
				m.chatViewport, cmd = m.chatViewport.Update(msg)
				cmds = append(cmds, cmd)
			}
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
	if m.selectedFile == "" {
		// PR overview: use all diffs (truncated)
		var allDiffs strings.Builder
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
	return ai.ChatPrompt, diff
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
	for i, msg := range messages {
		_ = i
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
	return fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, m.selectedFile, m.contextLines)
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

func (m *Model) selectCurrentFile() tea.Cmd {
	if m.fileTree.selectedIsDir() {
		m.fileTree.toggle()
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
	return fetchStyledDiff(m.pr.BaseRefName, m.pr.HeadRefName, path, m.contextLines)
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

	title := lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
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

	b.WriteString(label.Render(fmt.Sprintf("Files changed: %d", len(m.pr.Files))) + "\n\n")

	addClr := lipgloss.NewStyle().Foreground(accentGreen)
	delClr := lipgloss.NewStyle().Foreground(accentRed)
	totalAdd, totalDel := 0, 0
	for _, f := range m.pr.Files {
		totalAdd += f.Additions
		totalDel += f.Deletions
		b.WriteString("  " + value.Render(f.Path) + "  " +
			addClr.Render(fmt.Sprintf("+%d", f.Additions)) + " " +
			delClr.Render(fmt.Sprintf("−%d", f.Deletions)) + "\n")
	}

	b.WriteString("\n" + label.Render("Total: ") +
		addClr.Render(fmt.Sprintf("+%d", totalAdd)) + " " +
		delClr.Render(fmt.Sprintf("−%d", totalDel)) + "\n")

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

	m.fileTree.width = cols[0] - 2
	m.fileTree.height = ih - 2

	m.diffViewport.Width = cols[1] - 2
	m.diffViewport.Height = ih - 2

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

func (m *Model) syncFocus() tea.Cmd {
	if m.focusedPane == PaneChat {
		return m.chatInput.Focus()
	}
	m.chatInput.Blur()
	return nil
}

func (m Model) columns() [3]int {
	avail := m.width - 2
	l := max(16, avail*22/100)
	r := max(22, avail*25/100)
	mid := avail - l - r
	if mid < 12 {
		mid = 12
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

	left := m.renderPane("FILES", m.fileTree.View(), cols[0], ih, m.focusedPane == PaneFileList)
	diffTitle := "DIFF"
	if m.selectedFile != "" {
		diffTitle = fmt.Sprintf("DIFF (±%d)", m.contextLines)
	}
	middle := m.renderPane(diffTitle, m.diffViewport.View(), cols[1], ih, m.focusedPane == PaneDiff)

	sep := lipgloss.NewStyle().Foreground(textSubtle).Render(strings.Repeat("─", cols[2]-2))
	chatBody := m.chatViewport.View() + "\n" + sep + "\n" + m.chatInput.View()

	// Show streaming indicator in pane title
	chatTitle := "AI CHAT"
	if m.aiStreaming {
		chatTitle = "AI CHAT " + lipgloss.NewStyle().Foreground(accentYellow).Render("●")
	}
	right := m.renderPane(chatTitle, chatBody, cols[2], ih, m.focusedPane == PaneChat)

	panes := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", middle, " ", right)

	footer := m.viewFooter()

	return header + "\n" + panes + "\n" + footer
}

// ── Bordered pane rendering ─────────────────────────────────────────────

func (m Model) renderPane(title, content string, width, height int, focused bool) string {
	var border lipgloss.Style
	var tStyle lipgloss.Style

	if focused {
		border = paneFocusedStyle.Copy().Width(width - 2).Height(height - 2)
		tStyle = titleFocusedStyle
	} else {
		border = paneStyle.Copy().Width(width - 2).Height(height - 2)
		tStyle = titleStyle
	}

	titleLabel := tStyle.Render(" " + title + " ")

	box := border.Render(content)

	lines := strings.Split(box, "\n")
	if len(lines) > 0 {
		topBorder := lines[0]
		runes := []rune(topBorder)
		titleRunes := []rune(titleLabel)
		insertPos := 3
		if insertPos+len(titleRunes) < len(runes) {
			copy(runes[insertPos:insertPos+len(titleRunes)], titleRunes)
		}
		lines[0] = string(runes)
	}

	return strings.Join(lines, "\n")
}

// ── Header ──────────────────────────────────────────────────────────────

func (m Model) viewHeader() string {
	logo := lipgloss.NewStyle().
		Foreground(accentBlue).
		Bold(true).
		Render("prr")

	prTitle := ""
	if m.pr != nil {
		prTitle = lipgloss.NewStyle().
			Foreground(textSecondary).
			Render(fmt.Sprintf(" · %s", m.pr.Title))
	}

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

	left := " " + logo + prInfo + prTitle
	right := reviewBadge + " "
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
		bindings = append(bindings,
			struct{ key, desc string }{"j/k", "navigate"},
			struct{ key, desc string }{"Enter", "select"},
			struct{ key, desc string }{"Space", "review"},
			struct{ key, desc string }{"h/l", "collapse/expand"},
			struct{ key, desc string }{"n/p", "next/prev unreviewed"},
			struct{ key, desc string }{"a", "AI review"},
		)
	case PaneDiff:
		bindings = append(bindings,
			struct{ key, desc string }{"j/k", "scroll"},
			struct{ key, desc string }{"+/−", "context"},
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

	bindings = append(bindings, struct{ key, desc string }{"q", "quit"})

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

package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Pane identifies which section of the UI has focus
type Pane int

const (
	PaneFileList Pane = iota
	PaneDiff
	PaneChat
)

// Model is the main Bubble Tea model for the application
type Model struct {
	// Bubbles components
	fileList     list.Model
	diffViewport viewport.Model
	chatViewport viewport.Model
	chatInput    textarea.Model

	// State
	focusedPane Pane
	prNumber    string
	width       int
	height      int
	ready       bool
}

// item implements list.Item for the file list
type item struct {
	title       string
	description string
}

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.description }
func (i item) FilterValue() string { return i.title }

// NewModel initializes the main UI model
func NewModel(prNumber string) Model {
	// Initialize List
	items := []list.Item{
		item{title: "[PR Overview]", description: "General PR Discussion"},
		item{title: "cmd/prr/main.go", description: "Modified - 10 additions, 5 deletions"},
		item{title: "internal/ui/model.go", description: "Unreviewed - 150 additions"},
		item{title: "internal/git/gh.go", description: "Reviewed"},
	}
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Files"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)

	// Initialize Viewports
	diffVp := viewport.New(0, 0)
	diffVp.SetContent("Select a file to view its diff here.\n\n\n\n\n\nMock Diff Content:\n\n+ // Added some code\n- // Removed some code")

	chatVp := viewport.New(0, 0)
	chatVp.SetContent("Chat history will appear here...\n\nUser: Can you explain this?\nAI: Sure, this code does X.")

	// Initialize Textarea
	ta := textarea.New()
	ta.Placeholder = "Ask a question..."
	ta.Focus() // Start with textarea unfocused initially, but we will manage focus manually
	ta.Prompt = "┃ "
	ta.CharLimit = 500
	ta.SetWidth(30)
	ta.SetHeight(3)

	return Model{
		fileList:     l,
		diffViewport: diffVp,
		chatViewport: chatVp,
		chatInput:    ta,
		focusedPane:  PaneFileList,
		prNumber:     prNumber,
	}
}

func (m Model) Init() tea.Cmd {
	return textarea.Blink
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
		m.resizeComponents()

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
			
		case "tab":
			// Cycle focus
			m.focusedPane = (m.focusedPane + 1) % 3
			
		case "shift+tab":
			m.focusedPane--
			if m.focusedPane < 0 {
				m.focusedPane = PaneChat
			}
		}
	}

	// Route updates based on focus
	switch m.focusedPane {
	case PaneFileList:
		m.fileList, cmd = m.fileList.Update(msg)
		cmds = append(cmds, cmd)
	case PaneDiff:
		m.diffViewport, cmd = m.diffViewport.Update(msg)
		cmds = append(cmds, cmd)
	case PaneChat:
		// Send keys to chat input or viewport depending on what we want to do
		// For now just route to textarea
		m.chatInput, cmd = m.chatInput.Update(msg)
		cmds = append(cmds, cmd)
		
		// If page up/down, route to chat viewport
		switch msg := msg.(type) {
		case tea.KeyMsg:
			if msg.String() == "pgup" || msg.String() == "pgdown" {
				m.chatViewport, cmd = m.chatViewport.Update(msg)
				cmds = append(cmds, cmd)
			}
		}
	}

	// Always update textarea blink
	if m.focusedPane != PaneChat {
		m.chatInput, cmd = m.chatInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) resizeComponents() {
	if !m.ready {
		return
	}

	headerHeight := 2 // PR Title/Progress
	footerHeight := 1 // Help text
	contentHeight := m.height - headerHeight - footerHeight

	// Widths (20%, 55%, 25%)
	leftWidth := int(float64(m.width) * 0.20)
	rightWidth := int(float64(m.width) * 0.25)
	middleWidth := m.width - leftWidth - rightWidth - 4 // -4 for borders/padding

	// File List
	m.fileList.SetSize(leftWidth, contentHeight)

	// Diff Viewport
	m.diffViewport.Width = middleWidth
	m.diffViewport.Height = contentHeight

	// Chat Area
	chatInputHeight := 3
	m.chatInput.SetWidth(rightWidth)
	m.chatInput.SetHeight(chatInputHeight)
	
	m.chatViewport.Width = rightWidth
	m.chatViewport.Height = contentHeight - chatInputHeight - 1 // -1 for padding/border between them
}

func (m Model) View() string {
	if !m.ready {
		return "Initializing...\n"
	}

	// Header
	header := headerStyle.Render(fmt.Sprintf("PR #%s Review (Mock Progress: 2/5 files)", m.prNumber))

	// Left Pane (File List)
	leftStyle := paneStyle.Copy().Width(m.fileList.Width()).Height(m.fileList.Height())
	if m.focusedPane == PaneFileList {
		leftStyle = leftStyle.BorderForeground(focusColor)
	}
	left := leftStyle.Render(m.fileList.View())

	// Middle Pane (Diff)
	middleStyle := paneStyle.Copy().Width(m.diffViewport.Width).Height(m.diffViewport.Height)
	if m.focusedPane == PaneDiff {
		middleStyle = middleStyle.BorderForeground(focusColor)
	}
	middle := middleStyle.Render(m.diffViewport.View())

	// Right Pane (Chat)
	rightStyle := paneStyle.Copy().Width(m.chatInput.Width()).Height(m.diffViewport.Height)
	if m.focusedPane == PaneChat {
		rightStyle = rightStyle.BorderForeground(focusColor)
	}
	
	// Construct Right Pane content
	chatContent := lipgloss.JoinVertical(
		lipgloss.Left,
		m.chatViewport.View(),
		lipgloss.NewStyle().Border(lipgloss.NormalBorder(), true, false, false, false).Render(m.chatInput.View()),
	)
	right := rightStyle.Render(chatContent)

	// Join Panes
	mainContent := lipgloss.JoinHorizontal(lipgloss.Top, left, middle, right)

	// Footer
	footer := helpStyle.Render("tab/shift+tab: switch focus • q/ctrl+c: quit")

	// Final Layout
	return lipgloss.JoinVertical(lipgloss.Left, header, mainContent, footer)
}

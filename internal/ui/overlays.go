package ui

import (
	"fmt"
	"strings"

	"prr/internal/ai"
	"prr/internal/config"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ── PR Picker ───────────────────────────────────────────────────────────

// renderPRPicker renders the pull request selection overlay.
func (m Model) renderPRPicker() string {
	width := 64
	var b strings.Builder

	b.WriteString(styleAccentBlueBold.Render("  SELECT PULL REQUEST"))
	b.WriteString("\n")

	if m.prPickerLoading {
		b.WriteString("\n")
		b.WriteString("  " + m.spinner.View() + " " + styleTextSecondary.Render("Fetching open pull requests..."))
		b.WriteString("\n")
		return b.String()
	}

	if m.prPickerError != "" {
		b.WriteString("\n")
		b.WriteString("  " + styleAccentRed.Render(m.prPickerError))
		b.WriteString("\n\n")
		b.WriteString(styleTextMuted.Render("  Press q to quit"))
		return b.String()
	}

	b.WriteString("\n")

	// Clamp visible items to fit the terminal (header=2, footer=2, border=2)
	maxVisible := m.height - 8
	if maxVisible < 5 {
		maxVisible = 5
	}
	total := len(m.prPickerItems)

	// Compute visible window centered on cursor
	start := 0
	end := total
	if total > maxVisible {
		start = m.prPickerCursor - maxVisible/2
		if start < 0 {
			start = 0
		}
		end = start + maxVisible
		if end > total {
			end = total
			start = end - maxVisible
		}
	}

	if start > 0 {
		b.WriteString(styleTextMuted.Render("  ...") + "\n")
	}

	for i := start; i < end; i++ {
		pr := m.prPickerItems[i]
		isSelected := i == m.prPickerCursor

		marker := "  "
		if isSelected {
			marker = styleAccentBlueBold.Render("> ")
		}

		num := fmt.Sprintf("#%-4d", pr.Number)
		title := pr.Title
		maxTitle := width - 16 // room for marker + #num + author
		if maxTitle < 20 {
			maxTitle = 20
		}
		titleRunes := []rune(title)
		if len(titleRunes) > maxTitle {
			title = string(titleRunes[:maxTitle-3]) + "..."
		}
		author := pr.Author.Login

		var line string
		if isSelected {
			line = fmt.Sprintf("%s%s %s %s", marker,
				styleAccentBlueBold.Render(num),
				styleTextPrimary.Bold(true).Render(title),
				styleTextMuted.Render("("+author+")"))
		} else {
			line = fmt.Sprintf("%s%s %s %s", marker,
				styleTextSecondary.Render(num),
				styleTextSecondary.Render(title),
				styleTextMuted.Render("("+author+")"))
		}
		line = truncateToWidth(line, width)
		b.WriteString(line + "\n")
	}

	if end < total {
		b.WriteString(styleTextMuted.Render("  ...") + "\n")
	}

	b.WriteString("\n")
	b.WriteString(styleTextMuted.Render("  Enter select  q quit"))

	return b.String()
}

// ── Model Picker ────────────────────────────────────────────────────────

// modelPickerItem represents a selectable model in the picker.
type modelPickerItem struct {
	id       string
	label    string // display label (short human-friendly name)
	thinking bool   // whether the model supports thinking
}

// availableModels returns the ordered list of Gemini models for the picker.
func availableModels() []modelPickerItem {
	return []modelPickerItem{
		{id: "gemini-3.1-pro-preview", label: "Gemini 3.1 Pro", thinking: true},
		{id: "gemini-3.1-flash-lite-preview", label: "Gemini 3.1 Flash Lite", thinking: true},
		{id: "gemini-2.5-flash", label: "Gemini 2.5 Flash", thinking: true},
	}
}

// switchModel attempts to switch the AI client to the given model.
// Returns the new model name on success.
func (m *Model) switchModel(modelID string) string {
	switcher, ok := m.aiClient.(ai.ModelSwitcher)
	if !ok {
		return m.aiModelName
	}

	models, _ := config.LoadModels()
	mcfg := config.GetModelConfig(models, modelID)

	if err := switcher.SwitchModel(modelID, mcfg.MaxOutputTokens, mcfg.Temperature, mcfg.ThinkingBudget); err != nil {
		return m.aiModelName
	}

	m.aiModelName = modelID
	return modelID
}

// renderModelPicker renders the model selection overlay.
func (m Model) renderModelPicker() string {
	models := availableModels()

	width := 40
	var b strings.Builder

	b.WriteString(styleAccentBlueBold.Render("  SELECT MODEL"))
	b.WriteString("\n\n")

	for i, model := range models {
		isSelected := i == m.modelPickerCursor
		isCurrent := model.id == m.aiModelName

		marker := "  "
		if isSelected {
			marker = styleAccentBlueBold.Render("> ")
		}

		name := model.label
		if isSelected {
			name = styleTextPrimary.Bold(true).Render(name)
		} else {
			name = styleTextSecondary.Render(name)
		}

		suffix := ""
		if model.thinking {
			suffix = styleTextMuted.Render(" [thinking]")
		}
		if isCurrent {
			suffix += styleAccentGreen.Render(" *")
		}

		line := fmt.Sprintf("%s%s%s", marker, name, suffix)
		line = truncateToWidth(line, width)
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	b.WriteString(styleTextMuted.Render("  Enter select  Esc cancel"))

	return b.String()
}

// ── Help Modal ──────────────────────────────────────────────────────────

type helpBinding struct {
	key  string
	desc string
}

type helpSection struct {
	title    string
	bindings []helpBinding
}

func (m Model) helpSections() []helpSection {
	sections := []helpSection{
		{
			title: "GLOBAL",
			bindings: []helpBinding{
				{"Tab / S-Tab", "Cycle panes"},
				{"Ctrl+A", "Toggle AI panel"},
				{"Ctrl+B", "Toggle file panel"},
				{"a", "AI review (file or PR)"},
				{"A", "Force re-review (no cache)"},
				{"m", "Switch model"},
				{"?", "Toggle this help"},
				{"q", "Quit"},
			},
		},
		{
			title: "FILE LIST",
			bindings: []helpBinding{
				{"j/k", "Navigate files"},
				{"Enter", "Select file"},
				{"l/h", "Expand / collapse dir"},
				{"Space", "Toggle reviewed status"},
				{"r", "Toggle hide reviewed"},
				{"n/p", "Next / prev unreviewed"},
			},
		},
		{
			title: "DIFF",
			bindings: []helpBinding{
				{"j/k", "Move cursor"},
				{"+/-", "More / less context"},
				{"c", "Comment on line"},
				{"Esc", "Back to review (from finding)"},
			},
		},
	}

	// AI panel section varies by tab
	if m.aiPanelTab == 0 {
		sections = append(sections, helpSection{
			title: "REVIEW",
			bindings: []helpBinding{
				{"j/k", "Navigate findings"},
				{"Enter", "Jump to finding"},
				{"Tab", "Switch to Chat tab"},
				{"Ctrl+S", "Submit review to GitHub"},
			},
		})
	} else {
		sections = append(sections, helpSection{
			title: "CHAT",
			bindings: []helpBinding{
				{"Enter", "Send message"},
				{"Ctrl+K", "Clear chat"},
				{"Tab", "Switch to Review tab"},
			},
		})
	}

	return sections
}

// renderHelpModal renders the full-screen help overlay.
func (m Model) renderHelpModal() string {
	sections := m.helpSections()

	maxWidth := m.width - 8
	if maxWidth < 40 {
		maxWidth = 40
	}
	if maxWidth > 60 {
		maxWidth = 60
	}

	var b strings.Builder

	b.WriteString(styleAccentBlueBold.Render("  KEYBOARD SHORTCUTS"))
	b.WriteString("\n")

	for _, section := range sections {
		b.WriteString("\n")
		b.WriteString(styleAccentYellowBold.Render("  " + section.title))
		b.WriteString("\n")

		for _, bind := range section.bindings {
			key := footerKeyStyle.Render(fmt.Sprintf("  %-14s", bind.key))
			desc := styleTextSecondary.Render(bind.desc)
			line := key + "  " + desc
			line = truncateToWidth(line, maxWidth)
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(styleTextMuted.Render("  Press ? or Esc to close"))

	return b.String()
}

// ── Submit Review Modal ─────────────────────────────────────────────────

// renderSubmitReviewModal renders the review submission confirmation overlay.
func (m Model) renderSubmitReviewModal() string {
	if m.reviewState == nil || m.reviewState.Review == nil || m.reviewState.Review.Structured == nil {
		return ""
	}

	verdict := m.reviewState.Review.Structured.Verdict
	verdictLabel, verdictStyle := formatVerdict(verdict)

	width := 50
	var b strings.Builder

	b.WriteString(styleAccentBlueBold.Render("  SUBMIT REVIEW TO GITHUB"))
	b.WriteString("\n\n")

	b.WriteString("  Verdict: ")
	b.WriteString(verdictStyle.Render(verdictLabel))
	b.WriteString("\n\n")

	summary := m.reviewState.Review.Structured.Summary
	if len(summary) > 200 {
		summary = summary[:197] + "..."
	}
	b.WriteString(wrapStyled(styleTextSecondary, "  "+summary, width-4))
	b.WriteString("\n\n")

	b.WriteString(styleTextMuted.Render("  This will submit a formal review on the PR."))
	b.WriteString("\n\n")

	for i, opt := range []string{"Submit", "Cancel"} {
		isSelected := i == m.submitReviewCursor
		marker := "  "
		if isSelected {
			marker = styleAccentBlueBold.Render("> ")
		}
		label := opt
		if isSelected {
			label = styleTextPrimary.Bold(true).Render(label)
		} else {
			label = styleTextSecondary.Render(label)
		}
		b.WriteString(marker + label + "\n")
	}

	return b.String()
}

// centerOverlay renders content as a centered overlay on top of the existing view.
func centerOverlay(content string, screenWidth, screenHeight int) string {
	lines := strings.Split(content, "\n")

	// Compute content dimensions
	maxW := 0
	for _, l := range lines {
		w := ansi.StringWidth(l)
		if w > maxW {
			maxW = w
		}
	}

	// Add padding/border
	boxWidth := maxW + 4
	if boxWidth > screenWidth-4 {
		boxWidth = screenWidth - 4
	}

	// Vertical centering
	topPad := (screenHeight - len(lines) - 2) / 2
	if topPad < 1 {
		topPad = 1
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderFocus).
		Padding(0, 1).
		Width(boxWidth)

	box := boxStyle.Render(content)
	boxLines := strings.Split(box, "\n")

	var b strings.Builder
	// Pad top
	for i := 0; i < topPad; i++ {
		b.WriteString(strings.Repeat(" ", screenWidth) + "\n")
	}
	// Center horizontally
	for _, line := range boxLines {
		w := ansi.StringWidth(line)
		leftPad := (screenWidth - w) / 2
		if leftPad < 0 {
			leftPad = 0
		}
		b.WriteString(strings.Repeat(" ", leftPad) + line + "\n")
	}

	return b.String()
}

package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andreujuanc/prr/internal/opencode"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Permission overlay ──────────────────────────────────────────────────

// permissionModal holds the state of a tool permission approval overlay.
type permissionModal struct {
	taskID     int
	permission opencode.Permission
	cursor     int // 0 = Approve, 1 = Deny
}

// renderPermissionModal renders the permission approval overlay content.
func (m *Model) renderPermissionModal() string {
	if m.permissionOverlay == nil {
		return ""
	}

	var b strings.Builder
	perm := &m.permissionOverlay.permission

	b.WriteString(styleAccentYellowBold.Render("⚠ Permission Required") + "\n\n")
	b.WriteString(fmt.Sprintf("  Tool: %s\n", styleAccentBlueBold.Render(perm.Tool)))

	// Show a brief summary of the tool input
	summary := formatPermissionInput(perm.Tool, perm.Input)
	if summary != "" {
		b.WriteString(fmt.Sprintf("  %s\n", styleTextSecondary.Render(summary)))
	}
	b.WriteString("\n")

	// Task context
	for _, t := range m.tasks {
		if t.ID == m.permissionOverlay.taskID {
			b.WriteString(fmt.Sprintf("  Task: %s\n\n", styleTextMuted.Render(t.Title)))
			break
		}
	}

	// Selection cursor
	approveLabel := "  Approve"
	denyLabel := "  Deny"
	if m.permissionOverlay.cursor == 0 {
		approveLabel = styleAccentGreen.Render("▸ Approve")
		denyLabel = "  " + styleTextSecondary.Render("Deny")
	} else {
		approveLabel = "  " + styleTextSecondary.Render("Approve")
		denyLabel = styleAccentRed.Render("▸ Deny")
	}
	b.WriteString(approveLabel + "    " + denyLabel + "\n\n")
	b.WriteString(styleTextMuted.Render("  [Enter] Select   [y] Approve   [n] Deny   [Esc] Deny"))

	return b.String()
}

// formatPermissionInput creates a brief human-readable summary of the tool input.
func formatPermissionInput(tool string, input map[string]interface{}) string {
	switch tool {
	case "bash":
		if cmd, ok := input["command"].(string); ok {
			if len(cmd) > 100 {
				cmd = cmd[:97] + "..."
			}
			return "$ " + cmd
		}
	case "write":
		if fp, ok := input["filePath"].(string); ok {
			return "file: " + fp
		}
	case "edit":
		if fp, ok := input["filePath"].(string); ok {
			return "file: " + fp
		}
	case "read":
		if fp, ok := input["filePath"].(string); ok {
			return "file: " + fp
		}
	case "glob":
		if pat, ok := input["pattern"].(string); ok {
			return "pattern: " + pat
		}
	case "grep":
		if pat, ok := input["pattern"].(string); ok {
			return "pattern: " + pat
		}
	}

	// Fallback: show first key=value
	for k, v := range input {
		s := fmt.Sprintf("%v", v)
		if len(s) > 80 {
			s = s[:77] + "..."
		}
		return fmt.Sprintf("%s: %s", k, s)
	}
	return ""
}

// ── Question overlay ────────────────────────────────────────────────────

// questionModal holds the state of an interactive question overlay.
type questionModal struct {
	taskID   int
	question opencode.Question
	cursor   int // index into options (0-based); last position = "Dismiss"
}

// optionCount returns the number of selectable items (options + dismiss).
func (qm *questionModal) optionCount() int {
	if len(qm.question.Questions) == 0 {
		return 1 // just "Dismiss"
	}
	return len(qm.question.Questions[0].Options) + 1 // options + Dismiss
}

// renderQuestionModal renders the interactive question overlay content.
func (m *Model) renderQuestionModal() string {
	if m.questionOverlay == nil {
		return ""
	}

	var b strings.Builder
	q := &m.questionOverlay.question

	b.WriteString(styleAccentBlueBold.Render("❓ Question") + "\n\n")

	if len(q.Questions) > 0 {
		prompt := q.Questions[0]
		if prompt.Header != "" {
			b.WriteString("  " + styleTextPrimary.Render(prompt.Header) + "\n")
		}
		if prompt.Question != "" {
			b.WriteString("  " + styleTextSecondary.Render(prompt.Question) + "\n")
		}
		b.WriteString("\n")

		// Render options
		for i, opt := range prompt.Options {
			cursor := "  "
			labelStyle := styleTextSecondary
			if i == m.questionOverlay.cursor {
				cursor = styleAccentBlueBold.Render("▸ ")
				labelStyle = styleTextPrimary
			}
			line := labelStyle.Render(opt.Label)
			if opt.Description != "" {
				line += " " + styleTextMuted.Render("— "+opt.Description)
			}
			b.WriteString(cursor + line + "\n")
		}

		// Dismiss option at the end
		dismissIdx := len(prompt.Options)
		cursor := "  "
		labelStyle := styleTextSecondary
		if m.questionOverlay.cursor == dismissIdx {
			cursor = styleAccentRed.Render("▸ ")
			labelStyle = styleAccentRed
		}
		b.WriteString(cursor + labelStyle.Render("Dismiss (skip question)") + "\n")
	} else {
		b.WriteString("  " + styleTextMuted.Render("(empty question)") + "\n\n")
		b.WriteString("  " + styleAccentRed.Render("▸ Dismiss") + "\n")
	}

	// Task context
	for _, t := range m.tasks {
		if t.ID == m.questionOverlay.taskID {
			b.WriteString("\n  " + styleTextMuted.Render("Task: "+t.Title) + "\n")
			break
		}
	}

	b.WriteString("\n" + styleTextMuted.Render("  [Enter] Select   [Esc] Dismiss"))

	return b.String()
}

// ── Permission actions ──────────────────────────────────────────────────

// approvePermission sends approval for the pending permission request.
func (m *Model) approvePermission() tea.Cmd {
	if m.permissionOverlay == nil || m.opencodeMgr == nil {
		return nil
	}
	perm := m.permissionOverlay.permission
	sessionID := perm.SessionID
	permID := perm.ID
	taskID := m.permissionOverlay.taskID
	mgr := m.opencodeMgr
	tasks := m.tasks // slice header copy; elements are *Task (stable pointers)

	return func() tea.Msg {
		client := mgr.Client()
		if client == nil {
			return TaskOutputMsg{ID: taskID, Lines: "✗ Cannot approve: server not connected\n"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.RespondPermission(ctx, sessionID, permID, true); err != nil {
			return TaskOutputMsg{ID: taskID, Lines: fmt.Sprintf("✗ Approve failed: %v\n", err)}
		}
		for _, t := range tasks {
			if t.ID == taskID {
				t.setPermission(nil)
				break
			}
		}
		return TaskOutputMsg{ID: taskID, Lines: "✓ Permission approved\n"}
	}
}

// denyPermission sends denial for the pending permission request.
func (m *Model) denyPermission() tea.Cmd {
	if m.permissionOverlay == nil || m.opencodeMgr == nil {
		return nil
	}
	perm := m.permissionOverlay.permission
	sessionID := perm.SessionID
	permID := perm.ID
	taskID := m.permissionOverlay.taskID
	mgr := m.opencodeMgr
	tasks := m.tasks

	return func() tea.Msg {
		client := mgr.Client()
		if client == nil {
			return TaskOutputMsg{ID: taskID, Lines: "✗ Cannot deny: server not connected\n"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.RespondPermission(ctx, sessionID, permID, false); err != nil {
			return TaskOutputMsg{ID: taskID, Lines: fmt.Sprintf("✗ Deny failed: %v\n", err)}
		}
		for _, t := range tasks {
			if t.ID == taskID {
				t.setPermission(nil)
				break
			}
		}
		return TaskOutputMsg{ID: taskID, Lines: "✗ Permission denied\n"}
	}
}

// ── Question actions ────────────────────────────────────────────────────

// selectQuestionOption sends the selected answer for the pending question.
func (m *Model) selectQuestionOption() tea.Cmd {
	if m.questionOverlay == nil || m.opencodeMgr == nil {
		return nil
	}
	q := m.questionOverlay.question
	cursor := m.questionOverlay.cursor
	taskID := m.questionOverlay.taskID
	mgr := m.opencodeMgr
	tasks := m.tasks

	// If cursor is at the "Dismiss" position, reject instead
	if len(q.Questions) == 0 || cursor >= len(q.Questions[0].Options) {
		return m.dismissQuestion()
	}

	selectedLabel := q.Questions[0].Options[cursor].Label
	questionID := q.ID

	return func() tea.Msg {
		client := mgr.Client()
		if client == nil {
			return TaskOutputMsg{ID: taskID, Lines: "✗ Cannot reply: server not connected\n"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.ReplyQuestion(ctx, questionID, [][]string{{selectedLabel}}); err != nil {
			return TaskOutputMsg{ID: taskID, Lines: fmt.Sprintf("✗ Reply failed: %v\n", err)}
		}
		for _, t := range tasks {
			if t.ID == taskID {
				t.setQuestion(nil)
				break
			}
		}
		return TaskOutputMsg{ID: taskID, Lines: fmt.Sprintf("✓ Answered: %s\n", selectedLabel)}
	}
}

// dismissQuestion rejects the pending question without answering.
func (m *Model) dismissQuestion() tea.Cmd {
	if m.questionOverlay == nil || m.opencodeMgr == nil {
		return nil
	}
	questionID := m.questionOverlay.question.ID
	taskID := m.questionOverlay.taskID
	mgr := m.opencodeMgr
	tasks := m.tasks

	return func() tea.Msg {
		client := mgr.Client()
		if client == nil {
			return TaskOutputMsg{ID: taskID, Lines: "✗ Cannot dismiss: server not connected\n"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.RejectQuestion(ctx, questionID); err != nil {
			return TaskOutputMsg{ID: taskID, Lines: fmt.Sprintf("✗ Dismiss failed: %v\n", err)}
		}
		for _, t := range tasks {
			if t.ID == taskID {
				t.setQuestion(nil)
				break
			}
		}
		return TaskOutputMsg{ID: taskID, Lines: "✗ Question dismissed\n"}
	}
}

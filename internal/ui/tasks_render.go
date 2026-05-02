package ui

import (
	"fmt"
	"strings"
	"time"
)

// renderTasksTab renders the Tasks tab content for the AI panel.
func (m Model) renderTasksTab(width int) string {
	if len(m.tasks) == 0 {
		return styleTextMuted.Render("  No tasks yet.\n\n  Press f on a finding to fix it with OpenCode.")
	}

	var b strings.Builder
	b.WriteString("\n")

	for i, t := range m.tasks {
		isSelected := i == m.taskCursor
		renderTaskRow(&b, t, width, isSelected)
	}

	b.WriteString("\n")
	b.WriteString(styleTextMuted.Render("  [Enter] view output  [d] cancel  [x] remove"))

	return b.String()
}

// renderTaskRow renders a single task row in the Tasks tab.
func renderTaskRow(b *strings.Builder, t *Task, width int, isSelected bool) {
	marker := "  "
	if isSelected {
		marker = styleAccentBlueBold.Render("▸ ")
	}

	icon := taskStatusIcon(t.Status)
	elapsed := taskElapsed(t)

	title := t.Title
	// Truncate title to fit width (accounting for marker + icon + elapsed)
	maxTitle := width - 12 // rough: marker(2) + icon(2) + space(1) + elapsed(~7)
	if maxTitle < 10 {
		maxTitle = 10
	}
	if len(title) > maxTitle {
		title = title[:maxTitle-3] + "..."
	}

	titleStyle := styleTextPrimary
	if t.Status == TaskCompleted {
		titleStyle = styleAccentGreen
	} else if t.Status == TaskFailed {
		titleStyle = styleAccentRed
	} else if t.Status == TaskCancelled {
		titleStyle = styleTextMuted
	}

	line := fmt.Sprintf("%s%s %s  %s",
		marker,
		icon,
		titleStyle.Render(title),
		styleTextSubtle.Render(elapsed),
	)
	if width > 0 {
		line = truncateToWidth(line, width)
	}
	b.WriteString(line + "\n")

	// Show error on second line for failed tasks
	if t.Status == TaskFailed && t.Error != "" && isSelected {
		errLine := fmt.Sprintf("    %s", styleTextMuted.Render(t.Error))
		if width > 0 {
			errLine = truncateToWidth(errLine, width)
		}
		b.WriteString(errLine + "\n")
	}
}

// taskStatusIcon returns a styled icon for the task status.
func taskStatusIcon(status TaskStatus) string {
	switch status {
	case TaskRunning:
		return styleAccentBlueBold.Render("⟳")
	case TaskCompleted:
		return styleAccentGreen.Render("✓")
	case TaskFailed:
		return styleAccentRed.Render("✗")
	case TaskCancelled:
		return styleTextMuted.Render("○")
	default:
		return " "
	}
}

// taskElapsed returns a human-friendly elapsed time string.
func taskElapsed(t *Task) string {
	if t.StartedAt.IsZero() {
		return ""
	}
	d := time.Since(t.StartedAt)
	switch {
	case d < time.Second:
		return "<1s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// renderTaskOutput renders a task's output for display in the diff pane.
func (m Model) renderTaskOutput(taskID int) string {
	var task *Task
	for _, t := range m.tasks {
		if t.ID == taskID {
			task = t
			break
		}
	}
	if task == nil {
		return styleTextMuted.Render("Task not found")
	}

	var b strings.Builder
	b.WriteString(styleAccentBlueBold.Render(task.Title) + "\n")
	b.WriteString(styleTextSubtle.Render(strings.Repeat("─", 40)) + "\n\n")

	output := task.Output()
	if output == "" {
		if task.Status == TaskRunning {
			b.WriteString(styleTextMuted.Render("  Waiting for output..."))
		} else {
			b.WriteString(styleTextMuted.Render("  No output"))
		}
	} else {
		b.WriteString(output)
	}

	if task.Status == TaskRunning {
		b.WriteString("\n\n" + styleAccentBlueBold.Render("  ⟳ Running..."))
	} else if task.Status == TaskFailed {
		b.WriteString("\n\n" + styleAccentRed.Render("  ✗ Failed: "+task.Error))
	} else if task.Status == TaskCompleted {
		b.WriteString("\n\n" + styleAccentGreen.Render("  ✓ Completed"))
	} else if task.Status == TaskCancelled {
		b.WriteString("\n\n" + styleTextMuted.Render("  ○ Cancelled"))
	}

	return b.String()
}

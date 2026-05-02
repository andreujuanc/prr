package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// renderActionsView renders the GitHub Actions status view for the diff viewport.
func (m *Model) renderActionsView() string {
	w := m.diffViewport.Width
	if w < 20 {
		w = 40
	}
	inner := w - 4 // margin

	var b strings.Builder

	// ── Header ──────────────────────────────────────────────
	b.WriteString(styleAccentBlueBold.Render("  ⚙ GitHub Actions") + "\n")
	b.WriteString("  " + styleTextSubtle.Render(strings.Repeat("═", inner)) + "\n\n")

	if m.actionsLoading && len(m.actionsRuns) == 0 {
		b.WriteString(styleTextMuted.Render("  Loading workflow runs...") + "\n")
		return b.String()
	}

	if len(m.actionsRuns) == 0 {
		b.WriteString(styleTextMuted.Render("  No workflow runs found for this PR.") + "\n\n")
		b.WriteString(styleTextSubtle.Render("  Press r to refresh") + "\n")
		return b.String()
	}

	// ── Workflow runs ───────────────────────────────────────
	row := 0
	for _, run := range m.actionsRuns {
		isSelected := row == m.actionsCursor

		icon := actionStatusIcon(run.Status, run.Conclusion)
		nameMaxW := inner - 30
		if nameMaxW < 10 {
			nameMaxW = 10
		}
		name := truncateToWidth(run.Name, nameMaxW)
		status := actionStatusLabel(run.Status, run.Conclusion)
		age := actionAge(run.UpdatedAt)

		// Build the line
		marker := "  "
		if isSelected {
			marker = styleAccentBlueBold.Render("▸ ")
		}

		nameStyle := styleTextSecondary
		if isSelected {
			nameStyle = styleAccentBlueBold
		}

		// Expand indicator
		expandIcon := ""
		if _, expanded := m.actionsExpanded[run.ID]; expanded {
			expandIcon = " ▾"
		}

		line := fmt.Sprintf("%s%s %s%s", marker, icon, nameStyle.Render(name), expandIcon)
		// Right-align status + age
		statusPart := fmt.Sprintf("  %s  %s", status, styleTextMuted.Render(age))
		line += statusPart

		if inner > 0 {
			line = truncateToWidth(line, w)
		}
		b.WriteString(line + "\n")
		row++

		// ── Expanded jobs ───────────────────────────────────
		if jobs, ok := m.actionsExpanded[run.ID]; ok {
			for i, job := range jobs {
				isJobSelected := row == m.actionsCursor

				jobIcon := actionStatusIcon(job.Status, job.Conclusion)
				jobStatus := actionStatusLabel(job.Status, job.Conclusion)

				// Tree connector
				connector := "├── "
				if i == len(jobs)-1 {
					connector = "└── "
				}

				jobMarker := "  "
				if isJobSelected {
					jobMarker = styleAccentBlueBold.Render("▸ ")
				}

				jobNameStyle := styleTextSecondary
				if isJobSelected {
					jobNameStyle = styleAccentBlueBold
				}

				jobLine := fmt.Sprintf("%s    %s%s %s  %s",
					jobMarker,
					styleTextSubtle.Render(connector),
					jobIcon,
					jobNameStyle.Render(job.Name),
					jobStatus,
				)
				if inner > 0 {
					jobLine = truncateToWidth(jobLine, w)
				}
				b.WriteString(jobLine + "\n")
				row++

				// ── Steps (shown for all jobs when run is expanded) ──
				if len(job.Steps) > 0 {
					for si, step := range job.Steps {
						stepIcon := actionStatusIcon(step.Status, step.Conclusion)
						stepConnector := "│       ├── "
						if si == len(job.Steps)-1 {
							stepConnector = "│       └── "
						}
						if i == len(jobs)-1 {
							stepConnector = "        ├── "
							if si == len(job.Steps)-1 {
								stepConnector = "        └── "
							}
						}
						stepLine := fmt.Sprintf("      %s%s %s",
							styleTextSubtle.Render(stepConnector),
							stepIcon,
							styleTextMuted.Render(step.Name),
						)
						if inner > 0 {
							stepLine = truncateToWidth(stepLine, w)
						}
						b.WriteString(stepLine + "\n")
					}
				}
			}
		}
	}

	// ── Footer ──────────────────────────────────────────────
	b.WriteString("\n")
	b.WriteString("  " + styleTextSubtle.Render(strings.Repeat("─", inner)) + "\n")

	hints := []string{"j/k navigate", "Enter expand/collapse", "r refresh"}
	if m.actionsPolling {
		hints = append(hints, styleAccentYellow.Render("polling"))
	}
	b.WriteString("  " + styleTextMuted.Render(strings.Join(hints, "  ·  ")) + "\n")

	return b.String()
}

// ── Action status rendering helpers ─────────────────────────────────────

func actionStatusIcon(status, conclusion string) string {
	switch status {
	case "completed":
		switch conclusion {
		case "success":
			return ftIconReviewedSt.Render("✓")
		case "failure", "timed_out":
			return lipgloss.NewStyle().Foreground(accentRed).Render("✗")
		case "cancelled":
			return styleTextMuted.Render("⊘")
		case "skipped":
			return styleTextMuted.Render("○")
		default:
			return styleTextMuted.Render("·")
		}
	case "in_progress":
		return styleAccentYellow.Render("⟳")
	case "queued":
		return styleTextMuted.Render("◌")
	default:
		return styleTextMuted.Render("·")
	}
}

func actionStatusLabel(status, conclusion string) string {
	switch status {
	case "completed":
		switch conclusion {
		case "success":
			return ftIconReviewedSt.Render("passed")
		case "failure":
			return lipgloss.NewStyle().Foreground(accentRed).Render("failed")
		case "timed_out":
			return lipgloss.NewStyle().Foreground(accentRed).Render("timed out")
		case "cancelled":
			return styleTextMuted.Render("cancelled")
		case "skipped":
			return styleTextMuted.Render("skipped")
		default:
			return styleTextMuted.Render(conclusion)
		}
	case "in_progress":
		return styleAccentYellow.Render("running")
	case "queued":
		return styleTextMuted.Render("queued")
	default:
		return styleTextMuted.Render(status)
	}
}

func actionAge(updatedAt string) string {
	t, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return ""
	}
	d := time.Since(t)

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

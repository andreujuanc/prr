package ui

import "github.com/charmbracelet/lipgloss"

var (
	primaryColor = lipgloss.Color("#00BFFF")
	focusColor   = lipgloss.Color("#FF8C00") // Orange for focused pane
	
	headerStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFF")).
		Background(primaryColor).
		Padding(0, 1).
		MarginBottom(1).
		Bold(true)

	paneStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#666"))

	helpStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262")).
		MarginTop(0)
)

package audit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/state"
	"github.com/charmbracelet/lipgloss"
)

// Chart styles (Catppuccin Mocha palette).
var (
	chartTitle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	chartLabel    = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))
	chartMuted    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))
	chartCritical = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8"))
	chartHigh     = lipgloss.NewStyle().Foreground(lipgloss.Color("#FAB387"))
	chartMedium   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F9E2AF"))
	chartLow      = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
)

// RenderCategoryChart returns a horizontal bar chart of findings by category.
func RenderCategoryChart(findings []state.DeepFinding) string {
	if len(findings) == 0 {
		return ""
	}

	// Count by category
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Category.String()]++
	}

	// Sort by count descending
	type catCount struct {
		name  string
		count int
	}
	sorted := make([]catCount, 0, len(counts))
	for name, count := range counts {
		sorted = append(sorted, catCount{name, count})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].count > sorted[j].count
	})

	// Find max for scaling
	maxCount := sorted[0].count
	maxBarWidth := 30

	// Find longest label for alignment
	maxLabel := 0
	for _, cc := range sorted {
		if len(cc.name) > maxLabel {
			maxLabel = len(cc.name)
		}
	}

	var b strings.Builder
	b.WriteString(chartTitle.Render("  Findings by Category"))
	b.WriteString("\n\n")

	for _, cc := range sorted {
		barLen := max((cc.count*maxBarWidth)/maxCount, 1)

		bar := strings.Repeat("█", barLen)
		padding := strings.Repeat(" ", maxLabel-len(cc.name))

		b.WriteString(fmt.Sprintf("  %s%s  %s %s\n",
			chartLabel.Render(cc.name), padding,
			chartMuted.Render(bar),
			chartLabel.Render(fmt.Sprintf("%d", cc.count))))
	}

	b.WriteString("\n")
	return b.String()
}

// RenderSeverityBar returns a single proportional bar showing severity distribution.
func RenderSeverityBar(findings []state.DeepFinding) string {
	if len(findings) == 0 {
		return ""
	}

	// Count by severity
	counts := map[string]int{}
	for _, f := range findings {
		sev := f.Severity
		if sev == "" {
			sev = "low"
		}
		counts[sev]++
	}

	total := len(findings)
	barWidth := 40

	// Order: critical, high, medium, low
	type sevEntry struct {
		name  string
		count int
		style lipgloss.Style
		char  rune
	}
	sevs := []sevEntry{
		{"critical", counts["critical"], chartCritical, '█'},
		{"high", counts["high"], chartHigh, '█'},
		{"medium", counts["medium"], chartMedium, '░'},
		{"low", counts["low"], chartLow, '░'},
	}

	var b strings.Builder
	b.WriteString(chartTitle.Render("  Severity"))
	b.WriteString("\n\n  ")

	// Render proportional bar
	for _, s := range sevs {
		if s.count == 0 {
			continue
		}
		segLen := max((s.count*barWidth)/total, 1)
		b.WriteString(s.style.Render(strings.Repeat(string(s.char), segLen)))
	}

	// Legend
	b.WriteString("\n  ")
	parts := []string{}
	for _, s := range sevs {
		if s.count == 0 {
			continue
		}
		parts = append(parts, s.style.Render(fmt.Sprintf("%d %s", s.count, s.name)))
	}
	b.WriteString(strings.Join(parts, chartMuted.Render("  ")))
	b.WriteString("\n\n")

	return b.String()
}

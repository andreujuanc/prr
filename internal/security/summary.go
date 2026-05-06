package security

import (
	"fmt"
	"sort"
	"strings"
)

// BuildSecuritySummary aggregates security metrics from review findings
// and AOI scan results into a SecuritySummary.
func BuildSecuritySummary(
	findings []FindingForRevalidation,
	revalidations []Revalidation,
	aoiReport *AOIReport,
) *SecuritySummary {
	summary := &SecuritySummary{
		BySeverity: make(map[string]int),
		ByCWE:      make(map[string]int),
	}

	if aoiReport != nil {
		summary.AOIsTotal = aoiReport.TotalAOIs
		summary.HighRiskFiles = aoiReport.HighRiskFiles
	}

	for _, f := range findings {
		if f.Category != "security" {
			continue
		}
		summary.TotalFindings++
		summary.BySeverity[f.Severity]++
		if f.CWE != "" {
			summary.ByCWE[f.CWE]++
		}
	}

	for i, r := range revalidations {
		if i >= len(findings) {
			break
		}
		summary.RevalidatedCount++
		switch r.Verdict {
		case "true-positive":
			summary.TruePositives++
		case "false-positive":
			summary.FalsePositives++
		}
	}

	return summary
}

// FormatSecuritySummary renders a SecuritySummary as a human-readable
// markdown block suitable for display in the TUI review pane.
func FormatSecuritySummary(s *SecuritySummary) string {
	if s == nil || s.TotalFindings == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Security Summary\n\n")
	sb.WriteString(fmt.Sprintf("**%d security finding(s)**", s.TotalFindings))

	if s.RevalidatedCount > 0 {
		sb.WriteString(fmt.Sprintf(" | %d revalidated (%d TP, %d FP)",
			s.RevalidatedCount, s.TruePositives, s.FalsePositives))
	}
	sb.WriteString("\n\n")

	// Severity breakdown
	if len(s.BySeverity) > 0 {
		sb.WriteString("By severity: ")
		order := []string{"critical", "high", "medium", "low", "nit"}
		var parts []string
		for _, sev := range order {
			if n, ok := s.BySeverity[sev]; ok && n > 0 {
				parts = append(parts, fmt.Sprintf("%s=%d", sev, n))
			}
		}
		sb.WriteString(strings.Join(parts, ", "))
		sb.WriteString("\n")
	}

	// CWE breakdown
	if len(s.ByCWE) > 0 {
		sb.WriteString("By CWE: ")
		type cwePair struct {
			cwe   string
			count int
		}
		var cwes []cwePair
		for c, n := range s.ByCWE {
			cwes = append(cwes, cwePair{c, n})
		}
		sort.Slice(cwes, func(i, j int) bool { return cwes[i].count > cwes[j].count })
		var parts []string
		for _, c := range cwes {
			parts = append(parts, fmt.Sprintf("%s(%d)", c.cwe, c.count))
		}
		sb.WriteString(strings.Join(parts, ", "))
		sb.WriteString("\n")
	}

	// AOI coverage
	if s.AOIsTotal > 0 {
		sb.WriteString(fmt.Sprintf("AOI coverage: %d areas scanned", s.AOIsTotal))
		if len(s.HighRiskFiles) > 0 {
			sb.WriteString(fmt.Sprintf(", %d high-risk files", len(s.HighRiskFiles)))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

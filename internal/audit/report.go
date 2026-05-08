package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/state"
)

// ReportJSON is the JSON-serializable form of an audit result.
type ReportJSON struct {
	FilesScanned             int                 `json:"files_scanned"`
	AOIsGenerated            int                 `json:"aois_generated"`
	ReviewCalls              int                 `json:"review_calls"`
	IndividualReviews        int                 `json:"individual_reviews"`
	GroupedReviews           int                 `json:"grouped_reviews"`
	Findings                 []state.DeepFinding `json:"findings"`
	Dismissals               int                 `json:"dismissals"`
	CrossCuttingObservations []string            `json:"cross_cutting_observations,omitempty"`
	SkippedSubcategories     []string            `json:"skipped_subcategories,omitempty"`
}

func toReportJSON(r *Result) ReportJSON {
	findings := r.Findings
	if findings == nil {
		findings = []state.DeepFinding{}
	}
	return ReportJSON{
		FilesScanned:             r.FilesScanned,
		AOIsGenerated:            r.AOIsGenerated,
		ReviewCalls:              r.ReviewCalls,
		IndividualReviews:        r.IndividualReviews,
		GroupedReviews:           r.GroupedReviews,
		Findings:                 findings,
		Dismissals:               r.Dismissals,
		CrossCuttingObservations: r.CrossCuttingObservations,
		SkippedSubcategories:     r.SkippedSubcategories,
	}
}

// ExportJSON writes the audit result as structured JSON to the given path.
func ExportJSON(result *Result, path string) error {
	report := toReportJSON(result)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// ExportMarkdown writes a styled markdown report to the given path.
func ExportMarkdown(result *Result, path string) error {
	var b strings.Builder

	b.WriteString("# prr Audit Report\n\n")

	// Summary
	b.WriteString("## Summary\n")
	fmt.Fprintf(&b, "- Files scanned: %d\n", result.FilesScanned)
	fmt.Fprintf(&b, "- Areas of interest: %d\n", result.AOIsGenerated)
	fmt.Fprintf(&b, "- Review calls: %d (%d individual, %d grouped)\n",
		result.ReviewCalls, result.IndividualReviews, result.GroupedReviews)
	fmt.Fprintf(&b, "- Findings: %d\n", len(result.Findings))
	fmt.Fprintf(&b, "- Dismissed: %d\n", result.Dismissals)
	b.WriteString("\n")

	// Findings grouped by severity
	severityOrder := []string{"critical", "high", "medium", "low"}
	grouped := map[string][]state.DeepFinding{}
	for _, f := range result.Findings {
		grouped[f.Severity] = append(grouped[f.Severity], f)
	}
	// Sort each group by file path
	for _, findings := range grouped {
		sort.Slice(findings, func(i, j int) bool {
			return findings[i].File < findings[j].File
		})
	}

	if len(result.Findings) > 0 {
		b.WriteString("## Findings\n\n")
		for _, sev := range severityOrder {
			findings := grouped[sev]
			if len(findings) == 0 {
				continue
			}
			b.WriteString("### " + strings.Title(sev) + "\n\n")
			for _, f := range findings {
				loc := f.File
				if f.Lines != "" {
					loc += ":" + f.Lines
				}
				fmt.Fprintf(&b, "#### %s [%s] %s\n", f.FindingID, loc, f.Title)
				cat := f.Category
				if f.Subcategory != "" {
					cat += " / " + f.Subcategory
				}
				fmt.Fprintf(&b, "**Category:** %s  \n", cat)
				fmt.Fprintf(&b, "**Trigger:** %s  \n", f.Trigger)
				if f.Suggestion != "" {
					fmt.Fprintf(&b, "**Fix:** %s  \n", f.Suggestion)
				}
				b.WriteString("\n")
			}
		}
	}

	// Cross-cutting observations are used as input to Phase 4 synthesis
	// but not included in the report — the synthesis output covers them.

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// Export auto-detects format from file extension (.json or .md/.markdown).
func Export(result *Result, path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		return ExportJSON(result, path)
	case ".md", ".markdown":
		return ExportMarkdown(result, path)
	default:
		return fmt.Errorf("unsupported export format %q (use .json or .md)", ext)
	}
}

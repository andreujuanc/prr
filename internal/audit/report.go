package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/andreujuanc/prr/internal/state"
)

// reportSeverityOrder is the canonical severity ordering used in the markdown
// and JSON reports. Keep in sync with the 5-level scale used elsewhere.
var reportSeverityOrder = []string{"critical", "high", "medium", "low", "nit"}

// ReportJSON is the JSON-serializable form of an audit result.
type ReportJSON struct {
	FilesScanned             int                     `json:"files_scanned"`
	AOIsGenerated            int                     `json:"aois_generated"`
	ReviewCalls              int                     `json:"review_calls"`
	IndividualReviews        int                     `json:"individual_reviews"`
	GroupedReviews           int                     `json:"grouped_reviews"`
	FailedReviews            int                     `json:"failed_reviews,omitempty"`
	Findings                 []state.DeepFinding     `json:"findings"`
	Dismissals               int                     `json:"dismissals"`
	RecheckDismissals        []state.DismissedRecord `json:"recheck_dismissals,omitempty"`
	CrossCuttingObservations []string                `json:"cross_cutting_observations,omitempty"`
	SkippedSubcategories     []string                `json:"skipped_subcategories,omitempty"`
	Synthesis                *SynthesisResult        `json:"synthesis,omitempty"`
}

func toReportJSON(r *Result, synthesis *SynthesisResult) ReportJSON {
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
		FailedReviews:            r.FailedReviews,
		Findings:                 findings,
		Dismissals:               r.Dismissals,
		RecheckDismissals:        r.RecheckDismissals,
		CrossCuttingObservations: r.CrossCuttingObservations,
		SkippedSubcategories:     r.SkippedSubcategories,
		Synthesis:                synthesis,
	}
}

// MarshalJSON returns the JSON byte payload for an audit result.
// Shared between ExportJSON (writes user-specified path) and the
// auto-persisted snapshot in `prr audit` (writes
// .git/pr-tui/audits/...). Both paths produce byte-identical
// content so users diffing or scripting against either get the same
// shape.
//
// synthesis may be nil when synthesis was disabled or failed.
func MarshalJSON(result *Result, synthesis *SynthesisResult) ([]byte, error) {
	report := toReportJSON(result, synthesis)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal report: %w", err)
	}
	return data, nil
}

// ExportJSON writes the audit result as structured JSON to the given path.
// synthesis may be nil when synthesis was disabled or failed.
func ExportJSON(result *Result, synthesis *SynthesisResult, path string) error {
	data, err := MarshalJSON(result, synthesis)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ExportMarkdown writes a styled markdown report to the given path.
// synthesis may be nil when synthesis was disabled or failed.
func ExportMarkdown(result *Result, synthesis *SynthesisResult, path string) error {
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
	if result.FailedReviews > 0 {
		fmt.Fprintf(&b, "- Failed reviews: %d (these AOIs were not reviewed; see logs)\n", result.FailedReviews)
	}
	b.WriteString("\n")

	// Synthesis (if available) — most valuable for executive consumers, so it
	// goes ahead of the per-finding detail.
	writeSynthesisMarkdown(&b, synthesis)

	// Findings grouped by severity
	if len(result.Findings) > 0 {
		grouped := groupFindingsBySeverity(result.Findings)

		b.WriteString("## Findings\n\n")
		for _, sev := range reportSeverityOrder {
			findings := grouped[sev]
			if len(findings) == 0 {
				continue
			}
			b.WriteString("### " + strings.ToUpper(sev[:1]) + sev[1:] + "\n\n")
			for _, f := range findings {
				loc := f.File
				if f.Lines != "" {
					loc += ":" + f.Lines
				}
				fmt.Fprintf(&b, "#### %s [%s] %s\n", f.FindingID, loc, f.Title)
				cat := f.Category.String()
				if f.Subcategory != "" {
					cat += " / " + f.Subcategory
				}
				fmt.Fprintf(&b, "**Category:** %s  \n", cat)
				if f.Trigger.Repro != "" {
					fmt.Fprintf(&b, "**Trigger:** %s  \n", f.Trigger.Repro)
				}
				if f.Trigger.Observable != "" {
					fmt.Fprintf(&b, "**Observable:** %s  \n", f.Trigger.Observable)
				}
				if f.ConfidenceScore > 0 {
					if f.ConfidenceReasoning != "" {
						fmt.Fprintf(&b, "**Confidence:** %d/100 — %s  \n", f.ConfidenceScore, f.ConfidenceReasoning)
					} else {
						fmt.Fprintf(&b, "**Confidence:** %d/100  \n", f.ConfidenceScore)
					}
				}
				if len(f.Trace) > 0 {
					b.WriteString("**Trace:**\n")
					for _, h := range f.Trace {
						role := h.Role
						if role == "" {
							role = "?"
						}
						loc := h.File
						if h.Lines != "" {
							loc += ":" + h.Lines
						}
						if h.Evidence != "" {
							fmt.Fprintf(&b, "- `%s` %s — %s\n", role, loc, h.Evidence)
						} else {
							fmt.Fprintf(&b, "- `%s` %s\n", role, loc)
						}
					}
				}
				if len(f.DefensesChecked) > 0 {
					fmt.Fprintf(&b, "**Defenses checked:** %s  \n", strings.Join(f.DefensesChecked, ", "))
				}
				if f.SiblingDeviation != nil {
					fmt.Fprintf(&b, "**Sibling pattern:** %s  \n", strings.TrimSpace(f.SiblingDeviation.Pattern))
					if ids := f.SiblingDeviation.SiblingIDs; len(ids) > 0 {
						capped := ids
						if len(capped) > 5 {
							capped = capped[:5]
						}
						fmt.Fprintf(&b, "**Conforming siblings:** %s  \n", strings.Join(capped, ", "))
					}
				}
				if len(f.AffectedSites) > 0 {
					b.WriteString("**Affected sites:**\n")
					for _, s := range f.AffectedSites {
						loc := s.File
						if s.Lines != "" {
							loc += ":" + s.Lines
						}
						if s.Symbol != "" {
							fmt.Fprintf(&b, "- %s (`%s`)\n", loc, s.Symbol)
						} else {
							fmt.Fprintf(&b, "- %s\n", loc)
						}
					}
				}
				if f.Suggestion != "" {
					fmt.Fprintf(&b, "**Fix:** %s  \n", f.Suggestion)
				}
				b.WriteString("\n")
			}
		}
	}

	// Cross-cutting observations — kept verbatim. Synthesis paraphrases them
	// for the executive summary; the originals are still useful raw input.
	if len(result.CrossCuttingObservations) > 0 {
		b.WriteString("## Cross-Cutting Observations\n\n")
		for _, obs := range result.CrossCuttingObservations {
			fmt.Fprintf(&b, "- %s\n", obs)
		}
		b.WriteString("\n")
	}

	// Recheck dismissals — show users what got dropped and why.
	// Without this, a finding's disappearance after recheck is
	// invisible; with it, the user can spot over-aggressive
	// dismissals and override them in future runs.
	if len(result.RecheckDismissals) > 0 {
		writeRecheckDismissalsMarkdown(&b, result.RecheckDismissals)
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// writeRecheckDismissalsMarkdown renders the per-finding dismissal log
// from Phase 3b. Goes at the end of the report because it's an audit-
// trail section, not a primary deliverable.
func writeRecheckDismissalsMarkdown(b *strings.Builder, dismissals []state.DismissedRecord) {
	fmt.Fprintf(b, "## Recheck Dismissals (%d)\n\n", len(dismissals))
	b.WriteString("Findings the recheck pass removed, with the model's rationale. ")
	b.WriteString("Review here to override any that look wrong on closer reading.\n\n")
	for _, d := range dismissals {
		loc := d.Finding.File
		if d.Finding.Lines != "" {
			loc += ":" + d.Finding.Lines
		}
		title := d.Finding.Title
		if title == "" {
			title = "(no title)"
		}
		fmt.Fprintf(b, "#### %s [%s] %s\n", d.FindingID, loc, title)
		if d.Finding.Severity != "" {
			fmt.Fprintf(b, "**Severity (pre-recheck):** %s  \n", d.Finding.Severity)
		}
		rationale := d.Rationale
		if rationale == "" {
			rationale = "(no rationale provided)"
		}
		fmt.Fprintf(b, "**Rationale:** %s  \n\n", rationale)
	}
}

// writeSynthesisMarkdown emits the executive summary, top risks, systemic
// patterns, and recommendations from a synthesis result. No-op when nil.
func writeSynthesisMarkdown(b *strings.Builder, s *SynthesisResult) {
	if s == nil {
		return
	}
	if s.ExecutiveSummary != "" {
		b.WriteString("## Executive Summary\n\n")
		b.WriteString(s.ExecutiveSummary)
		b.WriteString("\n\n")
	}
	if len(s.TopRisks) > 0 {
		b.WriteString("## Top Risks\n\n")
		for i, r := range s.TopRisks {
			fmt.Fprintf(b, "%d. %s\n", i+1, r)
		}
		b.WriteString("\n")
	}
	if len(s.SystemicPatterns) > 0 {
		b.WriteString("## Systemic Patterns\n\n")
		for _, p := range s.SystemicPatterns {
			fmt.Fprintf(b, "- %s\n", p)
		}
		b.WriteString("\n")
	}
	if len(s.Recommendations) > 0 {
		b.WriteString("## Recommendations\n\n")
		for i, r := range s.Recommendations {
			fmt.Fprintf(b, "%d. %s\n", i+1, r)
		}
		b.WriteString("\n")
	}
}

// groupFindingsBySeverity groups findings by their severity string and sorts
// each bucket by file path then by the leading line number in the Lines
// field. Lines is a string like "33-41"; we parse the leading int (or 0 on
// failure) so two findings in the same file appear in source order.
func groupFindingsBySeverity(findings []state.DeepFinding) map[string][]state.DeepFinding {
	grouped := make(map[string][]state.DeepFinding)
	for _, f := range findings {
		grouped[f.Severity] = append(grouped[f.Severity], f)
	}
	for _, group := range grouped {
		sort.Slice(group, func(i, j int) bool {
			if group[i].File != group[j].File {
				return group[i].File < group[j].File
			}
			return leadingLineNumber(group[i].Lines) < leadingLineNumber(group[j].Lines)
		})
	}
	return grouped
}

// leadingLineNumber parses the integer at the start of a "33-41" / "42" /
// "L42" style range. Returns 0 if no leading digits are present.
func leadingLineNumber(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

// Export auto-detects format from file extension (.json or .md/.markdown).
// synthesis may be nil; the report is still written without the executive
// summary section in that case.
func Export(result *Result, synthesis *SynthesisResult, path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		return ExportJSON(result, synthesis, path)
	case ".md", ".markdown":
		return ExportMarkdown(result, synthesis, path)
	default:
		return fmt.Errorf("unsupported export format %q (use .json or .md)", ext)
	}
}

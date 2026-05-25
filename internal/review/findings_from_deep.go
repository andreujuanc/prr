package review

import (
	"sort"
	"strconv"
	"strings"

	"github.com/andreujuanc/prr/internal/state"
)

// BuildFindingsFromDeep maps post-recheck DeepFindings to ReviewFindings
// deterministically — no LLM involvement. Each DeepFinding becomes one
// ReviewFinding with source_ids=[FindingID]. Category, severity, and all
// other fields are copied straight through, so the output cannot be
// invalidated by a synthesis LLM picking off-list category slugs.
//
// Recheck (Phase 1c) is responsible for cross-file consolidation and
// dismissal; by the time findings reach here they are already the final
// set. Sorted by severity (critical first, nit last) to match the order
// the legacy LLM-authored synthesis used.
func BuildFindingsFromDeep(deep []state.DeepFinding) []state.ReviewFinding {
	out := make([]state.ReviewFinding, 0, len(deep))
	for _, d := range deep {
		out = append(out, state.ReviewFinding{
			Severity:            d.Severity,
			ConfidenceScore:     d.ConfidenceScore,
			ConfidenceReasoning: d.ConfidenceReasoning,
			Category:            d.Category,
			File:                d.File,
			Line:                firstLine(d.Lines),
			Title:               d.Title,
			Detail:              d.Description,
			Suggestion:          d.Suggestion,
			SourceIDs:           sourceIDs(d.FindingID),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].SeverityRank() < out[j].SeverityRank()
	})
	return out
}

// firstLine parses the first integer out of a Lines field like "31" or
// "31-33". Returns 0 when no number is present.
func firstLine(lines string) int {
	s := strings.TrimSpace(lines)
	if s == "" {
		return 0
	}
	if i := strings.IndexAny(s, "-,: "); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func sourceIDs(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

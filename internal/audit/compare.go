package audit

import (
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/state"
)

// CompareResult holds the diff between two audit runs.
type CompareResult struct {
	New        []state.DeepFinding // findings in current but not previous
	Resolved   []state.DeepFinding // findings in previous but not current
	Persistent []state.DeepFinding // findings in both (may have changed severity etc)
}

// CompareFindings compares current findings against a previous set.
// Matching is done by (File, Category, Subcategory, Title) tuple —
// line numbers may shift between runs.
func CompareFindings(current, previous []state.DeepFinding) CompareResult {
	prevMap := make(map[string]state.DeepFinding, len(previous))
	for _, f := range previous {
		prevMap[findingKey(f)] = f
	}

	curMap := make(map[string]state.DeepFinding, len(current))
	for _, f := range current {
		curMap[findingKey(f)] = f
	}

	var result CompareResult

	for _, f := range current {
		key := findingKey(f)
		if _, ok := prevMap[key]; ok {
			result.Persistent = append(result.Persistent, f)
		} else {
			result.New = append(result.New, f)
		}
	}

	for _, f := range previous {
		key := findingKey(f)
		if _, ok := curMap[key]; !ok {
			result.Resolved = append(result.Resolved, f)
		}
	}

	return result
}

// findingKey returns the identity key for matching findings across runs.
func findingKey(f state.DeepFinding) string {
	return strings.Join([]string{f.File, f.Category, f.Subcategory, f.Title}, "\x00")
}

// FormatComparison returns a human-readable summary like:
// "3 new findings, 2 resolved, 10 persistent"
func (c CompareResult) FormatComparison() string {
	return fmt.Sprintf("%d new findings, %d resolved, %d persistent",
		len(c.New), len(c.Resolved), len(c.Persistent))
}

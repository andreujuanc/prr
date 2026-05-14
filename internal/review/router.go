// Package review implements the unified review pipeline for both PR review
// and full-project audit. It handles Phase 3 routing (individual vs grouped
// review calls) and Phase 4 synthesis.
package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/security"
)

// ReviewCall represents a single LLM call to be made during Phase 3.
// Either an individual deep review or a grouped subcategory review.
type ReviewCall struct {
	// Type is "individual" or "grouped".
	Type string

	// Category and Subcategory identify the concern area.
	Category    string
	Subcategory string

	// AOIs contains the areas of interest for this call.
	// For individual calls, this has exactly one element.
	// For grouped calls, this has all AOIs in the subcategory.
	AOIs []security.AreaOfInterest

	// Files lists all unique files referenced by AOIs in this call.
	Files []string
}

// RouteResult holds the organized review calls after Phase 3 routing.
type RouteResult struct {
	// Individual calls, ordered by file path then line number.
	Individual []ReviewCall

	// Grouped calls, ordered by AOI count descending (most AOIs first).
	Grouped []ReviewCall

	// Stats for reporting.
	TotalAOIs        int
	IndividualCount  int
	GroupedCount     int
	SubcategoryCount int
}

// RouteAOIs takes all AOI scan results and organizes them into review calls.
// Individual AOIs get their own call; grouped AOIs are batched by subcategory.
//
// focusDimensions filters which AOIs make it to Phase 3. If nil or empty,
// all AOIs are included. If set, only AOIs whose dimensions overlap with
// the focus set are included.
//
// maxGroupSize caps the number of AOIs per grouped call. If a subcategory
// has more AOIs than this, it is split into multiple grouped calls.
// Use 0 for no limit.
func RouteAOIs(results []security.AOIScanResult, focusDimensions []string, maxGroupSize int) *RouteResult {
	if maxGroupSize <= 0 {
		maxGroupSize = 10 // default cap per the plan
	}

	focusSet := make(map[string]bool, len(focusDimensions))
	for _, d := range focusDimensions {
		focusSet[d] = true
	}
	hasFocus := len(focusSet) > 0

	// Collect all AOIs across all files
	var allAOIs []security.AreaOfInterest
	for _, r := range results {
		for _, aoi := range r.AreasOfInterest {
			// Apply focus filter
			if hasFocus && !aoiMatchesFocus(aoi, focusSet) {
				continue
			}
			allAOIs = append(allAOIs, aoi)
		}
	}

	result := &RouteResult{
		TotalAOIs: len(allAOIs),
	}

	// Separate individual from grouped
	// Key for grouped: "category/subcategory"
	grouped := make(map[string][]security.AreaOfInterest)

	for _, aoi := range allAOIs {
		if aoi.Urgency == "individual" {
			call := ReviewCall{
				Type:        "individual",
				Category:    aoi.Category,
				Subcategory: aoi.Subcategory,
				AOIs:        []security.AreaOfInterest{aoi},
				Files:       []string{aoi.File},
			}
			result.Individual = append(result.Individual, call)
			result.IndividualCount++
		} else {
			// Default to grouped
			key := subcategoryKey(aoi.Category, aoi.Subcategory)
			grouped[key] = append(grouped[key], aoi)
		}
	}

	// Sort individual calls by file path, then line number
	sort.Slice(result.Individual, func(i, j int) bool {
		a, b := result.Individual[i].AOIs[0], result.Individual[j].AOIs[0]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})

	// Build grouped calls, splitting large subcategories
	var groupedCalls []ReviewCall
	for key, aois := range grouped {
		cat, subcat := parseSubcategoryKey(key)

		// Sort AOIs within group by file then line
		sort.Slice(aois, func(i, j int) bool {
			if aois[i].File != aois[j].File {
				return aois[i].File < aois[j].File
			}
			return aois[i].Line < aois[j].Line
		})

		// Split into chunks if needed
		for i := 0; i < len(aois); i += maxGroupSize {
			end := min(i+maxGroupSize, len(aois))
			chunk := aois[i:end]

			files := uniqueFiles(chunk)
			groupedCalls = append(groupedCalls, ReviewCall{
				Type:        "grouped",
				Category:    cat,
				Subcategory: subcat,
				AOIs:        chunk,
				Files:       files,
			})
		}
	}

	// Sort grouped calls by AOI count descending (most AOIs = most likely systemic)
	sort.Slice(groupedCalls, func(i, j int) bool {
		return len(groupedCalls[i].AOIs) > len(groupedCalls[j].AOIs)
	})

	result.Grouped = groupedCalls
	result.GroupedCount = len(allAOIs) - result.IndividualCount
	result.SubcategoryCount = len(grouped)

	return result
}

// PrioritizedCalls returns all review calls in priority order:
// individual calls first (they're critical), then grouped calls
// ordered by AOI count descending. If maxCalls > 0, returns at most
// that many calls.
func (r *RouteResult) PrioritizedCalls(maxCalls int) []ReviewCall {
	var calls []ReviewCall
	calls = append(calls, r.Individual...)
	calls = append(calls, r.Grouped...)

	if maxCalls > 0 && len(calls) > maxCalls {
		calls = calls[:maxCalls]
	}
	return calls
}

// SkippedSubcategories returns the subcategories that would be skipped
// if maxCalls is applied. Useful for reporting to the user.
func (r *RouteResult) SkippedSubcategories(maxCalls int) []string {
	if maxCalls <= 0 {
		return nil
	}

	allCalls := r.PrioritizedCalls(0)
	if maxCalls >= len(allCalls) {
		return nil
	}

	skipped := allCalls[maxCalls:]
	seen := make(map[string]bool)
	var result []string
	for _, call := range skipped {
		key := subcategoryKey(call.Category, call.Subcategory)
		if !seen[key] {
			seen[key] = true
			result = append(result, key)
		}
	}
	return result
}

// aoiMatchesFocus returns true if any of the AOI's dimensions overlap
// with the focus set.
func aoiMatchesFocus(aoi security.AreaOfInterest, focusSet map[string]bool) bool {
	if len(aoi.Dimensions) == 0 {
		// Legacy AOIs without dimensions: always include
		return true
	}
	for _, dim := range aoi.Dimensions {
		if focusSet[dim] {
			return true
		}
	}
	return false
}

func subcategoryKey(category, subcategory string) string {
	if subcategory == "" {
		return category
	}
	return category + "/" + subcategory
}

func parseSubcategoryKey(key string) (category, subcategory string) {
	if before, after, ok := strings.Cut(key, "/"); ok {
		return before, after
	}
	return key, ""
}

func uniqueFiles(aois []security.AreaOfInterest) []string {
	seen := make(map[string]bool, len(aois))
	var files []string
	for _, aoi := range aois {
		if !seen[aoi.File] {
			seen[aoi.File] = true
			files = append(files, aoi.File)
		}
	}
	sort.Strings(files)
	return files
}

// FormatSummary returns a human-readable summary of the routing result.
func (r *RouteResult) FormatSummary() string {
	if r.TotalAOIs == 0 {
		return "No areas of interest found."
	}

	return fmt.Sprintf("%d AOIs → %d individual review(s) + %d grouped review(s) across %d subcategorie(s) = %d total call(s)",
		r.TotalAOIs,
		r.IndividualCount,
		len(r.Grouped),
		r.SubcategoryCount,
		len(r.Individual)+len(r.Grouped),
	)
}

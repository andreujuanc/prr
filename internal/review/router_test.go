package review

import (
	"testing"

	"github.com/andreujuanc/prr/internal/security"
)

func makeAOI(file string, line int, cat, subcat, urgency string, cats []string) security.AreaOfInterest {
	return security.AreaOfInterest{
		File:        file,
		Line:        line,
		Category:    cat,
		Subcategory: subcat,
		Urgency:     urgency,
		Categories:  cats,
		Concern:     "test concern",
		ID:          file + "-" + subcat,
	}
}

func TestRouteAOIs_BasicRouting(t *testing.T) {
	results := []security.AOIScanResult{
		{
			File: "billing/charge.go",
			AreasOfInterest: []security.AreaOfInterest{
				makeAOI("billing/charge.go", 45, "financial", "money-arithmetic", "individual", []string{"correctness"}),
				makeAOI("billing/charge.go", 88, "error-handling", "swallowed-errors", "grouped", []string{"error-handling"}),
			},
		},
		{
			File: "auth/handler.go",
			AreasOfInterest: []security.AreaOfInterest{
				makeAOI("auth/handler.go", 33, "error-handling", "swallowed-errors", "grouped", []string{"error-handling"}),
				makeAOI("auth/handler.go", 100, "authentication", "token-validation", "individual", []string{"authentication"}),
			},
		},
	}

	r := RouteAOIs(results, nil, 0)

	if r.TotalAOIs != 4 {
		t.Errorf("TotalAOIs: got %d, want 4", r.TotalAOIs)
	}
	if r.IndividualCount != 2 {
		t.Errorf("IndividualCount: got %d, want 2", r.IndividualCount)
	}
	if len(r.Individual) != 2 {
		t.Fatalf("Individual calls: got %d, want 2", len(r.Individual))
	}
	if r.GroupedCount != 2 {
		t.Errorf("GroupedCount: got %d, want 2", r.GroupedCount)
	}

	// Grouped: both swallowed-errors AOIs should be in one call
	if len(r.Grouped) != 1 {
		t.Fatalf("Grouped calls: got %d, want 1", len(r.Grouped))
	}
	g := r.Grouped[0]
	if g.Subcategory != "swallowed-errors" {
		t.Errorf("Grouped subcategory: got %q, want %q", g.Subcategory, "swallowed-errors")
	}
	if len(g.AOIs) != 2 {
		t.Errorf("Grouped AOIs: got %d, want 2", len(g.AOIs))
	}
	if len(g.Files) != 2 {
		t.Errorf("Grouped files: got %d, want 2", len(g.Files))
	}
}

func TestRouteAOIs_FocusFilter(t *testing.T) {
	results := []security.AOIScanResult{
		{
			File: "a.go",
			AreasOfInterest: []security.AreaOfInterest{
				makeAOI("a.go", 10, "financial", "money-arithmetic", "individual", []string{"correctness", "financial"}),
				makeAOI("a.go", 20, "error-handling", "swallowed-errors", "grouped", []string{"error-handling"}),
				makeAOI("a.go", 30, "performance", "memory", "grouped", []string{"performance"}),
			},
		},
	}

	// Focus on correctness + financial only
	r := RouteAOIs(results, []string{"correctness", "financial"}, 0)

	if r.TotalAOIs != 1 {
		t.Errorf("TotalAOIs: got %d, want 1 (only the financial AOI matches focus)", r.TotalAOIs)
	}
	if r.IndividualCount != 1 {
		t.Errorf("IndividualCount: got %d, want 1", r.IndividualCount)
	}
	if len(r.Grouped) != 0 {
		t.Errorf("Grouped: got %d, want 0", len(r.Grouped))
	}
}

func TestRouteAOIs_MaxGroupSize(t *testing.T) {
	var aois []security.AreaOfInterest
	for i := range 25 {
		aois = append(aois, makeAOI("a.go", i+1, "error-handling", "swallowed-errors", "grouped", []string{"error-handling"}))
	}

	results := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: aois},
	}

	r := RouteAOIs(results, nil, 10)

	// 25 grouped AOIs with maxGroupSize=10 → 3 grouped calls (10+10+5)
	if len(r.Grouped) != 3 {
		t.Errorf("Grouped calls: got %d, want 3", len(r.Grouped))
	}
}

func TestRouteAOIs_PrioritizedCalls(t *testing.T) {
	results := []security.AOIScanResult{
		{
			File: "a.go",
			AreasOfInterest: []security.AreaOfInterest{
				makeAOI("a.go", 10, "authentication", "token-validation", "individual", nil),
				makeAOI("a.go", 20, "error-handling", "swallowed-errors", "grouped", nil),
				makeAOI("a.go", 30, "concurrency", "race-conditions", "grouped", nil),
			},
		},
	}

	r := RouteAOIs(results, nil, 0)

	// With maxCalls=2: should get 1 individual + 1 grouped
	calls := r.PrioritizedCalls(2)
	if len(calls) != 2 {
		t.Fatalf("PrioritizedCalls(2): got %d, want 2", len(calls))
	}
	if calls[0].Type != "individual" {
		t.Errorf("First call should be individual, got %q", calls[0].Type)
	}
}

func TestRouteAOIs_EmptyUrgencyDefaultsToGrouped(t *testing.T) {
	results := []security.AOIScanResult{
		{
			File: "a.go",
			AreasOfInterest: []security.AreaOfInterest{
				makeAOI("a.go", 10, "error-handling", "swallowed-errors", "", nil), // empty urgency
			},
		},
	}

	r := RouteAOIs(results, nil, 0)

	if r.IndividualCount != 0 {
		t.Errorf("IndividualCount: got %d, want 0 (empty urgency should default to grouped)", r.IndividualCount)
	}
	if len(r.Grouped) != 1 {
		t.Errorf("Grouped: got %d, want 1", len(r.Grouped))
	}
}

func TestRouteAOIs_LegacyAOIsPassFocusFilter(t *testing.T) {
	// Legacy AOIs without dimensions should always pass focus filter
	results := []security.AOIScanResult{
		{
			File: "a.go",
			AreasOfInterest: []security.AreaOfInterest{
				{File: "a.go", Line: 10, Category: "sql", Confidence: "high"}, // legacy, no dimensions
			},
		},
	}

	r := RouteAOIs(results, []string{"correctness"}, 0)

	if r.TotalAOIs != 1 {
		t.Errorf("TotalAOIs: got %d, want 1 (legacy AOIs should pass focus filter)", r.TotalAOIs)
	}
}

func TestSkippedSubcategories(t *testing.T) {
	results := []security.AOIScanResult{
		{
			File: "a.go",
			AreasOfInterest: []security.AreaOfInterest{
				makeAOI("a.go", 10, "auth", "token-validation", "individual", nil),
				makeAOI("a.go", 20, "error-handling", "swallowed-errors", "grouped", nil),
				makeAOI("a.go", 30, "concurrency", "race-conditions", "grouped", nil),
			},
		},
	}

	r := RouteAOIs(results, nil, 0)

	// With maxCalls=2: 1 individual + 2 grouped = 3 total, so 1 is skipped
	skipped := r.SkippedSubcategories(2)
	if len(skipped) != 1 {
		t.Errorf("SkippedSubcategories(2): got %d, want 1", len(skipped))
	}
}

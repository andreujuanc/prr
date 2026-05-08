package audit

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/review"
	"github.com/andreujuanc/prr/internal/security"
)

func makeRouteResult(individual, grouped int) *review.RouteResult {
	r := &review.RouteResult{}
	for i := 0; i < individual; i++ {
		r.Individual = append(r.Individual, review.ReviewCall{
			Type: "individual",
			AOIs: []security.AreaOfInterest{{File: "a.go", Line: i + 1}},
		})
	}
	for i := 0; i < grouped; i++ {
		r.Grouped = append(r.Grouped, review.ReviewCall{
			Type: "grouped",
			AOIs: []security.AreaOfInterest{{File: "b.go"}, {File: "c.go"}},
		})
	}
	return r
}

func TestEstimateCost_NoCap(t *testing.T) {
	routing := makeRouteResult(12, 8)
	pricing := DefaultPricing("gemini-3.1-pro-preview")
	est := EstimateCost(routing, 0, pricing)

	if est.TotalCalls != 20 {
		t.Errorf("TotalCalls = %d, want 20", est.TotalCalls)
	}
	if est.IndividualCalls != 12 {
		t.Errorf("IndividualCalls = %d, want 12", est.IndividualCalls)
	}
	if est.GroupedCalls != 8 {
		t.Errorf("GroupedCalls = %d, want 8", est.GroupedCalls)
	}
	if est.SkippedCalls != 0 {
		t.Errorf("SkippedCalls = %d, want 0", est.SkippedCalls)
	}

	expectedInput := 12*4000 + 8*6000
	if est.EstInputTokens != expectedInput {
		t.Errorf("EstInputTokens = %d, want %d", est.EstInputTokens, expectedInput)
	}
	expectedOutput := 20 * 1500
	if est.EstOutputTokens != expectedOutput {
		t.Errorf("EstOutputTokens = %d, want %d", est.EstOutputTokens, expectedOutput)
	}
	if est.EstCostUSD <= 0 {
		t.Error("EstCostUSD should be positive")
	}
}

func TestEstimateCost_WithCap(t *testing.T) {
	routing := makeRouteResult(10, 10)
	pricing := DefaultPricing("gemini-3.1-flash-lite")
	est := EstimateCost(routing, 15, pricing)

	if est.TotalCalls != 15 {
		t.Errorf("TotalCalls = %d, want 15", est.TotalCalls)
	}
	if est.IndividualCalls != 10 {
		t.Errorf("IndividualCalls = %d, want 10", est.IndividualCalls)
	}
	if est.GroupedCalls != 5 {
		t.Errorf("GroupedCalls = %d, want 5", est.GroupedCalls)
	}
	if est.SkippedCalls != 5 {
		t.Errorf("SkippedCalls = %d, want 5", est.SkippedCalls)
	}
}

func TestEstimateCost_CapBelowIndividual(t *testing.T) {
	routing := makeRouteResult(10, 5)
	est := EstimateCost(routing, 7, DefaultPricing("unknown"))

	if est.IndividualCalls != 7 {
		t.Errorf("IndividualCalls = %d, want 7", est.IndividualCalls)
	}
	if est.GroupedCalls != 0 {
		t.Errorf("GroupedCalls = %d, want 0", est.GroupedCalls)
	}
	if est.SkippedCalls != 8 {
		t.Errorf("SkippedCalls = %d, want 8", est.SkippedCalls)
	}
}

func TestDefaultPricing(t *testing.T) {
	tests := []struct {
		model     string
		wantInput float64
	}{
		{"gemini-3.1-pro-preview", 2.50},
		{"gemini-3.1-flash-lite", 0.02},
		{"gemini-3.1-flash-lite-preview", 0.02},
		{"something-else", 2.50},
	}
	for _, tt := range tests {
		p := DefaultPricing(tt.model)
		if p.InputPerMTok != tt.wantInput {
			t.Errorf("DefaultPricing(%q).InputPerMTok = %f, want %f", tt.model, p.InputPerMTok, tt.wantInput)
		}
	}
}

func TestFormatEstimate(t *testing.T) {
	est := CostEstimate{
		TotalCalls:      20,
		IndividualCalls: 12,
		GroupedCalls:    8,
		EstInputTokens:  96000,
		EstOutputTokens: 30000,
		EstCostUSD:      0.85,
	}
	s := est.FormatEstimate()
	if !strings.Contains(s, "12 individual") {
		t.Errorf("missing individual count in: %s", s)
	}
	if !strings.Contains(s, "8 grouped") {
		t.Errorf("missing grouped count in: %s", s)
	}
	if !strings.Contains(s, "20 calls") {
		t.Errorf("missing total calls in: %s", s)
	}
	if strings.Contains(s, "skipped") {
		t.Error("should not mention skipped when SkippedCalls=0")
	}

	est.SkippedCalls = 5
	s = est.FormatEstimate()
	if !strings.Contains(s, "5 calls skipped by --max-reviews") {
		t.Errorf("missing skipped info in: %s", s)
	}
}

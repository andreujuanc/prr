package audit

import (
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/review"
)

// CostEstimate holds pre-execution cost projections for Phase 3.
type CostEstimate struct {
	TotalCalls      int
	IndividualCalls int
	GroupedCalls    int
	EstInputTokens  int // rough estimate of total input tokens
	EstOutputTokens int // rough estimate of total output tokens
	EstCostUSD      float64
	SkippedCalls    int // calls that would be skipped by --max-reviews
}

// ModelPricing holds per-1M-token costs.
type ModelPricing struct {
	InputPerMTok  float64
	OutputPerMTok float64
	ModelName     string
}

const (
	avgIndividualInputTokens = 4000
	avgGroupedInputTokens    = 6000
	avgOutputTokensPerCall   = 1500
)

// DefaultPricing returns pricing for common models.
func DefaultPricing(modelName string) ModelPricing {
	name := strings.ToLower(modelName)
	switch {
	case strings.Contains(name, "gemini-3.1-pro"):
		return ModelPricing{InputPerMTok: 2.50, OutputPerMTok: 15.00, ModelName: modelName}
	case strings.Contains(name, "gemini-2.5-pro"):
		return ModelPricing{InputPerMTok: 1.25, OutputPerMTok: 10.00, ModelName: modelName}
	case strings.Contains(name, "gemini-3.1-flash-lite"):
		return ModelPricing{InputPerMTok: 0.02, OutputPerMTok: 0.10, ModelName: modelName}
	case strings.Contains(name, "gemini-2.5-flash-lite"):
		return ModelPricing{InputPerMTok: 0.02, OutputPerMTok: 0.10, ModelName: modelName}
	case strings.Contains(name, "gemini-2.5-flash"):
		return ModelPricing{InputPerMTok: 0.15, OutputPerMTok: 3.50, ModelName: modelName}
	default:
		return ModelPricing{InputPerMTok: 2.50, OutputPerMTok: 10.00, ModelName: modelName}
	}
}

// EstimateCost projects Phase 3 costs from routing results.
func EstimateCost(routing *review.RouteResult, maxReviews int, pricing ModelPricing) CostEstimate {
	totalIndividual := len(routing.Individual)
	totalGrouped := len(routing.Grouped)
	totalAll := totalIndividual + totalGrouped

	active := totalAll
	skipped := 0
	if maxReviews > 0 && totalAll > maxReviews {
		skipped = totalAll - maxReviews
		active = maxReviews
	}

	// Count individual vs grouped among active calls (PrioritizedCalls takes individual first)
	activeIndividual := totalIndividual
	activeGrouped := totalGrouped
	if skipped > 0 {
		if active <= totalIndividual {
			activeIndividual = active
			activeGrouped = 0
		} else {
			activeGrouped = active - totalIndividual
		}
	}

	inputTokens := activeIndividual*avgIndividualInputTokens + activeGrouped*avgGroupedInputTokens
	outputTokens := active * avgOutputTokensPerCall

	costUSD := float64(inputTokens)/1_000_000*pricing.InputPerMTok +
		float64(outputTokens)/1_000_000*pricing.OutputPerMTok

	return CostEstimate{
		TotalCalls:      active,
		IndividualCalls: activeIndividual,
		GroupedCalls:    activeGrouped,
		EstInputTokens:  inputTokens,
		EstOutputTokens: outputTokens,
		EstCostUSD:      costUSD,
		SkippedCalls:    skipped,
	}
}

// FormatEstimate returns a human-readable cost summary.
func (e CostEstimate) FormatEstimate() string {
	inputK := fmt.Sprintf("~%dK", (e.EstInputTokens+500)/1000)
	outputK := fmt.Sprintf("~%dK", (e.EstOutputTokens+500)/1000)

	s := fmt.Sprintf("Phase 3: %d individual + %d grouped = %d calls (%s input, %s output tokens, est. ~$%.2f)",
		e.IndividualCalls, e.GroupedCalls, e.TotalCalls, inputK, outputK, e.EstCostUSD)

	if e.SkippedCalls > 0 {
		s += fmt.Sprintf("\n(%d calls skipped by --max-reviews)", e.SkippedCalls)
	}
	return s
}

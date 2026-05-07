package config

import "fmt"

// KnownModel describes a model available for selection in pickers and validation.
type KnownModel struct {
	ID       string // API model ID (e.g. "gemini-2.5-flash")
	Label    string // Human-friendly display label
	Provider string // "gemini", "anthropic", "openai"
	Thinking bool   // Whether the model supports thinking/reasoning
	AOI      bool   // Whether this model is suitable for AOI pre-scanning (cheap/fast)
	Review   bool   // Whether this model is suitable for deep review

	// Pricing per 1M tokens (USD). Zero means unknown/free-tier.
	InputPricePer1M  float64
	OutputPricePer1M float64

	// Speed is a qualitative indicator: "fast", "medium", "slow".
	// Based on benchmark data where available, otherwise estimated.
	Speed string
}

// knownModels is the canonical list of supported models.
// Order matters — it determines picker display order.
var knownModels = []KnownModel{
	// Gemini review models
	{ID: "gemini-3.1-pro-preview", Label: "Gemini 3.1 Pro", Provider: "gemini", Thinking: true, Review: true,
		InputPricePer1M: 2.50, OutputPricePer1M: 15.00, Speed: "slow"},
	{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro", Provider: "gemini", Thinking: true, Review: true,
		InputPricePer1M: 1.25, OutputPricePer1M: 10.00, Speed: "slow"},
	{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash", Provider: "gemini", Thinking: true, Review: true, AOI: true,
		InputPricePer1M: 0.15, OutputPricePer1M: 0.60, Speed: "medium"},

	// Gemini AOI-suitable models (cheap/fast)
	{ID: "gemini-3.1-flash-lite-preview", Label: "Gemini 3.1 Flash Lite", Provider: "gemini", Thinking: true, Review: true, AOI: true,
		InputPricePer1M: 0.02, OutputPricePer1M: 0.10, Speed: "fast"},
	{ID: "gemini-2.5-flash-lite", Label: "Gemini 2.5 Flash Lite", Provider: "gemini", AOI: true,
		InputPricePer1M: 0.02, OutputPricePer1M: 0.10, Speed: "medium"},

	// Anthropic
	{ID: "claude-sonnet-4-20250514", Label: "Claude Sonnet 4", Provider: "anthropic", Thinking: true, Review: true,
		InputPricePer1M: 3.00, OutputPricePer1M: 15.00, Speed: "medium"},
	{ID: "claude-haiku-3-5", Label: "Claude Haiku 3.5", Provider: "anthropic", AOI: true,
		InputPricePer1M: 0.80, OutputPricePer1M: 4.00, Speed: "fast"},

	// OpenAI
	{ID: "gpt-4o", Label: "GPT-4o", Provider: "openai", Review: true,
		InputPricePer1M: 2.50, OutputPricePer1M: 10.00, Speed: "medium"},
	{ID: "gpt-4o-mini", Label: "GPT-4o Mini", Provider: "openai", AOI: true,
		InputPricePer1M: 0.15, OutputPricePer1M: 0.60, Speed: "fast"},
}

// knownModelSet is built at init for fast lookup.
var knownModelSet map[string]KnownModel

func init() {
	knownModelSet = make(map[string]KnownModel, len(knownModels))
	for _, m := range knownModels {
		knownModelSet[m.ID] = m
	}
}

// IsKnownModel returns true if the model ID is in the known models list.
func IsKnownModel(id string) bool {
	_, ok := knownModelSet[id]
	return ok
}

// GetKnownModel returns the KnownModel for an ID, or ok=false if unknown.
func GetKnownModel(id string) (KnownModel, bool) {
	m, ok := knownModelSet[id]
	return m, ok
}

// ReviewModels returns known models suitable for review, filtered by provider.
func ReviewModels(provider string) []KnownModel {
	var result []KnownModel
	for _, m := range knownModels {
		if m.Provider == provider && m.Review {
			result = append(result, m)
		}
	}
	return result
}

// AOIModels returns known models suitable for AOI pre-scanning, filtered by provider.
func AOIModels(provider string) []KnownModel {
	var result []KnownModel
	for _, m := range knownModels {
		if m.Provider == provider && m.AOI {
			result = append(result, m)
		}
	}
	return result
}

// KnownProviders returns the list of supported provider names.
func KnownProviders() []string {
	return []string{"gemini", "anthropic", "openai"}
}

// PriceTag returns a short human-readable price string like "$0.15/1M in".
// Returns "" if pricing is unknown.
func (m KnownModel) PriceTag() string {
	if m.InputPricePer1M == 0 && m.OutputPricePer1M == 0 {
		return ""
	}
	return fmt.Sprintf("$%.2f/$%.2f per 1M tok", m.InputPricePer1M, m.OutputPricePer1M)
}

// SpeedIcon returns a short icon/label for display: "⚡" fast, "●" medium, "◐" slow.
func (m KnownModel) SpeedIcon() string {
	switch m.Speed {
	case "fast":
		return "⚡"
	case "medium":
		return "●"
	case "slow":
		return "◐"
	default:
		return ""
	}
}

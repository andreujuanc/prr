package config

// EstimateCost returns the estimated USD cost for a request given input
// and output token counts. Looks pricing up against the known-models table
// via GetKnownModel. Returns 0 when the model ID is unknown or has no
// pricing recorded (e.g., GitHub Copilot / Claude Code, where the cost is
// borne by a separate subscription).
//
// Pricing is simplified to a single per-1M-token rate per direction;
// Gemini Pro's tier above 200K context is not modelled, so very long
// prompts to Pro will under-count. See known_models.go for the source.
func EstimateCost(modelID string, inputTokens, outputTokens int) float64 {
	m, ok := GetKnownModel(modelID)
	if !ok {
		return 0
	}
	return float64(inputTokens)/1_000_000.0*m.InputPricePer1M +
		float64(outputTokens)/1_000_000.0*m.OutputPricePer1M
}

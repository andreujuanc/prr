package config

// cachedInputDiscount is the fraction of the normal input rate that the
// provider charges for cached input tokens. Gemini and Anthropic both
// price cached reads at 10% of the normal input rate per their public
// pricing pages; OpenAI prices cached reads at 50% (cached input on
// gpt-4o is $1.25/M vs $2.50/M uncached). We use 10% as the default
// because Gemini is the only provider where this codepath currently
// runs explicit-cache requests — the Anthropic path goes through
// Claude Code which handles billing internally. If we add direct
// OpenAI/Anthropic cache plumbing later, this constant should move
// to a per-model field (CachedInputPricePer1M) on KnownModel.
const cachedInputDiscount = 0.10

// EstimateCost returns the estimated USD cost for a request given input
// and output token counts. Looks pricing up against the known-models
// table via GetKnownModel. Returns 0 when the model ID is unknown or has
// no pricing recorded.
//
// Subscription-billed providers (GitHub Copilot, Claude Code) carry
// *shadow* prices — the per-token rate from the equivalent metered API
// (Anthropic direct, OpenAI direct). The user's actual bill is a flat
// subscription, but the shadow rate gives a fair cross-provider cost
// comparison since the same model and the same bytes are involved.
// Copilot entries are still 0 because we don't have a stable per-token
// rate for the bundled GH Copilot models.
//
// Optional cachedInputTokens (variadic; pass one value) breaks the input
// total into two priced segments: (inputTokens - cachedInputTokens) at
// the full rate plus cachedInputTokens at the cached rate (10% of full,
// per cachedInputDiscount). Callers that don't care about caching can
// omit the argument and get the legacy behaviour. Negative or oversized
// cached counts are clamped to [0, inputTokens].
//
// Pricing is simplified to a single per-1M-token rate per direction;
// Gemini Pro's tier above 200K context is not modelled, so very long
// prompts to Pro will under-count. See known_models.go for the source.
func EstimateCost(modelID string, inputTokens, outputTokens int, cachedInputTokens ...int) float64 {
	m, ok := GetKnownModel(modelID)
	if !ok {
		return 0
	}

	cached := 0
	if len(cachedInputTokens) > 0 {
		cached = cachedInputTokens[0]
	}
	if cached < 0 {
		cached = 0
	}
	if cached > inputTokens {
		cached = inputTokens
	}

	uncached := inputTokens - cached
	inputCost := float64(uncached)/1_000_000.0*m.InputPricePer1M +
		float64(cached)/1_000_000.0*m.InputPricePer1M*cachedInputDiscount
	outputCost := float64(outputTokens) / 1_000_000.0 * m.OutputPricePer1M
	return inputCost + outputCost
}

package security

// AOIModelProfile holds tuned settings for a specific model used in the
// AOI security pre-scan. Values are derived from benchmark results in
// aoi_model_comparison_test.go (TestAOIContextLineComparison).
//
// When adding a new model, run the comparison tests first to determine
// optimal settings, then add a profile here.
type AOIModelProfile struct {
	// Model is the model ID (e.g. "gemini-2.5-flash-lite").
	Model string

	// ContextLines controls how many surrounding lines of diff context
	// are passed to the AOI scanner. More context helps trace data flow
	// (source → sink), but too much noise hurts weaker models.
	//
	// Benchmark results (TestAOIContextLineComparison):
	//   gemini-2.5-flash-lite:  U3=6/6 FP:1  vs U10=5/6 FP:4  → U3 wins
	//   gemini-3-flash-preview: U3=6/6 FP:0  vs U10=6/6 FP:1  → U10 fine
	ContextLines int

	// Temperature for generation. Lower = more deterministic.
	Temperature float64

	// ThinkingBudget is the thinking token budget (0 = disabled).
	ThinkingBudget int

	// MaxOutputTokens caps the model's response length.
	MaxOutputTokens int
}

// aoiProfiles maps model IDs to their benchmark-tuned settings.
// Add new entries after running TestAOIModelComparison and
// TestAOIContextLineComparison.
var aoiProfiles = map[string]AOIModelProfile{
	"gemini-2.5-flash-lite": {
		Model:           "gemini-2.5-flash-lite",
		ContextLines:    3,    // U10 hurts recall and increases FP
		Temperature:     0.1,
		ThinkingBudget:  0,
		MaxOutputTokens: 8192,
	},
	"gemini-2.5-flash": {
		Model:           "gemini-2.5-flash",
		ContextLines:    10,
		Temperature:     0.1,
		ThinkingBudget:  0,
		MaxOutputTokens: 8192,
	},
	"gemini-3.1-flash-lite-preview": {
		Model:           "gemini-3.1-flash-lite-preview",
		ContextLines:    3, // small model, same concern as flash-lite
		Temperature:     0.1,
		ThinkingBudget:  0,
		MaxOutputTokens: 8192,
	},
	"gemini-3-flash-preview": {
		Model:           "gemini-3-flash-preview",
		ContextLines:    10,
		Temperature:     0.1,
		ThinkingBudget:  0,
		MaxOutputTokens: 8192,
	},
	"gemini-3.1-pro-preview": {
		Model:           "gemini-3.1-pro-preview",
		ContextLines:    10,
		Temperature:     0.1,
		ThinkingBudget:  0,
		MaxOutputTokens: 8192,
	},
}

// defaultProfile is used when no model-specific profile exists.
var defaultProfile = AOIModelProfile{
	ContextLines:    3, // safe default — extra context can hurt weak models
	Temperature:     0.1,
	ThinkingBudget:  0,
	MaxOutputTokens: 8192,
}

// GetAOIProfile returns the benchmark-tuned profile for the given model.
// Falls back to conservative defaults for unknown models.
func GetAOIProfile(model string) AOIModelProfile {
	if p, ok := aoiProfiles[model]; ok {
		return p
	}
	p := defaultProfile
	p.Model = model
	return p
}

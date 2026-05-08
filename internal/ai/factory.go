package ai

import "fmt"

// ProviderConfig holds the parameters needed to create a Provider.
type ProviderConfig struct {
	ProviderName    string // "gemini", "openai", "github-copilot"
	ModelID         string // e.g. "gemini-3.1-flash-lite"
	APIKey          string
	BaseURL         string // optional endpoint override
	MaxOutputTokens int
	Temperature     float64
	ThinkingBudget  int // 0 = disabled
}

// NewProvider creates a Provider from the given config.
// This is the single place where provider construction happens,
// replacing the duplicated switch statements in createAIClient/createAOIClient.
func NewProvider(cfg ProviderConfig) (Provider, error) {
	switch cfg.ProviderName {
	case "gemini":
		gp := &GeminiProvider{
			APIKey:  cfg.APIKey,
			Model:   cfg.ModelID,
			BaseURL: cfg.BaseURL,
		}
		gp.ModelConfig.MaxOutputTokens = cfg.MaxOutputTokens
		gp.ModelConfig.Temperature = cfg.Temperature
		gp.ModelConfig.ThinkingBudget = cfg.ThinkingBudget
		return gp, nil

	case "openai":
		op := &OpenAIProvider{
			APIKey:  cfg.APIKey,
			Model:   cfg.ModelID,
			BaseURL: cfg.BaseURL,
		}
		op.ModelConfig.MaxOutputTokens = cfg.MaxOutputTokens
		op.ModelConfig.Temperature = cfg.Temperature
		op.ModelConfig.ThinkingBudget = cfg.ThinkingBudget
		return op, nil

	case "github-copilot":
		baseURL := CopilotBaseURL
		if cfg.BaseURL != "" {
			baseURL = cfg.BaseURL
		}
		op := &OpenAIProvider{
			APIKey:        cfg.APIKey,
			Model:         cfg.ModelID,
			BaseURL:       baseURL,
			ProviderLabel: "github-copilot",
			ExtraHeaders: map[string]string{
				"Openai-Intent": "conversation-edits",
				"User-Agent":    "prr",
			},
		}
		op.ModelConfig.MaxOutputTokens = cfg.MaxOutputTokens
		op.ModelConfig.Temperature = cfg.Temperature
		op.ModelConfig.ThinkingBudget = cfg.ThinkingBudget
		return op, nil

	default:
		return nil, fmt.Errorf("unsupported AI provider: %q (supported: gemini, openai, github-copilot)", cfg.ProviderName)
	}
}

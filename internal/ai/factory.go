package ai

import (
	"fmt"
	"time"
)

// DefaultRequestTimeout is the per-HTTP-call deadline applied to every
// LLM request constructed via NewProvider. Sized to be generous for big
// reasoning runs (synthesis on large PRs with thinking-budget models)
// while still bounding a true hang.
//
// Per-call: each provider.StreamChat invocation gets its own fresh
// timer. A multi-round agent loop or a retry wrapper can spend many
// such windows back-to-back; the cap is on individual HTTP calls, not
// on a whole prr review/audit run.
const DefaultRequestTimeout = 15 * time.Minute

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
			APIKey:         cfg.APIKey,
			Model:          cfg.ModelID,
			BaseURL:        cfg.BaseURL,
			RequestTimeout: DefaultRequestTimeout,
		}
		gp.ModelConfig.MaxOutputTokens = cfg.MaxOutputTokens
		gp.ModelConfig.Temperature = cfg.Temperature
		gp.ModelConfig.ThinkingBudget = cfg.ThinkingBudget
		return gp, nil

	case "openai":
		op := &OpenAIProvider{
			APIKey:         cfg.APIKey,
			Model:          cfg.ModelID,
			BaseURL:        cfg.BaseURL,
			RequestTimeout: DefaultRequestTimeout,
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
			APIKey:         cfg.APIKey,
			Model:          cfg.ModelID,
			BaseURL:        baseURL,
			RequestTimeout: DefaultRequestTimeout,
			ProviderLabel:  "github-copilot",
			ExtraHeaders: map[string]string{
				"Openai-Intent": "conversation-edits",
				"User-Agent":    "prr",
			},
		}
		op.ModelConfig.MaxOutputTokens = cfg.MaxOutputTokens
		op.ModelConfig.Temperature = cfg.Temperature
		op.ModelConfig.ThinkingBudget = cfg.ThinkingBudget
		return op, nil

	case "claude-code":
		if !DetectClaudeCode() {
			return nil, fmt.Errorf("claude-code: %q binary not found on PATH — install Claude Code to use this provider", claudeCodeBinaryName)
		}
		return &ClaudeCodeProvider{Model: cfg.ModelID}, nil

	default:
		return nil, fmt.Errorf("unsupported AI provider: %q (supported: gemini, openai, github-copilot, claude-code)", cfg.ProviderName)
	}
}

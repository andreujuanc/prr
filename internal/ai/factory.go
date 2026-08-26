package ai

import (
	"fmt"
	"time"
)

// DefaultRequestTimeout is the per-HTTP-call deadline applied to every
// LLM request constructed via NewProvider. Sized from real Gemini Pro
// traffic: a deep-review-shaped call with 32K thinking budget
// completed in 2.7 minutes wall-clock; the theoretical worst case
// where the model also fills its 65K output budget pushes to ~9
// minutes. 10 minutes is the smallest ceiling that comfortably
// covers both, with the mid-stream silence cap
// (DefaultMaxStreamSilence) handling stuck-but-alive calls within
// 30s so this timer rarely needs to fire.
//
// Per-call: each provider.StreamChat invocation gets its own fresh
// timer. A multi-round agent loop or a retry wrapper can spend many
// such windows back-to-back; the cap is on individual HTTP calls, not
// on a whole prr review/audit run.
const DefaultRequestTimeout = 10 * time.Minute

// DefaultHeartbeatInterval emits a heartbeat event when a stream goes
// silent for this long. Sized for long thinking runs: short enough to
// be a useful "still alive" signal, long enough to avoid noise on
// normal token-by-token output.
const DefaultHeartbeatInterval = 60 * time.Second

// DefaultMaxStreamSilence aborts a streaming request when no SSE data
// has been seen for this long. Sized from real Gemini Pro traffic:
// on a 37KB prompt with a 32K thinking budget, the worst observed
// inter-chunk gap was 4.4s. 30s gives ~7× headroom over healthy
// traffic and fires within seconds of a true hang — vastly tighter
// than waiting for RequestTimeout.
const DefaultMaxStreamSilence = 30 * time.Second

// ProviderConfig holds the parameters needed to create a Provider.
//
// Temperature is *float64 so callers can request explicit greedy
// decoding (0) and leave nil for "use the provider default". Use
// TempPtr to convert a config float64 with the legacy "0 = unset"
// convention.
type ProviderConfig struct {
	ProviderName    string // "gemini", "openai", "github-copilot"
	ModelID         string // e.g. "gemini-3.1-flash-lite"
	APIKey          string
	BaseURL         string // optional endpoint override
	MaxOutputTokens int
	Temperature     *float64
	ThinkingBudget  int // 0 = disabled

	// Effort pins the reasoning-effort level for providers that expose
	// one (currently claude-code's --effort). Empty means "provider
	// default".
	Effort string
}

// NewProvider creates a Provider from the given config.
// This is the single place where provider construction happens,
// replacing the duplicated switch statements in createAIClient/createAOIClient.
func NewProvider(cfg ProviderConfig) (Provider, error) {
	switch cfg.ProviderName {
	case "gemini":
		gp := &GeminiProvider{
			APIKey:            cfg.APIKey,
			Model:             cfg.ModelID,
			BaseURL:           cfg.BaseURL,
			RequestTimeout:    DefaultRequestTimeout,
			HeartbeatInterval: DefaultHeartbeatInterval,
			MaxStreamSilence:  DefaultMaxStreamSilence,
		}
		gp.ModelConfig.MaxOutputTokens = cfg.MaxOutputTokens
		gp.ModelConfig.Temperature = cfg.Temperature
		gp.ModelConfig.ThinkingBudget = cfg.ThinkingBudget
		return gp, nil

	case "openai":
		op := &OpenAIProvider{
			APIKey:            cfg.APIKey,
			Model:             cfg.ModelID,
			BaseURL:           cfg.BaseURL,
			RequestTimeout:    DefaultRequestTimeout,
			HeartbeatInterval: DefaultHeartbeatInterval,
			MaxStreamSilence:  DefaultMaxStreamSilence,
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
			APIKey:            cfg.APIKey,
			Model:             cfg.ModelID,
			BaseURL:           baseURL,
			RequestTimeout:    DefaultRequestTimeout,
			HeartbeatInterval: DefaultHeartbeatInterval,
			MaxStreamSilence:  DefaultMaxStreamSilence,
			ProviderLabel:     "github-copilot",
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
		return &ClaudeCodeProvider{Model: cfg.ModelID, Effort: cfg.Effort}, nil

	case "opencode":
		if !DetectOpenCode() {
			return nil, fmt.Errorf("opencode: %q binary not found on PATH or at %s — install opencode to use this provider", openCodeBinaryName, openCodeStandardInstallPath)
		}
		// opencode requires the full "provider/model-id" form on --model.
		// Callers that omit the prefix get a clear error from OpenCodeProvider.resolveModel.
		return &OpenCodeProvider{Model: cfg.ModelID}, nil

	default:
		return nil, fmt.Errorf("unsupported AI provider: %q (supported: gemini, openai, github-copilot, claude-code, opencode)", cfg.ProviderName)
	}
}

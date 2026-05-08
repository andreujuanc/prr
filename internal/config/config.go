package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andreujuanc/prr/internal/pipe"
)

// ModelRef is a parsed "provider/model-id" reference.
type ModelRef struct {
	Provider string // e.g. "gemini", "openai", "github-copilot"
	ModelID  string // e.g. "gemini-3.1-flash-lite"
}

// String returns the canonical "provider/model-id" format.
func (r ModelRef) String() string {
	return r.Provider + "/" + r.ModelID
}

// ParseModelRef parses a "provider/model-id" string into a ModelRef.
// Returns an error if the format is invalid.
func ParseModelRef(s string) (ModelRef, error) {
	idx := strings.Index(s, "/")
	if idx <= 0 || idx == len(s)-1 {
		return ModelRef{}, fmt.Errorf("invalid model reference %q — expected \"provider/model-id\" format (e.g. \"gemini/gemini-3.1-flash-lite\")", s)
	}
	return ModelRef{
		Provider: s[:idx],
		ModelID:  s[idx+1:],
	}, nil
}

// DefaultStrongModel is the default strong model reference.
// Opus 4.6 via Copilot: 100% recall, 0 FP, 27 tool calls, 202s on deep review benchmark.
const DefaultStrongModel = "github-copilot/claude-opus-4.6"

// DefaultFastModel is the default fast model reference.
// Flash Lite via Gemini: 92.9% AOI recall, 0 FP, 5.1s, $0.0004 per scan.
const DefaultFastModel = "gemini/gemini-3.1-flash-lite"

// Config holds the application configuration.
//
// Models are specified in "provider/model-id" format (e.g. "gemini/gemini-3.1-pro-preview").
// This allows mixing providers — e.g. a Gemini fast model with a GitHub Copilot strong model.
type Config struct {
	// StrongModel is the "provider/model" ref for deep review, re-review, and synthesis phases.
	StrongModel string `json:"strong_model"`
	// FastModel is the "provider/model" ref for discovery and AOI phases.
	FastModel string `json:"fast_model,omitempty"`

	Theme           string                    `json:"theme,omitempty"`            // UI theme ID
	ParallelReviews int                       `json:"parallel_reviews,omitempty"` // number of concurrent batch reviews (default 3)
	Pipes           []pipe.Target             `json:"pipes,omitempty"`            // external process pipe targets
	Providers       map[string]ProviderConfig `json:"providers,omitempty"`        // per-provider configs
	Debug           bool                      `json:"-"`                          // set via --debug flag, not persisted
}

// ProviderConfig holds credentials and settings for a single provider.
type ProviderConfig struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url,omitempty"` // optional endpoint override
}

// ProviderConfigFor returns the ProviderConfig for the given provider name, if any.
func (c *Config) ProviderConfigFor(provider string) ProviderConfig {
	if c.Providers != nil {
		if pc, ok := c.Providers[provider]; ok {
			return pc
		}
	}
	return ProviderConfig{}
}

// APIKeyFor returns the API key for the given provider.
// Returns empty string if no key is configured.
func (c *Config) APIKeyFor(provider string) string {
	if c.Providers != nil {
		if pc, ok := c.Providers[provider]; ok {
			return pc.APIKey
		}
	}
	return ""
}

// DefaultConfigPath returns ~/.config/prr/config.json
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "prr", "config.json"), nil
}

// Load reads the config from the default path.
// Returns a descriptive error if the file is missing or malformed.
func Load() (*Config, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}

// LoadFrom reads and validates config from a specific path.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Create default config file
			if err := createDefault(path); err != nil {
				return nil, fmt.Errorf("failed to create default config: %w", err)
			}
			return nil, fmt.Errorf("config file created at %s\n  Edit it to add your API key, then run prr again", path)
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config JSON in %s: %w", path, err)
	}

	// ── Legacy migration ──────────────────────────────────────────────
	// Check for old-format fields and migrate them to the new format.
	migrated, migrateErr := migrateIfNeeded(data, &cfg)
	if migrateErr != nil {
		return nil, fmt.Errorf("config migration failed: %w", migrateErr)
	}
	if migrated {
		// Persist the migrated config
		if err := SaveTo(&cfg, path); err != nil {
			// Non-fatal: we can still use the in-memory migrated config
			fmt.Fprintf(os.Stderr, "Warning: could not save migrated config: %v\n", err)
		}
	}

	// ── Resolve defaults ──────────────────────────────────────────────
	if cfg.StrongModel == "" {
		cfg.StrongModel = DefaultStrongModel
	}
	if cfg.FastModel == "" {
		cfg.FastModel = DefaultFastModel
	}

	if cfg.ParallelReviews <= 0 {
		cfg.ParallelReviews = 3
	}

	// ── Validate model refs ───────────────────────────────────────────
	strongRef, err := ParseModelRef(cfg.StrongModel)
	if err != nil {
		return nil, fmt.Errorf("config: invalid strong_model: %w", err)
	}
	fastRef, err := ParseModelRef(cfg.FastModel)
	if err != nil {
		return nil, fmt.Errorf("config: invalid fast_model: %w", err)
	}

	// Check API keys exist for each provider used
	if cfg.APIKeyFor(strongRef.Provider) == "" {
		return nil, fmt.Errorf("config: no API key for provider %q (used by strong_model %q). Add it under providers.%s.api_key", strongRef.Provider, cfg.StrongModel, strongRef.Provider)
	}
	if cfg.APIKeyFor(fastRef.Provider) == "" {
		return nil, fmt.Errorf("config: no API key for provider %q (used by fast_model %q). Add it under providers.%s.api_key", fastRef.Provider, cfg.FastModel, fastRef.Provider)
	}

	return &cfg, nil
}

// migrateIfNeeded checks for old-format config fields (provider, model, api_key, etc.)
// and migrates them to the new strong_model/fast_model format.
// Returns true if migration occurred.
func migrateIfNeeded(data []byte, cfg *Config) (bool, error) {
	// Parse raw JSON to check for legacy fields
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, err
	}

	// Check if legacy fields exist
	_, hasProvider := raw["provider"]
	_, hasModel := raw["model"]
	_, hasAPIKey := raw["api_key"]

	if !hasProvider && !hasModel && !hasAPIKey {
		return false, nil // Not a legacy config
	}

	// Already has new-format fields — no migration needed
	_, hasStrong := raw["strong_model"]
	_, hasFast := raw["fast_model"]
	if hasStrong || hasFast {
		return false, nil
	}

	// Parse legacy fields
	var legacy struct {
		Provider  string                    `json:"provider"`
		APIKey    string                    `json:"api_key"`
		Model     string                    `json:"model"`
		AOIModel  string                    `json:"aoi_model"`
		AOIAPIKey string                    `json:"aoi_api_key"`
		Providers map[string]ProviderConfig `json:"providers"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return false, err
	}

	provider := legacy.Provider
	if provider == "" {
		// Try to infer from providers map
		if legacy.Providers != nil {
			for name, pc := range legacy.Providers {
				if pc.APIKey != "" {
					provider = name
					break
				}
			}
		}
	}
	if provider == "" {
		return false, fmt.Errorf("cannot migrate: no provider configured")
	}

	// Migrate providers map: ensure the provider has its API key
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	if legacy.Providers != nil {
		for name, pc := range legacy.Providers {
			cfg.Providers[name] = pc
		}
	}
	// If top-level api_key exists and no providers entry for this provider, add it
	if legacy.APIKey != "" && legacy.APIKey != "YOUR_API_KEY" {
		if _, ok := cfg.Providers[provider]; !ok {
			cfg.Providers[provider] = ProviderConfig{APIKey: legacy.APIKey}
		}
	}

	// Build model refs
	model := legacy.Model
	if model == "" {
		model = defaultModelForProvider(provider)
	}
	cfg.StrongModel = provider + "/" + model

	aoiModel := legacy.AOIModel
	if aoiModel == "" {
		aoiModel = defaultFastModelForProvider(provider)
	}
	aoiProvider := provider
	// If AOI has a separate API key, it might use a different provider
	if legacy.AOIAPIKey != "" {
		// Keep same provider but ensure key is stored
		if _, ok := cfg.Providers[aoiProvider]; !ok || cfg.Providers[aoiProvider].APIKey == "" {
			cfg.Providers[aoiProvider] = ProviderConfig{APIKey: legacy.AOIAPIKey}
		}
	}
	cfg.FastModel = aoiProvider + "/" + aoiModel

	return true, nil
}

// defaultModelForProvider returns the legacy default model ID for a provider (used during migration only).
func defaultModelForProvider(provider string) string {
	switch provider {
	case "gemini":
		return "gemini-3.1-pro-preview"
	case "openai":
		return "gpt-5.4"
	case "github-copilot":
		return "claude-opus-4.6"
	default:
		return ""
	}
}

// defaultFastModelForProvider returns the legacy default fast/AOI model for a provider (used during migration only).
func defaultFastModelForProvider(provider string) string {
	switch provider {
	case "gemini":
		return "gemini-3.1-flash-lite"
	case "openai":
		return "gpt-5.4-mini"
	case "github-copilot":
		return "gpt-4.1"
	default:
		return ""
	}
}

// LoadRaw reads the config from the default path without validation.
// Used by the config wizard to work with incomplete configs.
func LoadRaw() (*Config, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadRawFrom(path)
}

// LoadRawFrom reads config from a specific path without validation.
func LoadRawFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{
				Providers:       make(map[string]ProviderConfig),
				ParallelReviews: 3,
			}, nil
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config JSON in %s: %w", path, err)
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	if cfg.ParallelReviews <= 0 {
		cfg.ParallelReviews = 3
	}

	// Attempt migration for raw loads too (so config wizard sees new fields)
	migrated, _ := migrateIfNeeded(data, &cfg)
	if migrated {
		_ = SaveTo(&cfg, path)
	}

	return &cfg, nil
}

// ConfiguredProviders returns providers that have API keys set.
func (c *Config) ConfiguredProviders() []string {
	var result []string
	if c.Providers != nil {
		for name, pc := range c.Providers {
			if pc.APIKey != "" {
				result = append(result, name)
			}
		}
	}
	return result
}

// createDefault writes a template config file with placeholder values.
func createDefault(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	defaultCfg := map[string]interface{}{
		"strong_model": DefaultStrongModel,
		"fast_model":   DefaultFastModel,
		"providers": map[string]interface{}{
			"gemini": map[string]interface{}{
				"api_key": "YOUR_API_KEY",
			},
		},
	}

	data, err := json.MarshalIndent(defaultCfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, append(data, '\n'), 0600)
}

// Save writes the config back to the default path, preserving any fields
// the user may have set manually. It re-reads the file first so that
// only known fields are overwritten (provider, model, parallel_reviews).
func Save(cfg *Config) error {
	path, err := DefaultConfigPath()
	if err != nil {
		return err
	}
	return SaveTo(cfg, path)
}

// SaveTo saves config to a specific path. Exported for testing.
func SaveTo(cfg *Config, path string) error {
	// Read existing file to preserve unknown/extra fields and formatting
	existing := make(map[string]interface{})
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing)
	}

	// Remove legacy fields if present (migration cleanup)
	delete(existing, "provider")
	delete(existing, "api_key")
	delete(existing, "model")
	delete(existing, "aoi_model")
	delete(existing, "aoi_api_key")

	// Update only the fields we manage
	if cfg.StrongModel != "" {
		existing["strong_model"] = cfg.StrongModel
	}
	if cfg.FastModel != "" {
		existing["fast_model"] = cfg.FastModel
	}
	if cfg.Theme != "" {
		existing["theme"] = cfg.Theme
	}
	if cfg.ParallelReviews > 0 {
		existing["parallel_reviews"] = cfg.ParallelReviews
	}

	// Merge providers: preserve existing providers, add/update from cfg
	if len(cfg.Providers) > 0 {
		existingProviders := make(map[string]interface{})
		if ep, ok := existing["providers"]; ok {
			if m, ok := ep.(map[string]interface{}); ok {
				existingProviders = m
			}
		}
		for name, pc := range cfg.Providers {
			existingProviders[name] = pc
		}
		existing["providers"] = existingProviders
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	return os.WriteFile(path, append(data, '\n'), 0600)
}

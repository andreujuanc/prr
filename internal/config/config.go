package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andreujuanc/prr/internal/pipe"
)

// Config holds the application configuration.
type Config struct {
	Provider        string        `json:"provider"` // "gemini", "anthropic", "openai"
	APIKey          string        `json:"api_key"`
	Model           string        `json:"model"`
	Theme           string        `json:"theme,omitempty"`            // UI theme ID (e.g. "catppuccin-mocha", "dracula")
	ParallelReviews int           `json:"parallel_reviews,omitempty"` // number of concurrent batch reviews (default 3)
	Pipes           []pipe.Target `json:"pipes,omitempty"`            // external process pipe targets
	Debug           bool          `json:"-"`                          // set via --debug flag, not persisted
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

	if cfg.Provider == "" {
		return nil, fmt.Errorf("config: \"provider\" is required (gemini, anthropic, or openai)")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("config: \"api_key\" is required")
	}
	if cfg.Model == "" {
		// Default models per provider
		switch cfg.Provider {
		case "gemini":
			cfg.Model = "gemini-2.5-flash"
		case "anthropic":
			cfg.Model = "claude-sonnet-4-20250514"
		case "openai":
			cfg.Model = "gpt-4o"
		}
	}

	if cfg.ParallelReviews <= 0 {
		cfg.ParallelReviews = 3
	}

	return &cfg, nil
}

// createDefault writes a template config file with placeholder values.
func createDefault(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	defaultCfg := Config{
		Provider: "gemini",
		APIKey:   "YOUR_API_KEY",
		Model:    "gemini-2.5-flash",
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

	// Read existing file to preserve unknown/extra fields and formatting
	existing := make(map[string]interface{})
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing)
	}

	// Update only the fields we manage
	existing["provider"] = cfg.Provider
	existing["api_key"] = cfg.APIKey
	existing["model"] = cfg.Model
	if cfg.Theme != "" {
		existing["theme"] = cfg.Theme
	}
	if cfg.ParallelReviews > 0 {
		existing["parallel_reviews"] = cfg.ParallelReviews
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return os.WriteFile(path, append(data, '\n'), 0600)
}

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds the application configuration.
type Config struct {
	Provider        string `json:"provider"`                   // "gemini", "anthropic", "openai"
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	ParallelReviews int    `json:"parallel_reviews,omitempty"` // number of concurrent batch reviews (default 3)
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
			cfg.Model = "gemini-2.5-pro"
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
		Model:    "gemini-2.5-pro",
	}

	data, err := json.MarshalIndent(defaultCfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, append(data, '\n'), 0600)
}

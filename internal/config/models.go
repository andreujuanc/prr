package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
)

//go:embed models.json
var defaultModelsJSON []byte

// ModelConfig holds per-model tuning parameters.
type ModelConfig struct {
	MaxOutputTokens int             `json:"max_output_tokens"`
	Temperature     float64         `json:"temperature"`
	ThinkingBudget  ThinkingBudgets `json:"thinking_budget"`             // per-mode thinking budgets
	AOIContextLines int             `json:"aoi_context_lines,omitempty"` // diff context lines for AOI scanning (0 = default 3)
}

// ThinkingBudgets holds per-mode thinking token budgets.
// Zero means thinking is disabled for that mode.
type ThinkingBudgets struct {
	Review int `json:"review"` // deep review, re-review, synthesis
	Chat   int `json:"chat"`   // interactive TUI chat
	Fast   int `json:"fast"`   // discovery, AOI pre-scan
}

// LoadModels returns model configs by merging embedded defaults with
// user overrides from ~/.config/prr/models.json. If the user file
// doesn't exist, it is created with the embedded defaults.
func LoadModels() (map[string]ModelConfig, error) {
	// Parse embedded defaults
	defaults := make(map[string]ModelConfig)
	if err := json.Unmarshal(defaultModelsJSON, &defaults); err != nil {
		return nil, fmt.Errorf("config: failed to parse embedded models.json: %w", err)
	}

	// Locate user override file
	userPath, err := modelsConfigPath()
	if err != nil {
		return defaults, nil // can't determine path, use defaults
	}

	data, err := os.ReadFile(userPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create the file from defaults so the user can customize it
			if writeErr := writeModelsFile(userPath, defaults); writeErr != nil {
				log.Printf("Warning: could not write default models.json: %v", writeErr)
			}
			return defaults, nil
		}
		return defaults, nil // unreadable file, use defaults
	}

	// Parse user overrides and merge — user values win
	var overrides map[string]ModelConfig
	if err := json.Unmarshal(data, &overrides); err != nil {
		log.Printf("Warning: invalid models.json at %s, using defaults: %v", userPath, err)
		return defaults, nil
	}

	maps.Copy(defaults, overrides)

	return defaults, nil
}

// GetModelConfig returns the config for a specific model.
// If no exact match, returns a sensible fallback.
func GetModelConfig(models map[string]ModelConfig, modelID string) ModelConfig {
	if cfg, ok := models[modelID]; ok {
		return cfg
	}
	// Fallback: conservative defaults for unknown models
	return ModelConfig{
		MaxOutputTokens: 8192,
		Temperature:     0.2,
	}
}

// ResolvedAOIContextLines returns the AOI context lines for a model,
// defaulting to 3 if not configured.
func (mc ModelConfig) ResolvedAOIContextLines() int {
	if mc.AOIContextLines > 0 {
		return mc.AOIContextLines
	}
	return 3
}

func modelsConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "prr", "models.json"), nil
}

func writeModelsFile(path string, models map[string]ModelConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(models, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

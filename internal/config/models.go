package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

//go:embed models.json
var defaultModelsJSON []byte

// ModelConfig holds per-model tuning parameters.
type ModelConfig struct {
	MaxOutputTokens int     `json:"max_output_tokens"`
	Temperature     float64 `json:"temperature"`
	ThinkingBudget  int     `json:"thinking_budget"` // 0 = thinking disabled for this model
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

	for model, cfg := range overrides {
		defaults[model] = cfg
	}

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
		ThinkingBudget:  0,
	}
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

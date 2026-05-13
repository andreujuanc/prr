package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadModels_EmbeddedDefaults(t *testing.T) {
	models, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error: %v", err)
	}

	// Verify known models from the embedded defaults
	tests := []struct {
		model           string
		maxOutputTokens int
		reviewBudget    int
		chatBudget      int
		fastBudget      int
	}{
		{"gemini-3.1-pro-preview", 65536, 32768, 2048, 1024},
		{"gemini-3.1-flash-lite", 65536, 8192, 2048, 2048},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			cfg, ok := models[tt.model]
			if !ok {
				t.Fatalf("model %q not found in defaults", tt.model)
			}
			if cfg.MaxOutputTokens != tt.maxOutputTokens {
				t.Errorf("MaxOutputTokens = %d, want %d", cfg.MaxOutputTokens, tt.maxOutputTokens)
			}
			if cfg.ThinkingBudget.Review != tt.reviewBudget {
				t.Errorf("ThinkingBudget.Review = %d, want %d", cfg.ThinkingBudget.Review, tt.reviewBudget)
			}
			if cfg.ThinkingBudget.Chat != tt.chatBudget {
				t.Errorf("ThinkingBudget.Chat = %d, want %d", cfg.ThinkingBudget.Chat, tt.chatBudget)
			}
			if cfg.ThinkingBudget.Fast != tt.fastBudget {
				t.Errorf("ThinkingBudget.Fast = %d, want %d", cfg.ThinkingBudget.Fast, tt.fastBudget)
			}
			if cfg.Temperature != 0.1 {
				t.Errorf("Temperature = %f, want 0.1", cfg.Temperature)
			}
		})
	}
}

func TestGetModelConfig_Known(t *testing.T) {
	models, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error: %v", err)
	}

	cfg := GetModelConfig(models, "gemini-3.1-flash-lite")
	if cfg.MaxOutputTokens != 65536 {
		t.Errorf("MaxOutputTokens = %d, want 65536", cfg.MaxOutputTokens)
	}
	if cfg.ThinkingBudget.Review != 8192 {
		t.Errorf("ThinkingBudget.Review = %d, want 8192", cfg.ThinkingBudget.Review)
	}
}

func TestGetModelConfig_Unknown(t *testing.T) {
	models, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error: %v", err)
	}

	cfg := GetModelConfig(models, "unknown-model-xyz")
	if cfg.MaxOutputTokens != 8192 {
		t.Errorf("fallback MaxOutputTokens = %d, want 8192", cfg.MaxOutputTokens)
	}
	if cfg.ThinkingBudget.Review != 0 {
		t.Errorf("fallback ThinkingBudget.Review = %d, want 0", cfg.ThinkingBudget.Review)
	}
	if cfg.Temperature != 0.2 {
		t.Errorf("fallback Temperature = %f, want 0.2", cfg.Temperature)
	}
}

func TestLoadModels_UserOverrideMerges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Write a user override file with a custom model and an override for an existing one
	dir := filepath.Join(home, ".config", "prr")
	os.MkdirAll(dir, 0755)

	overrides := map[string]ModelConfig{
		"my-custom-model":       {MaxOutputTokens: 1024, Temperature: 0.5, ThinkingBudget: ThinkingBudgets{}},
		"gemini-3.1-flash-lite": {MaxOutputTokens: 99999, Temperature: 0.9, ThinkingBudget: ThinkingBudgets{}},
	}
	data, _ := json.MarshalIndent(overrides, "", "  ")
	os.WriteFile(filepath.Join(dir, "models.json"), data, 0644)

	models, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error: %v", err)
	}

	// User's custom model should be present
	custom, ok := models["my-custom-model"]
	if !ok {
		t.Fatal("custom model not found after merge")
	}
	if custom.MaxOutputTokens != 1024 {
		t.Errorf("custom MaxOutputTokens = %d, want 1024", custom.MaxOutputTokens)
	}

	// User override should win over embedded default
	flash := models["gemini-3.1-flash-lite"]
	if flash.MaxOutputTokens != 99999 {
		t.Errorf("overridden MaxOutputTokens = %d, want 99999", flash.MaxOutputTokens)
	}

	// Non-overridden embedded models should still be present
	if _, ok := models["gemini-3.1-pro-preview"]; !ok {
		t.Error("embedded model gemini-3.1-pro-preview should still exist")
	}
}

func TestLoadModels_CorruptUserFileFallsBackToDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".config", "prr")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte("{corrupt json"), 0644)

	models, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error: %v", err)
	}

	// Should fall back to embedded defaults
	if _, ok := models["gemini-3.1-flash-lite"]; !ok {
		t.Error("expected embedded defaults when user file is corrupt")
	}
}

func TestLoadModels_CreatesUserFileWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	models, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error: %v", err)
	}

	// Should return embedded defaults
	if _, ok := models["gemini-3.1-flash-lite"]; !ok {
		t.Error("expected embedded defaults")
	}

	// File should have been created
	path := filepath.Join(home, ".config", "prr", "models.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected models.json to be created")
	}
}

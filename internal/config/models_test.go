package config

import (
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
		thinkingBudget  int
	}{
		{"gemini-3.1-pro-preview", 65536, 16384},
		{"gemini-3.1-flash-lite-preview", 65536, 8192},
		{"gemini-2.5-flash", 65536, 8192},
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
			if cfg.ThinkingBudget != tt.thinkingBudget {
				t.Errorf("ThinkingBudget = %d, want %d", cfg.ThinkingBudget, tt.thinkingBudget)
			}
			if cfg.Temperature != 0.2 {
				t.Errorf("Temperature = %f, want 0.2", cfg.Temperature)
			}
		})
	}
}

func TestGetModelConfig_Known(t *testing.T) {
	models, err := LoadModels()
	if err != nil {
		t.Fatalf("LoadModels() error: %v", err)
	}

	cfg := GetModelConfig(models, "gemini-2.5-flash")
	if cfg.MaxOutputTokens != 65536 {
		t.Errorf("MaxOutputTokens = %d, want 65536", cfg.MaxOutputTokens)
	}
	if cfg.ThinkingBudget != 8192 {
		t.Errorf("ThinkingBudget = %d, want 8192", cfg.ThinkingBudget)
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
	if cfg.ThinkingBudget != 0 {
		t.Errorf("fallback ThinkingBudget = %d, want 0", cfg.ThinkingBudget)
	}
	if cfg.Temperature != 0.2 {
		t.Errorf("fallback Temperature = %f, want 0.2", cfg.Temperature)
	}
}

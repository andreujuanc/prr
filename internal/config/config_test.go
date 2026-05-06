package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setTestHome overrides $HOME so DefaultConfigPath() and friends
// resolve to a temp directory. Returns the fake home path.
func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// writeTestConfig writes a JSON config file under the fake home.
func writeTestConfig(t *testing.T, home string, data interface{}) string {
	t.Helper()
	dir := filepath.Join(home, ".config", "prr")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ── DefaultConfigPath ───────────────────────────────────────────────────

func TestDefaultConfigPath(t *testing.T) {
	home := setTestHome(t)
	path, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath() error: %v", err)
	}
	expected := filepath.Join(home, ".config", "prr", "config.json")
	if path != expected {
		t.Errorf("path = %q, want %q", path, expected)
	}
}

// ── Load ────────────────────────────────────────────────────────────────

func TestLoad_CreatesDefaultWhenMissing(t *testing.T) {
	home := setTestHome(t)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error instructing user to edit config")
	}
	if !strings.Contains(err.Error(), "config file created") {
		t.Errorf("expected 'config file created' message, got: %v", err)
	}

	// Verify the default file was actually written
	path := filepath.Join(home, ".config", "prr", "config.json")
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("default config not created: %v", readErr)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("default config is not valid JSON: %v", err)
	}
	if cfg.Provider != "gemini" {
		t.Errorf("default provider = %q, want %q", cfg.Provider, "gemini")
	}
	if cfg.APIKey != "YOUR_API_KEY" {
		t.Errorf("default api_key = %q, want %q", cfg.APIKey, "YOUR_API_KEY")
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"provider": "anthropic",
		"api_key":  "sk-test-key",
		"model":    "claude-sonnet-4-20250514",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "anthropic")
	}
	if cfg.APIKey != "sk-test-key" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "sk-test-key")
	}
	if cfg.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-sonnet-4-20250514")
	}
	// ParallelReviews should default to 3
	if cfg.ParallelReviews != 3 {
		t.Errorf("ParallelReviews = %d, want 3", cfg.ParallelReviews)
	}
}

func TestLoad_DefaultModelPerProvider(t *testing.T) {
	tests := []struct {
		provider      string
		expectedModel string
	}{
		{"gemini", "gemini-2.5-flash"},
		{"anthropic", "claude-sonnet-4-20250514"},
		{"openai", "gpt-4o"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			home := setTestHome(t)
			writeTestConfig(t, home, map[string]interface{}{
				"provider": tt.provider,
				"api_key":  "test-key",
				// no model specified
			})

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if cfg.Model != tt.expectedModel {
				t.Errorf("Model = %q, want %q", cfg.Model, tt.expectedModel)
			}
		})
	}
}

func TestLoad_MissingProvider(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"api_key": "test-key",
	})

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("expected error about provider, got: %v", err)
	}
}

func TestLoad_MissingAPIKey(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"provider": "gemini",
	})

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("expected error about api_key, got: %v", err)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	home := setTestHome(t)
	dir := filepath.Join(home, ".config", "prr")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("{bad json"), 0600)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid config JSON") {
		t.Errorf("expected 'invalid config JSON' message, got: %v", err)
	}
}

func TestLoad_ParallelReviewsPreserved(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"provider":         "gemini",
		"api_key":          "key",
		"parallel_reviews": 5,
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ParallelReviews != 5 {
		t.Errorf("ParallelReviews = %d, want 5", cfg.ParallelReviews)
	}
}

// ── Save ────────────────────────────────────────────────────────────────

func TestSave_RoundTrip(t *testing.T) {
	home := setTestHome(t)
	// Write initial config
	writeTestConfig(t, home, map[string]interface{}{
		"provider": "gemini",
		"api_key":  "old-key",
	})

	// Save updated config
	err := Save(&Config{
		Provider:        "anthropic",
		APIKey:          "new-key",
		Model:           "claude-sonnet-4-20250514",
		ParallelReviews: 5,
	})
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Load it back
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() after Save() error: %v", err)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "anthropic")
	}
	if cfg.APIKey != "new-key" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "new-key")
	}
	if cfg.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-sonnet-4-20250514")
	}
	if cfg.ParallelReviews != 5 {
		t.Errorf("ParallelReviews = %d, want 5", cfg.ParallelReviews)
	}
}

func TestSave_PreservesUnknownFields(t *testing.T) {
	home := setTestHome(t)
	// Write config with an extra field that Config struct doesn't know about
	writeTestConfig(t, home, map[string]interface{}{
		"provider":     "gemini",
		"api_key":      "key",
		"custom_field": "should survive",
	})

	err := Save(&Config{
		Provider: "gemini",
		APIKey:   "key",
		Model:    "gemini-2.5-flash",
	})
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Read raw JSON and check the custom field survived
	path := filepath.Join(home, ".config", "prr", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	if raw["custom_field"] != "should survive" {
		t.Errorf("custom_field lost after Save, got: %v", raw["custom_field"])
	}
}

func TestSave_Theme(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"provider": "gemini",
		"api_key":  "key",
	})

	err := Save(&Config{
		Provider: "gemini",
		APIKey:   "key",
		Model:    "gemini-2.5-flash",
		Theme:    "dracula",
	})
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	path := filepath.Join(home, ".config", "prr", "config.json")
	data, _ := os.ReadFile(path)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	if raw["theme"] != "dracula" {
		t.Errorf("theme = %v, want %q", raw["theme"], "dracula")
	}
}

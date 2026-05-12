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

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("default config is not valid JSON: %v", err)
	}
	if raw["strong_model"] != DefaultStrongModel {
		t.Errorf("default strong_model = %v, want %q", raw["strong_model"], DefaultStrongModel)
	}
	if raw["fast_model"] != DefaultFastModel {
		t.Errorf("default fast_model = %v, want %q", raw["fast_model"], DefaultFastModel)
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"strong_model": "github-copilot/claude-sonnet-4-6",
		"fast_model":   "gemini/gemini-2.5-flash-lite",
		"providers": map[string]interface{}{
			"github-copilot": map[string]interface{}{"api_key": "ghp-test"},
			"gemini":         map[string]interface{}{"api_key": "AIza-test"},
		},
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.StrongModel != "github-copilot/claude-sonnet-4-6" {
		t.Errorf("StrongModel = %q, want %q", cfg.StrongModel, "github-copilot/claude-sonnet-4-6")
	}
	if cfg.FastModel != "gemini/gemini-2.5-flash-lite" {
		t.Errorf("FastModel = %q, want %q", cfg.FastModel, "gemini/gemini-2.5-flash-lite")
	}
	if cfg.ParallelReviews != 3 {
		t.Errorf("ParallelReviews = %d, want 3", cfg.ParallelReviews)
	}
}

func TestLoad_DefaultsApplied(t *testing.T) {
	home := setTestHome(t)
	// No strong_model or fast_model — should use defaults
	writeTestConfig(t, home, map[string]interface{}{
		"providers": map[string]interface{}{
			"gemini":         map[string]interface{}{"api_key": "test-key"},
			"github-copilot": map[string]interface{}{"api_key": "test-copilot-key"},
		},
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.StrongModel != DefaultStrongModel {
		t.Errorf("StrongModel = %q, want %q", cfg.StrongModel, DefaultStrongModel)
	}
	if cfg.FastModel != DefaultFastModel {
		t.Errorf("FastModel = %q, want %q", cfg.FastModel, DefaultFastModel)
	}
}

func TestLoad_InvalidModelRef(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"strong_model": "no-slash-here",
		"providers": map[string]interface{}{
			"gemini": map[string]interface{}{"api_key": "key"},
		},
	})

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid model ref")
	}
	if !strings.Contains(err.Error(), "provider/model-id") {
		t.Errorf("expected format hint in error, got: %v", err)
	}
}

func TestLoad_MissingAPIKey(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"strong_model": "gemini/gemini-2.5-flash",
		"providers":    map[string]interface{}{},
	})

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
	if !strings.Contains(err.Error(), "no API key") {
		t.Errorf("expected error about API key, got: %v", err)
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
		"strong_model":     "gemini/gemini-2.5-flash",
		"parallel_reviews": 5,
		"providers": map[string]interface{}{
			"gemini": map[string]interface{}{"api_key": "key"},
		},
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ParallelReviews != 5 {
		t.Errorf("ParallelReviews = %d, want 5", cfg.ParallelReviews)
	}
}

// ── Legacy Migration ────────────────────────────────────────────────────

func TestLoad_MigratesLegacyConfig(t *testing.T) {
	home := setTestHome(t)
	// Write a legacy-format config
	writeTestConfig(t, home, map[string]interface{}{
		"provider":  "gemini",
		"api_key":   "AIzaSy_test",
		"model":     "gemini-2.5-pro",
		"aoi_model": "gemini-2.5-flash-lite",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.StrongModel != "gemini/gemini-2.5-pro" {
		t.Errorf("StrongModel = %q, want %q", cfg.StrongModel, "gemini/gemini-2.5-pro")
	}
	if cfg.FastModel != "gemini/gemini-2.5-flash-lite" {
		t.Errorf("FastModel = %q, want %q", cfg.FastModel, "gemini/gemini-2.5-flash-lite")
	}

	// Verify the migrated config was persisted
	path := filepath.Join(home, ".config", "prr", "config.json")
	data, _ := os.ReadFile(path)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	// Legacy fields should be removed
	if _, ok := raw["provider"]; ok {
		t.Error("legacy 'provider' field should have been removed")
	}
	if _, ok := raw["api_key"]; ok {
		t.Error("legacy 'api_key' field should have been removed")
	}
	if _, ok := raw["model"]; ok {
		t.Error("legacy 'model' field should have been removed")
	}
}

func TestLoad_MigratesLegacyWithProvidersMap(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"provider": "github-copilot",
		"model":    "claude-sonnet-4-6",
		"providers": map[string]interface{}{
			"github-copilot": map[string]interface{}{"api_key": "ghp-test"},
		},
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.StrongModel != "github-copilot/claude-sonnet-4-6" {
		t.Errorf("StrongModel = %q, want %q", cfg.StrongModel, "github-copilot/claude-sonnet-4-6")
	}
}

func TestLoad_NoMigrationIfNewFormat(t *testing.T) {
	home := setTestHome(t)
	// Config has both old and new fields — new fields should take precedence
	writeTestConfig(t, home, map[string]interface{}{
		"strong_model": "openai/gpt-5.4",
		"provider":     "gemini",
		"model":        "gemini-2.5-flash",
		"providers": map[string]interface{}{
			"openai": map[string]interface{}{"api_key": "sk-test"},
			"gemini": map[string]interface{}{"api_key": "AIza-test"},
		},
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// Should use new-format field, not legacy
	if cfg.StrongModel != "openai/gpt-5.4" {
		t.Errorf("StrongModel = %q, want %q (should not migrate when new fields exist)", cfg.StrongModel, "openai/gpt-5.4")
	}
}

// ── Save ────────────────────────────────────────────────────────────────

func TestSave_RoundTrip(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"strong_model": "gemini/gemini-2.5-flash",
		"providers": map[string]interface{}{
			"gemini":         map[string]interface{}{"api_key": "old-key"},
			"github-copilot": map[string]interface{}{"api_key": "ghp-key"},
		},
	})

	err := Save(&Config{
		StrongModel:     "github-copilot/claude-sonnet-4-6",
		FastModel:       "gemini/gemini-2.5-flash-lite",
		ParallelReviews: 5,
		Providers: map[string]ProviderConfig{
			"github-copilot": {APIKey: "ghp-key"},
			"gemini":         {APIKey: "old-key"},
		},
	})
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() after Save() error: %v", err)
	}
	if cfg.StrongModel != "github-copilot/claude-sonnet-4-6" {
		t.Errorf("StrongModel = %q, want %q", cfg.StrongModel, "github-copilot/claude-sonnet-4-6")
	}
	if cfg.FastModel != "gemini/gemini-2.5-flash-lite" {
		t.Errorf("FastModel = %q, want %q", cfg.FastModel, "gemini/gemini-2.5-flash-lite")
	}
	if cfg.ParallelReviews != 5 {
		t.Errorf("ParallelReviews = %d, want 5", cfg.ParallelReviews)
	}
}

func TestSave_PreservesUnknownFields(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"strong_model": "gemini/gemini-2.5-flash",
		"custom_field": "should survive",
		"providers": map[string]interface{}{
			"gemini": map[string]interface{}{"api_key": "key"},
		},
	})

	err := Save(&Config{
		StrongModel: "gemini/gemini-2.5-flash",
		Providers: map[string]ProviderConfig{
			"gemini": {APIKey: "key"},
		},
	})
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

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
		"strong_model": "gemini/gemini-2.5-flash",
		"providers": map[string]interface{}{
			"gemini": map[string]interface{}{"api_key": "key"},
		},
	})

	err := Save(&Config{
		StrongModel: "gemini/gemini-2.5-flash",
		Theme:       "dracula",
		Providers: map[string]ProviderConfig{
			"gemini": {APIKey: "key"},
		},
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

func TestSaveTo_PersistsProviders(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.json")
	initial := map[string]interface{}{
		"strong_model": "gemini/gemini-2.5-flash",
		"providers": map[string]interface{}{
			"gemini": map[string]interface{}{"api_key": "AIza-test"},
		},
	}
	b, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(tmpFile, b, 0600)

	cfg := &Config{
		StrongModel: "github-copilot/claude-sonnet-4-6",
		Providers: map[string]ProviderConfig{
			"github-copilot": {APIKey: "github_pat_abc123"},
		},
	}

	if err := SaveTo(cfg, tmpFile); err != nil {
		t.Fatalf("SaveTo() error: %v", err)
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if raw["strong_model"] != "github-copilot/claude-sonnet-4-6" {
		t.Errorf("strong_model = %v, want %q", raw["strong_model"], "github-copilot/claude-sonnet-4-6")
	}

	providers, ok := raw["providers"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers not saved in config file; raw keys: %v", raw)
	}

	ghCopilot, ok := providers["github-copilot"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers.github-copilot missing; providers = %v", providers)
	}

	if ghCopilot["api_key"] != "github_pat_abc123" {
		t.Errorf("providers.github-copilot.api_key = %v, want %q", ghCopilot["api_key"], "github_pat_abc123")
	}
}

func TestSaveTo_MergesProviders(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.json")
	initial := map[string]interface{}{
		"strong_model": "gemini/gemini-2.5-flash",
		"providers": map[string]interface{}{
			"gemini": map[string]interface{}{"api_key": "AIzaSy_test"},
		},
	}
	b, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(tmpFile, b, 0600)

	cfg := &Config{
		StrongModel: "openai/gpt-5.4",
		Providers: map[string]ProviderConfig{
			"openai": {APIKey: "sk-new"},
		},
	}
	if err := SaveTo(cfg, tmpFile); err != nil {
		t.Fatalf("SaveTo() error: %v", err)
	}

	data, _ := os.ReadFile(tmpFile)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	providers, ok := raw["providers"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers missing from saved config")
	}

	// gemini should still be there
	gemini, ok := providers["gemini"].(map[string]interface{})
	if !ok {
		t.Fatalf("gemini provider was wiped! providers = %v", providers)
	}
	if gemini["api_key"] != "AIzaSy_test" {
		t.Errorf("gemini api_key = %v, want AIzaSy_test", gemini["api_key"])
	}

	// openai should be added
	openai, ok := providers["openai"].(map[string]interface{})
	if !ok {
		t.Fatalf("openai provider not added! providers = %v", providers)
	}
	if openai["api_key"] != "sk-new" {
		t.Errorf("openai api_key = %v, want sk-new", openai["api_key"])
	}
}

// ── ParseModelRef ───────────────────────────────────────────────────────

func TestParseModelRef_Valid(t *testing.T) {
	tests := []struct {
		input    string
		provider string
		modelID  string
	}{
		{"gemini/gemini-2.5-flash", "gemini", "gemini-2.5-flash"},
		{"openai/gpt-5.4", "openai", "gpt-5.4"},
		{"github-copilot/claude-sonnet-4-6", "github-copilot", "claude-sonnet-4-6"},
	}
	for _, tt := range tests {
		ref, err := ParseModelRef(tt.input)
		if err != nil {
			t.Errorf("ParseModelRef(%q) error: %v", tt.input, err)
			continue
		}
		if ref.Provider != tt.provider {
			t.Errorf("ParseModelRef(%q).Provider = %q, want %q", tt.input, ref.Provider, tt.provider)
		}
		if ref.ModelID != tt.modelID {
			t.Errorf("ParseModelRef(%q).ModelID = %q, want %q", tt.input, ref.ModelID, tt.modelID)
		}
	}
}

func TestParseModelRef_Invalid(t *testing.T) {
	tests := []string{
		"no-slash",
		"/no-provider",
		"no-model/",
		"",
	}
	for _, input := range tests {
		_, err := ParseModelRef(input)
		if err == nil {
			t.Errorf("ParseModelRef(%q) expected error, got nil", input)
		}
	}
}

func TestModelRef_String(t *testing.T) {
	ref := ModelRef{Provider: "gemini", ModelID: "gemini-2.5-flash"}
	if got := ref.String(); got != "gemini/gemini-2.5-flash" {
		t.Errorf("String() = %q, want %q", got, "gemini/gemini-2.5-flash")
	}
}

// ── ConfiguredProviders ─────────────────────────────────────────────────

func TestConfiguredProviders_Empty(t *testing.T) {
	cfg := &Config{}
	if got := cfg.ConfiguredProviders(); len(got) != 0 {
		t.Errorf("ConfiguredProviders() = %v, want empty", got)
	}
}

func TestConfiguredProviders_FromProvidersMap(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"gemini": {APIKey: "key1"},
			"openai": {APIKey: "key2"},
		},
	}
	got := cfg.ConfiguredProviders()
	if len(got) != 2 {
		t.Fatalf("ConfiguredProviders() len = %d, want 2", len(got))
	}
}

func TestConfiguredProviders_SkipsEmptyKey(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"gemini": {APIKey: ""},
			"openai": {APIKey: "key"},
		},
	}
	got := cfg.ConfiguredProviders()
	if len(got) != 1 || got[0] != "openai" {
		t.Errorf("ConfiguredProviders() = %v, want [openai]", got)
	}
}

// ── LoadRaw ─────────────────────────────────────────────────────────────

func TestLoadRaw_NonexistentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	cfg, err := LoadRawFrom(path)
	if err != nil {
		t.Fatalf("LoadRawFrom() error: %v", err)
	}
	if cfg.Providers == nil {
		t.Error("expected non-nil Providers map")
	}
	if cfg.ParallelReviews != 3 {
		t.Errorf("ParallelReviews = %d, want 3", cfg.ParallelReviews)
	}
}

func TestLoadRaw_IncompleteConfig(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"strong_model": "gemini/gemini-2.5-flash"}`)
	os.WriteFile(tmpFile, data, 0600)

	cfg, err := LoadRawFrom(tmpFile)
	if err != nil {
		t.Fatalf("LoadRawFrom() error: %v", err)
	}
	if cfg.StrongModel != "gemini/gemini-2.5-flash" {
		t.Errorf("StrongModel = %q, want %q", cfg.StrongModel, "gemini/gemini-2.5-flash")
	}
	if cfg.Providers == nil {
		t.Error("expected non-nil Providers map")
	}
}

func TestLoadRaw_InvalidJSON(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(tmpFile, []byte("{bad"), 0600)

	_, err := LoadRawFrom(tmpFile)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ── APIKeyFor ───────────────────────────────────────────────────────────

func TestAPIKeyFor(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"gemini": {APIKey: "gemini-key"},
			"openai": {APIKey: "openai-key"},
		},
	}
	if got := cfg.APIKeyFor("gemini"); got != "gemini-key" {
		t.Errorf("APIKeyFor(gemini) = %q, want %q", got, "gemini-key")
	}
	if got := cfg.APIKeyFor("openai"); got != "openai-key" {
		t.Errorf("APIKeyFor(openai) = %q, want %q", got, "openai-key")
	}
	if got := cfg.APIKeyFor("missing"); got != "" {
		t.Errorf("APIKeyFor(missing) = %q, want empty", got)
	}
}

// ── Mixed provider config ──────────────────────────────────────────────

func TestLoad_MixedProviders(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"strong_model": "openai/gpt-5.4",
		"fast_model":   "gemini/gemini-2.5-flash-lite",
		"providers": map[string]interface{}{
			"openai": map[string]interface{}{"api_key": "sk-test"},
			"gemini": map[string]interface{}{"api_key": "AIza-test"},
		},
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.StrongModel != "openai/gpt-5.4" {
		t.Errorf("StrongModel = %q, want %q", cfg.StrongModel, "openai/gpt-5.4")
	}
	if cfg.FastModel != "gemini/gemini-2.5-flash-lite" {
		t.Errorf("FastModel = %q, want %q", cfg.FastModel, "gemini/gemini-2.5-flash-lite")
	}
}

func TestLoad_MissingProviderKey_ForFastModel(t *testing.T) {
	home := setTestHome(t)
	writeTestConfig(t, home, map[string]interface{}{
		"strong_model": "gemini/gemini-2.5-flash",
		"fast_model":   "openai/gpt-5.4-mini",
		"providers": map[string]interface{}{
			"gemini": map[string]interface{}{"api_key": "key"},
			// openai key missing
		},
	})

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing openai API key")
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Errorf("expected error about openai, got: %v", err)
	}
}

// TestProviderConfig_UseCLI_RoundTrip pins the wizard's claude-code
// placeholder contract: a ProviderConfig{UseCLI: true} survives Save →
// Load intact, the saved JSON contains "use_cli": true, and it does NOT
// contain an empty api_key (the json:"api_key,omitempty" tag ensures
// the field is omitted when empty so we don't ship misleading blanks).
//
// Why this matters (Rule 9): the marker exists so a user inspecting
// config.json can see the wizard's claude-code selection. A regression
// that strips the field on save or accidentally re-adds an empty key
// would break that visibility — silently. This test fails loudly.
func TestProviderConfig_UseCLI_RoundTrip(t *testing.T) {
	home := setTestHome(t)
	path := filepath.Join(home, ".config", "prr", "config.json")

	cfg := &Config{
		StrongModel: "claude-code/claude-opus-4-7",
		FastModel:   "claude-code/claude-haiku-4-5",
		Providers: map[string]ProviderConfig{
			"claude-code": {UseCLI: true},
		},
	}
	if err := SaveTo(cfg, path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	// Inspect the raw JSON bytes — both presence of use_cli and absence
	// of an empty api_key matter.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(raw), `"use_cli": true`) {
		t.Errorf("saved config missing \"use_cli\": true; got:\n%s", string(raw))
	}
	if strings.Contains(string(raw), `"api_key": ""`) {
		t.Errorf("saved config contains empty api_key (should be omitted via omitempty); got:\n%s", string(raw))
	}

	// Load and verify the field deserializes back. Bypass the full
	// validator path (Load enforces real model refs / api keys); use
	// raw decode against the actual file we just wrote.
	loaded, err := LoadRawFrom(path)
	if err != nil {
		t.Fatalf("LoadRawFrom: %v", err)
	}
	pc, ok := loaded.Providers["claude-code"]
	if !ok {
		t.Fatal("claude-code provider missing after round-trip")
	}
	if !pc.UseCLI {
		t.Errorf("UseCLI = false after round-trip, want true")
	}
	if pc.APIKey != "" {
		t.Errorf("APIKey = %q after round-trip, want empty", pc.APIKey)
	}
}

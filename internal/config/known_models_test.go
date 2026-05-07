package config

import "testing"

func TestIsKnownModel(t *testing.T) {
	known := []string{
		"gemini-2.5-flash", "gemini-2.5-pro", "gemini-2.5-flash-lite",
		"gemini-3.1-pro-preview", "gemini-3.1-flash-lite-preview",
		"claude-sonnet-4-20250514", "claude-haiku-3-5",
		"gpt-4o", "gpt-4o-mini",
	}
	for _, id := range known {
		if !IsKnownModel(id) {
			t.Errorf("IsKnownModel(%q) = false, want true", id)
		}
	}

	unknown := []string{"gpt-3", "llama-70b", "made-up-model", ""}
	for _, id := range unknown {
		if IsKnownModel(id) {
			t.Errorf("IsKnownModel(%q) = true, want false", id)
		}
	}
}

func TestReviewModels(t *testing.T) {
	models := ReviewModels("gemini")
	if len(models) == 0 {
		t.Fatal("expected gemini review models")
	}
	for _, m := range models {
		if m.Provider != "gemini" {
			t.Errorf("ReviewModels(gemini) returned model with provider %q", m.Provider)
		}
		if !m.Review {
			t.Errorf("ReviewModels returned non-review model %q", m.ID)
		}
	}
}

func TestAOIModels(t *testing.T) {
	models := AOIModels("gemini")
	if len(models) == 0 {
		t.Fatal("expected gemini AOI models")
	}
	for _, m := range models {
		if m.Provider != "gemini" {
			t.Errorf("AOIModels(gemini) returned model with provider %q", m.Provider)
		}
		if !m.AOI {
			t.Errorf("AOIModels returned non-AOI model %q", m.ID)
		}
	}

	// flash-lite should be in AOI models
	found := false
	for _, m := range models {
		if m.ID == "gemini-2.5-flash-lite" {
			found = true
			break
		}
	}
	if !found {
		t.Error("gemini-2.5-flash-lite should be in AOI models")
	}
}

func TestReviewModelsUnknownProvider(t *testing.T) {
	models := ReviewModels("unknown-provider")
	if len(models) != 0 {
		t.Errorf("expected 0 models for unknown provider, got %d", len(models))
	}
}

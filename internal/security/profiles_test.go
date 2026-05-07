package security

import "testing"

func TestGetAOIProfile_KnownModel(t *testing.T) {
	p := GetAOIProfile("gemini-2.5-flash-lite")
	if p.Model != "gemini-2.5-flash-lite" {
		t.Errorf("Model = %q, want %q", p.Model, "gemini-2.5-flash-lite")
	}
	if p.ContextLines != 3 {
		t.Errorf("ContextLines = %d, want 3", p.ContextLines)
	}
	if p.Temperature != 0.1 {
		t.Errorf("Temperature = %f, want 0.1", p.Temperature)
	}
	if p.MaxOutputTokens != 8192 {
		t.Errorf("MaxOutputTokens = %d, want 8192", p.MaxOutputTokens)
	}
}

func TestGetAOIProfile_UnknownModel(t *testing.T) {
	p := GetAOIProfile("some-unknown-model-xyz")
	if p.Model != "some-unknown-model-xyz" {
		t.Errorf("Model = %q, want %q", p.Model, "some-unknown-model-xyz")
	}
	// Should use default values
	if p.ContextLines != 3 {
		t.Errorf("ContextLines = %d, want default 3", p.ContextLines)
	}
	if p.Temperature != 0.1 {
		t.Errorf("Temperature = %f, want default 0.1", p.Temperature)
	}
}

func TestGetAOIProfile_AllKnownModels(t *testing.T) {
	models := []string{
		"gemini-2.5-flash-lite",
		"gemini-2.5-flash",
		"gemini-3.1-flash-lite-preview",
		"gemini-3.1-pro-preview",
	}
	for _, m := range models {
		p := GetAOIProfile(m)
		if p.Model != m {
			t.Errorf("GetAOIProfile(%q).Model = %q", m, p.Model)
		}
		if p.MaxOutputTokens == 0 {
			t.Errorf("GetAOIProfile(%q).MaxOutputTokens should not be 0", m)
		}
	}
}

package config

import "testing"

func TestPriceTag_WithPricing(t *testing.T) {
	m := KnownModel{InputPricePer1M: 1.25, OutputPricePer1M: 10.00}
	got := m.PriceTag()
	want := "$1.25/$10.00 per 1M tok"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPriceTag_ZeroPricing(t *testing.T) {
	m := KnownModel{}
	if got := m.PriceTag(); got != "" {
		t.Errorf("expected empty string for zero pricing, got %q", got)
	}
}

func TestPriceTag_PartialPricing(t *testing.T) {
	m := KnownModel{InputPricePer1M: 0.15}
	got := m.PriceTag()
	if got == "" {
		t.Error("expected non-empty price tag when only input price is set")
	}
}

func TestSpeedIcon_Fast(t *testing.T) {
	m := KnownModel{Speed: "fast"}
	if got := m.SpeedIcon(); got != "⚡" {
		t.Errorf("got %q, want ⚡", got)
	}
}

func TestSpeedIcon_Medium(t *testing.T) {
	m := KnownModel{Speed: "medium"}
	if got := m.SpeedIcon(); got != "●" {
		t.Errorf("got %q, want ●", got)
	}
}

func TestSpeedIcon_Slow(t *testing.T) {
	m := KnownModel{Speed: "slow"}
	if got := m.SpeedIcon(); got != "◐" {
		t.Errorf("got %q, want ◐", got)
	}
}

func TestSpeedIcon_Unknown(t *testing.T) {
	m := KnownModel{Speed: ""}
	if got := m.SpeedIcon(); got != "" {
		t.Errorf("expected empty string for unknown speed, got %q", got)
	}
}

func TestIsKnownModel_BareID(t *testing.T) {
	if !IsKnownModel("gpt-5.4") {
		t.Error("gpt-5.4 should be known")
	}
	if IsKnownModel("nonexistent-model") {
		t.Error("nonexistent-model should not be known")
	}
}

func TestIsKnownModel_ProviderQualified(t *testing.T) {
	if !IsKnownModel("openai/gpt-5.4") {
		t.Error("openai/gpt-5.4 should be known")
	}
	if !IsKnownModel("github-copilot/gpt-5.4") {
		t.Error("github-copilot/gpt-5.4 should be known")
	}
	if IsKnownModel("anthropic/gpt-5.4") {
		t.Error("anthropic/gpt-5.4 should not be known")
	}
}

func TestGetKnownModel_BareID_PrefersPricing(t *testing.T) {
	m, ok := GetKnownModel("gpt-5.4")
	if !ok {
		t.Fatal("gpt-5.4 should be found")
	}
	// Bare lookup should return the entry with pricing (openai), not zero-price copilot
	if m.InputPricePer1M == 0 {
		t.Errorf("bare gpt-5.4 should return entry with pricing, got provider=%s price=%.2f", m.Provider, m.InputPricePer1M)
	}
	if m.Provider != "openai" {
		t.Errorf("bare gpt-5.4 should resolve to openai, got %s", m.Provider)
	}
}

func TestGetKnownModel_ProviderQualified(t *testing.T) {
	m, ok := GetKnownModel("github-copilot/gpt-5.4")
	if !ok {
		t.Fatal("github-copilot/gpt-5.4 should be found")
	}
	if m.Provider != "github-copilot" {
		t.Errorf("expected provider github-copilot, got %s", m.Provider)
	}

	m, ok = GetKnownModel("openai/gpt-5.4")
	if !ok {
		t.Fatal("openai/gpt-5.4 should be found")
	}
	if m.Provider != "openai" {
		t.Errorf("expected provider openai, got %s", m.Provider)
	}
}

func TestGetKnownModel_NotFound(t *testing.T) {
	_, ok := GetKnownModel("nonexistent")
	if ok {
		t.Error("should not find nonexistent model")
	}
	_, ok = GetKnownModel("openai/nonexistent")
	if ok {
		t.Error("should not find openai/nonexistent")
	}
}

func TestGetKnownModelForProvider(t *testing.T) {
	m, ok := GetKnownModelForProvider("github-copilot", "gpt-5.4")
	if !ok {
		t.Fatal("github-copilot gpt-5.4 should be found")
	}
	if m.Provider != "github-copilot" {
		t.Errorf("expected github-copilot, got %s", m.Provider)
	}

	m, ok = GetKnownModelForProvider("openai", "gpt-5.4")
	if !ok {
		t.Fatal("openai gpt-5.4 should be found")
	}
	if m.Provider != "openai" {
		t.Errorf("expected openai, got %s", m.Provider)
	}

	_, ok = GetKnownModelForProvider("anthropic", "gpt-5.4")
	if ok {
		t.Error("anthropic should not have gpt-5.4")
	}
}

func TestKnownModelsForProvider(t *testing.T) {
	copilotModels := KnownModelsForProvider("github-copilot")
	if len(copilotModels) == 0 {
		t.Fatal("should have github-copilot models")
	}
	for _, m := range copilotModels {
		if m.Provider != "github-copilot" {
			t.Errorf("got provider %s in github-copilot list", m.Provider)
		}
	}

	none := KnownModelsForProvider("nonexistent")
	if len(none) != 0 {
		t.Errorf("nonexistent provider should return empty, got %d", len(none))
	}
}

func TestInitDedup_PricingPreserved(t *testing.T) {
	// gemini-3.1-pro-preview exists on both gemini (with pricing) and github-copilot (zero pricing)
	// The bare lookup should return the one with pricing
	m, ok := GetKnownModel("gemini-3.1-pro-preview")
	if !ok {
		t.Fatal("gemini-3.1-pro-preview should be found")
	}
	if m.InputPricePer1M == 0 {
		t.Error("bare lookup should return entry with pricing")
	}

	// But provider-qualified should return the copilot one
	mc, ok := GetKnownModel("github-copilot/gemini-3.1-pro-preview")
	if !ok {
		t.Fatal("github-copilot/gemini-3.1-pro-preview should be found")
	}
	if mc.Provider != "github-copilot" {
		t.Errorf("expected github-copilot, got %s", mc.Provider)
	}
}

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

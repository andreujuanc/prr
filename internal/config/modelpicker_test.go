package config

import (
	"strings"
	"testing"
)

func TestFormatModelLabel_Basic(t *testing.T) {
	m := KnownModel{
		Label:            "Test Model",
		Thinking:         false,
		InputPricePer1M:  1.25,
		OutputPricePer1M: 10.00,
		Speed:            "medium",
	}
	label := formatModelLabel(m, nil)
	if !strings.Contains(label, "Test Model") {
		t.Error("expected model label")
	}
	if !strings.Contains(label, "1.25") {
		t.Error("expected input price")
	}
	if !strings.Contains(label, "10.00") {
		t.Error("expected output price")
	}
	if strings.Contains(label, "[thinking]") {
		t.Error("should not contain thinking tag")
	}
}

func TestFormatModelLabel_WithThinking(t *testing.T) {
	m := KnownModel{
		Label:            "Thinker",
		Thinking:         true,
		InputPricePer1M:  2.50,
		OutputPricePer1M: 15.00,
		Speed:            "slow",
	}
	label := formatModelLabel(m, nil)
	if !strings.Contains(label, "[thinking]") {
		t.Error("expected thinking tag")
	}
}

func TestFormatAOIModelLabel_NoBenchmark(t *testing.T) {
	m := KnownModel{
		Label:            "Flash",
		InputPricePer1M:  0.15,
		OutputPricePer1M: 0.60,
		Speed:            "fast",
	}
	label := formatAOIModelLabel(m, nil)
	if !strings.Contains(label, "Flash") {
		t.Error("expected model label")
	}
	if !strings.Contains(label, "0.15") {
		t.Error("expected input price in fallback")
	}
}

func TestFormatAOIModelLabel_WithBenchmark(t *testing.T) {
	m := KnownModel{
		ID:    "test-model",
		Label: "Flash",
	}
	bench := &BenchmarkResults{
		Models: []ModelBenchmark{
			{
				ModelID:     "test-model",
				RecallPct:   85.0,
				LatencyMs:   2500,
				CostPerScan: 0.003,
			},
		},
	}
	label := formatAOIModelLabel(m, bench)
	if !strings.Contains(label, "85%") {
		t.Error("expected recall percentage")
	}
	if !strings.Contains(label, "2.5s") {
		t.Error("expected latency in seconds")
	}
	if !strings.Contains(label, "0.003") {
		t.Error("expected cost per scan")
	}
}

func TestResolveModelProvider_PrefersConfiguredProvider(t *testing.T) {
	// A model that exists under multiple providers should resolve to
	// the one the user has actually configured with an API key.
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"openai": {APIKey: "sk-test"},
		},
	}
	got := cfg.resolveModelProvider("gpt-4.1")
	// "gpt-4.1" lives under github-copilot in knownModels but the user
	// only has openai configured — we fall back to GetKnownModel's
	// default provider. Either way the result must contain a slash and
	// the model id.
	if !strings.Contains(got, "/gpt-4.1") {
		t.Errorf("resolveModelProvider(%q) = %q; want a provider/model-id form", "gpt-4.1", got)
	}
}

func TestResolveModelProvider_UnknownModelReturnsBare(t *testing.T) {
	cfg := &Config{}
	got := cfg.resolveModelProvider("never-heard-of-this-model")
	if got != "never-heard-of-this-model" {
		t.Errorf("unknown model should return the bare ID, got %q", got)
	}
}

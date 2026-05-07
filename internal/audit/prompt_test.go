package audit

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/config"
)

func TestFormatModelLabel_Basic(t *testing.T) {
	m := config.KnownModel{
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
	m := config.KnownModel{
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
	m := config.KnownModel{
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
	m := config.KnownModel{
		ID:    "test-model",
		Label: "Flash",
	}
	bench := &config.BenchmarkResults{
		Models: []config.ModelBenchmark{
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

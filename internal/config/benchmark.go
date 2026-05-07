package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

//go:embed benchmark.json
var defaultBenchmarkData []byte

// ModelBenchmark holds benchmark results for a single model configuration.
type ModelBenchmark struct {
	ModelID     string  `json:"model_id"`
	ConfigName  string  `json:"config_name"`            // e.g. "temp=0.1", "thinking=2k"
	Temperature float64 `json:"temperature"`
	ThinkingBudget int  `json:"thinking_budget,omitempty"`
	RecallPct   float64 `json:"recall_pct"`             // overall recall percentage (0-100)
	MustFindPct float64 `json:"must_find_pct"`          // must-find recall percentage (0-100)
	LatencyMs   int     `json:"latency_ms"`             // scan latency in milliseconds
	CostPerScan float64 `json:"cost_per_scan"`          // estimated USD per scan
	TotalAOIs   int     `json:"total_aois"`             // AOIs found
	FalseAlarms int     `json:"false_alarms"`           // false positives
}

// BenchmarkResults holds the full benchmark output file.
type BenchmarkResults struct {
	Version   int              `json:"version"`    // schema version, currently 1
	Timestamp time.Time        `json:"timestamp"`  // when the benchmark was run
	Models    []ModelBenchmark `json:"models"`
}

// BenchmarkPath returns ~/.config/prr/benchmark.json.
func BenchmarkPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "prr", "benchmark.json"), nil
}

// SaveBenchmarkResults writes benchmark results to ~/.config/prr/benchmark.json.
func SaveBenchmarkResults(results *BenchmarkResults) error {
	path, err := BenchmarkPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal benchmark results: %w", err)
	}

	return os.WriteFile(path, data, 0o644)
}

// LoadBenchmarkResults reads benchmark results, preferring a user-local file
// at ~/.config/prr/benchmark.json over the defaults embedded in the binary.
// Always returns non-nil results (falls back to embedded defaults).
func LoadBenchmarkResults() (*BenchmarkResults, error) {
	// Try user-local file first (written by running the benchmark test).
	path, err := BenchmarkPath()
	if err == nil {
		if data, err := os.ReadFile(path); err == nil {
			var results BenchmarkResults
			if err := json.Unmarshal(data, &results); err == nil {
				return &results, nil
			}
		}
	}

	// Fall back to embedded defaults.
	var results BenchmarkResults
	if err := json.Unmarshal(defaultBenchmarkData, &results); err != nil {
		return nil, fmt.Errorf("parse embedded benchmark data: %w", err)
	}

	return &results, nil
}

// GetModelBenchmark returns benchmark data for a specific model, or nil if not found.
// When multiple runs exist for the same model (different configs), returns the
// one with the highest recall.
func (b *BenchmarkResults) GetModelBenchmark(modelID string) *ModelBenchmark {
	if b == nil {
		return nil
	}
	var best *ModelBenchmark
	for i := range b.Models {
		if b.Models[i].ModelID == modelID {
			if best == nil || b.Models[i].RecallPct > best.RecallPct {
				best = &b.Models[i]
			}
		}
	}
	return best
}

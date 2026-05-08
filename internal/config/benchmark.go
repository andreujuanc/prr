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
	ModelID        string  `json:"model_id"`
	Provider       string  `json:"provider"`    // e.g. "gemini", "github-copilot"
	ConfigName     string  `json:"config_name"` // e.g. "temp=0.1", "thinking=2k"
	Temperature    float64 `json:"temperature"`
	ThinkingBudget int     `json:"thinking_budget,omitempty"`
	RecallPct      float64 `json:"recall_pct"`    // overall recall percentage (0-100)
	MustFindPct    float64 `json:"must_find_pct"` // must-find recall percentage (0-100)
	LatencyMs      int     `json:"latency_ms"`    // scan latency in milliseconds
	CostPerScan    float64 `json:"cost_per_scan"` // estimated USD per scan
	TotalAOIs      int     `json:"total_aois"`    // AOIs found
	FalseAlarms    int     `json:"false_alarms"`  // false positives
}

// BenchmarkResults holds the full benchmark output file.
type BenchmarkResults struct {
	Version   int              `json:"version"`   // schema version, currently 1
	Timestamp time.Time        `json:"timestamp"` // when the benchmark was run
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

// SaveBenchmarkResults merges new benchmark results into ~/.config/prr/benchmark.json.
// Existing entries are updated if they match on provider+model_id+config_name;
// new entries are appended. The timestamp is always updated.
func SaveBenchmarkResults(results *BenchmarkResults) error {
	path, err := BenchmarkPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Load existing results to merge with.
	var existing BenchmarkResults
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing) // ignore errors, start fresh if corrupt
	}

	// Build index of existing entries for fast lookup.
	type key struct{ provider, modelID, configName string }
	idx := make(map[key]int, len(existing.Models))
	for i, m := range existing.Models {
		idx[key{m.Provider, m.ModelID, m.ConfigName}] = i
	}

	// Merge new results: update existing or append.
	for _, m := range results.Models {
		k := key{m.Provider, m.ModelID, m.ConfigName}
		if i, ok := idx[k]; ok {
			existing.Models[i] = m // update
		} else {
			existing.Models = append(existing.Models, m)
			idx[k] = len(existing.Models) - 1
		}
	}

	existing.Version = results.Version
	existing.Timestamp = results.Timestamp

	data, err := json.MarshalIndent(&existing, "", "  ")
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
// When provider is non-empty, it matches on provider+model_id.
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

// GetModelBenchmarkForProvider returns benchmark data matching both provider and model ID.
// Falls back to provider-agnostic match if no provider-specific entry exists.
func (b *BenchmarkResults) GetModelBenchmarkForProvider(provider, modelID string) *ModelBenchmark {
	if b == nil {
		return nil
	}
	var bestExact, bestFallback *ModelBenchmark
	for i := range b.Models {
		if b.Models[i].ModelID != modelID {
			continue
		}
		if b.Models[i].Provider == provider {
			if bestExact == nil || b.Models[i].RecallPct > bestExact.RecallPct {
				bestExact = &b.Models[i]
			}
		} else if b.Models[i].Provider == "" {
			if bestFallback == nil || b.Models[i].RecallPct > bestFallback.RecallPct {
				bestFallback = &b.Models[i]
			}
		}
	}
	if bestExact != nil {
		return bestExact
	}
	return bestFallback
}

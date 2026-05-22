package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed aoi-benchmark.json
var defaultAOIBenchmarkData []byte

// embeddedBenchmarkDefaults maps benchmark name → embedded fallback JSON.
// Used by LoadBenchmarkResults when no on-disk archives exist for that name.
// Benchmarks without an embedded default just fall back to an empty result.
var embeddedBenchmarkDefaults = map[string][]byte{
	"aoi": defaultAOIBenchmarkData,
}

// ModelBenchmark holds benchmark results for a single model configuration.
// The schema is shared across benchmarks; deep-review-only metrics
// (severity accuracy, tool calls) are tagged omitempty so AOI archives
// don't carry empty fields.
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
	TotalAOIs      int     `json:"total_aois"`    // AOI count (or finding count for deep review)
	FalseAlarms    int     `json:"false_alarms"`  // false positives

	// Deep review only.
	SeverityCorrect int `json:"severity_correct,omitempty"`
	SeverityTotal   int `json:"severity_total,omitempty"`
	ToolCalls       int `json:"tool_calls,omitempty"`
}

// BenchmarkResults holds the full benchmark output file.
type BenchmarkResults struct {
	Version   int              `json:"version"`   // schema version, currently 1
	Timestamp time.Time        `json:"timestamp"` // when the benchmark was run
	GitSHA    string           `json:"git_sha,omitempty"`
	GitDirty  bool             `json:"git_dirty,omitempty"` // working tree had uncommitted changes
	Tag       string           `json:"tag,omitempty"`       // free-form label from PRR_BENCH_TAG
	Models    []ModelBenchmark `json:"models"`
}

// captureGitContext returns the short HEAD SHA and whether the working tree
// has uncommitted changes. Best-effort: returns zero values if git is
// unavailable or the cwd isn't a repo.
func captureGitContext() (sha string, dirty bool) {
	out, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return "", false
	}
	sha = strings.TrimSpace(string(out))
	if status, err := exec.Command("git", "status", "--porcelain").Output(); err == nil {
		dirty = len(strings.TrimSpace(string(status))) > 0
	}
	return sha, dirty
}

// BenchmarkDir returns ~/.config/prr/benchmarks/ — the directory holding
// benchmark archives. Kept as a subdirectory of the prr config dir so the
// growing archive set doesn't clutter the main config folder.
func BenchmarkDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "prr", "benchmarks"), nil
}

// BenchmarkArchivePath returns the path of the benchmark archive for name +
// time t, formatted as ~/.config/prr/benchmarks/<name>-benchmark-YYYY-MM-DDTHH-MM-SS.json
// (UTC). Colons are replaced with dashes for filesystem portability.
//
// The name prefix lets different benchmarks (aoi, review, ...) coexist in
// the same directory without colliding.
func BenchmarkArchivePath(name string, t time.Time) (string, error) {
	dir, err := BenchmarkDir()
	if err != nil {
		return "", err
	}
	file := fmt.Sprintf("%s-benchmark-%s.json", name, t.UTC().Format("2006-01-02T15-04-05"))
	return filepath.Join(dir, file), nil
}

// SaveBenchmarkResults writes the run as a dated archive in
// ~/.config/prr/benchmarks/. The name is the benchmark identifier (e.g.
// "aoi" or "review"); it determines the filename prefix. Each run is an
// immutable snapshot of just that run's models; the merged "current state"
// view is produced by LoadBenchmarkResults walking all archives latest-wins.
func SaveBenchmarkResults(name string, results *BenchmarkResults) error {
	if results.GitSHA == "" {
		results.GitSHA, results.GitDirty = captureGitContext()
	}
	if results.Tag == "" {
		results.Tag = os.Getenv("PRR_BENCH_TAG")
	}
	path, err := BenchmarkArchivePath(name, results.Timestamp)
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

// LoadBenchmarkResults walks all dated archives for the given benchmark name
// in ~/.config/prr/benchmarks/, merging them latest-wins by
// provider+model_id+config_name. Falls back to embedded defaults if no
// archives exist on disk and an embedded default is registered for that
// name. Always returns non-nil results.
func LoadBenchmarkResults(name string) (*BenchmarkResults, error) {
	dir, err := BenchmarkDir()
	if err == nil {
		if archives := listBenchmarkArchives(dir, name); len(archives) > 0 {
			if merged, ok := mergeBenchmarkArchives(archives); ok {
				return merged, nil
			}
		}
	}

	if data, ok := embeddedBenchmarkDefaults[name]; ok {
		var results BenchmarkResults
		if err := json.Unmarshal(data, &results); err != nil {
			return nil, fmt.Errorf("parse embedded %s benchmark data: %w", name, err)
		}
		return &results, nil
	}

	return &BenchmarkResults{Version: 1}, nil
}

// listBenchmarkArchives returns dated archive paths for the given benchmark
// name, sorted chronologically (oldest first). The dated filename format sorts
// correctly as text, so a plain string sort puts oldest first.
func listBenchmarkArchives(dir, name string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := name + "-benchmark-"
	var paths []string
	for _, e := range entries {
		fname := e.Name()
		if strings.HasPrefix(fname, prefix) && strings.HasSuffix(fname, ".json") {
			paths = append(paths, filepath.Join(dir, fname))
		}
	}
	sort.Strings(paths)
	return paths
}

// mergeBenchmarkArchives reads archives in order and merges them
// latest-wins by (provider, model_id, config_name). The returned bool is
// true if at least one archive parsed successfully.
func mergeBenchmarkArchives(paths []string) (*BenchmarkResults, bool) {
	type key struct{ provider, modelID, configName string }
	idx := make(map[key]int)
	var merged BenchmarkResults
	loaded := false
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r BenchmarkResults
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		loaded = true
		if r.Timestamp.After(merged.Timestamp) {
			merged.Timestamp = r.Timestamp
			merged.Version = r.Version
		}
		for _, m := range r.Models {
			k := key{m.Provider, m.ModelID, m.ConfigName}
			if i, ok := idx[k]; ok {
				merged.Models[i] = m
			} else {
				merged.Models = append(merged.Models, m)
				idx[k] = len(merged.Models) - 1
			}
		}
	}
	return &merged, loaded
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

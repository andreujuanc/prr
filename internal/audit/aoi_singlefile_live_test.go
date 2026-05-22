package audit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/security"
)

// TestLive_AOIScanSingleFile runs the AOI scanner (audit mode) against a
// single real repo file and prints the AOIs it found. The intent is to
// grade how well prr's Phase 2 catches the same areas a human reviewer
// would call out — feed it a file you've already audited by hand and
// compare.
//
// Run with:
//
//	PRR_LIVE_TESTS=1 go test ./internal/audit/ -run TestLive_AOIScanSingleFile -v -timeout 5m
//
// Optional env vars:
//
//	PRR_AOI_TARGET   path relative to repo root (default: internal/audit/boundaries.go)
//	PRR_AOI_MODEL    single model id, "model" or "provider/model" (default: configured fast model)
//	PRR_AOI_MODELS   comma-separated list; overrides PRR_AOI_MODEL and runs each
//	PRR_AOI_EXPECT   line spec the scanner should land on, e.g. "98,308-322"
//
// The scanner is invoked with auditMode=true and fileCategories=nil (= all
// categories). That gives the model the broadest possible surface — a miss
// here is strong evidence the scanner can't see the issue regardless of
// category routing.
func TestLive_AOIScanSingleFile(t *testing.T) {
	cfg := liveConfigOrSkip(t)

	target := os.Getenv("PRR_AOI_TARGET")
	if target == "" {
		target = "internal/audit/boundaries.go"
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(repoRoot, target))
	if err != nil {
		t.Fatalf("read target file %s: %v", target, err)
	}

	models, err := config.LoadModels()
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}

	specs := pickAOISpecs(cfg, models)
	if len(specs) == 0 {
		t.Skip("no usable model spec — set PRR_AOI_MODEL / PRR_AOI_MODELS or configure a fast model")
	}

	expect := parseExpectedLines(os.Getenv("PRR_AOI_EXPECT"))

	for _, spec := range specs {
		spec := spec
		t.Run(spec.label, func(t *testing.T) {
			runAOIOnFile(t, spec, target, string(body), expect)
		})
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

type aoiModelSpec struct {
	label          string
	modelID        string
	provider       string
	apiKey         string
	baseURL        string
	maxOutput      int
	temperature    float64
	thinkingBudget int
}

func liveConfigOrSkip(t *testing.T) *config.Config {
	t.Helper()
	if os.Getenv("PRR_LIVE_TESTS") != "1" {
		t.Skip("PRR_LIVE_TESTS=1 not set")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("config.Load: %v", err)
	}
	return cfg
}

// pickAOISpecs returns the set of models to run. Precedence:
//   - PRR_AOI_MODELS (comma list) — runs each
//   - PRR_AOI_MODEL  (single)     — runs one
//   - cfg.FastModel               — runs one
func pickAOISpecs(cfg *config.Config, models map[string]config.ModelConfig) []aoiModelSpec {
	fastRef, _ := config.ParseModelRef(cfg.FastModel)
	resolve := func(entry string) aoiModelSpec {
		providerName, modelID := fastRef.Provider, entry
		if ref, err := config.ParseModelRef(entry); err == nil && ref.Provider != "" {
			providerName, modelID = ref.Provider, ref.ModelID
		}
		pc := cfg.ProviderConfigFor(providerName)
		mc := config.GetModelConfig(models, modelID)
		return aoiModelSpec{
			label:          entry,
			modelID:        modelID,
			provider:       providerName,
			apiKey:         pc.APIKey,
			baseURL:        pc.BaseURL,
			maxOutput:      mc.MaxOutputTokens,
			temperature:    mc.Temperature,
			thinkingBudget: mc.ThinkingBudget.Fast,
		}
	}

	if list := os.Getenv("PRR_AOI_MODELS"); list != "" {
		var out []aoiModelSpec
		for _, e := range strings.Split(list, ",") {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			out = append(out, resolve(e))
		}
		return out
	}
	if single := os.Getenv("PRR_AOI_MODEL"); single != "" {
		return []aoiModelSpec{resolve(single)}
	}
	if cfg.FastModel != "" {
		return []aoiModelSpec{resolve(cfg.FastModel)}
	}
	return nil
}

// lineRange is an inclusive line span used both for expected hits and
// for grading whether an AOI "covered" a target line.
type lineRange struct{ start, end int }

func parseExpectedLines(s string) []lineRange {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []lineRange
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx := strings.Index(part, "-"); idx >= 0 {
			a, errA := strconv.Atoi(strings.TrimSpace(part[:idx]))
			b, errB := strconv.Atoi(strings.TrimSpace(part[idx+1:]))
			if errA == nil && errB == nil && a > 0 && b >= a {
				out = append(out, lineRange{a, b})
			}
			continue
		}
		if n, err := strconv.Atoi(part); err == nil && n > 0 {
			out = append(out, lineRange{n, n})
		}
	}
	return out
}

// aoiCoversLine reports whether the AOI's [Line, EndLine] span intersects
// the requested line. EndLine == 0 is treated as a single-line AOI at Line.
func aoiCoversLine(aoi security.AreaOfInterest, line int) bool {
	start := aoi.Line
	end := aoi.EndLine
	if end < start {
		end = start
	}
	return line >= start && line <= end
}

func runAOIOnFile(t *testing.T, spec aoiModelSpec, target, body string, expect []lineRange) {
	t.Helper()
	if spec.apiKey == "" {
		t.Skipf("no API key configured for provider %q", spec.provider)
	}

	provider, err := ai.NewProvider(ai.ProviderConfig{
		ProviderName:    spec.provider,
		ModelID:         spec.modelID,
		APIKey:          spec.apiKey,
		BaseURL:         spec.baseURL,
		MaxOutputTokens: spec.maxOutput,
		Temperature:     ai.TempPtr(spec.temperature),
		ThinkingBudget:  spec.thinkingBudget,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	tracker := &ai.UsageTracker{}
	client := ai.NewAgent(provider, nil, ai.WithUsageTracker(tracker))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// auditMode=true matches `prr audit`'s call site; fileCategories=nil
	// means "look for everything" (no per-file category narrowing).
	inputs := map[string]string{target: body}
	start := time.Now()
	report, err := security.ScanAreasOfInterestClassified(
		ctx, client, inputs, nil, nil,
		func(status string) { t.Logf("[progress] %s", status) },
		nil, true,
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("AOI scan failed after %s: %v", elapsed, err)
	}

	usage := tracker.Snapshot()
	t.Logf("\nmodel=%s  time=%.1fs  total_aois=%d  tokens=%d in + %d out",
		spec.label, elapsed.Seconds(), report.TotalAOIs, usage.InputTokens, usage.OutputTokens)

	// Find the result for our file (the scanner may also drop it silently
	// — surface that distinctly).
	var fileResult *security.AOIScanResult
	for i := range report.Files {
		if report.Files[i].File == target {
			fileResult = &report.Files[i]
			break
		}
	}
	if fileResult == nil {
		t.Errorf("scanner returned no result for %s (silent drop)", target)
		return
	}
	aois := fileResult.AreasOfInterest

	// Sort by line for stable diff-able output.
	sort.SliceStable(aois, func(i, j int) bool { return aois[i].Line < aois[j].Line })

	t.Logf("── %s ── (%d AOIs)", target, len(aois))
	for _, aoi := range aois {
		loc := fmt.Sprintf("L%d", aoi.Line)
		if aoi.EndLine > 0 && aoi.EndLine != aoi.Line {
			loc = fmt.Sprintf("L%d-%d", aoi.Line, aoi.EndLine)
		}
		cat := aoi.Category.String()
		if aoi.Subcategory != "" {
			cat += "/" + aoi.Subcategory
		}
		urgency := aoi.Urgency
		if urgency == "" {
			urgency = "grouped"
		}
		concern := aoi.Concern
		if concern == "" {
			concern = aoi.Reasoning
		}
		t.Logf("  %-10s [%s] (%s)  %s", loc, cat, urgency, oneLine(concern, 140))
	}

	// Grade against expected lines, if any.
	if len(expect) > 0 {
		t.Log("")
		t.Logf("expected vs found:")
		hits := 0
		for _, exp := range expect {
			label := strconv.Itoa(exp.start)
			if exp.end != exp.start {
				label = fmt.Sprintf("%d-%d", exp.start, exp.end)
			}
			covering := []string{}
			for _, aoi := range aois {
				for ln := exp.start; ln <= exp.end; ln++ {
					if aoiCoversLine(aoi, ln) {
						r := fmt.Sprintf("L%d", aoi.Line)
						if aoi.EndLine > 0 && aoi.EndLine != aoi.Line {
							r = fmt.Sprintf("L%d-%d", aoi.Line, aoi.EndLine)
						}
						covering = append(covering, r)
						break
					}
				}
			}
			if len(covering) > 0 {
				hits++
				t.Logf("  L%-8s HIT   covered by %s", label, strings.Join(dedup(covering), ", "))
			} else {
				t.Logf("  L%-8s MISS", label)
			}
		}
		t.Logf("hit rate: %d/%d", hits, len(expect))
	}
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if max > 0 && len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

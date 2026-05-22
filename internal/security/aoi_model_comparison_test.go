package security_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/security"
)

// ── AOI Model Comparison Test ─────────────────────────────────────────────
//
// This test compares multiple models on the AOI security pre-scan task.
// It uses a fixed set of diffs with KNOWN security issues (ground truth)
// and measures which model catches the most real issues at the lowest cost.
//
// Credentials are read from ~/.config/prr/config.json (same as the app).
// Model tuning parameters are read from models.json.
//
// Run with:
//   PRR_LIVE_TESTS=1 go test ./internal/security/ -run TestAOIModelComparison -v -timeout 10m
//
// Override models list:
//   PRR_AOI_MODELS="gemini-3.1-flash-lite,gemini-3.1-pro-preview" PRR_LIVE_TESTS=1 go test ...

// groundTruthAOI defines an expected security area of interest.
// The test checks whether the model found it.
type groundTruthAOI struct {
	file       string
	lineRange  [2]int // [startLine, endLine] — match if model's line falls in this range
	category   string // expected category
	importance string // "must-find" or "nice-to-find"
	desc       string // human description for the report
}

// securityTestDiffs returns realistic diffs with known security issues.
// Each issue is documented in the groundTruth slice.
func securityTestDiffs() (map[string]string, []groundTruthAOI) {
	diffs := map[string]string{
		"internal/api/handler.go": `@@ -0,0 +1,62 @@
+package api
+
+import (
+	"database/sql"
+	"fmt"
+	"net/http"
+	"os/exec"
+	"html/template"
+)
+
+var db *sql.DB
+
+// SearchHandler handles user search requests
+func SearchHandler(w http.ResponseWriter, r *http.Request) {
+	query := r.URL.Query().Get("q")
+	
+	// BUG: SQL injection — string concatenation in query
+	rows, err := db.Query("SELECT * FROM users WHERE name = '" + query + "'")
+	if err != nil {
+		http.Error(w, err.Error(), 500)
+		return
+	}
+	defer rows.Close()
+	
+	// BUG: XSS — writing user input directly to response
+	fmt.Fprintf(w, "<h1>Results for: %s</h1>", query)
+}
+
+// ExecHandler runs a command based on user input
+func ExecHandler(w http.ResponseWriter, r *http.Request) {
+	cmd := r.FormValue("cmd")
+	
+	// BUG: Command injection — user input in exec.Command
+	out, err := exec.Command("sh", "-c", cmd).Output()
+	if err != nil {
+		http.Error(w, "command failed: " + err.Error(), 500)
+		return
+	}
+	w.Write(out)
+}
+
+// AdminHandler — no auth check
+func AdminHandler(w http.ResponseWriter, r *http.Request) {
+	// BUG: Missing authentication — admin endpoint with no auth
+	w.Write([]byte("admin panel"))
+}
+
+// TemplateHandler renders user-controlled template
+func TemplateHandler(w http.ResponseWriter, r *http.Request) {
+	tmplStr := r.FormValue("template")
+	
+	// BUG: Server-side template injection
+	t, err := template.New("user").Parse(tmplStr)
+	if err != nil {
+		http.Error(w, "bad template", 400)
+		return
+	}
+	t.Execute(w, nil)
+}`,

		"internal/api/redirect.go": `@@ -0,0 +1,28 @@
+package api
+
+import (
+	"io"
+	"net/http"
+	"os"
+	"path/filepath"
+)
+
+// RedirectHandler redirects to a user-provided URL
+func RedirectHandler(w http.ResponseWriter, r *http.Request) {
+	target := r.URL.Query().Get("url")
+	// BUG: Open redirect — no validation of target URL
+	http.Redirect(w, r, target, http.StatusFound)
+}
+
+// FileHandler serves files based on user input
+func FileHandler(w http.ResponseWriter, r *http.Request) {
+	name := r.URL.Query().Get("file")
+	// BUG: Path traversal — user controls file path
+	path := filepath.Join("/data", name)
+	f, err := os.Open(path)
+	if err != nil {
+		http.Error(w, "not found", 404)
+		return
+	}
+	defer f.Close()
+	io.Copy(w, f)
+}`,

		"internal/auth/token.go": `@@ -0,0 +1,35 @@
+package auth
+
+import (
+	"crypto/md5"
+	"encoding/hex"
+	"fmt"
+	"log"
+	"time"
+)
+
+const secretKey = "super-secret-key-12345"  // BUG: Hardcoded secret
+
+// HashPassword hashes a password
+func HashPassword(password string) string {
+	// BUG: Weak hashing — MD5 is not suitable for passwords
+	h := md5.Sum([]byte(password))
+	return hex.EncodeToString(h[:])
+}
+
+// GenerateToken creates an auth token
+func GenerateToken(userID string) string {
+	// BUG: Predictable token — uses timestamp, not crypto/rand
+	token := fmt.Sprintf("%s-%d", userID, time.Now().UnixNano())
+	return HashPassword(token)
+}
+
+// ValidateToken checks a token (leaks info in logs)
+func ValidateToken(token string) bool {
+	// BUG: Logs sensitive data
+	log.Printf("Validating token: %s", token)
+	
+	// BUG: Timing attack — non-constant-time comparison
+	expected := GenerateToken("admin")
+	return token == expected
+}`,

		"internal/config/settings.go": `@@ -0,0 +1,20 @@
+package config
+
+import (
+	"encoding/json"
+	"net/http"
+)
+
+// FetchConfig fetches configuration from a user-provided URL
+func FetchConfig(configURL string) (map[string]interface{}, error) {
+	// BUG: SSRF — fetching from user-controlled URL
+	resp, err := http.Get(configURL)
+	if err != nil {
+		return nil, err
+	}
+	defer resp.Body.Close()
+	
+	var cfg map[string]interface{}
+	json.NewDecoder(resp.Body).Decode(&cfg)
+	return cfg, nil
+}`,

		"internal/util/helpers.go": `@@ -0,0 +1,15 @@
+package util
+
+import "strings"
+
+// Capitalize capitalizes the first letter of a string.
+func Capitalize(s string) string {
+	if s == "" {
+		return s
+	}
+	return strings.ToUpper(s[:1]) + s[1:]
+}
+
+// Max returns the larger of two ints.
+func Max(a, b int) int {
+	if a > b { return a }; return b
+}`,

		"go.mod": `@@ -1,3 +1,5 @@
 module example.com/app
 
-go 1.21
+go 1.22
+
+require github.com/some/dep v1.0.0`,
	}

	groundTruth := []groundTruthAOI{
		// handler.go
		{file: "internal/api/handler.go", lineRange: [2]int{15, 22}, category: "input-validation", importance: "must-find", desc: "SQL injection via string concatenation"},
		{file: "internal/api/handler.go", lineRange: [2]int{25, 26}, category: "input-validation", importance: "must-find", desc: "XSS via fmt.Fprintf with user input"},
		{file: "internal/api/handler.go", lineRange: [2]int{30, 38}, category: "input-validation", importance: "must-find", desc: "Command injection via exec.Command with user input"},
		{file: "internal/api/handler.go", lineRange: [2]int{42, 45}, category: "authorization", importance: "must-find", desc: "Admin endpoint without authentication"},
		{file: "internal/api/handler.go", lineRange: [2]int{49, 57}, category: "input-validation", importance: "nice-to-find", desc: "Server-side template injection"},

		// redirect.go
		{file: "internal/api/redirect.go", lineRange: [2]int{11, 14}, category: "input-validation", importance: "must-find", desc: "Open redirect with user-controlled URL"},
		{file: "internal/api/redirect.go", lineRange: [2]int{18, 28}, category: "input-validation", importance: "must-find", desc: "Path traversal via user-controlled file path"},

		// token.go
		{file: "internal/auth/token.go", lineRange: [2]int{11, 11}, category: "configuration", importance: "must-find", desc: "Hardcoded secret key in source"},
		{file: "internal/auth/token.go", lineRange: [2]int{15, 17}, category: "cryptography", importance: "must-find", desc: "MD5 used for password hashing"},
		{file: "internal/auth/token.go", lineRange: [2]int{22, 24}, category: "cryptography", importance: "nice-to-find", desc: "Predictable token generation (no crypto/rand)"},
		{file: "internal/auth/token.go", lineRange: [2]int{29, 30}, category: "data-integrity", importance: "must-find", desc: "Token logged in plaintext"},
		{file: "internal/auth/token.go", lineRange: [2]int{32, 33}, category: "cryptography", importance: "nice-to-find", desc: "Non-constant-time string comparison for secrets"},

		// settings.go
		{file: "internal/config/settings.go", lineRange: [2]int{9, 11}, category: "external-io", importance: "must-find", desc: "SSRF via http.Get with user-controlled URL"},

		// helpers.go — SHOULD have no findings (clean file)
	}

	return diffs, groundTruth
}

// Benchmark tuning is independent of the user's models.json so results are
// reproducible across machines. Each model fans out into the cross product of
// temperatures × thinking budgets — one run per combination. Override defaults
// per-run with env vars:
//
//	PRR_BENCH_TEMPERATURE           float, default 0.1   — baseline applied to every spec
//	PRR_BENCH_TEMPERATURE_VARIANTS  csv,   default ""    — extra temperatures to sweep
//	PRR_BENCH_THINKING_BUDGET       int,   default 0     — baseline thinking budget
//	PRR_BENCH_THINKING_VARIANTS     csv,   default "1024,2048"
//	    extra thinking budgets to sweep for thinking-capable non-copilot,
//	    non-claude-code models. Both providers ignore the explicit thinking
//	    budget knob (their CLIs/servers decide internally), so fanning them
//	    out wastes calls.
//	PRR_BENCH_MAX_OUTPUT            int,   default 8192
//
// Set a *_VARIANTS env var to the empty string to disable that axis's fan-out
// while keeping the baseline.
func benchmarkTemperature() float64 {
	if v, ok := envFloat("PRR_BENCH_TEMPERATURE"); ok {
		return v
	}
	return 0.1
}

func benchmarkMaxOutputTokens() int {
	if v, ok := envInt("PRR_BENCH_MAX_OUTPUT"); ok {
		return v
	}
	return 8192
}

func benchmarkThinkingBudget() int {
	if v, ok := envInt("PRR_BENCH_THINKING_BUDGET"); ok {
		return v
	}
	return 0
}

// benchmarkTemperatures returns the temperatures to sweep — baseline plus any
// PRR_BENCH_TEMPERATURE_VARIANTS, with duplicates removed.
func benchmarkTemperatures() []float64 {
	base := benchmarkTemperature()
	out := []float64{base}
	s, present := os.LookupEnv("PRR_BENCH_TEMPERATURE_VARIANTS")
	if !present {
		return out
	}
	seen := map[float64]bool{base: true}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if v, err := strconv.ParseFloat(p, 64); err == nil && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// benchmarkThinkingBudgets returns the thinking budgets to sweep — baseline
// plus any PRR_BENCH_THINKING_VARIANTS, with duplicates removed.
func benchmarkThinkingBudgets() []int {
	base := benchmarkThinkingBudget()
	out := []int{base}
	s, present := os.LookupEnv("PRR_BENCH_THINKING_VARIANTS")
	if !present {
		for _, v := range []int{1024, 2048} {
			if v != base {
				out = append(out, v)
			}
		}
		return out
	}
	seen := map[int]bool{base: true}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if v, err := strconv.Atoi(p); err == nil && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func envInt(name string) (int, bool) {
	s := os.Getenv(name)
	if s == "" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	return v, err == nil
}

func envFloat(name string) (float64, bool) {
	s := os.Getenv(name)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// formatThinkingLabel renders a thinking budget for spec names: "0", "1k",
// "512" — using the k-suffix only when the budget is a clean multiple of 1024.
func formatThinkingLabel(budget int) string {
	if budget == 0 {
		return "thinking=0"
	}
	if budget%1024 == 0 {
		return fmt.Sprintf("thinking=%dk", budget/1024)
	}
	return fmt.Sprintf("thinking=%d", budget)
}

// providerHonorsThinkingBudget reports whether the provider actually applies
// the explicit thinking-budget knob. Copilot and Claude Code both decide
// thinking internally, so sweeping budgets for them produces redundant runs.
func providerHonorsThinkingBudget(provider string) bool {
	return provider != "github-copilot" && provider != "claude-code"
}

// expandSpecs fans out one (provider, model) into the cross product of
// temperatures × thinking budgets. For models/providers that don't honor
// the explicit thinking knob, only the baseline budget is used.
func expandSpecs(provider, modelID, apiKey, baseURL string, supportsThinking bool) []modelSpec {
	temps := benchmarkTemperatures()
	budgets := []int{benchmarkThinkingBudget()}
	if supportsThinking && providerHonorsThinkingBudget(provider) {
		budgets = benchmarkThinkingBudgets()
	}

	multiTemp := len(temps) > 1
	multiBudget := len(budgets) > 1

	var specs []modelSpec
	for _, t := range temps {
		for _, b := range budgets {
			name := fmt.Sprintf("[%s] %s", provider, modelID)
			var parts []string
			if multiTemp {
				parts = append(parts, fmt.Sprintf("temp=%g", t))
			}
			if multiBudget {
				parts = append(parts, formatThinkingLabel(b))
			}
			if len(parts) > 0 {
				name += " (" + strings.Join(parts, ", ") + ")"
			}
			specs = append(specs, modelSpec{
				name:           name,
				model:          modelID,
				provider:       provider,
				apiKey:         apiKey,
				baseURL:        baseURL,
				temperature:    t,
				thinkingBudget: b,
				maxOutput:      benchmarkMaxOutputTokens(),
			})
		}
	}
	return specs
}

// modelSpec defines a model to test.
type modelSpec struct {
	name           string
	model          string
	provider       string // provider name (e.g. "gemini", "github-copilot")
	apiKey         string
	baseURL        string // optional endpoint override
	thinkingBudget int
	temperature    float64
	maxOutput      int
}

// newSpec creates a modelSpec at the baseline benchmark tuning.
// Callers may overwrite individual fields (e.g. thinkingBudget) afterwards.
func newSpec(name, modelID, provider, apiKey, baseURL string) modelSpec {
	return modelSpec{
		name:           name,
		model:          modelID,
		provider:       provider,
		apiKey:         apiKey,
		baseURL:        baseURL,
		thinkingBudget: benchmarkThinkingBudget(),
		temperature:    benchmarkTemperature(),
		maxOutput:      benchmarkMaxOutputTokens(),
	}
}

// defaultModels returns models to compare, iterating over ALL configured providers
// and including every known model tagged with AOI=true or Review=true for that provider.
// Each model is expanded by expandSpecs into the temperature × thinking-budget
// cross product.
//
// Only credentials (API key, base URL) are sourced from cfg — tuning is fixed
// by the benchmark constants so results don't drift with the user's models.json.
func defaultModels(cfg *config.Config) []modelSpec {
	var specs []modelSpec

	for providerName, pc := range cfg.Providers {
		if pc.APIKey == "" {
			continue
		}

		for _, km := range config.KnownModelsForProvider(providerName) {
			if !km.AOI && !km.Review {
				continue // skip models not useful for AOI
			}
			specs = append(specs, expandSpecs(providerName, km.ID, pc.APIKey, pc.BaseURL, km.Thinking)...)
		}
	}

	return specs
}

// createProvider creates an ai.Provider from a modelSpec using the provider factory.
func createProvider(spec modelSpec) (ai.Provider, error) {
	return ai.NewProvider(ai.ProviderConfig{
		ProviderName:    spec.provider,
		ModelID:         spec.model,
		APIKey:          spec.apiKey,
		BaseURL:         spec.baseURL,
		MaxOutputTokens: spec.maxOutput,
		Temperature:     ai.TempPtr(spec.temperature),
		ThinkingBudget:  spec.thinkingBudget,
	})
}

// loadTestConfig loads ~/.config/prr/config.json for live tests.
// Skips the test unless PRR_LIVE_TESTS=1 is set (live tests make real API calls).
// Returns nil if the config file is missing or invalid (caller should t.Skip).
func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	if os.Getenv("PRR_LIVE_TESTS") != "1" {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	return cfg
}

// parseModelsFromEnv parses PRR_AOI_MODELS env var into model specs.
// Each entry is expanded by expandSpecs into the temperature × thinking-budget
// cross product. Tuning is fixed by the benchmark constants/env vars;
// credentials come from config.json.
//
// Model list format: entries may be either "provider/model-id" or just "model-id".
// If a provider is omitted the provider from the configured fast_model is used.
// Example: PRR_AOI_MODELS="github-copilot/gpt-5-mini,gemini-3.1-flash-lite"
func parseModelsFromEnv(cfg *config.Config) []modelSpec {
	envModels := os.Getenv("PRR_AOI_MODELS")
	if envModels == "" {
		return nil
	}

	// Fast model provider is used as the fallback when an entry omits the provider.
	fastRef, _ := config.ParseModelRef(cfg.FastModel)

	var specs []modelSpec
	for entry := range strings.SplitSeq(envModels, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// Allow either "provider/model-id" or just "model-id". If the
		// provider is omitted, fall back to the configured fast model's provider.
		var providerName, modelID string
		if ref, err := config.ParseModelRef(entry); err == nil {
			providerName = ref.Provider
			modelID = ref.ModelID
		} else {
			providerName = fastRef.Provider
			modelID = entry
		}

		pc := cfg.ProviderConfigFor(providerName)
		supportsThinking := false
		if km, ok := config.GetKnownModelForProvider(providerName, modelID); ok {
			supportsThinking = km.Thinking
		}
		specs = append(specs, expandSpecs(providerName, modelID, pc.APIKey, pc.BaseURL, supportsThinking)...)
	}
	return specs
}

// modelResult holds the result of testing one model.
type modelResult struct {
	spec     modelSpec
	report   *security.AOIReport
	duration time.Duration
	err      error

	// Scoring
	mustFindTotal int
	mustFindHits  int
	niceFindTotal int
	niceFindHits  int
	falseAlarms   int // AOIs that don't match any ground truth
	totalAOIs     int

	// Token usage & cost
	inputTokens  int
	outputTokens int
	cost         float64 // estimated USD
}

// matchAOI checks if a model-produced AOI matches a ground truth entry.
// Matching is flexible: file must match, line must fall in the range,
// and category should match (but we also count cross-category matches).
func matchAOI(aoi security.AreaOfInterest, gt groundTruthAOI) (fileMatch, lineMatch, categoryMatch bool) {
	fileMatch = aoi.File == gt.file
	if !fileMatch {
		return
	}

	lineMatch = aoi.Line >= gt.lineRange[0] && aoi.Line <= gt.lineRange[1]
	if aoi.EndLine > 0 {
		// Also match if the AOI range overlaps with ground truth range
		lineMatch = lineMatch || (aoi.EndLine >= gt.lineRange[0] && aoi.Line <= gt.lineRange[1])
	}

	categoryMatch = aoi.Category.String() == gt.category
	return
}

func TestAOIModelComparison(t *testing.T) {
	cfg := loadTestConfig(t)
	if cfg == nil {
		t.Skip("PRR_LIVE_TESTS=1 not set or no valid config — skipping live AOI model comparison test")
	}

	models := parseModelsFromEnv(cfg)
	if models == nil {
		models = defaultModels(cfg)
	}

	diffs, groundTruth := securityTestDiffs()

	mustFindCount := 0
	niceFindCount := 0
	for _, gt := range groundTruth {
		if gt.importance == "must-find" {
			mustFindCount++
		} else {
			niceFindCount++
		}
	}

	t.Logf("Ground truth: %d must-find + %d nice-to-find = %d total AOIs across %d files",
		mustFindCount, niceFindCount, len(groundTruth), len(diffs))
	t.Logf("Testing %d model configurations\n", len(models))

	// ── Run each model ────────────────────────────────────────────────
	results := make([]modelResult, len(models))

	for i, spec := range models {
		t.Run(spec.name, func(t *testing.T) {
			provider, err := createProvider(spec)
			if err != nil {
				t.Fatalf("createProvider: %v", err)
			}

			tracker := &ai.UsageTracker{}
			// Enable verbose debug logging for this detailed run to capture HTTP/debug info.
			client := ai.NewAgent(provider, nil, ai.WithUsageTracker(tracker), ai.WithDebugLogger(os.Stderr))

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			start := time.Now()
			report, err := security.ScanAreasOfInterest(ctx, client, diffs, nil, func(status string) {
				t.Logf("  [%s] %s", spec.name, status)
			})
			elapsed := time.Since(start)

			usage := tracker.Snapshot()
			result := modelResult{
				spec:          spec,
				report:        report,
				duration:      elapsed,
				err:           err,
				mustFindTotal: mustFindCount,
				niceFindTotal: niceFindCount,
				inputTokens:   usage.InputTokens,
				outputTokens:  usage.OutputTokens,
				cost:          config.EstimateCost(spec.provider+"/"+spec.model, usage.InputTokens, usage.OutputTokens),
			}

			if err != nil {
				t.Logf("  ERROR: %v (%.1fs)", err, elapsed.Seconds())
				results[i] = result
				return
			}

			result.totalAOIs = report.TotalAOIs

			// ── Score: each ground truth is an independent assertion. ──
			// A GT is "hit" if ANY model AOI overlaps it (file + line).
			// A model AOI is a "false alarm" if it overlaps no GT at all.
			// Slots are not consumed — overlapping GTs both get credited
			// when one AOI satisfies both, which is the correct semantics
			// for "did the model find each expected issue?"
			gtMatched := make([]bool, len(groundTruth))
			for gi, gt := range groundTruth {
				for _, fileResult := range report.Files {
					for _, aoi := range fileResult.AreasOfInterest {
						fileOK, lineOK, _ := matchAOI(aoi, gt)
						if fileOK && lineOK {
							gtMatched[gi] = true
							break
						}
					}
					if gtMatched[gi] {
						break
					}
				}
			}

			for _, fileResult := range report.Files {
				for _, aoi := range fileResult.AreasOfInterest {
					matched := false
					for _, gt := range groundTruth {
						fileOK, lineOK, _ := matchAOI(aoi, gt)
						if fileOK && lineOK {
							matched = true
							break
						}
					}
					if !matched {
						result.falseAlarms++
					}
				}
			}

			for gi, gt := range groundTruth {
				if gtMatched[gi] {
					if gt.importance == "must-find" {
						result.mustFindHits++
					} else {
						result.niceFindHits++
					}
				}
			}

			results[i] = result

			t.Logf("  Must-find: %d/%d | Nice-to-find: %d/%d | False alarms: %d | Total AOIs: %d | Tokens: %d in + %d out | Cost: $%.4f | Time: %.1fs",
				result.mustFindHits, result.mustFindTotal,
				result.niceFindHits, result.niceFindTotal,
				result.falseAlarms, result.totalAOIs,
				result.inputTokens, result.outputTokens,
				result.cost,
				elapsed.Seconds())

			// Log missed must-finds
			for gi, gt := range groundTruth {
				if !gtMatched[gi] && gt.importance == "must-find" {
					t.Logf("  MISSED (must-find): %s:%d-%d [%s] %s",
						gt.file, gt.lineRange[0], gt.lineRange[1], gt.category, gt.desc)
				}
			}
		})
	}

	// ── Print comparison table ────────────────────────────────────────
	t.Log("")
	t.Log("══════════════════════════════════════════════════════════════════════════════════════════════════════════════════")
	t.Log("  AOI MODEL COMPARISON RESULTS")
	t.Log("══════════════════════════════════════════════════════════════════════════════════════════════════════════════════")
	t.Log("")
	t.Logf("  %-40s %7s %7s %5s %5s %7s %7s %7s %9s %7s",
		"Model", "Must", "Nice", "FP", "AOIs", "Recall", "In Tok", "Out Tok", "Cost", "Time")
	t.Logf("  %-40s %7s %7s %5s %5s %7s %7s %7s %9s %7s",
		strings.Repeat("─", 40), "───────", "───────", "─────", "─────", "───────", "───────", "───────", "─────────", "───────")

	for _, r := range results {
		if r.err != nil {
			t.Logf("  %-40s   ERROR: %v", r.spec.name, r.err)
			continue
		}

		recall := float64(0)
		totalGT := r.mustFindTotal + r.niceFindTotal
		if totalGT > 0 {
			recall = float64(r.mustFindHits+r.niceFindHits) / float64(totalGT) * 100
		}

		t.Logf("  %-40s %3d/%-3d %3d/%-3d %5d %5d  %5.1f%% %6dk %6dk  $%.4f %6.1fs",
			r.spec.name,
			r.mustFindHits, r.mustFindTotal,
			r.niceFindHits, r.niceFindTotal,
			r.falseAlarms,
			r.totalAOIs,
			recall,
			r.inputTokens/1000, r.outputTokens/1000,
			r.cost,
			r.duration.Seconds())
	}

	t.Log("")
	t.Log("  Must   = must-find hits / total must-finds")
	t.Log("  Nice   = nice-to-find hits / total nice-to-finds")
	t.Log("  FP     = false alarms (AOIs not matching any ground truth)")
	t.Log("  AOIs   = total AOIs reported by the model")
	t.Log("  Recall = (must+nice hits) / total ground truth")
	t.Log("  In/Out = input/output tokens (thousands)")
	t.Log("  Cost   = estimated USD (standard tier pricing)")
	t.Log("")

	// ── Determine winner ─────────────────────────────────────────────
	bestIdx := -1
	bestScore := -1.0
	for i, r := range results {
		if r.err != nil {
			continue
		}
		// Score: must-find recall * 2 + nice-find recall - FP penalty
		score := float64(r.mustFindHits)*2.0 + float64(r.niceFindHits) - float64(r.falseAlarms)*0.3
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	if bestIdx >= 0 {
		w := results[bestIdx]
		t.Logf("  RECOMMENDED: %s", w.spec.name)
		t.Logf("    Model: %s | Thinking: %d | Temp: %.1f",
			w.spec.model, w.spec.thinkingBudget, w.spec.temperature)
		t.Logf("    Must-find recall: %d/%d (%.0f%%)",
			w.mustFindHits, w.mustFindTotal,
			float64(w.mustFindHits)/float64(w.mustFindTotal)*100)
		t.Logf("    Tokens: %dk in + %dk out | Cost: $%.4f | Time: %.1fs",
			w.inputTokens/1000, w.outputTokens/1000, w.cost, w.duration.Seconds())
	}

	t.Log("═══════════════════════════════════════════════════════════════════════════════════════════")

	// ── Export benchmark results to ~/.config/prr/benchmark.json ──────
	benchmarks := &config.BenchmarkResults{
		Version:   1,
		Timestamp: time.Now(),
	}
	for _, r := range results {
		if r.err != nil {
			continue
		}
		totalGT := r.mustFindTotal + r.niceFindTotal
		recallPct := float64(0)
		if totalGT > 0 {
			recallPct = float64(r.mustFindHits+r.niceFindHits) / float64(totalGT) * 100
		}
		mustFindPct := float64(0)
		if r.mustFindTotal > 0 {
			mustFindPct = float64(r.mustFindHits) / float64(r.mustFindTotal) * 100
		}
		benchmarks.Models = append(benchmarks.Models, config.ModelBenchmark{
			ModelID:        r.spec.model,
			Provider:       r.spec.provider,
			ConfigName:     r.spec.name,
			Temperature:    r.spec.temperature,
			ThinkingBudget: r.spec.thinkingBudget,
			RecallPct:      recallPct,
			MustFindPct:    mustFindPct,
			LatencyMs:      int(r.duration.Milliseconds()),
			CostPerScan:    r.cost,
			TotalAOIs:      r.totalAOIs,
			FalseAlarms:    r.falseAlarms,
		})
	}
	if err := config.SaveBenchmarkResults("aoi", benchmarks); err != nil {
		// Treat as a test failure rather than a Logf — silently
		// losing benchmark output makes "everything passes" reads
		// of the suite misleading, and a broken persistence layer
		// is exactly the kind of regression this test ought to catch.
		t.Errorf("failed to save benchmark results: %v", err)
	} else {
		p, _ := config.BenchmarkArchivePath("aoi", benchmarks.Timestamp)
		t.Logf("  Benchmark results saved to %s", p)
	}
}

// TestAOIModelComparison_DetailedOutput runs a single model and prints
// the full AOI output for manual inspection. Useful for prompt tuning.
func TestAOIModelComparison_DetailedOutput(t *testing.T) {
	cfg := loadTestConfig(t)
	if cfg == nil {
		t.Skip("PRR_LIVE_TESTS=1 not set or no valid config — skipping live AOI detail test")
	}

	envModel := os.Getenv("PRR_AOI_MODEL")
	fastRef, _ := config.ParseModelRef(cfg.FastModel)

	// Resolve to provider/model. PRR_AOI_MODEL may be "provider/model-id"
	// (cross-provider) or a bare model-id (uses the fast-model provider).
	providerName := fastRef.Provider
	modelID := fastRef.ModelID
	displayName := cfg.FastModel
	if envModel != "" {
		if ref, err := config.ParseModelRef(envModel); err == nil {
			providerName = ref.Provider
			modelID = ref.ModelID
		} else {
			modelID = envModel
		}
		displayName = envModel
	}

	pc := cfg.ProviderConfigFor(providerName)

	diffs, groundTruth := securityTestDiffs()

	spec := newSpec(displayName, modelID, providerName, pc.APIKey, pc.BaseURL)
	provider, err := createProvider(spec)
	if err != nil {
		t.Fatalf("createProvider: %v", err)
	}

	tracker := &ai.UsageTracker{}
	client := ai.NewAgent(provider, nil, ai.WithUsageTracker(tracker))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	start := time.Now()
	report, err := security.ScanAreasOfInterest(ctx, client, diffs, nil, func(status string) {
		t.Logf("[progress] %s", status)
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AOI scan failed: %v", err)
	}

	usage := tracker.Snapshot()
	cost := config.EstimateCost(spec.provider+"/"+modelID, usage.InputTokens, usage.OutputTokens)
	t.Logf("\nModel: %s | Time: %.1fs | Total AOIs: %d | Tokens: %d in + %d out | Cost: $%.4f\n",
		displayName, elapsed.Seconds(), report.TotalAOIs,
		usage.InputTokens, usage.OutputTokens, cost)

	// Print each file's AOIs
	for _, fileResult := range report.Files {
		t.Logf("── %s ──", fileResult.File)

		if len(fileResult.AreasOfInterest) == 0 {
			t.Logf("   (no AOIs)")
			continue
		}

		for _, aoi := range fileResult.AreasOfInterest {
			lineStr := fmt.Sprintf("L%d", aoi.Line)
			if aoi.EndLine > 0 && aoi.EndLine != aoi.Line {
				lineStr = fmt.Sprintf("L%d-%d", aoi.Line, aoi.EndLine)
			}

			// Check against ground truth
			gtMatch := ""
			for _, gt := range groundTruth {
				fileOK, lineOK, catOK := matchAOI(aoi, gt)
				if fileOK && lineOK {
					if catOK {
						gtMatch = fmt.Sprintf(" [GT-MATCH: %s]", gt.desc)
					} else {
						gtMatch = fmt.Sprintf(" [GT-MATCH(cat=%s): %s]", gt.category, gt.desc)
					}
					break
				}
			}

			t.Logf("   [%s] %s (%s, %s): %s%s",
				aoi.Category, lineStr, aoi.Confidence,
				truncate(aoi.Snippet, 60),
				aoi.Reasoning, gtMatch)
		}
		t.Log("")
	}

	// Print the security digest
	t.Log("── SECURITY DIGEST (injected into review prompts) ──")
	t.Log(report.SecurityDigest)
}

// contextLineDiffs returns three versions of the same diffs at -U3, -U5, and
// -U10. The vulnerabilities are the same, but with U3 the user-input source
// is often invisible — the model only sees the sink. U5 partially reveals
// sources that sit just outside the U3 window. U10 covers all sources used
// by the ground-truth fixtures, enabling full data-flow analysis.
//
// Scenario: existing Go API server. The PR modifies handler functions,
// adding vulnerable code. The source (r.URL.Query, r.FormValue, etc.) was
// written in an earlier commit and only appears as context.
func contextLineDiffs() (u3 map[string]string, u5 map[string]string, u10 map[string]string, gt []groundTruthAOI) {
	// ── internal/api/handler.go ─────────────────────────────────────
	// The file has ~50 lines. The PR changes lines 18-19 (the SQL query)
	// and lines 27-28 (the response write). The user input source is at
	// line 10 (query := r.URL.Query().Get("q")).
	//
	// With U3: you see lines 15-22 around the SQL change — line 10 is NOT visible.
	// With U10: you see lines 8-29 — line 10 IS visible.

	u3 = map[string]string{
		"internal/api/handler.go": `@@ -15,7 +15,7 @@ func SearchHandler(w http.ResponseWriter, r *http.Request) {
 
 	// Execute the search
 	sqlStr := "SELECT * FROM users WHERE name = ?"
-	rows, err := db.Query(sqlStr, query)
+	rows, err := db.Query("SELECT * FROM users WHERE name = '" + query + "'")
 	if err != nil {
 		http.Error(w, err.Error(), 500)
 		return
@@ -25,7 +25,7 @@ func SearchHandler(w http.ResponseWriter, r *http.Request) {
 
 	// Render results
 	for rows.Next() {
-		renderUserRow(w, rows)
+		fmt.Fprintf(w, "<h1>Results for: %s</h1>", query)
 	}
 }
 
@@ -40,7 +40,8 @@ func ExecHandler(w http.ResponseWriter, r *http.Request) {
 	}
 
 	// Run the validated command
-	out, err := exec.Command(allowed[cmd]).Output()
+	// TODO: switch back to allowlist after testing
+	out, err := exec.Command("sh", "-c", cmd).Output()
 	if err != nil {
 		http.Error(w, "command failed", 500)
 		return`,

		"internal/auth/token.go": `@@ -12,9 +12,9 @@ import (
 	"time"
 )
 
-const secretKey = os.Getenv("AUTH_SECRET")
+const secretKey = "super-secret-key-12345"
 
-// HashPassword hashes a password using bcrypt
+// HashPassword hashes a password
 func HashPassword(password string) string {
-	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
-	return string(hash)
+	h := md5.Sum([]byte(password))
+	return hex.EncodeToString(h[:])
 }`,

		"internal/config/settings.go": `@@ -7,7 +7,7 @@ import (
 )

 // FetchConfig fetches configuration from the config server
-func FetchConfig() (map[string]interface{}, error) {
-	resp, err := http.Get("https://config.internal.example.com/api/v1/config")
+func FetchConfig(configURL string) (map[string]interface{}, error) {
+	resp, err := http.Get(configURL)
 	if err != nil {
 		return nil, err
 	}`,
	}

	// U5: 5 lines of context. For handler.go this is enough to reveal the
	// SQL/XSS source (`query := r.URL.Query()`) because both sinks sit
	// within 5 lines of that assignment, so the U3 hunks merge into one.
	// The exec sink still misses its source — `cmd := r.FormValue("cmd")`
	// is >5 lines away — so we expect U5 to partially close the gap.
	u5 = map[string]string{
		"internal/api/handler.go": `@@ -13,21 +13,21 @@ var db *sql.DB
 // SearchHandler handles user search requests
 func SearchHandler(w http.ResponseWriter, r *http.Request) {
 	query := r.URL.Query().Get("q")

 	// Execute the search
 	sqlStr := "SELECT * FROM users WHERE name = ?"
-	rows, err := db.Query(sqlStr, query)
+	rows, err := db.Query("SELECT * FROM users WHERE name = '" + query + "'")
 	if err != nil {
 		http.Error(w, err.Error(), 500)
 		return
 	}
 	defer rows.Close()

 	// Render results
 	for rows.Next() {
-		renderUserRow(w, rows)
+		fmt.Fprintf(w, "<h1>Results for: %s</h1>", query)
 	}
 }

@@ -38,11 +38,12 @@ func ExecHandler(w http.ResponseWriter, r *http.Request) {

 	// Validate against allowlist
 	allowed := map[string]string{
 		"status": "/usr/bin/status",
 		"health": "/usr/bin/health",
 	}

 	// Run the validated command
-	out, err := exec.Command(allowed[cmd]).Output()
+	// TODO: switch back to allowlist after testing
+	out, err := exec.Command("sh", "-c", cmd).Output()
 	if err != nil {
 		http.Error(w, "command failed", 500)
 		return`,

		"internal/auth/token.go": `@@ -10,11 +10,11 @@ import (
 	"log"
 	"os"
 	"time"
 )

-const secretKey = os.Getenv("AUTH_SECRET")
+const secretKey = "super-secret-key-12345"

-// HashPassword hashes a password using bcrypt
+// HashPassword hashes a password
 func HashPassword(password string) string {
-	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
-	return string(hash)
+	h := md5.Sum([]byte(password))
+	return hex.EncodeToString(h[:])
 }`,

		"internal/config/settings.go": `@@ -5,9 +5,9 @@ import (
 	"net/http"
 )

 // FetchConfig fetches configuration from the config server
-func FetchConfig() (map[string]interface{}, error) {
-	resp, err := http.Get("https://config.internal.example.com/api/v1/config")
+func FetchConfig(configURL string) (map[string]interface{}, error) {
+	resp, err := http.Get(configURL)
 	if err != nil {
 		return nil, err
 	}
 	defer resp.Body.Close()`,
	}

	u10 = map[string]string{
		"internal/api/handler.go": `@@ -5,27 +5,27 @@ import (
 	"database/sql"
 	"fmt"
 	"net/http"
 	"os/exec"
 )
 
 var db *sql.DB
 
 // SearchHandler handles user search requests
 func SearchHandler(w http.ResponseWriter, r *http.Request) {
 	query := r.URL.Query().Get("q")
 
 	// Execute the search
 	sqlStr := "SELECT * FROM users WHERE name = ?"
-	rows, err := db.Query(sqlStr, query)
+	rows, err := db.Query("SELECT * FROM users WHERE name = '" + query + "'")
 	if err != nil {
 		http.Error(w, err.Error(), 500)
 		return
 	}
 	defer rows.Close()
 
 	// Render results
 	for rows.Next() {
-		renderUserRow(w, rows)
+		fmt.Fprintf(w, "<h1>Results for: %s</h1>", query)
 	}
 }
 
@@ -33,14 +33,15 @@ func ExecHandler(w http.ResponseWriter, r *http.Request) {
 	cmd := r.FormValue("cmd")
 
 	// Validate against allowlist
 	allowed := map[string]string{
 		"status": "/usr/bin/status",
 		"health": "/usr/bin/health",
 	}
 
 	// Run the validated command
-	out, err := exec.Command(allowed[cmd]).Output()
+	// TODO: switch back to allowlist after testing
+	out, err := exec.Command("sh", "-c", cmd).Output()
 	if err != nil {
 		http.Error(w, "command failed", 500)
 		return`,

		"internal/auth/token.go": `@@ -2,19 +2,19 @@ package auth
 
 import (
 	"crypto/md5"
 	"encoding/hex"
 	"fmt"
 	"log"
-	"os"
 	"time"
-	"golang.org/x/crypto/bcrypt"
 )
 
-const secretKey = os.Getenv("AUTH_SECRET")
+const secretKey = "super-secret-key-12345"
 
-// HashPassword hashes a password using bcrypt
+// HashPassword hashes a password
 func HashPassword(password string) string {
-	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
-	return string(hash)
+	h := md5.Sum([]byte(password))
+	return hex.EncodeToString(h[:])
 }`,

		"internal/config/settings.go": `@@ -1,17 +1,17 @@ package config
 
 import (
 	"encoding/json"
 	"net/http"
 )
 
 // FetchConfig fetches configuration from the config server
-func FetchConfig() (map[string]interface{}, error) {
-	resp, err := http.Get("https://config.internal.example.com/api/v1/config")
+func FetchConfig(configURL string) (map[string]interface{}, error) {
+	resp, err := http.Get(configURL)
 	if err != nil {
 		return nil, err
 	}
 	defer resp.Body.Close()
 
 	var cfg map[string]interface{}
 	json.NewDecoder(resp.Body).Decode(&cfg)`,
	}

	gt = []groundTruthAOI{
		// handler.go — SQL injection: source (query from r.URL.Query) only visible with U10
		// Line ranges widened to accommodate both hunk offset styles
		{file: "internal/api/handler.go", lineRange: [2]int{14, 22}, category: "sql", importance: "must-find", desc: "SQL injection via string concat (source visible only in U10)"},
		// handler.go — XSS: source (query) only visible with U10
		{file: "internal/api/handler.go", lineRange: [2]int{22, 30}, category: "xss", importance: "must-find", desc: "XSS via fmt.Fprintf with user input (source visible only in U10)"},
		// handler.go — Command injection: source (cmd from r.FormValue) visible in U10 only
		{file: "internal/api/handler.go", lineRange: [2]int{33, 48}, category: "exec", importance: "must-find", desc: "Command injection via exec.Command (source visible only in U10)"},
		// token.go — Hardcoded secret (visible in both U3 and U10)
		{file: "internal/auth/token.go", lineRange: [2]int{5, 16}, category: "secrets", importance: "must-find", desc: "Hardcoded secret key (visible in both U3 and U10)"},
		// token.go — MD5 for password hashing (visible in both)
		{file: "internal/auth/token.go", lineRange: [2]int{14, 22}, category: "crypto", importance: "must-find", desc: "MD5 used for password hashing (visible in both U3 and U10)"},
		// settings.go — SSRF: with U3 it's less clear configURL is user-controlled
		{file: "internal/config/settings.go", lineRange: [2]int{5, 12}, category: "network", importance: "must-find", desc: "SSRF via http.Get with user-controlled URL"},
	}

	return u3, u5, u10, gt
}

// TestAOIContextLineComparison tests whether having more context lines (U10
// vs U3) improves the model's ability to detect vulnerabilities where the
// source (user input) is not in the changed lines but in surrounding context.
func TestAOIContextLineComparison(t *testing.T) {
	cfg := loadTestConfig(t)
	if cfg == nil {
		t.Skip("PRR_LIVE_TESTS=1 not set or no valid config — skipping context line comparison test")
	}

	u3Diffs, u5Diffs, u10Diffs, groundTruth := contextLineDiffs()

	mustFindCount := 0
	niceFindCount := 0
	for _, gt := range groundTruth {
		if gt.importance == "must-find" {
			mustFindCount++
		} else {
			niceFindCount++
		}
	}

	// Models to test — use env override or a focused subset
	fastRef, _ := config.ParseModelRef(cfg.FastModel)
	pc := cfg.ProviderConfigFor(fastRef.Provider)

	models := parseModelsFromEnv(cfg)
	if models == nil {
		models = []modelSpec{
			newSpec("3.1-flash-lite", "gemini-3.1-flash-lite", fastRef.Provider, pc.APIKey, pc.BaseURL),
		}
	}

	type contextResult struct {
		modelName string
		context   string // "U3", "U5", or "U10"
		result    modelResult
	}
	var allResults []contextResult

	for _, spec := range models {
		for _, tc := range []struct {
			label string
			diffs map[string]string
		}{
			{"U3", u3Diffs},
			{"U5", u5Diffs},
			{"U10", u10Diffs},
		} {
			testName := fmt.Sprintf("%s/%s", spec.name, tc.label)
			t.Run(testName, func(t *testing.T) {
				provider, err := createProvider(spec)
				if err != nil {
					t.Fatalf("createProvider: %v", err)
				}

				tracker := &ai.UsageTracker{}
				client := ai.NewAgent(provider, nil, ai.WithUsageTracker(tracker))

				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()

				start := time.Now()
				report, err := security.ScanAreasOfInterest(ctx, client, tc.diffs, nil, func(status string) {
					t.Logf("  [%s/%s] %s", spec.name, tc.label, status)
				})
				elapsed := time.Since(start)

				usage := tracker.Snapshot()
				r := modelResult{
					spec:          spec,
					report:        report,
					duration:      elapsed,
					err:           err,
					mustFindTotal: mustFindCount,
					niceFindTotal: niceFindCount,
					inputTokens:   usage.InputTokens,
					outputTokens:  usage.OutputTokens,
					cost:          config.EstimateCost(spec.provider+"/"+spec.model, usage.InputTokens, usage.OutputTokens),
				}

				if err != nil {
					t.Logf("  ERROR: %v", err)
					allResults = append(allResults, contextResult{spec.name, tc.label, r})
					return
				}

				r.totalAOIs = report.TotalAOIs

				// Score: independent per-GT assertion (see TestAOIModelComparison
				// for full rationale).
				gtMatched := make([]bool, len(groundTruth))
				for gi, gt := range groundTruth {
					for _, fileResult := range report.Files {
						for _, aoi := range fileResult.AreasOfInterest {
							fileOK, lineOK, _ := matchAOI(aoi, gt)
							if fileOK && lineOK {
								gtMatched[gi] = true
								break
							}
						}
						if gtMatched[gi] {
							break
						}
					}
				}

				for _, fileResult := range report.Files {
					for _, aoi := range fileResult.AreasOfInterest {
						matched := false
						for _, gt := range groundTruth {
							fileOK, lineOK, _ := matchAOI(aoi, gt)
							if fileOK && lineOK {
								matched = true
								break
							}
						}
						if !matched {
							r.falseAlarms++
						}
					}
				}

				for gi, gt := range groundTruth {
					if gtMatched[gi] {
						if gt.importance == "must-find" {
							r.mustFindHits++
						} else {
							r.niceFindHits++
						}
					}
				}

				allResults = append(allResults, contextResult{spec.name, tc.label, r})

				t.Logf("  Must: %d/%d | Nice: %d/%d | FP: %d | Cost: $%.4f | Time: %.1fs",
					r.mustFindHits, r.mustFindTotal,
					r.niceFindHits, r.niceFindTotal,
					r.falseAlarms, r.cost, elapsed.Seconds())

				// Log missed must-finds
				for gi, gt := range groundTruth {
					if !gtMatched[gi] && gt.importance == "must-find" {
						t.Logf("  MISSED: %s:%d-%d [%s] %s",
							gt.file, gt.lineRange[0], gt.lineRange[1], gt.category, gt.desc)
					}
				}
			})
		}
	}

	// ── Comparison table ─────────────────────────────────────────────
	t.Log("")
	t.Log("══════════════════════════════════════════════════════════════════════════════════════════════")
	t.Log("  CONTEXT LINE COMPARISON: U3 (default git) vs U5 vs U10 (extra context)")
	t.Log("══════════════════════════════════════════════════════════════════════════════════════════════")
	t.Log("")
	t.Logf("  %-30s %4s %7s %7s %5s %5s %9s %7s",
		"Model", "Ctx", "Must", "Nice", "FP", "AOIs", "Cost", "Time")
	t.Logf("  %-30s %4s %7s %7s %5s %5s %9s %7s",
		strings.Repeat("─", 30), "────", "───────", "───────", "─────", "─────", "─────────", "───────")

	for _, cr := range allResults {
		r := cr.result
		if r.err != nil {
			t.Logf("  %-30s %4s   ERROR: %v", cr.modelName, cr.context, r.err)
			continue
		}
		t.Logf("  %-30s %4s %3d/%-3d %3d/%-3d %5d %5d  $%.4f %6.1fs",
			cr.modelName, cr.context,
			r.mustFindHits, r.mustFindTotal,
			r.niceFindHits, r.niceFindTotal,
			r.falseAlarms, r.totalAOIs,
			r.cost, r.duration.Seconds())
	}

	t.Log("")
	t.Log("  Key insight: vulnerabilities where the user-input SOURCE is outside")
	t.Log("  the changed lines should be detected more often with U10 context.")
	t.Log("  Items 1-3 (SQL injection, XSS, command injection) have the source")
	t.Log("  at line 10-11 which is only visible with U10 context.")
	t.Log("  Items 4-6 (hardcoded secret, MD5, SSRF) are visible in both.")
	t.Log("")
	t.Log("══════════════════════════════════════════════════════════════════════════════════════════════")
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

package review_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/review"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// ── Deep Review Model Comparison Test ─────────────────────────────────────
//
// Compares thinking models (gemini-3.1-pro-preview, claude-opus-4.6, gpt-5.4)
// on the Phase 3 deep review task using a fixed set of synthetic source files
// with known security issues (ground truth).
//
// The test:
//   1. Creates a temp git repo with synthetic Go source files.
//   2. Runs AOI scanning to identify areas of interest.
//   3. Routes AOIs into ReviewCalls (same as production pipeline).
//   4. For each model config, runs RunReviewCalls with full tool access.
//   5. Scores findings against ground truth.
//
// Run with:
//   PRR_LIVE_TESTS=1 go test ./internal/review/ -run TestDeepReviewModelComparison -v -timeout 30m
//
// Override models:
//   PRR_DEEP_MODELS="gemini-3.1-pro-preview,claude-opus-4.6" PRR_LIVE_TESTS=1 go test ...

// ── Ground truth for deep review scoring ──────────────────────────────────

type deepGroundTruth struct {
	file       string
	lineRange  [2]int // [startLine, endLine]
	severity   string // expected severity: "critical", "high", "medium", "low"
	category   string
	desc       string
	importance string // "must-find" or "nice-to-find"
}

func deepReviewGroundTruth() []deepGroundTruth {
	return []deepGroundTruth{
		// critical
		{file: "internal/api/handler.go", lineRange: [2]int{15, 22}, severity: "critical", category: "input-validation", importance: "must-find", desc: "SQL injection via string concatenation"},
		{file: "internal/api/handler.go", lineRange: [2]int{30, 38}, severity: "critical", category: "input-validation", importance: "must-find", desc: "Command injection via exec.Command"},
		{file: "internal/api/handler.go", lineRange: [2]int{49, 57}, severity: "high", category: "input-validation", importance: "must-find", desc: "Server-side template injection"},

		// high
		{file: "internal/api/handler.go", lineRange: [2]int{25, 26}, severity: "high", category: "input-validation", importance: "must-find", desc: "XSS via fmt.Fprintf with user input"},
		{file: "internal/api/handler.go", lineRange: [2]int{42, 45}, severity: "high", category: "authorization", importance: "must-find", desc: "Admin endpoint without authentication"},
		{file: "internal/api/redirect.go", lineRange: [2]int{11, 14}, severity: "high", category: "input-validation", importance: "must-find", desc: "Open redirect"},
		{file: "internal/api/redirect.go", lineRange: [2]int{18, 28}, severity: "high", category: "input-validation", importance: "must-find", desc: "Path traversal"},
		{file: "internal/config/settings.go", lineRange: [2]int{9, 11}, severity: "high", category: "external-io", importance: "must-find", desc: "SSRF via http.Get"},

		// medium
		{file: "internal/auth/token.go", lineRange: [2]int{11, 11}, severity: "medium", category: "configuration", importance: "must-find", desc: "Hardcoded secret key"},
		{file: "internal/auth/token.go", lineRange: [2]int{15, 17}, severity: "medium", category: "cryptography", importance: "must-find", desc: "MD5 for password hashing"},
		{file: "internal/auth/token.go", lineRange: [2]int{22, 24}, severity: "medium", category: "cryptography", importance: "nice-to-find", desc: "Predictable token generation"},
		{file: "internal/auth/token.go", lineRange: [2]int{29, 30}, severity: "medium", category: "data-integrity", importance: "nice-to-find", desc: "Token logged in plaintext"},
		{file: "internal/auth/token.go", lineRange: [2]int{32, 34}, severity: "low", category: "cryptography", importance: "nice-to-find", desc: "Non-constant-time comparison"},
	}
}

// ── Synthetic source files (same as AOI benchmark, but full file content) ──

func syntheticSourceFiles() map[string]string {
	return map[string]string{
		"go.mod": `module example.com/app

go 1.22

require github.com/some/dep v1.0.0
`,
		"internal/api/handler.go": `package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"os/exec"
	"html/template"
)

var db *sql.DB

// SearchHandler handles user search requests
func SearchHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	
	// BUG: SQL injection — string concatenation in query
	rows, err := db.Query("SELECT * FROM users WHERE name = '" + query + "'")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	
	// BUG: XSS — writing user input directly to response
	fmt.Fprintf(w, "<h1>Results for: %s</h1>", query)
}

// ExecHandler runs a command based on user input
func ExecHandler(w http.ResponseWriter, r *http.Request) {
	cmd := r.FormValue("cmd")
	
	// BUG: Command injection — user input in exec.Command
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		http.Error(w, "command failed: " + err.Error(), 500)
		return
	}
	w.Write(out)
}

// AdminHandler — no auth check
func AdminHandler(w http.ResponseWriter, r *http.Request) {
	// BUG: Missing authentication — admin endpoint with no auth
	w.Write([]byte("admin panel"))
}

// TemplateHandler renders user-controlled template
func TemplateHandler(w http.ResponseWriter, r *http.Request) {
	tmplStr := r.FormValue("template")
	
	// BUG: Server-side template injection
	t, err := template.New("user").Parse(tmplStr)
	if err != nil {
		http.Error(w, "bad template", 400)
		return
	}
	t.Execute(w, nil)
}
`,
		"internal/api/redirect.go": `package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// RedirectHandler redirects to a user-provided URL
func RedirectHandler(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	// BUG: Open redirect — no validation of target URL
	http.Redirect(w, r, target, http.StatusFound)
}

// FileHandler serves files based on user input
func FileHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("file")
	// BUG: Path traversal — user controls file path
	path := filepath.Join("/data", name)
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	defer f.Close()
	io.Copy(w, f)
}
`,
		"internal/auth/token.go": `package auth

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"time"
)

const secretKey = "super-secret-key-12345"  // BUG: Hardcoded secret

// HashPassword hashes a password
func HashPassword(password string) string {
	// BUG: Weak hashing — MD5 is not suitable for passwords
	h := md5.Sum([]byte(password))
	return hex.EncodeToString(h[:])
}

// GenerateToken creates an auth token
func GenerateToken(userID string) string {
	// BUG: Predictable token — uses timestamp, not crypto/rand
	token := fmt.Sprintf("%s-%d", userID, time.Now().UnixNano())
	return HashPassword(token)
}

// ValidateToken checks a token (leaks info in logs)
func ValidateToken(token string) bool {
	// BUG: Logs sensitive data
	log.Printf("Validating token: %s", token)
	
	// BUG: Timing attack — non-constant-time comparison
	expected := GenerateToken("admin")
	return token == expected
}
`,
		"internal/config/settings.go": `package config

import (
	"encoding/json"
	"net/http"
)

// FetchConfig fetches configuration from a user-provided URL
func FetchConfig(configURL string) (map[string]interface{}, error) {
	// BUG: SSRF — fetching from user-controlled URL
	resp, err := http.Get(configURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	var cfg map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&cfg)
	return cfg, nil
}
`,
		"internal/util/helpers.go": `package util

import "strings"

// Capitalize capitalizes the first letter of a string.
func Capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Max returns the larger of two ints.
func Max(a, b int) int {
	if a > b { return a }; return b
}
`,
	}
}

// ── Temp git repo setup ───────────────────────────────────────────────────

func setupTempGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	// git init
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init")
	run("checkout", "-b", "main")

	// Write files and commit
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("add", "-A")
	run("commit", "-m", "initial")

	return dir
}

// ── Model spec for deep review ────────────────────────────────────────────

type deepModelSpec struct {
	name           string
	model          string
	provider       string
	apiKey         string
	baseURL        string
	thinkingBudget int
	temperature    float64
	maxOutput      int
}

// ── Scoring ───────────────────────────────────────────────────────────────

type deepModelResult struct {
	spec     deepModelSpec
	findings []state.DeepFinding
	duration time.Duration
	err      error

	mustFindTotal int
	mustFindHits  int
	niceFindTotal int
	niceFindHits  int
	falseAlarms   int
	totalFindings int

	// Severity accuracy: how many findings had the correct severity
	severityCorrect int
	severityTotal   int

	// Token usage
	inputTokens  int
	outputTokens int
	cachedTokens int // input tokens served from provider-side cache (Gemini cachedContentTokenCount)

	// Tool usage
	toolCalls int
}

func matchDeepFinding(f state.DeepFinding, gt deepGroundTruth) bool {
	if f.File != gt.file {
		return false
	}
	// Parse finding line range
	fStart, fEnd := parseLineRange(f.Lines)
	if fStart == 0 {
		return false
	}
	// Match if any overlap between finding range and ground truth range
	return fStart <= gt.lineRange[1] && fEnd >= gt.lineRange[0]
}

func parseLineRange(lines string) (int, int) {
	// Lines can be "18", "18-22", "L18", "L18-L22"
	s := strings.TrimPrefix(lines, "L")
	parts := strings.SplitN(s, "-", 2)
	var start, end int
	fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &start)
	if len(parts) == 2 {
		fmt.Sscanf(strings.TrimPrefix(strings.TrimSpace(parts[1]), "L"), "%d", &end)
	}
	if end == 0 {
		end = start
	}
	return start, end
}

var severityRanks = map[string]int{
	"critical": 0,
	"high":     1,
	"medium":   2,
	"low":      3,
}

func severityDistance(actual, expected string) int {
	a, ok1 := severityRanks[strings.ToLower(actual)]
	e, ok2 := severityRanks[strings.ToLower(expected)]
	if !ok1 || !ok2 {
		return 99
	}
	d := a - e
	if d < 0 {
		return -d
	}
	return d
}

func scoreDeepResult(findings []state.DeepFinding, groundTruth []deepGroundTruth) deepModelResult {
	var r deepModelResult
	r.totalFindings = len(findings)

	// Each ground truth is an independent assertion: "did ANY finding catch
	// this expected issue?" The old design consumed GT slots first-match-wins,
	// which silently underreported recall when GT entries overlapped (same
	// bug the AOI matcher had — see TestAOIModelComparison for details).
	gtHit := make([]bool, len(groundTruth))
	severityHitFinding := make([]*state.DeepFinding, len(groundTruth))
	for gi, gt := range groundTruth {
		for i := range findings {
			if matchDeepFinding(findings[i], gt) {
				gtHit[gi] = true
				severityHitFinding[gi] = &findings[i]
				break
			}
		}
	}

	for gi, gt := range groundTruth {
		if gt.importance == "must-find" {
			r.mustFindTotal++
		} else {
			r.niceFindTotal++
		}
		if !gtHit[gi] {
			continue
		}
		if gt.importance == "must-find" {
			r.mustFindHits++
		} else {
			r.niceFindHits++
		}
		// Severity accuracy: score the first finding that hit this GT.
		f := severityHitFinding[gi]
		r.severityTotal++
		if severityDistance(f.Severity, gt.severity) == 0 {
			r.severityCorrect++
		}
	}

	// False alarms: findings that overlap no GT at all.
	for i := range findings {
		matched := false
		for _, gt := range groundTruth {
			if matchDeepFinding(findings[i], gt) {
				matched = true
				break
			}
		}
		if !matched {
			r.falseAlarms++
		}
	}

	return r
}

// ── The benchmark ─────────────────────────────────────────────────────────

func TestDeepReviewModelComparison(t *testing.T) {
	if os.Getenv("PRR_LIVE_TESTS") != "1" {
		t.Skip("PRR_LIVE_TESTS=1 not set")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Skipf("no valid config: %v", err)
	}

	// Set up temp git repo with synthetic source files
	files := syntheticSourceFiles()
	repoDir := setupTempGitRepo(t, files)
	t.Logf("Temp repo: %s", repoDir)

	// Change to repo dir for tool execution
	origDir, _ := os.Getwd()
	os.Chdir(repoDir)
	defer os.Chdir(origDir)

	// Build diffs (simulate all files as new)
	diffs := make(map[string]string)
	for path, content := range files {
		lines := strings.Split(content, "\n")
		var diffLines []string
		diffLines = append(diffLines, fmt.Sprintf("@@ -0,0 +1,%d @@", len(lines)))
		for _, l := range lines {
			diffLines = append(diffLines, "+"+l)
		}
		diffs[path] = strings.Join(diffLines, "\n")
	}

	// Run AOI scan to get realistic AOIs
	t.Log("Running AOI scan to generate review inputs...")
	// AOI scan provider follows the configured fast model (override with
	// PRR_AOI_MODEL="provider/model-id") so the benchmark runs on any
	// provider — including keyless claude-code — not just Gemini.
	aoiModelRef := cfg.FastModel
	if env := os.Getenv("PRR_AOI_MODEL"); env != "" {
		aoiModelRef = env
	}
	aoiRef, err := config.ParseModelRef(aoiModelRef)
	if err != nil {
		t.Fatalf("AOI model ref %q: %v", aoiModelRef, err)
	}
	aoiKey := cfg.APIKeyFor(aoiRef.Provider)
	if !config.IsKeylessProvider(aoiRef.Provider) && aoiKey == "" {
		t.Skipf("no API key for AOI provider %q", aoiRef.Provider)
	}
	aoiModels, err := config.LoadModels()
	if err != nil {
		t.Fatalf("load model configs: %v", err)
	}
	aoiModelCfg := config.GetModelConfig(aoiModels, aoiRef.ModelID)
	aoiProvider, err := ai.NewProvider(ai.ProviderConfig{
		ProviderName:    aoiRef.Provider,
		ModelID:         aoiRef.ModelID,
		APIKey:          aoiKey,
		BaseURL:         cfg.ProviderConfigFor(aoiRef.Provider).BaseURL,
		ThinkingBudget:  aoiModelCfg.ThinkingBudget.Fast,
		Temperature:     ai.TempPtr(aoiModelCfg.Temperature),
		MaxOutputTokens: aoiModelCfg.MaxOutputTokens,
	})
	if err != nil {
		t.Fatalf("AOI provider: %v", err)
	}
	aoiAgent := ai.NewAgent(aoiProvider, nil)
	aoiReport, err := security.ScanAreasOfInterest(
		context.Background(),
		aoiAgent,
		diffs,
		nil,
		func(status string) { t.Logf("  AOI: %s", status) },
	)
	if err != nil {
		t.Fatalf("AOI scan failed: %v", err)
	}

	var totalAOIs int
	for _, r := range aoiReport.Files {
		totalAOIs += len(r.AreasOfInterest)
	}
	t.Logf("AOI scan found %d areas of interest", totalAOIs)

	// Allow filtering AOIs by category: PRR_DEEP_CATEGORY="cryptography"
	focusCategory := os.Getenv("PRR_DEEP_CATEGORY")

	// Route AOIs into review calls
	var filteredFiles []security.AOIScanResult
	for _, r := range aoiReport.Files {
		if focusCategory == "" {
			filteredFiles = append(filteredFiles, r)
			continue
		}
		var filtered security.AOIScanResult
		filtered.File = r.File
		for _, aoi := range r.AreasOfInterest {
			if strings.Contains(strings.ToLower(aoi.Category.String()), strings.ToLower(focusCategory)) {
				filtered.AreasOfInterest = append(filtered.AreasOfInterest, aoi)
			}
		}
		if len(filtered.AreasOfInterest) > 0 {
			filteredFiles = append(filteredFiles, filtered)
		}
	}

	var totalAOIsFiltered int
	for _, r := range filteredFiles {
		totalAOIsFiltered += len(r.AreasOfInterest)
	}
	if focusCategory != "" {
		t.Logf("Filtered to category %q: %d AOIs", focusCategory, totalAOIsFiltered)
	}

	routeResult := review.RouteAOIs(filteredFiles, nil, 10)
	allCalls := append(routeResult.Individual, routeResult.Grouped...)
	t.Logf("Routed into %d review calls (%d individual, %d grouped)",
		len(allCalls), routeResult.IndividualCount, routeResult.GroupedCount)

	// Define model configs to test
	groundTruth := deepReviewGroundTruth()
	if focusCategory != "" {
		var filtered []deepGroundTruth
		for _, gt := range groundTruth {
			if strings.Contains(strings.ToLower(gt.category), strings.ToLower(focusCategory)) {
				filtered = append(filtered, gt)
			}
		}
		groundTruth = filtered
	}
	specs := deepModelsFromConfig(cfg)
	if envSpecs := deepModelsFromEnv(cfg); len(envSpecs) > 0 {
		specs = envSpecs
	}

	// Concurrency levels to test: PRR_DEEP_CONCURRENCY="1,2,4,8"
	concurrencyLevels := []int{1}
	if envConc := os.Getenv("PRR_DEEP_CONCURRENCY"); envConc != "" {
		concurrencyLevels = nil
		for s := range strings.SplitSeq(envConc, ",") {
			s = strings.TrimSpace(s)
			var c int
			fmt.Sscanf(s, "%d", &c)
			if c > 0 {
				concurrencyLevels = append(concurrencyLevels, c)
			}
		}
	}

	t.Logf("Ground truth: %d must-find + %d nice-to-find = %d total",
		countImportance(groundTruth, "must-find"),
		countImportance(groundTruth, "nice-to-find"),
		len(groundTruth))
	t.Logf("Testing %d model configurations x %d concurrency levels", len(specs), len(concurrencyLevels))

	// Run each model x concurrency combo
	var results []deepModelResult

	for _, spec := range specs {
		for _, conc := range concurrencyLevels {
			concLabel := fmt.Sprintf("conc=%d", conc)
			testName := fmt.Sprintf("%s (%s)", spec.name, concLabel)
			concCopy := conc
			specCopy := spec
			t.Run(testName, func(t *testing.T) {
				provider, err := ai.NewProvider(ai.ProviderConfig{
					ProviderName:    specCopy.provider,
					ModelID:         specCopy.model,
					APIKey:          specCopy.apiKey,
					BaseURL:         specCopy.baseURL,
					MaxOutputTokens: specCopy.maxOutput,
					// This benchmark exists to compare temperature settings,
					// including the explicit-zero (greedy) case. Bypass
					// ai.TempPtr here — it folds 0 to nil, which silently
					// makes PRR_DEEP_TEMP="0.0" a no-op against the
					// provider default.
					Temperature:    &specCopy.temperature,
					ThinkingBudget: specCopy.thinkingBudget,
				})
				if err != nil {
					t.Fatalf("create provider: %v", err)
				}

				toolExec := &ai.ToolExecutor{
					HeadRef:  "HEAD",
					BaseRef:  "HEAD",
					RawDiffs: diffs,
				}
				agent := ai.NewAgent(provider, toolExec)

				var toolCalls int
				start := time.Now()

				execResult, err := review.RunReviewCalls(
					context.Background(),
					agent,
					allCalls,
					review.ExecuteOptions{
						Mode:           review.ModeAudit,
						ProjectContext: "Go web application with HTTP handlers, authentication, and configuration management.",
						MaxConcurrency: concCopy,
						OnProgress: func(completed, total int, cached bool, callErr error) {
							if callErr != nil {
								t.Logf("  Call %d/%d failed: %v", completed, total, callErr)
							} else {
								t.Logf("  Call %d/%d complete", completed, total)
							}
						},
						OnToolCall: func(idx int, name string, args string, status string, dur string) {
							if status == "start" {
								toolCalls++
							}
						},
					},
				)

				duration := time.Since(start)

				labeledSpec := specCopy
				labeledSpec.name = fmt.Sprintf("%s (conc=%d)", specCopy.name, concCopy)

				r := deepModelResult{spec: labeledSpec, duration: duration}
				if err != nil {
					r.err = err
					t.Logf("  ERROR: %v (%.1fs)", err, duration.Seconds())
				} else {
					r = scoreDeepResult(execResult.Findings, groundTruth)
					r.spec = labeledSpec
					r.duration = duration
					r.findings = execResult.Findings
					r.toolCalls = toolCalls

					// Token usage from provider
					usage := ai.SnapshotUsage(agent)
					r.inputTokens = usage.InputTokens
					r.outputTokens = usage.OutputTokens
					r.cachedTokens = usage.CacheHits

					recall := float64(0)
					totalGT := r.mustFindTotal + r.niceFindTotal
					if totalGT > 0 {
						recall = float64(r.mustFindHits+r.niceFindHits) / float64(totalGT) * 100
					}
					sevAcc := float64(0)
					if r.severityTotal > 0 {
						sevAcc = float64(r.severityCorrect) / float64(r.severityTotal) * 100
					}

					cost := config.EstimateCost(specCopy.provider+"/"+specCopy.model, r.inputTokens, r.outputTokens, r.cachedTokens)
					t.Logf("  Must: %d/%d | Nice: %d/%d | FP: %d | Findings: %d | Recall: %.1f%% | SevAcc: %.0f%% | Tools: %d | InTok: %d | OutTok: %d | Cost: $%.4f | Time: %.1fs",
						r.mustFindHits, r.mustFindTotal,
						r.niceFindHits, r.niceFindTotal,
						r.falseAlarms,
						r.totalFindings,
						recall, sevAcc,
						toolCalls,
						r.inputTokens, r.outputTokens,
						cost,
						duration.Seconds())

					// Print missed findings
					matched := make([]bool, len(groundTruth))
					for _, f := range execResult.Findings {
						for gi, gt := range groundTruth {
							if !matched[gi] && matchDeepFinding(f, gt) {
								matched[gi] = true
								break
							}
						}
					}
					for gi, gt := range groundTruth {
						if !matched[gi] {
							t.Logf("  MISSED (%s): %s:%d-%d [%s] %s",
								gt.importance, gt.file, gt.lineRange[0], gt.lineRange[1], gt.category, gt.desc)
						}
					}
				}

				results = append(results, r)
			})
		}
	}

	// ── Print comparison table ────────────────────────────────────────
	t.Log("")
	t.Log("══════════════════════════════════════════════════════════════════════════════════════════════════════════════════")
	t.Log("  DEEP REVIEW MODEL COMPARISON RESULTS")
	t.Log("══════════════════════════════════════════════════════════════════════════════════════════════════════════════════")
	t.Log("")
	t.Logf("  %-45s %7s %7s %5s %5s %7s %6s %6s %8s %8s %8s %7s",
		"Model", "Must", "Nice", "FP", "Find", "Recall", "SvAcc", "Tools", "InTok", "OutTok", "Cost", "Time")
	t.Logf("  %-45s %7s %7s %5s %5s %7s %6s %6s %8s %8s %8s %7s",
		strings.Repeat("─", 45), "───────", "───────", "─────", "─────", "───────", "──────", "──────", "────────", "────────", "────────", "───────")

	for _, r := range results {
		if r.err != nil {
			t.Logf("  %-45s   ERROR: %v", r.spec.name, r.err)
			continue
		}

		recall := float64(0)
		totalGT := r.mustFindTotal + r.niceFindTotal
		if totalGT > 0 {
			recall = float64(r.mustFindHits+r.niceFindHits) / float64(totalGT) * 100
		}
		sevAcc := float64(0)
		if r.severityTotal > 0 {
			sevAcc = float64(r.severityCorrect) / float64(r.severityTotal) * 100
		}

		cost := config.EstimateCost(r.spec.provider+"/"+r.spec.model, r.inputTokens, r.outputTokens, r.cachedTokens)
		t.Logf("  %-45s %3d/%-3d %3d/%-3d %5d %5d  %5.1f%% %5.0f%% %6d %8d %8d  $%6.4f %6.1fs",
			r.spec.name,
			r.mustFindHits, r.mustFindTotal,
			r.niceFindHits, r.niceFindTotal,
			r.falseAlarms,
			r.totalFindings,
			recall, sevAcc,
			r.toolCalls,
			r.inputTokens, r.outputTokens,
			cost,
			r.duration.Seconds())
	}

	t.Log("")
	t.Log("  Must   = must-find hits / total must-finds")
	t.Log("  Nice   = nice-to-find hits / total nice-to-finds")
	t.Log("  FP     = false alarms (findings not matching any ground truth)")
	t.Log("  Find   = total findings reported")
	t.Log("  Recall = (must+nice hits) / total ground truth")
	t.Log("  SvAcc  = severity accuracy (exact match %)")
	t.Log("  Tools  = total tool invocations")
	t.Log("")

	// Find best model
	var bestIdx int
	var bestScore float64
	for i, r := range results {
		if r.err != nil {
			continue
		}
		totalGT := r.mustFindTotal + r.niceFindTotal
		if totalGT == 0 {
			continue
		}
		recall := float64(r.mustFindHits+r.niceFindHits) / float64(totalGT) * 100
		// Score: recall - FP penalty - time penalty
		score := recall - float64(r.falseAlarms)*2 - r.duration.Seconds()*0.1
		if score > bestScore || i == 0 {
			bestScore = score
			bestIdx = i
		}
	}

	if len(results) > 0 && results[bestIdx].err == nil {
		best := results[bestIdx]
		t.Logf("  RECOMMENDED: %s", best.spec.name)
		t.Logf("    Model: %s | Thinking: %d | Temp: %.2f",
			best.spec.model, best.spec.thinkingBudget, best.spec.temperature)
		recall := float64(best.mustFindHits+best.niceFindHits) / float64(best.mustFindTotal+best.niceFindTotal) * 100
		t.Logf("    Must-find recall: %d/%d (%.0f%%)", best.mustFindHits, best.mustFindTotal,
			float64(best.mustFindHits)/float64(best.mustFindTotal)*100)
		t.Logf("    Recall: %.1f%% | FP: %d | Tools: %d | Time: %.1fs",
			recall, best.falseAlarms, best.toolCalls, best.duration.Seconds())
	}
	t.Log("═══════════════════════════════════════════════════════════════════════════════════════════")

	// ── Export benchmark results to ~/.config/prr/review-benchmark-<date>.json ─
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
		cost := config.EstimateCost(r.spec.provider+"/"+r.spec.model, r.inputTokens, r.outputTokens, r.cachedTokens)
		benchmarks.Models = append(benchmarks.Models, config.ModelBenchmark{
			ModelID:         r.spec.model,
			Provider:        r.spec.provider,
			ConfigName:      r.spec.name,
			Temperature:     r.spec.temperature,
			ThinkingBudget:  r.spec.thinkingBudget,
			RecallPct:       recallPct,
			MustFindPct:     mustFindPct,
			LatencyMs:       int(r.duration.Milliseconds()),
			CostPerScan:     cost,
			TotalAOIs:       r.totalFindings,
			FalseAlarms:     r.falseAlarms,
			SeverityCorrect: r.severityCorrect,
			SeverityTotal:   r.severityTotal,
			ToolCalls:       r.toolCalls,
		})
	}
	if err := config.SaveBenchmarkResults("review", benchmarks); err != nil {
		// A broken persistence layer is exactly the kind of regression
		// this test should catch — fail loudly rather than silently lose
		// the benchmark output.
		t.Errorf("failed to save benchmark results: %v", err)
	} else {
		p, _ := config.BenchmarkArchivePath("review", benchmarks.Timestamp)
		t.Logf("  Benchmark results saved to %s", p)
	}

	// Print detailed findings for each model
	for _, r := range results {
		if r.err != nil || len(r.findings) == 0 {
			continue
		}
		t.Logf("\n── %s: Detailed Findings ──", r.spec.name)
		// Sort by file then line
		sort.Slice(r.findings, func(i, j int) bool {
			if r.findings[i].File != r.findings[j].File {
				return r.findings[i].File < r.findings[j].File
			}
			si, _ := parseLineRange(r.findings[i].Lines)
			sj, _ := parseLineRange(r.findings[j].Lines)
			return si < sj
		})
		for _, f := range r.findings {
			gt := "FP"
			for _, g := range groundTruth {
				if matchDeepFinding(f, g) {
					gt = fmt.Sprintf("GT(%s)", g.importance)
					break
				}
			}
			t.Logf("  [%s] %s:%s — %s [%s] %s",
				f.Severity, f.File, f.Lines, f.Title, f.Category, gt)
		}
	}
}

// ── Benchmark tuning ──────────────────────────────────────────────────────
//
// Tuning is fixed by the benchmark so results don't drift with the user's
// models.json. Each model fans out into the cross product of temperatures ×
// thinking budgets — one run per combination. Override defaults per-run with
// env vars:
//
//	PRR_DEEP_TEMPERATURE           float, default 0.1   — baseline applied to every spec
//	PRR_DEEP_TEMPERATURE_VARIANTS  csv,   default ""    — extra temperatures to sweep
//	PRR_DEEP_THINKING_BUDGET       int,   default 8192  — baseline thinking budget
//	PRR_DEEP_THINKING_VARIANTS     csv,   default "16384,32768"
//	    extra budgets explored for thinking-capable models on providers that
//	    honor the explicit knob (gemini/openai). Copilot and claude-code
//	    decide thinking internally, so fanning them out wastes calls.
//	PRR_DEEP_MAX_OUTPUT            int,   default 32000
//
// Set a *_VARIANTS env var to the empty string to disable that axis's fan-out
// while keeping the baseline.

func deepBenchmarkTemperature() float64 {
	if v, ok := deepEnvFloat("PRR_DEEP_TEMPERATURE"); ok {
		return v
	}
	return 0.1
}

func deepBenchmarkMaxOutput() int {
	if v, ok := deepEnvInt("PRR_DEEP_MAX_OUTPUT"); ok {
		return v
	}
	return 32000
}

func deepBenchmarkThinkingBudget() int {
	if v, ok := deepEnvInt("PRR_DEEP_THINKING_BUDGET"); ok {
		return v
	}
	return 8192
}

// deepBenchmarkTemperatures returns the temperatures to sweep — baseline plus
// any PRR_DEEP_TEMPERATURE_VARIANTS, with duplicates removed.
func deepBenchmarkTemperatures() []float64 {
	base := deepBenchmarkTemperature()
	out := []float64{base}
	s, present := os.LookupEnv("PRR_DEEP_TEMPERATURE_VARIANTS")
	if !present {
		return out
	}
	seen := map[float64]bool{base: true}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(p, "%f", &v); err != nil {
			continue
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// deepBenchmarkThinkingBudgets returns the thinking budgets to sweep — baseline
// plus any PRR_DEEP_THINKING_VARIANTS, with duplicates removed.
func deepBenchmarkThinkingBudgets() []int {
	base := deepBenchmarkThinkingBudget()
	out := []int{base}
	s, present := os.LookupEnv("PRR_DEEP_THINKING_VARIANTS")
	if !present {
		for _, v := range []int{16384, 32768} {
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
		var v int
		if _, err := fmt.Sscanf(p, "%d", &v); err != nil {
			continue
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func deepEnvInt(name string) (int, bool) {
	s := os.Getenv(name)
	if s == "" {
		return 0, false
	}
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, false
	}
	return v, true
}

func deepEnvFloat(name string) (float64, bool) {
	s := os.Getenv(name)
	if s == "" {
		return 0, false
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0, false
	}
	return v, true
}

// deepProviderHonorsThinkingBudget reports whether the provider actually
// applies the explicit thinking-budget knob. Copilot and claude-code both
// decide thinking internally, so sweeping budgets for them produces
// redundant runs.
func deepProviderHonorsThinkingBudget(provider string) bool {
	return provider != "github-copilot" && provider != "claude-code"
}

// formatDeepThinkingLabel renders a thinking budget for spec names: "0",
// "1k", "512" — using the k-suffix only when the budget is a clean
// multiple of 1024.
func formatDeepThinkingLabel(budget int) string {
	if budget == 0 {
		return "thinking=0"
	}
	if budget%1024 == 0 {
		return fmt.Sprintf("thinking=%dk", budget/1024)
	}
	return fmt.Sprintf("thinking=%d", budget)
}

// expandDeepSpecs fans out one (provider, model) into the cross product of
// temperatures × thinking budgets. For providers that don't honor the
// explicit knob, only the baseline budget is used.
func expandDeepSpecs(provider, modelID, apiKey, baseURL string, supportsThinking bool) []deepModelSpec {
	temps := deepBenchmarkTemperatures()
	budgets := []int{deepBenchmarkThinkingBudget()}
	if supportsThinking && deepProviderHonorsThinkingBudget(provider) {
		budgets = deepBenchmarkThinkingBudgets()
	}

	multiTemp := len(temps) > 1
	multiBudget := len(budgets) > 1
	maxOut := deepBenchmarkMaxOutput()

	var specs []deepModelSpec
	for _, t := range temps {
		for _, b := range budgets {
			name := fmt.Sprintf("[%s] %s", provider, modelID)
			var parts []string
			if multiTemp {
				parts = append(parts, fmt.Sprintf("temp=%g", t))
			}
			if multiBudget {
				parts = append(parts, formatDeepThinkingLabel(b))
			}
			if len(parts) > 0 {
				name += " (" + strings.Join(parts, ", ") + ")"
			}
			specs = append(specs, deepModelSpec{
				name:           name,
				model:          modelID,
				provider:       provider,
				apiKey:         apiKey,
				baseURL:        baseURL,
				temperature:    t,
				thinkingBudget: b,
				maxOutput:      maxOut,
			})
		}
	}
	return specs
}

// ── Model config helpers ──────────────────────────────────────────────────

func deepModelsFromConfig(cfg *config.Config) []deepModelSpec {
	var specs []deepModelSpec
	for providerName, pc := range cfg.Providers {
		if pc.APIKey == "" {
			continue
		}
		for _, km := range config.KnownModelsForProvider(providerName) {
			if !km.Review || !km.Thinking {
				continue
			}
			specs = append(specs, expandDeepSpecs(providerName, km.ID, pc.APIKey, pc.BaseURL, km.Thinking)...)
		}
	}
	return specs
}

func deepModelsFromEnv(cfg *config.Config) []deepModelSpec {
	envModels := os.Getenv("PRR_DEEP_MODELS")
	if envModels == "" {
		return nil
	}

	providerName := os.Getenv("PRR_DEEP_PROVIDER")

	var specs []deepModelSpec
	for m := range strings.SplitSeq(envModels, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}

		// Determine provider(s) — find all configured providers that offer this model
		var provs []string
		if providerName != "" {
			provs = []string{providerName}
		} else {
			for pName, pc := range cfg.Providers {
				if pc.APIKey == "" {
					continue
				}
				for _, km := range config.KnownModelsForProvider(pName) {
					if km.ID == m {
						provs = append(provs, pName)
						break
					}
				}
			}
			if len(provs) == 0 {
				if km, ok := config.GetKnownModel(m); ok {
					provs = []string{km.Provider}
				} else {
					provs = []string{"gemini"}
				}
			}
		}

		for _, prov := range provs {
			pc := cfg.ProviderConfigFor(prov)
			supportsThinking := false
			if km, ok := config.GetKnownModelForProvider(prov, m); ok {
				supportsThinking = km.Thinking
			}
			specs = append(specs, expandDeepSpecs(prov, m, pc.APIKey, pc.BaseURL, supportsThinking)...)
		}
	}

	return specs
}

func countImportance(gt []deepGroundTruth, importance string) int {
	n := 0
	for _, g := range gt {
		if g.importance == importance {
			n++
		}
	}
	return n
}

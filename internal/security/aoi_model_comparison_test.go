package security_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
)

// ── AOI Model Comparison Test ─────────────────────────────────────────────
//
// This test compares multiple models on the AOI security pre-scan task.
// It uses a fixed set of diffs with KNOWN security issues (ground truth)
// and measures which model catches the most real issues at the lowest cost.
//
// Run with:
//   PRR_API_KEY=your_key go test ./internal/security/ -run TestAOIModelComparison -v -timeout 10m
//
// Override models list:
//   PRR_AOI_MODELS="gemini-2.5-flash,gemini-2.5-pro" PRR_API_KEY=... go test ...

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
		{file: "internal/api/handler.go", lineRange: [2]int{15, 22}, category: "sql", importance: "must-find", desc: "SQL injection via string concatenation"},
		{file: "internal/api/handler.go", lineRange: [2]int{14, 26}, category: "user-input", importance: "nice-to-find", desc: "User input from URL query parameter (q)"},
		{file: "internal/api/handler.go", lineRange: [2]int{25, 26}, category: "xss", importance: "must-find", desc: "XSS via fmt.Fprintf with user input"},
		{file: "internal/api/handler.go", lineRange: [2]int{30, 38}, category: "exec", importance: "must-find", desc: "Command injection via exec.Command with user input"},
		{file: "internal/api/handler.go", lineRange: [2]int{42, 45}, category: "auth", importance: "must-find", desc: "Admin endpoint without authentication"},
		{file: "internal/api/handler.go", lineRange: [2]int{49, 57}, category: "xss", importance: "nice-to-find", desc: "Server-side template injection"},

		// redirect.go
		{file: "internal/api/redirect.go", lineRange: [2]int{11, 14}, category: "redirect", importance: "must-find", desc: "Open redirect with user-controlled URL"},
		{file: "internal/api/redirect.go", lineRange: [2]int{18, 28}, category: "file-access", importance: "must-find", desc: "Path traversal via user-controlled file path"},

		// token.go
		{file: "internal/auth/token.go", lineRange: [2]int{11, 11}, category: "secrets", importance: "must-find", desc: "Hardcoded secret key in source"},
		{file: "internal/auth/token.go", lineRange: [2]int{15, 17}, category: "crypto", importance: "must-find", desc: "MD5 used for password hashing"},
		{file: "internal/auth/token.go", lineRange: [2]int{22, 24}, category: "crypto", importance: "nice-to-find", desc: "Predictable token generation (no crypto/rand)"},
		{file: "internal/auth/token.go", lineRange: [2]int{29, 30}, category: "secrets", importance: "must-find", desc: "Token logged in plaintext"},
		{file: "internal/auth/token.go", lineRange: [2]int{32, 33}, category: "crypto", importance: "nice-to-find", desc: "Non-constant-time string comparison for secrets"},

		// settings.go
		{file: "internal/config/settings.go", lineRange: [2]int{9, 11}, category: "network", importance: "must-find", desc: "SSRF via http.Get with user-controlled URL"},

		// helpers.go — SHOULD have no findings (clean file)
	}

	return diffs, groundTruth
}

// modelSpec defines a model to test.
type modelSpec struct {
	name           string
	model          string
	thinkingBudget int
	temperature    float64
	maxOutput      int
}

// defaultModels returns the list of models to compare.
// Models are ordered cheapest → most expensive. The test helps find the
// cheapest model that still achieves acceptable recall for AOI pre-scan.
//
// Current Gemini lineup (as of 2026-05):
//
//	Tier          Model ID
//	─────────     ─────────────────────────────
//	cheapest      gemini-2.5-flash-lite
//	cheap         gemini-2.5-flash
//	new-cheap     gemini-3.1-flash-lite-preview
//	new-flash     gemini-3-flash-preview
//	baseline      gemini-3.1-pro-preview      (latest, most capable)
func defaultModels() []modelSpec {
	return []modelSpec{
		// ── Cheapest: gemini-2.5-flash-lite ──────────────────────────
		{
			name:        "2.5-flash-lite (temp=0.1)",
			model:       "gemini-2.5-flash-lite",
			temperature: 0.1,
			maxOutput:   8192,
		},
		{
			name:        "2.5-flash-lite (temp=0.3)",
			model:       "gemini-2.5-flash-lite",
			temperature: 0.3,
			maxOutput:   8192,
		},

		// ── Cheap: gemini-2.5-flash (current AOI default) ───────────
		{
			name:        "2.5-flash (no thinking)",
			model:       "gemini-2.5-flash",
			temperature: 0.1,
			maxOutput:   8192,
		},
		{
			name:           "2.5-flash (thinking=2k)",
			model:          "gemini-2.5-flash",
			thinkingBudget: 2048,
			temperature:    0.1,
			maxOutput:      8192,
		},

		// ── New cheap: gemini-3.1-flash-lite-preview ─────────────────
		{
			name:        "3.1-flash-lite-preview (temp=0.1)",
			model:       "gemini-3.1-flash-lite-preview",
			temperature: 0.1,
			maxOutput:   8192,
		},

		// ── New flash: gemini-3-flash-preview ────────────────────────
		{
			name:        "3-flash-preview (temp=0.1)",
			model:       "gemini-3-flash-preview",
			temperature: 0.1,
			maxOutput:   8192,
		},
		{
			name:           "3-flash-preview (thinking=2k)",
			model:          "gemini-3-flash-preview",
			thinkingBudget: 2048,
			temperature:    0.1,
			maxOutput:      8192,
		},

		// ── Expensive baseline: gemini-3.1-pro-preview ──────────────
		// Included as a recall ceiling — NOT intended for AOI use.
		{
			name:        "3.1-pro-preview (baseline)",
			model:       "gemini-3.1-pro-preview",
			temperature: 0.1,
			maxOutput:   8192,
		},
	}
}

// parseModelsFromEnv parses PRR_AOI_MODELS env var into model specs.
// Format: "model1,model2,..." — all use default settings.
func parseModelsFromEnv() []modelSpec {
	envModels := os.Getenv("PRR_AOI_MODELS")
	if envModels == "" {
		return nil
	}

	var specs []modelSpec
	for _, m := range strings.Split(envModels, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		specs = append(specs, modelSpec{
			name:        m,
			model:       m,
			temperature: 0.1,
			maxOutput:   8192,
		})
	}
	return specs
}

// ── Gemini pricing (Standard tier, per 1M tokens, text input) ─────────
// Source: ai.google.dev/gemini-api/docs/pricing (2026-04-30)
// Output prices include thinking tokens.
type modelPricing struct {
	inputPerMTok  float64 // USD per 1M input tokens
	outputPerMTok float64 // USD per 1M output tokens
}

var geminiPricing = map[string]modelPricing{
	"gemini-2.5-flash-lite":         {0.10, 0.40},
	"gemini-2.5-flash":              {0.30, 2.50},
	"gemini-3.1-flash-lite-preview": {0.25, 1.50},
	"gemini-3-flash-preview":        {0.50, 3.00},
	"gemini-2.5-pro":                {1.25, 10.00}, // ≤200k prompt
	"gemini-3.1-pro-preview":        {2.00, 12.00}, // ≤200k prompt
}

func estimateCost(model string, inputTokens, outputTokens int) float64 {
	p, ok := geminiPricing[model]
	if !ok {
		return 0
	}
	return (float64(inputTokens)/1_000_000)*p.inputPerMTok +
		(float64(outputTokens)/1_000_000)*p.outputPerMTok
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

	categoryMatch = aoi.Category == gt.category
	return
}

func TestAOIModelComparison(t *testing.T) {
	apiKey := os.Getenv("PRR_API_KEY")
	if apiKey == "" {
		t.Skip("PRR_API_KEY not set — skipping live AOI model comparison test")
	}

	models := parseModelsFromEnv()
	if models == nil {
		models = defaultModels()
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
			provider := &ai.GeminiProvider{
				APIKey: apiKey,
				Model:  spec.model,
			}
			provider.ModelConfig.MaxOutputTokens = spec.maxOutput
			provider.ModelConfig.Temperature = spec.temperature
			provider.ModelConfig.ThinkingBudget = spec.thinkingBudget

			tracker := &ai.UsageTracker{}
			client := ai.NewAgent(provider, nil, ai.WithUsageTracker(tracker))

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			start := time.Now()
			report, err := security.ScanAreasOfInterest(ctx, client, diffs, func(status string) {
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
				inputTokens:  usage.InputTokens,
				outputTokens: usage.OutputTokens,
				cost:         estimateCost(spec.model, usage.InputTokens, usage.OutputTokens),
			}

			if err != nil {
				t.Logf("  ERROR: %v (%.1fs)", err, elapsed.Seconds())
				results[i] = result
				return
			}

			result.totalAOIs = report.TotalAOIs

			// ── Score: match each ground truth against model output ───
			gtMatched := make([]bool, len(groundTruth))

			for _, fileResult := range report.Files {
				for _, aoi := range fileResult.AreasOfInterest {
					matched := false
					for gi, gt := range groundTruth {
						if gtMatched[gi] {
							continue // already matched
						}
						fileOK, lineOK, catOK := matchAOI(aoi, gt)
						// Count as a hit if file+line match (category is a bonus)
						if fileOK && lineOK {
							gtMatched[gi] = true
							matched = true
							_ = catOK // we track it but don't require it
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
}

// TestAOIModelComparison_DetailedOutput runs a single model and prints
// the full AOI output for manual inspection. Useful for prompt tuning.
func TestAOIModelComparison_DetailedOutput(t *testing.T) {
	apiKey := os.Getenv("PRR_API_KEY")
	if apiKey == "" {
		t.Skip("PRR_API_KEY not set — skipping live AOI detail test")
	}

	model := os.Getenv("PRR_AOI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash-lite"
	}

	diffs, groundTruth := securityTestDiffs()

	provider := &ai.GeminiProvider{
		APIKey: apiKey,
		Model:  model,
	}
	provider.ModelConfig.MaxOutputTokens = 8192
	provider.ModelConfig.Temperature = 0.1

	tracker := &ai.UsageTracker{}
	client := ai.NewAgent(provider, nil, ai.WithUsageTracker(tracker))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	start := time.Now()
	report, err := security.ScanAreasOfInterest(ctx, client, diffs, func(status string) {
		t.Logf("[progress] %s", status)
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AOI scan failed: %v", err)
	}

	usage := tracker.Snapshot()
	cost := estimateCost(model, usage.InputTokens, usage.OutputTokens)
	t.Logf("\nModel: %s | Time: %.1fs | Total AOIs: %d | Overall risk: %s | Tokens: %d in + %d out | Cost: $%.4f\n",
		model, elapsed.Seconds(), report.TotalAOIs, report.OverallRisk,
		usage.InputTokens, usage.OutputTokens, cost)

	// Print each file's AOIs
	for _, fileResult := range report.Files {
		t.Logf("── %s (risk: %s) ──", fileResult.File, fileResult.RiskLevel)
		t.Logf("   Summary: %s", fileResult.RiskSummary)

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

// contextLineDiffs returns two versions of the same diffs: one with 3 lines
// of context (simulating git diff -U3) and one with 10 lines (git diff -U10).
// The vulnerabilities are the same, but with U3 the user-input source is often
// invisible — the model only sees the sink. With U10, the source is visible in
// context lines, enabling data-flow analysis.
//
// Scenario: existing Go API server. The PR modifies handler functions,
// adding vulnerable code. The source (r.URL.Query, r.FormValue, etc.) was
// written in an earlier commit and only appears as context.
func contextLineDiffs() (u3 map[string]string, u10 map[string]string, gt []groundTruthAOI) {
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

	return u3, u10, gt
}

// TestAOIContextLineComparison tests whether having more context lines (U10
// vs U3) improves the model's ability to detect vulnerabilities where the
// source (user input) is not in the changed lines but in surrounding context.
func TestAOIContextLineComparison(t *testing.T) {
	apiKey := os.Getenv("PRR_API_KEY")
	if apiKey == "" {
		t.Skip("PRR_API_KEY not set — skipping context line comparison test")
	}

	u3Diffs, u10Diffs, groundTruth := contextLineDiffs()

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
	models := parseModelsFromEnv()
	if models == nil {
		models = []modelSpec{
			{name: "2.5-flash-lite", model: "gemini-2.5-flash-lite", temperature: 0.1, maxOutput: 8192},
			{name: "3-flash-preview", model: "gemini-3-flash-preview", temperature: 0.1, maxOutput: 8192},
		}
	}

	type contextResult struct {
		modelName string
		context   string // "U3" or "U10"
		result    modelResult
	}
	var allResults []contextResult

	for _, spec := range models {
		for _, tc := range []struct {
			label string
			diffs map[string]string
		}{
			{"U3", u3Diffs},
			{"U10", u10Diffs},
		} {
			testName := fmt.Sprintf("%s/%s", spec.name, tc.label)
			t.Run(testName, func(t *testing.T) {
				provider := &ai.GeminiProvider{
					APIKey: apiKey,
					Model:  spec.model,
				}
				provider.ModelConfig.MaxOutputTokens = spec.maxOutput
				provider.ModelConfig.Temperature = spec.temperature
				provider.ModelConfig.ThinkingBudget = spec.thinkingBudget

				tracker := &ai.UsageTracker{}
				client := ai.NewAgent(provider, nil, ai.WithUsageTracker(tracker))

				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()

				start := time.Now()
				report, err := security.ScanAreasOfInterest(ctx, client, tc.diffs, func(status string) {
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
					cost:          estimateCost(spec.model, usage.InputTokens, usage.OutputTokens),
				}

				if err != nil {
					t.Logf("  ERROR: %v", err)
					allResults = append(allResults, contextResult{spec.name, tc.label, r})
					return
				}

				r.totalAOIs = report.TotalAOIs

				// Score against ground truth
				gtMatched := make([]bool, len(groundTruth))
				for _, fileResult := range report.Files {
					for _, aoi := range fileResult.AreasOfInterest {
						matched := false
						for gi, gt := range groundTruth {
							if gtMatched[gi] {
								continue
							}
							fileOK, lineOK, _ := matchAOI(aoi, gt)
							if fileOK && lineOK {
								gtMatched[gi] = true
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
	t.Log("  CONTEXT LINE COMPARISON: U3 (default git) vs U10 (extra context)")
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

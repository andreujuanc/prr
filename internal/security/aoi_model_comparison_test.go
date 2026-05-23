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

// securityTestDiffs returns realistic diffs covering the AOI categories the
// benchmark exercises: security vulnerabilities, dep-file edits, correctness
// bugs, unit mismatches, trojan-source patterns, and API-confusion. Each
// item is documented in the groundTruth slice. declaredClean lists
// per-file [start,end] ranges that should not host any AOI — anything
// emitted in those ranges is treated as a hallucination.
func securityTestDiffs() (map[string]string, []groundTruthAOI, map[string][][2]int) {
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
		// handler.go — every GT below points at the SINGLE line where the
		// bug actually executes (the sink), not the surrounding scaffold.
		// Models emitting AOIs at the comment one line up or at the
		// closing brace below are imprecise; they fall to "aligned",
		// not "covered". See user feedback: GT must mark "where the
		// problem is" so deep review can assert correctly.
		{file: "internal/api/handler.go", lineRange: [2]int{18, 18}, category: "input-validation", importance: "must-find", desc: "SQL injection: db.Query with string-concatenated user input"},
		{file: "internal/api/handler.go", lineRange: [2]int{26, 26}, category: "input-validation", importance: "must-find", desc: "XSS: fmt.Fprintf reflecting user query into HTML response"},
		{file: "internal/api/handler.go", lineRange: [2]int{34, 34}, category: "input-validation", importance: "must-find", desc: "Command injection: exec.Command(\"sh\", \"-c\", cmd) with user-supplied cmd"},
		// Admin no-auth: the bug is the *missing* check spanning the
		// function body — the deep reviewer must see the function to
		// recommend adding auth, so the range stays multi-line over
		// L43-45 (declaration through the unprotected write). L42 is a
		// standalone comment and is excluded.
		{file: "internal/api/handler.go", lineRange: [2]int{43, 45}, category: "authorization", importance: "must-find", desc: "AdminHandler has no auth check — entire function body lacks authorization"},
		{file: "internal/api/handler.go", lineRange: [2]int{53, 53}, category: "input-validation", importance: "nice-to-find", desc: "Server-side template injection: template.New(...).Parse(tmplStr) on user input"},

		// redirect.go
		{file: "internal/api/redirect.go", lineRange: [2]int{14, 14}, category: "input-validation", importance: "must-find", desc: "Open redirect: http.Redirect to user-controlled URL"},
		{file: "internal/api/redirect.go", lineRange: [2]int{22, 22}, category: "input-validation", importance: "must-find", desc: "Path traversal: os.Open on filepath.Join(\"/data\", user-input)"},

		// token.go
		{file: "internal/auth/token.go", lineRange: [2]int{11, 11}, category: "configuration", importance: "must-find", desc: "Hardcoded secret key"},
		{file: "internal/auth/token.go", lineRange: [2]int{16, 16}, category: "cryptography", importance: "must-find", desc: "MD5 used for password hashing"},
		{file: "internal/auth/token.go", lineRange: [2]int{23, 23}, category: "cryptography", importance: "nice-to-find", desc: "Predictable token: timestamp-derived, not crypto/rand"},
		{file: "internal/auth/token.go", lineRange: [2]int{30, 30}, category: "data-integrity", importance: "must-find", desc: "log.Printf leaks token in plaintext"},
		{file: "internal/auth/token.go", lineRange: [2]int{34, 34}, category: "cryptography", importance: "nice-to-find", desc: "Non-constant-time comparison: token == expected"},

		// settings.go
		{file: "internal/config/settings.go", lineRange: [2]int{11, 11}, category: "external-io", importance: "must-find", desc: "SSRF: http.Get on user-controlled URL"},
		{file: "internal/config/settings.go", lineRange: [2]int{18, 18}, category: "error-handling", importance: "nice-to-find", desc: "json.Decode error ignored — malformed responses silently yield empty config"},

		// handler.go — t.Execute return value ignored in TemplateHandler.
		// Render / write failures pass silently to the client.
		{file: "internal/api/handler.go", lineRange: [2]int{58, 58}, category: "error-handling", importance: "nice-to-find", desc: "t.Execute return ignored — render/write failures silently dropped"},

		// helpers.go — `s[:1]` slices bytes; breaks for any multi-byte
		// first rune (é, 你, 🔥). A recall-biased AOI scanner should flag
		// the correctness bug here.
		{file: "internal/util/helpers.go", lineRange: [2]int{10, 10}, category: "correctness", importance: "must-find", desc: "Capitalize uses s[:1] — slices bytes, breaks on multi-byte first rune"},

		// go.mod — surface-area rule: every dependency-file edit gets an
		// AOI per `aoi_scan.md:162-178`, regardless of how routine the
		// change looks. L5 is the newly-introduced require entry, the
		// precise change to flag.
		{file: "go.mod", lineRange: [2]int{5, 5}, category: "malicious-code", importance: "must-find", desc: "new require entry: github.com/some/dep — confirm provenance"},
	}

	declaredClean := map[string][][2]int{
		// helpers.go line 1 is just `package util` — any AOI on the
		// package declaration is hallucinated.
		"internal/util/helpers.go": {{1, 1}},
	}

	// Merge extra fixtures covering categories not exercised by the
	// security-focused base set above.
	extraDiffs, extraGT, extraClean := extraTestDiffs()
	for file, diff := range extraDiffs {
		diffs[file] = diff
	}
	groundTruth = append(groundTruth, extraGT...)
	for file, ranges := range extraClean {
		declaredClean[file] = append(declaredClean[file], ranges...)
	}

	return diffs, groundTruth, declaredClean
}

// extraTestDiffs covers categories the base security fixtures don't
// reach: data-integrity/unit-mismatch, malicious-code/obfuscation,
// malicious-code/suspicious-dependencies (lockfile mismatch), and
// design/api-confusion. Each fixture is small (≤25 lines) so the live
// benchmark wall-clock stays comparable to the previous suite.
func extraTestDiffs() (map[string]string, []groundTruthAOI, map[string][][2]int) {
	diffs := map[string]string{
		"internal/billing/charge.go": `@@ -0,0 +1,16 @@
+package billing
+
+func ChargeAmountCents(userID string) int64 {
+	return lookupCents(userID)
+}
+
+// ApplyDiscount discounts a USD dollar amount.
+func ApplyDiscount(amountDollars float64, pct float64) float64 {
+	return amountDollars * (1 - pct/100)
+}
+
+// FinalizeBill computes the final user bill.
+func FinalizeBill(userID string, pct float64) float64 {
+	raw := ChargeAmountCents(userID)
+	return ApplyDiscount(float64(raw), pct)
+}`,

		// Embeds an actual U+202E RIGHT-TO-LEFT OVERRIDE codepoint in the
		// string literal — the AOI scanner should flag bidi/zero-width
		// characters per `aoi_scan.md:140-159`. The Go compiler turns
		// "‮" in the test source into the real character at compile
		// time, so the diff text the scanner receives contains the
		// actual codepoint.
		"internal/admin/marker.go": "@@ -0,0 +1,7 @@\n" +
			"+package admin\n" +
			"+\n" +
			"+const adminMarker = \"‮ADMIN\"\n" +
			"+\n" +
			"+func isPrivileged(role string) bool {\n" +
			"+\treturn role == adminMarker\n" +
			"+}",

		// Lockfile-mismatch: package-lock.json bumps a dep version, but
		// package.json sees an unrelated change. The "lockfile changes
		// that don't match the source-side changes in the same PR are
		// extra-suspicious" clause from aoi_scan.md:175-177.
		"package.json": `@@ -1,5 +1,5 @@
 {
   "name": "demo",
-  "description": "old description",
+  "description": "new description",
   "version": "1.0.0"
 }`,
		"package-lock.json": `@@ -40,7 +40,7 @@
     "node_modules/lodash": {
-      "version": "4.17.21",
+      "version": "4.17.22-evil-xyz",
       "resolved": "https://registry.npmjs.org/lodash/-/lodash.tgz",
       "integrity": "sha512-abcdefghijklmnop==",
       "license": "MIT",
       "engines": { "node": ">=8" }
     }`,

		"internal/ledger/transfers.go": `@@ -0,0 +1,18 @@
+package ledger
+
+// transfer sends amount from one account to another.
+// Argument order: (from, to, amount).
+func transfer(from, to string, amount int64) error {
+	return move(from, to, amount)
+}
+
+// refund returns amount to the customer. NOTE: argument order is
+// REVERSED from transfer — the recipient (customer) comes first.
+func refund(customerAcct, merchantAcct string, amount int64) error {
+	return move(merchantAcct, customerAcct, amount)
+}
+
+// chargeback refunds the customer from the merchant's account.
+func chargeback(customer, merchant string, cents int64) error {
+	return refund(merchant, customer, cents)
+}`,
	}

	gt := []groundTruthAOI{
		// charge.go — cents (int64) flows into ApplyDiscount expecting USD
		// dollars (float64). The bare float64() conversion at line 15
		// preserves the cents magnitude, so the discount applies to a
		// number that is 100× too large.
		{file: "internal/billing/charge.go", lineRange: [2]int{15, 15}, category: "data-integrity", importance: "must-find", desc: "cents value flows into a USD-dollar consumer (unit mismatch on the ApplyDiscount call)"},

		// charge.go — independent concern: monetary discount is computed
		// in float64. Floating-point arithmetic on money causes rounding
		// drift and precision loss; standard advice is integer cents or
		// decimal types.
		{file: "internal/billing/charge.go", lineRange: [2]int{9, 9}, category: "data-integrity", importance: "must-find", desc: "monetary discount computed in float64 — rounding / precision loss"},

		// marker.go — bidi override in a string literal that controls
		// privilege checks. The deep reviewer should see this.
		{file: "internal/admin/marker.go", lineRange: [2]int{3, 3}, category: "malicious-code", importance: "must-find", desc: "U+202E bidi override in a privilege-control string literal"},

		// marker.go — direct consequence of the bidi: a comparison against
		// the obfuscated constant means a legitimate "ADMIN" input never
		// matches. Separate AOI from the bidi itself, but caused by it.
		{file: "internal/admin/marker.go", lineRange: [2]int{6, 6}, category: "authorization", importance: "must-find", desc: "privilege check compares user role to obfuscated constant — legitimate ADMIN input never matches"},

		// package-lock.json — version bump on lodash without matching
		// package.json change. L41 is the precise version line.
		{file: "package-lock.json", lineRange: [2]int{41, 41}, category: "malicious-code", importance: "must-find", desc: "lockfile lodash version bump to suspicious tag, no matching package.json change"},

		// package.json — touching any dependency file is itself an AOI
		// per the surface-area rule, even when the change looks routine.
		// L3 is the line that actually changed.
		{file: "package.json", lineRange: [2]int{3, 3}, category: "malicious-code", importance: "nice-to-find", desc: "package.json description modified — provenance check on dependency manifest"},

		// transfers.go — chargeback calls refund(merchant, customer) but
		// refund's signature puts the customer first (customerAcct,
		// merchantAcct). The call at L17 refunds the MERCHANT instead
		// of the customer.
		{file: "internal/ledger/transfers.go", lineRange: [2]int{17, 17}, category: "design", importance: "must-find", desc: "chargeback passes (merchant, customer) to refund(customer, merchant) — refunds the wrong party"},

		// transfers.go — root cause of the chargeback bug above:
		// declaring refund with arguments reversed vs transfer is an API
		// design choice that invites caller mistakes. Flagging the
		// signature itself (not just the buggy call site) is what lets
		// the deep reviewer recommend a structural fix.
		{file: "internal/ledger/transfers.go", lineRange: [2]int{11, 11}, category: "design", importance: "nice-to-find", desc: "refund declared with reversed argument order vs transfer — API shape invites caller mistakes"},
	}

	declaredClean := map[string][][2]int{
		// Package declaration line — never a real AOI in these fixtures.
		"internal/billing/charge.go":   {{1, 1}},
		"internal/admin/marker.go":     {{1, 1}},
		"internal/ledger/transfers.go": {{1, 1}},
	}

	return diffs, gt, declaredClean
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
// benchmarkTemperature returns the baseline temperature applied to every
// spec. 0.3 is the empirically-tuned sweet spot from the temperature
// sweep on gemini-3.1-flash-lite: temp ∈ [0.05, 0.5] consistently kept
// flash-lite in the 52-56% coverage band, while temp=0 fell into a 40%
// low mode about 40% of the time. 0.3 sits in the middle of the
// winning band and is reasonable across providers — purely greedy
// (temp=0) made flash-lite stick in a local minimum.
func benchmarkTemperature() float64 {
	if v, ok := envFloat("PRR_BENCH_TEMPERATURE"); ok {
		return v
	}
	return 0.3
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
//
// Bypasses ai.TempPtr (which treats <=0 as "unset / use provider default") so
// the benchmark can send literal 0 = greedy decoding when configured. Negative
// temperatures still fall through to provider default.
func createProvider(spec modelSpec) (ai.Provider, error) {
	var temp *float64
	if spec.temperature >= 0 {
		t := spec.temperature
		temp = &t
	}
	return ai.NewProvider(ai.ProviderConfig{
		ProviderName:    spec.provider,
		ModelID:         spec.model,
		APIKey:          spec.apiKey,
		BaseURL:         spec.baseURL,
		MaxOutputTokens: spec.maxOutput,
		Temperature:     temp,
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

	// Scoring — AOIs are a recall-biased pre-filter, so "unmatched"
	// is not penalized. See classifyAOI for the contract.
	mustFindTotal  int
	mustFindHits   int
	niceFindTotal  int
	niceFindHits   int
	covered        int     // AOIs that overlap a ground-truth entry
	aligned        int     // AOIs not in GT but on real diff lines (acceptable)
	hallucinations int     // AOIs that fail structural checks
	totalAOIs      int     // count of AOIs the model emitted
	coveragePct    float64 // % of ground truth surfaced
	aoiDensity     float64 // AOIs per 100 scanned LoC

	// Line-offset precision: for each non-hallucinated AOI on a file
	// that has any GT, the offset is the distance (in lines) to the
	// nearest GT range — 0 if the AOI overlaps a GT. avgLineOffset is
	// the sum / count over all those samples. Lower is better; tells
	// you whether aligned AOIs are near-misses (off by 1-2 lines) or
	// far from any real bug.
	offsetSum     int     // sum of per-AOI offsets
	offsetSamples int     // count of AOIs that contributed an offset
	avgLineOffset float64 // offsetSum / offsetSamples

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

// aoiClassification labels each emitted AOI for scoring.
//
//   - aoiCovered — overlaps a ground-truth entry (file + line range).
//   - aoiAligned — doesn't match GT but isn't hallucinated. AOIs are a
//     recall-biased pre-filter (see internal/security/prompts/aoi_scan.md);
//     "aligned noise" is acceptable by design and not penalized.
//   - aoiHallucinated — fails a structural check: wrong file, line outside
//     the diff hunks, declared-clean overlap, or (for security-shaped
//     categories) cites sources/sinks that don't appear within ±3 lines
//     of the reported location.
type aoiClassification int

const (
	aoiCovered aoiClassification = iota
	aoiAligned
	aoiHallucinated
)

// fixtureMeta carries per-file diff metadata used by classifyAOI.
type fixtureMeta struct {
	// validLines is the set of new-side line numbers present in the diff
	// (added "+" lines and context " " lines). AOIs on any other line
	// number for this file are hallucinations.
	validLines map[int]bool
	// lineText maps new-side line number → that line's content (without
	// the leading +/space marker). Used to verify identifier proximity
	// for security-shaped AOIs.
	lineText map[int]string
	// declaredClean lists [start, end] line ranges that should not host
	// any AOI in this file. Any AOI overlapping a declared-clean range
	// is hallucinated by fiat. Empty in commit 1; fixtures populate
	// these as they grow.
	declaredClean [][2]int
}

// buildFixtureMeta parses unified-diff snippets in diffs into per-file
// metadata. The benchmark fixtures begin with one or more hunk headers
// like "@@ -0,0 +1,62 @@" followed by added/context lines.
func buildFixtureMeta(diffs map[string]string, declaredClean map[string][][2]int) map[string]fixtureMeta {
	out := make(map[string]fixtureMeta, len(diffs))
	for file, diff := range diffs {
		meta := fixtureMeta{
			validLines:    make(map[int]bool),
			lineText:      make(map[int]string),
			declaredClean: declaredClean[file],
		}
		curLine := 0
		for _, line := range strings.Split(diff, "\n") {
			if strings.HasPrefix(line, "@@") {
				curLine = parseHunkStart(line)
				continue
			}
			if curLine == 0 {
				continue
			}
			if strings.HasPrefix(line, "-") {
				continue // removed line, no new-side line number
			}
			content := ""
			if len(line) > 0 {
				content = line[1:]
			}
			meta.validLines[curLine] = true
			meta.lineText[curLine] = content
			curLine++
		}
		out[file] = meta
	}
	return out
}

// parseHunkStart returns the new-side starting line of a unified-diff
// hunk header. "@@ -0,0 +1,62 @@" → 1. Returns 0 if not parseable.
func parseHunkStart(hunk string) int {
	idx := strings.Index(hunk, "+")
	if idx < 0 {
		return 0
	}
	rest := hunk[idx+1:]
	end := strings.IndexAny(rest, ", \t@")
	if end <= 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

// aoiLineOffset returns the smallest line distance between this AOI's
// range and any GT range on the same file. 0 means the AOI overlaps a
// GT (covered). A positive number is the "off by N lines" precision
// metric — an aligned AOI 1 line above the real bug returns 1.
// Returns -1 when the file has no GT entries at all; callers should
// skip those from the average.
func aoiLineOffset(aoi security.AreaOfInterest, gt []groundTruthAOI) int {
	aoiStart, aoiEnd := aoi.Line, aoi.EndLine
	if aoiEnd <= 0 {
		aoiEnd = aoiStart
	}
	minDist := -1
	for _, g := range gt {
		if g.file != aoi.File {
			continue
		}
		var d int
		switch {
		case aoiEnd < g.lineRange[0]:
			d = g.lineRange[0] - aoiEnd
		case g.lineRange[1] < aoiStart:
			d = aoiStart - g.lineRange[1]
		default:
			d = 0 // overlap
		}
		if minDist < 0 || d < minDist {
			minDist = d
		}
	}
	return minDist
}

// classifyAOI labels one AOI against the fixture's ground truth and
// per-file metadata. See aoiClassification for the rule set.
func classifyAOI(aoi security.AreaOfInterest, gt []groundTruthAOI, meta map[string]fixtureMeta) aoiClassification {
	fm, ok := meta[aoi.File]
	if !ok {
		return aoiHallucinated
	}
	if !fm.validLines[aoi.Line] {
		return aoiHallucinated
	}
	for _, r := range fm.declaredClean {
		if aoi.Line >= r[0] && aoi.Line <= r[1] {
			return aoiHallucinated
		}
	}
	if isSecurityCategory(aoi.Category.String()) && !securityIdentifiersPresent(aoi, fm) {
		return aoiHallucinated
	}
	for _, g := range gt {
		if fileOK, lineOK, _ := matchAOI(aoi, g); fileOK && lineOK {
			return aoiCovered
		}
	}
	return aoiAligned
}

// isSecurityCategory mirrors the security-shaped category list in
// internal/security/prompts/aoi_scan.md — the set of categories the
// prompt requires to carry sources/sinks/sanitizers.
func isSecurityCategory(cat string) bool {
	switch cat {
	case "input-validation", "external-io", "authorization",
		"authentication", "cryptography", "web-security":
		return true
	}
	return false
}

// securityIdentifiersPresent verifies the AOI's sources/sinks reference
// identifiers that appear within ±3 lines of the reported location.
// An empty sources+sinks list is treated as present — the "no
// sanitizers" signal lives separately and isn't a hallucination by
// itself. Returns true if at least one identifier is found nearby.
func securityIdentifiersPresent(aoi security.AreaOfInterest, fm fixtureMeta) bool {
	idents := append([]string(nil), aoi.Sources...)
	idents = append(idents, aoi.Sinks...)
	if len(idents) == 0 {
		return true
	}
	const window = 3
	start := aoi.Line - window
	end := aoi.Line + window
	if aoi.EndLine > 0 && aoi.EndLine+window > end {
		end = aoi.EndLine + window
	}
	for ln := start; ln <= end; ln++ {
		text, ok := fm.lineText[ln]
		if !ok {
			continue
		}
		for _, id := range idents {
			id = strings.TrimSpace(id)
			if len(id) < 2 {
				continue
			}
			if strings.Contains(text, id) {
				return true
			}
		}
	}
	return false
}

// benchmarkScanTimeout returns the per-model AOI scan deadline. Default
// 120s is enough for paid models; slow free-tier models throttle and
// need a longer ceiling — override via PRR_BENCH_SCAN_TIMEOUT_SEC.
func benchmarkScanTimeout() time.Duration {
	if v, ok := envInt("PRR_BENCH_SCAN_TIMEOUT_SEC"); ok && v > 0 {
		return time.Duration(v) * time.Second
	}
	return 120 * time.Second
}

// resolveCost prefers a provider-reported cost (e.g., opencode emits
// per-call cost in its step_finish event) over the per-1M-token estimate
// from known_models.go. Returns the reported number when it's non-zero,
// otherwise falls back to EstimateCost. The two never co-exist for the
// same call — providers either report cost or they don't.
//
// Takes the fields by value (not a UsageTracker pointer) so the call
// sites can stay one-liners; passing the tracker by value tripped
// `go vet`'s sync.Mutex-copy warning.
func resolveCost(reported float64, inputTokens, outputTokens int, provider, modelID string) float64 {
	if reported > 0 {
		return reported
	}
	return config.EstimateCost(provider+"/"+modelID, inputTokens, outputTokens)
}

// totalDiffLines sums validLines counts across the fixture — the
// denominator for AOI density.
func totalDiffLines(meta map[string]fixtureMeta) int {
	n := 0
	for _, fm := range meta {
		n += len(fm.validLines)
	}
	return n
}

// TestAOIBenchmarkFixturesValid verifies every ground-truth entry and
// declaredClean range references a real new-side line in its fixture's
// diff. Without this, GT entries can silently drift off the actual bug
// location (e.g. a [32,33] range when the bug is on line 34), making
// the benchmark score wrong without anyone noticing.
//
// Runs without PRR_LIVE_TESTS — pure local check, suitable for CI.
func TestAOIBenchmarkFixturesValid(t *testing.T) {
	check := func(name string, diffs map[string]string, gt []groundTruthAOI, declaredClean map[string][][2]int) {
		t.Helper()
		meta := buildFixtureMeta(diffs, declaredClean)

		for _, g := range gt {
			fm, ok := meta[g.file]
			if !ok {
				t.Errorf("[%s] GT references file not in diffs: %s (%s)", name, g.file, g.desc)
				continue
			}
			if len(fm.validLines) == 0 {
				t.Errorf("[%s] GT file %s has no parsed lines — bad hunk header? (%s)", name, g.file, g.desc)
				continue
			}
			minLine, maxLine := 1<<31, 0
			for ln := range fm.validLines {
				if ln < minLine {
					minLine = ln
				}
				if ln > maxLine {
					maxLine = ln
				}
			}
			if g.lineRange[0] < 1 || g.lineRange[1] < g.lineRange[0] {
				t.Errorf("[%s] GT range %s:[%d,%d] is malformed — %s", name, g.file, g.lineRange[0], g.lineRange[1], g.desc)
				continue
			}
			// Multi-context fixtures (contextLineDiffs) widen ranges
			// intentionally to match across U3/U5/U10 hunk shifts. For
			// those we only require overlap with validLines — extending
			// above maxLine or below minLine is harmless because no AOI
			// can be emitted at a non-existent line anyway.
			multiContext := strings.HasPrefix(name, "contextLineDiffs/")
			if multiContext {
				overlap := false
				for ln := g.lineRange[0]; ln <= g.lineRange[1]; ln++ {
					if fm.validLines[ln] {
						overlap = true
						break
					}
				}
				if !overlap {
					t.Errorf("[%s] GT range %s:[%d,%d] overlaps no diff line (lines %d-%d) — %s",
						name, g.file, g.lineRange[0], g.lineRange[1], minLine, maxLine, g.desc)
				}
				continue
			}
			// Single-context: every line in [start, end] must be a real
			// diff line. A range that extends past the diff means either
			// the hunk header is wrong or the GT is sloppy — both bugs
			// the benchmark author needs to know about.
			for ln := g.lineRange[0]; ln <= g.lineRange[1]; ln++ {
				if !fm.validLines[ln] {
					t.Errorf("[%s] GT range %s:[%d,%d] includes line %d which is not in the diff (max line %d) — %s",
						name, g.file, g.lineRange[0], g.lineRange[1], ln, maxLine, g.desc)
					break
				}
			}
		}

		for file, ranges := range declaredClean {
			fm, ok := meta[file]
			if !ok {
				t.Errorf("[%s] declaredClean references file not in diffs: %s", name, file)
				continue
			}
			for _, r := range ranges {
				hit := false
				for ln := r[0]; ln <= r[1]; ln++ {
					if fm.validLines[ln] {
						hit = true
						break
					}
				}
				if !hit {
					t.Errorf("[%s] declaredClean %s:[%d,%d] overlaps no diff line", name, file, r[0], r[1])
				}
			}
		}
	}

	diffs, gt, declared := securityTestDiffs()
	check("securityTestDiffs", diffs, gt, declared)

	// Audit dump: for every GT entry, show the diff lines it covers so we
	// can eyeball that the GT actually points at the bug. Set
	// PRR_AOI_AUDIT=1 to enable.
	if os.Getenv("PRR_AOI_AUDIT") == "1" {
		dbgMeta := buildFixtureMeta(diffs, declared)
		for _, g := range gt {
			fm := dbgMeta[g.file]
			t.Logf("── GT %s [%d,%d] %s/%s — %s",
				g.file, g.lineRange[0], g.lineRange[1], g.category, g.importance, g.desc)
			for ln := g.lineRange[0]; ln <= g.lineRange[1]; ln++ {
				if text, ok := fm.lineText[ln]; ok {
					t.Logf("    L%d: %q", ln, text)
				} else {
					t.Logf("    L%d: <not in diff>", ln)
				}
			}
		}
	}

	u3, u5, u10, ctxGT := contextLineDiffs()
	empty := map[string][][2]int{}
	check("contextLineDiffs/U3", u3, ctxGT, empty)
	check("contextLineDiffs/U5", u5, ctxGT, empty)
	check("contextLineDiffs/U10", u10, ctxGT, empty)
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

	diffs, groundTruth, declaredClean := securityTestDiffs()

	mustFindCount := 0
	niceFindCount := 0
	for _, gt := range groundTruth {
		if gt.importance == "must-find" {
			mustFindCount++
		} else {
			niceFindCount++
		}
	}

	meta := buildFixtureMeta(diffs, declaredClean)
	totalLoC := totalDiffLines(meta)

	t.Logf("Ground truth: %d must-find + %d nice-to-find = %d total AOIs across %d files (%d scanned LoC)",
		mustFindCount, niceFindCount, len(groundTruth), len(diffs), totalLoC)
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

			ctx, cancel := context.WithTimeout(context.Background(), benchmarkScanTimeout())
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
				cost:          resolveCost(usage.ReportedCostUSD, usage.InputTokens, usage.OutputTokens, spec.provider, spec.model),
			}

			if err != nil {
				t.Logf("  ERROR: %v (%.1fs)", err, elapsed.Seconds())
				results[i] = result
				return
			}

			result.totalAOIs = report.TotalAOIs

			// ── Score: classify each AOI as covered/aligned/hallucinated ──
			// Coverage tracks "did ANY AOI overlap this ground truth?"; an
			// AOI's classification is independent (see classifyAOI). Aligned
			// AOIs are accepted as recall-biased noise.
			gtMatched := make([]bool, len(groundTruth))
			for _, fileResult := range report.Files {
				for _, aoi := range fileResult.AreasOfInterest {
					cls := classifyAOI(aoi, groundTruth, meta)
					switch cls {
					case aoiCovered:
						result.covered++
						for gi, gt := range groundTruth {
							if fileOK, lineOK, _ := matchAOI(aoi, gt); fileOK && lineOK {
								gtMatched[gi] = true
							}
						}
					case aoiAligned:
						result.aligned++
					case aoiHallucinated:
						result.hallucinations++
					}
					// Skip hallucinated AOIs from offset stats — they
					// point at nonexistent lines so "distance to GT" is
					// not meaningful. Skip when file has no GT at all
					// (offset = -1) for the same reason.
					if cls != aoiHallucinated {
						if d := aoiLineOffset(aoi, groundTruth); d >= 0 {
							result.offsetSum += d
							result.offsetSamples++
						}
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

			totalGT := result.mustFindTotal + result.niceFindTotal
			if totalGT > 0 {
				result.coveragePct = float64(result.mustFindHits+result.niceFindHits) / float64(totalGT) * 100
			}
			if result.offsetSamples > 0 {
				result.avgLineOffset = float64(result.offsetSum) / float64(result.offsetSamples)
			}
			if totalLoC > 0 {
				result.aoiDensity = float64(result.totalAOIs) / float64(totalLoC) * 100
			}

			results[i] = result

			t.Logf("  Must: %d/%d | Nice: %d/%d | Aligned: %d | Halluc: %d | AOIs: %d | Coverage: %.1f%% | Density: %.1f/100LoC | AvgOffset: %.2f lines | Tokens: %d in + %d out | Cost: $%.4f | Time: %.1fs",
				result.mustFindHits, result.mustFindTotal,
				result.niceFindHits, result.niceFindTotal,
				result.aligned, result.hallucinations, result.totalAOIs,
				result.coveragePct, result.aoiDensity, result.avgLineOffset,
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
	t.Logf("  %-40s %7s %7s %6s %5s %8s %7s %7s %7s %7s %9s %7s",
		"Model", "Must", "Nice", "Halluc", "AOIs", "Coverage", "Densty", "Offset", "In Tok", "Out Tok", "Cost", "Time")
	t.Logf("  %-40s %7s %7s %6s %5s %8s %7s %7s %7s %7s %9s %7s",
		strings.Repeat("─", 40), "───────", "───────", "──────", "─────", "────────", "───────", "───────", "───────", "───────", "─────────", "───────")

	for _, r := range results {
		if r.err != nil {
			t.Logf("  %-40s   ERROR: %v", r.spec.name, r.err)
			continue
		}

		t.Logf("  %-40s %3d/%-3d %3d/%-3d %6d %5d   %5.1f%% %6.1f %6.2f %6dk %6dk  $%.4f %6.1fs",
			r.spec.name,
			r.mustFindHits, r.mustFindTotal,
			r.niceFindHits, r.niceFindTotal,
			r.hallucinations,
			r.totalAOIs,
			r.coveragePct,
			r.aoiDensity,
			r.avgLineOffset,
			r.inputTokens/1000, r.outputTokens/1000,
			r.cost,
			r.duration.Seconds())
	}

	t.Log("")
	t.Log("  Must     = must-find ground truth surfaced")
	t.Log("  Nice     = nice-to-find ground truth surfaced")
	t.Log("  Halluc   = AOIs at nonexistent lines / declared-clean ranges / fabricated identifiers")
	t.Log("  AOIs     = total AOIs emitted by the model")
	t.Log("  Coverage = (must+nice hits) / total ground truth")
	t.Log("  Densty   = AOIs per 100 scanned LoC (informational; not penalized)")
	t.Log("  Offset   = avg line distance from each non-hallucinated AOI to the nearest GT")
	t.Log("             (0 = on the exact bug line; higher = AOIs land off-target)")
	t.Log("  In/Out   = input/output tokens (thousands)")
	t.Log("  Cost     = estimated USD (metered API rate; for subscription")
	t.Log("             providers like Claude Code this is shadow pricing —")
	t.Log("             what this run would cost on the equivalent metered API)")
	t.Log("")
	t.Log("  Note: AOIs are a recall-biased pre-filter. Aligned-but-unmatched")
	t.Log("  AOIs are acceptable by design and not penalized in the score.")
	t.Log("")

	// ── Determine winner ─────────────────────────────────────────────
	// Score = coverage_pct - hallucination_pct * 0.5. Hallucinations
	// are normalized against the model's own AOI count so a model that
	// emits many aligned AOIs isn't penalized just for being verbose.
	bestIdx := -1
	bestScore := -1e9
	for i, r := range results {
		if r.err != nil {
			continue
		}
		hallucPct := float64(0)
		if r.totalAOIs > 0 {
			hallucPct = float64(r.hallucinations) / float64(r.totalAOIs) * 100
		}
		score := r.coveragePct - hallucPct*0.5
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
			Hallucinations: r.hallucinations,
			CoveragePct:    r.coveragePct,
			AOIDensity:     r.aoiDensity,
			AvgLineOffset:  r.avgLineOffset,
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

	diffs, groundTruth, _ := securityTestDiffs()

	spec := newSpec(displayName, modelID, providerName, pc.APIKey, pc.BaseURL)
	provider, err := createProvider(spec)
	if err != nil {
		t.Fatalf("createProvider: %v", err)
	}

	tracker := &ai.UsageTracker{}
	client := ai.NewAgent(provider, nil, ai.WithUsageTracker(tracker))

	ctx, cancel := context.WithTimeout(context.Background(), benchmarkScanTimeout())
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
	cost := resolveCost(usage.ReportedCostUSD, usage.InputTokens, usage.OutputTokens, spec.provider, modelID)
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

		// handler.go — actionable TODO comment ("switch back to allowlist
		// after testing") sits next to the exec sink. The aoi_scan.md
		// surface-area rule mandates an AOI for actionable TODO/FIXME.
		// Line numbers across U3/U5/U10: TODO appears at line 43 (U3),
		// line 46 (U5), line 42 (U10) — the widened range covers all
		// three.
		{file: "internal/api/handler.go", lineRange: [2]int{40, 50}, category: "correctness", importance: "must-find", desc: "actionable TODO admits known gap next to exec sink"},
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

				ctx, cancel := context.WithTimeout(context.Background(), benchmarkScanTimeout())
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
					cost:          resolveCost(usage.ReportedCostUSD, usage.InputTokens, usage.OutputTokens, spec.provider, spec.model),
				}

				if err != nil {
					t.Logf("  ERROR: %v", err)
					allResults = append(allResults, contextResult{spec.name, tc.label, r})
					return
				}

				r.totalAOIs = report.TotalAOIs

				// Fixture meta is per-context-tier — U3/U5/U10 each have
				// different diff hunks, so each has its own valid-line set.
				meta := buildFixtureMeta(tc.diffs, map[string][][2]int{})
				totalLoC := totalDiffLines(meta)

				gtMatched := make([]bool, len(groundTruth))
				for _, fileResult := range report.Files {
					for _, aoi := range fileResult.AreasOfInterest {
						cls := classifyAOI(aoi, groundTruth, meta)
						switch cls {
						case aoiCovered:
							r.covered++
							for gi, gt := range groundTruth {
								if fileOK, lineOK, _ := matchAOI(aoi, gt); fileOK && lineOK {
									gtMatched[gi] = true
								}
							}
						case aoiAligned:
							r.aligned++
						case aoiHallucinated:
							r.hallucinations++
						}
						if cls != aoiHallucinated {
							if d := aoiLineOffset(aoi, groundTruth); d >= 0 {
								r.offsetSum += d
								r.offsetSamples++
							}
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

				totalGT := r.mustFindTotal + r.niceFindTotal
				if totalGT > 0 {
					r.coveragePct = float64(r.mustFindHits+r.niceFindHits) / float64(totalGT) * 100
				}
				if totalLoC > 0 {
					r.aoiDensity = float64(r.totalAOIs) / float64(totalLoC) * 100
				}
				if r.offsetSamples > 0 {
					r.avgLineOffset = float64(r.offsetSum) / float64(r.offsetSamples)
				}

				allResults = append(allResults, contextResult{spec.name, tc.label, r})

				t.Logf("  Must: %d/%d | Nice: %d/%d | Aligned: %d | Halluc: %d | Coverage: %.1f%% | Cost: $%.4f | Time: %.1fs",
					r.mustFindHits, r.mustFindTotal,
					r.niceFindHits, r.niceFindTotal,
					r.aligned, r.hallucinations,
					r.coveragePct, r.cost, elapsed.Seconds())

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
	t.Logf("  %-30s %4s %7s %7s %6s %5s %8s %9s %7s",
		"Model", "Ctx", "Must", "Nice", "Halluc", "AOIs", "Coverage", "Cost", "Time")
	t.Logf("  %-30s %4s %7s %7s %6s %5s %8s %9s %7s",
		strings.Repeat("─", 30), "────", "───────", "───────", "──────", "─────", "────────", "─────────", "───────")

	for _, cr := range allResults {
		r := cr.result
		if r.err != nil {
			t.Logf("  %-30s %4s   ERROR: %v", cr.modelName, cr.context, r.err)
			continue
		}
		t.Logf("  %-30s %4s %3d/%-3d %3d/%-3d %6d %5d   %5.1f%%  $%.4f %6.1fs",
			cr.modelName, cr.context,
			r.mustFindHits, r.mustFindTotal,
			r.niceFindHits, r.niceFindTotal,
			r.hallucinations, r.totalAOIs,
			r.coveragePct, r.cost, r.duration.Seconds())
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

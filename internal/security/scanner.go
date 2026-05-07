package security

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
)

//go:embed prompts/aoi_scan.md
var aoiScanPrompt string

//go:embed prompts/revalidate.md
var revalidatePrompt string

// AOIScanPrompt returns the AOI scan system prompt for PR review mode.
func AOIScanPrompt() string { return buildAOIScanPrompt(false) }

// AOIAuditPrompt returns the AOI scan system prompt for full-project audit mode.
func AOIAuditPrompt() string { return buildAOIScanPrompt(true) }

const prModeRules = `1. ONLY flag code in the DIFF (added or modified lines, the + lines).
2. Do NOT flag pre-existing code that was not changed.
3. Use the CONTEXT lines (unchanged lines around the diff hunks) to understand
   data flow — trace where variables originate and how they reach sinks.
   The diff may include extra context lines beyond the standard 3 to help you
   see the full picture. Use them.`

const auditModeRules = `1. Scan ALL code in the file — this is a full-project audit, not a diff review.
2. Flag any code location that could contain a bug, vulnerability, or design flaw.
3. Use the full file context to understand data flow, variable origins, and sinks.`

// buildAOIScanPrompt composes the AOI scan prompt template with all dimension
// partials injected at the {DIMENSIONS} placeholder.
func buildAOIScanPrompt(auditMode bool) string {
	return buildAOIScanPromptWithDimensions(auditMode, nil)
}

// buildAOIScanPromptWithDimensions composes the AOI scan prompt with specific
// dimensions. If dims is nil or empty, all dimensions are included.
func buildAOIScanPromptWithDimensions(auditMode bool, dims []string) string {
	var dimensionContent string
	if len(dims) > 0 {
		dimensionContent = ai.GetDimensions(dims)
	} else {
		dimensionContent = ai.AllDimensions()
	}
	prompt := strings.Replace(aoiScanPrompt, "{DIMENSIONS}", dimensionContent, 1)
	rules := prModeRules
	if auditMode {
		rules = auditModeRules
	}
	return strings.Replace(prompt, "{MODE_RULES}", rules, 1)
}

// RevalidatePrompt returns the embedded revalidation system prompt.
func RevalidatePrompt() string { return revalidatePrompt }

// aoiBatchMaxChars is the max diff size per AOI scan batch.
// Kept generous since the cheap model handles large contexts fast.
const aoiBatchMaxChars = 30000

// aoiMaxConcurrency is the max number of AOI batches that run in parallel.
// Capped to avoid hitting API rate limits on the provider.
const aoiMaxConcurrency = 5

// ScanAreasOfInterest runs the AOI pre-scan on all changed files using
// a lightweight LLM. It batches files by dimension set (or all together
// if no classifications are provided) and runs up to aoiMaxConcurrency
// batches in parallel.
//
// cachedResults maps file paths to previously cached AOIScanResult entries.
// Files with cached results are skipped — only uncached files are sent to
// the LLM. Pass nil to scan everything.
//
// The onProgress callback is called with status updates for the UI.
// The client should be configured with a cheap/fast model.
func ScanAreasOfInterest(
	ctx context.Context,
	client ai.Client,
	rawDiffs map[string]string,
	cachedResults map[string]*AOIScanResult,
	onProgress func(status string),
) (*AOIReport, error) {
	return ScanAreasOfInterestDebug(ctx, client, rawDiffs, cachedResults, onProgress, nil, false)
}

// AOIDebugHook is called for each LLM call in the AOI scanner with the prompt, input, and response.
type AOIDebugHook func(files []string, systemPrompt string, userMessage string, response string)

// ScanAreasOfInterestDebug is like ScanAreasOfInterest but with an optional debug hook.
func ScanAreasOfInterestDebug(
	ctx context.Context,
	client ai.Client,
	rawDiffs map[string]string,
	cachedResults map[string]*AOIScanResult,
	onProgress func(status string),
	debugHook AOIDebugHook,
	auditMode bool,
) (*AOIReport, error) {
	return ScanAreasOfInterestClassified(ctx, client, rawDiffs, cachedResults, nil, onProgress, debugHook, auditMode)
}

// ScanAreasOfInterestClassified is like ScanAreasOfInterestDebug but with
// per-file dimension filtering. fileDimensions maps file paths to their
// dimension slugs. Files not in the map get all dimensions. If fileDimensions
// is nil, all files get all dimensions.
func ScanAreasOfInterestClassified(
	ctx context.Context,
	client ai.Client,
	rawDiffs map[string]string,
	cachedResults map[string]*AOIScanResult,
	fileDimensions map[string][]string,
	onProgress func(status string),
	debugHook AOIDebugHook,
	auditMode bool,
) (*AOIReport, error) {
	// Separate cached vs uncached files
	uncachedDiffs := make(map[string]string)
	var cachedAOIs []AOIScanResult

	for filePath, diff := range rawDiffs {
		if cached, ok := cachedResults[filePath]; ok && cached != nil {
			cachedAOIs = append(cachedAOIs, *cached)
		} else {
			uncachedDiffs[filePath] = diff
		}
	}

	if len(cachedAOIs) > 0 && onProgress != nil {
		onProgress(fmt.Sprintf("using cached AOI results for %d file(s)", len(cachedAOIs)))
	}

	batches := buildAOIBatchesClassified(uncachedDiffs, fileDimensions)
	log.Printf("[aoi-debug] built %d batches from %d uncached files", len(batches), len(uncachedDiffs))
	if len(batches) == 0 && len(cachedAOIs) == 0 {
		return &AOIReport{}, nil
	}

	if len(batches) == 0 {
		// All files were cached
		if onProgress != nil {
			onProgress("all AOI results from cache")
		}
		report := buildReport(cachedAOIs)
		return report, nil
	}

	if onProgress != nil {
		onProgress(fmt.Sprintf("scanning %d file(s) for areas of interest (%d cached)...", countFiles(batches), len(cachedAOIs)))
	}

	// Run batches in parallel with bounded concurrency.
	type batchResult struct {
		index   int
		results []AOIScanResult
		err     error
	}

	resultsCh := make(chan batchResult, len(batches))
	sem := make(chan struct{}, aoiMaxConcurrency)
	var wg sync.WaitGroup

	for i, batch := range batches {
		wg.Add(1)
		go func(i int, batch aoiBatch) {
			defer wg.Done()

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				resultsCh <- batchResult{index: i, err: ctx.Err()}
				return
			}

			results, err := scanBatch(ctx, client, batch, debugHook, auditMode)
			resultsCh <- batchResult{index: i, results: results, err: err}
		}(i, batch)
	}

	// Close channel when all goroutines finish
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	// Collect results in order of completion, report sequential progress
	allResults := make([][]AOIScanResult, len(batches))
	completed := 0
	var batchErrors []string
	for br := range resultsCh {
		completed++
		if br.err != nil {
			errMsg := fmt.Sprintf("AOI scan batch %d/%d failed: %v", br.index+1, len(batches), br.err)
			log.Printf("%s", errMsg)
			batchErrors = append(batchErrors, errMsg)
			if onProgress != nil {
				onProgress(errMsg)
			}
		} else {
			allResults[br.index] = br.results
		}
		if onProgress != nil {
			onProgress(fmt.Sprintf("AOI scan %d/%d complete", completed, len(batches)))
		}
	}

	// If all batches failed, return an error so the caller knows no scanning occurred
	if len(batchErrors) == len(batches) {
		return nil, fmt.Errorf("all %d AOI scan batch(es) failed:\n  %s", len(batches), strings.Join(batchErrors, "\n  "))
	}

	// Log partial failures as a warning
	if len(batchErrors) > 0 {
		log.Printf("WARNING: %d/%d AOI scan batches failed", len(batchErrors), len(batches))
	}

	// Flatten in batch order, then append cached results
	var flat []AOIScanResult
	for _, r := range allResults {
		flat = append(flat, r...)
	}
	flat = append(flat, cachedAOIs...)

	report := buildReport(flat)
	return report, nil
}

// RevalidateFindings runs a security-focused revalidation pass on the
// security-category findings from a review. Returns revalidation verdicts.
func RevalidateFindings(
	ctx context.Context,
	client ai.Client,
	findings []FindingForRevalidation,
	onProgress func(status string),
) ([]Revalidation, error) {
	if len(findings) == 0 {
		return nil, nil
	}

	if onProgress != nil {
		onProgress(fmt.Sprintf("revalidating %d security finding(s)...", len(findings)))
	}

	findingsJSON, err := json.Marshal(findings)
	if err != nil {
		return nil, fmt.Errorf("marshal findings: %w", err)
	}

	messages := []ai.Message{
		{Role: "user", Content: fmt.Sprintf(
			"Revalidate these %d security findings. Use tools to verify each one against the actual code.\n\n%s",
			len(findings), string(findingsJSON),
		)},
	}

	result, err := client.ChatStream(ctx, revalidatePrompt, messages, nil)
	if err != nil {
		return nil, fmt.Errorf("revalidation: %w", err)
	}

	return parseRevalidationResult(result)
}

// FindingForRevalidation is a simplified finding struct for the revalidation prompt.
type FindingForRevalidation struct {
	Index      int    `json:"finding_index"`
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Suggestion string `json:"suggestion,omitempty"`
	CWE        string `json:"cwe,omitempty"`
}

// ── AOI batch logic ────────────────────────────────────────────────────

type aoiBatch struct {
	label      string
	files      []string
	diffs      string
	dimensions []string // dimension slugs for this batch (nil = all)
}

func buildAOIBatches(rawDiffs map[string]string) []aoiBatch {
	return buildAOIBatchesClassified(rawDiffs, nil)
}

// dimensionKey returns a stable string key for a set of dimension slugs.
// Used to group files with the same dimensions into the same batch.
func dimensionKey(dims []string) string {
	if len(dims) == 0 {
		return "_all_"
	}
	sorted := make([]string, len(dims))
	copy(sorted, dims)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func buildAOIBatchesClassified(rawDiffs map[string]string, fileDimensions map[string][]string) []aoiBatch {
	// Group by dimension set, skip excluded files
	type fileEntry struct {
		path string
		diff string
	}
	groups := make(map[string][]fileEntry)
	groupDims := make(map[string][]string)

	for p, diff := range rawDiffs {
		if config.ShouldExcludeFromReview(p) {
			continue
		}
		var dims []string
		if fileDimensions != nil {
			dims = fileDimensions[p]
		}
		key := dimensionKey(dims)
		groups[key] = append(groups[key], fileEntry{path: p, diff: diff})
		if _, ok := groupDims[key]; !ok {
			groupDims[key] = dims
		}
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var batches []aoiBatch
	for _, key := range keys {
		entries := groups[key]
		dims := groupDims[key]

		// Sort files within group for determinism
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].path < entries[j].path
		})

		var curFiles []string
		var curDiff strings.Builder

		for _, e := range entries {
			entry := fmt.Sprintf("=== %s ===\n%s\n\n", e.path, e.diff)

			if curDiff.Len() > 0 && curDiff.Len()+len(entry) > aoiBatchMaxChars {
				batches = append(batches, aoiBatch{
					label:      key,
					files:      curFiles,
					diffs:      curDiff.String(),
					dimensions: dims,
				})
				curFiles = nil
				curDiff.Reset()
			}

			curDiff.WriteString(entry)
			curFiles = append(curFiles, e.path)
		}

		if len(curFiles) > 0 {
			batches = append(batches, aoiBatch{
				label:      key,
				files:      curFiles,
				diffs:      curDiff.String(),
				dimensions: dims,
			})
		}
	}

	return batches
}

func countFiles(batches []aoiBatch) int {
	n := 0
	for _, b := range batches {
		n += len(b.files)
	}
	return n
}

// scanBatch sends a single batch of diffs to the AOI scanner.
func scanBatch(ctx context.Context, client ai.Client, batch aoiBatch, debugHook AOIDebugHook, auditMode bool) ([]AOIScanResult, error) {
	systemPrompt := buildAOIScanPromptWithDimensions(auditMode, batch.dimensions)
	userMsg := fmt.Sprintf(
		"Scan these %d file(s) for areas of interest:\n\n%s",
		len(batch.files), batch.diffs,
	)

	messages := []ai.Message{
		{Role: "user", Content: userMsg},
	}

	log.Printf("[aoi-debug] calling LLM for batch %q (%d files, %d chars)", batch.label, len(batch.files), len(userMsg))

	result, err := client.ChatStream(ctx, systemPrompt, messages, nil)
	if err != nil {
		return nil, err
	}

	log.Printf("[aoi-debug] LLM response length: %d chars", len(result))

	// Debug hook
	if debugHook != nil {
		debugHook(batch.files, systemPrompt, userMsg, result)
	}

	if strings.TrimSpace(result) == "" {
		return nil, fmt.Errorf("LLM returned empty response for batch %q (%d files)", batch.label, len(batch.files))
	}

	parsed, parseErr := parseAOIResult(result)
	if parseErr != nil {
		// Log a truncated snippet of the raw response to aid debugging
		snippet := result
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return nil, fmt.Errorf("%w (raw response: %s)", parseErr, snippet)
	}

	log.Printf("[aoi-debug] parsed %d file results", len(parsed))
	return parsed, nil
}

// ── Parsing ────────────────────────────────────────────────────────────

// sanitizeJSON cleans up common LLM JSON quirks that break strict parsing:
//   - Literal tabs inside strings (invalid per RFC 8259, but models emit them)
//   - Other control characters that occasionally appear
func sanitizeJSON(s string) string {
	// Replace literal tabs with \t escape sequences.
	// We can't blindly replace all tabs since they might be outside strings,
	// but tabs outside strings are just whitespace that json.Unmarshal handles.
	// The problem is tabs INSIDE strings — replace all literal tabs with spaces
	// which is safe for both in-string and whitespace positions.
	s = strings.ReplaceAll(s, "\t", " ")
	return s
}

func parseAOIResult(raw string) ([]AOIScanResult, error) {
	s := strings.TrimSpace(raw)

	// Strip markdown code fences
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	// Try to find the JSON array
	if !strings.HasPrefix(s, "[") {
		start := strings.Index(s, "[")
		if start == -1 {
			return nil, fmt.Errorf("no JSON array found in AOI response")
		}
		s = s[start:]
	}

	var results []AOIScanResult
	s = sanitizeJSON(s)
	if err := json.Unmarshal([]byte(s), &results); err != nil {
		return nil, fmt.Errorf("parse AOI JSON: %w", err)
	}

	// Normalize: merge Areas into AreasOfInterest for backward compat,
	// and propagate the parent file path into each AOI.
	for i := range results {
		results[i].NormalizeAOIs()
		for j := range results[i].AreasOfInterest {
			if results[i].AreasOfInterest[j].File == "" {
				results[i].AreasOfInterest[j].File = results[i].File
			}
		}
	}

	return results, nil
}

func parseRevalidationResult(raw string) ([]Revalidation, error) {
	s := strings.TrimSpace(raw)

	// Strip markdown code fences
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	if !strings.HasPrefix(s, "[") {
		start := strings.Index(s, "[")
		if start == -1 {
			return nil, fmt.Errorf("no JSON array found in revalidation response")
		}
		s = s[start:]
	}

	// Parse into intermediate struct that includes finding_index
	type revalEntry struct {
		FindingIndex int    `json:"finding_index"`
		Verdict      string `json:"verdict"`
		Reasoning    string `json:"reasoning"`
		Confidence   string `json:"confidence"`
		CWE          string `json:"cwe,omitempty"`
	}

	var entries []revalEntry
	s = sanitizeJSON(s)
	if err := json.Unmarshal([]byte(s), &entries); err != nil {
		return nil, fmt.Errorf("parse revalidation JSON: %w", err)
	}

	results := make([]Revalidation, len(entries))
	for i, e := range entries {
		results[i] = Revalidation{
			Verdict:    e.Verdict,
			Reasoning:  e.Reasoning,
			Confidence: e.Confidence,
			CWE:        e.CWE,
		}
	}

	return results, nil
}

// ── Report building ────────────────────────────────────────────────────

func buildReport(results []AOIScanResult) *AOIReport {
	report := &AOIReport{
		Files: results,
	}

	for _, r := range results {
		report.TotalAOIs += len(r.AreasOfInterest)
	}

	report.SecurityDigest = formatDigest(report)
	return report
}

// formatDigest produces a human-readable summary for injection into review prompts.
func formatDigest(report *AOIReport) string {
	if report.TotalAOIs == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Pre-Scan: %d Areas of Interest Found\n\n", report.TotalAOIs))

	// Group AOIs by category (or category/subcategory) for a compact view
	catCounts := make(map[string]int)
	for _, r := range report.Files {
		for _, aoi := range r.AreasOfInterest {
			key := aoi.Category
			if aoi.Subcategory != "" {
				key = aoi.Category + "/" + aoi.Subcategory
			}
			catCounts[key]++
		}
	}

	sb.WriteString("AOI breakdown by category:\n")
	// Sort categories by count (descending)
	type catCount struct {
		cat   string
		count int
	}
	var sorted []catCount
	for c, n := range catCounts {
		sorted = append(sorted, catCount{c, n})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
	for _, cc := range sorted {
		sb.WriteString(fmt.Sprintf("- %s: %d\n", cc.cat, cc.count))
	}
	sb.WriteString("\n")

	// List individual AOIs grouped by file
	sb.WriteString("### Detailed AOI Locations\n\n")
	for _, r := range report.Files {
		if len(r.AreasOfInterest) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("**%s**\n", r.File))
		for _, aoi := range r.AreasOfInterest {
			lineRange := fmt.Sprintf("L%d", aoi.Line)
			if aoi.EndLine > 0 && aoi.EndLine != aoi.Line {
				lineRange = fmt.Sprintf("L%d-%d", aoi.Line, aoi.EndLine)
			}

			// Format depends on whether this is new-format (with subcategory) or legacy
			cat := aoi.Category
			if aoi.Subcategory != "" {
				cat = aoi.Category + "/" + aoi.Subcategory
			}

			desc := aoi.Reasoning // legacy
			if aoi.Concern != "" {
				desc = aoi.Concern // new format
			}

			urgencyTag := ""
			if aoi.Urgency == "individual" {
				urgencyTag = " [!!]"
			}

			confTag := ""
			if aoi.Confidence != "" {
				confTag = fmt.Sprintf(" (%s)", aoi.Confidence)
			}

			sb.WriteString(fmt.Sprintf("  - [%s] %s%s%s: %s\n",
				cat, lineRange, confTag, urgencyTag, desc))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

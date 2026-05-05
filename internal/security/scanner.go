package security

import (
	"context"
	"encoding/json"
	_ "embed"
	"fmt"
	"log"
	"path/filepath"
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

// AOIScanPrompt returns the embedded AOI scan system prompt.
func AOIScanPrompt() string { return aoiScanPrompt }

// RevalidatePrompt returns the embedded revalidation system prompt.
func RevalidatePrompt() string { return revalidatePrompt }

// aoiBatchMaxChars is the max diff size per AOI scan batch.
// Kept generous since the cheap model handles large contexts fast.
const aoiBatchMaxChars = 30000

// aoiMaxConcurrency is the max number of AOI batches that run in parallel.
// Capped to avoid hitting API rate limits on the provider.
const aoiMaxConcurrency = 5

// ScanAreasOfInterest runs the AOI pre-scan on all changed files using
// a lightweight LLM. It batches files by directory (like the main review)
// and runs up to aoiMaxConcurrency batches in parallel.
//
// The onProgress callback is called with status updates for the UI.
// The client should be configured with a cheap/fast model.
func ScanAreasOfInterest(
	ctx context.Context,
	client ai.Client,
	rawDiffs map[string]string,
	onProgress func(status string),
) (*AOIReport, error) {
	batches := buildAOIBatches(rawDiffs)
	if len(batches) == 0 {
		return &AOIReport{OverallRisk: "none"}, nil
	}

	if onProgress != nil {
		onProgress(fmt.Sprintf("scanning %d file(s) for security areas of interest...", countFiles(batches)))
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

			if onProgress != nil {
				onProgress(fmt.Sprintf("AOI scan batch %d/%d (%s)...", i+1, len(batches), batch.label))
			}

			results, err := scanBatch(ctx, client, batch)
			resultsCh <- batchResult{index: i, results: results, err: err}
		}(i, batch)
	}

	// Close channel when all goroutines finish
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	// Collect results in order
	allResults := make([][]AOIScanResult, len(batches))
	for br := range resultsCh {
		if br.err != nil {
			log.Printf("AOI scan batch %d failed: %v", br.index+1, br.err)
			continue // non-fatal: we still get results from other batches
		}
		allResults[br.index] = br.results
	}

	// Flatten in batch order
	var flat []AOIScanResult
	for _, r := range allResults {
		flat = append(flat, r...)
	}

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
	label string
	files []string
	diffs string
}

func buildAOIBatches(rawDiffs map[string]string) []aoiBatch {
	// Group by directory, skip excluded files
	dirFiles := make(map[string][]string)
	for p := range rawDiffs {
		if config.ShouldExcludeFromReview(p) {
			continue
		}
		dir := filepath.Dir(p)
		if dir == "." {
			dir = "root"
		}
		dirFiles[dir] = append(dirFiles[dir], p)
	}

	dirs := make([]string, 0, len(dirFiles))
	for d := range dirFiles {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var batches []aoiBatch
	for _, dir := range dirs {
		files := dirFiles[dir]
		sort.Strings(files)

		var curFiles []string
		var curDiff strings.Builder

		for _, f := range files {
			diff := rawDiffs[f]
			entry := fmt.Sprintf("=== %s ===\n%s\n\n", f, diff)

			if curDiff.Len() > 0 && curDiff.Len()+len(entry) > aoiBatchMaxChars {
				batches = append(batches, aoiBatch{
					label: dir,
					files: curFiles,
					diffs: curDiff.String(),
				})
				curFiles = nil
				curDiff.Reset()
			}

			curDiff.WriteString(entry)
			curFiles = append(curFiles, f)
		}

		if len(curFiles) > 0 {
			batches = append(batches, aoiBatch{
				label: dir,
				files: curFiles,
				diffs: curDiff.String(),
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
func scanBatch(ctx context.Context, client ai.Client, batch aoiBatch) ([]AOIScanResult, error) {
	messages := []ai.Message{
		{Role: "user", Content: fmt.Sprintf(
			"Scan these %d file(s) for security areas of interest:\n\n%s",
			len(batch.files), batch.diffs,
		)},
	}

	result, err := client.ChatStream(ctx, aoiScanPrompt, messages, nil)
	if err != nil {
		return nil, err
	}

	return parseAOIResult(result)
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
		Files:       results,
		OverallRisk: "none",
	}

	riskRank := map[string]int{
		"critical": 4,
		"high":     3,
		"medium":   2,
		"low":      1,
		"none":     0,
	}

	maxRisk := 0
	for _, r := range results {
		report.TotalAOIs += len(r.AreasOfInterest)

		rank := riskRank[r.RiskLevel]
		if rank > maxRisk {
			maxRisk = rank
			report.OverallRisk = r.RiskLevel
		}

		if r.RiskLevel == "critical" || r.RiskLevel == "high" {
			report.HighRiskFiles = append(report.HighRiskFiles, r.File)
		}
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
	sb.WriteString(fmt.Sprintf("## Security Pre-Scan: %d Areas of Interest Found\n\n", report.TotalAOIs))
	sb.WriteString(fmt.Sprintf("Overall risk level: **%s**\n\n", report.OverallRisk))

	if len(report.HighRiskFiles) > 0 {
		sb.WriteString("High-risk files requiring extra scrutiny:\n")
		for _, f := range report.HighRiskFiles {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
		sb.WriteString("\n")
	}

	// Group AOIs by category for a compact view
	catCounts := make(map[string]int)
	for _, r := range report.Files {
		for _, aoi := range r.AreasOfInterest {
			catCounts[aoi.Category]++
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
		sb.WriteString(fmt.Sprintf("**%s** (risk: %s)\n", r.File, r.RiskLevel))
		for _, aoi := range r.AreasOfInterest {
			lineRange := fmt.Sprintf("L%d", aoi.Line)
			if aoi.EndLine > 0 && aoi.EndLine != aoi.Line {
				lineRange = fmt.Sprintf("L%d-%d", aoi.Line, aoi.EndLine)
			}
			sb.WriteString(fmt.Sprintf("  - [%s] %s (%s): %s\n",
				aoi.Category, lineRange, aoi.Confidence, aoi.Reasoning))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

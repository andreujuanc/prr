package security

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
)

//go:embed prompts/aoi_scan.md
var aoiScanPrompt string

//go:embed prompts/revalidate.md
var revalidatePrompt string

// AOIScanPrompt returns the AOI scan system prompt for PR review mode.
func AOIScanPrompt() string { return buildAOIScanPrompt(false) }

// AOIScanPromptHash returns a short sha256 hash of the AOI scan
// prompt for cache-invalidation purposes. Mixed into the Phase 2
// AOI cache key so prompt edits (e.g. commit 7's TODO/FIXME +
// unit-type rules) auto-invalidate stale entries.
//
// auditMode selects which prompt variant to hash. The two modes
// embed different mode-specific rules so they need separate hashes.
func AOIScanPromptHash(auditMode bool) string {
	h := sha256.Sum256([]byte(buildAOIScanPrompt(auditMode)))
	return hex.EncodeToString(h[:])
}

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

// aoiMaxConcurrency is the default max number of AOI batches that run in
// parallel. Capped to avoid hitting API rate limits on the provider.
// SetAOIConcurrency overrides this for the lifetime of the process.
const defaultAOIMaxConcurrency = 5

var aoiMaxConcurrency = defaultAOIMaxConcurrency

// SetAOIConcurrency sets the max number of AOI batches run in parallel.
// Values <= 0 reset to the default. Not safe to call concurrently with
// scans in flight; intended to be called once at startup.
func SetAOIConcurrency(n int) {
	if n <= 0 {
		aoiMaxConcurrency = defaultAOIMaxConcurrency
		return
	}
	aoiMaxConcurrency = n
}

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

	batches := buildAOIBatchesClassified(uncachedDiffs, fileDimensions, auditMode)
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
		inputs  []string // file paths the batch was asked to scan
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
				resultsCh <- batchResult{index: i, inputs: batch.files, err: ctx.Err()}
				return
			}

			results, err := scanBatchWithRetry(ctx, client, batch, debugHook, auditMode)
			resultsCh <- batchResult{index: i, inputs: batch.files, results: results, err: err}
		}(i, batch)
	}

	// Close channel when all goroutines finish
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	// Collect results in order of completion, report sequential progress.
	// Track silent LLM drops separately from outright batch failures —
	// a batch that returned 5 of 8 input files looks "successful" but
	// actually lost AOIs for 3 files.
	allResults := make([][]AOIScanResult, len(batches))
	completed := 0
	var (
		batchErrors      []string
		failedInputPaths []string // file paths whose batch errored (no AOIs)
		totalDropped     int      // files in successful batches that the LLM omitted
		droppedFiles     []string // paths of those silently-dropped files
	)
	for br := range resultsCh {
		completed++
		if br.err != nil {
			errMsg := fmt.Sprintf("AOI scan batch %d/%d failed: %v", br.index+1, len(batches), br.err)
			// Log the per-batch error to disk for diagnostics, but
			// don't blast it to onProgress — the consolidated
			// terminal message below is what the user reads.
			log.Printf("%s", errMsg)
			batchErrors = append(batchErrors, errMsg)
			failedInputPaths = append(failedInputPaths, br.inputs...)
		} else {
			allResults[br.index] = br.results

			// Drop detection: every input path must appear in the
			// output. Missing files are silent data loss — the model
			// truncated the response, hit a token limit, or dropped
			// files for no clear reason. Those files end up with NO
			// AOIs and won't be reviewed in Phase 3.
			returned := make(map[string]bool, len(br.results))
			for _, r := range br.results {
				returned[r.File] = true
			}
			for _, in := range br.inputs {
				if !returned[in] {
					totalDropped++
					droppedFiles = append(droppedFiles, in)
				}
			}
		}
		if onProgress != nil {
			// Counter-only emit. Previously this was "AOI scan X/Y
			// complete" which the TUI rendered as the detail line —
			// duplicating the X/Y already shown by the inline counter.
			onProgress(fmt.Sprintf("AOI scan %d/%d", completed, len(batches)))
		}
	}

	if totalDropped > 0 {
		// Surface ONCE at the end — emitting per-batch would
		// out-shout the regular progress line. The full path list
		// goes to the log; the progress message keeps it terse.
		log.Printf("aoi: %d file(s) silently dropped by LLM (no AOIs scanned): %v",
			totalDropped, droppedFiles)
		if onProgress != nil {
			onProgress(fmt.Sprintf(
				"⚠ %d file(s) silently dropped by LLM during AOI scan (no AOIs — see log for paths)",
				totalDropped))
		}
	}

	// If ALL batches failed there's nothing to return — abort with
	// the full error list regardless of count. The 2-batch floor
	// below doesn't cover this: a single-batch run that fails 1/1
	// has no useful results to ship.
	if len(batchErrors) == len(batches) && len(batches) > 0 {
		return nil, fmt.Errorf("all %d AOI scan batch(es) failed:\n  %s",
			len(batches), strings.Join(batchErrors, "\n  "))
	}

	// Aggregate-fail: abort when too many batches fail. Previously
	// only a 100% failure aborted (handled above), so 4-of-5 batches
	// failing (80% of files unscanned) still returned "success" with
	// whatever the one surviving batch produced. Same shape as
	// Phase 1's file-read aggregate-fail.
	if shouldAggregateFailAOI(len(batchErrors), len(batches)) {
		return nil, fmt.Errorf(
			"phase 2: %d/%d AOI scan batches failed (>%.0f%% threshold) — aborting; %d file(s) had no AOIs scanned:\n  %s",
			len(batchErrors), len(batches), aoiAggregateFailRatio*100,
			len(failedInputPaths), strings.Join(batchErrors, "\n  "))
	}

	// Partial failure under the threshold: surface ONCE so the user
	// knows recall is degraded, instead of burying it in per-batch
	// log lines. Per-batch errors are still in the log for forensics.
	if len(batchErrors) > 0 {
		log.Printf("aoi: %d/%d batches failed; %d file(s) have no AOIs and will not be reviewed: %v",
			len(batchErrors), len(batches), len(failedInputPaths), failedInputPaths)
		if onProgress != nil {
			onProgress(fmt.Sprintf(
				"⚠ %d/%d AOI batches failed; %d file(s) will not be deep-reviewed (see log for paths)",
				len(batchErrors), len(batches), len(failedInputPaths)))
		}
	}

	// Flatten in batch order, then append cached results
	var flat []AOIScanResult
	for _, r := range allResults {
		flat = append(flat, r...)
	}
	flat = append(flat, cachedAOIs...)

	report := buildReport(flat)

	// Empty-audit warning: in audit mode, zero AOIs across all files
	// almost always indicates the model is broken or the prompt isn't
	// landing — real codebases have something to flag in audit mode
	// (the "be recall-biased" rule should ensure even nits surface).
	// PR mode is different: a clean PR diff legitimately yields zero
	// AOIs, so we don't warn there.
	if auditMode && report.TotalAOIs == 0 && len(batches) > 0 {
		log.Printf("aoi: audit returned 0 AOIs across %d batch(es) — model may be broken or prompt may not be landing", len(batches))
		if onProgress != nil {
			onProgress(fmt.Sprintf(
				"⚠ audit returned 0 AOIs across %d batch(es) — review prompt may not be landing",
				len(batches)))
		}
	}

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

	// Retry transient HTTP errors.
	result, err := ai.RetryTransient(ctx, 3, "security-revalidate", func(ctx context.Context) (string, error) {
		return client.ChatStream(ctx, revalidatePrompt, messages, nil)
	})
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
	return buildAOIBatchesClassified(rawDiffs, nil, false)
}

// prefixLineNumbers returns body with every line prefixed by its 1-based
// source line number followed by ": ". Numbers are right-padded to the
// width of the largest line number for readable alignment. A trailing
// newline in the input produces a trailing newline in the output; no
// content is added or removed. Intended for audit mode, where the
// scanner sees whole files — PR mode passes unified diffs whose own
// "@@" headers are the canonical line metadata and must not be
// re-numbered.
func prefixLineNumbers(body string) string {
	if body == "" {
		return ""
	}
	hadTrailingNewline := body[len(body)-1] == '\n'
	trimmed := body
	if hadTrailingNewline {
		trimmed = body[:len(body)-1]
	}
	lines := strings.Split(trimmed, "\n")
	width := len(strconv.Itoa(len(lines)))
	var b strings.Builder
	b.Grow(len(body) + (width+2)*len(lines))
	for i, line := range lines {
		fmt.Fprintf(&b, "%*d: %s", width, i+1, line)
		if i < len(lines)-1 || hadTrailingNewline {
			b.WriteByte('\n')
		}
	}
	return b.String()
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

func buildAOIBatchesClassified(rawDiffs map[string]string, fileDimensions map[string][]string, auditMode bool) []aoiBatch {
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
			content := e.diff
			if auditMode {
				content = prefixLineNumbers(content)
			}
			entry := fmt.Sprintf("=== %s ===\n%s\n\n", e.path, content)

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

// errAOIParse marks errors that come from the response shape (bad JSON,
// missing array, empty response). Distinguished from transport-side
// errors (network, rate-limit, 5xx) so retry can short-circuit: re-
// running the same prompt twice won't fix a parse failure, just doubles
// the token spend.
var errAOIParse = errors.New("aoi: parse failure")

// validateAOIs surfaces semantic issues with parsed AOI results that
// would silently propagate into Phase 3 routing:
//
//   - Empty or out-of-taxonomy `category` values. Phase 3 buckets AOIs
//     by subcategory; a category like "shitposting" or "" would still
//     be bucketed somewhere and pollute that bucket's review.
//   - Dimension slugs not in the partial taxonomy. Used by --focus
//     filtering; an unknown dim is silently dropped from filtering.
//   - Duplicate IDs within a single file. IDs feed caching and
//     cross-referencing; duplicates corrupt those.
//
// All issues are logged (no coercion). The model's output is left
// intact so the user can see what the LLM emitted vs what we wanted.
// Coercion would hide prompt-drift problems that need human attention.
func validateAOIs(results []AOIScanResult) {
	for _, r := range results {
		seen := make(map[string]int, len(r.AreasOfInterest))
		for _, aoi := range r.AreasOfInterest {
			if aoi.Category == "" {
				log.Printf("aoi: %s [id=%s] missing category (model output, not coerced)",
					r.File, aoi.ID)
			} else if !ai.DimensionExists(aoi.Category) {
				log.Printf("aoi: %s [id=%s] uses out-of-taxonomy category %q (not coerced; Phase 3 routing may be off)",
					r.File, aoi.ID, aoi.Category)
			}

			for _, d := range aoi.Dimensions {
				if !ai.DimensionExists(d) {
					log.Printf("aoi: %s [id=%s] uses unknown dimension %q (will be ignored by --focus filtering)",
						r.File, aoi.ID, d)
				}
			}

			if aoi.ID != "" {
				seen[aoi.ID]++
				if seen[aoi.ID] == 2 {
					// Log the SECOND occurrence so we don't spam for triples,
					// while still surfacing the collision.
					log.Printf("aoi: %s has duplicate AOI id %q (caching and cross-referencing will collide)",
						r.File, aoi.ID)
				}
			}
		}
	}
}

// aoiRetryBackoff is the wait before the single retry of a transient
// AOI batch failure. Slightly longer than classify's 750ms because AOI
// batches are larger (more tokens to retransmit) so the API is more
// likely to be rate-limiting us.
const aoiRetryBackoff = 1 * time.Second

// Aggregate-fail thresholds for Phase 2 AOI batches.
//
// Previously a Phase 2 scan only aborted when 100% of batches failed.
// 4 of 5 failing (20% recall) was "successful". With a >20% threshold
// and a 2-batch floor, a handful of transient failures still proceed
// with a clear warning, but a structural breakdown aborts cleanly.
const (
	aoiAggregateFailRatio    = 0.20
	aoiAggregateFailMinBatch = 2
)

// shouldAggregateFailAOI reports whether the (failed, total) batch
// counts cross the abort threshold.
func shouldAggregateFailAOI(failed, total int) bool {
	if failed < aoiAggregateFailMinBatch {
		return false
	}
	if total <= 0 {
		return false
	}
	return float64(failed)/float64(total) > aoiAggregateFailRatio
}

// scanBatchWithRetry runs scanBatch and retries ONCE on transient
// errors. Parse failures (errAOIParse) short-circuit — they reflect
// a model issue, not a network blip. Per-call HTTP timeouts on a live
// parent are treated as transient via ai.IsTransientError.
func scanBatchWithRetry(ctx context.Context, client ai.Client, batch aoiBatch, debugHook AOIDebugHook, auditMode bool) ([]AOIScanResult, error) {
	res, err := scanBatch(ctx, client, batch, debugHook, auditMode)
	if err == nil {
		return res, nil
	}
	if errors.Is(err, errAOIParse) {
		return res, err
	}
	if !ai.IsTransientError(err, ctx) {
		return res, err
	}

	select {
	case <-time.After(aoiRetryBackoff):
	case <-ctx.Done():
		return res, ctx.Err()
	}
	log.Printf("aoi: retrying batch %q (%d files) after transient error: %v",
		batch.label, len(batch.files), err)
	return scanBatch(ctx, client, batch, debugHook, auditMode)
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

	// Resolve {{TOOLS}} before ChatStream so the debug hook sees
	// the same text the LLM sees (Agent.ChatStream resolves on a
	// local copy of its parameter).
	systemPrompt = ai.ResolveToolsForClient(client, systemPrompt)

	log.Printf("[aoi-debug] calling LLM for batch %q (%d files, %d chars)", batch.label, len(batch.files), len(userMsg))

	// Single ChatStream call. Retry for transient HTTP errors lives
	// in scanBatchWithRetry one level up — nesting retries here would
	// multiply attempts and confuse the batch-error accounting.
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
		// Empty response is a parse-shape failure — retry won't make
		// a silent model start emitting JSON. Wrap with errAOIParse
		// so scanBatchWithRetry short-circuits.
		return nil, fmt.Errorf("%w: LLM returned empty response for batch %q (%d files)",
			errAOIParse, batch.label, len(batch.files))
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

	// Surface semantic issues in the parsed results (invalid categories,
	// unknown dimensions, duplicate IDs). Informational only — output
	// is not modified, so the caller can still see what the model
	// emitted.
	validateAOIs(parsed)

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
			return nil, fmt.Errorf("%w: no JSON array found in AOI response", errAOIParse)
		}
		s = s[start:]
	}

	var results []AOIScanResult
	s = sanitizeJSON(s)
	if err := json.Unmarshal([]byte(s), &results); err != nil {
		return nil, fmt.Errorf("%w: parse AOI JSON: %v", errAOIParse, err)
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

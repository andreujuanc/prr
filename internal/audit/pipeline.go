package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/project"
	"github.com/andreujuanc/prr/internal/review"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// Options configures an audit run.
type Options struct {
	// RepoRoot is the absolute path to the repository root.
	RepoRoot string

	// Focus is a list of dimension slugs to focus on. Empty = all.
	Focus []string

	// ExcludePatterns are additional globs to exclude (from --exclude and .prr/audit-exclude).
	ExcludePatterns []string

	// IncludePatterns are globs to force-include (from --include).
	IncludePatterns []string

	// MaxReviews caps the number of Phase 3 review calls. 0 = no limit.
	MaxReviews int

	// NoCache disables incremental caching — re-audit everything.
	NoCache bool

	// AOIContextLines is the number of context lines for AOI generation.
	AOIContextLines int
}

// Result holds the output of an audit run.
type Result struct {
	// FilesScanned is the number of files that passed Phase 1 filtering.
	FilesScanned int

	// AOIsGenerated is the total number of AOIs from Phase 2.
	AOIsGenerated int

	// ReviewCalls is the number of Phase 3 LLM calls made.
	ReviewCalls int

	// IndividualReviews is the count of individual review calls.
	IndividualReviews int

	// GroupedReviews is the count of grouped review calls.
	GroupedReviews int

	// Findings is all confirmed findings from Phase 3.
	Findings []state.DeepFinding

	// Dismissals is all dismissed AOIs from Phase 3.
	Dismissals int

	// CrossCuttingObservations from grouped reviews.
	CrossCuttingObservations []string

	// SkippedSubcategories lists subcategories skipped due to --max-reviews.
	SkippedSubcategories []string

	// Routing is the Phase 3 routing result (for summary display).
	Routing *review.RouteResult
}

// OnProgress is called with status updates during the audit.
type OnProgress func(phase string, message string)

// Run executes the full audit pipeline (Phases 0-3).
// Phase 4 (synthesis) is handled separately by the caller.
func Run(
	ctx context.Context,
	reviewClient ai.Client,
	aoiClient ai.Client,
	opts Options,
	onProgress OnProgress,
) (*Result, error) {
	if onProgress == nil {
		onProgress = func(_, _ string) {}
	}

	// Load or create audit state
	auditState, err := state.Load("audit")
	if err != nil {
		return nil, fmt.Errorf("loading audit state: %w", err)
	}

	if opts.NoCache {
		auditState.ClearAllCaches()
	}

	// ── Phase 0: Project Context Discovery ──────────────────────────────

	onProgress("phase0", "Discovering project context...")
	projectContext, err := runPhase0(ctx, aoiClient, opts.RepoRoot, auditState, onProgress)
	if err != nil {
		log.Printf("Phase 0 (project context) failed: %v", err)
		// Non-fatal — continue without project context
	}

	// ── Phase 1: File Collection ────────────────────────────────────────

	onProgress("phase1", "Collecting files...")
	files, err := CollectFiles(opts.RepoRoot, opts.ExcludePatterns, opts.IncludePatterns)
	if err != nil {
		return nil, fmt.Errorf("phase 1 file collection: %w", err)
	}
	onProgress("phase1", fmt.Sprintf("Phase 1 complete: %d files to audit", len(files)))

	if len(files) == 0 {
		return &Result{}, nil
	}

	// ── Phase 2: AOI Generation ─────────────────────────────────────────

	onProgress("phase2", fmt.Sprintf("Scanning %d files for areas of interest...", len(files)))
	aoiResults, err := runPhase2(ctx, aoiClient, opts, auditState, files, onProgress)
	if err != nil {
		return nil, fmt.Errorf("phase 2 AOI generation: %w", err)
	}

	// Save state after Phase 2
	if err := state.Save(auditState); err != nil {
		log.Printf("Warning: failed to save audit state after Phase 2: %v", err)
	}

	// ── Phase 3: Deep Review (routing + execution) ──────────────────────

	routing := review.RouteAOIs(aoiResults, opts.Focus, 10)
	onProgress("phase3", routing.FormatSummary())

	result := &Result{
		FilesScanned:         len(files),
		AOIsGenerated:        routing.TotalAOIs,
		Routing:              routing,
		SkippedSubcategories: routing.SkippedSubcategories(opts.MaxReviews),
	}

	if routing.TotalAOIs == 0 {
		onProgress("phase3", "No areas of interest found — audit complete.")
		return result, nil
	}

	calls := routing.PrioritizedCalls(opts.MaxReviews)
	onProgress("phase3", fmt.Sprintf("Executing %d review calls...", len(calls)))

	findings, dismissals, crossCutting, err := runPhase3(
		ctx, reviewClient, opts, auditState, projectContext, calls, onProgress,
	)
	if err != nil {
		return nil, fmt.Errorf("phase 3 deep review: %w", err)
	}

	// Save state after Phase 3
	if err := state.Save(auditState); err != nil {
		log.Printf("Warning: failed to save audit state after Phase 3: %v", err)
	}

	result.Findings = findings
	result.Dismissals = dismissals
	result.CrossCuttingObservations = crossCutting
	result.ReviewCalls = len(calls)
	result.IndividualReviews = len(routing.Individual)
	result.GroupedReviews = len(routing.Grouped)

	return result, nil
}

// ── Phase 0: Project Context ────────────────────────────────────────────

func runPhase0(
	ctx context.Context,
	client ai.Client,
	repoRoot string,
	auditState *state.State,
	onProgress OnProgress,
) (string, error) {
	cachedSummary, cachedHash := auditState.GetProjectContext()

	result, err := project.Discover(ctx, repoRoot, client, cachedHash, func(status string) {
		onProgress("phase0", status)
	})
	if err != nil {
		return cachedSummary, err
	}

	if result.FromCache {
		return cachedSummary, nil
	}

	// Cache the new context
	auditState.SetProjectContext(result.Summary, result.InputHash)
	return result.Summary, nil
}

// ── Phase 2: AOI Generation ─────────────────────────────────────────────

func runPhase2(
	ctx context.Context,
	aoiClient ai.Client,
	opts Options,
	auditState *state.State,
	files []string,
	onProgress OnProgress,
) ([]security.AOIScanResult, error) {
	// Read file contents and build "diffs" (for audit mode, the full file content)
	fileContents := make(map[string]string)
	cachedResults := make(map[string]*security.AOIScanResult)

	for _, filePath := range files {
		absPath := filepath.Join(opts.RepoRoot, filePath)
		content, err := os.ReadFile(absPath)
		if err != nil {
			log.Printf("Warning: skipping %s: %v", filePath, err)
			continue
		}

		contentStr := string(content)

		// Check cache: if file content hash matches, reuse cached AOI results
		contentHash := hashContent(contentStr)
		if !opts.NoCache {
			raw, cachedCtxLines := auditState.GetAOIResults(filePath)
			if raw != nil && cachedCtxLines == opts.AOIContextLines {
				// Check if the content hash matches
				if fs, ok := auditState.Files[filePath]; ok && fs.DiffHash == contentHash {
					var cached security.AOIScanResult
					if err := json.Unmarshal(raw, &cached); err == nil {
						cached.NormalizeAOIs()
						cachedResults[filePath] = &cached
						continue
					}
				}
			}
		}

		// Store content hash for future cache validation
		ensureFileState(auditState, filePath, contentHash)
		fileContents[filePath] = contentStr
	}

	if len(fileContents) == 0 && len(cachedResults) > 0 {
		onProgress("phase2", fmt.Sprintf("All %d files from AOI cache", len(cachedResults)))
		// Collect cached results
		var results []security.AOIScanResult
		for _, r := range cachedResults {
			results = append(results, *r)
		}
		return results, nil
	}

	// Run AOI scan on uncached files
	report, err := security.ScanAreasOfInterest(ctx, aoiClient, fileContents, cachedResults, func(status string) {
		onProgress("phase2", status)
	})
	if err != nil {
		return nil, err
	}

	// Cache results per-file
	for _, fileResult := range report.Files {
		data, err := json.Marshal(fileResult)
		if err != nil {
			continue
		}
		auditState.SetAOIResults(fileResult.File, data, opts.AOIContextLines)
	}

	return report.Files, nil
}

// ── Phase 3: Deep Review ────────────────────────────────────────────────

const phase3MaxConcurrency = 10

func runPhase3(
	ctx context.Context,
	reviewClient ai.Client,
	opts Options,
	auditState *state.State,
	projectContext string,
	calls []review.ReviewCall,
	onProgress OnProgress,
) (findings []state.DeepFinding, dismissals int, crossCutting []string, err error) {
	type callResult struct {
		index    int
		result   *state.DeepReviewResult
		err      error
		fromCache bool
	}

	resultsCh := make(chan callResult, len(calls))
	sem := make(chan struct{}, phase3MaxConcurrency)
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(i int, call review.ReviewCall) {
			defer wg.Done()

			// Check cache
			cacheKey := computeCacheKey(call, opts)
			if !opts.NoCache {
				if cached := auditState.GetDeepReview(cacheKey); cached != nil {
					resultsCh <- callResult{index: i, result: cached, fromCache: true}
					return
				}
			}

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				resultsCh <- callResult{index: i, err: ctx.Err()}
				return
			}

			// Build prompt and execute
			var systemPrompt string
			if call.Type == "individual" {
				systemPrompt = review.BuildIndividualPrompt(
					review.ModeAudit, projectContext, "", call.AOIs[0],
				)
			} else {
				systemPrompt = review.BuildGroupedPrompt(
					review.ModeAudit, projectContext, "", call,
				)
			}

			messages := []ai.Message{
				{Role: "user", Content: "Investigate the area(s) of interest described in the system prompt. Use tools to verify."},
			}

			// Phase 3 review client has tools
			raw, callErr := reviewClient.ChatStream(ctx, systemPrompt, messages, nil)
			if callErr != nil {
				resultsCh <- callResult{index: i, err: callErr}
				return
			}

			result := parseDeepReviewResult(call, raw)
			result.CacheKey = cacheKey

			// Cache the result
			auditState.SetDeepReview(cacheKey, result)

			resultsCh <- callResult{index: i, result: result}
		}(i, call)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	// Collect results
	completed := 0
	cachedCount := 0
	for cr := range resultsCh {
		completed++
		if cr.fromCache {
			cachedCount++
		}
		if cr.err != nil {
			log.Printf("Phase 3 call %d failed: %v", cr.index+1, cr.err)
			onProgress("phase3", fmt.Sprintf("Review %d/%d failed: %v", completed, len(calls), cr.err))
			continue
		}
		if cr.result != nil {
			findings = append(findings, cr.result.Findings...)
			dismissals += len(cr.result.Dismissals)
			if cr.result.CrossCutting != "" {
				crossCutting = append(crossCutting, cr.result.CrossCutting)
			}
		}
		cacheTag := ""
		if cr.fromCache {
			cacheTag = " (cached)"
		}
		onProgress("phase3", fmt.Sprintf("Review %d/%d complete%s", completed, len(calls), cacheTag))
	}

	// Sort findings by severity
	sort.Slice(findings, func(i, j int) bool {
		return severityRank(findings[i].Severity) < severityRank(findings[j].Severity)
	})

	return findings, dismissals, crossCutting, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────

func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])[:32]
}

func ensureFileState(s *state.State, path, contentHash string) {
	s.SetBatchFindings(path, "", "") // ensure FileState exists
	if fs, ok := s.Files[path]; ok {
		fs.DiffHash = contentHash
	}
}

func computeCacheKey(call review.ReviewCall, opts Options) string {
	if call.Type == "individual" {
		// For individual: hash file content + AOI. We use AOI serialization
		// which includes file path, so we don't need to separately hash file content.
		return review.IndividualCacheKey("", call.AOIs[0], opts.Focus)
	}
	return review.GroupedCacheKey(call.AOIs, opts.Focus)
}

func parseDeepReviewResult(call review.ReviewCall, raw string) *state.DeepReviewResult {
	result := &state.DeepReviewResult{
		Type:        call.Type,
		Category:    call.Category,
		Subcategory: call.Subcategory,
		RawOutput:   json.RawMessage(raw),
	}

	// Try to parse as JSON — the LLM should return structured output
	s := strings.TrimSpace(raw)

	// Strip markdown fences
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	// Find JSON start
	jsonStart := strings.IndexAny(s, "{[")
	if jsonStart == -1 {
		log.Printf("Phase 3: no JSON found in response for %s/%s", call.Category, call.Subcategory)
		return result
	}
	s = s[jsonStart:]

	if call.Type == "individual" {
		var parsed struct {
			AOIID              string `json:"aoi_id"`
			Status             string `json:"status"`
			File               string `json:"file"`
			Lines              string `json:"lines"`
			Severity           string `json:"severity"`
			Category           string `json:"category"`
			Subcategory        string `json:"subcategory"`
			Dimension          string `json:"dimension"`
			Title              string `json:"title"`
			Description        string `json:"description"`
			Trigger            string `json:"trigger"`
			Suggestion         string `json:"suggestion"`
			DismissedRationale string `json:"dismissed_rationale"`
		}
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			log.Printf("Phase 3: failed to parse individual response: %v", err)
			return result
		}
		if parsed.Status == "finding" {
			result.Findings = append(result.Findings, state.DeepFinding{
				AOIID:       parsed.AOIID,
				File:        parsed.File,
				Lines:       parsed.Lines,
				Severity:    parsed.Severity,
				Category:    parsed.Category,
				Subcategory: parsed.Subcategory,
				Dimension:   parsed.Dimension,
				Title:       parsed.Title,
				Description: parsed.Description,
				Trigger:     parsed.Trigger,
				Suggestion:  parsed.Suggestion,
			})
		} else {
			result.Dismissals = append(result.Dismissals, state.DeepDismissal{
				AOIID:     parsed.AOIID,
				Rationale: parsed.DismissedRationale,
			})
		}
	} else {
		// Grouped response
		var parsed struct {
			Subcategory  string `json:"subcategory"`
			CrossCutting string `json:"cross_cutting"`
			Results      []struct {
				AOIID              string `json:"aoi_id"`
				Status             string `json:"status"`
				File               string `json:"file"`
				Lines              string `json:"lines"`
				Severity           string `json:"severity"`
				Category           string `json:"category"`
				Subcategory        string `json:"subcategory"`
				Dimension          string `json:"dimension"`
				Title              string `json:"title"`
				Description        string `json:"description"`
				Trigger            string `json:"trigger"`
				Suggestion         string `json:"suggestion"`
				DismissedRationale string `json:"dismissed_rationale"`
			} `json:"results"`
		}
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			log.Printf("Phase 3: failed to parse grouped response: %v", err)
			return result
		}
		result.CrossCutting = parsed.CrossCutting
		for _, r := range parsed.Results {
			if r.Status == "finding" {
				result.Findings = append(result.Findings, state.DeepFinding{
					AOIID:       r.AOIID,
					File:        r.File,
					Lines:       r.Lines,
					Severity:    r.Severity,
					Category:    r.Category,
					Subcategory: r.Subcategory,
					Dimension:   r.Dimension,
					Title:       r.Title,
					Description: r.Description,
					Trigger:     r.Trigger,
					Suggestion:  r.Suggestion,
				})
			} else {
				result.Dismissals = append(result.Dismissals, state.DeepDismissal{
					AOIID:     r.AOIID,
					Rationale: r.DismissedRationale,
				})
			}
		}
	}

	return result
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}

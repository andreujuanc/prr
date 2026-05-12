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

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/classify"
	"github.com/andreujuanc/prr/internal/dbg"
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

	// Debug enables verbose output of all LLM prompts and responses.
	Debug bool

	// DebugFile restricts the audit to a single file (path relative to repo root).
	DebugFile string

	// LargeFileThreshold is the file count above which a warning is
	// displayed. Defaults to 200 when zero.
	LargeFileThreshold int

	// Concurrency tunes per-phase concurrency caps. Each zero field falls
	// back to the package default (currently 5).
	Concurrency ConcurrencyConfig
}

// ConcurrencyConfig holds per-phase concurrency caps. Each field is the
// maximum number of in-flight LLM calls for that phase. Zero = use the
// package default.
type ConcurrencyConfig struct {
	// Classify caps Phase 1b classification calls.
	Classify int
	// AOIScan caps Phase 2 AOI batch calls.
	AOIScan int
	// DeepReview caps Phase 3 review calls.
	DeepReview int
	// Recheck caps Phase 3b recheck batch calls.
	Recheck int
	// HierarchicalSynth caps Phase 4 per-category synthesis calls.
	HierarchicalSynth int
}

// concurrencyOr returns n when positive, otherwise defaultVal.
func concurrencyOr(n, defaultVal int) int {
	if n <= 0 {
		return defaultVal
	}
	return n
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

	// FailedReviews is the count of Phase 3 review calls that errored and
	// produced no findings. The user should know about these — silently
	// dropping them means the audit's recall is lower than it appears.
	FailedReviews int

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

	// ProjectContext is the Phase 0 project briefing. Captured here so the
	// caller can plumb it into Phase 4 synthesis (otherwise the work done in
	// Phase 0 would not reach the executive summary).
	ProjectContext string

	// TokenUsage tracks actual token consumption per phase.
	Usage PhaseUsage
}

// PhaseUsage holds per-phase token usage for cost reporting.
type PhaseUsage struct {
	AOI     ai.TokenUsage // Phase 2: AOI pre-scan
	Review  ai.TokenUsage // Phase 3: deep review
	Recheck ai.TokenUsage // Phase 3b: recheck/dedup
	Synth   ai.TokenUsage // Phase 4: synthesis
}

// Total returns aggregate token usage across all phases.
func (u PhaseUsage) Total() ai.TokenUsage {
	return ai.TokenUsage{
		InputTokens:  u.AOI.InputTokens + u.Review.InputTokens + u.Recheck.InputTokens + u.Synth.InputTokens,
		OutputTokens: u.AOI.OutputTokens + u.Review.OutputTokens + u.Recheck.OutputTokens + u.Synth.OutputTokens,
		CacheHits:    u.AOI.CacheHits + u.Review.CacheHits + u.Recheck.CacheHits + u.Synth.CacheHits,
	}
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

	dbgw := dbg.New(opts.Debug)

	// Load or create audit state
	auditState, err := state.Load("audit")
	if err != nil {
		return nil, fmt.Errorf("loading audit state: %w", err)
	}

	if opts.NoCache {
		auditState.ClearAllCaches()
	}

	// Apply configured per-phase concurrency. Each setter accepts <=0 as
	// "reset to default", so leaving fields zero preserves baseline behavior.
	classify.SetMaxConcurrency(opts.Concurrency.Classify)
	security.SetAOIConcurrency(opts.Concurrency.AOIScan)
	SetHierarchicalSynthConcurrency(opts.Concurrency.HierarchicalSynth)

	// ── Phase 0 + Phase 1 in parallel ───────────────────────────────────
	// Phase 0 (project discovery) is one LLM call; Phase 1 (file collection)
	// is `git ls-files` plus filters. They have no dependency on each other,
	// and Phase 0 only feeds Phase 3 prompts and Phase 4 synthesis.
	dbgw.Phase("PHASE 0+1: Project Discovery + File Collection (parallel)")

	type p0Result struct {
		ctx string
		err error
	}
	type p1Result struct {
		paths []string
		err   error
	}

	p0Ch := make(chan p0Result, 1)
	p1Ch := make(chan p1Result, 1)

	onProgress("phase0", "Discovering project context...")
	go func() {
		ctxOut, err := review.DiscoverProjectContext(ctx, aoiClient, opts.RepoRoot, auditState, func(status string) {
			onProgress("phase0", status)
		})
		p0Ch <- p0Result{ctx: ctxOut, err: err}
	}()

	onProgress("phase1", "Collecting files...")
	go func() {
		paths, err := CollectFiles(opts.RepoRoot, opts.ExcludePatterns, opts.IncludePatterns)
		p1Ch <- p1Result{paths: paths, err: err}
	}()

	p0 := <-p0Ch
	p1 := <-p1Ch

	projectContext := p0.ctx
	if p0.err != nil {
		log.Printf("Phase 0 (project context) failed: %v", p0.err)
		// Non-fatal — continue without project context.
	}
	dbgw.Section("Project Context Result")
	if projectContext != "" {
		dbgw.Text("%s", projectContext)
	} else {
		dbgw.Text("(no project context discovered)")
	}

	if p1.err != nil {
		return nil, fmt.Errorf("phase 1 file collection: %w", p1.err)
	}
	filePaths := p1.paths

	// Warn about unexpectedly large file sets
	threshold := opts.LargeFileThreshold
	if threshold <= 0 {
		threshold = 200
	}
	if len(filePaths) > threshold {
		onProgress("warning", fmt.Sprintf("⚠ %d files collected (threshold %d) — this may include untracked files that should be in .gitignore. Press Ctrl+C to abort.", len(filePaths), threshold))
	}

	// Apply --file filter
	if opts.DebugFile != "" {
		var filtered []string
		for _, f := range filePaths {
			if f == opts.DebugFile {
				filtered = append(filtered, f)
				break
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("--file %q not found in collected files (%d files scanned)", opts.DebugFile, len(filePaths))
		}
		dbgw.Text("Filtered to single file: %s", opts.DebugFile)
		filePaths = filtered
	}

	// Load file contents
	files := make([]classify.File, 0, len(filePaths))
	for _, fp := range filePaths {
		absPath := filepath.Join(opts.RepoRoot, fp)
		content, readErr := os.ReadFile(absPath)
		if readErr != nil {
			log.Printf("Warning: skipping %s: %v", fp, readErr)
			continue
		}
		files = append(files, classify.File{Path: fp, Content: string(content)})
	}

	onProgress("phase1", fmt.Sprintf("Phase 1 complete: %d files to audit", len(files)))
	for _, f := range files {
		dbgw.Text("  %s (%d bytes)", f.Path, len(f.Content))
	}

	if len(files) == 0 {
		return &Result{}, nil
	}

	// ── Phase 1b: File Classification ───────────────────────────────────

	dbgw.Phase("PHASE 1b: File Classification")
	onProgress("phase1b", fmt.Sprintf("Classifying %d file(s)...", len(files)))

	classifications, err := runPhase1b(ctx, aoiClient, opts, auditState, files, onProgress)
	if err != nil {
		log.Printf("Phase 1b (classification) failed: %v — all files will use all dimensions", err)
		classifications = make(map[string]classify.FileType, len(files))
		for _, f := range files {
			classifications[f.Path] = classify.FileTypeUnknown
		}
	}

	if dbgw.Enabled() {
		dbgw.Section("File Classifications")
		typeCounts := make(map[classify.FileType]int)
		for _, ft := range classifications {
			typeCounts[ft]++
		}
		for ft, count := range typeCounts {
			dbgw.Text("  %s: %d file(s)", ft, count)
		}
	}

	// Save state after Phase 1b
	if err := state.Save(auditState); err != nil {
		log.Printf("Warning: failed to save audit state after Phase 1b: %v", err)
	}

	// ── Phase 2: AOI Generation ─────────────────────────────────────────

	dbgw.Phase("PHASE 2: AOI Pre-scan")
	onProgress("phase2", fmt.Sprintf("Scanning %d files for areas of interest...", len(files)))

	// Build AOI debug hook
	var aoiDebugHook security.AOIDebugHook
	if dbgw.Enabled() {
		aoiDebugHook = func(batchFiles []string, systemPrompt string, userMessage string, response string) {
			dbgw.Section(fmt.Sprintf("AOI Scan: %v", batchFiles))
			dbgw.Prompt(systemPrompt, userMessage)
			dbgw.Response(response)
			dbgw.Separator()
		}
	}

	aoiResults, err := runPhase2(ctx, aoiClient, opts, auditState, files, classifications, onProgress, aoiDebugHook)
	if err != nil {
		return nil, fmt.Errorf("phase 2 AOI generation: %w", err)
	}

	// Debug: show parsed AOIs
	if dbgw.Enabled() {
		dbgw.Section("Parsed AOIs")
		totalAOIs := 0
		for _, r := range aoiResults {
			for _, aoi := range r.AreasOfInterest {
				totalAOIs++
				dbgw.Text("  %s:%d [%s] %s/%s — %s",
					aoi.File, aoi.Line, aoi.Urgency, aoi.Category, aoi.Subcategory, aoi.Concern)
			}
		}
		dbgw.Text("\n  Total: %d AOIs", totalAOIs)
	}

	// Save state after Phase 2
	if err := state.Save(auditState); err != nil {
		log.Printf("Warning: failed to save audit state after Phase 2: %v", err)
	}
	aoiUsage := ai.SnapshotUsage(aoiClient)

	// ── Phase 3: Deep Review (routing + execution) ──────────────────────

	dbgw.Phase("PHASE 3: Deep Review")
	routing := review.RouteAOIs(aoiResults, opts.Focus, 10)
	onProgress("phase3", routing.FormatSummary())
	dbgw.Text("Routing: %s", routing.FormatSummary())

	result := &Result{
		FilesScanned:         len(files),
		AOIsGenerated:        routing.TotalAOIs,
		Routing:              routing,
		ProjectContext:       projectContext,
		SkippedSubcategories: routing.SkippedSubcategories(opts.MaxReviews),
	}

	if routing.TotalAOIs == 0 {
		onProgress("phase3", "No areas of interest found — audit complete.")
		return result, nil
	}

	calls := routing.PrioritizedCalls(opts.MaxReviews)
	onProgress("phase3", fmt.Sprintf("Executing %d review calls...", len(calls)))

	// Build Phase 3 debug hook
	var phase3DebugHook func(index int, call review.ReviewCall, systemPrompt string, userMsg string, response string)
	var phase3ToolHook func(callIndex int, toolName string, args string, status string, duration string)
	if dbgw.Enabled() {
		phase3DebugHook = func(index int, call review.ReviewCall, systemPrompt string, userMsg string, response string) {
			label := call.Category
			if call.Subcategory != "" {
				label += "/" + call.Subcategory
			}
			dbgw.Section(fmt.Sprintf("Review Call %d [%s]: %s (%v)", index+1, call.Type, label, call.Files))
			dbgw.Prompt(systemPrompt, userMsg)
			dbgw.Response(response)
			dbgw.Separator()
		}
		phase3ToolHook = func(callIndex int, toolName string, args string, status string, duration string) {
			if status == "start" {
				dbgw.Text("  [call %d] tool: %s(%s)", callIndex+1, toolName, args)
			} else {
				dbgw.Text("  [call %d] tool done: %s → %s (%s)", callIndex+1, toolName, status, duration)
			}
		}
	}

	if mi, ok := reviewClient.(ai.ModelInfo); ok {
		log.Printf("Phase 3: using review model %s/%s", mi.ProviderName(), mi.ModelName())
	}
	findings, dismissals, crossCutting, failed, err := runPhase3(
		ctx, reviewClient, opts, auditState, projectContext, calls, onProgress, phase3DebugHook, phase3ToolHook,
	)
	if err != nil {
		return nil, fmt.Errorf("phase 3 deep review: %w", err)
	}

	// Save state after Phase 3
	if err := state.Save(auditState); err != nil {
		log.Printf("Warning: failed to save audit state after Phase 3: %v", err)
	}
	reviewUsage := ai.SnapshotUsage(reviewClient)

	// ── Phase 3b: Recheck — deduplicate and filter findings ─────
	dbgw.Phase("PHASE 3b: Recheck")
	if len(findings) > 0 {
		recheckKey := computeRecheckCacheKey(findings, projectContext, "audit")
		if !opts.NoCache {
			if raw := auditState.GetRecheckCache(recheckKey); raw != nil {
				var cached []state.DeepFinding
				if err := json.Unmarshal(raw, &cached); err == nil {
					findings = cached
					onProgress("recheck", "Using cached recheck result")
					goto afterRecheck
				}
				log.Printf("Recheck cache: ignoring corrupted entry: %v", err)
			}
		}

		// Build recheck debug hook
		var recheckDebugHook func(systemPrompt string, userMsg string, response string)
		if dbgw.Enabled() {
			recheckDebugHook = func(systemPrompt string, userMsg string, response string) {
				dbgw.Section("Recheck LLM Call")
				dbgw.Prompt(systemPrompt, userMsg)
				dbgw.Response(response)
				dbgw.Separator()
			}
		}

		var changed bool
		findings, changed = review.RunRecheck(ctx, reviewClient, findings, review.ModeAudit, projectContext,
			func(status string) { onProgress("recheck", status) }, recheckDebugHook,
			review.RecheckSettings{MaxConcurrency: opts.Concurrency.Recheck})

		// Persist on success.
		if changed {
			if raw, marshalErr := json.Marshal(findings); marshalErr == nil {
				auditState.SetRecheckCache(recheckKey, raw)
			}
		}
	}
afterRecheck:
	recheckUsage := ai.SnapshotUsage(reviewClient)

	result.Findings = findings
	result.Dismissals = dismissals
	result.CrossCuttingObservations = crossCutting
	result.ReviewCalls = len(calls)
	result.IndividualReviews = len(routing.Individual)
	result.GroupedReviews = len(routing.Grouped)
	result.FailedReviews = failed
	result.Usage = PhaseUsage{
		AOI:     aoiUsage,
		Review:  reviewUsage,
		Recheck: recheckUsage,
	}

	return result, nil
}

// ── Phase 1b: File Classification ────────────────────────────────────────

func runPhase1b(
	ctx context.Context,
	client ai.Client,
	opts Options,
	auditState *state.State,
	files []classify.File,
	onProgress OnProgress,
) (map[string]classify.FileType, error) {
	// Build cached types from state
	cachedTypes := make(map[string]classify.FileType)
	if !opts.NoCache {
		for _, f := range files {
			contentHash := hashContent(f.Content)
			if fs, ok := auditState.Files[f.Path]; ok && fs.DiffHash == contentHash && fs.FileType != "" {
				cachedTypes[f.Path] = classify.FileType(fs.FileType)
			}
		}
	}

	result, err := classify.Classify(ctx, client, files, cachedTypes, func(status string) {
		onProgress("phase1b", status)
	})
	if err != nil {
		// Partial results: surface the failure as a warning but proceed —
		// files from failed batches are already filled in as unknown, which
		// triggers the conservative full-dimension AOI scan downstream.
		onProgress("warning", fmt.Sprintf("classification partial: %v", err))
	}

	// Cache results
	for path, ft := range result {
		auditState.SetFileType(path, string(ft))
	}

	return result, nil
}

// ── Phase 2: AOI Generation ─────────────────────────────────────────────

func runPhase2(
	ctx context.Context,
	aoiClient ai.Client,
	opts Options,
	auditState *state.State,
	files []classify.File,
	classifications map[string]classify.FileType,
	onProgress OnProgress,
	debugHook security.AOIDebugHook,
) ([]security.AOIScanResult, error) {
	// Read file contents and build "diffs" (for audit mode, the full file content)
	fileContents := make(map[string]string)
	cachedResults := make(map[string]*security.AOIScanResult)

	for _, f := range files {
		filePath := f.Path
		contentStr := f.Content

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

	// Build per-file dimension map from classifications
	fileDimensions := make(map[string][]string, len(fileContents))
	for path := range fileContents {
		ft := classifications[path]
		fileDimensions[path] = classify.DimensionsForType(ft)
	}

	// Run AOI scan on uncached files
	report, err := security.ScanAreasOfInterestClassified(ctx, aoiClient, fileContents, cachedResults, fileDimensions, func(status string) {
		onProgress("phase2", status)
	}, debugHook, true)
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

// phase3MaxConcurrency caps parallel deep review calls.
// Benchmarked at 5 concurrent Opus 4.6 calls via Copilot: 100% recall, 0 FP, ~48s.
// Higher values (7+) occasionally trigger malformed responses from the API.
const phase3MaxConcurrency = 5

func runPhase3(
	ctx context.Context,
	reviewClient ai.Client,
	opts Options,
	auditState *state.State,
	projectContext string,
	calls []review.ReviewCall,
	onProgress OnProgress,
	debugHook func(index int, call review.ReviewCall, systemPrompt string, userMsg string, response string),
	toolHook func(callIndex int, toolName string, args string, status string, duration string),
) (findings []state.DeepFinding, dismissals int, crossCutting []string, failed int, err error) {
	execOpts := review.ExecuteOptions{
		Mode:            review.ModeAudit,
		ProjectContext:  projectContext,
		FocusDimensions: opts.Focus,
		MaxConcurrency:  concurrencyOr(opts.Concurrency.DeepReview, phase3MaxConcurrency),
		NoCache:         opts.NoCache,
		OnLLMCall:       debugHook,
		OnToolCall:      toolHook,
		OnProgress: func(completed, total int, cached bool, callErr error) {
			if callErr != nil {
				onProgress("phase3", fmt.Sprintf("Review %d/%d failed: %v", completed, total, callErr))
				return
			}
			cacheTag := ""
			if cached {
				cacheTag = " (cached)"
			}
			onProgress("phase3", fmt.Sprintf("Review %d/%d complete%s", completed, total, cacheTag))
		},
	}

	// Wire up caching to audit state
	if auditState != nil {
		execOpts.CacheGet = func(key string) *state.DeepReviewResult {
			return auditState.GetDeepReview(key)
		}
		execOpts.CacheSet = func(key string, result *state.DeepReviewResult) {
			auditState.SetDeepReview(key, result)
		}
	}

	execResult, execErr := review.RunReviewCalls(ctx, reviewClient, calls, execOpts)
	if execErr != nil {
		return nil, 0, nil, 0, execErr
	}

	return execResult.Findings, execResult.Dismissals, execResult.CrossCutting, execResult.Failed, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────

func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])[:32]
}

// computeRecheckCacheKey hashes the recheck inputs into a stable key.
// Findings are sorted by FindingID before hashing so order doesn't matter.
func computeRecheckCacheKey(findings []state.DeepFinding, projectContext, mode string) string {
	sorted := make([]state.DeepFinding, len(findings))
	copy(sorted, findings)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].FindingID < sorted[j].FindingID })

	h := sha256.New()
	h.Write([]byte(mode))
	h.Write([]byte{0})
	h.Write([]byte(projectContext))
	h.Write([]byte{0})
	if data, err := json.Marshal(sorted); err == nil {
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// computeSynthesisCacheKey hashes the synthesis inputs into a stable key.
func computeSynthesisCacheKey(findings []state.DeepFinding, crossCutting []string, projectContext string) string {
	sortedFindings := make([]state.DeepFinding, len(findings))
	copy(sortedFindings, findings)
	sort.Slice(sortedFindings, func(i, j int) bool { return sortedFindings[i].FindingID < sortedFindings[j].FindingID })

	sortedCC := make([]string, len(crossCutting))
	copy(sortedCC, crossCutting)
	sort.Strings(sortedCC)

	h := sha256.New()
	h.Write([]byte(projectContext))
	h.Write([]byte{0})
	if data, err := json.Marshal(sortedFindings); err == nil {
		h.Write(data)
	}
	h.Write([]byte{0})
	if data, err := json.Marshal(sortedCC); err == nil {
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func ensureFileState(s *state.State, path, contentHash string) {
	s.SetBatchFindings(path, "", "") // ensure FileState exists
	if fs, ok := s.Files[path]; ok {
		fs.DiffHash = contentHash
	}
}

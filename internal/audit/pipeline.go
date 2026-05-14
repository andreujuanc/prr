package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"

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

	// MaxFileBytes caps per-file size during Phase 1 ingestion. Files
	// exceeding the cap are skipped with a warning (not loaded into
	// prompts). Zero falls back to MaxAuditFileBytes.
	MaxFileBytes int

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

	// FailedAOIIDs is the list of AOI IDs whose Phase 3 review call
	// failed (and therefore produced no finding or dismissal). Carries
	// the per-call FailedAOIIDs from review.ExecuteResult so synthesis
	// can mention recall gaps in the executive summary and the reporter
	// can name exactly which areas of the codebase didn't get reviewed.
	FailedAOIIDs []string

	// Findings is all confirmed findings from Phase 3.
	Findings []state.DeepFinding

	// Dismissals is all dismissed AOIs from Phase 3.
	Dismissals int

	// RecheckDismissals is the Phase 3b dismissal log — every finding
	// the recheck pass removed, with the rationale the model gave.
	// Separate from Dismissals (which counts Phase 3 AOI-level
	// dismissals before any finding was emitted); these are findings
	// that survived Phase 3 and were then dropped by recheck.
	RecheckDismissals []state.DismissedRecord

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
		stats CollectStats
		err   error
	}

	p0Ch := make(chan p0Result, 1)
	p1Ch := make(chan p1Result, 1)

	// project.Discover emits its own "Discovering project context..."
	// inside, so we don't duplicate it here.
	go func() {
		ctxOut, err := review.DiscoverProjectContext(ctx, aoiClient, opts.RepoRoot, auditState, func(status string) {
			onProgress("phase0", status)
		})
		p0Ch <- p0Result{ctx: ctxOut, err: err}
	}()

	onProgress("phase1", "Collecting files...")
	go func() {
		paths, stats, err := CollectFiles(opts.RepoRoot, opts.ExcludePatterns, opts.IncludePatterns)
		p1Ch <- p1Result{paths: paths, stats: stats, err: err}
	}()

	p0 := <-p0Ch
	p1 := <-p1Ch

	projectContext := p0.ctx
	if p0.err != nil {
		// Phase 0 is load-bearing: every downstream prompt embeds the
		// project context. A failure here usually means the configured
		// fast model is unreachable (invalid model name, expired key,
		// network). Continuing with empty context would produce
		// findings that ignore project conventions; previously we
		// silently degraded to a 600-line raw-doc dump which made
		// every later prompt enormous. Fail fast with the original
		// error so the user can fix their model config.
		return nil, fmt.Errorf("phase 0 (project context): %w", p0.err)
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
	collectStats := p1.stats

	if dbgw.Enabled() {
		dbgw.Section("File Collection Stats")
		dbgw.Text("  total listed:    %d", collectStats.TotalListed)
		dbgw.Text("  included:        %d (%d tracked, %d untracked)",
			collectStats.Included, collectStats.Tracked, collectStats.Untracked)
		dbgw.Text("  excluded review: %d (locks, vendor, generated, etc.)", collectStats.ExcludedReview)
		dbgw.Text("  excluded audit:  %d (docs, build artifacts, IDE, etc.)", collectStats.ExcludedAudit)
		dbgw.Text("  excluded custom: %d (user --exclude patterns)", collectStats.ExcludedCustom)
		if collectStats.ForceIncluded > 0 {
			dbgw.Text("  force-included:  %d (user --include patterns)", collectStats.ForceIncluded)
		}
	}

	// Untracked files matching transient patterns (logs, debug dumps,
	// state snapshots) are almost always a gitignore miss. Surface
	// them once so the user can decide; we don't auto-exclude because
	// a legitimately-named file could match.
	if len(collectStats.UntrackedTransients) > 0 {
		preview := collectStats.UntrackedTransients
		suffix := ""
		if len(preview) > 5 {
			preview = preview[:5]
			suffix = ", ..."
		}
		onProgress("warning", fmt.Sprintf("⚠ including %d untracked file(s) that look like local tooling output (%s%s) — consider .gitignore",
			len(collectStats.UntrackedTransients),
			strings.Join(preview, ", "),
			suffix))
	}

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

	// Load file contents with per-file guards (symlink, size cap,
	// binary detection, empty check) and aggregate-fail on read errors.
	maxBytes := int64(opts.MaxFileBytes)
	if maxBytes <= 0 {
		maxBytes = int64(MaxAuditFileBytes)
	}

	files := make([]classify.File, 0, len(filePaths))
	var skipCounts struct {
		symlink, binary, empty, large, notFound, errored int
	}
	for _, fp := range filePaths {
		absPath := filepath.Join(opts.RepoRoot, fp)
		res := loadAuditFile(absPath, fp, maxBytes)
		switch res.Outcome {
		case loadedOK:
			files = append(files, res.File)
		case skippedSymlink:
			skipCounts.symlink++
			dbgw.Text("  skipped %s (symlink/non-regular)", fp)
		case skippedTooLarge:
			skipCounts.large++
			onProgress("warning", fmt.Sprintf("⚠ skipped %s (%d KB exceeds %d KB cap)",
				fp, res.Size/1024, maxBytes/1024))
			dbgw.Text("  skipped %s (%d bytes > %d cap)", fp, res.Size, maxBytes)
		case skippedBinary:
			skipCounts.binary++
			dbgw.Text("  skipped %s (binary content)", fp)
		case skippedEmpty:
			skipCounts.empty++
			dbgw.Text("  skipped %s (empty)", fp)
		case skippedNotFound:
			skipCounts.notFound++
			log.Printf("audit: %s vanished between ls-files and read (likely git rm race); skipping", fp)
		case loadErrored:
			skipCounts.errored++
			log.Printf("audit: read error on %s: %v", fp, res.Err)
		}
	}

	// Aggregate-fail: a handful of transient read errors is acceptable,
	// but if >20% of reads fail something structural is wrong and we
	// should abort rather than ship a degraded audit.
	attempted := len(filePaths)
	if shouldAggregateFail(skipCounts.errored, attempted) {
		return nil, fmt.Errorf("phase 1: %d/%d files failed to read (>%.0f%% threshold) — aborting; check working directory and permissions",
			skipCounts.errored, attempted, aggregateFailRatio*100)
	}

	totalSkipped := skipCounts.symlink + skipCounts.binary + skipCounts.empty +
		skipCounts.large + skipCounts.notFound + skipCounts.errored

	if totalSkipped > 0 {
		onProgress("phase1", fmt.Sprintf(
			"Phase 1 skip breakdown: %d binary, %d large, %d empty, %d symlink, %d missing, %d errored",
			skipCounts.binary, skipCounts.large, skipCounts.empty,
			skipCounts.symlink, skipCounts.notFound, skipCounts.errored))
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

	// Emit the terminal phase-2 summary the TUI parser needs to populate
	// aoi_count. Without this, the summary row renders "0 AOIs" forever
	// even when the scan found dozens — the review pipeline emits the
	// same message at the equivalent point; keep them symmetrical.
	{
		total := 0
		for _, r := range aoiResults {
			total += len(r.AreasOfInterest)
		}
		onProgress("phase2", fmt.Sprintf("found %d areas of interest", total))
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
	findings, dismissals, crossCutting, failed, failedAOIIDs, err := runPhase3(
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
					// Restore the dismissal trail from state — the
					// cache stores findings only, but state holds
					// the per-finding rationales from the original
					// recheck run. Without this, the report on a
					// cache hit shows the deduped finding list but
					// no audit trail of what got dropped or why.
					result.RecheckDismissals = auditState.GetRecheckDismissals()
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
		var dismissals []state.DismissedRecord
		findings, dismissals, changed = review.RunRecheck(ctx, reviewClient, findings, review.ModeAudit, projectContext,
			func(status string) { onProgress("recheck", status) }, recheckDebugHook,
			review.RecheckSettings{MaxConcurrency: opts.Concurrency.Recheck})

		// Persist on success — both the deduped finding set (used for
		// cache hits next run) AND the dismissal rationale log (used
		// by the audit report).
		if changed {
			if raw, marshalErr := json.Marshal(findings); marshalErr == nil {
				auditState.SetRecheckCache(recheckKey, raw)
			}
			auditState.SetRecheckDismissals(dismissals)
		}
		result.RecheckDismissals = dismissals
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
	result.FailedAOIIDs = failedAOIIDs
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

	// Surface the partial-cache hit count so the TUI's AOI summary
	// row can render "K cached". The scanner only emits this when it
	// sees cached entries in rawDiffs — and we filtered them out
	// upstream, so the scanner counts 0 cached. Emit it here from
	// the count we already have.
	if len(cachedResults) > 0 {
		onProgress("phase2", fmt.Sprintf("using cached AOI results for %d file(s)", len(cachedResults)))
	}

	// Run AOI scan on uncached files
	report, err := security.ScanAreasOfInterestClassified(ctx, aoiClient, fileContents, cachedResults, fileDimensions, func(status string) {
		onProgress("phase2", status)
	}, debugHook, true)
	if err != nil {
		return nil, err
	}

	// Cache results per-file (fresh results only — cached entries are
	// already in state).
	for _, fileResult := range report.Files {
		data, err := json.Marshal(fileResult)
		if err != nil {
			continue
		}
		auditState.SetAOIResults(fileResult.File, data, opts.AOIContextLines)
	}

	// Merge cached results into the return value. The scanner's internal
	// merge loop iterates rawDiffs (the uncached-only map we passed in)
	// looking up each path in cachedResults — none of those paths are
	// in the cache, so its cachedAOIs slice stays empty and report.Files
	// contains only fresh scans. Without this merge, Phase 3 would route
	// only the freshly-scanned AOIs and silently lose all cache-hit ones.
	if len(cachedResults) > 0 {
		combined := make([]security.AOIScanResult, 0, len(report.Files)+len(cachedResults))
		combined = append(combined, report.Files...)
		for _, r := range cachedResults {
			combined = append(combined, *r)
		}
		return combined, nil
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
) (findings []state.DeepFinding, dismissals int, crossCutting []string, failed int, failedAOIIDs []string, err error) {
	execOpts := review.ExecuteOptions{
		Mode:            review.ModeAudit,
		ProjectContext:  projectContext,
		FocusDimensions: opts.Focus,
		MaxConcurrency:  concurrencyOr(opts.Concurrency.DeepReview, phase3MaxConcurrency),
		NoCache:         opts.NoCache,
		RepoRoot:        opts.RepoRoot,
		OnLLMCall:       debugHook,
		OnToolCall:      toolHook,
		OnProgress: func(completed, total int, cached bool, callErr error) {
			// Two emits per call: counter first, then status. The TUI
			// shows the latest message as the detail line, so emitting
			// the counter first and the status second yields:
			//   <phase label>  X/Y  <status>
			// instead of the previously-redundant:
			//   <phase label>  X/Y  Review X/Y complete
			// where the counter was duplicated in the detail.
			onProgress("phase3", fmt.Sprintf("Review %d/%d", completed, total))
			if callErr != nil {
				onProgress("phase3", fmt.Sprintf("failed: %v", callErr))
				return
			}
			if cached {
				onProgress("phase3", "complete (cached)")
			} else {
				onProgress("phase3", "complete")
			}
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
		// Even on error, RunReviewCalls may return a non-nil ExecuteResult
		// with partial findings + the FailedAOIIDs list. Surface those to
		// callers so they can see what was lost; the error still bubbles
		// up to abort the run.
		if execResult != nil {
			return execResult.Findings, execResult.Dismissals, execResult.CrossCutting,
				execResult.Failed, execResult.FailedAOIIDs, execErr
		}
		return nil, 0, nil, 0, nil, execErr
	}

	return execResult.Findings, execResult.Dismissals, execResult.CrossCutting,
		execResult.Failed, execResult.FailedAOIIDs, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────

func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])[:32]
}

// computeRecheckCacheKey hashes the recheck inputs into a stable key.
// Findings are sorted by FindingID before hashing so order doesn't matter.
//
// Both recheck prompts are part of the key so tuning either one
// invalidates stale cache entries automatically. Without this, a
// prompt change would silently serve recheck results produced by the
// previous prompt for the duration of the cached entry.
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
	h.Write([]byte{0})
	consolHash := sha256.Sum256([]byte(ai.RecheckConsolidatePrompt))
	h.Write(consolHash[:])
	dismissHash := sha256.Sum256([]byte(ai.RecheckDismissPrompt))
	h.Write(dismissHash[:])
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// computeSynthesisCacheKey hashes the synthesis inputs into a stable key.
//
// failedAOICount is part of the key so a re-run that resolves the
// transient errors (count drops to zero) regenerates synthesis
// instead of returning the stale "recall degraded" summary from
// the prior run.
func computeSynthesisCacheKey(findings []state.DeepFinding, crossCutting []string, projectContext string, failedAOICount int) string {
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
	h.Write([]byte{0})
	fmt.Fprintf(h, "failedAOIs=%d", failedAOICount)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func ensureFileState(s *state.State, path, contentHash string) {
	s.SetBatchFindings(path, "", "") // ensure FileState exists
	if fs, ok := s.Files[path]; ok {
		fs.DiffHash = contentHash
	}
}

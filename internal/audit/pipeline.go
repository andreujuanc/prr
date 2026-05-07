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

	// Debug enables verbose output of all LLM prompts and responses.
	Debug bool

	// DebugFile restricts the audit to a single file (path relative to repo root).
	DebugFile string
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

	// TokenUsage tracks actual token consumption per phase.
	Usage PhaseUsage
}

// PhaseUsage holds per-phase token usage for cost reporting.
type PhaseUsage struct {
	AOI      ai.TokenUsage // Phase 2: AOI pre-scan
	Review   ai.TokenUsage // Phase 3: deep review
	Recheck  ai.TokenUsage // Phase 3b: recheck/dedup
	Synth    ai.TokenUsage // Phase 4: synthesis
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

// AuditFile holds a file path and its content for auditing.
type AuditFile struct {
	Path    string
	Content string
}

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

	dbg := NewDebugWriter(opts.Debug)

	// Usage tracking: snapshot and reset between phases
	snapshotUsage := func(client ai.Client) ai.TokenUsage {
		if ur, ok := client.(ai.UsageReporter); ok {
			u := ur.Usage()
			ur.ResetUsage()
			return u
		}
		return ai.TokenUsage{}
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

	dbg.Phase("PHASE 0: Project Context Discovery")
	onProgress("phase0", "Discovering project context...")
	projectContext, err := runPhase0(ctx, aoiClient, opts.RepoRoot, auditState, onProgress)
	if err != nil {
		log.Printf("Phase 0 (project context) failed: %v", err)
		// Non-fatal — continue without project context
	}
	dbg.Section("Project Context Result")
	if projectContext != "" {
		dbg.Text("%s", projectContext)
	} else {
		dbg.Text("(no project context discovered)")
	}

	// ── Phase 1: File Collection ────────────────────────────────────────

	dbg.Phase("PHASE 1: File Collection")
	onProgress("phase1", "Collecting files...")
	filePaths, err := CollectFiles(opts.RepoRoot, opts.ExcludePatterns, opts.IncludePatterns)
	if err != nil {
		return nil, fmt.Errorf("phase 1 file collection: %w", err)
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
		dbg.Text("Filtered to single file: %s", opts.DebugFile)
		filePaths = filtered
	}

	// Load file contents
	files := make([]AuditFile, 0, len(filePaths))
	for _, fp := range filePaths {
		absPath := filepath.Join(opts.RepoRoot, fp)
		content, readErr := os.ReadFile(absPath)
		if readErr != nil {
			log.Printf("Warning: skipping %s: %v", fp, readErr)
			continue
		}
		files = append(files, AuditFile{Path: fp, Content: string(content)})
	}

	onProgress("phase1", fmt.Sprintf("Phase 1 complete: %d files to audit", len(files)))
	for _, f := range files {
		dbg.Text("  %s (%d bytes)", f.Path, len(f.Content))
	}

	if len(files) == 0 {
		return &Result{}, nil
	}

	// ── Phase 2: AOI Generation ─────────────────────────────────────────

	dbg.Phase("PHASE 2: AOI Pre-scan")
	onProgress("phase2", fmt.Sprintf("Scanning %d files for areas of interest...", len(files)))

	// Build AOI debug hook
	var aoiDebugHook security.AOIDebugHook
	if dbg.Enabled() {
		aoiDebugHook = func(batchFiles []string, systemPrompt string, userMessage string, response string) {
			dbg.Section(fmt.Sprintf("AOI Scan: %v", batchFiles))
			dbg.Prompt(systemPrompt, userMessage)
			dbg.Response(response)
			dbg.Separator()
		}
	}

	aoiResults, err := runPhase2(ctx, aoiClient, opts, auditState, files, onProgress, aoiDebugHook)
	if err != nil {
		return nil, fmt.Errorf("phase 2 AOI generation: %w", err)
	}

	// Debug: show parsed AOIs
	if dbg.Enabled() {
		dbg.Section("Parsed AOIs")
		totalAOIs := 0
		for _, r := range aoiResults {
			for _, aoi := range r.AreasOfInterest {
				totalAOIs++
				dbg.Text("  %s:%d [%s] %s/%s — %s",
					aoi.File, aoi.Line, aoi.Urgency, aoi.Category, aoi.Subcategory, aoi.Concern)
			}
		}
		dbg.Text("\n  Total: %d AOIs", totalAOIs)
	}

	// Save state after Phase 2
	if err := state.Save(auditState); err != nil {
		log.Printf("Warning: failed to save audit state after Phase 2: %v", err)
	}
	aoiUsage := snapshotUsage(aoiClient)

	// ── Phase 3: Deep Review (routing + execution) ──────────────────────

	dbg.Phase("PHASE 3: Deep Review")
	routing := review.RouteAOIs(aoiResults, opts.Focus, 10)
	onProgress("phase3", routing.FormatSummary())
	dbg.Text("Routing: %s", routing.FormatSummary())

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

	// Build Phase 3 debug hook
	var phase3DebugHook func(index int, call review.ReviewCall, systemPrompt string, userMsg string, response string)
	var phase3ToolHook func(callIndex int, toolName string, args string, status string, duration string)
	if dbg.Enabled() {
		phase3DebugHook = func(index int, call review.ReviewCall, systemPrompt string, userMsg string, response string) {
			label := call.Category
			if call.Subcategory != "" {
				label += "/" + call.Subcategory
			}
			dbg.Section(fmt.Sprintf("Review Call %d [%s]: %s (%v)", index+1, call.Type, label, call.Files))
			dbg.Prompt(systemPrompt, userMsg)
			dbg.Response(response)
			dbg.Separator()
		}
		phase3ToolHook = func(callIndex int, toolName string, args string, status string, duration string) {
			if status == "start" {
				dbg.Text("  [call %d] tool: %s(%s)", callIndex+1, toolName, args)
			} else {
				dbg.Text("  [call %d] tool done: %s → %s (%s)", callIndex+1, toolName, status, duration)
			}
		}
	}

	findings, dismissals, crossCutting, err := runPhase3(
		ctx, reviewClient, opts, auditState, projectContext, calls, onProgress, phase3DebugHook, phase3ToolHook,
	)
	if err != nil {
		return nil, fmt.Errorf("phase 3 deep review: %w", err)
	}

	// Save state after Phase 3
	if err := state.Save(auditState); err != nil {
		log.Printf("Warning: failed to save audit state after Phase 3: %v", err)
	}
	reviewUsage := snapshotUsage(reviewClient)

	// ── Phase 3b: Recheck — deduplicate and filter findings ─────
	dbg.Phase("PHASE 3b: Recheck")
	if len(findings) > 0 {
		onProgress("recheck", fmt.Sprintf("Rechecking %d findings...", len(findings)))

		// Build recheck debug hook
		var recheckDebugHook func(systemPrompt string, userMsg string, response string)
		if dbg.Enabled() {
			recheckDebugHook = func(systemPrompt string, userMsg string, response string) {
				dbg.Section("Recheck LLM Call")
				dbg.Prompt(systemPrompt, userMsg)
				dbg.Response(response)
				dbg.Separator()
			}
		}

		recheckResult, recheckErr := review.RecheckFindings(ctx, reviewClient, findings, review.RecheckOptions{
			Mode:           review.ModeAudit,
			ProjectContext: projectContext,
			OnLLMCall:      recheckDebugHook,
		})
		if recheckErr != nil {
			log.Printf("Recheck failed (non-fatal): %v — keeping all findings", recheckErr)
			onProgress("recheck", "Recheck failed, keeping all findings")
		} else {
			onProgress("recheck", fmt.Sprintf(
				"Recheck complete: kept %d, dismissed %d, consolidated %d, modified %d",
				len(recheckResult.Findings), recheckResult.DismissedCount,
				recheckResult.ConsolidatedCount, recheckResult.ModifiedCount,
			))
			findings = recheckResult.Findings
		}
	}
	recheckUsage := snapshotUsage(reviewClient)

	result.Findings = findings
	result.Dismissals = dismissals
	result.CrossCuttingObservations = crossCutting
	result.ReviewCalls = len(calls)
	result.IndividualReviews = len(routing.Individual)
	result.GroupedReviews = len(routing.Grouped)
	result.Usage = PhaseUsage{
		AOI:     aoiUsage,
		Review:  reviewUsage,
		Recheck: recheckUsage,
	}

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
	files []AuditFile,
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

	// Run AOI scan on uncached files
	report, err := security.ScanAreasOfInterestDebug(ctx, aoiClient, fileContents, cachedResults, func(status string) {
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

const phase3MaxConcurrency = 10

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
) (findings []state.DeepFinding, dismissals int, crossCutting []string, err error) {
	execOpts := review.ExecuteOptions{
		Mode:               review.ModeAudit,
		ProjectContext:     projectContext,
		FocusDimensions:    opts.Focus,
		MaxConcurrency:     phase3MaxConcurrency,
		NoCache:            opts.NoCache,
		OnLLMCall:          debugHook,
		OnToolCall:         toolHook,
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
		return nil, 0, nil, execErr
	}

	return execResult.Findings, execResult.Dismissals, execResult.CrossCutting, nil
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

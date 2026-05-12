package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/prcontext"
	"github.com/andreujuanc/prr/internal/project"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// PRReviewOptions configures a headless PR review run.
type PRReviewOptions struct {
	// PRNumber is the pull request number (as string).
	PRNumber string

	// RepoRoot is the absolute path to the repository root.
	RepoRoot string

	// NoCache disables incremental caching.
	NoCache bool

	// NoSynthesis skips the synthesis phase.
	NoSynthesis bool

	// ParallelReviews is the max concurrency for batch reviews. 0 = default (5).
	ParallelReviews int

	// AOIContextLines is the number of context lines for AOI diffing. 0 = default (3).
	AOIContextLines int

	// CustomInstructions from user config.
	CustomInstructions string

	// Debug enables verbose output.
	Debug bool

	// WatchdogTap (optional) is called on every pipeline activity event
	// — phase boundaries AND streamed tokens. Headless callers wire
	// this to an ai.IdleWatch so stalls during long synthesis runs are
	// detected even though no phase events are firing.
	WatchdogTap func(string)
}

// PRReviewResult holds the output of a headless PR review.
type PRReviewResult struct {
	// PR is the pull request metadata.
	PR *git.PullRequest

	// FilesReviewed is the number of files reviewed.
	FilesReviewed int

	// Review is the synthesized review output.
	Review *state.AIReview

	// StructuredReview is the parsed structured review (if synthesis succeeded).
	StructuredReview *state.ReviewOutput

	// DeepFindings from AOI-driven review calls.
	DeepFindings []state.DeepFinding

	// FileFindings maps file paths to their batch findings.
	FileFindings map[string]string
}

// RunPRReview executes the full multi-pass PR review pipeline headlessly.
// This is the same pipeline that runs inside the TUI when pressing 'a':
//
//	Phase 0: Project context discovery + AOI pre-scan
//	Phase 1a: AOI-driven review calls
//	Phase 1b: Fallback directory batch reviews
//	Phase 1c: Recheck/dedup findings
//	Phase 2: Synthesis
func RunPRReview(
	ctx context.Context,
	reviewClient ai.Client,
	aoiClient ai.Client,
	opts PRReviewOptions,
	onProgress func(phase string, message string),
) (*PRReviewResult, error) {
	if onProgress == nil {
		onProgress = func(_, _ string) {}
	}

	// ── Fetch PR metadata ────────────────────────────────────────
	onProgress("fetch", "Fetching PR metadata...")
	pr, err := git.FetchPR(opts.PRNumber)
	if err != nil {
		return nil, fmt.Errorf("fetching PR: %w", err)
	}
	onProgress("fetch", fmt.Sprintf("PR #%d: %s (%d files)", pr.Number, pr.Title, len(pr.Files)))

	// ── Fetch git refs ───────────────────────────────────────────
	onProgress("fetch", "Fetching git refs...")
	if err := git.FetchRefs(pr.BaseRefName, pr.HeadRefName, pr.HeadRefOid); err != nil {
		return nil, fmt.Errorf("fetching refs: %w", err)
	}

	// ── Compute diffs and filter files ───────────────────────────
	onProgress("fetch", "Computing diffs...")
	reviewState, err := state.Load(opts.PRNumber)
	if err != nil {
		return nil, fmt.Errorf("loading state: %w", err)
	}
	if opts.NoCache {
		reviewState.ClearAllCaches()
	}

	base := pr.BaseRefName
	head := pr.HeadRefName

	hashes := make(map[string]string, len(pr.Files))
	rawDiffs := make(map[string]string, len(pr.Files))
	prFiles := make(map[string]bool, len(pr.Files))
	for _, f := range pr.Files {
		prFiles[f.Path] = true
		rawDiff, err := git.GetRawDiff(base, head, f.Path)
		if err != nil {
			log.Printf("Warning: failed to get raw diff for %s: %v", f.Path, err)
			continue
		}
		hashes[f.Path] = git.HashDiff(rawDiff)

		if skip, reason := git.ShouldSkipForAI(f.Path, rawDiff); skip {
			log.Printf("Skipping %s from AI review: %s", f.Path, reason)
			continue
		}
		rawDiffs[f.Path] = rawDiff
	}

	reviewState.SyncWithDiffs(hashes, prFiles)
	if err := state.Save(reviewState); err != nil {
		log.Printf("Warning: failed to save state: %v", err)
	}

	if len(rawDiffs) == 0 {
		return nil, fmt.Errorf("no reviewable files in PR (all binary/generated/large)")
	}
	onProgress("fetch", fmt.Sprintf("Collected diffs for %d files", len(rawDiffs)))

	// Build PR metadata string. PR Brief (condensed comments / prior
	// reviews / CI) is built inside RunReviewCore — both this headless
	// path and the TUI path go through there, so building it once
	// covers both.
	var meta strings.Builder
	meta.WriteString(fmt.Sprintf("PR #%d: %s\n", pr.Number, pr.Title))
	if pr.Body != "" {
		meta.WriteString(fmt.Sprintf("Description:\n%s\n", pr.Body))
	}
	meta.WriteString(fmt.Sprintf("Base: %s → Head: %s\n\n", pr.BaseRefName, pr.HeadRefName))
	prMeta := meta.String()

	// Run the shared core pipeline. If a watchdog tap is provided,
	// wrap the reporter so streamed tokens (which progressReporter
	// ignores) still reset the watchdog.
	var rr Reporter = &progressReporter{onProgress: onProgress}
	if opts.WatchdogTap != nil {
		rr = &WatchdogReporter{Inner: rr, Tap: opts.WatchdogTap}
	}
	coreResult, err := RunReviewCore(ctx, reviewClient, aoiClient, CoreOptions{
		PRMeta:             prMeta,
		RawDiffs:           rawDiffs,
		CustomInstructions: opts.CustomInstructions,
		ReviewState:        reviewState,
		ParallelReviews:    opts.ParallelReviews,
		Base:               base,
		Head:               head,
		AOIContextLines:    opts.AOIContextLines,
		RepoRoot:           opts.RepoRoot,
		NoSynthesis:        opts.NoSynthesis,
		PR:                 pr,
	}, rr)
	if err != nil {
		return nil, err
	}

	return &PRReviewResult{
		PR:               pr,
		FilesReviewed:    len(rawDiffs),
		Review:           coreResult.Review,
		StructuredReview: coreResult.StructuredReview,
		DeepFindings:     coreResult.DeepFindings,
		FileFindings:     coreResult.FileFindings,
	}, nil
}

// summarizeCacheState builds a human-readable one-liner listing the
// phases whose results were loaded from the persisted state — emitted
// at the start of RunReviewCore so callers see what's being reused
// instead of re-computed. Returns "" when nothing is cached (fresh run).
//
// All state reads go through locked accessors. Earlier versions of this
// function dipped into s.Files directly between HasFile calls, which
// was racy against concurrent writers (theoretical at startup, real if
// the function ever moves later in the pipeline).
func summarizeCacheState(s *state.State, rawDiffs map[string]string) string {
	if s == nil {
		return ""
	}
	var parts []string
	if ctxSummary, _ := s.GetProjectContext(); ctxSummary != "" {
		parts = append(parts, "project_ctx ✓")
	}
	if brief, _ := s.GetPRBrief(); brief != "" {
		parts = append(parts, "pr_brief ✓")
	}
	if total := len(rawDiffs); total > 0 {
		paths := make([]string, 0, total)
		for p := range rawDiffs {
			paths = append(paths, p)
		}
		if cached := s.CountCachedBatchFindings(paths); cached > 0 {
			parts = append(parts, fmt.Sprintf("batches %d/%d ✓", cached, total))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " | ")
}

// progressReporter adapts the simple onProgress callback to the Reporter interface.
type progressReporter struct {
	onProgress func(phase, message string)
}

func (p *progressReporter) AOIProgress(status string, done bool, aoiCount int) {
	p.onProgress("phase0", status)
}
func (p *progressReporter) InitBatches(batches []BatchInfo) {
	p.onProgress("phase1", fmt.Sprintf("Initialized %d batches", len(batches)))
}
func (p *progressReporter) BatchProgress(batch int, status BatchStatus) {
	label := "done"
	switch status {
	case StatusActive:
		label = "active"
	case StatusCached:
		label = "cached"
	case StatusFailed:
		label = "failed"
	}
	p.onProgress("phase1", fmt.Sprintf("Batch %d: %s", batch+1, label))
}
func (p *progressReporter) SynthesisStarted() {
	p.onProgress("phase2", "Synthesizing review...")
}
func (p *progressReporter) Token(token string) {}

// ── Shared pipeline steps ────────────────────────────────────────────────

// DiscoverPRBrief runs PR-specific context discovery with state caching.
// Mirrors DiscoverProjectContext: returns the cached brief on hash match,
// otherwise gathers gh data and summarizes via the fast client.
//
// Failure is non-fatal — on any error returns the cached brief (if any)
// or empty string. The brief is a quality enhancement; review can
// proceed without it.
func DiscoverPRBrief(
	ctx context.Context,
	fastClient ai.Client,
	pr *git.PullRequest,
	reviewState *state.State,
	onProgress func(string),
) (string, error) {
	if onProgress == nil {
		onProgress = func(string) {}
	}

	var cachedBrief, cachedHash string
	if reviewState != nil {
		cachedBrief, cachedHash = reviewState.GetPRBrief()
	}

	result, err := prcontext.BuildPRBrief(ctx, fastClient, pr, reviewState, cachedHash, onProgress)
	if err != nil {
		// Defensive — BuildPRBrief never returns errors today (all paths
		// log and return empty Brief), but mirror DiscoverProjectContext's
		// shape for symmetry.
		return cachedBrief, err
	}

	if result.FromCache {
		return cachedBrief, nil
	}

	// Cache the new brief on success.
	if reviewState != nil && result.Summary != "" {
		reviewState.SetPRBrief(result.Summary, result.InputHash)
	}
	return result.Summary, nil
}

// DiscoverProjectContext runs project context discovery with state caching.
// Both the audit and review pipelines use this to get a project summary.
func DiscoverProjectContext(
	ctx context.Context,
	client ai.Client,
	repoRoot string,
	reviewState *state.State,
	onProgress func(string),
) (string, error) {
	if onProgress == nil {
		onProgress = func(string) {}
	}

	var cachedCtx, cachedHash string
	if reviewState != nil {
		cachedCtx, cachedHash = reviewState.GetProjectContext()
	}

	result, err := project.Discover(ctx, repoRoot, client, cachedHash, onProgress)
	if err != nil {
		return cachedCtx, err
	}

	if result.FromCache {
		return cachedCtx, nil
	}

	// Cache the new context
	if reviewState != nil && result.Summary != "" {
		reviewState.SetProjectContext(result.Summary, result.InputHash)
	}
	return result.Summary, nil
}

// RecheckSettings is an optional settings bundle for RunRecheck. Zero-value
// fields fall back to defaults inside RecheckFindings.
type RecheckSettings struct {
	MaxConcurrency int
}

// RunRecheck validates and deduplicates deep findings. On failure, returns
// the original findings unchanged (non-fatal). The returned bool indicates
// whether the findings were modified by the recheck.
func RunRecheck(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	mode Mode,
	projectContext string,
	onProgress func(string),
	debugHook func(systemPrompt, userMsg, response string),
	settings ...RecheckSettings,
) ([]state.DeepFinding, bool) {
	if len(findings) == 0 {
		return findings, false
	}

	if onProgress == nil {
		onProgress = func(string) {}
	}

	var s RecheckSettings
	if len(settings) > 0 {
		s = settings[0]
	}

	onProgress(fmt.Sprintf("Rechecking %d findings...", len(findings)))

	recheckResult, recheckErr := RecheckFindings(ctx, client, findings, RecheckOptions{
		Mode:           mode,
		ProjectContext: projectContext,
		MaxConcurrency: s.MaxConcurrency,
		OnLLMCall:      debugHook,
	})
	if recheckErr != nil {
		log.Printf("Recheck failed (non-fatal): %v — keeping all findings", recheckErr)
		onProgress("Recheck failed, keeping all findings")
		return findings, false
	}

	msg := fmt.Sprintf("Recheck complete: kept %d, dismissed %d, consolidated %d, modified %d",
		len(recheckResult.Findings), recheckResult.DismissedCount,
		recheckResult.ConsolidatedCount, recheckResult.ModifiedCount)
	log.Printf("Recheck: %s", msg)
	onProgress(msg)
	return recheckResult.Findings, true
}

// ── Core review pipeline ────────────────────────────────────────────────

// CoreOptions configures the shared review pipeline core (used by both TUI and CLI).
type CoreOptions struct {
	PRMeta             string
	RawDiffs           map[string]string
	CustomInstructions string
	ReviewState        *state.State
	ParallelReviews    int
	Base, Head         string // git refs for re-diffing
	AOIContextLines    int
	RepoRoot           string
	NoSynthesis        bool

	// SkipSynthesis tells the pipeline to stop after recheck and skip
	// the synthesis (Phase 2) entirely. The CoreResult comes back with
	// Review=nil and DeepFindings populated. The TUI uses this by
	// default — synthesis is an expensive extra LLM call that doesn't
	// add value to the navigable findings list. Headless review keeps
	// it (set false) because CI consumers want the JSON ReviewOutput.
	//
	// Different from NoSynthesis: NoSynthesis still returns a Review
	// (with raw findings string) to satisfy the legacy contract.
	// SkipSynthesis returns no Review at all — DeepFindings is the
	// source of truth for the UI.
	SkipSynthesis bool

	// PR is the pull request metadata, when one is available. Used by
	// the PR-brief discovery step in Phase 0 to gather comments, prior
	// AI reviews, and CI status from gh. Nil for audit mode (no PR).
	PR *git.PullRequest
}

// CoreResult holds the output of the shared review pipeline core.
type CoreResult struct {
	Review           *state.AIReview
	StructuredReview *state.ReviewOutput
	DeepFindings     []state.DeepFinding
	FileFindings     map[string]string
}

// RunReviewCore is the shared pipeline core used by both the TUI and the headless CLI.
// It runs: Phase 0 (project context + AOI) → Phase 1 (review calls) → Phase 1c (recheck) → Phase 2 (synthesis).
func RunReviewCore(
	ctx context.Context,
	reviewClient ai.Client,
	aoiClient ai.Client,
	opts CoreOptions,
	rr Reporter,
) (*CoreResult, error) {
	if rr == nil {
		rr = NopReporter{}
	}

	reviewState := opts.ReviewState

	// Emit a cache-resume summary so the user can see what's being
	// reused on this run. Reduces the "did my re-run skip work?"
	// anxiety, especially after a failed/cancelled previous attempt.
	if msg := summarizeCacheState(reviewState, opts.RawDiffs); msg != "" {
		rr.AOIProgress("Resuming: "+msg, false, 0)
	}

	// ── Phase 0: Project context + AOI pre-scan ──────────────────
	var securityDigest string
	var aoiScanResults []security.AOIScanResult

	// Project context discovery
	var projectContext string
	if opts.RepoRoot != "" {
		pctx, err := DiscoverProjectContext(ctx, aoiClient, opts.RepoRoot, reviewState, func(status string) {
			rr.AOIProgress("Project context: "+status, false, 0)
		})
		if err != nil {
			log.Printf("Project context discovery failed (non-fatal): %v", err)
		}
		projectContext = pctx
	}

	// PR Brief discovery — condensed summary of comments / prior AI
	// reviews / CI status. Appended to PRMeta so every downstream
	// prompt sees it. Non-fatal: failure leaves PRMeta unchanged.
	if opts.PR != nil {
		brief, err := DiscoverPRBrief(ctx, aoiClient, opts.PR, reviewState, func(status string) {
			rr.AOIProgress("PR brief: "+status, false, 0)
		})
		if err != nil {
			log.Printf("PR brief discovery failed (non-fatal): %v", err)
		}
		if brief != "" {
			if !strings.HasSuffix(opts.PRMeta, "\n") {
				opts.PRMeta += "\n"
			}
			opts.PRMeta += brief
			if !strings.HasSuffix(brief, "\n") {
				opts.PRMeta += "\n"
			}
			// Persist immediately so the brief survives even if later
			// phases fail.
			if reviewState != nil {
				if saveErr := state.Save(reviewState); saveErr != nil {
					log.Printf("Warning: failed to save state after PR brief: %v", saveErr)
				}
			}
		}
	}

	// AOI pre-scan
	aoiContextLines := opts.AOIContextLines
	if aoiContextLines <= 0 {
		aoiContextLines = 3
	}

	if aoiClient != nil {
		rr.AOIProgress("starting security pre-scan...", false, 0)

		aoiDiffs := opts.RawDiffs
		if aoiContextLines > 3 && opts.Base != "" && opts.Head != "" {
			rr.AOIProgress(fmt.Sprintf("re-diffing with %d context lines...", aoiContextLines), false, 0)
			aoiDiffs = make(map[string]string, len(opts.RawDiffs))
			for filePath := range opts.RawDiffs {
				d, err := git.GetRawDiffWithContext(opts.Base, opts.Head, filePath, aoiContextLines)
				if err != nil {
					log.Printf("AOI re-diff failed for %s (falling back to 3-line): %v", filePath, err)
					aoiDiffs[filePath] = opts.RawDiffs[filePath]
				} else {
					aoiDiffs[filePath] = d
				}
			}
		}

		// Build AOI cache from state
		var aoiCache map[string]*security.AOIScanResult
		if reviewState != nil {
			aoiCache = make(map[string]*security.AOIScanResult)
			for filePath := range aoiDiffs {
				raw, cachedCtxLines := reviewState.GetAOIResults(filePath)
				if raw != nil && cachedCtxLines == aoiContextLines {
					var cached security.AOIScanResult
					if err := json.Unmarshal(raw, &cached); err == nil {
						aoiCache[filePath] = &cached
					}
				} else if raw != nil && cachedCtxLines != aoiContextLines {
					log.Printf("AOI cache miss for %s: context lines changed (%d -> %d)", filePath, cachedCtxLines, aoiContextLines)
				}
			}
		}

		aoiReport, err := security.ScanAreasOfInterest(ctx, aoiClient, aoiDiffs, aoiCache, func(status string) {
			rr.AOIProgress(status, false, 0)
		})
		if err != nil {
			log.Printf("AOI scan failed (non-fatal): %v", err)
			rr.AOIProgress("security pre-scan failed (continuing without)", true, 0)
		} else if aoiReport != nil {
			// Save AOI results to state
			if reviewState != nil {
				for _, fileResult := range aoiReport.Files {
					filePath := fileResult.File
					if !reviewState.HasFile(filePath) {
						cleaned := filepath.Clean(filePath)
						if cleaned != filePath && reviewState.HasFile(cleaned) {
							log.Printf("AOI path normalized: %q -> %q", filePath, cleaned)
							filePath = cleaned
						} else {
							log.Printf("Warning: AOI returned file %q not found in state (skipping cache)", filePath)
							continue
						}
					}
					if data, err := json.Marshal(fileResult); err == nil {
						reviewState.SetAOIResults(filePath, data, aoiContextLines)
					} else {
						log.Printf("Warning: failed to marshal AOI result for %s: %v", filePath, err)
					}
				}
				if err := state.Save(reviewState); err != nil {
					log.Printf("Warning: failed to persist AOI results: %v", err)
				}
			}

			if aoiReport.TotalAOIs > 0 {
				securityDigest = aoiReport.SecurityDigest
				aoiScanResults = aoiReport.Files
				rr.AOIProgress(
					fmt.Sprintf("found %d areas of interest", aoiReport.TotalAOIs),
					true, aoiReport.TotalAOIs,
				)
			} else {
				rr.AOIProgress("no security areas of interest found", true, 0)
			}
		} else {
			rr.AOIProgress("no security areas of interest found", true, 0)
		}
	}

	// Build enhanced instructions
	enhancedInstructions := ""
	if projectContext != "" {
		enhancedInstructions = projectContext + "\n\n"
	}
	enhancedInstructions += opts.CustomInstructions
	if securityDigest != "" {
		enhancedInstructions += "\n\n" + securityDigest
	}
	enhancedInstructions = strings.TrimSpace(enhancedInstructions)

	// ── Phase 1: AOI-driven review + fallback batches ────────────

	routeResult := RouteAOIs(aoiScanResults, nil, 10)

	aoiCoveredFiles := make(map[string]bool)
	var reviewCalls []ReviewCall
	if routeResult != nil && routeResult.TotalAOIs > 0 {
		reviewCalls = routeResult.PrioritizedCalls(0)
		for _, call := range reviewCalls {
			for _, f := range call.Files {
				aoiCoveredFiles[f] = true
			}
		}
		log.Printf("AOI routing: %s", routeResult.FormatSummary())
	}

	// Build fallback batches for files WITHOUT AOIs
	fallbackDiffs := make(map[string]string)
	for fp, diff := range opts.RawDiffs {
		if !aoiCoveredFiles[fp] && !config.ShouldExcludeFromReview(fp) {
			fallbackDiffs[fp] = diff
		}
	}
	fallbackBatches := BuildBatches(fallbackDiffs)

	totalCalls := len(reviewCalls) + len(fallbackBatches)
	if totalCalls == 0 {
		return nil, fmt.Errorf("no files to review")
	}

	// Initialize batch list in reporter
	batchInfos := make([]BatchInfo, 0, totalCalls)
	for _, call := range reviewCalls {
		label := call.Category
		if call.Subcategory != "" {
			label += "/" + call.Subcategory
		}
		if call.Type == "individual" {
			label += " [critical]"
		}
		batchInfos = append(batchInfos, BatchInfo{
			Label:    label,
			NumFiles: len(call.Files),
		})
	}
	for _, b := range fallbackBatches {
		batchInfos = append(batchInfos, BatchInfo{
			Label:    b.Label,
			NumFiles: len(b.Files),
		})
	}
	rr.InitBatches(batchInfos)

	// ── Phase 1a: AOI-driven review calls ────────────────────────
	var allFindings strings.Builder
	allFileFindings := make(map[string]string)
	var deepFindings []state.DeepFinding

	if len(reviewCalls) > 0 {
		maxConc := opts.ParallelReviews
		if maxConc <= 0 {
			maxConc = 5
		}

		execOpts := ExecuteOptions{
			Mode:               ModePR,
			ProjectContext:     projectContext,
			CustomInstructions: enhancedInstructions,
			MaxConcurrency:     maxConc,
			OnProgress: func(completed, total int, cached bool, callErr error) {
				idx := completed - 1
				if idx < 0 || idx >= len(reviewCalls) {
					return
				}
				if cached {
					rr.BatchProgress(idx, StatusCached)
				} else if callErr != nil {
					rr.BatchProgress(idx, StatusFailed)
				} else {
					rr.BatchProgress(idx, StatusDone)
				}
			},
		}

		// Wire up deep review caching to review state
		if reviewState != nil {
			execOpts.CacheGet = func(key string) *state.DeepReviewResult {
				return reviewState.GetDeepReview(key)
			}
			execOpts.CacheSet = func(key string, result *state.DeepReviewResult) {
				reviewState.SetDeepReview(key, result)
				// Persist immediately so cache survives crashes
				if err := state.Save(reviewState); err != nil {
					log.Printf("Warning: failed to persist deep review cache: %v", err)
				}
			}
		}

		execResult, execErr := RunReviewCalls(ctx, reviewClient, reviewCalls, execOpts)
		if execErr != nil {
			return nil, fmt.Errorf("AOI review: %w", execErr)
		}

		deepFindings = execResult.Findings
		AppendDeepFindings(&allFindings, allFileFindings, deepFindings)

		// Persist the deep findings to state immediately so a crash,
		// cancellation, or skipped synthesis doesn't throw them away.
		// This is the load-bearing fix: previously findings were only
		// saved when the final Review object was constructed, which
		// meant any failure between here and synthesis discarded them.
		if reviewState != nil {
			reviewState.SetDeepFindings(deepFindings)
			if err := state.Save(reviewState); err != nil {
				log.Printf("Warning: failed to persist deep findings after Phase 1a: %v", err)
			}
		}

		// Mark all AOI call batches as done
		for i := range reviewCalls {
			rr.BatchProgress(i, StatusDone)
		}
	}

	aoiCallOffset := len(reviewCalls)

	// ── Phase 1b: Fallback directory batches ─────────────────────
	if len(fallbackBatches) > 0 {
		fbReporter := &OffsetReporter{RR: rr, Offset: aoiCallOffset}

		fbFindings, fbFF, fbErr := RunBatchesOnly(ctx, reviewClient, opts.PRMeta, opts.RawDiffs,
			enhancedInstructions, reviewState, fallbackBatches, fbReporter)
		if fbErr != nil {
			return nil, fmt.Errorf("fallback batches: %w", fbErr)
		}

		allFindings.WriteString(fbFindings)
		for f, findings := range fbFF {
			allFileFindings[f] = findings
		}
	}

	// ── Phase 1c: Recheck ────────────────────────────────────────
	rechecked, changed := RunRecheck(ctx, reviewClient, deepFindings, ModePR, projectContext, nil, nil)
	if changed {
		deepFindings = rechecked
		// Rebuild synthesis input from rechecked findings
		allFindings.Reset()
		AppendDeepFindings(&allFindings, allFileFindings, deepFindings)
		// Persist the post-recheck findings (which may be deduped /
		// consolidated / dismissed relative to the pre-recheck set).
		if reviewState != nil {
			reviewState.SetDeepFindings(deepFindings)
			if err := state.Save(reviewState); err != nil {
				log.Printf("Warning: failed to persist deep findings after recheck: %v", err)
			}
		}
	}

	// ── Phase 2: Synthesis ───────────────────────────────────────
	// SkipSynthesis (TUI default): return immediately with DeepFindings
	// as the source of truth. Review is nil — the UI renders findings
	// directly from state.DeepFindings.
	if opts.SkipSynthesis {
		return &CoreResult{
			DeepFindings: deepFindings,
			FileFindings: allFileFindings,
		}, nil
	}
	if opts.NoSynthesis {
		return &CoreResult{
			Review: &state.AIReview{
				Findings: allFindings.String(),
			},
			DeepFindings: deepFindings,
			FileFindings: allFileFindings,
		}, nil
	}

	synthResult, synthErr := RunSynthesis(ctx, reviewClient, opts.PRMeta, opts.RawDiffs,
		enhancedInstructions, allFindings.String(), allFileFindings, rr)
	if synthErr != nil {
		return nil, synthErr
	}

	return &CoreResult{
		Review:           synthResult.Review,
		StructuredReview: synthResult.Structured,
		DeepFindings:     deepFindings,
		FileFindings:     allFileFindings,
	}, nil
}

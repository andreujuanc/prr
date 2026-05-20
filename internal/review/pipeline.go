package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/bugpriors"
	"github.com/andreujuanc/prr/internal/classify"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/dbg"
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

	// BugPriors mines fix-shaped commits from the repo's git log and
	// injects them as a "Known failure modes in this codebase" section
	// into every Phase 3 deep-review prompt. Off by default — opt in
	// via the --bug-priors CLI flag.
	BugPriors bool

	// ReviewMode controls which files reach Phase 3. See ReviewMode
	// constants for available modes. Empty string uses the package
	// default (currently ReviewModeFull).
	ReviewMode ReviewMode
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

// BuildPRMeta is the canonical PR-metadata header passed to RunReviewCore.
//
// Both `prr review` (headless) and the TUI's `a` keystroke must feed
// the model the SAME header so the two paths produce comparable
// results — divergent prMeta has bitten us once (the TUI used to
// append a "Files changed" listing + a hint prompt, while the CLI
// passed only the header).
//
// Shape:
//
//	PR #N: <title>
//	Description:
//	<body, when non-empty>
//	Base: <base ref> → Head: <head ref>
//	<blank>
//
// Safe to call with a nil PR — returns empty string.
func BuildPRMeta(pr *git.PullRequest) string {
	if pr == nil {
		return ""
	}
	var meta strings.Builder
	meta.WriteString(fmt.Sprintf("PR #%d: %s\n", pr.Number, pr.Title))
	if pr.Body != "" {
		meta.WriteString(fmt.Sprintf("Description:\n%s\n", pr.Body))
	}
	meta.WriteString(fmt.Sprintf("Base: %s → Head: %s\n\n", pr.BaseRefName, pr.HeadRefName))
	return meta.String()
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
	// path and the TUI path go through there.
	prMeta := BuildPRMeta(pr)

	// Run the shared core pipeline.
	var rr Reporter = &progressReporter{onProgress: onProgress}
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
		Debug:              opts.Debug,
		BugPriors:          opts.BugPriors,
		ReviewMode:         opts.ReviewMode,
	}, rr)
	if err != nil {
		return nil, err
	}

	// Validate / normalise the structured review before handing it off.
	// Dropped findings (hallucinated file paths, empty titles, etc.)
	// surface in the log so the count is visible without polluting the
	// return shape.
	if coreResult.StructuredReview != nil {
		hunks := make(map[string][]HunkRange, len(rawDiffs))
		for path, patch := range rawDiffs {
			hunks[path] = ParseHunkRanges(patch)
		}
		_, dropped := ValidateAndNormalize(coreResult.StructuredReview, pr.Files, hunks)
		if len(dropped) > 0 {
			log.Printf("validation: dropped %d malformed finding(s)", len(dropped))
			for _, d := range dropped {
				log.Printf("  - %q (%s): %s", d.Title, d.File, d.Reason)
			}
		}
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

	// Synthesis streaming counters. Used by Token to fill the
	// synthesis progress bar based on actual content received from
	// the LLM rather than the old sin-wave pulse.
	synthReceived int
	synthLastEmit int
}

func (p *progressReporter) DiscoveryProgress(status string) {
	p.onProgress("discovery", status)
}
func (p *progressReporter) ClassifyProgress(status string) {
	p.onProgress("classify", status)
}
func (p *progressReporter) AOIPrescanProgress(status string, done bool, aoiCount int) {
	p.onProgress("aoi", status)
}
func (p *progressReporter) InitBatches(batches []BatchInfo) {
	// Emit total + breakdown in one message so the TUI's deep-review
	// row can show "5 AOI-driven + 7 general" instead of just a flat
	// count. When there are no AOI-driven calls the parens are still
	// useful — the user sees "12 batches (12 general)" and can
	// reconcile that with the upstream "0 AOIs" they just saw.
	var aoi, general int
	for _, b := range batches {
		switch b.Kind {
		case BatchAOIDriven:
			aoi++
		case BatchGeneral:
			general++
		}
	}
	p.onProgress("phase1", fmt.Sprintf("Initialized %d batches (%d AOI-driven, %d general)",
		len(batches), aoi, general))

	// Per-batch identity. These feed the Batches panel parser so each
	// batch row knows what it's about. The aggregate line above keeps
	// the phase Summary working; the per-batch lines populate
	// progress.State.Batches without touching the aggregate counters.
	for i, b := range batches {
		kind := "aoi-driven"
		if b.Kind == BatchGeneral {
			kind = "general"
		}
		p.onProgress("phase1", fmt.Sprintf("Batch %d: init label=%q files=%d kind=%s",
			i+1, b.Label, b.NumFiles, kind))
	}
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
func (p *progressReporter) BatchStream(batch int, bytes int) {
	// The producer (RunReviewCalls / RunBatchesOnly) already throttles
	// at ≥256-byte deltas, so we just forward verbatim. The Batches
	// panel parser updates the per-batch Bytes counter on every emit.
	p.onProgress("phase1", fmt.Sprintf("Batch %d: stream bytes=%d", batch+1, bytes))
}
func (p *progressReporter) RecheckProgress(status string) {
	p.onProgress("recheck", status)
}
func (p *progressReporter) SynthesisStarted() {
	p.onProgress("phase2", "Synthesizing review...")
	// Reset streaming counters in case this is a re-run within the
	// same session (cached batches but uncached synthesis).
	p.synthReceived = 0
	p.synthLastEmit = 0
	// Seed the estimate so synthesisProgress can compute a ratio.
	// We don't have a clean per-finding count here (synthesis input
	// is a string of formatted per-batch findings), so use a
	// generous fixed estimate. Review syntheses are bounded similarly
	// to audit's; erring HIGH keeps the bar honest — it fills slower
	// than reality and never claims done early.
	p.onProgress("phase2", "synthesis estimate 6000")
}
func (p *progressReporter) Token(token string) {
	if len(token) == 0 || token[0] == 0x00 {
		// Control tokens (\x00THOUGHT:..., \x00TOOL_*:...) aren't
		// part of the output text — exclude them so the bar tracks
		// only what the user reads.
		return
	}
	p.synthReceived += len(token)
	// Throttle: emit roughly every 150 chars (~30-50 tokens) so the
	// TUI sees smooth motion without one event per token.
	if p.synthReceived-p.synthLastEmit >= 150 {
		p.onProgress("phase2", fmt.Sprintf("synthesis received %d", p.synthReceived))
		p.synthLastEmit = p.synthReceived
	}
}

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
		// Don't fall back to the cached context here — if Discover
		// failed, it's because the LLM call failed on inputs whose
		// hash didn't match the cache. The cached summary is for a
		// different repo state and would be misleading. Surface the
		// error so the caller can abort with a clear message.
		return "", err
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

// DiscoverRuntimeModel runs Phase 0.5 runtime-model discovery with
// state caching. Mirrors DiscoverProjectContext's contract: returns
// the cached model on hash match, otherwise calls the LLM and caches
// the new result.
//
// The runtime model is a quality enhancement, not load-bearing — on
// LLM or parse failure we log and return a nil model rather than
// failing the whole audit. The caller threads the (possibly-nil)
// model into Phase 3 prompt construction.
func DiscoverRuntimeModel(
	ctx context.Context,
	client ai.Client,
	repoRoot string,
	projectSummary string,
	reviewState *state.State,
	onProgress func(string),
) *state.RuntimeModel {
	if onProgress == nil {
		onProgress = func(string) {}
	}

	var cachedModel *state.RuntimeModel
	var cachedHash string
	if reviewState != nil {
		cachedModel, cachedHash = reviewState.GetRuntimeModel()
	}

	res, err := project.DiscoverRuntimeModel(ctx, client, repoRoot, projectSummary, cachedHash, onProgress)
	if err != nil {
		// Non-fatal — log and fall back to whatever we had cached
		// (possibly nil). Phase 3 still runs; the prompt just omits
		// the runtime model section.
		log.Printf("Runtime model discovery failed (non-fatal): %v", err)
		return cachedModel
	}

	if res.FromCache {
		return cachedModel
	}

	if reviewState != nil && res.Model != nil {
		reviewState.SetRuntimeModel(res.Model, res.InputHash)
	}
	return res.Model
}

// RecheckSettings is an optional settings bundle for RunRecheck. Zero-value
// fields fall back to defaults inside RecheckFindings.
type RecheckSettings struct {
	MaxConcurrency int

	// RepoRoot is the absolute path to the repository root. When
	// set, the dismiss pass runs a test-suite cross-check per
	// finding (see RecheckOptions.RepoRoot). Empty = skip.
	RepoRoot string
}

// RunRecheck validates and deduplicates deep findings. On failure, returns
// the original findings unchanged (non-fatal). The returned bool indicates
// whether the findings were modified by the recheck. Dismissals carries
// the per-finding rationale log; callers persist it on State so the
// audit report can show users what got dismissed and why.
func RunRecheck(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	mode Mode,
	projectContext string,
	onProgress func(string),
	debugHook func(systemPrompt, userMsg, response string),
	settings ...RecheckSettings,
) (kept []state.DeepFinding, dismissals []state.DismissedRecord, changed bool) {
	if onProgress == nil {
		onProgress = func(string) {}
	}

	if len(findings) == 0 {
		// Still ping the progress channel so the Recheck phase row
		// activates and gets promoted to done — without this, a clean
		// PR with no deep findings leaves the row in gray
		// (PhaseWaiting), which the user reads as "skipped".
		onProgress("no findings to recheck")
		return findings, nil, false
	}

	var s RecheckSettings
	if len(settings) > 0 {
		s = settings[0]
	}

	onProgress(fmt.Sprintf("Rechecking %d findings...", len(findings)))

	recheckResult, recheckErr := RecheckFindings(ctx, client, findings, RecheckOptions{
		Mode:           mode,
		ProjectContext: projectContext,
		RepoRoot:       s.RepoRoot,
		MaxConcurrency: s.MaxConcurrency,
		OnLLMCall:      debugHook,
		OnProgress: func(done, total int) {
			// Counter-only emit. Previously "rechecked X/Y findings"
			// duplicated the X/Y already shown by the inline counter.
			// "findings" is implicit from the phase label "Recheck".
			onProgress(fmt.Sprintf("rechecked %d/%d", done, total))
		},
	})
	if recheckErr != nil {
		log.Printf("Recheck failed (non-fatal): %v — keeping all findings", recheckErr)
		onProgress("Recheck failed, keeping all findings")
		return findings, nil, false
	}

	msg := fmt.Sprintf("Recheck complete: kept %d, dismissed %d, consolidated %d, modified %d",
		len(recheckResult.Findings), recheckResult.DismissedCount,
		recheckResult.ConsolidatedCount, recheckResult.ModifiedCount)
	log.Printf("Recheck: %s", msg)
	onProgress(msg)
	return recheckResult.Findings, recheckResult.Dismissed, true
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

	// Debug, when true, prints every LLM prompt and response to stderr
	// via internal/dbg. Covers the AOI pre-scan, deep review calls,
	// and recheck. Synthesis is not yet instrumented (no OnLLMCall
	// hook on RunSynthesis).
	Debug bool

	// BugPriors, when true, mines fix-shaped commits from git log and
	// injects them as a "Known failure modes in this codebase" section
	// into every Phase 3 deep-review prompt. The rendered content is
	// also folded into the deep-review cache key so a new fix-commit
	// landing between runs invalidates stale entries cleanly.
	BugPriors bool

	// ReviewMode controls which files reach Phase 3. ReviewModeAOIOnly
	// skips files without AOIs entirely; ReviewModeFull (the default)
	// reviews them through the fallback diff batches. Empty string
	// uses the package default.
	ReviewMode ReviewMode
}

// CoreResult holds the output of the shared review pipeline core.
type CoreResult struct {
	Review           *state.AIReview
	StructuredReview *state.ReviewOutput
	DeepFindings     []state.DeepFinding
	FileFindings     map[string]string
	// Coverage is the per-file breakdown of AOIs / findings /
	// dismissals / orphans. Nil when the pipeline ran without an
	// AOI scan or with empty inputs. The TUI uses this to render
	// the Coverage section even when synthesis is skipped.
	Coverage *state.ReviewCoverage
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
	dbgw := dbg.New(opts.Debug)

	// Emit a cache-resume summary so the user can see what's being
	// reused on this run. Reduces the "did my re-run skip work?"
	// anxiety, especially after a failed/cancelled previous attempt.
	if msg := summarizeCacheState(reviewState, opts.RawDiffs); msg != "" {
		rr.DiscoveryProgress("Resuming: " + msg)
	}

	// ── Discovery: project context + PR brief ────────────────────
	var securityDigest string
	var aoiScanResults []security.AOIScanResult

	// Project context discovery
	//
	// Phase 0 is load-bearing: every downstream prompt embeds the
	// project context. A failure here usually means the configured
	// fast model is unreachable. Continuing with empty context would
	// produce findings that ignore project conventions; previously
	// the project package silently degraded to a raw doc dump which
	// then bloated every later prompt. Fail fast.
	var projectContext string
	var runtimeModel *state.RuntimeModel
	if opts.RepoRoot != "" {
		pctx, err := DiscoverProjectContext(ctx, aoiClient, opts.RepoRoot, reviewState, func(status string) {
			rr.DiscoveryProgress("Project context: " + status)
		})
		if err != nil {
			return nil, fmt.Errorf("project context discovery: %w", err)
		}
		projectContext = pctx

		// Phase 0.5 — runtime model. Non-fatal: failure leaves
		// runtimeModel nil and Phase 3 prompts omit the section.
		runtimeModel = DiscoverRuntimeModel(ctx, aoiClient, opts.RepoRoot, projectContext, reviewState, func(status string) {
			rr.DiscoveryProgress("Runtime model: " + status)
		})
	}

	// PR Brief discovery — condensed summary of comments / prior AI
	// reviews / CI status. Appended to PRMeta so every downstream
	// prompt sees it. Non-fatal: failure leaves PRMeta unchanged.
	if opts.PR != nil {
		brief, err := DiscoverPRBrief(ctx, aoiClient, opts.PR, reviewState, func(status string) {
			rr.DiscoveryProgress("PR brief: " + status)
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

	// ── Classification: narrow per-file dimensions ───────────────
	// Each diffed file gets classified by the fast model (handler /
	// test / repository / model / …). The result drives the AOI
	// pre-scan: a test file doesn't get a cryptography pass, a
	// handler does get input-validation, etc. Cached per file path
	// in state.FileType; --no-cache forces re-classification.
	// Cache invalidation: RunPRReview already calls ClearAllCaches
	// when --no-cache is set, so the FileType lookups below find no
	// entries on a forced re-run. No need to thread the flag through.
	fileDimensions := classifyChangedFiles(ctx, aoiClient, reviewState, opts.RepoRoot, opts.RawDiffs, rr.ClassifyProgress)

	// AOI pre-scan
	aoiContextLines := opts.AOIContextLines
	if aoiContextLines <= 0 {
		aoiContextLines = 3
	}

	if aoiClient != nil {
		rr.AOIPrescanProgress("starting security pre-scan...", false, 0)

		aoiDiffs := opts.RawDiffs
		if aoiContextLines > 3 && opts.Base != "" && opts.Head != "" {
			rr.AOIPrescanProgress(fmt.Sprintf("re-diffing with %d context lines...", aoiContextLines), false, 0)
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

		var aoiDebugHook security.AOIDebugHook
		if dbgw.Enabled() {
			dbgw.Phase("AOI Pre-scan")
			aoiDebugHook = func(files []string, systemPrompt string, userMessage string, response string) {
				dbgw.Section(fmt.Sprintf("AOI Scan: %v", files))
				dbgw.Prompt(systemPrompt, userMessage)
				dbgw.Response(response)
				dbgw.Separator()
			}
		}
		aoiReport, err := security.ScanAreasOfInterestClassified(ctx, aoiClient, aoiDiffs, aoiCache, fileDimensions, func(status string) {
			rr.AOIPrescanProgress(status, false, 0)
		}, aoiDebugHook, false)
		if err != nil {
			log.Printf("AOI scan failed (non-fatal): %v", err)
			rr.AOIPrescanProgress("security pre-scan failed (continuing without)", true, 0)
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
						// PR review uses the diff-mode AOI prompt
						// (auditMode=false). Hash it into the cache
						// entry so future prompt edits auto-invalidate.
						reviewState.SetAOIResults(filePath, data, aoiContextLines, security.AOIScanPromptHash(false))
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
				rr.AOIPrescanProgress(
					fmt.Sprintf("found %d areas of interest", aoiReport.TotalAOIs),
					true, aoiReport.TotalAOIs,
				)
			} else {
				rr.AOIPrescanProgress("no security areas of interest found", true, 0)
			}
		} else {
			rr.AOIPrescanProgress("no security areas of interest found", true, 0)
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

	routeResult := RouteAOIs(aoiScanResults, nil, 5)

	aoiCoveredFiles := make(map[string]bool)
	var reviewCalls []ReviewCall
	if routeResult != nil && routeResult.TotalAOIs > 0 {
		reviewCalls = routeResult.PrioritizedCalls(0)
		// Attach the file diff to each call so the deep review prompt
		// shows the actual changed lines without forcing a tool call.
		// PR mode only — audit has no diff and uses AttachAOISources
		// instead.
		AttachFileDiffs(reviewCalls, opts.RawDiffs)
		for _, call := range reviewCalls {
			for _, f := range call.Files {
				aoiCoveredFiles[f] = true
			}
		}
		log.Printf("AOI routing: %s", routeResult.FormatSummary())
	}

	// Resolve the review mode for this run. Empty → package default
	// (currently ReviewModeFull). Invalid values are rejected upstream
	// by ParseReviewMode; here we only need to branch on the resolved
	// value. The aoi-only path skips fallback batches entirely so the
	// AOI scan is the sole signal for "what gets reviewed."
	mode := opts.ReviewMode
	if mode == "" {
		mode = defaultReviewMode
	}

	// Build fallback batches for files WITHOUT AOIs (full mode only).
	// In aoi-only mode the non-AOI files are intentionally skipped —
	// tracked under skippedNonAOI so the coverage report can surface
	// what was left unreviewed.
	fallbackDiffs := make(map[string]string)
	var skippedNonAOI []string
	for fp, diff := range opts.RawDiffs {
		if config.ShouldExcludeFromReview(fp) {
			continue
		}
		if aoiCoveredFiles[fp] {
			continue
		}
		switch mode {
		case ReviewModeAOIOnly:
			skippedNonAOI = append(skippedNonAOI, fp)
		default:
			fallbackDiffs[fp] = diff
		}
	}
	sort.Strings(skippedNonAOI)
	fallbackBatches := BuildBatches(fallbackDiffs)

	totalCalls := len(reviewCalls) + len(fallbackBatches)
	if totalCalls == 0 {
		if mode == ReviewModeAOIOnly && len(skippedNonAOI) > 0 {
			return nil, fmt.Errorf(
				"no files to review: --review-mode=aoi-only and no AOIs were found; %d file(s) skipped (use --review-mode=full to review them)",
				len(skippedNonAOI))
		}
		return nil, fmt.Errorf("no files to review")
	}

	if mode == ReviewModeAOIOnly && len(skippedNonAOI) > 0 {
		log.Printf("review-mode=aoi-only: %d file(s) skipped (no AOIs)", len(skippedNonAOI))
	}

	// Wrap fallback directory batches as ReviewCall entries with
	// Type="fallback-batch". After this point every item in
	// reviewCalls flows through the same executor (RunReviewCalls)
	// and produces DeepFinding-shape output — one queue, one
	// semaphore, one recheck pass over the union.
	for _, b := range fallbackBatches {
		fileDiffs := make(map[string]string, len(b.Files))
		for _, f := range b.Files {
			if d, ok := opts.RawDiffs[f]; ok {
				fileDiffs[f] = d
			}
		}
		reviewCalls = append(reviewCalls, ReviewCall{
			Type:      "fallback-batch",
			Category:  b.Label,
			Files:     b.Files,
			FileDiffs: fileDiffs,
		})
	}

	// Initialize batch list in reporter. Kind lets the progress UI
	// render the AOI-driven / general breakdown — without it the
	// "Initialized N batches" row gives no hint that some calls are
	// targeted on AOI findings while others are blanket diff reviews.
	batchInfos := make([]BatchInfo, 0, len(reviewCalls))
	for _, call := range reviewCalls {
		kind := BatchAOIDriven
		label := call.Category
		if call.Subcategory != "" {
			label += "/" + call.Subcategory
		}
		switch call.Type {
		case "individual":
			label += " [critical]"
		case "fallback-batch":
			kind = BatchGeneral
		}
		batchInfos = append(batchInfos, BatchInfo{
			Label:    label,
			NumFiles: len(call.Files),
			Kind:     kind,
		})
	}
	rr.InitBatches(batchInfos)

	// ── Phase 1a: AOI-driven review calls ────────────────────────
	var allFindings strings.Builder
	allFileFindings := make(map[string]string)
	var deepFindings []state.DeepFinding

	// deepDismissals / failedAOIIDs flow from Phase 3 through to the
	// coverage stamp at the end of the pipeline. Both default to nil
	// when no review calls run.
	var deepDismissals []state.DeepDismissal
	var failedAOIIDs []string

	if len(reviewCalls) > 0 {
		maxConc := opts.ParallelReviews
		if maxConc <= 0 {
			maxConc = 5
		}

		// Extract bug-priors once when opted in. Failure / empty repo /
		// no matches all return empty string — the prompt-builder
		// treats empty as "no priors section", so a miss costs nothing.
		// Error is intentionally dropped: bugpriors.Extract documents
		// it as best-effort and a priors miss must never fail a review.
		var bugPriorsContent string
		if opts.BugPriors && opts.RepoRoot != "" {
			rendered, _ := bugpriors.Extract(opts.RepoRoot, bugpriors.DefaultLookback)
			bugPriorsContent = rendered
		}

		execOpts := ExecuteOptions{
			Mode:               ModePR,
			ProjectContext:     projectContext,
			PRMeta:             opts.PRMeta,
			RuntimeModel:       runtimeModel,
			CustomInstructions: enhancedInstructions,
			MaxConcurrency:     maxConc,
			RepoRoot:           opts.RepoRoot,
			BugPriors:          bugPriorsContent,
			OnCallStart: func(idx int) {
				rr.BatchProgress(idx, StatusActive)
			},
			OnCallEnd: func(idx int, cached bool, callErr error) {
				switch {
				case cached:
					rr.BatchProgress(idx, StatusCached)
				case callErr != nil:
					rr.BatchProgress(idx, StatusFailed)
				default:
					rr.BatchProgress(idx, StatusDone)
				}
			},
			OnCallStream: func(idx, bytes int) {
				rr.BatchStream(idx, bytes)
			},
		}
		if dbgw.Enabled() {
			dbgw.Phase("Deep Review")
			execOpts.OnLLMCall = func(index int, call ReviewCall, systemPrompt string, userMsg string, response string) {
				label := call.Subcategory
				if label == "" {
					label = call.Category
				}
				dbgw.Section(fmt.Sprintf("Review Call %d [%s]: %s (%v)", index+1, call.Type, label, call.Files))
				dbgw.Prompt(systemPrompt, userMsg)
				dbgw.Response(response)
				dbgw.Separator()
			}
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
		deepDismissals = execResult.Dismissals
		failedAOIIDs = execResult.FailedAOIIDs
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
		// Note: do NOT emit another round of BatchProgress(StatusDone)
		// here. RunReviewCalls already fired the terminal events via
		// execOpts.OnProgress as each call completed, with the real
		// status (done / cached / failed). A second pass would
		// double-count completions and overwrite cached/failed with
		// done — both wrong.
	}

	// ── Phase 1c: Recheck ────────────────────────────────────────
	var recheckDebugHook func(systemPrompt, userMsg, response string)
	if dbgw.Enabled() {
		dbgw.Phase("Recheck")
		recheckDebugHook = func(systemPrompt, userMsg, response string) {
			dbgw.Section("Recheck LLM Call")
			dbgw.Prompt(systemPrompt, userMsg)
			dbgw.Response(response)
			dbgw.Separator()
		}
	}
	rechecked, dismissals, changed := RunRecheck(ctx, reviewClient, deepFindings, ModePR, projectContext,
		func(status string) { rr.RecheckProgress(status) }, recheckDebugHook,
		RecheckSettings{RepoRoot: opts.RepoRoot})
	if changed {
		deepFindings = rechecked
		// Rebuild synthesis input from rechecked findings
		allFindings.Reset()
		AppendDeepFindings(&allFindings, allFileFindings, deepFindings)
		// Persist the post-recheck findings (which may be deduped /
		// consolidated / dismissed relative to the pre-recheck set)
		// alongside the dismissal rationale log.
		if reviewState != nil {
			reviewState.SetDeepFindings(deepFindings)
			reviewState.SetRecheckDismissals(dismissals)
			if err := state.Save(reviewState); err != nil {
				log.Printf("Warning: failed to persist deep findings after recheck: %v", err)
			}
		}
	}

	// Coverage is computed deterministically from inputs already in
	// memory — never authored by the LLM, so it stays trustworthy
	// even when synthesis hallucinates. Stamped onto StructuredReview
	// when synthesis runs; surfaced through CoreResult.Coverage in
	// the SkipSynthesis / NoSynthesis paths.
	filesInScope := make([]string, 0, len(opts.RawDiffs))
	for f := range opts.RawDiffs {
		filesInScope = append(filesInScope, f)
	}
	coverage := BuildCoverage(aoiScanResults, deepFindings, deepDismissals, failedAOIIDs, filesInScope, skippedNonAOI)

	// ── Phase 2: Synthesis ───────────────────────────────────────
	// SkipSynthesis (TUI default): return immediately with DeepFindings
	// as the source of truth. Review is nil — the UI renders findings
	// directly from state.DeepFindings.
	if opts.SkipSynthesis {
		recordReviewMeta(reviewState, deepFindings, len(dismissals), "")
		return &CoreResult{
			DeepFindings: deepFindings,
			FileFindings: allFileFindings,
			Coverage:     coverage,
		}, nil
	}
	if opts.NoSynthesis {
		recordReviewMeta(reviewState, deepFindings, len(dismissals), "")
		return &CoreResult{
			Review: &state.AIReview{
				Findings: allFindings.String(),
			},
			DeepFindings: deepFindings,
			FileFindings: allFileFindings,
			Coverage:     coverage,
		}, nil
	}

	synthResult, synthErr := RunSynthesis(ctx, reviewClient, opts.PRMeta, opts.RawDiffs,
		enhancedInstructions, allFindings.String(), allFileFindings, rr)
	if synthErr != nil {
		return nil, synthErr
	}

	// Stamp coverage onto the structured output so JSON consumers
	// see it alongside findings. Synthesis itself doesn't author
	// the field — we trust the upstream count exactly because no
	// LLM authored it.
	if synthResult.Structured != nil && coverage != nil {
		synthResult.Structured.Coverage = coverage
	}

	synthVerdict := ""
	if synthResult.Structured != nil {
		synthVerdict = synthResult.Structured.Verdict
	}
	recordReviewMeta(reviewState, deepFindings, len(dismissals), synthVerdict)

	return &CoreResult{
		Review:           synthResult.Review,
		StructuredReview: synthResult.Structured,
		DeepFindings:     deepFindings,
		FileFindings:     allFileFindings,
		Coverage:         coverage,
	}, nil
}

// recordReviewMeta stamps a LastReview marker on state so the TUI can
// distinguish "review ran, clean PR" from "no review yet" — both
// previously left Review and DeepFindings empty and looked identical.
// Called at every successful end-of-run path in RunReviewCore.
//
// verdict is set when synthesis ran; for SkipSynthesis/NoSynthesis the
// verdict is inferred from finding counts (clean → approve, otherwise
// comment).
func recordReviewMeta(s *state.State, findings []state.DeepFinding, dismissed int, verdict string) {
	if s == nil {
		return
	}
	if verdict == "" {
		if len(findings) == 0 {
			verdict = "approve"
		} else {
			verdict = "comment"
		}
	}
	summary := ""
	switch {
	case len(findings) == 0 && dismissed == 0:
		summary = "No findings — PR looks clean."
	case len(findings) == 0 && dismissed > 0:
		summary = fmt.Sprintf("No surviving findings — recheck dismissed all %d.", dismissed)
	case len(findings) > 0:
		summary = fmt.Sprintf("%d finding(s).", len(findings))
		if dismissed > 0 {
			summary += fmt.Sprintf(" %d dismissed in recheck.", dismissed)
		}
	}
	s.SetLastReview(&state.ReviewMeta{
		Verdict:        verdict,
		Summary:        summary,
		FindingsCount:  len(findings),
		DismissedCount: dismissed,
	})
	if err := state.Save(s); err != nil {
		log.Printf("Warning: failed to persist LastReview marker: %v", err)
	}
}

// classifyChangedFiles classifies each diffed file by architectural
// role (handler / test / repository / …) so the AOI pre-scan can be
// narrowed to relevant dimensions per file. Returns a map of
// filepath → dimension slugs suitable for
// security.ScanAreasOfInterestClassified.
//
// File contents are read from the working tree relative to repoRoot.
// This matches what `prr audit` does via CollectFiles — by the time
// prr is invoked the user has already checked out the head ref we
// want to review. Files that can't be read (deleted, outside repo,
// perm error) are skipped — they get all dimensions (unknown
// classification) when the AOI scan runs.
//
// Classifications are cached in state.FileType. Diff-driven
// invalidation happens via state.SyncWithDiffs upstream (it clears
// AOI results when the diff hash changes but intentionally keeps
// FileType, since architectural role rarely shifts with a small
// diff). --no-cache is handled at the RunPRReview boundary by
// ClearAllCaches, which empties FileType too — so we don't need to
// thread the flag through here.
//
// Failures are non-fatal: a classifier error means we fall back to
// nil (all dimensions for all files), which is the same as the
// pre-classification behavior.
func classifyChangedFiles(
	ctx context.Context,
	aoiClient ai.Client,
	reviewState *state.State,
	repoRoot string,
	rawDiffs map[string]string,
	onProgress func(string),
) map[string][]string {
	if onProgress == nil {
		onProgress = func(string) {}
	}
	if aoiClient == nil || repoRoot == "" || len(rawDiffs) == 0 {
		return nil
	}

	files := make([]classify.File, 0, len(rawDiffs))
	for path := range rawDiffs {
		full := filepath.Join(repoRoot, path)
		content, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		files = append(files, classify.File{Path: path, Content: string(content)})
	}
	if len(files) == 0 {
		return nil
	}

	var cached map[string]classify.FileType
	if reviewState != nil {
		cached = make(map[string]classify.FileType, len(files))
		for _, f := range files {
			if ft := reviewState.GetFileType(f.Path); ft != "" {
				cached[f.Path] = classify.FileType(ft)
			}
		}
	}

	classifications, err := classify.Classify(ctx, aoiClient, files, cached, onProgress)
	if err != nil {
		log.Printf("Classification partial/failed (non-fatal): %v — affected files fall back to all dimensions", err)
	}

	if reviewState != nil {
		for path, ft := range classifications {
			reviewState.SetFileType(path, string(ft))
		}
	}

	dims := make(map[string][]string, len(classifications))
	for path, ft := range classifications {
		dims[path] = classify.DimensionsForType(ft)
	}
	return dims
}

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/audit"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/review"
	"github.com/andreujuanc/prr/internal/state"
	"github.com/andreujuanc/prr/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Set by GoReleaser via ldflags at build time.
var version = "dev"

func main() {
	// Force truecolor early so styled error output works too
	lipgloss.SetColorProfile(termenv.TrueColor)

	// Parse global flags — but pass through flags after "audit" subcommand
	debug := false
	useChroma := false
	args := os.Args[1:]
	var positional []string
	seenSubcommand := false
	for _, arg := range args {
		// Once we see a subcommand, pass all remaining args through
		if !seenSubcommand && (arg == "audit" || arg == "review" || arg == "config") {
			seenSubcommand = true
			positional = append(positional, arg)
			continue
		}
		if seenSubcommand {
			positional = append(positional, arg)
			continue
		}
		if arg == "--debug" {
			debug = true
		} else if arg == "--chroma" {
			useChroma = true
		} else if arg == "--version" || arg == "-v" {
			fmt.Println("prr " + version)
			os.Exit(0)
		} else if arg == "--help" || arg == "-h" {
			printUsage()
			os.Exit(0)
		} else {
			positional = append(positional, arg)
		}
	}

	var prNumber string
	if len(positional) >= 1 {
		// Check for subcommands
		if positional[0] == "audit" {
			runAudit(debug, positional[1:])
			return
		}
		if positional[0] == "review" {
			runReview(debug, positional[1:])
			return
		}
		if positional[0] == "config" {
			runConfig()
			return
		}
		prNumber = positional[0]
	}

	// Pre-flight checks before launching the TUI
	if err := preflight(prNumber, useChroma); err != nil {
		printError(err)
		os.Exit(1)
	}

	// Load config for AI
	cfg, err := loadConfigAndLogger(debug)
	if err != nil {
		printError(err)
		os.Exit(1)
	}

	// Apply saved theme
	if cfg.Theme != "" {
		ui.SetTheme(ui.ThemeByID(cfg.Theme))
	}

	// Create AI client based on provider
	aiClient := createAIClient(cfg)

	// Create AOI client — uses the same provider but with a cheap/fast model
	// for the security pre-scan. No tools needed (just diff analysis).
	aoiClient, aoiErr := createAOIClient(cfg)
	if aoiErr != nil {
		// AOI is optional for PR review mode — just log and continue without it
		log.Printf("AOI client not available: %v", aoiErr)
	}

	prLabel := prNumber
	if prLabel == "" {
		prLabel = "(picker)"
	}
	aoiModelName := resolveAOIModelName(aoiClient, cfg)
	aoiContextLines := resolveAOIContextLines(aoiModelName)
	log.Printf("Starting PR review TUI for PR #%s (strong: %s, fast: %s, aoi_context: %d)", prLabel, cfg.StrongModel, cfg.FastModel, aoiContextLines)

	model := ui.NewModel(prNumber, aiClient, aoiClient, cfg.ParallelReviews, aoiContextLines, useChroma)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	ui.SetProgram(p)

	// Ensure OpenCode server is killed on signals (SIGINT/SIGTERM).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		ui.Shutdown()
		os.Exit(1)
	}()

	if _, err := p.Run(); err != nil {
		log.Fatalf("Error running program: %v", err)
	}
	ui.Shutdown()
}

// ── Audit mode ─────────────────────────────────────────────────────────

func runAudit(debug bool, args []string) {
	// Parse audit-specific flags
	var opts audit.Options
	var focusStr, excludeStr, includeStr, outputPath string
	noSynth := false
	quiet := false
	for _, arg := range args {
		if after, ok := strings.CutPrefix(arg, "--focus="); ok {
			focusStr = after
		} else if after, ok := strings.CutPrefix(arg, "--exclude="); ok {
			excludeStr = after
		} else if after, ok := strings.CutPrefix(arg, "--include="); ok {
			includeStr = after
		} else if after, ok := strings.CutPrefix(arg, "--max-reviews="); ok {
			n, err := strconv.Atoi(after)
			if err != nil {
				printError(fmt.Errorf("--max-reviews=%s: %w", after, err))
				os.Exit(1)
			}
			opts.MaxReviews = n
		} else if after, ok := strings.CutPrefix(arg, "--concurrency="); ok {
			n, err := strconv.Atoi(after)
			if err != nil {
				printError(fmt.Errorf("--concurrency=%s: %w", after, err))
				os.Exit(1)
			}
			if n > 0 {
				opts.Concurrency.Classify = n
				opts.Concurrency.AOIScan = n
				opts.Concurrency.DeepReview = n
				opts.Concurrency.Recheck = n
				opts.Concurrency.HierarchicalSynth = n
			}
		} else if after, ok := strings.CutPrefix(arg, "--output="); ok {
			outputPath = after
		} else if arg == "--no-cache" {
			opts.NoCache = true
		} else if arg == "--no-synthesis" {
			noSynth = true
		} else if arg == "--quiet" || arg == "-q" {
			quiet = true
		} else if arg == "--debug" {
			opts.Debug = true
		} else if after, ok := strings.CutPrefix(arg, "--file="); ok {
			opts.DebugFile = after
		} else if after, ok := strings.CutPrefix(arg, "--audit-recent="); ok {
			n, err := strconv.Atoi(after)
			if err != nil {
				printError(fmt.Errorf("--audit-recent=%s: %w", after, err))
				os.Exit(1)
			}
			opts.AuditRecent = n
		} else if arg == "--sibling-cluster" {
			opts.SiblingClustering = true
		} else if arg == "--bug-priors" {
			opts.BugPriors = true
		} else if arg == "--help" || arg == "-h" {
			printAuditUsage()
			os.Exit(0)
		} else {
			printError(fmt.Errorf("unknown audit flag: %s", arg))
			os.Exit(1)
		}
	}

	if focusStr != "" {
		opts.Focus = strings.Split(focusStr, ",")
	}
	if excludeStr != "" {
		opts.ExcludePatterns = strings.Split(excludeStr, ",")
	}
	if includeStr != "" {
		opts.IncludePatterns = strings.Split(includeStr, ",")
	}

	// Must be in a git repo
	if err := runSilent("git", "rev-parse", "--git-dir"); err != nil {
		printError(fmt.Errorf("not a git repository"))
		os.Exit(1)
	}

	repoRoot, err := git.RepoRoot()
	if err != nil {
		printError(fmt.Errorf("finding repo root: %w", err))
		os.Exit(1)
	}
	opts.RepoRoot = repoRoot

	// Load .prr/audit-exclude if it exists
	excludeFile := filepath.Join(repoRoot, ".prr", "audit-exclude")
	filePatterns, err := audit.LoadExcludeFile(excludeFile)
	if err != nil {
		printError(fmt.Errorf("loading %s: %w", excludeFile, err))
		os.Exit(1)
	}
	opts.ExcludePatterns = append(opts.ExcludePatterns, filePatterns...)

	// Load config
	cfg, err := loadConfigAndLogger(debug)
	if err != nil {
		printError(err)
		os.Exit(1)
	}

	// Interactive model selection (or PRR_REVIEW_MODEL/PRR_AOI_MODEL
	// env vars). Mutates cfg.StrongModel/cfg.FastModel.
	strongRef, fastRef, err := cfg.ResolveModels()
	if err != nil {
		printError(err)
		os.Exit(1)
	}

	// Create AI clients
	reviewClient := createAIClient(cfg)
	// In audit mode, restrict review tools to code inspection only
	if agent, ok := reviewClient.(*ai.Agent); ok {
		ai.WithToolFilter([]string{"read_file", "grep", "list_dir", "glob"})(agent)
	}
	aoiClient, aoiErr := createAOIClient(cfg)
	if aoiErr != nil {
		printError(fmt.Errorf("AOI client not available — audit mode requires an AOI model: %w", aoiErr))
		os.Exit(1)
	}

	// Get AOI profile for context lines
	opts.AOIContextLines = resolveAOIContextLines(fastRef.ModelID)

	log.Printf("Starting audit (strong: %s, fast: %s, focus: %v)",
		cfg.StrongModel, cfg.FastModel, opts.Focus)

	// Load previous findings for comparison
	previousFindings, _ := audit.LoadSnapshot(repoRoot)

	// Run audit with progress UI (or plain mode in debug)
	ctx := context.Background()
	var result *audit.Result
	var synthesis *audit.SynthesisResult
	if opts.Debug {
		// In debug mode, skip Bubble Tea UI — run directly with simple progress to stderr
		result, synthesis, err = audit.RunPlain(ctx, reviewClient, aoiClient, opts,
			cfg.StrongModel, cfg.FastModel, noSynth)
	} else {
		result, synthesis, err = audit.RunWithUI(ctx, reviewClient, aoiClient, opts,
			cfg.StrongModel, cfg.FastModel, noSynth)
	}
	if err != nil {
		printError(err)
		os.Exit(1)
	}

	if !quiet {
		if len(result.Findings) == 0 {
			fmt.Fprintf(os.Stderr, "  %s\n\n", cliInfo.Render("No issues found."))
		} else {
			// Print findings sorted by severity
			renderDeepFindings(result.Findings)

			// Visual charts
			fmt.Fprint(os.Stderr, audit.RenderSeverityBar(result.Findings))
			fmt.Fprint(os.Stderr, audit.RenderCategoryChart(result.Findings))
		}

		// Print synthesis
		if synthesis != nil && synthesis.ExecutiveSummary != "" {
			fmt.Fprintf(os.Stderr, "  %s\n\n", cliHeader.Render("Executive Summary"))
			fmt.Fprintf(os.Stderr, "  %s\n\n", cliInfo.Render(synthesis.ExecutiveSummary))

			if len(synthesis.TopRisks) > 0 {
				fmt.Fprintf(os.Stderr, "  %s\n", cliHeader.Render("Top Risks"))
				for i, risk := range synthesis.TopRisks {
					fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, cliInfo.Render(risk))
				}
				fmt.Fprintln(os.Stderr)
			}

			if len(synthesis.SystemicPatterns) > 0 {
				fmt.Fprintf(os.Stderr, "  %s\n", cliHeader.Render("Systemic Patterns"))
				for _, p := range synthesis.SystemicPatterns {
					fmt.Fprintf(os.Stderr, "  • %s\n", cliInfo.Render(p))
				}
				fmt.Fprintln(os.Stderr)
			}

			if len(synthesis.Recommendations) > 0 {
				fmt.Fprintf(os.Stderr, "  %s\n", cliHeader.Render("Recommendations"))
				for i, rec := range synthesis.Recommendations {
					fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, cliInfo.Render(rec))
				}
				fmt.Fprintln(os.Stderr)
			}
		}

		// Comparison with previous audit
		if previousFindings != nil {
			comparison := audit.CompareFindings(result.Findings, previousFindings)
			fmt.Fprintf(os.Stderr, "  %s %s\n", cliDim.Render("[vs last audit]"), cliInfo.Render(comparison.FormatComparison()))
		}

		// Cross-cutting observations are used as input to Phase 4 synthesis
		// but not displayed directly — the synthesis output covers them.
	}

	// Show actual token usage and cost
	{
		u := result.Usage
		total := u.Total()
		aoiPricing := audit.DefaultPricing(fastRef.ModelID)
		reviewPricing := audit.DefaultPricing(strongRef.ModelID)

		aoiCost := float64(u.AOI.InputTokens)/1_000_000*aoiPricing.InputPerMTok +
			float64(u.AOI.OutputTokens)/1_000_000*aoiPricing.OutputPerMTok
		reviewCost := float64(u.Review.InputTokens+u.Recheck.InputTokens+u.Synth.InputTokens)/1_000_000*reviewPricing.InputPerMTok +
			float64(u.Review.OutputTokens+u.Recheck.OutputTokens+u.Synth.OutputTokens)/1_000_000*reviewPricing.OutputPerMTok
		totalCost := aoiCost + reviewCost

		costLine := fmt.Sprintf("Tokens: %dk in / %dk out | AOI: $%.4f | Review: $%.4f | Total: $%.4f",
			total.InputTokens/1000, total.OutputTokens/1000, aoiCost, reviewCost, totalCost)
		fmt.Fprintf(os.Stderr, "  %s %s\n", cliDim.Render("[cost]"), cliInfo.Render(costLine))
	}
	fmt.Fprintln(os.Stderr)

	// Save snapshot for future comparisons
	if err := audit.SaveSnapshot(repoRoot, result.Findings); err != nil {
		log.Printf("Warning: failed to save audit snapshot: %v", err)
	}

	// Export report if requested
	if outputPath != "" {
		if err := audit.Export(result, synthesis, outputPath); err != nil {
			printError(fmt.Errorf("exporting report: %w", err))
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "  Report saved to %s\n\n", cliInfo.Render(outputPath))
	}
}

func printAuditUsage() {
	logo := lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))

	fmt.Fprintf(os.Stderr, "\n  %s %s  %s\n\n",
		logo.Render("prr audit"),
		dim.Render(version),
		dim.Render("— full-project code audit"))
	fmt.Fprintf(os.Stderr, "  %s  prr audit [flags]\n\n",
		dim.Render("Usage:"))
	fmt.Fprintf(os.Stderr, "  %s\n", dim.Render("Flags:"))
	fmt.Fprintf(os.Stderr, "    --focus=<dims>       Comma-separated dimensions to focus on (default: all)\n")
	fmt.Fprintf(os.Stderr, "    --exclude=<globs>    Additional exclude patterns (comma-separated)\n")
	fmt.Fprintf(os.Stderr, "    --include=<globs>    Force-include patterns (override exclusions)\n")
	fmt.Fprintf(os.Stderr, "    --max-reviews=<n>    Cap Phase 3 review calls\n")
	fmt.Fprintf(os.Stderr, "    --concurrency=<n>    Set per-phase parallelism cap (default 5)\n")
	fmt.Fprintf(os.Stderr, "    --output=<path>      Export report (.json or .md)\n")
	fmt.Fprintf(os.Stderr, "    --no-cache           Ignore cached results, re-audit everything\n")
	fmt.Fprintf(os.Stderr, "    --no-synthesis       Skip Phase 4 executive summary synthesis\n")
	fmt.Fprintf(os.Stderr, "    --quiet, -q          Suppress terminal output (use with --output)\n")
	fmt.Fprintf(os.Stderr, "    --debug              Print LLM tool calls, user messages, and responses to stderr\n")
	fmt.Fprintf(os.Stderr, "                         (compact by default; set PRR_DEBUG_VERBOSE=1 to include\n")
	fmt.Fprintf(os.Stderr, "                          full system prompts and unelided file content)\n")
	fmt.Fprintf(os.Stderr, "    --file=<path>        Restrict audit to a single file (relative to repo root)\n")
	fmt.Fprintf(os.Stderr, "    --audit-recent=<n>   Restrict audit to files touched in the last <n> commits\n")
	fmt.Fprintf(os.Stderr, "    --sibling-cluster    Enable Phase 2.5 sibling-outlier detection (experimental)\n")
	fmt.Fprintf(os.Stderr, "    --bug-priors         Inject recent fix-shaped commits as known-failure priors\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  %s\n", dim.Render("Available dimensions:"))
	fmt.Fprintf(os.Stderr, "    authentication, authorization, input-validation, data-integrity,\n")
	fmt.Fprintf(os.Stderr, "    cryptography, error-handling, concurrency, external-io, financial,\n")
	fmt.Fprintf(os.Stderr, "    configuration, api-design, resource-management, testing, test-coverage,\n")
	fmt.Fprintf(os.Stderr, "    correctness, design, performance, readability, cross-cutting,\n")
	fmt.Fprintf(os.Stderr, "    observability, web-security\n\n")
}

// ── Review mode ────────────────────────────────────────────────────────

func runReview(debug bool, args []string) {
	var outputPath string
	noSynth := false
	noCache := false
	quiet := false
	reviewDebug := debug
	bugPriors := false

	// Parse flags and find the PR number
	var prNumber string
	for _, arg := range args {
		if after, ok := strings.CutPrefix(arg, "--output="); ok {
			outputPath = after
		} else if arg == "--no-cache" {
			noCache = true
		} else if arg == "--no-synthesis" {
			noSynth = true
		} else if arg == "--quiet" || arg == "-q" {
			quiet = true
		} else if arg == "--debug" {
			reviewDebug = true
		} else if arg == "--bug-priors" {
			bugPriors = true
		} else if arg == "--help" || arg == "-h" {
			printReviewUsage()
			os.Exit(0)
		} else if !strings.HasPrefix(arg, "-") {
			prNumber = arg
		}
	}

	if prNumber == "" {
		printReviewUsage()
		printError(fmt.Errorf("PR number is required: prr review <number>"))
		os.Exit(1)
	}

	// Pre-flight: must be in a git repo with gh CLI
	if err := runSilent("git", "rev-parse", "--git-dir"); err != nil {
		printError(fmt.Errorf("not a git repository"))
		os.Exit(1)
	}

	repoRoot, err := git.RepoRoot()
	if err != nil {
		printError(fmt.Errorf("finding repo root: %w", err))
		os.Exit(1)
	}

	if err := runSilent("gh", "auth", "status"); err != nil {
		printError(fmt.Errorf("gh CLI not authenticated. Run: gh auth login"))
		os.Exit(1)
	}

	// Load config
	cfg, err := loadConfigAndLogger(debug)
	if err != nil {
		printError(err)
		os.Exit(1)
	}
	cfg.Debug = reviewDebug

	// Interactive model selection (or PRR_REVIEW_MODEL/PRR_AOI_MODEL
	// env vars). Mutates cfg.StrongModel/cfg.FastModel before the
	// clients below read them.
	_, fastRef, err := cfg.ResolveModels()
	if err != nil {
		printError(err)
		os.Exit(1)
	}

	// Create AI clients
	reviewClient := createAIClient(cfg)

	aoiClient, aoiErr := createAOIClient(cfg)
	if aoiErr != nil {
		log.Printf("AOI client not available: %v", aoiErr)
	}

	aoiCtxLines := resolveAOIContextLines(fastRef.ModelID)

	log.Printf("Starting headless PR review for PR #%s (strong: %s, fast: %s)",
		prNumber, cfg.StrongModel, cfg.FastModel)

	// Plain cancellable context — stalls are now bounded at the HTTP
	// layer (provider RequestTimeout = ai.DefaultRequestTimeout) and
	// per-call retry (ai.RetryTransient) handles transient errors.
	// The previous watchdog ceremony has been retired.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := review.PRReviewOptions{
		PRNumber:           prNumber,
		RepoRoot:           repoRoot,
		NoCache:            noCache,
		NoSynthesis:        noSynth,
		ParallelReviews:    cfg.ParallelReviews,
		AOIContextLines:    aoiCtxLines,
		CustomInstructions: config.LoadCustomInstructions(),
		Debug:              reviewDebug,
		BugPriors:          bugPriors,
	}

	// Default: shared progress TUI (same as `prr audit`). Falls back
	// to plain stderr lines in --quiet (no UI) or --debug (so debug
	// output isn't clobbered by alt-screen animations).
	var result *review.PRReviewResult
	if quiet || reviewDebug {
		if !quiet {
			// Mirror `prr audit`'s RunPlain header so the stderr
			// stream is self-describing — useful when piping to logs.
			fmt.Fprintf(os.Stderr, "\n  review: %s  aoi: %s\n\n", cfg.StrongModel, cfg.FastModel)
		}
		result, err = review.RunPRReview(ctx, reviewClient, aoiClient, opts, func(phase, msg string) {
			if !quiet {
				fmt.Fprintf(os.Stderr, "[%s] %s\n", phase, msg)
			}
		})
	} else {
		headerInfo := fmt.Sprintf("review: %s  aoi: %s", cfg.StrongModel, cfg.FastModel)
		result, err = review.RunWithUI(ctx, reviewClient, aoiClient, opts, "PR #"+prNumber, headerInfo)
	}
	if err != nil {
		printError(err)
		os.Exit(1)
	}

	if quiet && outputPath == "" {
		os.Exit(0)
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "\n  %s  PR #%s: %s\n\n",
			cliHeader.Render("Review Complete"),
			prNumber,
			cliInfo.Render(result.PR.Title))

		// Print structured review if available
		if result.StructuredReview != nil {
			sr := result.StructuredReview

			// Verdict
			verdictStyle := cliInfo
			switch sr.Verdict {
			case "approve":
				verdictStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1")).Bold(true)
			case "request_changes":
				verdictStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8")).Bold(true)
			}
			fmt.Fprintf(os.Stderr, "  %s %s\n\n",
				cliHeader.Render("Verdict:"),
				verdictStyle.Render(sr.Verdict))

			// Summary
			if sr.Summary != "" {
				fmt.Fprintf(os.Stderr, "  %s\n", cliHeader.Render("Summary"))
				fmt.Fprintf(os.Stderr, "  %s\n\n", cliInfo.Render(sr.Summary))
			}

			// Findings
			if len(sr.Findings) > 0 {
				fmt.Fprintf(os.Stderr, "  %s (%d)\n\n", cliHeader.Render("Findings"), len(sr.Findings))
				for _, f := range sr.Findings {
					fmt.Fprintf(os.Stderr, "  %s %s\n",
						sevStyle(f.Severity).Render("["+f.Severity+"]"),
						cliInfo.Render(f.Title))
					if f.File != "" {
						fmt.Fprintf(os.Stderr, "    %s:%d (%s)\n", f.File, f.Line, f.Category)
					}
					if f.Detail != "" {
						fmt.Fprintf(os.Stderr, "    %s\n", cliDim.Render(f.Detail))
					}
					if f.Suggestion != "" {
						fmt.Fprintf(os.Stderr, "    Fix: %s\n", cliDim.Render(f.Suggestion))
					}
					fmt.Fprintln(os.Stderr)
				}
			}

			// Questions for author
			if len(sr.QuestionsForAuthor) > 0 {
				fmt.Fprintf(os.Stderr, "  %s\n", cliHeader.Render("Questions for Author"))
				for i, q := range sr.QuestionsForAuthor {
					fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, cliInfo.Render(q))
				}
				fmt.Fprintln(os.Stderr)
			}

			// Missing tests
			if len(sr.MissingTests) > 0 {
				fmt.Fprintf(os.Stderr, "  %s\n", cliHeader.Render("Missing Tests"))
				for _, t := range sr.MissingTests {
					fmt.Fprintf(os.Stderr, "  • %s\n", cliInfo.Render(t))
				}
				fmt.Fprintln(os.Stderr)
			}
		} else if result.Review != nil && result.Review.Summary != "" {
			// Fallback: raw summary
			fmt.Fprintf(os.Stderr, "  %s\n\n", cliInfo.Render(result.Review.Summary))
		}

		// Deep findings (from AOI)
		if len(result.DeepFindings) > 0 {
			fmt.Fprintf(os.Stderr, "  %s (%d)\n\n", cliHeader.Render("Security Findings"), len(result.DeepFindings))
			renderDeepFindings(result.DeepFindings)
		}

		fmt.Fprintf(os.Stderr, "  %s %s files reviewed\n\n",
			cliDim.Render("[stats]"),
			cliInfo.Render(fmt.Sprintf("%d", result.FilesReviewed)))

		// Coverage hint — one line summary of what got reviewed vs
		// skipped, when available. Full per-file breakdown lives in
		// the JSON export.
		if result.StructuredReview != nil && result.StructuredReview.Coverage != nil {
			cov := result.StructuredReview.Coverage
			hint := fmt.Sprintf("Coverage: %d/%d files reviewed",
				cov.FilesReviewed, cov.FilesInScope)
			if n := len(cov.OrphanFiles); n > 0 {
				hint += fmt.Sprintf(", %d orphans (see --output for detail)", n)
			}
			fmt.Fprintf(os.Stderr, "  %s %s\n\n",
				cliDim.Render("[coverage]"),
				cliInfo.Render(hint))
		}
	}

	// Export if requested
	if outputPath != "" {
		if err := exportReviewResult(result, outputPath); err != nil {
			printError(fmt.Errorf("exporting report: %w", err))
			os.Exit(1)
		}
		if !quiet {
			fmt.Fprintf(os.Stderr, "  Report saved to %s\n\n", cliInfo.Render(outputPath))
		}
	}
}

// exportReviewResult writes the review result to a JSON file.
func exportReviewResult(result *review.PRReviewResult, path string) error {
	data := map[string]any{
		"pr_number":      result.PR.Number,
		"pr_title":       result.PR.Title,
		"files_reviewed": result.FilesReviewed,
	}
	if result.StructuredReview != nil {
		data["review"] = result.StructuredReview
	}
	if len(result.DeepFindings) > 0 {
		data["deep_findings"] = result.DeepFindings
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func printReviewUsage() {
	logo := lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))

	fmt.Fprintf(os.Stderr, "\n  %s %s  %s\n\n",
		logo.Render("prr review"),
		dim.Render(version),
		dim.Render("— headless PR review"))
	fmt.Fprintf(os.Stderr, "  %s  prr review <number> [flags]\n\n",
		dim.Render("Usage:"))
	fmt.Fprintf(os.Stderr, "  %s\n", dim.Render("Flags:"))
	fmt.Fprintf(os.Stderr, "    --output=<path>      Export report to JSON file\n")
	fmt.Fprintf(os.Stderr, "    --no-cache           Ignore cached results\n")
	fmt.Fprintf(os.Stderr, "    --no-synthesis       Skip synthesis phase\n")
	fmt.Fprintf(os.Stderr, "    --quiet, -q          Suppress terminal output (use with --output)\n")
	fmt.Fprintf(os.Stderr, "    --debug              Print LLM tool calls, user messages, and responses\n")
	fmt.Fprintf(os.Stderr, "                         (compact by default; PRR_DEBUG_VERBOSE=1 for full prompts)\n")
	fmt.Fprintf(os.Stderr, "    --bug-priors         Inject recent fix-shaped commits as known-failure priors\n\n")
}

func createAIClient(cfg *config.Config) ai.Client {
	ref, err := config.ParseModelRef(cfg.StrongModel)
	if err != nil {
		printError(fmt.Errorf("invalid strong_model: %w", err))
		os.Exit(1)
	}

	apiKey := cfg.APIKeyFor(ref.Provider)
	// Keyless providers (e.g. claude-code) authenticate via their own
	// CLI; prr does not need an API key for them. The factory will
	// detect availability and surface an error if the CLI isn't on PATH.
	if !config.IsKeylessProvider(ref.Provider) && (apiKey == "" || apiKey == "YOUR_API_KEY") {
		printError(fmt.Errorf("no API key configured for provider %q (used by strong_model %q)", ref.Provider, cfg.StrongModel))
		os.Exit(1)
	}

	toolExec := &ai.ToolExecutor{
		HeadRef: "HEAD",
	}

	// Load per-model tuning (maxOutputTokens, temperature, thinkingBudget)
	models, err := config.LoadModels()
	if err != nil {
		log.Printf("Warning: failed to load models config: %v", err)
	}
	modelCfg := config.GetModelConfig(models, ref.ModelID)

	pc := cfg.ProviderConfigFor(ref.Provider)

	provider, err := ai.NewProvider(ai.ProviderConfig{
		ProviderName:    ref.Provider,
		ModelID:         ref.ModelID,
		APIKey:          apiKey,
		BaseURL:         pc.BaseURL,
		MaxOutputTokens: modelCfg.MaxOutputTokens,
		Temperature:     modelCfg.Temperature,
		ThinkingBudget:  modelCfg.ThinkingBudget.Review,
	})
	if err != nil {
		log.Fatalf("Failed to create AI provider: %v", err)
	}

	var opts []ai.AgentOption
	if cfg.Debug {
		opts = append(opts, ai.WithDebugLogger(log.Writer()))
	}

	return ai.NewAgent(provider, toolExec, opts...)
}

// createAOIClient creates a lightweight AI client for the security AOI pre-scan.
// It uses the fast model with no tools — only diff analysis.
func createAOIClient(cfg *config.Config) (ai.Client, error) {
	fastModel := cfg.FastModel
	if envModel := os.Getenv("PRR_AOI_MODEL"); envModel != "" {
		// Legacy env var support: bare model ID → try to keep the configured fast model's provider
		if !strings.Contains(envModel, "/") {
			ref, err := config.ParseModelRef(cfg.FastModel)
			if err == nil {
				fastModel = ref.Provider + "/" + envModel
			} else {
				fastModel = "gemini/" + envModel
			}
		} else {
			fastModel = envModel
		}
	}

	ref, err := config.ParseModelRef(fastModel)
	if err != nil {
		return nil, fmt.Errorf("invalid fast_model: %w", err)
	}

	// API key: PRR_AOI_API_KEY env > providers[provider].api_key
	apiKey := cfg.APIKeyFor(ref.Provider)
	if envKey := os.Getenv("PRR_AOI_API_KEY"); envKey != "" {
		apiKey = envKey
	}

	// Keyless providers (e.g. claude-code) authenticate via their own
	// CLI; no key required here.
	if !config.IsKeylessProvider(ref.Provider) && (apiKey == "" || apiKey == "YOUR_API_KEY") {
		return nil, fmt.Errorf("no API key configured for provider %q (used by fast_model %q). Set PRR_AOI_API_KEY or configure providers.%s.api_key", ref.Provider, fastModel, ref.Provider)
	}

	// Load per-model tuning
	models, err := config.LoadModels()
	if err != nil {
		log.Printf("Warning: failed to load models config for AOI: %v", err)
	}
	modelCfg := config.GetModelConfig(models, ref.ModelID)

	pc := cfg.ProviderConfigFor(ref.Provider)

	provider, err := ai.NewProvider(ai.ProviderConfig{
		ProviderName:    ref.Provider,
		ModelID:         ref.ModelID,
		APIKey:          apiKey,
		BaseURL:         pc.BaseURL,
		MaxOutputTokens: modelCfg.MaxOutputTokens,
		Temperature:     modelCfg.Temperature,
		ThinkingBudget:  modelCfg.ThinkingBudget.Fast,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create AOI provider: %w", err)
	}

	// AOI client has no tool executor — it only analyzes the diffs passed to it
	return ai.NewAgent(provider, nil), nil
}

// ── Pre-flight checks ──────────────────────────────────────────────────

func preflight(prNumber string, useChroma bool) error {
	// 1. Check we're inside a git repository
	if err := runSilent("git", "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("not a git repository\n  Run prr from inside a git repo that has the PR you want to review")
	}

	// 2. Check gh CLI is installed
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh (GitHub CLI) is not installed\n  Install it: https://cli.github.com")
	}

	// 3. Check gh is authenticated
	if err := runSilent("gh", "auth", "status"); err != nil {
		return fmt.Errorf("gh is not authenticated\n  Run: gh auth login")
	}

	// 4. Check delta is installed (skip when using chroma renderer)
	if !useChroma {
		if _, err := exec.LookPath("delta"); err != nil {
			if runtime.GOOS == "linux" {
				if err := offerInstallDelta(); err != nil {
					return err
				}
			} else {
				hint := "Install it:\n"
				switch runtime.GOOS {
				case "darwin":
					hint += "  brew install git-delta"
				default:
					hint += "  See https://github.com/dandavison/delta#installation"
				}
				return fmt.Errorf("delta is not installed\n  %s", hint)
			}
		}
	}

	// 5. Ensure SSH host keys are trusted (prevents interactive prompt during fetch)
	ensureSSHHostKeys()

	// 6. Validate PR number (skip if empty — TUI picker will handle selection)
	if prNumber != "" {
		prNumber = strings.TrimSpace(prNumber)
		if prNumber == "" {
			return fmt.Errorf("PR number cannot be empty")
		}
	}

	return nil
}

// offerInstallDelta prompts the user to install delta on Linux.
func offerInstallDelta() error {
	info := lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))

	fmt.Fprintf(os.Stderr, "\n  %s delta (git-delta) is required but not installed.\n",
		info.Render("note:"))

	// Detect package manager and pick install command
	type installer struct {
		check string
		name  string
		args  []string
	}
	installers := []installer{
		{"dnf", "dnf", []string{"install", "-y", "git-delta"}},
		{"pacman", "pacman", []string{"-S", "--noconfirm", "git-delta"}},
	}

	var found *installer

	// On Debian/Ubuntu, delta isn't in default repos — install .deb from GitHub
	if _, err := exec.LookPath("apt-get"); err == nil {
		return installDeltaDeb()
	}

	for i := range installers {
		if _, err := exec.LookPath(installers[i].check); err == nil {
			found = &installers[i]
			break
		}
	}

	if found == nil {
		return fmt.Errorf("delta is not installed\n  Could not detect a supported package manager (apt, dnf, pacman)\n  Install manually: https://github.com/dandavison/delta#installation")
	}

	cmdStr := found.name + " " + strings.Join(found.args, " ")
	fmt.Fprintf(os.Stderr, "  Install now using %s?\n\n", dim.Render(cmdStr))
	fmt.Fprintf(os.Stderr, "  %s ", info.Render("[y/N]"))

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer != "y" && answer != "yes" {
		return fmt.Errorf("delta is required to run prr\n  Install it manually: https://github.com/dandavison/delta#installation")
	}

	fmt.Fprintf(os.Stderr, "\n  Installing delta...\n\n")

	// Run with sudo if not root
	var cmd *exec.Cmd
	if os.Getuid() == 0 {
		cmd = exec.Command(found.name, found.args...)
	} else {
		args := append([]string{found.name}, found.args...)
		cmd = exec.Command("sudo", args...)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install delta: %v\n  Install it manually: https://github.com/dandavison/delta#installation", err)
	}

	// Verify it worked
	if _, err := exec.LookPath("delta"); err != nil {
		return fmt.Errorf("delta was installed but not found in PATH\n  Try restarting your shell")
	}

	fmt.Fprintf(os.Stderr, "\n  %s\n\n", info.Render("delta installed successfully"))
	return nil
}

// installDeltaDeb downloads and installs the latest delta .deb from GitHub releases.
func installDeltaDeb() error {
	info := lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))

	// Map Go arch to delta release arch
	arch := runtime.GOARCH
	var debArch string
	switch arch {
	case "amd64":
		debArch = "amd64"
	case "arm64":
		debArch = "arm64"
	default:
		return fmt.Errorf("delta is not installed\n  No pre-built .deb for %s\n  Install via cargo: cargo install git-delta", arch)
	}

	// Use gh to find the latest release asset URL
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("delta is not installed\n  Install manually: sudo dpkg -i <delta.deb>\n  Download from https://github.com/dandavison/delta/releases")
	}

	fmt.Fprintf(os.Stderr, "  Will download the latest .deb from GitHub and install with dpkg.\n\n")
	fmt.Fprintf(os.Stderr, "  %s ", info.Render("[y/N]"))

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("delta is required to run prr\n  Install it manually: https://github.com/dandavison/delta#installation")
	}

	fmt.Fprintf(os.Stderr, "\n  Fetching latest release...\n")

	// Create a secure temporary directory for the download to prevent
	// symlink/race attacks in the shared /tmp directory.
	tmpDir, err := os.MkdirTemp("", "prr-delta-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Get download URL for the matching .deb asset
	pattern := fmt.Sprintf("git-delta_*_%s.deb", debArch)
	cmd := exec.Command("gh", "release", "download", "--repo", "dandavison/delta",
		"--pattern", pattern, "--dir", tmpDir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to download delta .deb: %v\n  Install manually: https://github.com/dandavison/delta/releases", err)
	}

	// Find the downloaded file in our isolated temp directory
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("failed to read temp dir: %v", err)
	}
	var debFile string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "git-delta_") && strings.HasSuffix(e.Name(), "_"+debArch+".deb") {
			debFile = filepath.Join(tmpDir, e.Name())
			break
		}
	}
	if debFile == "" {
		return fmt.Errorf("downloaded .deb not found in %s", tmpDir)
	}

	fmt.Fprintf(os.Stderr, "  Installing %s...\n\n", dim.Render(filepath.Base(debFile)))

	// Install with dpkg
	var dpkg *exec.Cmd
	if os.Getuid() == 0 {
		dpkg = exec.Command("dpkg", "-i", debFile)
	} else {
		dpkg = exec.Command("sudo", "dpkg", "-i", debFile)
	}
	dpkg.Stdout = os.Stderr
	dpkg.Stderr = os.Stderr
	dpkg.Stdin = os.Stdin

	if err := dpkg.Run(); err != nil {
		return fmt.Errorf("failed to install delta: %v\n  Try manually: sudo dpkg -i %s", err, debFile)
	}

	if _, err := exec.LookPath("delta"); err != nil {
		return fmt.Errorf("delta was installed but not found in PATH\n  Try restarting your shell")
	}

	fmt.Fprintf(os.Stderr, "\n  %s\n\n", info.Render("delta installed successfully"))
	return nil
}

// ensureSSHHostKeys checks if the git remote uses SSH and ensures the host key
// is in known_hosts. This prevents an interactive "authenticity of host" prompt
// from breaking the TUI during git fetch.
func ensureSSHHostKeys() {
	// Get the origin remote URL
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return
	}
	url := strings.TrimSpace(string(out))

	// Only relevant for SSH remotes (git@host:... or ssh://...)
	var host string
	if strings.HasPrefix(url, "git@") {
		// git@github.com:user/repo.git
		parts := strings.SplitN(url, ":", 2)
		host = strings.TrimPrefix(parts[0], "git@")
	} else if after, ok := strings.CutPrefix(url, "ssh://"); ok {
		// ssh://git@github.com/user/repo.git
		trimmed := after
		if at := strings.Index(trimmed, "@"); at >= 0 {
			trimmed = trimmed[at+1:]
		}
		host = strings.SplitN(trimmed, "/", 2)[0]
		// Strip port if present
		if colon := strings.Index(host, ":"); colon >= 0 {
			host = host[:colon]
		}
	} else {
		return // HTTPS remote, no SSH host key needed
	}

	if host == "" {
		return
	}

	// Check if host is already in known_hosts
	if runSilent("ssh-keygen", "-F", host) == nil {
		return // Already trusted
	}

	// Scan the host key first so we can show it
	info := lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	warn := lipgloss.NewStyle().Foreground(lipgloss.Color("#FAB387"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))

	scanCmd := exec.Command("ssh-keyscan", "-t", "ed25519,rsa", host)
	keys, err := scanCmd.Output()
	if err != nil || len(keys) == 0 {
		fmt.Fprintf(os.Stderr, "  %s Could not scan SSH host key for %s, git fetch may prompt\n",
			warn.Render("warn:"), host)
		return
	}

	// Show the key fingerprints and ask the user
	fmt.Fprintf(os.Stderr, "\n  %s SSH host key for %s is not in known_hosts.\n\n",
		warn.Render("note:"), host)

	// Compute and display fingerprints
	for line := range strings.SplitSeq(strings.TrimSpace(string(keys)), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fpCmd := exec.Command("ssh-keygen", "-lf", "-")
		fpCmd.Stdin = strings.NewReader(line + "\n")
		fp, err := fpCmd.Output()
		if err == nil {
			fmt.Fprintf(os.Stderr, "  %s\n", dim.Render(strings.TrimSpace(string(fp))))
		}
	}

	fmt.Fprintf(os.Stderr, "\n  Add to ~/.ssh/known_hosts? %s ", info.Render("[y/N]"))

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer != "y" && answer != "yes" {
		fmt.Fprintf(os.Stderr, "\n  %s Skipped. git fetch may fail with a host key prompt.\n\n",
			warn.Render("warn:"))
		return
	}

	// Ensure ~/.ssh exists. Each failure is surfaced to stderr (not
	// just log.Printf) so the user can see WHY the host was not
	// added — otherwise they retry git, get the same prompt, and
	// have no idea the previous attempt failed on permissions or a
	// read-only home.
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s could not resolve home dir: %v\n", warn.Render("err:"), err)
		return
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "  %s could not create %s: %v\n", warn.Render("err:"), sshDir, err)
		return
	}

	knownHosts := filepath.Join(sshDir, "known_hosts")
	f, err := os.OpenFile(knownHosts, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s could not open %s: %v\n", warn.Render("err:"), knownHosts, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(keys); err != nil {
		fmt.Fprintf(os.Stderr, "  %s could not write %s: %v\n", warn.Render("err:"), knownHosts, err)
		return
	}

	fmt.Fprintf(os.Stderr, "  %s %s added to %s\n\n", info.Render("done:"), host, knownHosts)
}

func runSilent(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// ── Styled CLI output ──────────────────────────────────────────────────

// CLI output styles (shared between audit and review commands).
var (
	cliHeader = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	cliInfo   = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))
	cliDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))
	sevStyles = map[string]lipgloss.Style{
		"critical": lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8")).Bold(true),
		"high":     lipgloss.NewStyle().Foreground(lipgloss.Color("#FAB387")).Bold(true),
		"medium":   lipgloss.NewStyle().Foreground(lipgloss.Color("#F9E2AF")),
		"low":      lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1")),
	}
)

// sevStyle returns the lipgloss style for a severity level.
func sevStyle(severity string) lipgloss.Style {
	if s, ok := sevStyles[severity]; ok {
		return s
	}
	return sevStyles["low"]
}

// renderDeepFindings prints DeepFinding entries to stderr.
func renderDeepFindings(findings []state.DeepFinding) {
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "  %s %s\n",
			sevStyle(f.Severity).Render("["+f.Severity+"]"),
			cliInfo.Render(f.Title))
		fmt.Fprintf(os.Stderr, "    %s:%s (%s/%s)\n",
			f.File, f.Lines, f.Category, f.Subcategory)
		if f.Description != "" {
			fmt.Fprintf(os.Stderr, "    %s\n", cliDim.Render(f.Description))
		}
		if !f.Trigger.IsZero() {
			if f.Trigger.Repro != "" {
				fmt.Fprintf(os.Stderr, "    Trigger: %s\n", cliDim.Render(f.Trigger.Repro))
			}
			if f.Trigger.Observable != "" {
				fmt.Fprintf(os.Stderr, "    Observable: %s\n", cliDim.Render(f.Trigger.Observable))
			}
		}
		if f.Suggestion != "" {
			fmt.Fprintf(os.Stderr, "    Fix: %s\n", cliDim.Render(f.Suggestion))
		}
		fmt.Fprintln(os.Stderr)
	}
}

// loadConfigAndLogger loads the config, sets debug mode, and initializes the logger.
func loadConfigAndLogger(debug bool) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	cfg.Debug = debug

	if err := initLogger(); err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}
	return cfg, nil
}

// resolveAOIModelName determines the AOI model name for display/profile lookup.
// Returns "disabled" if aoiClient is nil.
func resolveAOIModelName(aoiClient ai.Client, cfg *config.Config) string {
	if aoiClient == nil {
		return "disabled"
	}
	if mi, ok := aoiClient.(ai.ModelInfo); ok {
		return mi.ModelName()
	}
	if ref, err := config.ParseModelRef(cfg.FastModel); err == nil {
		return ref.ModelID
	}
	return "disabled"
}

// resolveAOIContextLines returns the AOI context lines for the given model ID
// from the model config. Falls back to 3.
func resolveAOIContextLines(modelID string) int {
	if modelID == "disabled" {
		return 3
	}
	models, err := config.LoadModels()
	if err != nil {
		return 3
	}
	return config.GetModelConfig(models, modelID).ResolvedAOIContextLines()
}

func printUsage() {
	logo := lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))

	fmt.Fprintf(os.Stderr, "\n  %s %s  %s\n\n",
		logo.Render("prr"),
		dim.Render(version),
		dim.Render("— review PRs in your terminal"))
	fmt.Fprintf(os.Stderr, "  %s  prr [pr_number]\n",
		dim.Render("Usage:"))
	fmt.Fprintf(os.Stderr, "  %s  prr review <number> [flags]\n",
		dim.Render("      "))
	fmt.Fprintf(os.Stderr, "  %s  prr audit [flags]\n\n",
		dim.Render("      "))
	fmt.Fprintf(os.Stderr, "  %s  prr 42            Review PR #42 (interactive TUI)\n",
		dim.Render(""))
	fmt.Fprintf(os.Stderr, "  %s  prr               Pick from open PRs\n",
		dim.Render(""))
	fmt.Fprintf(os.Stderr, "  %s  prr review 42     Headless PR review (prints to terminal)\n",
		dim.Render(""))
	fmt.Fprintf(os.Stderr, "  %s  prr audit         Full-project code audit\n",
		dim.Render(""))
	fmt.Fprintf(os.Stderr, "  %s  prr --chroma      Use experimental chroma renderer (no delta needed)\n\n",
		dim.Render(""))
}

func printError(err error) {
	errLabel := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#F38BA8")).
		Bold(true)
	msg := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#CDD6F4"))
	hint := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6C7086"))

	lines := strings.Split(err.Error(), "\n")
	fmt.Fprintf(os.Stderr, "\n  %s %s\n",
		errLabel.Render("error:"),
		msg.Render(lines[0]))
	for _, line := range lines[1:] {
		fmt.Fprintf(os.Stderr, "         %s\n", hint.Render(line))
	}
	fmt.Fprintln(os.Stderr)
}

// ── Logger ─────────────────────────────────────────────────────────────

func initLogger() error {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	logDir := filepath.Join(cacheDir, "prr")

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}

	logFile := filepath.Join(logDir, "debug.log")
	// NOTE: We intentionally don't close the log file; the OS reclaims it on exit.
	// tea.LogToFile sets log output globally, so it must stay open for the process lifetime.
	_, err = tea.LogToFile(logFile, "debug")
	return err
}

// ── prr config ─────────────────────────────────────────────────────────

func runConfig() {
	logo := lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))
	info := lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))
	success := lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))

	fmt.Fprintf(os.Stderr, "\n  %s %s\n\n",
		logo.Render("prr config"),
		dim.Render("— configure providers and models"))

	// Load existing config (or create default)
	cfg, err := config.LoadRaw()
	if err != nil {
		printError(err)
		os.Exit(1)
	}

	// Show current state
	fmt.Fprintf(os.Stderr, "  %s %s\n", info.Render("Strong model:"), logo.Render(cfg.StrongModel))
	fmt.Fprintf(os.Stderr, "  %s %s\n", info.Render("Fast model:  "), logo.Render(cfg.FastModel))
	fmt.Fprintf(os.Stderr, "  %s\n", dim.Render("Configured providers:"))
	if len(cfg.Providers) == 0 {
		fmt.Fprintf(os.Stderr, "    %s\n", dim.Render("(none)"))
	} else {
		for name, pc := range cfg.Providers {
			masked := maskKey(pc.APIKey)
			fmt.Fprintf(os.Stderr, "    %s %s\n", info.Render(name), dim.Render(masked))
		}
	}
	fmt.Fprintf(os.Stderr, "\n")

	// Ask what to do
	var action string
	actionForm := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("What would you like to do?").
				Options(
					huh.NewOption("Change strong model (deep review)", "strong"),
					huh.NewOption("Change fast model (discovery/AOI)", "fast"),
					huh.NewOption("Add or update a provider API key", "add"),
				).
				Value(&action),
		),
	).WithTheme(huh.ThemeCatppuccin())

	if err := actionForm.Run(); err != nil {
		return
	}

	var ok bool
	switch action {
	case "strong":
		ok = runConfigModel(cfg, "strong")
	case "fast":
		ok = runConfigModel(cfg, "fast")
	case "add":
		ok = runConfigAdd(cfg)
	}

	if !ok {
		return
	}

	// Save
	if err := config.Save(cfg); err != nil {
		printError(err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "\n  %s\n\n", success.Render("✓ Config saved"))
}

func runConfigAdd(cfg *config.Config) bool {
	providers := config.KnownProviders()
	opts := make([]huh.Option[string], 0, len(providers))
	for _, p := range providers {
		// Hide keyless providers whose detector reports them as
		// unavailable (e.g. claude-code without the CLI on PATH).
		if config.IsKeylessProvider(p) {
			detect, ok := config.KeylessProviderAvailable[p]
			if !ok || detect == nil || !detect() {
				continue
			}
			opts = append(opts, huh.NewOption(p+" (detected, no key needed)", p))
			continue
		}
		opts = append(opts, huh.NewOption(p, p))
	}

	var selected string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Which provider?").
				Options(opts...).
				Value(&selected),
		),
	).WithTheme(huh.ThemeCatppuccin())

	if err := form.Run(); err != nil {
		return false
	}

	return runConfigAddForProvider(cfg, selected)
}

func runConfigAddForProvider(cfg *config.Config, provider string) bool {
	// GitHub Copilot uses OAuth device flow instead of API key input
	if provider == "github-copilot" {
		return runCopilotOAuth(cfg)
	}

	// Claude Code authenticates via its own CLI (subscription / OAuth /
	// ANTHROPIC_API_KEY). We just confirm the binary is available; no
	// key needs to be stored in prr's config. We do write a small
	// marker entry so the user's selection is visible in config.json
	// (otherwise the wizard appears to save nothing for this provider).
	if provider == "claude-code" {
		detect, ok := config.KeylessProviderAvailable[provider]
		if !ok || detect == nil || !detect() {
			printError(fmt.Errorf("claude CLI not found on PATH — install Claude Code first (https://docs.claude.com/claude-code)"))
			return false
		}
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]config.ProviderConfig)
		}
		cfg.Providers["claude-code"] = config.ProviderConfig{UseCLI: true}
		fmt.Fprintf(os.Stderr, "  ✓ claude detected — using its own auth (subscription / OAuth / ANTHROPIC_API_KEY)\n")
		return true
	}

	var apiKey string
	hint := ""
	switch provider {
	case "gemini":
		hint = "Get your key at https://aistudio.google.com/apikey"
	case "openai":
		hint = "Get your key at https://platform.openai.com/api-keys"
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("API Key for " + provider).
				Description(hint).
				Value(&apiKey).
				EchoMode(huh.EchoModePassword),
		),
	).WithTheme(huh.ThemeCatppuccin())

	if err := form.Run(); err != nil || apiKey == "" {
		return false
	}

	// Validate the key by making a small API request
	fmt.Fprintf(os.Stderr, "  Validating API key...")
	if err := validateAPIKey(provider, apiKey); err != nil {
		fmt.Fprintf(os.Stderr, " ✗\n")
		printError(fmt.Errorf("API key validation failed: %w", err))
		return false
	}
	fmt.Fprintf(os.Stderr, " ✓\n")

	if cfg.Providers == nil {
		cfg.Providers = make(map[string]config.ProviderConfig)
	}
	cfg.Providers[provider] = config.ProviderConfig{
		APIKey: apiKey,
	}
	return true
}

func runCopilotOAuth(cfg *config.Config) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fmt.Fprintf(os.Stderr, "\n  Starting GitHub Copilot login...\n")

	auth, err := ai.CopilotRequestDeviceCode(ctx)
	if err != nil {
		printError(fmt.Errorf("failed to start Copilot login: %w", err))
		return false
	}

	fmt.Fprintf(os.Stderr, "\n  1. Open:  %s\n", auth.VerificationURI)
	fmt.Fprintf(os.Stderr, "  2. Enter: %s\n\n", auth.UserCode)
	fmt.Fprintf(os.Stderr, "  Waiting for authorization...")

	token, err := ai.CopilotPollForToken(ctx, auth.DeviceCode, auth.Interval)
	if err != nil {
		fmt.Fprintf(os.Stderr, " ✗\n")
		printError(fmt.Errorf("Copilot login failed: %w", err))
		return false
	}

	fmt.Fprintf(os.Stderr, " ✓\n")

	// Validate the token
	fmt.Fprintf(os.Stderr, "  Validating token...")
	if err := ai.CopilotValidateToken(ctx, token); err != nil {
		fmt.Fprintf(os.Stderr, " ✗\n")
		printError(fmt.Errorf("token validation failed: %w", err))
		return false
	}
	fmt.Fprintf(os.Stderr, " ✓\n")

	if cfg.Providers == nil {
		cfg.Providers = make(map[string]config.ProviderConfig)
	}
	cfg.Providers["github-copilot"] = config.ProviderConfig{
		APIKey: token,
	}
	return true
}

func runConfigModel(cfg *config.Config, slot string) bool {
	providers := cfg.ConfiguredProviders()
	var models []config.KnownModel
	var title string
	switch slot {
	case "strong":
		models = config.ReviewModels(providers...)
		title = "Select strong model (deep review, re-review, synthesis)"
	case "fast":
		models = config.AOIModels(providers...)
		title = "Select fast model (discovery, AOI pre-scan)"
	}

	if len(models) == 0 {
		fmt.Fprintf(os.Stderr, "  No models available\n")
		return false
	}

	// Group by provider for display
	opts := make([]huh.Option[string], len(models))
	for i, m := range models {
		ref := m.Provider + "/" + m.ID
		label := fmt.Sprintf("%-16s %-24s %s  %s",
			"["+m.Provider+"]", m.Label, m.SpeedIcon(), m.PriceTag())
		opts[i] = huh.NewOption(label, ref)
	}

	// Determine current selection for default
	var currentRef string
	switch slot {
	case "strong":
		currentRef = cfg.StrongModel
	case "fast":
		currentRef = cfg.FastModel
	}

	var selected string
	selected = currentRef

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title(title).
				Options(opts...).
				Value(&selected),
		),
	).WithTheme(huh.ThemeCatppuccin())

	if err := form.Run(); err != nil {
		return false
	}

	// Check the provider is usable. Keyless providers (claude-code)
	// are usable as long as their CLI is detected; no API key required.
	ref, err := config.ParseModelRef(selected)
	if err != nil {
		printError(err)
		return false
	}
	if cfg.APIKeyFor(ref.Provider) == "" && !config.IsKeylessProvider(ref.Provider) {
		fmt.Fprintf(os.Stderr, "  Provider %q needs an API key first.\n", ref.Provider)
		if !runConfigAddForProvider(cfg, ref.Provider) {
			return false
		}
	}

	switch slot {
	case "strong":
		cfg.StrongModel = selected
	case "fast":
		cfg.FastModel = selected
	}
	return true
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// validateAPIKey makes a lightweight API call to verify the key is valid.
// Uses the "list models" endpoint which requires auth but no inference.
func validateAPIKey(provider, apiKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var url string
	var authHeader string
	var authValue string

	switch provider {
	case "gemini":
		url = "https://generativelanguage.googleapis.com/v1beta/models"
		authHeader = "x-goog-api-key"
		authValue = apiKey
	case "openai":
		url = "https://api.openai.com/v1/models"
		authHeader = "Authorization"
		authValue = "Bearer " + apiKey
	case "github-copilot":
		// Copilot validation is handled in runCopilotOAuth, not here.
		return nil
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set(authHeader, authValue)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	// Surface only the status code in the user-visible error. Raw
	// provider response bodies sometimes echo back the rejected API
	// key, account metadata, or internal IDs — none of which we want
	// printed to stderr. The body is drained into a debug log only.
	_, _ = io.Copy(io.Discard, resp.Body)
	return fmt.Errorf("HTTP %d (response body suppressed; check provider dashboard for details)", resp.StatusCode)
}

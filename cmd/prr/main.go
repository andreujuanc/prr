package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/audit"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/review"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
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
		if !seenSubcommand && (arg == "audit" || arg == "review") {
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
		prNumber = positional[0]
	}

	// Pre-flight checks before launching the TUI
	if err := preflight(prNumber, useChroma); err != nil {
		printError(err)
		os.Exit(1)
	}

	// Load config for AI
	cfg, err := config.Load()
	if err != nil {
		printError(err)
		os.Exit(1)
	}
	cfg.Debug = debug

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

	// Initialize hidden debug logger
	if err := initLogger(); err != nil {
		printError(fmt.Errorf("failed to initialize logger: %w", err))
		os.Exit(1)
	}
	prLabel := prNumber
	if prLabel == "" {
		prLabel = "(picker)"
	}
	aoiModelName := "disabled"
	if aoiClient != nil {
		aoiModelName = os.Getenv("PRR_AOI_MODEL")
		if aoiModelName == "" && cfg.AOIModel != "" {
			aoiModelName = cfg.AOIModel
		}
		if aoiModelName == "" {
			aoiModelName = "gemini-2.5-flash-lite" // default
		}
	}
	aoiProfile := security.GetAOIProfile(aoiModelName)
	log.Printf("Starting PR review TUI for PR #%s (provider: %s, model: %s, aoi: %s, aoi_context: %d)", prLabel, cfg.Provider, cfg.Model, aoiModelName, aoiProfile.ContextLines)

	model := ui.NewModel(prNumber, aiClient, aoiClient, cfg.ParallelReviews, aoiProfile.ContextLines, useChroma, cfg.Provider)
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
		if strings.HasPrefix(arg, "--focus=") {
			focusStr = strings.TrimPrefix(arg, "--focus=")
		} else if strings.HasPrefix(arg, "--exclude=") {
			excludeStr = strings.TrimPrefix(arg, "--exclude=")
		} else if strings.HasPrefix(arg, "--include=") {
			includeStr = strings.TrimPrefix(arg, "--include=")
		} else if strings.HasPrefix(arg, "--max-reviews=") {
			fmt.Sscanf(strings.TrimPrefix(arg, "--max-reviews="), "%d", &opts.MaxReviews)
		} else if strings.HasPrefix(arg, "--output=") {
			outputPath = strings.TrimPrefix(arg, "--output=")
		} else if arg == "--no-cache" {
			opts.NoCache = true
		} else if arg == "--no-synthesis" {
			noSynth = true
		} else if arg == "--quiet" || arg == "-q" {
			quiet = true
		} else if arg == "--debug" {
			opts.Debug = true
		} else if strings.HasPrefix(arg, "--file=") {
			opts.DebugFile = strings.TrimPrefix(arg, "--file=")
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
	cfg, err := config.Load()
	if err != nil {
		printError(err)
		os.Exit(1)
	}
	cfg.Debug = debug

	// Initialize logger
	if err := initLogger(); err != nil {
		printError(fmt.Errorf("failed to initialize logger: %w", err))
		os.Exit(1)
	}

	// ── Interactive model selection ─────────────────────────────────────
	// Allow env vars to skip the interactive prompt
	envReview := os.Getenv("PRR_REVIEW_MODEL")
	envAOI := os.Getenv("PRR_AOI_MODEL")
	var selection *audit.ModelSelection
	if envReview != "" && envAOI != "" {
		selection = &audit.ModelSelection{
			ReviewModel: envReview,
			AOIModel:    envAOI,
		}
	} else {
		var selErr error
		selection, selErr = audit.PromptModels(cfg)
		if selErr != nil {
			printError(selErr)
			os.Exit(1)
		}
	}

	// Apply selections to config for client creation
	cfg.Model = selection.ReviewModel
	cfg.AOIModel = selection.AOIModel

	// Validate models are known and match the configured provider
	if rm, ok := config.GetKnownModel(selection.ReviewModel); !ok {
		printError(fmt.Errorf("unknown review model %q — run without PRR_REVIEW_MODEL to see available models", selection.ReviewModel))
		os.Exit(1)
	} else if rm.Provider != cfg.Provider {
		printError(fmt.Errorf("review model %q belongs to provider %q but configured provider is %q", selection.ReviewModel, rm.Provider, cfg.Provider))
		os.Exit(1)
	}
	if am, ok := config.GetKnownModel(selection.AOIModel); !ok {
		printError(fmt.Errorf("unknown AOI model %q — run without PRR_AOI_MODEL to see available models", selection.AOIModel))
		os.Exit(1)
	} else if am.Provider != cfg.Provider {
		printError(fmt.Errorf("AOI model %q belongs to provider %q but configured provider is %q", selection.AOIModel, am.Provider, cfg.Provider))
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
	aoiProfile := security.GetAOIProfile(selection.AOIModel)
	opts.AOIContextLines = aoiProfile.ContextLines

	log.Printf("Starting audit (provider: %s, model: %s, aoi: %s, focus: %v)",
		cfg.Provider, selection.ReviewModel, selection.AOIModel, opts.Focus)

	// Styled output helpers
	header := lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	info := lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))
	sevCritical := lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8")).Bold(true)
	sevHigh := lipgloss.NewStyle().Foreground(lipgloss.Color("#FAB387")).Bold(true)
	sevMedium := lipgloss.NewStyle().Foreground(lipgloss.Color("#F9E2AF"))
	sevLow := lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))

	// Load previous findings for comparison
	previousFindings, _ := audit.LoadSnapshot(repoRoot)

	// Run audit with progress UI (or plain mode in debug)
	ctx := context.Background()
	var result *audit.Result
	var synthesis *audit.SynthesisResult
	if opts.Debug {
		// In debug mode, skip Bubble Tea UI — run directly with simple progress to stderr
		result, synthesis, err = audit.RunPlain(ctx, reviewClient, aoiClient, opts,
			selection.ReviewModel, selection.AOIModel, noSynth)
	} else {
		result, synthesis, err = audit.RunWithUI(ctx, reviewClient, aoiClient, opts,
			selection.ReviewModel, selection.AOIModel, noSynth)
	}
	if err != nil {
		printError(err)
		os.Exit(1)
	}

	if !quiet {
		if len(result.Findings) == 0 {
			fmt.Fprintf(os.Stderr, "  %s\n\n", info.Render("No issues found."))
		} else {
			// Print findings sorted by severity
			for _, f := range result.Findings {
				var sevStyle lipgloss.Style
				switch f.Severity {
				case "critical":
					sevStyle = sevCritical
				case "high":
					sevStyle = sevHigh
				case "medium":
					sevStyle = sevMedium
				default:
					sevStyle = sevLow
				}

				fmt.Fprintf(os.Stderr, "  %s %s\n",
					sevStyle.Render("["+f.Severity+"]"),
					info.Render(f.Title))
				fmt.Fprintf(os.Stderr, "    %s:%s (%s/%s)\n",
					f.File, f.Lines, f.Category, f.Subcategory)
				if f.Description != "" {
					fmt.Fprintf(os.Stderr, "    %s\n", dimStyle.Render(f.Description))
				}
				if f.Trigger != "" {
					fmt.Fprintf(os.Stderr, "    Trigger: %s\n", dimStyle.Render(f.Trigger))
				}
				if f.Suggestion != "" {
					fmt.Fprintf(os.Stderr, "    Fix: %s\n", dimStyle.Render(f.Suggestion))
				}
				fmt.Fprintln(os.Stderr)
			}

			// Visual charts
			fmt.Fprint(os.Stderr, audit.RenderSeverityBar(result.Findings))
			fmt.Fprint(os.Stderr, audit.RenderCategoryChart(result.Findings))
		}

		// Print synthesis
		if synthesis != nil && synthesis.ExecutiveSummary != "" {
			fmt.Fprintf(os.Stderr, "  %s\n\n", header.Render("Executive Summary"))
			fmt.Fprintf(os.Stderr, "  %s\n\n", info.Render(synthesis.ExecutiveSummary))

			if len(synthesis.TopRisks) > 0 {
				fmt.Fprintf(os.Stderr, "  %s\n", header.Render("Top Risks"))
				for i, risk := range synthesis.TopRisks {
					fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, info.Render(risk))
				}
				fmt.Fprintln(os.Stderr)
			}

			if len(synthesis.SystemicPatterns) > 0 {
				fmt.Fprintf(os.Stderr, "  %s\n", header.Render("Systemic Patterns"))
				for _, p := range synthesis.SystemicPatterns {
					fmt.Fprintf(os.Stderr, "  • %s\n", info.Render(p))
				}
				fmt.Fprintln(os.Stderr)
			}

			if len(synthesis.Recommendations) > 0 {
				fmt.Fprintf(os.Stderr, "  %s\n", header.Render("Recommendations"))
				for i, rec := range synthesis.Recommendations {
					fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, info.Render(rec))
				}
				fmt.Fprintln(os.Stderr)
			}
		}

		// Comparison with previous audit
		if previousFindings != nil {
			comparison := audit.CompareFindings(result.Findings, previousFindings)
			fmt.Fprintf(os.Stderr, "  %s %s\n", dimStyle.Render("[vs last audit]"), info.Render(comparison.FormatComparison()))
		}

		// Cross-cutting observations are used as input to Phase 4 synthesis
		// but not displayed directly — the synthesis output covers them.
	}

	// Show actual token usage and cost
	{
		u := result.Usage
		total := u.Total()
		aoiPricing := audit.DefaultPricing(selection.AOIModel)
		reviewPricing := audit.DefaultPricing(selection.ReviewModel)

		aoiCost := float64(u.AOI.InputTokens)/1_000_000*aoiPricing.InputPerMTok +
			float64(u.AOI.OutputTokens)/1_000_000*aoiPricing.OutputPerMTok
		reviewCost := float64(u.Review.InputTokens+u.Recheck.InputTokens+u.Synth.InputTokens)/1_000_000*reviewPricing.InputPerMTok +
			float64(u.Review.OutputTokens+u.Recheck.OutputTokens+u.Synth.OutputTokens)/1_000_000*reviewPricing.OutputPerMTok
		totalCost := aoiCost + reviewCost

		costLine := fmt.Sprintf("Tokens: %dk in / %dk out | AOI: $%.4f | Review: $%.4f | Total: $%.4f",
			total.InputTokens/1000, total.OutputTokens/1000, aoiCost, reviewCost, totalCost)
		fmt.Fprintf(os.Stderr, "  %s %s\n", dimStyle.Render("[cost]"), info.Render(costLine))
	}
	fmt.Fprintln(os.Stderr)

	// Save snapshot for future comparisons
	if err := audit.SaveSnapshot(repoRoot, result.Findings); err != nil {
		log.Printf("Warning: failed to save audit snapshot: %v", err)
	}

	// Export report if requested
	if outputPath != "" {
		if err := audit.Export(result, outputPath); err != nil {
			printError(fmt.Errorf("exporting report: %w", err))
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "  Report saved to %s\n\n", info.Render(outputPath))
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
	fmt.Fprintf(os.Stderr, "    --output=<path>      Export report (.json or .md)\n")
	fmt.Fprintf(os.Stderr, "    --no-cache           Ignore cached results, re-audit everything\n")
	fmt.Fprintf(os.Stderr, "    --no-synthesis       Skip Phase 4 executive summary synthesis\n")
	fmt.Fprintf(os.Stderr, "    --quiet, -q          Suppress terminal output (use with --output)\n")
	fmt.Fprintf(os.Stderr, "    --debug              Print all LLM prompts and responses to stderr\n")
	fmt.Fprintf(os.Stderr, "    --file=<path>        Restrict audit to a single file (relative to repo root)\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  %s\n", dim.Render("Available dimensions:"))
	fmt.Fprintf(os.Stderr, "    authentication, authorization, input-validation, data-integrity,\n")
	fmt.Fprintf(os.Stderr, "    cryptography, error-handling, concurrency, external-io, financial,\n")
	fmt.Fprintf(os.Stderr, "    configuration, api-design, resource-management, testing, correctness,\n")
	fmt.Fprintf(os.Stderr, "    design, performance, readability, cross-cutting\n\n")
}

// ── Review mode ────────────────────────────────────────────────────────

func runReview(debug bool, args []string) {
	var outputPath string
	noSynth := false
	noCache := false
	quiet := false
	reviewDebug := debug

	// Parse flags and find the PR number
	var prNumber string
	for _, arg := range args {
		if strings.HasPrefix(arg, "--output=") {
			outputPath = strings.TrimPrefix(arg, "--output=")
		} else if arg == "--no-cache" {
			noCache = true
		} else if arg == "--no-synthesis" {
			noSynth = true
		} else if arg == "--quiet" || arg == "-q" {
			quiet = true
		} else if arg == "--debug" {
			reviewDebug = true
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
	cfg, err := config.Load()
	if err != nil {
		printError(err)
		os.Exit(1)
	}
	cfg.Debug = reviewDebug

	if err := initLogger(); err != nil {
		printError(fmt.Errorf("failed to initialize logger: %w", err))
		os.Exit(1)
	}

	// Create AI clients
	reviewClient := createAIClient(cfg)

	aoiClient, aoiErr := createAOIClient(cfg)
	if aoiErr != nil {
		log.Printf("AOI client not available: %v", aoiErr)
	}

	aoiModelName := "disabled"
	if aoiClient != nil {
		aoiModelName = os.Getenv("PRR_AOI_MODEL")
		if aoiModelName == "" && cfg.AOIModel != "" {
			aoiModelName = cfg.AOIModel
		}
		if aoiModelName == "" {
			aoiModelName = "gemini-2.5-flash-lite"
		}
	}
	aoiProfile := security.GetAOIProfile(aoiModelName)

	log.Printf("Starting headless PR review for PR #%s (provider: %s, model: %s, aoi: %s)",
		prNumber, cfg.Provider, cfg.Model, aoiModelName)

	// Styles
	header := lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	info := lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))
	sevCritical := lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8")).Bold(true)
	sevHigh := lipgloss.NewStyle().Foreground(lipgloss.Color("#FAB387")).Bold(true)
	sevMedium := lipgloss.NewStyle().Foreground(lipgloss.Color("#F9E2AF"))
	sevLow := lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))

	// Run the review pipeline
	ctx := context.Background()

	opts := review.PRReviewOptions{
		PRNumber:           prNumber,
		RepoRoot:           repoRoot,
		NoCache:            noCache,
		NoSynthesis:        noSynth,
		ParallelReviews:    cfg.ParallelReviews,
		AOIContextLines:    aoiProfile.ContextLines,
		CustomInstructions: config.LoadCustomInstructions(),
		Debug:              reviewDebug,
	}

	result, err := review.RunPRReview(ctx, reviewClient, aoiClient, opts, func(phase, msg string) {
		if !quiet {
			fmt.Fprintf(os.Stderr, "[%s] %s\n", phase, msg)
		}
	})
	if err != nil {
		printError(err)
		os.Exit(1)
	}

	if quiet && outputPath == "" {
		os.Exit(0)
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "\n  %s  PR #%s: %s\n\n",
			header.Render("Review Complete"),
			prNumber,
			info.Render(result.PR.Title))

		// Print structured review if available
		if result.StructuredReview != nil {
			sr := result.StructuredReview

			// Verdict
			verdictStyle := info
			switch sr.Verdict {
			case "approve":
				verdictStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1")).Bold(true)
			case "request_changes":
				verdictStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8")).Bold(true)
			}
			fmt.Fprintf(os.Stderr, "  %s %s\n\n",
				header.Render("Verdict:"),
				verdictStyle.Render(sr.Verdict))

			// Summary
			if sr.Summary != "" {
				fmt.Fprintf(os.Stderr, "  %s\n", header.Render("Summary"))
				fmt.Fprintf(os.Stderr, "  %s\n\n", info.Render(sr.Summary))
			}

			// Findings
			if len(sr.Findings) > 0 {
				fmt.Fprintf(os.Stderr, "  %s (%d)\n\n", header.Render("Findings"), len(sr.Findings))
				for _, f := range sr.Findings {
					var sevStyle lipgloss.Style
					switch f.Severity {
					case "critical":
						sevStyle = sevCritical
					case "high":
						sevStyle = sevHigh
					case "medium":
						sevStyle = sevMedium
					default:
						sevStyle = sevLow
					}

					fmt.Fprintf(os.Stderr, "  %s %s\n",
						sevStyle.Render("["+f.Severity+"]"),
						info.Render(f.Title))
					if f.File != "" {
						fmt.Fprintf(os.Stderr, "    %s:%d (%s)\n", f.File, f.Line, f.Category)
					}
					if f.Detail != "" {
						fmt.Fprintf(os.Stderr, "    %s\n", dimStyle.Render(f.Detail))
					}
					if f.Suggestion != "" {
						fmt.Fprintf(os.Stderr, "    Fix: %s\n", dimStyle.Render(f.Suggestion))
					}
					fmt.Fprintln(os.Stderr)
				}
			}

			// Questions for author
			if len(sr.QuestionsForAuthor) > 0 {
				fmt.Fprintf(os.Stderr, "  %s\n", header.Render("Questions for Author"))
				for i, q := range sr.QuestionsForAuthor {
					fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, info.Render(q))
				}
				fmt.Fprintln(os.Stderr)
			}

			// Missing tests
			if len(sr.MissingTests) > 0 {
				fmt.Fprintf(os.Stderr, "  %s\n", header.Render("Missing Tests"))
				for _, t := range sr.MissingTests {
					fmt.Fprintf(os.Stderr, "  • %s\n", info.Render(t))
				}
				fmt.Fprintln(os.Stderr)
			}
		} else if result.Review != nil && result.Review.Summary != "" {
			// Fallback: raw summary
			fmt.Fprintf(os.Stderr, "  %s\n\n", info.Render(result.Review.Summary))
		}

		// Deep findings (from AOI)
		if len(result.DeepFindings) > 0 {
			fmt.Fprintf(os.Stderr, "  %s (%d)\n\n", header.Render("Security Findings"), len(result.DeepFindings))
			for _, f := range result.DeepFindings {
				var sevStyle lipgloss.Style
				switch f.Severity {
				case "critical":
					sevStyle = sevCritical
				case "high":
					sevStyle = sevHigh
				case "medium":
					sevStyle = sevMedium
				default:
					sevStyle = sevLow
				}

				fmt.Fprintf(os.Stderr, "  %s %s\n",
					sevStyle.Render("["+f.Severity+"]"),
					info.Render(f.Title))
				fmt.Fprintf(os.Stderr, "    %s:%s (%s/%s)\n",
					f.File, f.Lines, f.Category, f.Subcategory)
				if f.Description != "" {
					fmt.Fprintf(os.Stderr, "    %s\n", dimStyle.Render(f.Description))
				}
				fmt.Fprintln(os.Stderr)
			}
		}

		fmt.Fprintf(os.Stderr, "  %s %s files reviewed\n\n",
			dimStyle.Render("[stats]"),
			info.Render(fmt.Sprintf("%d", result.FilesReviewed)))
	}

	// Export if requested
	if outputPath != "" {
		if err := exportReviewResult(result, outputPath); err != nil {
			printError(fmt.Errorf("exporting report: %w", err))
			os.Exit(1)
		}
		if !quiet {
			fmt.Fprintf(os.Stderr, "  Report saved to %s\n\n", info.Render(outputPath))
		}
	}
}

// exportReviewResult writes the review result to a JSON file.
func exportReviewResult(result *review.PRReviewResult, path string) error {
	data := map[string]interface{}{
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
	fmt.Fprintf(os.Stderr, "    --debug              Print all LLM prompts and responses\n\n")
}

func createAIClient(cfg *config.Config) ai.Client {
	if cfg.APIKey == "" || cfg.APIKey == "YOUR_API_KEY" {
		printError(fmt.Errorf("no API key configured. Set PRR_API_KEY or update api_key in your config file"))
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
	modelCfg := config.GetModelConfig(models, cfg.Model)

	var provider ai.Provider
	switch cfg.Provider {
	case "gemini":
		gp := &ai.GeminiProvider{
			APIKey: cfg.APIKey,
			Model:  cfg.Model,
		}
		gp.ModelConfig.MaxOutputTokens = modelCfg.MaxOutputTokens
		gp.ModelConfig.Temperature = modelCfg.Temperature
		gp.ModelConfig.ThinkingBudget = modelCfg.ThinkingBudget
		provider = gp
	default:
		log.Fatalf("Unsupported AI provider: %q. Supported: gemini, anthropic, openai", cfg.Provider)
	}

	var opts []ai.AgentOption
	if cfg.Debug {
		// Debug log goes to the same log file as the rest of the app
		opts = append(opts, ai.WithDebugLogger(log.Writer()))
	}

	return ai.NewAgent(provider, toolExec, opts...)
}

// createAOIClient creates a lightweight AI client for the security AOI pre-scan.
// It uses the same provider credentials but with the cheapest available model
// and no tools. The AOI scanner only needs to analyze diffs — no file reading or grep.
// createAOIClient creates the cheap LLM client for AOI pre-scanning.
// Priority: PRR_AOI_MODEL env var > config aoi_model > provider default.
func createAOIClient(cfg *config.Config) (ai.Client, error) {
	var aoiModel string
	if envModel := os.Getenv("PRR_AOI_MODEL"); envModel != "" {
		aoiModel = envModel
	} else if cfg.AOIModel != "" {
		aoiModel = cfg.AOIModel
	} else {
		switch cfg.Provider {
		case "gemini":
			aoiModel = "gemini-2.5-flash-lite"
		case "anthropic":
			aoiModel = "claude-haiku-3-5"
		case "openai":
			aoiModel = "gpt-4o-mini"
		default:
			return nil, fmt.Errorf("unsupported provider %q for AOI scanning", cfg.Provider)
		}
	}

	// API key: PRR_AOI_API_KEY env > config aoi_api_key > config api_key
	aoiAPIKey := cfg.APIKey
	if cfg.AOIAPIKey != "" {
		aoiAPIKey = cfg.AOIAPIKey
	}
	if envKey := os.Getenv("PRR_AOI_API_KEY"); envKey != "" {
		aoiAPIKey = envKey
	}

	if aoiAPIKey == "" || aoiAPIKey == "YOUR_API_KEY" {
		return nil, fmt.Errorf("no API key configured for AOI model (%s). Set PRR_AOI_API_KEY, or configure aoi_api_key / api_key in your config", aoiModel)
	}

	// Use benchmark-tuned settings from the model profile
	aoiProfile := security.GetAOIProfile(aoiModel)

	var provider ai.Provider
	switch cfg.Provider {
	case "gemini":
		gp := &ai.GeminiProvider{
			APIKey: aoiAPIKey,
			Model:  aoiModel,
		}
		gp.ModelConfig.MaxOutputTokens = aoiProfile.MaxOutputTokens
		gp.ModelConfig.Temperature = aoiProfile.Temperature
		gp.ModelConfig.ThinkingBudget = aoiProfile.ThinkingBudget
		provider = gp
	default:
		return nil, fmt.Errorf("unsupported provider %q for AOI scanning", cfg.Provider)
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
	} else if strings.HasPrefix(url, "ssh://") {
		// ssh://git@github.com/user/repo.git
		trimmed := strings.TrimPrefix(url, "ssh://")
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
	for _, line := range strings.Split(strings.TrimSpace(string(keys)), "\n") {
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

	// Ensure ~/.ssh exists
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("SSH known_hosts: failed to get home dir: %v", err)
		return
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		log.Printf("SSH known_hosts: failed to create %s: %v", sshDir, err)
		return
	}

	knownHosts := filepath.Join(sshDir, "known_hosts")
	f, err := os.OpenFile(knownHosts, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("SSH known_hosts: failed to open %s: %v", knownHosts, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(keys); err != nil {
		log.Printf("SSH known_hosts: failed to write to %s: %v", knownHosts, err)
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

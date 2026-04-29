package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"prr/internal/ai"
	"prr/internal/config"
	"prr/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func main() {
	// Force truecolor early so styled error output works too
	lipgloss.SetColorProfile(termenv.TrueColor)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	prNumber := os.Args[1]

	// Pre-flight checks before launching the TUI
	if err := preflight(prNumber); err != nil {
		printError(err)
		os.Exit(1)
	}

	// Load config for AI
	cfg, err := config.Load()
	if err != nil {
		printError(err)
		os.Exit(1)
	}

	// Create AI client based on provider
	aiClient := createAIClient(cfg)

	// Initialize hidden debug logger
	if err := initLogger(); err != nil {
		printError(fmt.Errorf("failed to initialize logger: %w", err))
		os.Exit(1)
	}
	log.Printf("Starting PR review TUI for PR #%s (provider: %s, model: %s)", prNumber, cfg.Provider, cfg.Model)

	model := ui.NewModel(prNumber, aiClient)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithANSICompressor())
	ui.SetProgram(p)

	if _, err := p.Run(); err != nil {
		log.Fatalf("Error running program: %v", err)
	}
}

// ── AI client factory ──────────────────────────────────────────────────

func createAIClient(cfg *config.Config) ai.Client {
	switch cfg.Provider {
	case "gemini":
		return &ai.GeminiClient{
			APIKey: cfg.APIKey,
			Model:  cfg.Model,
		}
	default:
		log.Fatalf("Unsupported AI provider: %q. Supported providers: gemini", cfg.Provider)
		return nil // unreachable, log.Fatalf exits
	}
}

// ── Pre-flight checks ──────────────────────────────────────────────────

func preflight(prNumber string) error {
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

	// 4. Check delta is installed
	if _, err := exec.LookPath("delta"); err != nil {
		return fmt.Errorf("delta is not installed\n  Install it: https://github.com/dandavison/delta")
	}

	// 5. Validate PR number (quick sanity check)
	prNumber = strings.TrimSpace(prNumber)
	if prNumber == "" {
		return fmt.Errorf("PR number cannot be empty")
	}

	return nil
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

	fmt.Fprintf(os.Stderr, "\n  %s  %s\n\n",
		logo.Render("prr"),
		dim.Render("— review PRs in your terminal"))
	fmt.Fprintf(os.Stderr, "  %s  prr <pr_number>\n\n",
		dim.Render("Usage:"))
	fmt.Fprintf(os.Stderr, "  %s  prr 42\n\n",
		dim.Render("Example:"))
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
	gitDir := ".git"
	logDir := filepath.Join(gitDir, "pr-tui")

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}

	logFile := filepath.Join(logDir, "debug.log")
	f, err := tea.LogToFile(logFile, "debug")
	if err != nil {
		return err
	}
	_ = f
	return nil
}

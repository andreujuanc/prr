package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	// Parse flags
	debug := false
	args := os.Args[1:]
	var positional []string
	for _, arg := range args {
		if arg == "--debug" {
			debug = true
		} else if arg == "--help" || arg == "-h" {
			printUsage()
			os.Exit(0)
		} else {
			positional = append(positional, arg)
		}
	}

	var prNumber string
	if len(positional) >= 1 {
		prNumber = positional[0]
	}

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
	cfg.Debug = debug

	// Create AI client based on provider
	aiClient := createAIClient(cfg)

	// Initialize hidden debug logger
	if err := initLogger(); err != nil {
		printError(fmt.Errorf("failed to initialize logger: %w", err))
		os.Exit(1)
	}
	prLabel := prNumber
	if prLabel == "" {
		prLabel = "(picker)"
	}
	log.Printf("Starting PR review TUI for PR #%s (provider: %s, model: %s)", prLabel, cfg.Provider, cfg.Model)

	model := ui.NewModel(prNumber, aiClient, cfg.ParallelReviews)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	ui.SetProgram(p)

	if _, err := p.Run(); err != nil {
		log.Fatalf("Error running program: %v", err)
	}
}

// ── AI client factory ──────────────────────────────────────────────────

func createAIClient(cfg *config.Config) ai.Client {
	toolExec := &ai.ToolExecutor{}

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

	// Get download URL for the matching .deb asset
	pattern := fmt.Sprintf("git-delta_*_%s.deb", debArch)
	cmd := exec.Command("gh", "release", "download", "--repo", "dandavison/delta",
		"--pattern", pattern, "--dir", os.TempDir())
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to download delta .deb: %v\n  Install manually: https://github.com/dandavison/delta/releases", err)
	}

	// Find the downloaded file
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return fmt.Errorf("failed to read temp dir: %v", err)
	}
	var debFile string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "git-delta_") && strings.HasSuffix(e.Name(), "_"+debArch+".deb") {
			debFile = filepath.Join(os.TempDir(), e.Name())
			break
		}
	}
	if debFile == "" {
		return fmt.Errorf("downloaded .deb not found in %s", os.TempDir())
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
		os.Remove(debFile)
		return fmt.Errorf("failed to install delta: %v\n  Try manually: sudo dpkg -i %s", err, debFile)
	}

	os.Remove(debFile)

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
		return
	}
	sshDir := filepath.Join(home, ".ssh")
	os.MkdirAll(sshDir, 0700)

	knownHosts := filepath.Join(sshDir, "known_hosts")
	f, err := os.OpenFile(knownHosts, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(keys)

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

	fmt.Fprintf(os.Stderr, "\n  %s  %s\n\n",
		logo.Render("prr"),
		dim.Render("— review PRs in your terminal"))
	fmt.Fprintf(os.Stderr, "  %s  prr [pr_number]\n\n",
		dim.Render("Usage:"))
	fmt.Fprintf(os.Stderr, "  %s  prr 42       Review PR #42\n",
		dim.Render(""))
	fmt.Fprintf(os.Stderr, "  %s  prr          Pick from open PRs\n\n",
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
	f, err := tea.LogToFile(logFile, "debug")
	if err != nil {
		return err
	}
	_ = f
	return nil
}

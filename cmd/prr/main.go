package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"prr/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <pr_number>\n", filepath.Base(os.Args[0]))
		os.Exit(1)
	}

	prNumber := os.Args[1]

	// Initialize hidden debug logger
	err := initLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing logger: %v\n", err)
		os.Exit(1)
	}
	log.Printf("Starting PR review TUI for PR #%s", prNumber)

	p := tea.NewProgram(ui.NewModel(prNumber), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatalf("Error running program: %v", err)
		os.Exit(1)
	}
}

func initLogger() error {
	// The plan specifies logging to .git/pr-tui/debug.log
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
	// Note: We don't close the file here because the standard log needs to write to it for the lifetime of the app
	// Usually LogToFile configures the default logger
	_ = f
	return nil
}

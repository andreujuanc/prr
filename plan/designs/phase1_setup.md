# Phase 1: Project Setup & Environment Design

## Objective
Initialize the Go project, configure dependency management, and establish the directory structure.

## Directory Structure & Files

1. **`go.mod` & `go.sum`**
   - Dependencies: `charmbracelet/bubbletea`, `charmbracelet/bubbles`, `charmbracelet/lipgloss`.

2. **`cmd/prr/main.go`**
   - **Responsibility**: Main entry point.
   - **Design**:
     - Requires the PR number as a CLI argument (e.g., `prr 123`). If missing, print usage instructions and exit.
     - Initializes a hidden debug logger to `.git/pr-tui/debug.log` to prevent TUI disruption.
     - Instantiates the Bubble Tea program.

3. **Package Directories**
   - `internal/git/`: GitHub/Git wrappers.
   - `internal/state/`: JSON state management (including chat histories).
   - `internal/ui/`: Bubble Tea models.
   - `internal/ai/`: LLM integration.
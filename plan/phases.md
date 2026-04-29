# PR Review TUI Tool - Implementation Plan

## Overview
A TUI tool built in Go using Bubble Tea to review GitHub Pull Requests file by file, keeping track of reviewed files locally, and incorporating AI-assisted code reviews on a per-file basis.

## Architecture
- **Language**: Go
- **UI Framework**: `charmbracelet/bubbletea` (TUI), `bubbles/list`, `bubbles/viewport`, `charmbracelet/lipgloss` (Styling).
- **Dependencies**: `gh` (GitHub CLI) for PR metadata, `git` and `delta` for generating styled diffs.
- **AI Integration**: Integration with an LLM API (e.g., OpenAI, Anthropic, or local via Ollama) to analyze file diffs.
- **State Storage**: Review states are stored locally inside the repository at `.git/pr-tui/<pr_number>.json`.
  - Storing in `.git/` naturally ignores the files from version control and scopes it per-repo.
  - State invalidates if a file's diff hash changes (i.e., new commits pushed to the PR).
  - Caches AI review text to avoid redundant, slow, and costly API calls.

---

## Phase 1: Project Setup & Environment
- [x] Initialize Go module (`go mod init`).
- [x] Install Bubble Tea dependencies.
- [x] Create basic project structure (`cmd/`, `internal/ui/`, `internal/git/`, `internal/state/`, `internal/ai/`).

## Phase 2: Git & GitHub CLI Wrappers (`internal/git`)
- [x] Create Go functions to execute `gh pr view <number> --json ...` to fetch files, base branch, and PR details.
- [x] Create Go functions to execute `git diff <base>...<head> -- <file> | delta` and capture the ANSI-styled output.
- [x] Create Go functions to fetch raw, unstyled diffs (to pass to the AI).
- [x] Create a utility to calculate SHA-256 hashes of the file diffs for invalidation logic.

## Phase 3: State Management (`internal/state`)
- [x] Define the Go structs for the JSON state (map of `filepath` -> `{status, diff_hash, chat}`).
- [x] Implement `Load(prNumber string)` to read from `.git/pr-tui/<pr_number>.json`.
- [x] Implement `Save(prNumber string, state State)` to save back to the file.
- [x] Implement invalidation logic (compare currently generated diff hash vs stored hash). If hash changes, invalidate both `status` and `chat`.

## Phase 4: TUI Skeleton (`internal/ui`)
- [ ] Build the main Bubble Tea Model (`Model` struct).
- [ ] Implement the 3-pane layout with `lipgloss`:
  - **Header**: PR info and progress bar.
  - **Left Pane (25%)**: File list using `bubbles/list`.
  - **Right-Top Pane (75% width, 65% height)**: Diff viewport using `bubbles/viewport`.
  - **Right-Bottom Pane (75% width, 35% height)**: AI Review viewport.
- [ ] Populate the UI with mock data to test styling.

## Phase 5: AI Integration (`internal/ai`)
- [ ] Create an LLM client wrapper (e.g., using `sashabaranov/go-openai` for standard compatibility, or a generic HTTP client for Ollama/others).
- [ ] Design the prompt: "Review the following code diff for the file <filename>. Focus on bugs, security, and performance. Keep it concise."
- [ ] Wire up asynchronous fetching of AI reviews so the UI doesn't block while waiting for the LLM response.

## Phase 6: Integration & Data Flow
- [ ] Fetch actual PR data on startup (`Init()`).
- [ ] Process diff hashing in the background to not block the initial render.
- [ ] Sync the file list with the loaded state (mark as unreviewed/reviewed/modified).
- [ ] Render the actual `delta` output into the Diff viewport.
- [ ] Render cached AI review into the AI viewport (if it exists).

## Phase 7: Interactivity & UX
- [ ] `Space`: Toggle file review status and trigger a state save.
- [ ] `a`: Trigger/Regenerate AI review for the currently selected file. Show a spinner while loading.
- [ ] `Tab` / `Shift+Tab`: Switch focus between File List, Diff Viewport, and AI Review Viewport.
- [ ] `j`/`k` or `Up`/`Down`: Scroll within the focused pane.
- [ ] Progress Bar: Update automatically as files are toggled.
- [ ] Keybindings: Add `n`/`p` for jumping to next/previous unreviewed file.
# prr

AI-powered pull request reviewer in your terminal. Review diffs, navigate findings, comment, and submit — without leaving the CLI.

Built with Go and [Bubble Tea](https://github.com/charmbracelet/bubbletea). Diffs styled by [delta](https://github.com/dandavison/delta).

![prr demo](docs/demo/demo.gif)

## Prerequisites

- [Go 1.26+](https://go.dev/dl/)
- [gh](https://cli.github.com/) (GitHub CLI, authenticated)
- [delta](https://github.com/dandavison/delta) (git-delta)
- A Gemini API key

## Install

**Download a binary** from the [latest release](https://github.com/andreujuanc/prr/releases/latest).

**Or via Go:**

```bash
go install github.com/andreujuanc/prr/cmd/prr@latest
```

**Or build from source:**

```bash
git clone https://github.com/andreujuanc/prr.git
cd prr
go build -o prr ./cmd/prr
```

## Setup

On first run, prr creates `~/.config/prr/config.json`:

```json
{
  "provider": "gemini",
  "api_key": "YOUR_API_KEY",
  "model": "gemini-2.5-pro",
  "parallel_reviews": 3,
  "pipes": []
}
```

Set your API key and you're ready to go. The `pipes` array is optional — see [Pipe Targets](#pipe-targets) for configuration.

### Claude Code (zero-config)

If you already have the [`claude` CLI](https://docs.claude.com/claude-code) installed and signed in, prr will detect it and offer Claude Opus / Sonnet / Haiku in the model picker without any API key in `config.json`. Auth is handled by Claude Code itself (subscription, OAuth, or `ANTHROPIC_API_KEY`), which is useful in environments where you can't easily set an Anthropic API key (e.g. Claude Enterprise).

The provider runs `claude -p` per request with a curated read-only toolset (`Read`, `Grep`, `Glob`, plus specific read-only `git` subcommands) and `--permission-mode bypassPermissions`, so it never edits files and never prompts for permission.

## Usage

```bash
prr 42        # Review PR #42
prr           # Pick from open PRs
prr --debug   # Enable debug logging
```

## Modes

prr has three modes. The interactive TUI is the primary one; the headless modes are for CI integration and scheduled audits.

**Interactive TUI** (`prr <PR#>`) — three-pane terminal UI to read diffs, navigate findings, and post review comments without leaving the CLI. See [Features](#features) and [Keybindings](#keybindings).

**Headless review** (`prr review <PR#>`) — runs the full multi-pass review pipeline against a single PR and emits a JSON report. Designed for CI: no TUI, no interaction. See [Headless review](#headless-review).

**Project audit** (`prr audit`) — scans the entire codebase (or a recent slice) for latent issues across configurable dimensions. Not tied to a PR. See [Project audit](#project-audit).

### Headless review

Runs the same review pipeline as the TUI's `a` key, but headlessly and with JSON export. Typical use: a CI step that uploads the JSON as an artifact or posts findings to a dashboard.

```bash
prr review 42 --output=review.json --bug-priors --quiet
```

| Flag | Action |
|------|--------|
| `--output=<path>` | Export report to JSON file |
| `--no-cache` | Ignore cached deep-review results |
| `--no-synthesis` | Skip the synthesis pass (only emit raw findings) |
| `--review-mode=<mode>` | `full` (default) reviews every file; `aoi-only` skips files without AOIs |
| `--bug-priors` | Inject recent fix-shaped commits as known-failure priors |
| `--quiet`, `-q` | Suppress terminal output (use with `--output`) |
| `--debug` | Print LLM tool calls, prompts, and responses to stderr |

The JSON output has these top-level keys: `pr_number`, `pr_title`, `files_reviewed`, `review` (the synthesised result — verdict, summary, findings, missing tests, questions for author, and a per-file `coverage` block), and `deep_findings` (raw AOI-derived findings). The full schema lives in [`internal/state/models.go`](internal/state/models.go).

### Project audit

Runs a full-project pass without a PR. Useful for periodic codebase health checks, onboarding audits, or measuring the impact of a refactor.

```bash
prr audit --focus=security,correctness --audit-recent=30
```

| Flag | Action |
|------|--------|
| `--focus=<dims>` | Comma-separated dimensions to focus on (default: all) |
| `--exclude=<globs>` | Additional exclude patterns |
| `--include=<globs>` | Force-include patterns (override exclusions) |
| `--max-reviews=<n>` | Cap on Phase 3 review calls |
| `--concurrency=<n>` | Per-phase parallelism cap (default 5) |
| `--output=<path>` | Export report (`.json` or `.md`) |
| `--no-cache` | Ignore cached results, re-audit everything |
| `--no-synthesis` | Skip Phase 4 executive summary |
| `--file=<path>` | Restrict audit to a single file (relative to repo root) |
| `--audit-recent=<n>` | Restrict audit to files touched in the last N commits |
| `--sibling-cluster` | Enable Phase 2.5 sibling-outlier detection (experimental) |
| `--bug-priors` | Inject recent fix-shaped commits as known-failure priors |
| `--quiet`, `-q` | Suppress terminal output (use with `--output`) |
| `--debug` | Print LLM tool calls, prompts, and responses to stderr |

**Available dimensions:** `authentication`, `authorization`, `input-validation`, `data-integrity`, `cryptography`, `error-handling`, `concurrency`, `external-io`, `financial`, `configuration`, `api-design`, `resource-management`, `testing`, `test-coverage`, `correctness`, `design`, `performance`, `readability`, `cross-cutting`, `observability`, `web-security`.

**Cache invalidation.** The deep-review and recheck cache keys hash the prompts, the runtime model, and (when `--bug-priors` is enabled) the bug-priors content. Prompt edits and new fix-shaped commits automatically invalidate stale entries on the next run; no manual cache flush needed.

## Features

**Three-pane layout** — file tree, diff viewer, and AI panel side by side.

**AI-powered review** — Multi-pass batch review with structured findings. The AI reads diffs, uses tools (grep, git blame, read file), and produces a prioritized list of issues with severity ratings and a recommended verdict.

**Inline findings** — AI review findings render as severity-colored boxes directly in the diff view, positioned below their target lines. Toggle with `F`. Findings are sorted by severity (critical first) and support file-level, right-side, and left-side placement.

**GitHub Actions status** — The file tree shows an "Actions" entry with a live status dot (green/red/yellow). Select it to see workflow runs, jobs, and steps in the diff pane. Active runs poll every 15 seconds automatically.

**Publish findings** — Post individual findings as GitHub line comments (`c`), submit all findings as a batch GitHub Review (`C`), or post as a general PR comment (`g`). Confirmation modals prevent accidental submissions.

**Pipe to external tools** — Send findings to external processes (linters, ticket systems, Slack bots) configured in your config file. Pipe targets receive structured JSON, markdown, or plain text via stdin.

**Review workflow** — Track file review status, navigate between unreviewed files, submit your review to GitHub with approve/comment/request-changes verdicts.

**Inline comments** — Position cursor on any diff line and press `c` to leave a review comment directly on the PR.

**Chat** — Ask follow-up questions about any file or the PR as a whole. The AI has access to the full codebase via tools.

**Model switching** — Press `m` to switch between models on the fly. Choice is persisted to config.

**Smart caching** — Per-file review findings are cached. Re-running a review only re-analyzes changed files. Stale reviews are detected and flagged.

**Refresh from origin** — Press `o` to pull the latest changes without restarting.

## Keybindings

### Global

| Key | Action |
|-----|--------|
| `Tab` / `Shift+Tab` | Cycle panes |
| `Ctrl+A` | Toggle AI panel |
| `Ctrl+B` | Toggle file panel |
| `a` | AI review (file or full PR) |
| `A` | Force re-review (ignore cache) |
| `m` | Switch model |
| `?` | Help |
| `q` | Quit |

### File List

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate files |
| `Enter` | Select file |
| `l` / `h` | Expand / collapse directory |
| `Space` | Toggle reviewed status |
| `r` | Hide/show reviewed files |
| `n` / `p` | Next / previous unreviewed |
| `o` | Refresh PR from origin |

### Diff

| Key | Action |
|-----|--------|
| `j` / `k` | Move cursor |
| `G` / `g` | Jump to bottom / top |
| `Ctrl+D` / `Ctrl+U` | Half-page down / up |
| `+` / `-` | More / less context lines |
| `Space` | Toggle reviewed status |
| `n` / `p` | Next / previous unreviewed |
| `c` | Comment on line |
| `F` | Toggle inline findings |

### Review Panel

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate findings |
| `Enter` | Jump to finding in diff |
| `c` | Post finding as line comment |
| `C` | Submit all findings as GitHub Review |
| `g` | Post finding as PR comment |
| `\|` | Pipe finding to first configured target |
| `x` | Open action menu (all publish/pipe options) |
| `Tab` | Switch to Chat tab |
| `Ctrl+S` | Submit review to GitHub |

### Chat Panel

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+K` | Clear chat history |
| `Tab` | Switch to Review tab |

## Custom Instructions

Add project-specific review instructions that get included in every AI review:

```
.prr/instructions.md          # preferred
.github/prr-instructions.md   # alternative
```

Example:

```markdown
- This project uses the repository pattern; data access should go through repository interfaces
- Error handling must use wrapped errors with fmt.Errorf("context: %w", err)
- All public functions need doc comments
```

## Model Configuration

Model parameters are in `~/.config/prr/models.json`:

```json
{
  "gemini-2.5-flash": {
    "max_output_tokens": 65536,
    "temperature": 0.2,
    "thinking_budget": 8192
  }
}
```

Available models (switchable with `m`):

| Model | Thinking |
|-------|----------|
| `gemini-3.1-pro-preview` | Yes (16K budget) |
| `gemini-3.1-flash-lite` | Yes (8K budget) |
| `gemini-2.5-flash` | Yes (8K budget) |

## Pipe Targets

Configure external processes to receive findings via stdin. Add a `pipes` array to `~/.config/prr/config.json`:

```json
{
  "provider": "gemini",
  "api_key": "...",
  "model": "gemini-2.5-pro",
  "pipes": [
    {
      "name": "Create JIRA ticket",
      "command": "jira-create",
      "args": ["--project", "BACKEND"],
      "format": "json"
    },
    {
      "name": "Notify Slack",
      "command": "slack-notify",
      "args": ["--channel", "#code-review"],
      "format": "markdown"
    }
  ]
}
```

Supported formats:
- `json` — structured payload with file, line, severity, category, title, detail, suggestion, repo_root
- `markdown` — formatted heading, metadata, and detail sections
- `text` — compact plain text summary

Pipe targets appear in the action menu (`x`) and can be triggered directly with number keys (`1`, `2`, ...) when the menu is open.

## Files Excluded from AI Review

prr automatically skips files that aren't useful for code review:

- **Binary files** — images, fonts, archives, compiled artifacts
- **Generated files** — lock files, `.min.js`, `.pb.go`, `.gen.go`
- **Large diffs** — files with diffs exceeding 100KB
- **Vendored code** — `vendor/`, `node_modules/`

Excluded files still appear in the file tree (dimmed with a tag) but aren't sent to the AI.

## State

Review state is stored per-PR at `.git/pr-tui/<pr_number>.json`. This includes review status per file, cached AI findings, and chat history. State is local to your clone and not committed.

## Troubleshooting

If prr misbehaves — hangs during a phase, returns empty findings, or
crashes — the steps below collect the data needed to diagnose it.

### Debug log

Every run writes to `~/.cache/prr/debug.log`. It captures cache misses,
batch retries, parse failures with response prefixes, AOI routing, and
any warnings emitted by the pipeline. The file is append-only across
runs, so timestamps matter.

```bash
# Last 200 lines of the most recent activity
tail -200 ~/.cache/prr/debug.log
```

For verbose output, pass `--debug` on the command line. Extra detail
from the agent loop (per-round LLM calls, tool calls, tool results)
goes to the same file.

### Capturing a hang without killing the process

If prr appears stuck in a phase, send `SIGUSR1` to dump every
goroutine's stack to a file. The process keeps running and the file is
readable while it's open. You can dump as many times as you like —
each dump gets its own timestamp.

```bash
# In another terminal:
kill -USR1 $(pgrep -f 'prr')

# Find the latest dump:
ls -ltr ~/.cache/prr/goroutines-*.txt | tail -1
```

The dump shows which goroutine is doing what. The ones to look at:

- A goroutine in `parseSSEStream` → `http2pipe.Read` is actively
  receiving from the model.
- A goroutine in `http2ClientConn.roundTrip` with no body read going
  means we sent the request and never got a response — likely an
  upstream silent hang. The provider's `ResponseHeaderTimeout` (90s
  default) should turn this into a fast retry.
- Multiple goroutines parked in `sync.WaitGroup.Wait` are the
  orchestration layer waiting on those workers above.

### Parse failures

When the AI's response can't be parsed as JSON, the log records the
first 500 chars of the response so you can see what the model
actually said. Look for lines like:

```
Warning: failed to parse batch JSON: invalid character 'm' looking for beginning of value — response prefix: "my analysis ..."
```

This tells you whether the model went conversational, hit a refusal,
returned markdown without code fences, or something else — the fix
depends on which.

### What to share when reporting an issue

1. `~/.cache/prr/debug.log` (or just the last few hundred lines around
   the failure).
2. The most recent `~/.cache/prr/goroutines-*.txt` if the run hung.
3. The exact command you ran (`prr 42 --no-cache`, etc.).
4. Your model selection (`gemini-2.5-pro`, etc.) — visible in the
   first lines of debug.log.

That set is enough to reconstruct what happened without needing your
repo content.

## Roadmap

Ideas we want to build but haven't yet.

- **Incremental re-review on push.** When a branch that's already been reviewed gets a new commit, prr re-uses the prior run's findings: marks which ones the new commit resolved, which still stand, and only spends model time on what actually changed plus any newly introduced issues. Needs per-branch finding persistence and stable IDs that survive line shifts.

## License

MIT

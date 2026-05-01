# Contributing to prr

Thanks for your interest in contributing to prr! Here's how to get started.

## Prerequisites

- Go 1.22+
- [gh](https://cli.github.com/) (GitHub CLI, authenticated)
- [delta](https://github.com/dandavison/delta) (git-delta)
- A Gemini API key (for running integration tests)

## Setup

```bash
git clone git@github.com:andreujuanc/prr.git
cd prr
go build ./...
go test ./...
```

## Project Structure

```
cmd/prr/              CLI entrypoint
internal/
  ai/                 AI client, agent loop, tool execution, Gemini provider
  config/             Config files, model settings, exclusion rules
  git/                Git/GitHub CLI wrappers, diff, PR fetching, file filters
  state/              Per-PR review state persistence
  ui/                 Bubble Tea TUI (model, view, update, overlays, file tree)
```

## Development Workflow

1. Fork and create a branch from `master`
2. Make your changes
3. Run tests: `go test ./...`
4. Build: `go build -o prr ./cmd/prr`
5. Test manually against a real PR: `./prr <pr_number>`
6. Submit a PR

## Tests

```bash
# Unit tests (no API key needed)
go test ./...

# Integration tests with real Gemini API
export PRR_API_KEY="your-key"
go test ./internal/ai/ -run TestLive -v
```

Integration tests are gated behind the `PRR_API_KEY` environment variable. They make real API calls and are skipped when the variable is not set.

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use `log.Printf` for debug/warning output (not `fmt.Print` — stdout is owned by Bubble Tea)
- Prefer `strconv.Itoa` over `fmt.Sprintf("%d", n)` when `strconv` is already imported
- Error wrapping: `fmt.Errorf("context: %w", err)`

## Architecture Notes

- The TUI follows the Elm architecture via Bubble Tea: `Init` / `Update` / `View`
- All AI interactions go through the `Agent` which handles the tool-call loop
- State is persisted per-PR at `.git/pr-tui/<pr_number>.json`
- Custom review instructions go in `.prr/instructions.md` or `.github/prr-instructions.md`

## Reporting Issues

- Include your Go version (`go version`)
- Include your terminal emulator and OS
- For rendering issues, a screenshot helps
- For AI issues, run with `--debug` and include relevant lines from the debug log

## License

By contributing, you agree that your contributions will be licensed under the MIT License.

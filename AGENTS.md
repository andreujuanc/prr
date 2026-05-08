# AGENTS.md

## Environment

- **Always set `GOTOOLCHAIN=auto`** before running any Go commands (the devcontainer Go version is older than what `go.mod` requires).
- **Never read `.env`** (it contains secrets). Use `source .env && ./prr` to load env vars when running the binary.

## Build & verify

```bash
export GOTOOLCHAIN=auto
go build -o prr ./cmd/prr        # build binary
go test ./...                     # all tests
gofmt -l .                        # lint (CI fails if any output)
go vet ./...                      # vet
```

CI runs: `gofmt` + `go vet` + `go test` on every PR to `master`.

## Project layout

Single Go module. Entrypoint: `cmd/prr/main.go`.

| Package | Role |
|---------|------|
| `internal/ui` | Bubble Tea TUI (Elm architecture: Model/View/Update) |
| `internal/ai` | Gemini AI client, agent loop, tool execution |
| `internal/git` | Git/GitHub CLI wrappers, diff parsing, PR fetching |
| `internal/config` | Config loading, model definitions, exclusion rules |
| `internal/review` | Review logic, prompt construction, routing |
| `internal/state` | Per-PR review state persistence |
| `internal/security` | Security scanning |
| `internal/pipe` | Export targets for findings |

## Key conventions

- **Never use `fmt.Print*` for output** — stdout is owned by Bubble Tea. Use `log.Printf` for debug logging.
- **Error wrapping**: `fmt.Errorf("context: %w", err)`.
- **Formatting**: `gofmt` only (no goimports, no custom config).
- **Codegen**: `scripts/update-models.sh` regenerates `internal/config/known_models.go` from the Gemini API. Reads the API key from `~/.config/prr/config.json`. Requires `python3`. Do not hand-edit that file.

## Testing

- Integration tests require `PRR_LIVE_TESTS=1` and a valid `~/.config/prr/config.json` with API keys configured. They are skipped otherwise.
- Run live tests: `PRR_LIVE_TESTS=1 go test ./internal/ai/ -run TestLive -v`
- Helper script: `scripts/test-live.sh` (sets `PRR_LIVE_TESTS=1` automatically).

## Branching & release

- Default branch: `master`.
- Releases: automated via release-please + goreleaser on `master`.

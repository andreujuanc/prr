# Phase 2: Git & GitHub CLI Wrappers Design

## Objective
Provide a robust Go API to interface with local `git` and `gh`.

## Files & Components

### 1. `internal/git/gh.go` & `models.go`
- `FetchPR()`: Uses `gh pr view` to fetch PR metadata and the file list. Handles pagination limits if necessary.

### 2. `internal/git/diff.go`
- **Crucial pre-flight step**: Must ensure local refs are up to date (e.g., `git fetch`) before calculating diffs, so `base...head` doesn't fail.
- `GetStyledDiff()`: Runs `git diff <base>...<head> -- <file> | delta`.
- `GetRawDiff()`: Runs `git diff <base>...<head> -- <file>`.

### 3. `internal/git/hash.go`
- Generates SHA-256 hashes of the *raw* file diffs for precise state invalidation.
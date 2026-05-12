package state

import (
    "encoding/json"
    "fmt"
    "io"
    "log"
    "os"
    "path/filepath"
    "regexp"

    "github.com/andreujuanc/prr/internal/git"
)

// stateDirRel is the path under the repository root where prr stores
// per-PR state. We deliberately put it under .git/ so it inherits git's
// "not tracked" status and lives alongside other tool data.
const stateDirRel = ".git/pr-tui"

// repoRootFn is the function used to discover the git repository root.
// Tests override this to point at a temp dir; production uses
// git.RepoRoot which shells out to `git rev-parse --show-toplevel`.
var repoRootFn = git.RepoRoot

var validStateKey = regexp.MustCompile(`^[\w-]+$`)

// fileRename is a thin wrapper around os.Rename so tests can inject
// failures (eg. simulate EXDEV) and ensure the code falls back to
// copy+remove semantics. Defaults to os.Rename in production.
var fileRename = os.Rename

// stateDir returns the absolute path to prr's state directory rooted at
// the git repo. Before this fix the path was cwd-relative, so launching
// prr from different working directories produced different state
// locations — and a cached review created from one cwd would be
// invisible from another, silently appearing as "no review yet."
func stateDir() string {
	root, err := repoRootFn()
	if err != nil || root == "" {
		// Soft fallback: use cwd-relative path. The git-repo guard at
		// prr startup means this branch is reached only in odd cases
		// (e.g. audit snapshot helpers invoked from tests).
		return stateDirRel
	}
	return filepath.Join(root, stateDirRel)
}

// getStateFilePath returns the path to the state file for a given key.
// The key can be a PR number (e.g., "42") or a special identifier
// (e.g., "audit"). It creates the parent directory if it doesn't exist.
func getStateFilePath(key string) (string, error) {
	if !validStateKey.MatchString(key) {
		return "", fmt.Errorf("invalid state key: %q", key)
	}
	dir := stateDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create state directory: %w", err)
	}
	return filepath.Join(dir, fmt.Sprintf("%s.json", key)), nil
}

// migrateOldCwdRelativeState looks for a state file at the legacy
// cwd-relative path (`./.git/pr-tui/<key>.json`) and, if found, renames
// it to the canonical repo-root path. Idempotent — subsequent loads
// find the file at the new path directly. Preserves users' existing
// cached reviews instead of forcing a re-run.
//
// Returns (true, nil) when a migration occurred. Errors are
// non-fatal: the caller logs and proceeds as if no migration happened.
func migrateOldCwdRelativeState(key, newPath string) (bool, error) {
	if _, err := os.Stat(newPath); err == nil {
		// New path already populated — nothing to migrate.
		return false, nil
	}
	oldPath := filepath.Join(stateDirRel, fmt.Sprintf("%s.json", key))
	abs, _ := filepath.Abs(oldPath)
	absNew, _ := filepath.Abs(newPath)
	if abs == absNew {
		// We're already at the right cwd — old and new paths resolve
		// to the same file. No migration needed.
		return false, nil
	}
	if _, err := os.Stat(oldPath); err != nil {
		return false, nil
	}
    if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
        return false, fmt.Errorf("preparing migration target: %w", err)
    }

    // Try a fast rename first. If it fails, fall back to a copy+remove
    // so migration works across filesystems.
    if err := fileRename(oldPath, newPath); err == nil {
        return true, nil
    } else {
        inf, err := os.Open(oldPath)
        if err != nil {
            return false, fmt.Errorf("opening legacy state for copy: %w", err)
        }
        defer inf.Close()

        outf, err := os.Create(newPath)
        if err != nil {
            return false, fmt.Errorf("creating migrated state file: %w", err)
        }
        _, err = io.Copy(outf, inf)
        if cerr := outf.Close(); cerr != nil && err == nil {
            err = cerr
        }
        if err != nil {
            _ = os.Remove(newPath)
            return false, fmt.Errorf("copying legacy state: %w", err)
        }
        if err := os.Remove(oldPath); err != nil {
            return false, fmt.Errorf("copied legacy state but failed to remove original: %w", err)
        }
        return true, nil
    }
}

// Load reads the state for a given PR number from disk.
// If the file does not exist, it returns a new, empty State.
func Load(prNumber string) (*State, error) {
	filePath, err := getStateFilePath(prNumber)
	if err != nil {
		return nil, err
	}

	// One-shot migration from the legacy cwd-relative location.
	// Best-effort: if it fails, fall through to the normal load
	// (which will create a fresh state).
	if moved, mErr := migrateOldCwdRelativeState(prNumber, filePath); mErr != nil {
		log.Printf("state: migration probe failed (non-fatal): %v", mErr)
	} else if moved {
		log.Printf("state: migrated %s.json from legacy cwd-relative path to %s", prNumber, filePath)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewState(prNumber), nil
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	state := NewState(prNumber)
	if err := json.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("failed to parse state file: %w", err)
	}

	return state, nil
}

// Save writes the given State to disk.
func Save(state *State) error {
    // Determine file path before taking locks (dir creation is idempotent).
    filePath, err := getStateFilePath(state.PRNumber)
    if err != nil {
        return err
    }

    // Marshal under read lock to get a consistent snapshot without
    // holding the lock during disk I/O.
    state.mu.RLock()
    data, err := json.MarshalIndent(state, "", "  ")
    state.mu.RUnlock()
    if err != nil {
        return fmt.Errorf("failed to serialize state: %w", err)
    }

    // Ensure trailing newline for readability.
    if len(data) == 0 || data[len(data)-1] != '\n' {
        data = append(data, '\n')
    }

    dir := filepath.Dir(filePath)
    tmpFile, err := os.CreateTemp(dir, "prr-state-*.tmp")
    if err != nil {
        return fmt.Errorf("creating temp state file: %w", err)
    }
    tmpPath := tmpFile.Name()

    if _, err := tmpFile.Write(data); err != nil {
        tmpFile.Close()
        _ = os.Remove(tmpPath)
        return fmt.Errorf("writing temp state file: %w", err)
    }
    if err := tmpFile.Sync(); err != nil {
        log.Printf("Warning: fsync failed for temp state file %s: %v", tmpPath, err)
    }
    if err := tmpFile.Close(); err != nil {
        _ = os.Remove(tmpPath)
        return fmt.Errorf("closing temp state file: %w", err)
    }
    if err := os.Chmod(tmpPath, 0644); err != nil {
        log.Printf("Warning: chmod temp state file: %v", err)
    }

    // Try rename; if it fails, attempt a copy+remove fallback.
    if err := fileRename(tmpPath, filePath); err == nil {
        return nil
    } else {
        // Fallback copy final file from temp path.
        inf, err := os.Open(tmpPath)
        if err != nil {
            _ = os.Remove(tmpPath)
            return fmt.Errorf("opening temp for fallback copy: %w", err)
        }
        defer inf.Close()
        outf, err := os.Create(filePath)
        if err != nil {
            _ = os.Remove(tmpPath)
            return fmt.Errorf("creating final state file during fallback: %w", err)
        }
        if _, err := io.Copy(outf, inf); err != nil {
            outf.Close()
            _ = os.Remove(tmpPath)
            _ = os.Remove(filePath)
            return fmt.Errorf("fallback copying state file: %w", err)
        }
        if err := outf.Sync(); err != nil {
            log.Printf("Warning: fsync final state file: %v", err)
        }
        if err := outf.Close(); err != nil {
            _ = os.Remove(tmpPath)
            return fmt.Errorf("closing final state file during fallback: %w", err)
        }
        if err := os.Remove(tmpPath); err != nil {
            return fmt.Errorf("removing temp state file after fallback: %w", err)
        }
        return nil
    }
}

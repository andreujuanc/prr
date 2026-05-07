package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const stateDir = ".git/pr-tui"

var validStateKey = regexp.MustCompile(`^[\w-]+$`)

// getStateFilePath returns the path to the state file for a given key.
// The key can be a PR number (e.g., "42") or a special identifier (e.g., "audit").
// It creates the parent directory if it doesn't exist.
func getStateFilePath(key string) (string, error) {
	if !validStateKey.MatchString(key) {
		return "", fmt.Errorf("invalid state key: %q", key)
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create state directory: %w", err)
	}
	return filepath.Join(stateDir, fmt.Sprintf("%s.json", key)), nil
}

// Load reads the state for a given PR number from disk.
// If the file does not exist, it returns a new, empty State.
func Load(prNumber string) (*State, error) {
	filePath, err := getStateFilePath(prNumber)
	if err != nil {
		return nil, err
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
	state.mu.Lock()
	defer state.mu.Unlock()

	filePath, err := getStateFilePath(state.PRNumber)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize state: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

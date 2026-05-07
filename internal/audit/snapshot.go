package audit

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/andreujuanc/prr/internal/state"
)

const snapshotFile = "audit-snapshot.json"

// SaveSnapshot persists current findings as the "last audit" baseline.
// Stored in .git/pr-tui/audit-snapshot.json
func SaveSnapshot(repoRoot string, findings []state.DeepFinding) error {
	dir := filepath.Join(repoRoot, ".git", "pr-tui")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, snapshotFile), data, 0o644)
}

// LoadSnapshot loads the previous audit's findings snapshot.
// Returns nil slice (not error) if no previous snapshot exists.
func LoadSnapshot(repoRoot string) ([]state.DeepFinding, error) {
	path := filepath.Join(repoRoot, ".git", "pr-tui", snapshotFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var findings []state.DeepFinding
	if err := json.Unmarshal(data, &findings); err != nil {
		return nil, err
	}
	return findings, nil
}

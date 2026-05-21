package state

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Per-run snapshots live alongside the per-PR state file but in
// dedicated subdirectories so they don't pollute the existing layout.
//
//	.git/pr-tui/
//	├── <PR>.json              latest per-PR state (existing)
//	├── reviews/               one file per `prr review` run
//	└── audits/                one file per `prr audit` run
//
// Both directories sit under .git/, so they inherit git's "not tracked"
// status. Nothing to add to .gitignore.
const (
	reviewsSubdir = "reviews"
	auditsSubdir  = "audits"
)

// snapshotTimestamp returns a filename-safe UTC timestamp. Hyphens
// in place of colons so the path is safe on Windows and other
// filesystems that reject colons. Sortable as a string.
//
// Resolution is one second. Two snapshots written in the same second
// for the same PR will collide and the second one's atomic rename
// will overwrite the first. Realistic risk is low (a `prr review`
// run takes minutes), so we don't add sub-second precision here.
func snapshotTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15-04-05Z")
}

// ReviewsDir returns the absolute path to the reviews snapshot
// directory and ensures the directory exists. Used by callers that
// want to enumerate or clean past snapshots without writing a new
// one.
func ReviewsDir() (string, error) {
	dir := filepath.Join(stateDir(), reviewsSubdir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating reviews dir: %w", err)
	}
	return dir, nil
}

// AuditsDir returns the absolute path to the audits snapshot
// directory and ensures the directory exists.
func AuditsDir() (string, error) {
	dir := filepath.Join(stateDir(), auditsSubdir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating audits dir: %w", err)
	}
	return dir, nil
}

// SaveReviewSnapshot writes data (already-marshalled JSON bytes from
// the caller — state owns the path layout, not the schema) to
// .git/pr-tui/reviews/pr-<N>-review-<timestamp>.json. Returns the
// absolute path on success.
//
// Caller chooses the bytes. This is intentional: state.SaveReviewSnapshot
// knows where files live; the review package knows what shape they take.
//
// prNumber is validated against the same charset Save() uses for state
// keys (no path separators, no shell metacharacters).
func SaveReviewSnapshot(prNumber string, data []byte) (string, error) {
	if !validStateKey.MatchString(prNumber) {
		return "", fmt.Errorf("invalid PR number for snapshot: %q", prNumber)
	}
	dir, err := ReviewsDir()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("pr-%s-review-%s.json", prNumber, snapshotTimestamp())
	return writeSnapshot(dir, name, data)
}

// SaveAuditSnapshot writes data to
// .git/pr-tui/audits/audit-<timestamp>.json. Returns the absolute
// path on success.
func SaveAuditSnapshot(data []byte) (string, error) {
	dir, err := AuditsDir()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("audit-%s.json", snapshotTimestamp())
	return writeSnapshot(dir, name, data)
}

// writeSnapshot writes data to dir/name using the same temp-file +
// fsync + chmod 0644 + rename pattern Save() uses. Reuses the
// package saveMu so concurrent snapshot writes serialise alongside
// regular Save() calls — important because the audit pipeline saves
// state and snapshots from the same goroutines.
func writeSnapshot(dir, name string, data []byte) (string, error) {
	saveMu.Lock()
	defer saveMu.Unlock()

	finalPath := filepath.Join(dir, name)
	tmpFile, err := os.CreateTemp(dir, "prr-snapshot-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating temp snapshot file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("writing temp snapshot file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		log.Printf("Warning: fsync failed for temp snapshot file %s: %v", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("closing temp snapshot file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		log.Printf("Warning: chmod temp snapshot file: %v", err)
	}
	if err := fileRename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("renaming snapshot file: %w", err)
	}
	return finalPath, nil
}

// ReviewSnapshot mirrors the JSON shape written by
// review.MarshalResultJSON. Single source of truth for "what one
// review run produced" — the TUI hydrates from this on open so a
// headless `prr review` and a TUI-triggered review both surface the
// same way.
type ReviewSnapshot struct {
	PRNumber      int           `json:"pr_number,omitempty"`
	PRTitle       string        `json:"pr_title,omitempty"`
	FilesReviewed int           `json:"files_reviewed,omitempty"`
	Review        *ReviewOutput `json:"review,omitempty"`
	DeepFindings  []DeepFinding `json:"deep_findings,omitempty"`
}

// LatestReviewSnapshot returns the most recent snapshot for the given
// PR, picked by the timestamp embedded in the filename (sortable as
// a string by construction). Returns (nil, "", nil) when no snapshot
// exists — callers treat that as "no prior review."
//
// Filename pattern: pr-<N>-review-<timestamp>.json. We rely on the
// timestamp being lexically sortable so this stays O(n) without
// stat'ing every file.
func LatestReviewSnapshot(prNumber string) (*ReviewSnapshot, string, error) {
	if !validStateKey.MatchString(prNumber) {
		return nil, "", fmt.Errorf("invalid PR number: %q", prNumber)
	}
	dir, err := ReviewsDir()
	if err != nil {
		return nil, "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", fmt.Errorf("listing reviews dir: %w", err)
	}
	prefix := fmt.Sprintf("pr-%s-review-", prNumber)
	var matching []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".json") {
			matching = append(matching, name)
		}
	}
	if len(matching) == 0 {
		return nil, "", nil
	}
	sort.Strings(matching)
	latestPath := filepath.Join(dir, matching[len(matching)-1])
	data, err := os.ReadFile(latestPath)
	if err != nil {
		return nil, latestPath, fmt.Errorf("reading snapshot %s: %w", latestPath, err)
	}
	var snap ReviewSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, latestPath, fmt.Errorf("parsing snapshot %s: %w", latestPath, err)
	}
	return &snap, latestPath, nil
}

package audit

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// CollectFiles returns the list of files to audit after applying all filters.
// It runs `git ls-files` and filters through the deterministic exclusion pipeline.
func CollectFiles(repoRoot string, excludePatterns, includePatterns []string) ([]string, error) {
	allFiles, err := listTrackedFiles(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("listing tracked files: %w", err)
	}

	var result []string
	for _, path := range allFiles {
		// Force-include overrides exclusion
		if ShouldForceInclude(path, includePatterns) {
			result = append(result, path)
			continue
		}

		// Apply exclusion filters
		if ShouldExcludeFromAuditWithCustom(path, excludePatterns) {
			continue
		}

		result = append(result, path)
	}

	sort.Strings(result)
	return result, nil
}

// listTrackedFiles returns all git-tracked files relative to the repo root.
func listTrackedFiles(repoRoot string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--cached", "--exclude-standard")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var files []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Normalize path separators
		files = append(files, filepath.ToSlash(line))
	}
	return files, nil
}

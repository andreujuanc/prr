package config

import (
	"os"
	"path/filepath"
	"unicode/utf8"
)

// instructionSearchPaths are the file paths to search for custom review instructions,
// in priority order. The first one found wins.
var instructionSearchPaths = []string{
	".prr/instructions.md",
	".github/prr-instructions.md",
}

// maxInstructionBytes is the maximum size of custom instructions to load.
// Content is truncated at a clean rune boundary at or below this limit.
const maxInstructionBytes = 4000

// LoadCustomInstructions searches for a custom instructions file in the repo root
// and returns its contents. Returns empty string if no file is found.
//
// Search order:
//  1. .prr/instructions.md
//  2. .github/prr-instructions.md
func LoadCustomInstructions() string {
	// Find repo root via git
	root := findRepoRoot()
	if root == "" {
		return ""
	}

	for _, rel := range instructionSearchPaths {
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > maxInstructionBytes {
			// Truncate at a clean rune boundary to avoid splitting
			// multi-byte UTF-8 characters.
			content = content[:maxInstructionBytes]
			for len(content) > 0 && !utf8.ValidString(content) {
				content = content[:len(content)-1]
			}
		}
		return content
	}

	return ""
}

// findRepoRoot returns the git repository root directory,
// or empty string if not in a git repo.
func findRepoRoot() string {
	// Walk up from current directory looking for .git
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached filesystem root
		}
		dir = parent
	}
}

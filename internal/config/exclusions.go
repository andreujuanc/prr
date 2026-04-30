package config

import (
	"path/filepath"
	"strings"
)

// defaultExcludePatterns are file patterns excluded from AI review by default.
// These are files that produce noise without useful review findings:
// lock files, generated code, vendored dependencies, minified assets.
var defaultExcludePatterns = []string{
	// Lock files
	"go.sum",
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"Gemfile.lock",
	"Pipfile.lock",
	"poetry.lock",
	"composer.lock",
	"Cargo.lock",

	// Vendored dependencies
	"vendor/**",
	"node_modules/**",

	// Generated code
	"*.generated.go",
	"*.gen.go",
	"*_generated.go",
	"*.pb.go",

	// Minified/bundled assets
	"*.min.js",
	"*.min.css",
	"*.bundle.js",

	// Binary/non-reviewable files
	"*.svg",
	"*.png",
	"*.jpg",
	"*.jpeg",
	"*.gif",
	"*.ico",
	"*.woff",
	"*.woff2",
	"*.ttf",
	"*.eot",
}

// ShouldExcludeFromReview returns true if the given file path should be
// excluded from AI review based on default exclusion patterns.
func ShouldExcludeFromReview(path string) bool {
	base := filepath.Base(path)

	for _, pattern := range defaultExcludePatterns {
		// Check if pattern uses directory glob (e.g. "vendor/**")
		if strings.Contains(pattern, "/") {
			if matched, _ := filepath.Match(pattern, path); matched {
				return true
			}
			// Also check if path starts with the directory prefix
			dirPrefix := strings.TrimSuffix(pattern, "/**")
			if strings.HasPrefix(path, dirPrefix+"/") {
				return true
			}
			continue
		}

		// Simple filename/extension pattern (e.g. "go.sum", "*.min.js")
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
	}

	return false
}

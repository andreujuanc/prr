package audit

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/andreujuanc/prr/internal/config"
)

// auditExcludePatterns are additional patterns excluded in audit mode
// beyond the standard review exclusions. These filter guaranteed noise
// from full-project scans — files that are never worth auditing.
//
// The existing config.ShouldExcludeFromReview() handles:
//   - Lock files (go.sum, package-lock.json, etc.)
//   - Vendored deps (vendor/, node_modules/)
//   - Generated code (*.pb.go, *.gen.go, etc.)
//   - Minified assets (*.min.js, *.min.css)
//   - Binary/media (*.png, *.jpg, *.svg, *.woff, etc.)
//
// This list adds audit-specific exclusions:
var auditExcludePatterns = []string{
	// Test files
	"*_test.go",
	"*.test.ts",
	"*.test.js",
	"*.test.tsx",
	"*.test.jsx",
	"*.spec.ts",
	"*.spec.js",
	"*.spec.tsx",
	"*.spec.jsx",
	"*_test.py",
	"test_*.py",
	"*Test.java",
	"*_spec.rb",

	// Test infrastructure
	"testdata/**",
	"__tests__/**",
	"__mocks__/**",
	"fixtures/**",
	"test_helpers/**",
	"conftest.py",

	// More generated code
	"*.generated.*",
	"*.d.ts",

	// Build artifacts
	"dist/**",
	"build/**",
	".next/**",
	"target/**",
	"out/**",
	"_build/**",

	// Documentation & non-code
	"*.md",
	"*.txt",
	"*.rst",
	"LICENSE*",
	"CHANGELOG*",
	"CONTRIBUTING*",
	"AUTHORS*",

	// Config files (non-logic)
	".gitignore",
	".gitattributes",
	".editorconfig",
	".prettierrc",
	".prettierrc.*",
	".eslintrc",
	".eslintrc.*",
	".stylelintrc",
	".stylelintrc.*",
	"tsconfig.json",
	"jsconfig.json",
	".babelrc",
	".babelrc.*",

	// IDE & tooling
	".vscode/**",
	".idea/**",
	".github/**",
	".circleci/**",

	// Assets
	"*.mp4",
	"*.mp3",
	"*.wav",
	"*.pdf",
	"*.zip",
	"*.tar",
	"*.gz",

	// Misc
	"*.map",
}

// ShouldExcludeFromAudit returns true if the given file path should be
// excluded from audit mode review. Checks both the standard review
// exclusions and the audit-specific exclusions.
func ShouldExcludeFromAudit(path string) bool {
	// Standard review exclusions first
	if config.ShouldExcludeFromReview(path) {
		return true
	}
	return matchesAuditPatterns(path, auditExcludePatterns)
}

// ShouldExcludeFromAuditWithCustom checks against standard, audit,
// and user-provided custom exclusion patterns.
func ShouldExcludeFromAuditWithCustom(path string, customPatterns []string) bool {
	if ShouldExcludeFromAudit(path) {
		return true
	}
	return matchesAuditPatterns(path, customPatterns)
}

// matchesAuditPatterns checks if a path matches any of the given glob patterns.
func matchesAuditPatterns(path string, patterns []string) bool {
	base := filepath.Base(path)

	for _, pattern := range patterns {
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}

		// Directory glob (e.g. "vendor/**", "__tests__/**")
		if strings.Contains(pattern, "/") {
			if matched, _ := filepath.Match(pattern, path); matched {
				return true
			}
			dirPrefix := strings.TrimSuffix(pattern, "/**")
			if dirPrefix != pattern && strings.HasPrefix(path, dirPrefix+"/") {
				return true
			}
			// Also try matching the pattern against parent dirs
			if strings.HasSuffix(pattern, "/**") {
				dirName := strings.TrimSuffix(pattern, "/**")
				// Check if any path component matches
				parts := strings.Split(path, "/")
				for _, part := range parts[:len(parts)-1] { // skip filename
					if part == dirName {
						return true
					}
				}
			}
			continue
		}

		// Simple filename/extension pattern
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
	}

	return false
}

// ShouldForceInclude returns true if the path matches any --include pattern.
// Force-included files override exclusion filters.
func ShouldForceInclude(path string, includePatterns []string) bool {
	if len(includePatterns) == 0 {
		return false
	}
	base := filepath.Base(path)
	for _, pattern := range includePatterns {
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, path); matched {
			return true
		}
	}
	return false
}

// LoadExcludeFile reads a glob-per-line exclude file (like .prr/audit-exclude).
// Blank lines and lines starting with # are skipped.
func LoadExcludeFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns, scanner.Err()
}

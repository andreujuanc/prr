package git

import (
	"path/filepath"
	"strings"
)

// maxDiffSizeForAI is the maximum raw diff size (in bytes) before a file
// is considered too large for AI review.
const maxDiffSizeForAI = 100 * 1024 // 100 KB

// SkipReason describes why a file was excluded from AI review.
type SkipReason string

const (
	SkipBinary    SkipReason = "binary"
	SkipGenerated SkipReason = "generated"
	SkipLarge     SkipReason = "large"
)

// binaryExtensions are file extensions that are always binary/non-reviewable.
var binaryExtensions = map[string]bool{
	// Images
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".bmp": true, ".ico": true, ".webp": true,
	".tiff": true, ".tif": true, ".avif": true,
	// Fonts
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	// Archives
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true,
	".rar": true, ".7z": true, ".zst": true, ".tgz": true,
	// Compiled / binary
	".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".o": true, ".a": true, ".pyc": true, ".pyo": true,
	".wasm": true, ".class": true,
	// Media
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true,
	".wav": true, ".flac": true, ".ogg": true, ".webm": true,
	// Documents (binary formats)
	".pdf": true, ".doc": true, ".docx": true,
	".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
	// Databases
	".sqlite": true, ".db": true,
}

// generatedFiles are exact base filenames that are generated/lock files.
var generatedFiles = map[string]bool{
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"go.sum":            true,
	"Cargo.lock":        true,
	"Gemfile.lock":      true,
	"composer.lock":     true,
	"poetry.lock":       true,
	"Pipfile.lock":      true,
	"flake.lock":        true,
	"bun.lockb":         true,
}

// generatedSuffixes are file name suffixes indicating generated/minified content.
var generatedSuffixes = []string{
	".min.js",
	".min.css",
	".min.map",
	".bundle.js",
	".bundle.css",
	".generated.go",
	".pb.go",
	".gen.go",
	".gen.ts",
}

// ShouldSkipForAI determines whether a file should be excluded from AI review.
// It checks (in order): git binary marker, extension blocklist, known
// generated/lock files, generated suffixes, and diff size threshold.
// Returns skip=true with a reason if the file should be skipped.
func ShouldSkipForAI(path string, rawDiff string) (skip bool, reason SkipReason) {
	// 1. Git binary marker — git diff outputs "Binary files ... differ"
	if isBinaryDiff(rawDiff) {
		return true, SkipBinary
	}

	// 2. Extension blocklist
	ext := strings.ToLower(filepath.Ext(path))
	if binaryExtensions[ext] {
		return true, SkipBinary
	}

	// 3. Lock/generated files by exact name
	base := filepath.Base(path)
	if generatedFiles[base] {
		return true, SkipGenerated
	}

	// 4. Generated file suffixes
	lowerBase := strings.ToLower(base)
	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(lowerBase, suffix) {
			return true, SkipGenerated
		}
	}

	// 5. Diff size threshold
	if len(rawDiff) > maxDiffSizeForAI {
		return true, SkipLarge
	}

	return false, ""
}

// isBinaryDiff checks whether a raw diff contains git's binary file marker.
// Git outputs lines like "Binary files /dev/null and b/image.png differ"
// or "Binary files a/old.png and b/new.png differ".
func isBinaryDiff(rawDiff string) bool {
	// Check each line for the binary marker prefix
	for _, line := range strings.SplitAfter(rawDiff, "\n") {
		if strings.HasPrefix(line, "Binary files ") {
			return true
		}
	}
	// Also check for git binary patch format
	return strings.Contains(rawDiff, "\nGIT binary patch\n")
}

package audit

import (
	"bytes"
	"path/filepath"
	"strings"
)

// MaxAuditFileBytes is the per-file size cap for audit ingestion. Files
// larger than this are skipped with a warning rather than loaded into
// memory and shipped to the classifier/AOI scanner — a single 10MB
// JSON config can balloon prompt sizes and offers no real audit value.
//
// 512KB comfortably accommodates hand-written code (the largest .go
// files in stdlib are ~300KB) while still catching generated artifacts
// and accidentally-committed data dumps. Audit.Options.MaxFileBytes
// overrides this at the call site.
const MaxAuditFileBytes = 512 * 1024

// binarySniffLimit bounds the prefix scanned by IsBinary. 8KB matches
// what git's diff machinery and ripgrep use — large enough that a
// clean window strongly implies text (binary formats put NUL bytes in
// their headers within the first few hundred bytes), small enough to
// be a single cheap read.
const binarySniffLimit = 8192

// IsBinary returns true if content appears to be a binary file. The
// heuristic: if any of the first binarySniffLimit bytes is NUL (0x00),
// treat as binary. Valid text encodings (UTF-8, ASCII, ISO-8859-*)
// never produce 0x00 in real content; binaries almost always have NUL
// bytes in headers, length-prefixed sections, or padding.
//
// Known false positives: UTF-16/UTF-32 source files (vanishingly rare
// for code). Users can force-include via --include if needed.
func IsBinary(content []byte) bool {
	limit := min(len(content), binarySniffLimit)
	return bytes.IndexByte(content[:limit], 0x00) >= 0
}

// transientUntrackedPatterns flags files that are commonly produced by
// local tooling (logs, debug dumps, state snapshots) and accidentally
// picked up by `git ls-files --others --exclude-standard` when the
// user hasn't gitignored them yet. Auditing these is pure noise.
//
// We don't auto-exclude — that would mask legitimate code matching
// these patterns. Instead the caller surfaces a one-line warning so
// the user can decide whether to gitignore.
var transientUntrackedPatterns = []string{
	"*.log",
	"*.tmp",
	"*-debug.log",
	"*-debug-*.log",
	"prr-*.json",
	"prr-*-*.log",
}

// IsTransientUntracked reports whether path looks like a locally
// generated artifact that should probably be in .gitignore. Only
// meaningful for files that came from `git ls-files --others`.
func IsTransientUntracked(path string) bool {
	base := filepath.Base(path)
	for _, pat := range transientUntrackedPatterns {
		if matched, _ := filepath.Match(pat, base); matched {
			return true
		}
	}
	// Catch-all: anything containing "-debug-" or "-headless-" in the
	// filename is almost certainly a transient dump.
	lower := strings.ToLower(base)
	if strings.Contains(lower, "-debug-") || strings.Contains(lower, "-headless-") {
		return true
	}
	return false
}

package audit

import (
	"bytes"
	"testing"
)

func TestIsBinary_NulByte(t *testing.T) {
	// A single NUL anywhere in the sniff window flips the verdict.
	content := append([]byte("hello world\n"), 0x00, 'm', 'o', 'r', 'e')
	if !IsBinary(content) {
		t.Error("expected NUL-containing content to be classified as binary")
	}
}

func TestIsBinary_NulByteAtStart(t *testing.T) {
	// Common case: ELF/Mach-O/PNG headers have NULs in the first ~16 bytes.
	content := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00, 0x00, 0x00}
	if !IsBinary(content) {
		t.Error("expected ELF-like header to be classified as binary")
	}
}

func TestIsBinary_PlainText(t *testing.T) {
	// Real source code has no NUL bytes.
	content := []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n")
	if IsBinary(content) {
		t.Error("plain Go source must not be classified as binary")
	}
}

func TestIsBinary_EmptyContent(t *testing.T) {
	// Empty is not binary — empty-file filtering is a separate concern.
	if IsBinary([]byte{}) {
		t.Error("empty content should not be classified as binary")
	}
}

func TestIsBinary_NulOutsideSniffWindow(t *testing.T) {
	// A NUL byte past the sniff limit is intentionally ignored. The
	// heuristic trades a small false-negative window for bounded cost.
	content := append(bytes.Repeat([]byte("a"), binarySniffLimit), 0x00)
	if IsBinary(content) {
		t.Error("NUL byte past binarySniffLimit should not trigger binary classification")
	}
}

func TestIsBinary_NulAtSniffBoundary(t *testing.T) {
	// NUL at the last byte of the sniff window must still be caught —
	// off-by-one in the slice bound would miss this.
	content := append(bytes.Repeat([]byte("a"), binarySniffLimit-1), 0x00)
	if !IsBinary(content) {
		t.Error("NUL at last byte of sniff window should be caught")
	}
}

func TestIsTransientUntracked(t *testing.T) {
	tests := []struct {
		path      string
		transient bool
	}{
		// Hits — these are exactly the kinds of files that
		// `git ls-files --others` picks up when the user forgets to
		// gitignore local tooling output.
		{"prr-debug.log", true},
		{"prr-state-after-review.json", true},
		{"prr-headless-1107.log", true},
		{"prr-review-1107.log", true},
		{"audit-debug.log", true},
		{"foo.tmp", true},
		{"some-debug-output.txt", true}, // catch-all "-debug-"
		{"prr-headless-thing.log", true},

		// Misses — legitimate code/config files. False positives
		// here would silently drop real code, so this list matters.
		{"internal/audit/pipeline.go", false},
		{"main.go", false},
		{"README.md", false},
		{"package.json", false},
		{"log.go", false}, // contains "log" but isn't a *.log file
		{"audit.md", false},
	}

	for _, tc := range tests {
		got := IsTransientUntracked(tc.path)
		if got != tc.transient {
			t.Errorf("IsTransientUntracked(%q) = %v, want %v", tc.path, got, tc.transient)
		}
	}
}

func TestMaxAuditFileBytes_Sane(t *testing.T) {
	// Pin the constant to catch accidental changes that would either
	// admit huge binary dumps (too high) or reject real source files
	// (too low). The largest hand-written file in this repo's history
	// is well under 200KB; 512KB gives headroom without inviting
	// data-dump ingestion.
	if MaxAuditFileBytes < 256*1024 {
		t.Errorf("MaxAuditFileBytes=%d is too low; would reject large but legitimate source files", MaxAuditFileBytes)
	}
	if MaxAuditFileBytes > 2*1024*1024 {
		t.Errorf("MaxAuditFileBytes=%d is too high; would admit data dumps and balloon prompt costs", MaxAuditFileBytes)
	}
}

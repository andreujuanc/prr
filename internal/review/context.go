package review

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// DefaultAOIContextLines is the number of source lines rendered on
// each side of an AOI's line range in audit mode.
const DefaultAOIContextLines = 10

// AttachFileDiffs populates ReviewCall.FileDiffs for every call in the
// slice, using the rawDiffs map (path -> unified diff). Used by PR mode
// before review execution so the model can see the changed lines in
// the prompt without a tool call. Files in a call that are missing
// from rawDiffs are skipped silently — the prompt builder handles
// missing entries by omitting that file's section.
func AttachFileDiffs(calls []ReviewCall, rawDiffs map[string]string) {
	if len(calls) == 0 || len(rawDiffs) == 0 {
		return
	}
	for i := range calls {
		diffs := make(map[string]string, len(calls[i].Files))
		for _, f := range calls[i].Files {
			if d, ok := rawDiffs[f]; ok && d != "" {
				diffs[f] = d
			}
		}
		if len(diffs) > 0 {
			calls[i].FileDiffs = diffs
		}
	}
}

// AttachAOISources populates ReviewCall.AOISources for every call
// in the slice. For each AOI, reads its file from repoRoot and slices
// `contextLines` lines on each side of the AOI's line range. Used by
// audit mode (no diff exists) so the model has surrounding source in
// the prompt without a mandatory read_file call.
//
// Files are read once and cached for the duration of the call. A read
// failure for a single file is non-fatal — that AOI's context is left
// empty and the prompt falls back to "no inline source; use tools."
//
// If contextLines <= 0, DefaultAOIContextLines is used.
func AttachAOISources(calls []ReviewCall, repoRoot string, contextLines int) {
	if len(calls) == 0 {
		return
	}
	if contextLines <= 0 {
		contextLines = DefaultAOIContextLines
	}

	fileLines := make(map[string][]string)
	readFile := func(path string) []string {
		if lines, ok := fileLines[path]; ok {
			return lines
		}
		full := path
		if repoRoot != "" && !filepath.IsAbs(path) {
			full = filepath.Join(repoRoot, path)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			log.Printf("AttachAOISources: cannot read %s (deep review will fall back to read_file tool): %v", full, err)
			fileLines[path] = nil
			return nil
		}
		// Trim a single trailing newline so files ending in "\n"
		// don't yield a phantom blank last line in the slice.
		content := strings.TrimSuffix(string(data), "\n")
		lines := strings.Split(content, "\n")
		fileLines[path] = lines
		return lines
	}

	for i := range calls {
		contexts := make([]string, len(calls[i].AOIs))
		for j, aoi := range calls[i].AOIs {
			lines := readFile(aoi.File)
			if lines == nil {
				continue
			}
			contexts[j] = sliceSourceAroundAOI(lines, aoi.Line, aoi.EndLine, contextLines)
		}
		calls[i].AOISources = contexts
	}
}

// sliceSourceAroundAOI returns a numbered source slice covering the
// AOI's line range plus `context` lines on each side, clamped to the
// file's bounds. Line numbers are 1-based as they appear in tools and
// editors. Returns empty string if line numbers are invalid.
func sliceSourceAroundAOI(lines []string, startLine, endLine, context int) string {
	if startLine <= 0 || len(lines) == 0 {
		return ""
	}
	if endLine < startLine {
		endLine = startLine
	}

	first := startLine - context
	if first < 1 {
		first = 1
	}
	last := endLine + context
	if last > len(lines) {
		last = len(lines)
	}
	if first > last {
		return ""
	}

	var b strings.Builder
	width := numWidth(last)
	for n := first; n <= last; n++ {
		fmt.Fprintf(&b, "%*d  %s\n", width, n, lines[n-1])
	}
	return b.String()
}

func numWidth(n int) int {
	if n < 10 {
		return 1
	}
	w := 0
	for n > 0 {
		w++
		n /= 10
	}
	return w
}

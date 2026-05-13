// Package dbg provides formatted debug output for the audit and review
// pipelines. When disabled, all methods are no-ops so debug-instrumented
// code paths stay zero-cost in production.
package dbg

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Writer handles formatted debug output. Zero value is invalid; use New.
type Writer struct {
	enabled bool
	w       io.Writer

	// elideFileBlocks, when true (default), collapses embedded file
	// content blocks in LLM prompts to just their headers. The AOI
	// scanner and similar phases build prompts like
	//   === path/to/file.go ===
	//   <hundreds of lines of diff>
	// and the contents drown out the rest of the debug log. Elision
	// preserves the path reference and a line count so a reader can
	// still trace which files were involved.
	elideFileBlocks bool
}

// New creates a debug writer. If enabled is false, all output is
// suppressed. Output goes to stderr by default; use SetOutput to
// redirect to a file.
//
// File-block eliding is on by default — see SetVerbose.
func New(enabled bool) *Writer {
	return &Writer{enabled: enabled, w: os.Stderr, elideFileBlocks: true}
}

// SetOutput redirects debug output to the given writer.
func (d *Writer) SetOutput(w io.Writer) { d.w = w }

// SetVerbose disables file-block eliding so Prompt renders full file
// contents inside prompts. Default is non-verbose — embedded file
// contents are collapsed to "=== path === [N lines elided]" headers.
func (d *Writer) SetVerbose(verbose bool) { d.elideFileBlocks = !verbose }

// Enabled reports whether debug output is active. Useful for callers
// that want to skip expensive formatting work entirely.
func (d *Writer) Enabled() bool { return d != nil && d.enabled }

// Phase prints a phase header.
func (d *Writer) Phase(name string) {
	if !d.Enabled() {
		return
	}
	fmt.Fprintf(d.w, "\n%s\n", strings.Repeat("═", 70))
	fmt.Fprintf(d.w, "  %s\n", name)
	fmt.Fprintf(d.w, "%s\n\n", strings.Repeat("═", 70))
}

// Section prints a section header within a phase.
func (d *Writer) Section(name string) {
	if !d.Enabled() {
		return
	}
	fmt.Fprintf(d.w, "─── %s ───\n", name)
}

// Text prints arbitrary text.
func (d *Writer) Text(format string, args ...interface{}) {
	if !d.Enabled() {
		return
	}
	fmt.Fprintf(d.w, format+"\n", args...)
}

// Prompt prints a full LLM prompt (system + user messages). When the
// writer's elideFileBlocks is on (default), embedded `=== <path> ===`
// file-content blocks in either message are collapsed to a header +
// line-count line. This keeps the debug log scannable on large audits.
func (d *Writer) Prompt(systemPrompt string, userMessage string) {
	if !d.Enabled() {
		return
	}
	d.Section("System Prompt")
	fmt.Fprintf(d.w, "%s\n\n", d.maybeElide(systemPrompt))
	d.Section("User Message")
	fmt.Fprintf(d.w, "%s\n\n", d.maybeElide(userMessage))
}

func (d *Writer) maybeElide(s string) string {
	if !d.elideFileBlocks {
		return s
	}
	return elideFileBlocks(s)
}

// elideFileBlocks collapses `=== <path> ===` blocks in a prompt's
// body. Pattern: a line matching `=== <path> ===` followed by content
// lines until the next such marker (or end of text). The content
// lines are replaced with a single `[<N> lines elided]` annotation.
//
// Blocks with ≤3 content lines pass through unchanged — collapsing
// them gains nothing and loses signal.
func elideFileBlocks(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		if isFileMarker(line) {
			// Find the next marker (or EOF) to delimit the block.
			j := i + 1
			for j < len(lines) && !isFileMarker(lines[j]) {
				j++
			}
			content := lines[i+1 : j]
			// Trim trailing blank lines so the elision count
			// reflects the real content size.
			n := len(content)
			for n > 0 && strings.TrimSpace(content[n-1]) == "" {
				n--
			}
			if n > 3 {
				out = append(out, fmt.Sprintf("%s  [%d lines elided]", line, n))
				out = append(out, "")
				i = j
				continue
			}
		}
		out = append(out, line)
		i++
	}
	return strings.Join(out, "\n")
}

// isFileMarker tests for the `=== <path> ===` pattern used to demarcate
// file-content blocks in AOI scan prompts (and any other site that
// adopts the same convention).
func isFileMarker(line string) bool {
	if !strings.HasPrefix(line, "=== ") || !strings.HasSuffix(line, " ===") {
		return false
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(line, "=== "), " ===")
	return mid != ""
}

// Response prints a raw LLM response.
func (d *Writer) Response(raw string) {
	if !d.Enabled() {
		return
	}
	d.Section("Raw Response")
	fmt.Fprintf(d.w, "%s\n\n", raw)
}

// Separator prints a visual separator.
func (d *Writer) Separator() {
	if !d.Enabled() {
		return
	}
	fmt.Fprintf(d.w, "\n%s\n\n", strings.Repeat("─", 50))
}

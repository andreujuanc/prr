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

	// verbose controls debug-output detail.
	//
	// false (default, "compact"):
	//   - Prompt() skips the System Prompt section entirely. The
	//     system prompt is static .md content embedded at build
	//     time — printing it on every LLM call is huge and
	//     redundant. Inspect the .md source if you need to see it.
	//   - The User Message section runs embedded `=== <path> ===`
	//     file-content blocks through the eliding transformer so
	//     diffs collapse to "[N lines elided]" headers.
	//
	// true ("verbose", opt-in via SetVerbose or PRR_DEBUG_VERBOSE):
	//   - Prompt() prints the full system prompt.
	//   - File-content blocks pass through verbatim.
	//
	// Response output is unaffected — raw LLM responses always
	// print full, since they're what you usually want to inspect.
	verbose bool
}

// New creates a debug writer. If enabled is false, all output is
// suppressed. Output goes to stderr by default; use SetOutput to
// redirect to a file.
//
// Compact mode is on by default. Setting the PRR_DEBUG_VERBOSE
// environment variable (to any non-empty value) makes the writer
// verbose at construction — useful for one-shot debug runs without
// touching code.
func New(enabled bool) *Writer {
	w := &Writer{enabled: enabled, w: os.Stderr, verbose: false}
	if os.Getenv("PRR_DEBUG_VERBOSE") != "" {
		w.verbose = true
	}
	return w
}

// SetOutput redirects debug output to the given writer.
func (d *Writer) SetOutput(w io.Writer) { d.w = w }

// SetVerbose toggles compact (false, default) vs verbose (true) mode.
// See the Writer doc comment for what each mode includes.
func (d *Writer) SetVerbose(verbose bool) { d.verbose = verbose }

// Verbose reports the current mode. Useful for callers that want to
// branch decisions on compact-vs-verbose.
func (d *Writer) Verbose() bool { return d.verbose }

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

// Prompt prints an LLM prompt. In compact mode (default) the system
// prompt is skipped entirely — it's static .md content reprinted on
// every LLM call and adds 100+ lines of noise per call. Only the
// User Message is printed, with embedded `=== <path> ===` file-content
// blocks collapsed to header-only.
//
// In verbose mode (SetVerbose(true) or PRR_DEBUG_VERBOSE env var)
// the full system prompt is included and file blocks pass through
// verbatim — useful when debugging prompt content specifically.
func (d *Writer) Prompt(systemPrompt string, userMessage string) {
	if !d.Enabled() {
		return
	}
	if d.verbose {
		d.Section("System Prompt")
		fmt.Fprintf(d.w, "%s\n\n", systemPrompt)
	}
	d.Section("User Message")
	fmt.Fprintf(d.w, "%s\n\n", d.maybeElide(userMessage))
}

func (d *Writer) maybeElide(s string) string {
	if d.verbose {
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

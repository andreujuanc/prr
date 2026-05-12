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
}

// New creates a debug writer. If enabled is false, all output is
// suppressed. Output goes to stderr by default; use SetOutput to
// redirect to a file.
func New(enabled bool) *Writer {
	return &Writer{enabled: enabled, w: os.Stderr}
}

// SetOutput redirects debug output to the given writer.
func (d *Writer) SetOutput(w io.Writer) { d.w = w }

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

// Prompt prints a full LLM prompt (system + user messages).
func (d *Writer) Prompt(systemPrompt string, userMessage string) {
	if !d.Enabled() {
		return
	}
	d.Section("System Prompt")
	fmt.Fprintf(d.w, "%s\n\n", systemPrompt)
	d.Section("User Message")
	fmt.Fprintf(d.w, "%s\n\n", userMessage)
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

package audit

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// DebugWriter handles formatted debug output for the audit pipeline.
// When disabled, all methods are no-ops.
type DebugWriter struct {
	enabled bool
	w       io.Writer
}

// NewDebugWriter creates a debug writer. If enabled is false, all output is suppressed.
// Output goes to stderr by default; use SetOutput to redirect to a file.
func NewDebugWriter(enabled bool) *DebugWriter {
	return &DebugWriter{enabled: enabled, w: os.Stderr}
}

// SetOutput redirects debug output to the given writer.
func (d *DebugWriter) SetOutput(w io.Writer) {
	d.w = w
}

// Phase prints a phase header.
func (d *DebugWriter) Phase(name string) {
	if !d.enabled {
		return
	}
	fmt.Fprintf(d.w, "\n%s\n", strings.Repeat("═", 70))
	fmt.Fprintf(d.w, "  %s\n", name)
	fmt.Fprintf(d.w, "%s\n\n", strings.Repeat("═", 70))
}

// Section prints a section header within a phase.
func (d *DebugWriter) Section(name string) {
	if !d.enabled {
		return
	}
	fmt.Fprintf(d.w, "─── %s ───\n", name)
}

// Text prints arbitrary text.
func (d *DebugWriter) Text(format string, args ...interface{}) {
	if !d.enabled {
		return
	}
	fmt.Fprintf(d.w, format+"\n", args...)
}

// Prompt prints a full LLM prompt (system + user messages).
func (d *DebugWriter) Prompt(systemPrompt string, userMessage string) {
	if !d.enabled {
		return
	}
	d.Section("System Prompt")
	fmt.Fprintf(d.w, "%s\n\n", systemPrompt)
	d.Section("User Message")
	fmt.Fprintf(d.w, "%s\n\n", userMessage)
}

// Response prints a raw LLM response.
func (d *DebugWriter) Response(raw string) {
	if !d.enabled {
		return
	}
	d.Section("Raw Response")
	fmt.Fprintf(d.w, "%s\n\n", raw)
}

// Separator prints a visual separator.
func (d *DebugWriter) Separator() {
	if !d.enabled {
		return
	}
	fmt.Fprintf(d.w, "\n%s\n\n", strings.Repeat("─", 50))
}

// Enabled returns whether debug output is active.
func (d *DebugWriter) Enabled() bool {
	return d.enabled
}

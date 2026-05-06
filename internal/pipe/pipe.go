package pipe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Target describes an external process to pipe findings to.
type Target struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Format  string   `json:"format"` // "json", "markdown", "text"
}

// Payload is the data sent to an external process via stdin.
type Payload struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Suggestion string `json:"suggestion,omitempty"`
	RepoRoot   string `json:"repo_root"`
}

const pipeTimeout = 60 * time.Second

// Execute runs the target command with the payload piped to stdin.
// Returns the combined stdout+stderr output.
func Execute(target Target, payload Payload) ([]byte, error) {
	var input []byte
	switch target.Format {
	case "json":
		var err error
		input, err = json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
	case "markdown":
		input = []byte(formatAsMarkdown(payload))
	default: // "text" or unrecognized
		input = []byte(formatAsPlainText(payload))
	}

	ctx, cancel := context.WithTimeout(context.Background(), pipeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, target.Command, target.Args...)
	cmd.Stdin = bytes.NewReader(input)

	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("command timed out after %s", pipeTimeout)
	}
	if err != nil {
		return output, fmt.Errorf("command failed: %w", err)
	}
	return output, nil
}

func formatAsMarkdown(p Payload) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Review Finding: %s\n\n", p.Title))
	b.WriteString(fmt.Sprintf("**Severity:** %s | **Category:** %s\n", p.Severity, p.Category))
	b.WriteString(fmt.Sprintf("**File:** %s:%d\n", p.File, p.Line))
	b.WriteString("\n## Detail\n\n")
	b.WriteString(p.Detail + "\n")
	if p.Suggestion != "" {
		b.WriteString("\n## Suggestion\n\n")
		b.WriteString(p.Suggestion + "\n")
	}
	return b.String()
}

func formatAsPlainText(p Payload) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[%s/%s] %s\n", p.Severity, p.Category, p.Title))
	b.WriteString(fmt.Sprintf("File: %s:%d\n\n", p.File, p.Line))
	b.WriteString(p.Detail + "\n")
	if p.Suggestion != "" {
		b.WriteString(fmt.Sprintf("\nSuggestion: %s\n", p.Suggestion))
	}
	return b.String()
}

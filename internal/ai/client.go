package ai

import (
	"context"
	"strings"
)

// Message represents a chat message.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// Client is the interface for LLM providers.
type Client interface {
	// ChatStream sends a conversation to the LLM and streams the response.
	// systemPrompt is prepended as a system instruction.
	// onToken is called for each streamed chunk. Chunks may be:
	//   - plain text (regular model output)
	//   - "\x00THOUGHT:<text>" — model thinking/reasoning
	//   - "\x00TOOL_START:<name>(<args>)" — tool execution starting
	//   - "\x00TOOL_DONE:<name>|<status>|<duration>" — tool execution finished
	// Returns the full assembled response text, or an error.
	ChatStream(ctx context.Context, systemPrompt string, messages []Message, onToken func(string)) (string, error)
}

// ModelInfo is optionally implemented by clients that can report their model identity.
type ModelInfo interface {
	// ProviderName returns the provider name (e.g. "gemini", "anthropic").
	ProviderName() string
	// ModelName returns the model identifier (e.g. "gemini-2.5-pro").
	ModelName() string
}

// ToolConfigurer is optionally implemented by clients that support tools.
type ToolConfigurer interface {
	// SetHeadRef configures the git ref used for file reading tools.
	SetHeadRef(ref string)
	// SetBaseRef configures the git ref for reading base-branch files (before changes).
	SetBaseRef(ref string)
	// SetRawDiffs provides the raw unified diffs for the git_diff tool.
	SetRawDiffs(diffs map[string]string)
	// SetReviewGetter provides a function that returns the latest PR review summary.
	SetReviewGetter(fn func() string)
}

// ModelSwitcher is optionally implemented by clients that support switching
// the underlying model at runtime (e.g. via a TUI model picker).
type ModelSwitcher interface {
	// SwitchModel changes the active model to the given ID and applies the
	// provided tuning parameters. Returns an error if the model is invalid.
	SwitchModel(modelID string, maxOutputTokens int, temperature float64, thinkingBudget int) error
}

// UsageReporter is optionally implemented by clients that track token usage.
type UsageReporter interface {
	// Usage returns the accumulated token usage since last reset.
	Usage() TokenUsage
	// ResetUsage zeroes the usage counters.
	ResetUsage()
}

// SnapshotUsage returns the current token usage from a client and resets
// the counters. If the client does not implement UsageReporter, returns
// a zero TokenUsage.
func SnapshotUsage(client Client) TokenUsage {
	if ur, ok := client.(UsageReporter); ok {
		u := ur.Usage()
		ur.ResetUsage()
		return u
	}
	return TokenUsage{}
}

// StripMarkdownFences removes ```json ... ``` wrapping that LLMs commonly
// add around JSON output. Returns the trimmed content.
func StripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}
	return s
}

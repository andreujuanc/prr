package ai

import "context"

// Message represents a chat message.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// Client is the interface for LLM providers.
type Client interface {
	// ChatStream sends a conversation to the LLM and streams the response.
	// systemPrompt is prepended as a system instruction.
	// onToken is called for each streamed text chunk.
	// Returns the full assembled response text, or an error.
	ChatStream(ctx context.Context, systemPrompt string, messages []Message, onToken func(string)) (string, error)
}

// ToolConfigurer is optionally implemented by clients that support tools.
type ToolConfigurer interface {
	// SetHeadRef configures the git ref used for file reading tools.
	SetHeadRef(ref string)
	// SetRawDiffs provides the raw unified diffs for the get_diff tool.
	SetRawDiffs(diffs map[string]string)
}

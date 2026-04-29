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

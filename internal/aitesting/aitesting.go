// Package aitesting provides shared test helpers for code that exercises
// the ai package's leak-prevention machinery from outside the ai package
// itself.
//
// The ai package's own tests (internal/ai/prompt_test.go) cannot import
// this package — that would be an import cycle. They use the same
// underlying constants directly (ai.PrrSpecificToolNames). Sibling
// packages (internal/security, internal/audit, future leak-test sites)
// use the re-exports and helpers here.
package aitesting

import (
	"context"

	"github.com/andreujuanc/prr/internal/ai"
)

// PrrSpecificToolNames re-exports ai.PrrSpecificToolNames so leak tests
// in other packages depend on a single import path. The source of truth
// is ai.PrrSpecificToolNames; do not maintain a separate copy here.
var PrrSpecificToolNames = ai.PrrSpecificToolNames

// ClaudeCodeProvider is a stub ai.Provider that reports
// RunsOwnToolLoop: true. Useful as the second argument to
// ai.ResolveTools in leak tests that need to drive the Claude-Code
// branch without spinning up a real subprocess.
//
// Chat and StreamChat return nil/zero — this is a test double, not a
// functional provider. Don't use it for anything that actually exercises
// the chat path.
type ClaudeCodeProvider struct{}

func (ClaudeCodeProvider) Name() string    { return "fake-claude-code" }
func (ClaudeCodeProvider) ModelID() string { return "fake-1" }
func (ClaudeCodeProvider) Capabilities() ai.Capabilities {
	return ai.Capabilities{RunsOwnToolLoop: true}
}
func (ClaudeCodeProvider) Chat(context.Context, ai.ChatRequest) (*ai.ChatResponse, error) {
	return nil, nil
}
func (ClaudeCodeProvider) StreamChat(context.Context, ai.ChatRequest) (<-chan ai.ChatEvent, error) {
	return nil, nil
}

package ai

import (
	"context"
	"encoding/json"
)

// ── Roles ───────────────────────────────────────────────────────────────

// Role represents a message participant.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ── Stop reasons ────────────────────────────────────────────────────────

// StopReason indicates why the model stopped generating.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopError     StopReason = "error"
)

// ── Tool choice ─────────────────────────────────────────────────────────

// ToolChoice controls how the model selects tools.
type ToolChoice string

const (
	ToolChoiceAuto     ToolChoice = "auto"
	ToolChoiceRequired ToolChoice = "required"
	ToolChoiceNone     ToolChoice = "none"
)

// ── Content blocks ──────────────────────────────────────────────────────

// ContentBlock is a piece of message content. Implementations: TextBlock,
// ThinkingBlock, ToolUseBlock, ToolResultBlock.
type ContentBlock interface {
	blockType() string
}

// TextBlock carries regular text content.
type TextBlock struct{ Text string }

func (TextBlock) blockType() string { return "text" }

// ThinkingBlock carries model reasoning/thinking text.
// Signature is an opaque continuity token (Gemini thoughtSignature).
type ThinkingBlock struct {
	Text      string
	Signature string
}

func (ThinkingBlock) blockType() string { return "thinking" }

// ToolUseBlock represents a tool/function call from the model.
type ToolUseBlock struct {
	ID        string
	Name      string
	Args      json.RawMessage
	Signature string // opaque thought signature (Gemini); must be echoed back
}

func (ToolUseBlock) blockType() string { return "tool_use" }

// ToolResultBlock carries the result of executing a tool.
type ToolResultBlock struct {
	ToolUseID string
	Name      string // tool name (needed by Gemini's functionResponse)
	Content   string
	IsError   bool
}

func (ToolResultBlock) blockType() string { return "tool_result" }

// ── Provider messages ───────────────────────────────────────────────────

// ProviderMessage is a rich message for the Provider interface.
// Unlike the simple Message type (used by the Agent's external API),
// this carries structured content blocks.
type ProviderMessage struct {
	Role    Role
	Content []ContentBlock
}

// ── Capabilities ────────────────────────────────────────────────────────

// Capabilities describes what a provider supports.
type Capabilities struct {
	PromptCaching     bool // explicit caching API (Anthropic, Gemini)
	StructuredOutput  bool // JSON-schema-constrained output
	ParallelToolCalls bool
	MaxContextTokens  int

	// RunsOwnToolLoop indicates the provider runs its own internal
	// tool-calling loop with a native toolset (e.g. Claude Code). When
	// true, prr's prompts should NOT inject prr-specific tool names
	// (read_file, git_diff, etc.) — the provider's native tools are
	// used instead. See ResolveTools.
	RunsOwnToolLoop bool
}

// ── Token usage ─────────────────────────────────────────────────────────

// TokenUsage reports token consumption for a single request.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
	CacheHits    int // tokens served from cache (if supported)
}

// ── JSON Schema ─────────────────────────────────────────────────────────

// JSONSchema is a simplified JSON Schema for structured output.
type JSONSchema struct {
	Name   string
	Schema json.RawMessage
}

// ── Tool definitions ────────────────────────────────────────────────────

// ToolDef defines a tool the model can call. Provider-agnostic;
// each adapter translates to its native format.
type ToolDef struct {
	Name        string
	Description string
	Parameters  ToolParams
	ReadOnly    bool // true if this tool only reads state (safe for parallel execution)
}

// ToolParams describes the parameters object for a tool.
type ToolParams struct {
	Type       string // always "object" for function parameters
	Properties map[string]ToolParam
	Required   []string
}

// ToolParam describes a single parameter.
type ToolParam struct {
	Type        string
	Description string
	Enum        []string   // optional enum values
	Items       *ToolParam // for array types
}

// ── Request / Response ──────────────────────────────────────────────────

// ChatRequest is the canonical request to a provider.
//
// Temperature is *float64 so an explicit 0 (greedy decoding) is
// distinguishable from "use the provider's default". nil = default.
type ChatRequest struct {
	Model           string
	System          string
	Messages        []ProviderMessage
	Tools           []ToolDef
	ToolChoice      ToolChoice
	MaxOutputTokens int
	Temperature     *float64
	JSONSchema      *JSONSchema // structured output, optional
	CachePrefix     bool        // hint: cache system + leading messages if supported
}

// TempPtr converts a config float64 to *float64, treating zero as
// "unset / use provider default". This preserves the historical
// behaviour where a 0 in models.json meant "don't send temperature"
// — explicit greedy decoding requires setting the pointer directly,
// not by writing 0 in JSON.
func TempPtr(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}

// ChatResponse is the canonical response from a provider.
type ChatResponse struct {
	Content    []ContentBlock // mix of TextBlock, ThinkingBlock, ToolUseBlock
	StopReason StopReason
	Usage      TokenUsage
}

// ── Streaming events ────────────────────────────────────────────────────

// ChatEventType categorizes streaming events.
type ChatEventType int

const (
	EventText     ChatEventType = iota // regular text chunk
	EventThinking                      // thinking/reasoning text
	EventToolUse                       // tool call
	EventDone                          // final event with complete response
	EventError                         // error during streaming
)

// ChatEvent is a single streaming event from a provider.
type ChatEvent struct {
	Type     ChatEventType
	Text     string        // EventText, EventThinking
	ToolUse  *ToolUseBlock // EventToolUse
	Response *ChatResponse // EventDone
	Err      error         // EventError
}

// ── Provider interface ──────────────────────────────────────────────────

// Provider is the canonical interface for LLM providers. Each supported
// model (Gemini, Anthropic, OpenAI) plugs in behind this interface.
//
// Chat performs a single non-streaming request.
// StreamChat performs a single streaming request; the returned channel
// delivers events until closed. The final event is always EventDone
// (on success) or EventError (on failure).
type Provider interface {
	Name() string
	ModelID() string
	Capabilities() Capabilities
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
}

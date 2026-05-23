package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
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

// BlobBlock carries inline binary data — typically an image. MimeType
// is the IANA type (e.g. "image/png", "image/jpeg"). Translated to
// Gemini's inlineData part and OpenAI's image_url content part with a
// base64 data URI. Existing providers without binary support drop
// BlobBlock silently.
type BlobBlock struct {
	Data     []byte
	MimeType string
}

func (BlobBlock) blockType() string { return "blob" }

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
//
// ReportedCostUSD is the provider-reported cost in USD for this single
// request, when the provider reports cost natively (e.g., OpenCode, which
// emits "cost" in its step_finish event). Providers without native cost
// reporting leave it at 0 and callers fall back to config.EstimateCost
// using the per-1M token prices in known_models.go.
type TokenUsage struct {
	InputTokens     int
	OutputTokens    int
	CacheHits       int     // tokens served from cache (if supported)
	ReportedCostUSD float64 // provider-reported cost for this request; 0 if unreported
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
//
// Properties and Required let callers describe nested objects — for
// example, an array of objects (Type="array", Items={Type:"object",
// Properties:...}). The Gemini and OpenAI converters recurse through
// these fields, so nested shape no longer gets silently flattened.
type ToolParam struct {
	Type        string
	Description string
	Enum        []string             // optional enum values (string-valued only)
	Items       *ToolParam           // for array types
	Properties  map[string]ToolParam // for object types
	Required    []string             // required keys when Type == "object"
}

// ValidateToolDef walks a ToolDef and returns the first structural
// issue found. Currently checks: Enum values are only attached to
// string-typed parameters, which is the only shape ToolParam.Enum can
// represent. Misuse silently corrupts the schema sent to the model
// (Gemini accepts the field but ignores the constraint), so callers
// should run this at startup time on every ToolDef they emit.
func ValidateToolDef(td ToolDef) error {
	return validateToolParams(td.Name, td.Parameters)
}

func validateToolParams(toolName string, p ToolParams) error {
	for name, sub := range p.Properties {
		if err := validateToolParam(toolName+"."+name, sub); err != nil {
			return err
		}
	}
	return nil
}

func validateToolParam(path string, p ToolParam) error {
	if len(p.Enum) > 0 && p.Type != "string" {
		return fmt.Errorf("tool param %s: Enum requires Type=\"string\", got %q", path, p.Type)
	}
	for name, sub := range p.Properties {
		if err := validateToolParam(path+"."+name, sub); err != nil {
			return err
		}
	}
	if p.Items != nil {
		if err := validateToolParam(path+"[]", *p.Items); err != nil {
			return err
		}
	}
	return nil
}

// ── Request / Response ──────────────────────────────────────────────────

// ChatRequest is the canonical request to a provider.
//
// Temperature is *float64 so an explicit 0 (greedy decoding) is
// distinguishable from "use the provider's default". nil = default.
//
// RequestTimeout, when non-zero, overrides the provider's configured
// RequestTimeout for this one call. Useful for short calls (a quick
// classification, an embedding lookup) that shouldn't pay the
// 15-minute synthesis-sized budget the factory sets globally.
type ChatRequest struct {
	Model           string
	System          string
	Messages        []ProviderMessage
	Tools           []ToolDef
	ToolChoice      ToolChoice
	MaxOutputTokens int
	Temperature     *float64
	JSONSchema      *JSONSchema   // structured output, optional
	CachePrefix     bool          // hint: cache system + leading messages if supported
	CachedContent   string        // explicit cache handle (e.g. Gemini "cachedContents/abc"); when set, provider omits the cached fields from the request body
	RequestTimeout  time.Duration // per-call override; 0 = use provider's RequestTimeout
}

// ── Cache support (optional capability) ─────────────────────────────────

// CacheSupport is implemented by providers that allow callers to upload a
// static prefix (system instruction, tool definitions, etc.) once and reuse
// it across many requests. The opaque handle returned by CreateContextCache
// is passed back via ChatRequest.CachedContent on subsequent calls.
//
// Providers without explicit cache management (OpenAI's automatic prefix
// caching, Claude Code's internal caching) do not implement this interface;
// callers should treat the absence of it as "no work to do here."
type CacheSupport interface {
	// CreateContextCache uploads (systemInstruction, tools) as a server-side
	// cache scoped to the given model. ttl bounds the cache lifetime; the
	// provider may enforce a minimum. Returns an opaque handle suitable for
	// ChatRequest.CachedContent.
	CreateContextCache(ctx context.Context, systemInstruction string, tools []ToolDef, ttl time.Duration) (string, error)

	// DeleteContextCache best-effort deletes a previously created cache.
	// Callers should usually rely on ttl expiry and only call this on a
	// successful audit teardown.
	DeleteContextCache(ctx context.Context, handle string) error
}

// TempPtr converts a config float64 to *float64, treating values
// <= 0 as "unset / use provider default". This preserves the
// historical behaviour where a 0 in models.json meant "don't send
// temperature" — explicit greedy decoding requires setting the
// pointer directly, not by writing 0 in JSON.
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
	EventText      ChatEventType = iota // regular text chunk
	EventThinking                       // thinking/reasoning text
	EventToolUse                        // tool call
	EventDone                           // final event with complete response
	EventError                          // error during streaming
	EventHeartbeat                      // no data received for HeartbeatInterval
)

// ChatEvent is a single streaming event from a provider.
//
// EventHeartbeat carries the silence duration in Silence; it fires
// when a stream has not produced a data line within the provider's
// HeartbeatInterval. Consumers that don't care can ignore the type
// (the agent loop uses a switch without a default, so heartbeats fall
// through harmlessly). Existing handlers stay correct without edits.
type ChatEvent struct {
	Type     ChatEventType
	Text     string        // EventText, EventThinking
	ToolUse  *ToolUseBlock // EventToolUse
	Response *ChatResponse // EventDone
	Err      error         // EventError
	Silence  time.Duration // EventHeartbeat
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

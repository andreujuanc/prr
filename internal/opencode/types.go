// Package opencode provides an HTTP API client for the OpenCode server,
// enabling session management, prompt submission, SSE event streaming,
// and interactive permission handling.
package opencode

import "time"

// ── Server status ───────────────────────────────────────────────────────

// ServerStatus represents the connection state of the OpenCode server.
type ServerStatus int

const (
	ServerDisconnected ServerStatus = iota
	ServerConnecting
	ServerConnected
)

// ── Session ─────────────────────────────────────────────────────────────

// Session represents an OpenCode chat session.
type Session struct {
	ID        string       `json:"id"`
	Slug      string       `json:"slug"`
	ProjectID string       `json:"projectID"`
	Directory string       `json:"directory"`
	Path      string       `json:"path"`
	Title     string       `json:"title"`
	Version   string       `json:"version"`
	Summary   *Summary     `json:"summary,omitempty"`
	Time      SessionTimes `json:"time"`
}

// Summary holds file-change stats for a session.
type Summary struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Files     int `json:"files"`
}

// SessionTimes holds created/updated timestamps.
type SessionTimes struct {
	Created int64 `json:"created"` // unix millis
	Updated int64 `json:"updated"` // unix millis
}

// SessionStatus describes whether the session is idle or busy.
type SessionStatus struct {
	Type string `json:"type"` // "idle", "busy"
}

// ── Messages & Parts ────────────────────────────────────────────────────

// MessageInfo describes a completed or in-progress message.
type MessageInfo struct {
	ID         string      `json:"id"`
	ParentID   string      `json:"parentID"`
	Role       string      `json:"role"` // "user" or "assistant"
	Mode       string      `json:"mode"`
	Agent      string      `json:"agent"`
	ModelID    string      `json:"modelID"`
	ProviderID string      `json:"providerID"`
	SessionID  string      `json:"sessionID"`
	Finish     string      `json:"finish,omitempty"` // "end-turn", "tool-calls", etc.
	Cost       float64     `json:"cost"`
	Tokens     TokenUsage  `json:"tokens"`
	Time       MessageTime `json:"time"`
}

// MessageTime holds created/completed timestamps.
type MessageTime struct {
	Created   int64 `json:"created"`   // unix millis
	Completed int64 `json:"completed"` // unix millis; 0 if still streaming
}

// TokenUsage tracks token consumption.
type TokenUsage struct {
	Total     int        `json:"total"`
	Input     int        `json:"input"`
	Output    int        `json:"output"`
	Reasoning int        `json:"reasoning"`
	Cache     CacheUsage `json:"cache"`
}

// CacheUsage breaks out cached tokens.
type CacheUsage struct {
	Write int `json:"write"`
	Read  int `json:"read"`
}

// Part represents a message part (text or tool call).
type Part struct {
	ID        string     `json:"id"`
	MessageID string     `json:"messageID"`
	SessionID string     `json:"sessionID"`
	Type      string     `json:"type"` // "text", "tool", "step-finish"
	Text      string     `json:"text,omitempty"`
	Tool      string     `json:"tool,omitempty"`
	CallID    string     `json:"callID,omitempty"`
	State     *ToolState `json:"state,omitempty"`
	Time      *PartTime  `json:"time,omitempty"`
}

// PartTime holds start/end timestamps for a part.
type PartTime struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// ToolState describes a tool call's lifecycle.
type ToolState struct {
	Status   string                 `json:"status"` // "pending", "running", "completed", "error"
	Input    map[string]interface{} `json:"input,omitempty"`
	Output   string                 `json:"output,omitempty"`
	Raw      string                 `json:"raw,omitempty"`
	Title    string                 `json:"title,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Time     *ToolTime              `json:"time,omitempty"`
}

// ToolTime holds start/end for a tool execution.
type ToolTime struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// ── SSE Events ──────────────────────────────────────────────────────────

// EventType enumerates the SSE event types emitted by the OpenCode server.
type EventType string

const (
	EventServerConnected    EventType = "server.connected"
	EventSessionUpdated     EventType = "session.updated"
	EventSessionStatus      EventType = "session.status"
	EventSessionSummary     EventType = "session.summary"
	EventMessageUpdated     EventType = "message.updated"
	EventMessagePartUpdated EventType = "message.part.updated"
	EventMessagePartDelta   EventType = "message.part.delta"
	EventFileEdited         EventType = "file.edited"
	EventFileWatcherUpdated EventType = "file.watcher.updated"
	EventPermissionAsked    EventType = "permission.asked"
	EventPermissionReplied  EventType = "permission.replied"
	EventQuestionAsked      EventType = "question.asked"
	EventQuestionReplied    EventType = "question.replied"
	EventQuestionRejected   EventType = "question.rejected"
)

// Event is a parsed SSE event from the OpenCode server.
type Event struct {
	Type       EventType
	Raw        []byte // raw JSON of the full event
	Properties EventProperties
}

// EventProperties is the top-level "properties" field in all events.
// Because different event types reuse field names (e.g. "info" for both
// Session and MessageInfo), we decode into raw JSON and dispatch per type.
type EventProperties struct {
	SessionID string `json:"sessionID,omitempty"`

	// Populated by event-type-aware parsing:
	Session     *Session       `json:"-"` // session.updated → .info
	MessageInfo *MessageInfo   `json:"-"` // message.updated → .info
	Status      *SessionStatus `json:"status,omitempty"`
	Part        *Part          `json:"part,omitempty"`

	// message.part.delta
	MessageID string `json:"messageID,omitempty"`
	PartID    string `json:"partID,omitempty"`
	Field     string `json:"field,omitempty"`
	Delta     string `json:"delta,omitempty"`

	// file.edited / file.watcher.updated
	File      string `json:"file,omitempty"`
	FileEvent string `json:"event,omitempty"`

	// session.summary
	Summary *SessionSummary `json:"summary,omitempty"`

	// permission.asked
	Permission *Permission `json:"-"`

	// question.asked
	Question *Question `json:"-"`

	// Raw time field
	Time int64 `json:"time,omitempty"`
}

// SessionSummary holds token/message stats for a session.
type SessionSummary struct {
	Messages int `json:"messages"`
	Tokens   int `json:"tokens"`
}

// ── Permission ──────────────────────────────────────────────────────────

// Permission represents a tool permission request that needs user approval.
type Permission struct {
	ID        string                 `json:"id"`
	SessionID string                 `json:"sessionID"`
	Tool      string                 `json:"tool"`
	Input     map[string]interface{} `json:"input"`
	CreatedAt time.Time              `json:"createdAt"`
}

// PermissionResponse is sent to approve or deny a permission request.
type PermissionResponse struct {
	Approved bool `json:"approved"`
}

// ── Question ────────────────────────────────────────────────────────────

// Question represents an interactive question from the OpenCode agent
// that requires user input before the session can continue.
type Question struct {
	ID        string           `json:"id"`
	SessionID string           `json:"sessionID"`
	Questions []QuestionPrompt `json:"questions"`
	Tool      *ToolRef         `json:"tool,omitempty"`
}

// QuestionPrompt is a single question with options.
type QuestionPrompt struct {
	Question string           `json:"question"`
	Header   string           `json:"header"`
	Options  []QuestionOption `json:"options"`
}

// QuestionOption is a selectable choice for a question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ToolRef references the tool call that triggered this question/permission.
type ToolRef struct {
	MessageID string `json:"messageID"`
	CallID    string `json:"callID"`
}

// QuestionReplyRequest is sent to answer a question.
type QuestionReplyRequest struct {
	Answers [][]string `json:"answers"`
}

// ── Health ──────────────────────────────────────────────────────────────

// HealthResponse is returned by GET /global/health.
type HealthResponse struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
}

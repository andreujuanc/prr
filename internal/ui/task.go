package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/andreujuanc/prr/internal/opencode"
	"github.com/andreujuanc/prr/internal/state"
)

// ── Task status ─────────────────────────────────────────────────────────

// TaskStatus represents the lifecycle state of a background task.
type TaskStatus int

const (
	TaskRunning   TaskStatus = iota // session is active
	TaskCompleted                   // finished successfully
	TaskFailed                      // finished with error
	TaskCancelled                   // user cancelled
)

// ── Task ────────────────────────────────────────────────────────────────

// Task represents a background "Fix with OpenCode" session.
type Task struct {
	ID         int
	Title      string              // short label e.g. "Fix: null check in auth.go:42"
	FindingIdx int                 // index into m.reviewFindings
	Finding    state.ReviewFinding // snapshot of the finding at spawn time
	StartedAt  time.Time
	SessionID  string // OpenCode session ID

	mu         sync.Mutex
	status     TaskStatus      // protected by mu
	err        string          // error message if failed; protected by mu
	finishedAt time.Time       // when task reached a terminal state; protected by mu
	output     strings.Builder // accumulated session output
	cancel     context.CancelFunc
	permission *opencode.Permission // pending permission request (nil if none)
	question   *opencode.Question   // pending question request (nil if none)
}

// Output returns the accumulated output (thread-safe).
func (t *Task) Output() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.output.String()
}

// Status returns the task status (thread-safe).
func (t *Task) GetStatus() TaskStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

// GetError returns the task error message (thread-safe).
func (t *Task) GetError() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// GetFinishedAt returns when the task reached a terminal state (thread-safe).
func (t *Task) GetFinishedAt() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.finishedAt
}

// setStatus sets status and optional error (thread-safe).
func (t *Task) setStatus(s TaskStatus, errMsg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = s
	t.err = errMsg
	if s == TaskCompleted || s == TaskFailed || s == TaskCancelled {
		t.finishedAt = time.Now()
	}
}

// GetPermission returns the pending permission request, if any (thread-safe).
func (t *Task) GetPermission() *opencode.Permission {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.permission
}

// setPermission sets or clears the pending permission (thread-safe).
func (t *Task) setPermission(p *opencode.Permission) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.permission = p
}

// GetQuestion returns the pending question request, if any (thread-safe).
func (t *Task) GetQuestion() *opencode.Question {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.question
}

// setQuestion sets or clears the pending question (thread-safe).
func (t *Task) setQuestion(q *opencode.Question) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.question = q
}

// appendOutput appends text to the output buffer (thread-safe).
func (t *Task) appendOutput(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output.WriteString(text)
}

// ── Messages ────────────────────────────────────────────────────────────

// TaskSpawnedMsg is sent when a task session has been created.
type TaskSpawnedMsg struct {
	ID int
}

// TaskOutputMsg delivers streaming output from a running task.
type TaskOutputMsg struct {
	ID    int
	Lines string // one or more lines of output
}

// TaskDoneMsg is sent when a task session finishes.
type TaskDoneMsg struct {
	ID  int
	Err error // nil on success
}

// TaskPermissionMsg is sent when a task needs permission approval.
type TaskPermissionMsg struct {
	ID         int
	Permission opencode.Permission
}

// TaskQuestionMsg is sent when a task needs the user to answer a question.
type TaskQuestionMsg struct {
	ID       int
	Question opencode.Question
}

// ── Spawn ───────────────────────────────────────────────────────────────

// spawnOpenCodeTask launches a fix task via the OpenCode HTTP API.
// It creates a session, sends the prompt, and monitors SSE events.
func spawnOpenCodeTask(task *Task, mgr *opencode.Manager, p *tea.Program) {
	ctx, cancel := context.WithCancel(mgr.Context())
	task.cancel = cancel

	client := mgr.Client()
	if client == nil {
		task.setStatus(TaskFailed, "OpenCode server not connected")
		p.Send(TaskDoneMsg{ID: task.ID, Err: fmt.Errorf("server not connected")})
		return
	}

	// Create a session for this task
	prompt := buildFixPrompt(task.Finding)
	session, err := client.CreateSession(ctx, task.Title)
	if err != nil {
		task.setStatus(TaskFailed, fmt.Sprintf("create session: %v", err))
		p.Send(TaskDoneMsg{ID: task.ID, Err: err})
		return
	}
	task.SessionID = session.ID

	// Subscribe to events for this session
	stream := mgr.Stream()
	if stream == nil {
		task.setStatus(TaskFailed, "event stream not available")
		p.Send(TaskDoneMsg{ID: task.ID, Err: fmt.Errorf("no event stream")})
		return
	}
	events, unsubscribe := stream.Subscribe(ctx, session.ID)
	defer unsubscribe()

	p.Send(TaskSpawnedMsg{ID: task.ID})

	// Send the prompt (fire-and-forget; results come via SSE)
	if err := client.SendPrompt(ctx, session.ID, prompt); err != nil {
		task.setStatus(TaskFailed, fmt.Sprintf("send prompt: %v", err))
		p.Send(TaskDoneMsg{ID: task.ID, Err: err})
		return
	}

	task.appendOutput("▶ Session created: " + session.ID + "\n")
	task.appendOutput("▶ Prompt sent, waiting for response...\n\n")
	p.Send(TaskOutputMsg{ID: task.ID, Lines: "▶ Session created\n▶ Prompt sent, waiting for response...\n\n"})

	// Monitor events for this session
	monitorSessionEvents(ctx, task, events, client, p)
}

// monitorSessionEvents reads events from the session-filtered channel
// and processes them until the session goes idle or the context is cancelled.
func monitorSessionEvents(ctx context.Context, task *Task, events <-chan opencode.Event, client *opencode.Client, p *tea.Program) {
	var batch strings.Builder
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	flush := func() {
		if batch.Len() > 0 {
			lines := batch.String()
			task.appendOutput(lines)
			p.Send(TaskOutputMsg{ID: task.ID, Lines: lines})
			batch.Reset()
		}
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			if task.GetStatus() == TaskRunning {
				task.setStatus(TaskCancelled, "")
				p.Send(TaskDoneMsg{ID: task.ID, Err: nil})
			}
			return

		case <-ticker.C:
			flush()

		case event, ok := <-events:
			if !ok {
				// Stream closed
				flush()
				if task.GetStatus() == TaskRunning {
					task.setStatus(TaskFailed, "event stream closed")
					p.Send(TaskDoneMsg{ID: task.ID, Err: fmt.Errorf("event stream closed")})
				}
				return
			}

			switch event.Type {
			case opencode.EventMessagePartDelta:
				// Streaming text delta — append to output
				if event.Properties.Delta != "" {
					batch.WriteString(event.Properties.Delta)
				}

			case opencode.EventMessagePartUpdated:
				part := event.Properties.Part
				if part == nil {
					continue
				}

				switch part.Type {
				case "tool":
					if part.State != nil {
						handleToolEvent(task, part, &batch, p)
					}
				case "text":
					// Full text part — already covered by deltas
				case "step-finish":
					// Step completed
				}

			case opencode.EventSessionStatus:
				if event.Properties.Status != nil && event.Properties.Status.Type == "idle" {
					// Session went idle — work is complete
					flush()
					task.setStatus(TaskCompleted, "")
					p.Send(TaskDoneMsg{ID: task.ID, Err: nil})
					return
				}

			case opencode.EventPermissionAsked:
				perm := event.Properties.Permission
				if perm != nil {
					flush()
					task.setPermission(perm)
					batch.WriteString(fmt.Sprintf("\n⚠ Permission requested: %s\n", perm.Tool))
					flush()
					p.Send(TaskPermissionMsg{ID: task.ID, Permission: *perm})
				}

			case opencode.EventPermissionReplied:
				// Permission was handled — clear pending state
				task.setPermission(nil)

			case opencode.EventQuestionAsked:
				q := event.Properties.Question
				if q != nil {
					flush()
					task.setQuestion(q)
					header := ""
					if len(q.Questions) > 0 {
						header = q.Questions[0].Header
					}
					batch.WriteString(fmt.Sprintf("\n❓ Question: %s\n", header))
					flush()
					p.Send(TaskQuestionMsg{ID: task.ID, Question: *q})
				}

			case opencode.EventQuestionReplied, opencode.EventQuestionRejected:
				// Question was handled — clear pending state
				task.setQuestion(nil)

			case opencode.EventMessageUpdated:
				// Track message completion for informational purposes
			}
		}
	}
}

// handleToolEvent processes tool call state changes and formats output.
func handleToolEvent(task *Task, part *opencode.Part, batch *strings.Builder, p *tea.Program) {
	if part.State == nil {
		return
	}

	switch part.State.Status {
	case "pending":
		batch.WriteString(fmt.Sprintf("\n⏳ Tool: %s (pending)\n", part.Tool))

	case "running":
		title := part.State.Title
		if title == "" {
			title = formatToolInput(part.Tool, part.State.Input)
		}
		batch.WriteString(fmt.Sprintf("\n▶ **%s** — `%s`\n", part.Tool, title))

	case "completed":
		output := part.State.Output
		if len(output) > 500 {
			output = output[:500] + "…"
		}
		if output != "" {
			batch.WriteString(fmt.Sprintf("\n✓ **%s** done: %s\n", part.Tool, strings.TrimSpace(output)))
		} else {
			batch.WriteString(fmt.Sprintf("\n✓ **%s** done\n", part.Tool))
		}

	case "error":
		errMsg := part.State.Output
		batch.WriteString(fmt.Sprintf("\n✗ **%s** error: %s\n", part.Tool, errMsg))
	}
}

// formatToolInput creates a brief description of a tool call from its input.
func formatToolInput(tool string, input map[string]any) string {
	switch tool {
	case "bash":
		if cmd, ok := input["command"].(string); ok {
			if len(cmd) > 80 {
				cmd = cmd[:77] + "..."
			}
			return cmd
		}
	case "write":
		if fp, ok := input["filePath"].(string); ok {
			return fp
		}
	case "edit":
		if fp, ok := input["filePath"].(string); ok {
			return fp
		}
	case "read":
		if fp, ok := input["filePath"].(string); ok {
			return fp
		}
	case "glob":
		if pat, ok := input["pattern"].(string); ok {
			return pat
		}
	case "grep":
		if pat, ok := input["pattern"].(string); ok {
			return pat
		}
	}
	return ""
}

// cancelTask sends cancel signal to a running task.
func cancelTask(task *Task, mgr *opencode.Manager) {
	if task.GetStatus() != TaskRunning {
		return
	}

	// Try to abort the session on the server
	if mgr != nil && task.SessionID != "" {
		client := mgr.Client()
		if client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = client.Abort(ctx, task.SessionID)
		}
	}

	// Cancel the local context
	if task.cancel != nil {
		task.cancel()
	}
}

// ── Prompt builder ──────────────────────────────────────────────────────

func buildFixPrompt(f state.ReviewFinding) string {
	var b strings.Builder
	b.WriteString("Fix the following code review finding. The issue is in the file and line indicated below.\n")
	b.WriteString("Do not ask for clarification — just fix it.\n\n")
	b.WriteString(fmt.Sprintf("File: %s:%d\n", f.File, f.Line))
	b.WriteString(fmt.Sprintf("Severity: %s | Category: %s\n", f.Severity, f.Category))
	b.WriteString("\n## Issue\n")
	b.WriteString(f.Title + "\n\n")
	b.WriteString(f.Detail + "\n")
	if f.Suggestion != "" {
		b.WriteString("\n## Suggestion\n")
		b.WriteString(f.Suggestion + "\n")
	}
	return b.String()
}

// ── Helpers ─────────────────────────────────────────────────────────────

// taskTitle builds a short title for a task from its finding.
func taskTitle(f state.ReviewFinding) string {
	title := f.Title
	if len(title) > 40 {
		title = title[:37] + "..."
	}
	return fmt.Sprintf("Fix: %s", title)
}

// hasRunningTaskForFinding checks if there's already a running task for the given finding index.
func hasRunningTaskForFinding(tasks []*Task, findingIdx int) bool {
	for _, t := range tasks {
		if t.FindingIdx == findingIdx && t.GetStatus() == TaskRunning {
			return true
		}
	}
	return false
}

// hasAnyRunningTask checks if there are any tasks still running.
func hasAnyRunningTask(tasks []*Task) bool {
	for _, t := range tasks {
		if t.GetStatus() == TaskRunning {
			return true
		}
	}
	return false
}

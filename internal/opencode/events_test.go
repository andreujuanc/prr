package opencode

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParsePermissionAskedEvent(t *testing.T) {
	raw := `{
		"type": "permission.asked",
		"properties": {
			"id": "per_abc123",
			"sessionID": "ses_xyz789",
			"tool": "bash",
			"input": {"command": "rm -rf /tmp/test"},
			"tool": "bash"
		}
	}`

	// Parse the event using parseEvent
	event, err := parseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parseEvent failed: %v", err)
	}

	if event.Type != EventPermissionAsked {
		t.Errorf("expected type %q, got %q", EventPermissionAsked, event.Type)
	}

	if event.Properties.Permission == nil {
		t.Fatal("expected Permission to be non-nil")
	}

	perm := event.Properties.Permission
	if perm.ID != "per_abc123" {
		t.Errorf("expected permission ID %q, got %q", "per_abc123", perm.ID)
	}
	if perm.SessionID != "ses_xyz789" {
		t.Errorf("expected session ID %q, got %q", "ses_xyz789", perm.SessionID)
	}
	if perm.Tool != "bash" {
		t.Errorf("expected tool %q, got %q", "bash", perm.Tool)
	}
	if event.Properties.SessionID != "ses_xyz789" {
		t.Errorf("expected properties.SessionID to be set, got %q", event.Properties.SessionID)
	}
}

func TestParseQuestionAskedEvent(t *testing.T) {
	raw := `{
		"type": "question.asked",
		"properties": {
			"id": "que_def456",
			"sessionID": "ses_xyz789",
			"questions": [
				{
					"question": "Which option do you prefer?",
					"header": "Choose one",
					"options": [
						{"label": "Option A", "description": "First choice"},
						{"label": "Option B", "description": "Second choice"}
					]
				}
			],
			"tool": {"messageID": "msg_001", "callID": "call_001"}
		}
	}`

	event, err := parseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parseEvent failed: %v", err)
	}

	if event.Type != EventQuestionAsked {
		t.Errorf("expected type %q, got %q", EventQuestionAsked, event.Type)
	}

	if event.Properties.Question == nil {
		t.Fatal("expected Question to be non-nil")
	}

	q := event.Properties.Question
	if q.ID != "que_def456" {
		t.Errorf("expected question ID %q, got %q", "que_def456", q.ID)
	}
	if q.SessionID != "ses_xyz789" {
		t.Errorf("expected session ID %q, got %q", "ses_xyz789", q.SessionID)
	}
	if len(q.Questions) != 1 {
		t.Fatalf("expected 1 question prompt, got %d", len(q.Questions))
	}

	prompt := q.Questions[0]
	if prompt.Header != "Choose one" {
		t.Errorf("expected header %q, got %q", "Choose one", prompt.Header)
	}
	if prompt.Question != "Which option do you prefer?" {
		t.Errorf("expected question text %q, got %q", "Which option do you prefer?", prompt.Question)
	}
	if len(prompt.Options) != 2 {
		t.Fatalf("expected 2 options, got %d", len(prompt.Options))
	}
	if prompt.Options[0].Label != "Option A" {
		t.Errorf("expected first option label %q, got %q", "Option A", prompt.Options[0].Label)
	}
	if prompt.Options[1].Description != "Second choice" {
		t.Errorf("expected second option description %q, got %q", "Second choice", prompt.Options[1].Description)
	}

	if event.Properties.SessionID != "ses_xyz789" {
		t.Errorf("expected properties.SessionID to be set, got %q", event.Properties.SessionID)
	}
}

func TestParseSessionStatusEvent(t *testing.T) {
	raw := `{
		"type": "session.status",
		"properties": {
			"sessionID": "ses_abc",
			"status": {"type": "idle"}
		}
	}`

	event, err := parseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parseEvent failed: %v", err)
	}

	if event.Type != EventSessionStatus {
		t.Errorf("expected type %q, got %q", EventSessionStatus, event.Type)
	}
	if event.Properties.SessionID != "ses_abc" {
		t.Errorf("expected sessionID %q, got %q", "ses_abc", event.Properties.SessionID)
	}
	if event.Properties.Status == nil {
		t.Fatal("expected Status to be non-nil")
	}
	if event.Properties.Status.Type != "idle" {
		t.Errorf("expected status type %q, got %q", "idle", event.Properties.Status.Type)
	}
}

func TestQuestionReplyRequestJSON(t *testing.T) {
	req := QuestionReplyRequest{
		Answers: [][]string{{"Option A"}},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	expected := `{"answers":[["Option A"]]}`
	if string(data) != expected {
		t.Errorf("expected %s, got %s", expected, string(data))
	}
}

// ── Extended parseEvent tests ───────────────────────────────────────────

func TestParseEvent_SessionUpdated(t *testing.T) {
	raw := `{
		"type": "session.updated",
		"properties": {
			"sessionID": "ses_001",
			"info": {
				"id": "ses_001",
				"title": "My Session",
				"slug": "my-session",
				"projectID": "proj_1",
				"directory": "/tmp",
				"path": "/tmp/session",
				"version": "1.0"
			}
		}
	}`

	event, err := parseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parseEvent failed: %v", err)
	}
	if event.Type != EventSessionUpdated {
		t.Errorf("type = %q, want %q", event.Type, EventSessionUpdated)
	}
	if event.Properties.Session == nil {
		t.Fatal("expected Session to be non-nil")
	}
	if event.Properties.Session.ID != "ses_001" {
		t.Errorf("Session.ID = %q, want %q", event.Properties.Session.ID, "ses_001")
	}
	if event.Properties.Session.Title != "My Session" {
		t.Errorf("Session.Title = %q, want %q", event.Properties.Session.Title, "My Session")
	}
}

func TestParseEvent_MessageUpdated(t *testing.T) {
	raw := `{
		"type": "message.updated",
		"properties": {
			"sessionID": "ses_001",
			"info": {
				"id": "msg_001",
				"parentID": "msg_000",
				"role": "assistant",
				"modelID": "gpt-4o",
				"sessionID": "ses_001",
				"tokens": {"total": 100, "input": 50, "output": 50}
			}
		}
	}`

	event, err := parseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parseEvent failed: %v", err)
	}
	if event.Type != EventMessageUpdated {
		t.Errorf("type = %q, want %q", event.Type, EventMessageUpdated)
	}
	if event.Properties.MessageInfo == nil {
		t.Fatal("expected MessageInfo to be non-nil")
	}
	if event.Properties.MessageInfo.ID != "msg_001" {
		t.Errorf("MessageInfo.ID = %q, want %q", event.Properties.MessageInfo.ID, "msg_001")
	}
	if event.Properties.MessageInfo.Role != "assistant" {
		t.Errorf("MessageInfo.Role = %q, want %q", event.Properties.MessageInfo.Role, "assistant")
	}
	if event.Properties.MessageInfo.Tokens.Total != 100 {
		t.Errorf("Tokens.Total = %d, want 100", event.Properties.MessageInfo.Tokens.Total)
	}
}

func TestParseEvent_MessagePartDelta(t *testing.T) {
	raw := `{
		"type": "message.part.delta",
		"properties": {
			"sessionID": "ses_001",
			"messageID": "msg_001",
			"partID": "part_001",
			"field": "text",
			"delta": "Hello "
		}
	}`

	event, err := parseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parseEvent failed: %v", err)
	}
	if event.Type != EventMessagePartDelta {
		t.Errorf("type = %q, want %q", event.Type, EventMessagePartDelta)
	}
	if event.Properties.MessageID != "msg_001" {
		t.Errorf("MessageID = %q, want %q", event.Properties.MessageID, "msg_001")
	}
	if event.Properties.Delta != "Hello " {
		t.Errorf("Delta = %q, want %q", event.Properties.Delta, "Hello ")
	}
}

func TestParseEvent_FileEdited(t *testing.T) {
	raw := `{
		"type": "file.edited",
		"properties": {
			"sessionID": "ses_001",
			"file": "/tmp/test.go"
		}
	}`

	event, err := parseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parseEvent failed: %v", err)
	}
	if event.Properties.File != "/tmp/test.go" {
		t.Errorf("File = %q, want %q", event.Properties.File, "/tmp/test.go")
	}
}

func TestParseEvent_MalformedJSON(t *testing.T) {
	_, err := parseEvent([]byte("not json at all"))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestParseEvent_EmptyProperties(t *testing.T) {
	raw := `{"type": "server.connected"}`
	event, err := parseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parseEvent failed: %v", err)
	}
	if event.Type != EventServerConnected {
		t.Errorf("type = %q, want %q", event.Type, EventServerConnected)
	}
}

func TestParseEvent_UnknownType(t *testing.T) {
	raw := `{
		"type": "some.future.event",
		"properties": {"sessionID": "ses_001"}
	}`
	event, err := parseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parseEvent failed for unknown type: %v", err)
	}
	if event.Type != "some.future.event" {
		t.Errorf("type = %q, want %q", event.Type, "some.future.event")
	}
	if event.Properties.SessionID != "ses_001" {
		t.Errorf("SessionID = %q, want %q", event.Properties.SessionID, "ses_001")
	}
}

func TestParseEvent_RawPreserved(t *testing.T) {
	raw := `{"type":"session.status","properties":{"sessionID":"s1","status":{"type":"busy"}}}`
	event, err := parseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parseEvent failed: %v", err)
	}
	if string(event.Raw) != raw {
		t.Errorf("Raw not preserved: got %q", string(event.Raw))
	}
}

// ── EventStream tests ──────────────────────────────────────────────────

// fakeReadCloser wraps an io.Reader to satisfy io.ReadCloser.
type fakeReadCloser struct {
	io.Reader
	closed bool
}

func (f *fakeReadCloser) Close() error {
	f.closed = true
	return nil
}

// newTestEventStream creates an EventStream using an io.Pipe for the SSE body,
// bypassing the HTTP connection entirely.
func newTestEventStream(t *testing.T, r io.ReadCloser) *EventStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	es := &EventStream{
		ctx:         ctx,
		cancel:      cancel,
		body:        r,
		subscribers: make(map[*subscriber]struct{}),
	}
	go es.readLoop()
	return es
}

func TestEventStream_SubscribeReceivesEvents(t *testing.T) {
	pr, pw := io.Pipe()

	es := newTestEventStream(t, pr)
	defer es.Close()

	ch, unsub := es.Subscribe(context.Background(), "")
	defer unsub()

	// Write an SSE event
	go func() {
		pw.Write([]byte("data: {\"type\":\"session.status\",\"properties\":{\"sessionID\":\"s1\",\"status\":{\"type\":\"idle\"}}}\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	select {
	case event := <-ch:
		if event.Type != EventSessionStatus {
			t.Errorf("type = %q, want %q", event.Type, EventSessionStatus)
		}
		if event.Properties.SessionID != "s1" {
			t.Errorf("SessionID = %q, want %q", event.Properties.SessionID, "s1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventStream_SessionFiltering(t *testing.T) {
	pr, pw := io.Pipe()

	es := newTestEventStream(t, pr)
	defer es.Close()

	// Subscribe only for session "s2"
	ch, unsub := es.Subscribe(context.Background(), "s2")
	defer unsub()

	go func() {
		// Event for s1 — should be filtered out
		pw.Write([]byte("data: {\"type\":\"session.status\",\"properties\":{\"sessionID\":\"s1\",\"status\":{\"type\":\"idle\"}}}\n"))
		// Event for s2 — should pass through
		pw.Write([]byte("data: {\"type\":\"session.status\",\"properties\":{\"sessionID\":\"s2\",\"status\":{\"type\":\"busy\"}}}\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	select {
	case event := <-ch:
		if event.Properties.SessionID != "s2" {
			t.Errorf("expected event for s2, got sessionID=%q", event.Properties.SessionID)
		}
		if event.Properties.Status.Type != "busy" {
			t.Errorf("expected status busy, got %q", event.Properties.Status.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for filtered event")
	}
}

func TestEventStream_MultipleSubscribers(t *testing.T) {
	pr, pw := io.Pipe()

	es := newTestEventStream(t, pr)
	defer es.Close()

	ch1, unsub1 := es.Subscribe(context.Background(), "")
	defer unsub1()
	ch2, unsub2 := es.Subscribe(context.Background(), "")
	defer unsub2()

	go func() {
		pw.Write([]byte("data: {\"type\":\"server.connected\",\"properties\":{}}\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case event := <-ch:
			if event.Type != EventServerConnected {
				t.Errorf("subscriber %d: type = %q, want %q", i, event.Type, EventServerConnected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}
}

func TestEventStream_UnsubscribeStopsDelivery(t *testing.T) {
	pr, pw := io.Pipe()

	es := newTestEventStream(t, pr)
	defer es.Close()

	ch, unsub := es.Subscribe(context.Background(), "")
	unsub() // unsubscribe immediately — channel should be closed

	go func() {
		pw.Write([]byte("data: {\"type\":\"server.connected\",\"properties\":{}}\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	// After unsubscribe, the underlying channel is closed.
	// Drain any buffered events then verify the channel is closed.
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed — correct behavior
			}
			// received a buffered event, keep draining
		case <-timeout:
			t.Fatal("channel was not closed after unsubscribe")
		}
	}
}

func TestEventStream_SkipsMalformedEvents(t *testing.T) {
	pr, pw := io.Pipe()

	es := newTestEventStream(t, pr)
	defer es.Close()

	ch, unsub := es.Subscribe(context.Background(), "")
	defer unsub()

	go func() {
		// Malformed event — should be skipped
		pw.Write([]byte("data: not-json\n"))
		// Non-data line — should be skipped
		pw.Write([]byte(": comment\n"))
		// Empty line — should be skipped
		pw.Write([]byte("\n"))
		// Valid event
		pw.Write([]byte("data: {\"type\":\"server.connected\",\"properties\":{}}\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	select {
	case event := <-ch:
		if event.Type != EventServerConnected {
			t.Errorf("type = %q, want %q", event.Type, EventServerConnected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out — malformed events may have blocked the stream")
	}
}

func TestEventStream_CloseClosesBody(t *testing.T) {
	body := &fakeReadCloser{Reader: strings.NewReader("")}
	ctx, cancel := context.WithCancel(context.Background())
	es := &EventStream{
		ctx:         ctx,
		cancel:      cancel,
		body:        body,
		subscribers: make(map[*subscriber]struct{}),
	}

	es.Close()

	if !body.closed {
		t.Error("expected body to be closed after EventStream.Close()")
	}
}

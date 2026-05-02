package opencode

import (
	"encoding/json"
	"testing"
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

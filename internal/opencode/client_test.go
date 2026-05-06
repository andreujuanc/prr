package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient creates a Client pointing at the given httptest.Server.
func newTestClient(ts *httptest.Server) *Client {
	return NewClient(ts.URL)
}

func TestNewClient(t *testing.T) {
	c := NewClient("http://localhost:1234")
	if c.BaseURL() != "http://localhost:1234" {
		t.Errorf("BaseURL() = %q, want %q", c.BaseURL(), "http://localhost:1234")
	}
}

// ── Health ──────────────────────────────────────────────────────────────

func TestHealth_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/global/health" {
			t.Errorf("expected path /global/health, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.2.3"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	resp, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if !resp.Healthy {
		t.Error("expected Healthy=true")
	}
	if resp.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", resp.Version, "1.2.3")
	}
}

func TestHealth_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestHealth_MalformedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestHealth_ContextCancelled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true})
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	c := newTestClient(ts)
	_, err := c.Health(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// ── CreateSession ───────────────────────────────────────────────────────

func TestCreateSession_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/session" {
			t.Errorf("expected path /session, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		// Verify request body
		var req CreateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if req.Title != "test session" {
			t.Errorf("expected title %q, got %q", "test session", req.Title)
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(Session{ID: "ses_123", Title: "test session"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	s, err := c.CreateSession(context.Background(), "test session")
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if s.ID != "ses_123" {
		t.Errorf("ID = %q, want %q", s.ID, "ses_123")
	}
	if s.Title != "test session" {
		t.Errorf("Title = %q, want %q", s.Title, "test session")
	}
}

func TestCreateSession_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.CreateSession(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

// ── SendPrompt ──────────────────────────────────────────────────────────

func TestSendPrompt_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/session/ses_abc/prompt_async" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req PromptRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if len(req.Parts) != 1 {
			t.Fatalf("expected 1 part, got %d", len(req.Parts))
		}
		if req.Parts[0].Type != "text" {
			t.Errorf("part type = %q, want %q", req.Parts[0].Type, "text")
		}
		if req.Parts[0].Text != "hello world" {
			t.Errorf("part text = %q, want %q", req.Parts[0].Text, "hello world")
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.SendPrompt(context.Background(), "ses_abc", "hello world")
	if err != nil {
		t.Fatalf("SendPrompt() error: %v", err)
	}
}

func TestSendPrompt_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.SendPrompt(context.Background(), "ses_abc", "hello")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

// ── Abort ───────────────────────────────────────────────────────────────

func TestAbort_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/session/ses_abc/abort" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	if err := c.Abort(context.Background(), "ses_abc"); err != nil {
		t.Fatalf("Abort() error: %v", err)
	}
}

func TestAbort_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	if err := c.Abort(context.Background(), "ses_abc"); err == nil {
		t.Fatal("expected error for 500 response")
	}
}

// ── RespondPermission ───────────────────────────────────────────────────

func TestRespondPermission_Approved(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/session/ses_1/permissions/perm_2" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var resp PermissionResponse
		if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if !resp.Approved {
			t.Error("expected Approved=true")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	if err := c.RespondPermission(context.Background(), "ses_1", "perm_2", true); err != nil {
		t.Fatalf("RespondPermission() error: %v", err)
	}
}

func TestRespondPermission_Denied(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var resp PermissionResponse
		if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if resp.Approved {
			t.Error("expected Approved=false")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	if err := c.RespondPermission(context.Background(), "ses_1", "perm_2", false); err != nil {
		t.Fatalf("RespondPermission() error: %v", err)
	}
}

func TestRespondPermission_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("forbidden"))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.RespondPermission(context.Background(), "ses_1", "perm_2", true)
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
}

// ── ReplyQuestion ───────────────────────────────────────────────────────

func TestReplyQuestion_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/question/q_123/reply" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req QuestionReplyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if len(req.Answers) != 1 || len(req.Answers[0]) != 2 {
			t.Fatalf("expected [[a,b]], got %v", req.Answers)
		}
		if req.Answers[0][0] != "Option A" || req.Answers[0][1] != "Option B" {
			t.Errorf("unexpected answers: %v", req.Answers)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.ReplyQuestion(context.Background(), "q_123", [][]string{{"Option A", "Option B"}})
	if err != nil {
		t.Fatalf("ReplyQuestion() error: %v", err)
	}
}

func TestReplyQuestion_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid"))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.ReplyQuestion(context.Background(), "q_123", [][]string{{"A"}})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

// ── RejectQuestion ──────────────────────────────────────────────────────

func TestRejectQuestion_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/question/q_456/reject" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		// Body should be nil/empty for reject
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 {
			t.Errorf("expected empty body, got %s", string(body))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	if err := c.RejectQuestion(context.Background(), "q_456"); err != nil {
		t.Fatalf("RejectQuestion() error: %v", err)
	}
}

func TestRejectQuestion_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.RejectQuestion(context.Background(), "q_456")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

// ── ConnectEvents ───────────────────────────────────────────────────────

func TestConnectEvents_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/event" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if accept := r.Header.Get("Accept"); accept != "text/event-stream" {
			t.Errorf("expected Accept text/event-stream, got %s", accept)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {}\n\n"))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	body, err := c.ConnectEvents(context.Background())
	if err != nil {
		t.Fatalf("ConnectEvents() error: %v", err)
	}
	defer body.Close()

	data, _ := io.ReadAll(body)
	if string(data) != "data: {}\n\n" {
		t.Errorf("unexpected body: %q", string(data))
	}
}

func TestConnectEvents_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.ConnectEvents(context.Background())
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
}

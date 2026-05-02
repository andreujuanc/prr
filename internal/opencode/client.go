package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client provides HTTP access to the OpenCode server API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new OpenCode API client for the given server URL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// BaseURL returns the server base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// ── Health ──────────────────────────────────────────────────────────────

// Health checks if the OpenCode server is running.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/global/health", nil)
	if err != nil {
		return nil, fmt.Errorf("health request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("health: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health: status %d", resp.StatusCode)
	}

	var h HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, fmt.Errorf("health decode: %w", err)
	}
	return &h, nil
}

// ── Sessions ────────────────────────────────────────────────────────────

// CreateSessionRequest is the body for POST /session.
type CreateSessionRequest struct {
	Title string `json:"title"`
}

// CreateSession creates a new session on the server.
func (c *Client) CreateSession(ctx context.Context, title string) (*Session, error) {
	body, err := json.Marshal(CreateSessionRequest{Title: title})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create session request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create session: status %d: %s", resp.StatusCode, string(respBody))
	}

	var s Session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("create session decode: %w", err)
	}
	return &s, nil
}

// ── Prompts ─────────────────────────────────────────────────────────────

// PromptPart is a single part of a prompt message.
type PromptPart struct {
	Type string `json:"type"` // "text"
	Text string `json:"text,omitempty"`
}

// PromptRequest is the body for POST /session/:id/prompt_async.
type PromptRequest struct {
	Parts []PromptPart `json:"parts"`
}

// SendPrompt sends a prompt to a session asynchronously.
// The server responds with 204 No Content immediately; results come via SSE.
func (c *Client) SendPrompt(ctx context.Context, sessionID string, text string) error {
	body, err := json.Marshal(PromptRequest{
		Parts: []PromptPart{{Type: "text", Text: text}},
	})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/session/%s/prompt_async", c.baseURL, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("send prompt request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send prompt: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("send prompt: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ── Abort ───────────────────────────────────────────────────────────────

// Abort cancels the current operation in a session.
func (c *Client) Abort(ctx context.Context, sessionID string) error {
	url := fmt.Sprintf("%s/session/%s/abort", c.baseURL, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("abort request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("abort: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("abort: status %d", resp.StatusCode)
	}
	return nil
}

// ── Permissions ─────────────────────────────────────────────────────────

// RespondPermission approves or denies a permission request.
func (c *Client) RespondPermission(ctx context.Context, sessionID, permissionID string, approved bool) error {
	body, err := json.Marshal(PermissionResponse{Approved: approved})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/session/%s/permissions/%s", c.baseURL, sessionID, permissionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("permission response request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("permission response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("permission response: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ── Questions ───────────────────────────────────────────────────────────

// ReplyQuestion answers a question with selected option labels.
// answers is a slice of label arrays, one per question in the prompt.
func (c *Client) ReplyQuestion(ctx context.Context, questionID string, answers [][]string) error {
	body, err := json.Marshal(QuestionReplyRequest{Answers: answers})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/question/%s/reply", c.baseURL, questionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("reply question request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("reply question: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("reply question: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// RejectQuestion dismisses a question without answering.
func (c *Client) RejectQuestion(ctx context.Context, questionID string) error {
	url := fmt.Sprintf("%s/question/%s/reject", c.baseURL, questionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("reject question request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("reject question: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("reject question: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ── Event stream ────────────────────────────────────────────────────────

// ConnectEvents opens the SSE event stream. The caller must close the
// returned body when done. Use ParseEventStream to consume events.
func (c *Client) ConnectEvents(ctx context.Context) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/event", nil)
	if err != nil {
		return nil, fmt.Errorf("event stream request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	// Use a client without timeout for the long-lived SSE connection
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("event stream connect: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("event stream: status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

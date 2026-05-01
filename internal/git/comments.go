package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
)

// ReviewComment represents a line-level review comment on a PR.
type ReviewComment struct {
	ID        int    `json:"id"`
	Path      string `json:"path"`
	Line      int    `json:"line"` // line number in the diff (new file side)
	Side      string `json:"side"` // "LEFT" or "RIGHT"
	Body      string `json:"body"`
	Author    string `json:"-"` // populated from user.login
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ghCommentResponse mirrors the JSON from GitHub's API for unmarshalling.
type ghCommentResponse struct {
	ID        int    `json:"id"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

// FetchReviewComments retrieves all line-level review comments for a PR.
func FetchReviewComments(prNumber string) ([]ReviewComment, error) {
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%s/comments", prNumber),
		"--paginate",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to fetch PR comments: %v\n%s", err, stderr.String())
	}

	var raw []ghCommentResponse
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse PR comments: %v", err)
	}

	comments := make([]ReviewComment, len(raw))
	for i, r := range raw {
		comments[i] = ReviewComment{
			ID:        r.ID,
			Path:      r.Path,
			Line:      r.Line,
			Side:      r.Side,
			Body:      r.Body,
			Author:    r.User.Login,
			CreatedAt: r.CreatedAt,
			UpdatedAt: r.UpdatedAt,
		}
	}

	return comments, nil
}

// CreateReviewComment posts a new line-level review comment on a PR.
func CreateReviewComment(prNumber, commitSHA, path, body string, line int, side string) (*ReviewComment, error) {
	payload := map[string]interface{}{
		"body":      body,
		"commit_id": commitSHA,
		"path":      path,
		"line":      line,
		"side":      side,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal comment payload: %v", err)
	}

	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%s/comments", prNumber),
		"--method", "POST",
		"--input", "-",
	)
	cmd.Stdin = bytes.NewReader(payloadBytes)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to create comment: %v\n%s", err, stderr.String())
	}

	var raw ghCommentResponse
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse created comment: %v", err)
	}

	return &ReviewComment{
		ID:        raw.ID,
		Path:      raw.Path,
		Line:      raw.Line,
		Side:      raw.Side,
		Body:      raw.Body,
		Author:    raw.User.Login,
		CreatedAt: raw.CreatedAt,
	}, nil
}

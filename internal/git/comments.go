package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
)

// ReviewComment represents a line-level review comment on a PR.
type ReviewComment struct {
	ID          int    `json:"id"`
	InReplyToID int    `json:"in_reply_to_id"` // non-zero for threaded replies
	Path        string `json:"path"`
	Line        int    `json:"line"` // line number in the diff (new file side)
	Side        string `json:"side"` // "LEFT" or "RIGHT"
	Body        string `json:"body"`
	Author      string `json:"-"` // populated from user.login
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ghCommentResponse mirrors the JSON from GitHub's API for unmarshalling.
type ghCommentResponse struct {
	ID          int    `json:"id"`
	InReplyToID int    `json:"in_reply_to_id"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Side        string `json:"side"`
	Body        string `json:"body"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	User        struct {
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
			ID:          r.ID,
			InReplyToID: r.InReplyToID,
			Path:        r.Path,
			Line:        r.Line,
			Side:        r.Side,
			Body:        r.Body,
			Author:      r.User.Login,
			CreatedAt:   r.CreatedAt,
			UpdatedAt:   r.UpdatedAt,
		}
	}

	// Replies from GitHub's API often have zero Line/empty Path/Side.
	// Inherit position from the parent (root) comment so they can be
	// placed correctly in the diff.
	byID := make(map[int]*ReviewComment, len(comments))
	for i := range comments {
		byID[comments[i].ID] = &comments[i]
	}
	for i := range comments {
		if comments[i].InReplyToID != 0 {
			if parent, ok := byID[comments[i].InReplyToID]; ok {
				if comments[i].Path == "" {
					comments[i].Path = parent.Path
				}
				if comments[i].Line == 0 {
					comments[i].Line = parent.Line
				}
				if comments[i].Side == "" {
					comments[i].Side = parent.Side
				}
			}
		}
	}

	return comments, nil
}

// CreateReviewComment posts a new line-level review comment on a PR.
func CreateReviewComment(prNumber, commitSHA, path, body string, line int, side string) (*ReviewComment, error) {
	payload := map[string]any{
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
		ID:          raw.ID,
		InReplyToID: raw.InReplyToID,
		Path:        raw.Path,
		Line:        raw.Line,
		Side:        raw.Side,
		Body:        raw.Body,
		Author:      raw.User.Login,
		CreatedAt:   raw.CreatedAt,
	}, nil
}

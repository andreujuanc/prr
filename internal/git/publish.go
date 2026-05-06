package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
)

// ReviewFindingComment is a single inline comment for a batch review submission.
type ReviewFindingComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
	Side string `json:"side"`
}

// SubmitReviewWithFindings creates a GitHub PR review with inline comments.
// Uses the GitHub REST API: POST repos/{owner}/{repo}/pulls/{pr}/reviews
func SubmitReviewWithFindings(prNumber, commitSHA, body string, comments []ReviewFindingComment) error {
	payload := map[string]interface{}{
		"commit_id": commitSHA,
		"body":      body,
		"event":     "COMMENT",
		"comments":  comments,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal review payload: %w", err)
	}

	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%s/reviews", prNumber),
		"--method", "POST",
		"--input", "-",
	)
	cmd.Stdin = bytes.NewReader(payloadBytes)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("submit review: %v\n%s", err, stderr.String())
	}

	return nil
}

// PostPRComment posts a general (non-line-specific) comment on a PR.
func PostPRComment(prNumber, body string) error {
	cmd := exec.Command("gh", "pr", "comment", prNumber, "--body", body)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("post PR comment: %v\n%s", err, stderr.String())
	}

	return nil
}

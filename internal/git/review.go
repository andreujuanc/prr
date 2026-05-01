package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
)

// PRListItem is a lightweight PR summary returned by ListPRs.
type PRListItem struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	HeadRefName string `json:"headRefName"`
	State       string `json:"state"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// ListPRs returns open pull requests for the current repository.
func ListPRs() ([]PRListItem, error) {
	cmd := exec.Command("gh", "pr", "list",
		"--state", "open",
		"--json", "number,title,author,headRefName,state,createdAt,updatedAt",
		"--limit", "30",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh pr list failed: %v\n%s", err, stderr.String())
	}

	var prs []PRListItem
	if err := json.Unmarshal(stdout.Bytes(), &prs); err != nil {
		return nil, fmt.Errorf("failed to parse gh output: %v", err)
	}

	return prs, nil
}

// SubmitReview submits a formal GitHub review (approve, request_changes, or comment).
// verdict must be one of: "APPROVE", "REQUEST_CHANGES", "COMMENT".
func SubmitReview(prNumber, verdict, body string) error {
	// Map to gh pr review flags
	var flag string
	switch verdict {
	case "APPROVE":
		flag = "--approve"
	case "REQUEST_CHANGES":
		flag = "--request-changes"
	case "COMMENT":
		flag = "--comment"
	default:
		return fmt.Errorf("invalid review verdict: %q", verdict)
	}

	args := []string{"pr", "review", prNumber, flag}
	if body != "" {
		args = append(args, "--body", body)
	}

	cmd := exec.Command("gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh pr review failed: %v\n%s", err, stderr.String())
	}

	return nil
}

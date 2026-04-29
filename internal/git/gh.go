package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
)

// FetchPR uses the GitHub CLI to retrieve PR metadata and file list.
func FetchPR(prNumber string) (*PullRequest, error) {
	// Specify the fields we want to retrieve
	fields := "number,title,body,baseRefName,headRefName,headRefOid,files"
	
	cmd := exec.Command("gh", "pr", "view", prNumber, "--json", fields)
	
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh pr view failed: %v\n%s", err, stderr.String())
	}
	
	var pr PullRequest
	if err := json.Unmarshal(stdout.Bytes(), &pr); err != nil {
		return nil, fmt.Errorf("failed to parse gh output: %v", err)
	}
	
	return &pr, nil
}

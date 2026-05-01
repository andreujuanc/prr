package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
)

// FetchPR uses the GitHub CLI to retrieve PR metadata and file list.
func FetchPR(prNumber string) (*PullRequest, error) {
	// Fetch PR metadata via GraphQL (exclude files — capped at 100 by GraphQL)
	fields := "number,title,body,state,baseRefName,headRefName,headRefOid,author,headRepository,reviewDecision"

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

	// Fetch all changed files via REST API (supports pagination beyond 100)
	files, err := fetchPRFiles(pr.Number)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch PR files: %w", err)
	}
	pr.Files = files

	// Fallback: if headRepository is empty, resolve from current repo
	if pr.HeadRepository.Owner.Login == "" || pr.HeadRepository.Name == "" {
		if owner, name, err := currentRepo(); err == nil {
			pr.HeadRepository.Owner.Login = owner
			pr.HeadRepository.Name = name
		}
	}

	return &pr, nil
}

// currentRepo returns the owner and name of the current GitHub repository
// by querying the gh CLI.
func currentRepo() (owner, name string, err error) {
	cmd := exec.Command("gh", "repo", "view", "--json", "owner,name")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", "", err
	}
	var repo struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &repo); err != nil {
		return "", "", err
	}
	return repo.Owner.Login, repo.Name, nil
}

// prFileREST matches the GitHub REST API response for PR file entries.
type prFileREST struct {
	Filename  string `json:"filename"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// fetchPRFiles retrieves all changed files for a PR using the REST API
// with automatic pagination (no 100-file cap).
// Uses {owner}/{repo} template variables resolved by gh to the current repo.
func fetchPRFiles(prNumber int) ([]PRFile, error) {
	endpoint := fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/files", prNumber)
	cmd := exec.Command("gh", "api", endpoint, "--paginate")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh api failed: %v\n%s", err, stderr.String())
	}

	var apiFiles []prFileREST
	if err := json.Unmarshal(stdout.Bytes(), &apiFiles); err != nil {
		return nil, fmt.Errorf("failed to parse file list: %v", err)
	}

	files := make([]PRFile, len(apiFiles))
	for i, f := range apiFiles {
		files[i] = PRFile{
			Path:      f.Filename,
			Additions: f.Additions,
			Deletions: f.Deletions,
		}
	}

	return files, nil
}

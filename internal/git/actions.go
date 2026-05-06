package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
)

// ── Workflow Run models ─────────────────────────────────────────────────

// WorkflowRun represents a GitHub Actions workflow run.
type WorkflowRun struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`     // "queued", "in_progress", "completed"
	Conclusion string `json:"conclusion"` // "success", "failure", "cancelled", "skipped", "neutral"
	URL        string `json:"html_url"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// WorkflowJob represents a single job within a workflow run.
type WorkflowJob struct {
	ID         int            `json:"id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`     // "queued", "in_progress", "completed"
	Conclusion string         `json:"conclusion"` // "success", "failure", "cancelled", "skipped"
	Steps      []WorkflowStep `json:"steps"`
}

// WorkflowStep represents a single step within a workflow job.
type WorkflowStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // "queued", "in_progress", "completed"
	Conclusion string `json:"conclusion"` // "success", "failure", "cancelled", "skipped"
	Number     int    `json:"number"`
}

// ── Aggregate status helpers ────────────────────────────────────────────

// ActionStatus represents the aggregate status across all workflow runs.
type ActionStatus int

const (
	ActionStatusNone       ActionStatus = iota // no runs found
	ActionStatusPassed                         // all completed successfully
	ActionStatusFailed                         // at least one failed
	ActionStatusInProgress                     // at least one in progress or queued
)

// AggregateActionStatus computes the overall status from a list of runs.
func AggregateActionStatus(runs []WorkflowRun) ActionStatus {
	if len(runs) == 0 {
		return ActionStatusNone
	}

	hasInProgress := false
	hasFailed := false

	for _, r := range runs {
		switch r.Status {
		case "queued", "in_progress":
			hasInProgress = true
		case "completed":
			switch r.Conclusion {
			case "failure", "timed_out":
				hasFailed = true
			}
		}
	}

	if hasInProgress {
		return ActionStatusInProgress
	}
	if hasFailed {
		return ActionStatusFailed
	}
	return ActionStatusPassed
}

// HasActiveRuns returns true if any run is still queued or in progress.
func HasActiveRuns(runs []WorkflowRun) bool {
	for _, r := range runs {
		if r.Status == "queued" || r.Status == "in_progress" {
			return true
		}
	}
	return false
}

// ── API functions ───────────────────────────────────────────────────────

// ghWorkflowRunsResponse mirrors the GitHub API response for listing runs.
type ghWorkflowRunsResponse struct {
	TotalCount int           `json:"total_count"`
	Runs       []WorkflowRun `json:"workflow_runs"`
}

// FetchWorkflowRuns retrieves all workflow runs for the given head SHA.
// Uses: gh api repos/{owner}/{repo}/actions/runs?head_sha={sha}
func FetchWorkflowRuns(headSHA string) ([]WorkflowRun, error) {
	endpoint := fmt.Sprintf("repos/{owner}/{repo}/actions/runs?head_sha=%s", headSHA)
	cmd := exec.Command("gh", "api", endpoint)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to fetch workflow runs: %v\n%s", err, stderr.String())
	}

	var resp ghWorkflowRunsResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse workflow runs: %v", err)
	}

	return resp.Runs, nil
}

// ghWorkflowJobsResponse mirrors the GitHub API response for listing jobs.
type ghWorkflowJobsResponse struct {
	TotalCount int           `json:"total_count"`
	Jobs       []WorkflowJob `json:"jobs"`
}

// FetchWorkflowJobs retrieves jobs for a specific workflow run.
// Uses: gh api repos/{owner}/{repo}/actions/runs/{id}/jobs
func FetchWorkflowJobs(runID int) ([]WorkflowJob, error) {
	endpoint := fmt.Sprintf("repos/{owner}/{repo}/actions/runs/%d/jobs", runID)
	cmd := exec.Command("gh", "api", endpoint)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to fetch workflow jobs: %v\n%s", err, stderr.String())
	}

	var resp ghWorkflowJobsResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse workflow jobs: %v", err)
	}

	return resp.Jobs, nil
}

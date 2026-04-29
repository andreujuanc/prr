package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// FetchRefs ensures that the local git repository has the latest
// commits for the base and head branches of the pull request.
func FetchRefs(base, head string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "fetch", "origin", base, head)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("git fetch timed out after 60s\n  Check your network connection and git remote")
		}
		return fmt.Errorf("git fetch failed: %v\n%s", err, stderr.String())
	}
	return nil
}

// GetRawDiff runs a git diff between the base and head branch for a specific file.
func GetRawDiff(base, head, file string) (string, error) {
	return GetRawDiffWithContext(base, head, file, 3)
}

// GetRawDiffWithContext runs a git diff with configurable context lines.
func GetRawDiffWithContext(base, head, file string, contextLines int) (string, error) {
	diffRange := fmt.Sprintf("origin/%s...origin/%s", base, head)
	cmd := exec.Command("git", "diff", fmt.Sprintf("-U%d", contextLines), diffRange, "--", file)
	
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff failed: %v\n%s", err, stderr.String())
	}
	
	return stdout.String(), nil
}

// GetStyledDiff runs a git diff and pipes it to delta to get ANSI-styled output.
func GetStyledDiff(base, head, file string) (string, error) {
	return GetStyledDiffWithContext(base, head, file, 3)
}

// GetStyledDiffWithContext runs a styled git diff with configurable context lines.
func GetStyledDiffWithContext(base, head, file string, contextLines int) (string, error) {
	diffRange := fmt.Sprintf("origin/%s...origin/%s", base, head)
	
	// Prepare the git command
	gitCmd := exec.Command("git", "diff", fmt.Sprintf("-U%d", contextLines), diffRange, "--", file)
	
	// Prepare the delta command with paging disabled
	deltaCmd := exec.Command("delta", "--paging=never")
	
	// Setup pipes
	gitStdout, err := gitCmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("error piping git diff stdout: %v", err)
	}
	
	deltaCmd.Stdin = gitStdout
	
	var deltaStdout, deltaStderr bytes.Buffer
	deltaCmd.Stdout = &deltaStdout
	deltaCmd.Stderr = &deltaStderr
	
	// Start git diff
	if err := gitCmd.Start(); err != nil {
		return "", fmt.Errorf("error starting git diff: %v", err)
	}
	
	// Start delta
	if err := deltaCmd.Start(); err != nil {
		return "", fmt.Errorf("error starting delta: %v\n%s", err, deltaStderr.String())
	}
	
	// Wait for commands to finish
	if err := gitCmd.Wait(); err != nil {
		return "", fmt.Errorf("git diff execution failed: %v", err)
	}
	
	if err := deltaCmd.Wait(); err != nil {
		return "", fmt.Errorf("delta execution failed: %v\n%s", err, deltaStderr.String())
	}
	
	return deltaStdout.String(), nil
}

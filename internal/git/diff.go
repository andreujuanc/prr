package git

import (
	"bytes"
	"fmt"
	"os/exec"
)

// FetchRefs ensures that the local git repository has the latest
// commits for the base and head branches of the pull request.
func FetchRefs(base, head string) error {
	// git fetch origin <base> <head>
	cmd := exec.Command("git", "fetch", "origin", base, head)
	
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git fetch failed: %v\n%s", err, stderr.String())
	}
	return nil
}

// GetRawDiff runs a git diff between the base and head branch for a specific file.
func GetRawDiff(base, head, file string) (string, error) {
	// Note: using origin/base...origin/head because we just fetched them
	diffRange := fmt.Sprintf("origin/%s...origin/%s", base, head)
	cmd := exec.Command("git", "diff", diffRange, "--", file)
	
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
	diffRange := fmt.Sprintf("origin/%s...origin/%s", base, head)
	
	// Prepare the git command
	gitCmd := exec.Command("git", "diff", diffRange, "--", file)
	
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

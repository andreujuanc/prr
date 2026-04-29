package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
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
		errMsg := stderr.String()
		if strings.Contains(strings.ToLower(errMsg), "authenticity of host") {
			return fmt.Errorf("SSH host key not trusted\n  Run: ssh-keyscan github.com >> ~/.ssh/known_hosts\n  Then retry prr")
		}
		return fmt.Errorf("git fetch failed: %v\n%s", err, errMsg)
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
	
	// Prepare the delta command styled to match the prr Catppuccin Mocha theme
	deltaCmd := exec.Command("delta",
		"--paging=never",
		"--dark",
		"--syntax-theme=Nord",

		// Added lines: subtle green tint
		"--plus-style", "syntax #1a2e22",
		"--plus-emph-style", "syntax #264032",
		"--plus-empty-line-marker-style", "syntax #1a2e22",

		// Removed lines: warm red tint
		"--minus-style", "syntax #3b1e26",
		"--minus-emph-style", "syntax #55293a",
		"--minus-empty-line-marker-style", "syntax #3b1e26",

		// Hunk headers: mauve text, boxed in surface color
		"--hunk-header-style", "file line-number #CBA6F7",
		"--hunk-header-decoration-style", "#45475A box",
		"--hunk-header-file-style", "#89B4FA bold",
		"--hunk-header-line-number-style", "#FAB387",

		// Line numbers
		"--line-numbers",
		"--line-numbers-minus-style", "#F38BA8",
		"--line-numbers-plus-style", "#A6E3A1",
		"--line-numbers-zero-style", "#45475A",
		"--line-numbers-left-format", "{nm:>3}│",
		"--line-numbers-right-format", "{np:>3}│ ",

		// File headers: blue accent with overline
		"--file-style", "bold #89B4FA",
		"--file-decoration-style", "#585B70 ol",
		"--file-modified-label", "modified:",
		"--file-added-label", "added:",
		"--file-removed-label", "removed:",

		// Context lines
		"--zero-style", "syntax",

		// Commit/meta styles
		"--commit-style", "#FAB387 bold",
		"--commit-decoration-style", "none",

		// Whitespace: don't highlight trailing whitespace
		"--whitespace-error-style", "normal",

		// Inline hints: show which words changed within a line
		"--inline-hint-style", "syntax",
	)
	
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
	
	return sanitizeANSI(deltaStdout.String()), nil
}

// sanitizeANSI strips terminal sequences that interfere with Bubble Tea's renderer:
// cursor movement, screen mode changes, and window title sequences.
// Preserves SGR (color/style) sequences which are \x1b[...m
var reProblematicANSI = regexp.MustCompile(
	`\x1b\[\d*[ABCDEFGHJKST]` + // cursor movement & clear
		`|\x1b\[\d*;\d*[Hf]` + // cursor positioning
		`|\x1b\[\?(?:25|47|1049)[hl]` + // show/hide cursor, alt screen
		`|\x1b\][^\x07]*\x07`, // OSC sequences (window title etc)
)

func sanitizeANSI(s string) string {
	return reProblematicANSI.ReplaceAllString(s, "")
}

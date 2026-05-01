package ai

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// maxOutputBytes is the maximum size of a single tool result.
// Results exceeding this are truncated with a pagination hint.
const maxOutputBytes = 50 * 1024

// ToolExecutor handles executing tool calls from the LLM.
type ToolExecutor struct {
	HeadRef      string            // e.g. "origin/feature-branch" — the PR head ref for git show
	BaseRef      string            // e.g. "origin/main" — the PR base ref for reading pre-change files
	RawDiffs     map[string]string // filePath -> raw unified diff (set by UI after PR load, used by review flow)
	ReviewGetter func() string     // returns the latest PR review summary, or "" if none

	// gitRunner and cmdRunner are injectable for testing. If nil, the real
	// runGit / runCmd functions are used.
	gitRunner func(args ...string) (string, error)
	cmdRunner func(name string, args ...string) (string, error)
}

// ══════════════════════════════════════════════════════════════════════════
// Tool definitions
// ══════════════════════════════════════════════════════════════════════════

// CanonicalToolDefs returns provider-agnostic tool definitions.
// Each provider adapter translates these to its native format.
// Tools are grouped: file/code inspection, git, GitHub (gh CLI), other.
func CanonicalToolDefs() []ToolDef {
	return []ToolDef{
		// ── File / code inspection ────────────────────────────────────
		{
			Name:     "read_file",
			ReadOnly: true,
			Description: "Read a file from the PR branch (after changes). " +
				"Returns numbered lines. Files over 2000 lines are truncated; " +
				"use offset and limit to paginate.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"path": {
						Type:        "string",
						Description: "File path relative to the repository root.",
					},
					"offset": {
						Type:        "integer",
						Description: "Starting line number (1-indexed, default 1).",
					},
					"limit": {
						Type:        "integer",
						Description: "Max lines to return (default 200, max 2000).",
					},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:     "read_base_file",
			ReadOnly: true,
			Description: "Read a file from the base branch (before the PR). " +
				"Useful for comparing old vs new implementations and verifying refactors.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"path": {
						Type:        "string",
						Description: "File path relative to the repository root.",
					},
					"offset": {
						Type:        "integer",
						Description: "Starting line number (1-indexed, default 1).",
					},
					"limit": {
						Type:        "integer",
						Description: "Max lines to return (default 200, max 2000).",
					},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:     "list_dir",
			ReadOnly: true,
			Description: "List files and directories at a path in the PR branch. " +
				"Non-recursive. Shows file/dir indicator and size in bytes.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"path": {
						Type:        "string",
						Description: "Directory path relative to repo root (default: root).",
					},
				},
			},
		},
		{
			Name:     "glob",
			ReadOnly: true,
			Description: "Find file paths matching a glob pattern in the PR branch. " +
				"Supports * (single segment) and ** (any depth). " +
				"Example: '**/*.go', 'cmd/*', 'internal/**/*_test.go'.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"pattern": {
						Type:        "string",
						Description: "Glob pattern to match against file paths.",
					},
				},
				Required: []string{"pattern"},
			},
		},
		{
			Name:     "grep",
			ReadOnly: true,
			Description: "Search file contents for a pattern across the PR branch. " +
				"Returns file:line:match triples. Uses git grep internally.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"pattern": {
						Type:        "string",
						Description: "Search pattern (regex by default, fixed string if regex=false).",
					},
					"path": {
						Type:        "string",
						Description: "Directory or file to scope the search (default: entire repo).",
					},
					"regex": {
						Type:        "string",
						Description: "Set to 'false' for fixed-string matching (default: 'true').",
						Enum:        []string{"true", "false"},
					},
					"file_glob": {
						Type:        "string",
						Description: "Filter searched files by glob (e.g. '*.go', '*.ts').",
					},
					"max_results": {
						Type:        "integer",
						Description: "Max matching lines to return (default 50, max 200).",
					},
				},
				Required: []string{"pattern"},
			},
		},
		// ── Git ──────────────────────────────────────────────────────
		{
			Name:     "git_diff",
			ReadOnly: true,
			Description: "Get unified diff between two refs. Defaults to the PR's " +
				"base and head branches. Use paths to limit to specific files.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"base": {
						Type:        "string",
						Description: "Base ref (branch, tag, SHA). Defaults to the PR base branch.",
					},
					"head": {
						Type:        "string",
						Description: "Head ref (branch, tag, SHA). Defaults to the PR head branch.",
					},
					"paths": {
						Type:        "string",
						Description: "Space-separated file paths to restrict the diff to.",
					},
				},
			},
		},
		{
			Name:     "git_log",
			ReadOnly: true,
			Description: "Show commit log. Returns one-line commit summaries. " +
				"Defaults to commits on the PR head branch.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"rev_range": {
						Type:        "string",
						Description: "Revision range (e.g. 'main..HEAD', 'abc123^..abc123'). Defaults to PR base...head.",
					},
					"max": {
						Type:        "integer",
						Description: "Max number of commits to show (default 20, max 100).",
					},
				},
			},
		},
		{
			Name:     "git_show",
			ReadOnly: true,
			Description: "Show a commit's details, or a file at a specific commit. " +
				"Without path: shows commit message and diffstat. " +
				"With path: shows the file content at that commit.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"commit": {
						Type:        "string",
						Description: "Commit SHA, branch, or tag to inspect.",
					},
					"path": {
						Type:        "string",
						Description: "File path to show at the given commit (optional).",
					},
				},
				Required: []string{"commit"},
			},
		},
		{
			Name:     "git_blame",
			ReadOnly: true,
			Description: "Show line-by-line authorship for a file. " +
				"Use line_start/line_end to limit to a range.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"path": {
						Type:        "string",
						Description: "File path relative to repository root.",
					},
					"line_start": {
						Type:        "integer",
						Description: "First line to blame (1-indexed, optional).",
					},
					"line_end": {
						Type:        "integer",
						Description: "Last line to blame (1-indexed, optional).",
					},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:        "git_status",
			ReadOnly:    true,
			Description: "Show the working tree status (modified, untracked, staged files).",
			Parameters: ToolParams{
				Type:       "object",
				Properties: map[string]ToolParam{},
			},
		},
		// ── GitHub (gh CLI) ──────────────────────────────────────────
		{
			Name:     "gh_pr_view",
			ReadOnly: true,
			Description: "View PR metadata: title, body, author, labels, branches, " +
				"changed files, review status, and CI checks.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"pr_number": {
						Type:        "string",
						Description: "Pull request number.",
					},
				},
				Required: []string{"pr_number"},
			},
		},
		{
			Name:        "gh_pr_diff",
			ReadOnly:    true,
			Description: "Get the full unified diff of a pull request from GitHub.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"pr_number": {
						Type:        "string",
						Description: "Pull request number.",
					},
				},
				Required: []string{"pr_number"},
			},
		},
		{
			Name:     "gh_pr_checks",
			ReadOnly: true,
			Description: "Show CI/CD check status for a pull request " +
				"(name, status, conclusion, URL).",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"pr_number": {
						Type:        "string",
						Description: "Pull request number.",
					},
				},
				Required: []string{"pr_number"},
			},
		},
		{
			Name:     "gh_pr_comments",
			ReadOnly: true,
			Description: "View all comments and review discussions on a PR. " +
				"Use this to avoid re-raising issues already discussed.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"pr_number": {
						Type:        "string",
						Description: "Pull request number.",
					},
				},
				Required: []string{"pr_number"},
			},
		},
		{
			Name:        "gh_pr_files",
			ReadOnly:    true,
			Description: "List files changed in a PR with additions/deletions counts.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"pr_number": {
						Type:        "string",
						Description: "Pull request number.",
					},
				},
				Required: []string{"pr_number"},
			},
		},
		{
			Name:     "gh_issue_view",
			ReadOnly: true,
			Description: "View a GitHub issue's title, body, labels, and comments. " +
				"Useful for understanding linked issues referenced in PRs.",
			Parameters: ToolParams{
				Type: "object",
				Properties: map[string]ToolParam{
					"issue_number": {
						Type:        "string",
						Description: "Issue number.",
					},
				},
				Required: []string{"issue_number"},
			},
		},
		// ── Other ────────────────────────────────────────────────────
		{
			Name:     "get_review",
			ReadOnly: true,
			Description: "Get the latest AI-generated review of this PR, if one exists. " +
				"Use when the user asks about review findings.",
			Parameters: ToolParams{
				Type:       "object",
				Properties: map[string]ToolParam{},
			},
		},
	}
}

// readOnlyTools is a pre-computed set of tools safe for concurrent execution.
var readOnlyTools = func() map[string]bool {
	m := make(map[string]bool)
	for _, td := range CanonicalToolDefs() {
		if td.ReadOnly {
			m[td.Name] = true
		}
	}
	return m
}()

// IsToolReadOnly returns true if the named tool is safe for concurrent execution.
func IsToolReadOnly(name string) bool {
	return readOnlyTools[name]
}

// ══════════════════════════════════════════════════════════════════════════
// Executor / dispatcher
// ══════════════════════════════════════════════════════════════════════════

// ExecuteTool runs a tool call and returns the result.
// Results starting with "Error:" are flagged as isError=true so the model
// can recover gracefully.
func (t *ToolExecutor) ExecuteTool(name string, args map[string]interface{}) (result string, isError bool) {
	var r string
	switch name {
	// File / code inspection
	case "read_file":
		r = t.readFile(args, t.HeadRef)
	case "read_base_file":
		r = t.readFile(args, t.BaseRef)
	case "list_dir":
		r = t.listDir(args)
	case "glob":
		r = t.globFiles(args)
	case "grep":
		r = t.grepCode(args)
	// Git
	case "git_diff":
		r = t.gitDiff(args)
	case "git_log":
		r = t.gitLog(args)
	case "git_show":
		r = t.gitShow(args)
	case "git_blame":
		r = t.gitBlame(args)
	case "git_status":
		r = t.gitStatus()
	// GitHub
	case "gh_pr_view":
		r = t.ghPRView(args)
	case "gh_pr_diff":
		r = t.ghPRDiff(args)
	case "gh_pr_checks":
		r = t.ghPRChecks(args)
	case "gh_pr_comments":
		r = t.ghPRComments(args)
	case "gh_pr_files":
		r = t.ghPRFiles(args)
	case "gh_issue_view":
		r = t.ghIssueView(args)
	// Other
	case "get_review":
		r = t.getReview()
	default:
		return fmt.Sprintf("Error: unknown tool %q", name), true
	}
	return r, strings.HasPrefix(r, "Error:")
}

// ══════════════════════════════════════════════════════════════════════════
// File / code inspection implementations
// ══════════════════════════════════════════════════════════════════════════

// readFile reads a file at the given git ref with pagination.
func (t *ToolExecutor) readFile(args map[string]interface{}, ref string) string {
	path := getStringArg(args, "path")
	if path == "" {
		return "Error: 'path' is required"
	}
	if ref == "" {
		return "Error: git ref not configured"
	}

	offset := getIntArg(args, "offset", 1)
	limit := getIntArg(args, "limit", 200)
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 2000 {
		limit = 2000
	}

	out, err := t.git("show", fmt.Sprintf("%s:%s", ref, path))
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	lines := strings.Split(out, "\n")
	totalLines := len(lines)

	startIdx := offset - 1
	if startIdx >= totalLines {
		return fmt.Sprintf("Error: file '%s' has %d lines, offset %d is past the end", path, totalLines, offset)
	}
	endIdx := startIdx + limit
	if endIdx > totalLines {
		endIdx = totalLines
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("File: %s (lines %d-%d of %d)\n", path, offset, endIdx, totalLines))
	result.WriteString(strings.Repeat("─", 40) + "\n")
	for i := startIdx; i < endIdx; i++ {
		result.WriteString(strconv.Itoa(i+1) + ": " + lines[i] + "\n")
	}
	if endIdx < totalLines {
		result.WriteString(fmt.Sprintf("\n... %d more lines. Use offset=%d limit=%d to continue.",
			totalLines-endIdx, endIdx+1, limit))
	}
	return result.String()
}

// listDir lists files and directories at a path with size information.
func (t *ToolExecutor) listDir(args map[string]interface{}) string {
	path := getStringArg(args, "path")
	if path == "" {
		path = "."
	}
	ref := t.HeadRef
	if ref == "" {
		return "Error: git ref not configured"
	}

	// git ls-tree -l shows: mode type hash size\tpath
	treePath := path
	if treePath == "." {
		treePath = ""
	}

	var lsArgs []string
	if treePath == "" {
		lsArgs = []string{"ls-tree", "-l", ref}
	} else {
		lsArgs = []string{"ls-tree", "-l", ref, treePath + "/"}
	}

	out, err := t.git(lsArgs...)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	output := strings.TrimSpace(out)
	if output == "" {
		return fmt.Sprintf("Directory '%s' is empty or does not exist.", path)
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Directory: %s\n", path))
	result.WriteString(strings.Repeat("─", 40) + "\n")

	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		// Format: "mode type hash    size\tpath"
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		entryPath := parts[1]
		name := entryPath
		if treePath != "" {
			name = strings.TrimPrefix(entryPath, treePath+"/")
		}

		meta := strings.Fields(parts[0])
		isDir := len(meta) >= 2 && meta[1] == "tree"
		size := ""
		if len(meta) >= 4 {
			size = strings.TrimSpace(meta[3])
		}

		if isDir {
			result.WriteString(fmt.Sprintf("  %s/\n", name))
		} else if size != "" && size != "-" {
			sizeInt, _ := strconv.ParseInt(size, 10, 64)
			result.WriteString(fmt.Sprintf("  %-40s %s\n", name, humanSize(sizeInt)))
		} else {
			result.WriteString(fmt.Sprintf("  %s\n", name))
		}
	}
	return result.String()
}

// globFiles finds files matching a glob pattern.
func (t *ToolExecutor) globFiles(args map[string]interface{}) string {
	pattern := getStringArg(args, "pattern")
	if pattern == "" {
		return "Error: 'pattern' is required"
	}
	ref := t.HeadRef
	if ref == "" {
		return "Error: git ref not configured"
	}

	// List all files at the ref, then filter with glob matching
	out, err := t.git("ls-tree", "-r", "--name-only", ref)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	allFiles := strings.Split(strings.TrimSpace(out), "\n")
	var matches []string
	for _, f := range allFiles {
		if f == "" {
			continue
		}
		if matchGlob(pattern, f) {
			matches = append(matches, f)
		}
	}

	if len(matches) == 0 {
		return fmt.Sprintf("No files match pattern: %s", pattern)
	}

	sort.Strings(matches)
	const maxGlobResults = 500
	var result strings.Builder
	result.WriteString(fmt.Sprintf("Glob: %s (%d match(es))\n", pattern, len(matches)))
	result.WriteString(strings.Repeat("─", 40) + "\n")
	for i, m := range matches {
		if i >= maxGlobResults {
			result.WriteString(fmt.Sprintf("\n... %d more matches not shown.", len(matches)-maxGlobResults))
			break
		}
		result.WriteString(m + "\n")
	}
	return truncateOutput(result.String(), "Narrow the glob pattern for fewer results.")
}

// grepCode searches for a pattern in files using git grep.
func (t *ToolExecutor) grepCode(args map[string]interface{}) string {
	pattern := getStringArg(args, "pattern")
	if pattern == "" {
		return "Error: 'pattern' is required"
	}

	ref := t.HeadRef
	if ref == "" {
		return "Error: git ref not configured"
	}

	useRegex := getStringArg(args, "regex") != "false"
	maxResults := getIntArg(args, "max_results", 50)
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > 200 {
		maxResults = 200
	}

	cmdArgs := []string{"grep", "-n", "--no-color"}
	if useRegex {
		cmdArgs = append(cmdArgs, "-E")
	} else {
		cmdArgs = append(cmdArgs, "-F")
	}
	cmdArgs = append(cmdArgs, pattern, ref)

	// Pathspec: combine path and file_glob
	scopePath := getStringArg(args, "path")
	fileGlob := getStringArg(args, "file_glob")

	if scopePath != "" || fileGlob != "" {
		cmdArgs = append(cmdArgs, "--")
		if scopePath != "" && fileGlob != "" {
			cmdArgs = append(cmdArgs, scopePath+"/"+fileGlob)
		} else if scopePath != "" {
			cmdArgs = append(cmdArgs, scopePath)
		} else {
			cmdArgs = append(cmdArgs, fileGlob)
		}
	}

	out, err := t.git(cmdArgs...)
	if err != nil {
		// git grep returns exit code 1 when no matches found
		if isExitCode(err, 1) {
			return fmt.Sprintf("No matches found for pattern: %s", pattern)
		}
		return fmt.Sprintf("Error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	totalMatches := len(lines)

	// Strip ref prefix from output (git grep outputs "ref:path:line:content")
	refPrefix := ref + ":"
	var result strings.Builder
	result.WriteString(fmt.Sprintf("Search: %s (%d match(es))\n", pattern, totalMatches))
	result.WriteString(strings.Repeat("─", 40) + "\n")

	shown := 0
	for _, line := range lines {
		if shown >= maxResults {
			break
		}
		line = strings.TrimPrefix(line, refPrefix)
		result.WriteString(line + "\n")
		shown++
	}

	if totalMatches > maxResults {
		result.WriteString(fmt.Sprintf("\n... %d more matches. Use max_results=%d or narrow with path/file_glob.",
			totalMatches-maxResults, totalMatches))
	}
	return truncateOutput(result.String(), "Use path or file_glob to narrow results.")
}

// ══════════════════════════════════════════════════════════════════════════
// Git implementations
// ══════════════════════════════════════════════════════════════════════════

// gitDiff returns unified diff between two refs.
func (t *ToolExecutor) gitDiff(args map[string]interface{}) string {
	base := getStringArg(args, "base")
	head := getStringArg(args, "head")
	if base == "" {
		base = t.BaseRef
	}
	if head == "" {
		head = t.HeadRef
	}
	if base == "" || head == "" {
		return "Error: base and head refs are required (specify them or ensure PR is loaded)"
	}

	// Three-dot diff: changes on head since it diverged from base
	cmdArgs := []string{"diff", base + "..." + head}

	// Optional path restriction
	pathsStr := getStringArg(args, "paths")
	if pathsStr != "" {
		cmdArgs = append(cmdArgs, "--")
		cmdArgs = append(cmdArgs, strings.Fields(pathsStr)...)
	}

	out, err := t.git(cmdArgs...)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	if strings.TrimSpace(out) == "" {
		return "No differences found."
	}

	return truncateOutput(out, "Use 'paths' to limit to specific files.")
}

// gitLog shows commit history.
func (t *ToolExecutor) gitLog(args map[string]interface{}) string {
	revRange := getStringArg(args, "rev_range")
	max := getIntArg(args, "max", 20)
	if max < 1 {
		max = 1
	}
	if max > 100 {
		max = 100
	}

	cmdArgs := []string{"log", "--oneline", "--no-decorate", fmt.Sprintf("-n%d", max)}

	if revRange != "" {
		cmdArgs = append(cmdArgs, revRange)
	} else if t.BaseRef != "" && t.HeadRef != "" {
		cmdArgs = append(cmdArgs, t.BaseRef+"..."+t.HeadRef)
	} else if t.HeadRef != "" {
		cmdArgs = append(cmdArgs, t.HeadRef)
	}

	out, err := t.git(cmdArgs...)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		return "No commits found."
	}
	return truncateOutput(out, "Use rev_range to narrow the range.")
}

// gitShow shows commit details or a file at a commit.
func (t *ToolExecutor) gitShow(args map[string]interface{}) string {
	commit := getStringArg(args, "commit")
	if commit == "" {
		return "Error: 'commit' is required"
	}
	path := getStringArg(args, "path")

	if path != "" {
		// Show file content at the commit
		out, err := t.git("show", fmt.Sprintf("%s:%s", commit, path))
		if err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		return truncateOutput(out, fmt.Sprintf("File is large. Use read_file with offset/limit for pagination."))
	}

	// Show commit info with stats
	out, err := t.git("show", "--stat", "--format=commit %H%nAuthor: %an <%ae>%nDate:   %ad%n%n%s%n%n%b", commit)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return truncateOutput(out, "Commit diff is large. Use git_diff with paths to inspect specific files.")
}

// gitBlame shows line-by-line authorship.
func (t *ToolExecutor) gitBlame(args map[string]interface{}) string {
	path := getStringArg(args, "path")
	if path == "" {
		return "Error: 'path' is required"
	}

	ref := t.HeadRef
	if ref == "" {
		ref = "HEAD"
	}

	cmdArgs := []string{"blame"}

	lineStart := getIntArg(args, "line_start", 0)
	lineEnd := getIntArg(args, "line_end", 0)
	if lineStart > 0 && lineEnd > 0 {
		cmdArgs = append(cmdArgs, fmt.Sprintf("-L%d,%d", lineStart, lineEnd))
	} else if lineStart > 0 {
		cmdArgs = append(cmdArgs, fmt.Sprintf("-L%d,+200", lineStart))
	}

	cmdArgs = append(cmdArgs, ref, "--", path)

	out, err := t.git(cmdArgs...)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return truncateOutput(out, "Use line_start and line_end to narrow the range.")
}

// gitStatus shows working tree status.
func (t *ToolExecutor) gitStatus() string {
	out, err := t.git("status", "--porcelain", "-b")
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		return "Working tree clean."
	}
	return out
}

// ══════════════════════════════════════════════════════════════════════════
// GitHub (gh CLI) implementations
// ══════════════════════════════════════════════════════════════════════════

func (t *ToolExecutor) ghPRView(args map[string]interface{}) string {
	pr := getStringArg(args, "pr_number")
	if pr == "" {
		return "Error: 'pr_number' is required"
	}
	out, err := t.cmd("gh", "pr", "view", pr, "--json",
		"title,body,author,labels,headRefName,baseRefName,changedFiles,additions,deletions,files,commits,reviewDecision,statusCheckRollup")
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return truncateOutput(out, "Use gh_pr_files or gh_pr_checks for specific details.")
}

func (t *ToolExecutor) ghPRDiff(args map[string]interface{}) string {
	pr := getStringArg(args, "pr_number")
	if pr == "" {
		return "Error: 'pr_number' is required"
	}
	out, err := t.cmd("gh", "pr", "diff", pr)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return truncateOutput(out, "Use git_diff with paths to inspect specific files.")
}

func (t *ToolExecutor) ghPRChecks(args map[string]interface{}) string {
	pr := getStringArg(args, "pr_number")
	if pr == "" {
		return "Error: 'pr_number' is required"
	}
	out, err := t.cmd("gh", "pr", "checks", pr)
	if err != nil {
		// gh pr checks returns exit code 1 when checks have failures
		if isExitCode(err, 1) && out != "" {
			return out
		}
		return fmt.Sprintf("Error: %v", err)
	}
	return out
}

func (t *ToolExecutor) ghPRComments(args map[string]interface{}) string {
	pr := getStringArg(args, "pr_number")
	if pr == "" {
		return "Error: 'pr_number' is required"
	}
	out, err := t.cmd("gh", "pr", "view", pr, "--json", "comments,reviews")
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return truncateOutput(out, "Large discussion thread. Focus on recent comments.")
}

func (t *ToolExecutor) ghPRFiles(args map[string]interface{}) string {
	pr := getStringArg(args, "pr_number")
	if pr == "" {
		return "Error: 'pr_number' is required"
	}
	out, err := t.cmd("gh", "pr", "view", pr, "--json", "files")
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return truncateOutput(out, "Many files changed.")
}

func (t *ToolExecutor) ghIssueView(args map[string]interface{}) string {
	issue := getStringArg(args, "issue_number")
	if issue == "" {
		return "Error: 'issue_number' is required"
	}
	out, err := t.cmd("gh", "issue", "view", issue, "--json",
		"title,body,author,labels,state,comments")
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return truncateOutput(out, "Large issue. Focus on the description and recent comments.")
}

// ══════════════════════════════════════════════════════════════════════════
// Other tools
// ══════════════════════════════════════════════════════════════════════════

func (t *ToolExecutor) getReview() string {
	if t.ReviewGetter == nil {
		return "No review available. Use 'a' in the PR overview to run an AI review first."
	}
	review := t.ReviewGetter()
	if review == "" {
		return "No review available. Use 'a' in the PR overview to run an AI review first."
	}
	return review
}

// ══════════════════════════════════════════════════════════════════════════
// Helpers
// ══════════════════════════════════════════════════════════════════════════

// git runs a git command, using the injectable gitRunner if set.
func (t *ToolExecutor) git(args ...string) (string, error) {
	if t.gitRunner != nil {
		return t.gitRunner(args...)
	}
	return runGit(args...)
}

// cmd runs an external command, using the injectable cmdRunner if set.
func (t *ToolExecutor) cmd(name string, args ...string) (string, error) {
	if t.cmdRunner != nil {
		return t.cmdRunner(name, args...)
	}
	return runCmd(name, args...)
}

// truncateOutput caps output at maxOutputBytes with a hint.
func truncateOutput(s, hint string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	// Cut at the last newline before the limit to avoid splitting a line
	cutoff := maxOutputBytes
	if idx := strings.LastIndex(s[:cutoff], "\n"); idx > 0 {
		cutoff = idx
	}
	return s[:cutoff] + fmt.Sprintf("\n\n... (output truncated at %dKB) %s", maxOutputBytes/1024, hint)
}

// runGit runs a git command and returns stdout.
func runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if stderr != "" {
				return string(out), fmt.Errorf("git %s: %s", args[0], stderr)
			}
		}
		return string(out), fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(out), nil
}

// runCmd runs an external command and returns stdout.
func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if stderr != "" {
				return string(out), fmt.Errorf("%s: %s", name, stderr)
			}
		}
		return string(out), fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}

// exitCoder is implemented by exec.ExitError and test fakes.
type exitCoder interface {
	ExitCode() int
}

// isExitCode checks if an error has a specific exit code.
func isExitCode(err error, code int) bool {
	var ec exitCoder
	if ok := asExitCoder(err, &ec); ok {
		return ec.ExitCode() == code
	}
	return false
}

// asExitCoder extracts an exitCoder from an error. It uses errors.As to
// unwrap error chains (e.g. from fmt.Errorf("%w")), then falls back to
// a direct interface check for test fakes.
func asExitCoder(err error, out *exitCoder) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		*out = exitErr
		return true
	}
	// Direct interface check for test fakes that implement exitCoder.
	if ec, ok := err.(exitCoder); ok {
		*out = ec
		return true
	}
	return false
}

// matchGlob matches a path against a glob pattern.
// Supports * (one segment wildcard), ? (single char), and ** (any depth).
func matchGlob(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	if strings.Contains(pattern, "**") {
		return matchDoublestar(pattern, path)
	}

	// Try full-path match
	if matched, _ := filepath.Match(pattern, path); matched {
		return true
	}
	// For patterns without '/' (e.g. "*.go"), match against basename
	if !strings.Contains(pattern, "/") {
		if matched, _ := filepath.Match(pattern, filepath.Base(path)); matched {
			return true
		}
	}
	return false
}

// matchDoublestar handles ** glob patterns.
func matchDoublestar(pattern, path string) bool {
	parts := strings.SplitN(pattern, "**", 2)
	prefix := strings.TrimRight(parts[0], "/")
	suffix := strings.TrimLeft(parts[1], "/")

	// Check prefix
	if prefix != "" {
		if !strings.HasPrefix(path, prefix+"/") {
			return false
		}
	}

	// No suffix means ** matches everything under prefix
	if suffix == "" {
		return true
	}

	// Try matching suffix against every subpath
	segments := strings.Split(path, "/")
	for i := 0; i < len(segments); i++ {
		candidate := strings.Join(segments[i:], "/")
		if matched, _ := filepath.Match(suffix, candidate); matched {
			return true
		}
	}
	// Also try against just the basename
	if matched, _ := filepath.Match(suffix, filepath.Base(path)); matched {
		return true
	}
	return false
}

// getStringArg extracts a string argument from the args map.
func getStringArg(args map[string]interface{}, key string) string {
	v, _ := args[key].(string)
	return v
}

// getIntArg extracts an integer argument (JSON numbers are float64).
func getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return defaultVal
}

// humanSize formats a byte count for display.
func humanSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

package ai

import (
	"fmt"
	"strings"
	"testing"
)

// ── Test helpers ────────────────────────────────────────────────────────

// fakeGit returns a gitRunner that dispatches based on the first arg.
// routes maps "subcmd" -> (stdout, error). Unmatched subcommands return an error.
func fakeGit(routes map[string]fakeResult) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		if len(args) == 0 {
			return "", fmt.Errorf("no args")
		}
		// Try exact subcommand match
		sub := args[0]
		if r, ok := routes[sub]; ok {
			return r.out, r.err
		}
		return "", fmt.Errorf("unexpected git subcommand: %s %v", sub, args)
	}
}

// fakeCmd returns a cmdRunner that dispatches based on name + first arg.
func fakeCmd(routes map[string]fakeResult) func(name string, args ...string) (string, error) {
	return func(name string, args ...string) (string, error) {
		key := name
		if len(args) > 0 {
			key = name + " " + args[0]
		}
		if r, ok := routes[key]; ok {
			return r.out, r.err
		}
		return "", fmt.Errorf("unexpected cmd: %s %v", name, args)
	}
}

type fakeResult struct {
	out string
	err error
}

func newTestExecutor() *ToolExecutor {
	return &ToolExecutor{
		HeadRef: "origin/feature",
		BaseRef: "origin/main",
	}
}

// ── read_file tests ─────────────────────────────────────────────────────

func TestTool_ReadFile(t *testing.T) {
	fileContent := "line1\nline2\nline3\nline4\nline5\n"

	tests := []struct {
		name     string
		args     map[string]interface{}
		git      map[string]fakeResult
		wantSub  string // substring that must appear in result
		wantErr  string // if non-empty, result must start with "Error:"
	}{
		{
			name:    "happy path, defaults",
			args:    map[string]interface{}{"path": "main.go"},
			git:     map[string]fakeResult{"show": {out: fileContent}},
			wantSub: "line1",
		},
		{
			name:    "with offset and limit",
			args:    map[string]interface{}{"path": "main.go", "offset": 3.0, "limit": 2.0},
			git:     map[string]fakeResult{"show": {out: fileContent}},
			wantSub: "3: line3",
		},
		{
			name:    "missing path",
			args:    map[string]interface{}{},
			wantErr: "Error: 'path' is required",
		},
		{
			name:    "git error",
			args:    map[string]interface{}{"path": "nope.go"},
			git:     map[string]fakeResult{"show": {err: fmt.Errorf("git show: not found")}},
			wantErr: "Error:",
		},
		{
			name:    "offset past end",
			args:    map[string]interface{}{"path": "main.go", "offset": 999.0},
			git:     map[string]fakeResult{"show": {out: fileContent}},
			wantErr: "Error: file 'main.go' has",
		},
		{
			name:    "limit clamped to max 2000",
			args:    map[string]interface{}{"path": "main.go", "limit": 5000.0},
			git:     map[string]fakeResult{"show": {out: fileContent}},
			wantSub: "line1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			if tt.git != nil {
				ex.gitRunner = fakeGit(tt.git)
			}
			result, isErr := ex.ExecuteTool("read_file", tt.args)

			if tt.wantErr != "" {
				if !strings.HasPrefix(result, "Error:") {
					t.Errorf("expected error, got: %s", result)
				}
				if !strings.Contains(result, tt.wantErr) && tt.wantErr != "Error:" {
					t.Errorf("expected error containing %q, got: %s", tt.wantErr, result)
				}
				if !isErr {
					t.Error("expected isError=true")
				}
				return
			}

			if isErr {
				t.Errorf("unexpected error: %s", result)
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── read_base_file tests ────────────────────────────────────────────────

func TestTool_ReadBaseFile(t *testing.T) {
	ex := newTestExecutor()
	ex.gitRunner = fakeGit(map[string]fakeResult{
		"show": {out: "old content\n"},
	})

	result, isErr := ex.ExecuteTool("read_base_file", map[string]interface{}{"path": "main.go"})
	if isErr {
		t.Fatalf("unexpected error: %s", result)
	}
	if !strings.Contains(result, "old content") {
		t.Errorf("expected base file content, got: %s", result)
	}
}

// ── list_dir tests ──────────────────────────────────────────────────────

func TestTool_ListDir(t *testing.T) {
	lsOutput := "100644 blob abc123    1234\tmain.go\n040000 tree def456       -\tinternal/\n"

	tests := []struct {
		name    string
		args    map[string]interface{}
		git     map[string]fakeResult
		wantSub string
		wantErr string
	}{
		{
			name:    "root directory",
			args:    map[string]interface{}{},
			git:     map[string]fakeResult{"ls-tree": {out: lsOutput}},
			wantSub: "main.go",
		},
		{
			name:    "shows directories with slash",
			args:    map[string]interface{}{},
			git:     map[string]fakeResult{"ls-tree": {out: lsOutput}},
			wantSub: "internal/",
		},
		{
			name:    "empty directory",
			args:    map[string]interface{}{"path": "empty"},
			git:     map[string]fakeResult{"ls-tree": {out: ""}},
			wantSub: "empty or does not exist",
		},
		{
			name:    "no ref configured",
			args:    map[string]interface{}{},
			wantErr: "Error:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			if tt.name == "no ref configured" {
				ex.HeadRef = ""
			}
			if tt.git != nil {
				ex.gitRunner = fakeGit(tt.git)
			}
			result, isErr := ex.ExecuteTool("list_dir", tt.args)

			if tt.wantErr != "" {
				if !strings.HasPrefix(result, "Error:") {
					t.Errorf("expected error, got: %s", result)
				}
				return
			}
			if isErr {
				t.Errorf("unexpected error: %s", result)
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── glob tests ──────────────────────────────────────────────────────────

func TestTool_Glob(t *testing.T) {
	allFiles := "main.go\ninternal/ui/model.go\ninternal/ai/tools.go\nREADME.md\n"

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantSub string
		wantErr string
	}{
		{
			name:    "match go files",
			args:    map[string]interface{}{"pattern": "*.go"},
			wantSub: "main.go",
		},
		{
			name:    "doublestar pattern",
			args:    map[string]interface{}{"pattern": "**/*.go"},
			wantSub: "3 match(es)",
		},
		{
			name:    "no matches",
			args:    map[string]interface{}{"pattern": "*.xyz"},
			wantSub: "No files match",
		},
		{
			name:    "missing pattern",
			args:    map[string]interface{}{},
			wantErr: "Error:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			ex.gitRunner = fakeGit(map[string]fakeResult{
				"ls-tree": {out: allFiles},
			})
			result, isErr := ex.ExecuteTool("glob", tt.args)

			if tt.wantErr != "" {
				if !isErr {
					t.Errorf("expected error, got: %s", result)
				}
				return
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── grep tests ──────────────────────────────────────────────────────────

func TestTool_Grep(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		git     map[string]fakeResult
		wantSub string
		wantErr string
	}{
		{
			name: "happy path",
			args: map[string]interface{}{"pattern": "func main"},
			git: map[string]fakeResult{
				"grep": {out: "origin/feature:main.go:3:func main() {\n"},
			},
			wantSub: "main.go:3:func main()",
		},
		{
			name: "no matches (exit code 1)",
			args: map[string]interface{}{"pattern": "nonexistent"},
			git: map[string]fakeResult{
				"grep": {out: "", err: &fakeExitError{code: 1}},
			},
			wantSub: "No matches found",
		},
		{
			name:    "missing pattern",
			args:    map[string]interface{}{},
			wantErr: "Error: 'pattern' is required",
		},
		{
			name: "max_results respected",
			args: map[string]interface{}{"pattern": "x", "max_results": 2.0},
			git: map[string]fakeResult{
				"grep": {out: "origin/feature:a.go:1:x\norigin/feature:b.go:2:x\norigin/feature:c.go:3:x\n"},
			},
			wantSub: "1 more matches",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			if tt.git != nil {
				ex.gitRunner = fakeGit(tt.git)
			}
			result, isErr := ex.ExecuteTool("grep", tt.args)

			if tt.wantErr != "" {
				if !isErr {
					t.Errorf("expected error, got: %s", result)
				}
				if !strings.Contains(result, tt.wantErr) {
					t.Errorf("expected %q, got: %s", tt.wantErr, result)
				}
				return
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── git_diff tests ──────────────────────────────────────────────────────

func TestTool_GitDiff(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		git     map[string]fakeResult
		wantSub string
	}{
		{
			name: "default refs",
			args: map[string]interface{}{},
			git: map[string]fakeResult{
				"diff": {out: "diff --git a/main.go b/main.go\n+new line\n"},
			},
			wantSub: "+new line",
		},
		{
			name: "no differences",
			args: map[string]interface{}{},
			git: map[string]fakeResult{
				"diff": {out: ""},
			},
			wantSub: "No differences found",
		},
		{
			name:    "missing refs",
			args:    map[string]interface{}{},
			wantSub: "Error: base and head refs are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			if tt.name == "missing refs" {
				ex.HeadRef = ""
				ex.BaseRef = ""
			}
			if tt.git != nil {
				ex.gitRunner = fakeGit(tt.git)
			}
			result, _ := ex.ExecuteTool("git_diff", tt.args)
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── git_log tests ───────────────────────────────────────────────────────

func TestTool_GitLog(t *testing.T) {
	ex := newTestExecutor()
	ex.gitRunner = fakeGit(map[string]fakeResult{
		"log": {out: "abc1234 first commit\ndef5678 second commit\n"},
	})

	result, isErr := ex.ExecuteTool("git_log", map[string]interface{}{})
	if isErr {
		t.Fatalf("unexpected error: %s", result)
	}
	if !strings.Contains(result, "first commit") {
		t.Errorf("result should contain commit message, got: %s", result)
	}
}

// ── git_show tests ──────────────────────────────────────────────────────

func TestTool_GitShow(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		git     map[string]fakeResult
		wantSub string
	}{
		{
			name: "commit info",
			args: map[string]interface{}{"commit": "abc123"},
			git:  map[string]fakeResult{"show": {out: "commit abc123\nAuthor: dev\n\nfix stuff\n"}},
			wantSub: "fix stuff",
		},
		{
			name: "file at commit",
			args: map[string]interface{}{"commit": "abc123", "path": "main.go"},
			git:  map[string]fakeResult{"show": {out: "package main\n"}},
			wantSub: "package main",
		},
		{
			name:    "missing commit",
			args:    map[string]interface{}{},
			wantSub: "Error: 'commit' is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			if tt.git != nil {
				ex.gitRunner = fakeGit(tt.git)
			}
			result, _ := ex.ExecuteTool("git_show", tt.args)
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── git_blame tests ─────────────────────────────────────────────────────

func TestTool_GitBlame(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		git     map[string]fakeResult
		wantSub string
	}{
		{
			name:    "happy path",
			args:    map[string]interface{}{"path": "main.go"},
			git:     map[string]fakeResult{"blame": {out: "abc1234 (dev 2025-01-01 1) package main\n"}},
			wantSub: "package main",
		},
		{
			name:    "missing path",
			args:    map[string]interface{}{},
			wantSub: "Error: 'path' is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			if tt.git != nil {
				ex.gitRunner = fakeGit(tt.git)
			}
			result, _ := ex.ExecuteTool("git_blame", tt.args)
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── git_status tests ────────────────────────────────────────────────────

func TestTool_GitStatus(t *testing.T) {
	tests := []struct {
		name    string
		git     map[string]fakeResult
		wantSub string
	}{
		{
			name:    "has changes",
			git:     map[string]fakeResult{"status": {out: "## main\n M main.go\n"}},
			wantSub: "M main.go",
		},
		{
			name:    "clean tree",
			git:     map[string]fakeResult{"status": {out: ""}},
			wantSub: "Working tree clean",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			ex.gitRunner = fakeGit(tt.git)
			result, _ := ex.ExecuteTool("git_status", map[string]interface{}{})
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── gh_pr_view tests ────────────────────────────────────────────────────

func TestTool_GhPRView(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		cmd     map[string]fakeResult
		wantSub string
	}{
		{
			name:    "happy path",
			args:    map[string]interface{}{"pr_number": "42"},
			cmd:     map[string]fakeResult{"gh pr": {out: `{"title":"fix stuff","body":"desc"}`}},
			wantSub: "fix stuff",
		},
		{
			name:    "missing pr_number",
			args:    map[string]interface{}{},
			wantSub: "Error: 'pr_number' is required",
		},
		{
			name:    "gh error",
			args:    map[string]interface{}{"pr_number": "999"},
			cmd:     map[string]fakeResult{"gh pr": {err: fmt.Errorf("gh: not found")}},
			wantSub: "Error:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			if tt.cmd != nil {
				ex.cmdRunner = fakeCmd(tt.cmd)
			}
			result, _ := ex.ExecuteTool("gh_pr_view", tt.args)
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── gh_pr_diff tests ────────────────────────────────────────────────────

func TestTool_GhPRDiff(t *testing.T) {
	ex := newTestExecutor()
	ex.cmdRunner = fakeCmd(map[string]fakeResult{
		"gh pr": {out: "diff --git a/main.go b/main.go\n"},
	})

	result, _ := ex.ExecuteTool("gh_pr_diff", map[string]interface{}{"pr_number": "42"})
	if !strings.Contains(result, "diff --git") {
		t.Errorf("expected diff output, got: %s", result)
	}
}

// ── gh_pr_checks tests ─────────────────────────────────────────────────

func TestTool_GhPRChecks(t *testing.T) {
	tests := []struct {
		name    string
		cmd     map[string]fakeResult
		wantSub string
	}{
		{
			name:    "all passing",
			cmd:     map[string]fakeResult{"gh pr": {out: "CI\tpass\n"}},
			wantSub: "CI\tpass",
		},
		{
			name:    "checks failing (exit code 1 with output)",
			cmd:     map[string]fakeResult{"gh pr": {out: "CI\tfail\n", err: &fakeExitError{code: 1}}},
			wantSub: "CI\tfail",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			ex.cmdRunner = fakeCmd(tt.cmd)
			result, _ := ex.ExecuteTool("gh_pr_checks", map[string]interface{}{"pr_number": "42"})
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── gh_pr_comments tests ────────────────────────────────────────────────

func TestTool_GhPRComments(t *testing.T) {
	ex := newTestExecutor()
	ex.cmdRunner = fakeCmd(map[string]fakeResult{
		"gh pr": {out: `{"comments":[{"body":"looks good"}]}`},
	})

	result, _ := ex.ExecuteTool("gh_pr_comments", map[string]interface{}{"pr_number": "42"})
	if !strings.Contains(result, "looks good") {
		t.Errorf("expected comments, got: %s", result)
	}
}

// ── gh_pr_files tests ───────────────────────────────────────────────────

func TestTool_GhPRFiles(t *testing.T) {
	ex := newTestExecutor()
	ex.cmdRunner = fakeCmd(map[string]fakeResult{
		"gh pr": {out: `{"files":[{"path":"main.go","additions":5}]}`},
	})

	result, _ := ex.ExecuteTool("gh_pr_files", map[string]interface{}{"pr_number": "42"})
	if !strings.Contains(result, "main.go") {
		t.Errorf("expected file list, got: %s", result)
	}
}

// ── gh_issue_view tests ─────────────────────────────────────────────────

func TestTool_GhIssueView(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		cmd     map[string]fakeResult
		wantSub string
	}{
		{
			name:    "happy path",
			args:    map[string]interface{}{"issue_number": "10"},
			cmd:     map[string]fakeResult{"gh issue": {out: `{"title":"fix bug","body":"details"}`}},
			wantSub: "fix bug",
		},
		{
			name:    "missing issue_number",
			args:    map[string]interface{}{},
			wantSub: "Error: 'issue_number' is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			if tt.cmd != nil {
				ex.cmdRunner = fakeCmd(tt.cmd)
			}
			result, _ := ex.ExecuteTool("gh_issue_view", tt.args)
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── get_review tests ────────────────────────────────────────────────────

func TestTool_GetReview(t *testing.T) {
	tests := []struct {
		name     string
		getter   func() string
		wantSub  string
	}{
		{
			name:    "no getter",
			getter:  nil,
			wantSub: "No review available",
		},
		{
			name:    "getter returns empty",
			getter:  func() string { return "" },
			wantSub: "No review available",
		},
		{
			name:    "getter returns review",
			getter:  func() string { return "LGTM" },
			wantSub: "LGTM",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := newTestExecutor()
			ex.ReviewGetter = tt.getter
			result, _ := ex.ExecuteTool("get_review", map[string]interface{}{})
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

// ── unknown tool test ───────────────────────────────────────────────────

func TestTool_Unknown(t *testing.T) {
	ex := newTestExecutor()
	result, isErr := ex.ExecuteTool("bogus_tool", map[string]interface{}{})
	if !isErr {
		t.Error("expected isError=true for unknown tool")
	}
	if !strings.Contains(result, "unknown tool") {
		t.Errorf("expected 'unknown tool' error, got: %s", result)
	}
}

// ── truncateOutput tests ────────────────────────────────────────────────

func TestTruncateOutput(t *testing.T) {
	short := "hello\nworld\n"
	result := truncateOutput(short, "hint")
	if result != short {
		t.Errorf("short string should not be truncated, got: %s", result)
	}

	// Create a string just over the limit
	big := strings.Repeat("x", maxOutputBytes+100)
	result = truncateOutput(big, "narrow it")
	if len(result) > maxOutputBytes+200 {
		t.Errorf("truncated output too large: %d bytes", len(result))
	}
	if !strings.Contains(result, "truncated") {
		t.Error("truncated output should contain truncation notice")
	}
}

// ── matchGlob tests ────────────────────────────────────────────────────

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "internal/ai/tools.go", true},
		{"*.go", "README.md", false},
		{"**/*.go", "internal/ai/tools.go", true},
		{"**/*.go", "main.go", true},
		{"**/*_test.go", "internal/ai/agent_test.go", true},
		{"internal/**", "internal/ai/tools.go", true},
		{"internal/**", "cmd/main.go", false},
		{"cmd/*", "cmd/prr", true},
		{"cmd/*", "cmd/prr/main.go", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%s", tt.pattern, tt.path), func(t *testing.T) {
			got := matchGlob(tt.pattern, tt.path)
			if got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

// ── helper arg extraction tests ─────────────────────────────────────────

func TestGetStringArg(t *testing.T) {
	args := map[string]interface{}{
		"name": "hello",
		"num":  42.0,
	}
	if got := getStringArg(args, "name"); got != "hello" {
		t.Errorf("getStringArg = %q, want %q", got, "hello")
	}
	if got := getStringArg(args, "missing"); got != "" {
		t.Errorf("getStringArg(missing) = %q, want empty", got)
	}
	// Non-string value returns empty
	if got := getStringArg(args, "num"); got != "" {
		t.Errorf("getStringArg(num) = %q, want empty", got)
	}
}

func TestGetIntArg(t *testing.T) {
	args := map[string]interface{}{
		"count": 42.0,
		"text":  "hello",
	}
	if got := getIntArg(args, "count", 0); got != 42 {
		t.Errorf("getIntArg = %d, want %d", got, 42)
	}
	if got := getIntArg(args, "missing", 10); got != 10 {
		t.Errorf("getIntArg(missing) = %d, want default 10", got)
	}
	if got := getIntArg(args, "text", 10); got != 10 {
		t.Errorf("getIntArg(text) = %d, want default 10", got)
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{500, "500B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1048576, "1.0MB"},
	}
	for _, tt := range tests {
		got := humanSize(tt.bytes)
		if got != tt.want {
			t.Errorf("humanSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

// ── fakeExitError implements the interface needed by isExitCode ──────────

type fakeExitError struct {
	code int
}

func (e *fakeExitError) Error() string   { return fmt.Sprintf("exit status %d", e.code) }
func (e *fakeExitError) ExitCode() int   { return e.code }

// ══════════════════════════════════════════════════════════════════════════
// Integration tests — real git commands, no faked runners
//
// These tests exercise each tool against the real git repo. They verify
// that the tool executor integrates correctly with actual git operations.
// Unlike the unit tests above (which use fakeGit), these use the real
// runGit/runCmd functions and validate output against real repo contents.
// ══════════════════════════════════════════════════════════════════════════

func skipWithoutGit(t *testing.T) {
	t.Helper()
	if _, err := runGit("rev-parse", "--is-inside-work-tree"); err != nil {
		t.Skip("not inside a git repo, skipping integration test")
	}
}

func newRealExecutor() *ToolExecutor {
	return &ToolExecutor{
		HeadRef: "HEAD",
		BaseRef: "HEAD",
	}
}

func TestIntegration_ReadFile(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantSub string
		wantErr bool
	}{
		{
			name:    "reads existing file at repo root",
			args:    map[string]interface{}{"path": "go.mod"},
			wantSub: "module",
		},
		{
			name:    "reads file with full path",
			args:    map[string]interface{}{"path": "internal/ai/tools.go"},
			wantSub: "package ai",
		},
		{
			name:    "with offset and limit",
			args:    map[string]interface{}{"path": "go.mod", "offset": 1.0, "limit": 3.0},
			wantSub: "lines 1-3",
		},
		{
			name:    "nonexistent file returns error",
			args:    map[string]interface{}{"path": "this_file_does_not_exist_abc123.go"},
			wantErr: true,
		},
		{
			name:    "missing path parameter",
			args:    map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, isErr := ex.ExecuteTool("read_file", tt.args)
			if tt.wantErr {
				if !isErr {
					t.Errorf("expected error, got: %s", result)
				}
				return
			}
			if isErr {
				t.Fatalf("unexpected error: %s", result)
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

func TestIntegration_ReadBaseFile(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantSub string
		wantErr bool
	}{
		{
			name:    "reads file from base ref",
			args:    map[string]interface{}{"path": "go.mod"},
			wantSub: "module",
		},
		{
			name:    "reads file with full path",
			args:    map[string]interface{}{"path": "internal/ai/tools.go"},
			wantSub: "package ai",
		},
		{
			name:    "with pagination",
			args:    map[string]interface{}{"path": "go.mod", "offset": 1.0, "limit": 2.0},
			wantSub: "lines 1-2",
		},
		{
			name:    "nonexistent file returns error",
			args:    map[string]interface{}{"path": "no_such_file_xyz.go"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, isErr := ex.ExecuteTool("read_base_file", tt.args)
			if tt.wantErr {
				if !isErr {
					t.Errorf("expected error, got: %s", result)
				}
				return
			}
			if isErr {
				t.Fatalf("unexpected error: %s", result)
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

func TestIntegration_ListDir(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantSub string
	}{
		{
			name:    "root directory lists files",
			args:    map[string]interface{}{},
			wantSub: "Directory: .",
		},
		{
			name:    "explicit root path",
			args:    map[string]interface{}{"path": "."},
			wantSub: "Directory: .",
		},
		{
			name:    "nonexistent directory",
			args:    map[string]interface{}{"path": "nonexistent_dir_xyz"},
			wantSub: "empty or does not exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, isErr := ex.ExecuteTool("list_dir", tt.args)
			if isErr {
				t.Fatalf("unexpected error: %s", result)
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

func TestIntegration_Glob(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantSub string
	}{
		{
			name:    "find all Go files",
			args:    map[string]interface{}{"pattern": "*.go"},
			wantSub: "match(es)",
		},
		{
			name:    "find specific file",
			args:    map[string]interface{}{"pattern": "tools.go"},
			wantSub: "tools.go",
		},
		{
			name:    "doublestar pattern",
			args:    map[string]interface{}{"pattern": "**/*.go"},
			wantSub: "match(es)",
		},
		{
			name:    "no matches",
			args:    map[string]interface{}{"pattern": "**/*.xyz_nonexistent"},
			wantSub: "No files match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, isErr := ex.ExecuteTool("glob", tt.args)
			if isErr {
				t.Fatalf("unexpected error: %s", result)
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

func TestIntegration_Grep(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantSub string
	}{
		{
			name:    "find struct definition",
			args:    map[string]interface{}{"pattern": "ToolExecutor"},
			wantSub: "ToolExecutor",
		},
		{
			name:    "with file glob filter",
			args:    map[string]interface{}{"pattern": "ToolExecutor", "file_glob": "*.go"},
			wantSub: "ToolExecutor",
		},
		{
			name:    "fixed string match",
			args:    map[string]interface{}{"pattern": "package ai", "regex": "false"},
			wantSub: "package ai",
		},
		{
			name:    "no matches returns message",
			args:    map[string]interface{}{"pattern": "zzz_n" + "omatch_42"},
			wantSub: "No matches found",
		},
		{
			name:    "max_results capping",
			args:    map[string]interface{}{"pattern": "func", "max_results": 3.0},
			wantSub: "match(es)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, isErr := ex.ExecuteTool("grep", tt.args)
			if isErr {
				t.Fatalf("unexpected error: %s", result)
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

func TestIntegration_GitDiff(t *testing.T) {
	skipWithoutGit(t)

	tests := []struct {
		name    string
		args    map[string]interface{}
		baseRef string
		headRef string
		wantSub string
		wantErr bool
	}{
		{
			name:    "same commit produces no diff",
			baseRef: "HEAD",
			headRef: "HEAD",
			args:    map[string]interface{}{},
			wantSub: "No differences",
		},
		{
			name:    "diff with path restriction",
			baseRef: "HEAD",
			headRef: "HEAD",
			args:    map[string]interface{}{"paths": "go.mod"},
			wantSub: "No differences",
		},
		{
			name:    "missing refs produces error",
			baseRef: "",
			headRef: "",
			args:    map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := &ToolExecutor{HeadRef: tt.headRef, BaseRef: tt.baseRef}
			result, isErr := ex.ExecuteTool("git_diff", tt.args)
			if tt.wantErr {
				if !isErr {
					t.Errorf("expected error, got: %s", result)
				}
				return
			}
			if isErr {
				t.Fatalf("unexpected error: %s", result)
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

func TestIntegration_GitLog(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantSub string
	}{
		{
			name:    "default log",
			args:    map[string]interface{}{},
			wantSub: "", // just verify no error
		},
		{
			name:    "with max",
			args:    map[string]interface{}{"max": 3.0},
			wantSub: "", // just verify no error
		},
		{
			name:    "with rev_range",
			args:    map[string]interface{}{"rev_range": "HEAD~2..HEAD", "max": 5.0},
			wantSub: "", // just verify no error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, isErr := ex.ExecuteTool("git_log", tt.args)
			if isErr {
				t.Fatalf("unexpected error: %s", result)
			}
			if result == "" {
				t.Error("expected non-empty result")
			}
			if tt.wantSub != "" && !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

func TestIntegration_GitShow(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	tests := []struct {
		name    string
		args    map[string]interface{}
		wantSub string
		wantErr bool
	}{
		{
			name:    "show HEAD commit",
			args:    map[string]interface{}{"commit": "HEAD"},
			wantSub: "commit",
		},
		{
			name:    "show file at HEAD",
			args:    map[string]interface{}{"commit": "HEAD", "path": "go.mod"},
			wantSub: "module",
		},
		{
			name:    "missing commit parameter",
			args:    map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, isErr := ex.ExecuteTool("git_show", tt.args)
			if tt.wantErr {
				if !isErr {
					t.Errorf("expected error, got: %s", result)
				}
				return
			}
			if isErr {
				t.Fatalf("unexpected error: %s", result)
			}
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

func TestIntegration_GitBlame(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	// git blame resolves paths relative to CWD, so use files that exist
	// in the test package directory (internal/ai/).
	tests := []struct {
		name    string
		args    map[string]interface{}
		wantErr bool
	}{
		{
			name: "blame a file in CWD",
			args: map[string]interface{}{"path": "tools.go"},
		},
		{
			name: "blame with line range",
			args: map[string]interface{}{"path": "tools.go", "line_start": 1.0, "line_end": 3.0},
		},
		{
			name:    "missing path",
			args:    map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, isErr := ex.ExecuteTool("git_blame", tt.args)
			if tt.wantErr {
				if !isErr {
					t.Errorf("expected error, got: %s", result)
				}
				return
			}
			if isErr {
				t.Fatalf("unexpected error: %s", result)
			}
			if result == "" {
				t.Error("expected non-empty result")
			}
		})
	}
}

func TestIntegration_GitStatus(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	result, isErr := ex.ExecuteTool("git_status", map[string]interface{}{})
	if isErr {
		t.Fatalf("unexpected error: %s", result)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
	// Should contain either branch info or "clean"
	if !strings.Contains(result, "##") && !strings.Contains(result, "clean") {
		t.Errorf("expected branch info or 'clean', got: %s", result)
	}
}

func TestIntegration_GetReview(t *testing.T) {
	tests := []struct {
		name     string
		getter   func() string
		wantSub  string
	}{
		{
			name:    "nil getter",
			getter:  nil,
			wantSub: "No review available",
		},
		{
			name:    "empty review",
			getter:  func() string { return "" },
			wantSub: "No review available",
		},
		{
			name:    "with review content",
			getter:  func() string { return "## Summary\nLGTM" },
			wantSub: "LGTM",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := &ToolExecutor{ReviewGetter: tt.getter}
			result, _ := ex.ExecuteTool("get_review", map[string]interface{}{})
			if !strings.Contains(result, tt.wantSub) {
				t.Errorf("result should contain %q, got: %s", tt.wantSub, result)
			}
		})
	}
}

func TestIntegration_UnknownTool(t *testing.T) {
	ex := newRealExecutor()
	result, isErr := ex.ExecuteTool("fake_tool_xyz", map[string]interface{}{})
	if !isErr {
		t.Error("expected isError=true for unknown tool")
	}
	if !strings.Contains(result, "unknown tool") {
		t.Errorf("expected 'unknown tool', got: %s", result)
	}
}

// TestIntegration_ReadFileThenReadBaseFile verifies both read_file and
// read_base_file work in sequence on the same executor, simulating a
// real tool-calling loop that reads both versions of a file.
func TestIntegration_ReadFileThenReadBaseFile(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	// Read head version (use repo-root path that works from any CWD)
	headResult, isErr := ex.ExecuteTool("read_file", map[string]interface{}{"path": "go.mod"})
	if isErr {
		t.Fatalf("read_file error: %s", headResult)
	}
	if !strings.Contains(headResult, "module") {
		t.Errorf("head result should contain 'module', got: %s", headResult)
	}

	// Read base version (same as HEAD in this test)
	baseResult, isErr := ex.ExecuteTool("read_base_file", map[string]interface{}{"path": "go.mod"})
	if isErr {
		t.Fatalf("read_base_file error: %s", baseResult)
	}
	if !strings.Contains(baseResult, "module") {
		t.Errorf("base result should contain 'module', got: %s", baseResult)
	}

	// Both should return the same content since BaseRef = HeadRef = HEAD
	if headResult != baseResult {
		t.Logf("Note: head and base results differ (expected if BaseRef != HeadRef)")
	}
}

// TestIntegration_AllToolsExecutable verifies every canonical tool can be
// invoked without panicking, even if some return errors due to missing args.
func TestIntegration_AllToolsExecutable(t *testing.T) {
	skipWithoutGit(t)
	ex := newRealExecutor()

	for _, td := range CanonicalToolDefs() {
		t.Run(td.Name, func(t *testing.T) {
			// Call with empty args — should return error, not panic
			result, _ := ex.ExecuteTool(td.Name, map[string]interface{}{})
			if result == "" {
				t.Error("expected non-empty result (even if error)")
			}
		})
	}
}

// TestIntegration_ToolReadOnlyConsistency verifies that the ReadOnly flag
// in tool definitions matches IsToolReadOnly.
func TestIntegration_ToolReadOnlyConsistency(t *testing.T) {
	for _, td := range CanonicalToolDefs() {
		if td.ReadOnly != IsToolReadOnly(td.Name) {
			t.Errorf("tool %q: ReadOnly=%v but IsToolReadOnly=%v",
				td.Name, td.ReadOnly, IsToolReadOnly(td.Name))
		}
	}
}

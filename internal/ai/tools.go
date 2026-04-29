package ai

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ToolExecutor handles executing tool calls from the LLM.
type ToolExecutor struct {
	HeadRef string // e.g. "origin/feature-branch" — the PR head ref for git show
}

// ToolDeclarations returns the Gemini-format tool declarations.
func ToolDeclarations() []geminiTool {
	return []geminiTool{
		{
			FunctionDeclarations: []geminiFunction{
				{
					Name:        "read_file",
					Description: "Read the contents of a file from the PR branch. Returns paginated content. Use offset and limit to read large files in chunks.",
					Parameters: geminiSchema{
						Type: "OBJECT",
						Properties: map[string]geminiSchema{
							"path": {
								Type:        "STRING",
								Description: "File path relative to the repository root (e.g. 'src/main.go')",
							},
							"offset": {
								Type:        "INTEGER",
								Description: "Line number to start reading from (1-indexed, default: 1)",
							},
							"limit": {
								Type:        "INTEGER",
								Description: "Maximum number of lines to return (default: 200, max: 500)",
							},
						},
						Required: []string{"path"},
					},
				},
				{
					Name:        "list_files",
					Description: "List files and directories at a given path in the PR branch. Returns one entry per line, directories end with '/'.",
					Parameters: geminiSchema{
						Type: "OBJECT",
						Properties: map[string]geminiSchema{
							"path": {
								Type:        "STRING",
								Description: "Directory path relative to repo root (default: '.' for root)",
							},
						},
					},
				},
			},
		},
	}
}

// ExecuteTool runs a tool call and returns the result as a string.
func (t *ToolExecutor) ExecuteTool(name string, args map[string]interface{}) string {
	switch name {
	case "read_file":
		return t.readFile(args)
	case "list_files":
		return t.listFiles(args)
	default:
		return fmt.Sprintf("Unknown tool: %s", name)
	}
}

func (t *ToolExecutor) readFile(args map[string]interface{}) string {
	path, _ := args["path"].(string)
	if path == "" {
		return "Error: 'path' is required"
	}

	offset := 1
	limit := 200

	if v, ok := args["offset"].(float64); ok {
		offset = int(v)
	}
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}

	// Read file from the PR head ref
	ref := t.HeadRef
	cmd := exec.Command("git", "show", fmt.Sprintf("%s:%s", ref, path))
	out, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Error reading file '%s': %v", path, err)
	}

	lines := strings.Split(string(out), "\n")
	totalLines := len(lines)

	// Apply pagination
	startIdx := offset - 1
	if startIdx >= totalLines {
		return fmt.Sprintf("File '%s' has %d lines, offset %d is past the end", path, totalLines, offset)
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
		result.WriteString(fmt.Sprintf("\n... %d more lines. Use offset=%d to continue reading.", totalLines-endIdx, endIdx+1))
	}

	return result.String()
}

func (t *ToolExecutor) listFiles(args map[string]interface{}) string {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}

	ref := t.HeadRef

	// Use git ls-tree to list directory contents
	treePath := path
	if treePath == "." {
		treePath = ""
	}

	var cmd *exec.Cmd
	if treePath == "" {
		cmd = exec.Command("git", "ls-tree", "--name-only", ref)
	} else {
		cmd = exec.Command("git", "ls-tree", "--name-only", ref, treePath+"/")
	}

	out, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Error listing directory '%s': %v", path, err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return fmt.Sprintf("Directory '%s' is empty or does not exist", path)
	}

	// Get tree entries with type info for dir markers
	if treePath == "" {
		cmd = exec.Command("git", "ls-tree", ref)
	} else {
		cmd = exec.Command("git", "ls-tree", ref, treePath+"/")
	}
	typeOut, err := cmd.Output()
	if err != nil {
		return output // fallback to names only
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Directory: %s\n", path))
	result.WriteString(strings.Repeat("─", 40) + "\n")
	for _, line := range strings.Split(strings.TrimSpace(string(typeOut)), "\n") {
		if line == "" {
			continue
		}
		// Format: "mode type hash\tpath"
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		entryPath := parts[1]
		// Strip directory prefix to show relative names
		name := entryPath
		if treePath != "" {
			name = strings.TrimPrefix(entryPath, treePath+"/")
		}
		if strings.Contains(parts[0], "tree") {
			result.WriteString(name + "/\n")
		} else {
			result.WriteString(name + "\n")
		}
	}

	return result.String()
}

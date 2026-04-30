package ai

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// ToolExecutor handles executing tool calls from the LLM.
type ToolExecutor struct {
	HeadRef  string            // e.g. "origin/feature-branch" — the PR head ref for git show
	BaseRef  string            // e.g. "origin/main" — the PR base ref for reading pre-change files
	RawDiffs map[string]string // filePath -> raw unified diff (set by UI after PR load)
}

// ToolDeclarations returns the Gemini-format tool declarations.
func ToolDeclarations() []geminiTool {
	return []geminiTool{
		{
			FunctionDeclarations: []geminiFunction{
				{
					Name:        "read_file",
					Description: "Read a file from the PR branch (after changes). Returns paginated content with line numbers. Use offset and limit for large files.",
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
					Name:        "read_base_file",
					Description: "Read a file from the base branch (before changes). Useful for understanding what the code looked like before the PR, comparing old vs new implementations, and verifying refactors. Returns paginated content with line numbers.",
					Parameters: geminiSchema{
						Type: "OBJECT",
						Properties: map[string]geminiSchema{
							"path": {
								Type:        "STRING",
								Description: "File path relative to the repository root",
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
					Name:        "search_code",
					Description: "Search for a pattern across all files in the PR branch using regex. Returns matching lines with file paths and line numbers. Useful for finding callers, usages, type definitions, and related code.",
					Parameters: geminiSchema{
						Type: "OBJECT",
						Properties: map[string]geminiSchema{
							"pattern": {
								Type:        "STRING",
								Description: "Regular expression pattern to search for (e.g. 'func.*Handler', 'import.*fmt')",
							},
							"path": {
								Type:        "STRING",
								Description: "Optional directory or file to scope the search (default: entire repo)",
							},
							"max_results": {
								Type:        "INTEGER",
								Description: "Maximum number of matching lines to return (default: 50, max: 200)",
							},
						},
						Required: []string{"pattern"},
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
				{
					Name:        "get_diff",
					Description: "Get the unified diffs for files changed in this PR. Returns diffs packed into chunks of complete files. Use page=1 to start, then increment to get more files. Each response indicates if there are more pages.",
					Parameters: geminiSchema{
						Type: "OBJECT",
						Properties: map[string]geminiSchema{
							"page": {
								Type:        "INTEGER",
								Description: "Page number (1-indexed, default: 1). Each page contains as many complete file diffs as fit within the size limit.",
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
		return t.readFile(args, t.HeadRef)
	case "read_base_file":
		return t.readFile(args, t.BaseRef)
	case "search_code":
		return t.searchCode(args)
	case "list_files":
		return t.listFiles(args)
	case "get_diff":
		return t.getDiff(args)
	default:
		return fmt.Sprintf("Unknown tool: %s", name)
	}
}

// readFile reads a file at the given git ref with pagination.
func (t *ToolExecutor) readFile(args map[string]interface{}, ref string) string {
	path, _ := args["path"].(string)
	if path == "" {
		return "Error: 'path' is required"
	}
	if ref == "" {
		return "Error: git ref not configured"
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

	cmd := exec.Command("git", "show", fmt.Sprintf("%s:%s", ref, path))
	out, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Error reading file '%s' at ref '%s': %v", path, ref, err)
	}

	lines := strings.Split(string(out), "\n")
	totalLines := len(lines)

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

// searchCode uses git grep to search for a pattern in the PR branch.
func (t *ToolExecutor) searchCode(args map[string]interface{}) string {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return "Error: 'pattern' is required"
	}

	maxResults := 50
	if v, ok := args["max_results"].(float64); ok {
		maxResults = int(v)
	}
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > 200 {
		maxResults = 200
	}

	ref := t.HeadRef
	if ref == "" {
		return "Error: git ref not configured"
	}

	// Build git grep command
	cmdArgs := []string{"grep", "-n", "-E", "--no-color", pattern, ref}

	// Optional path scope
	if scopePath, ok := args["path"].(string); ok && scopePath != "" {
		cmdArgs = append(cmdArgs, "--", scopePath)
	}

	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		// git grep returns exit code 1 when no matches found
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return fmt.Sprintf("No matches found for pattern: %s", pattern)
		}
		return fmt.Sprintf("Error searching: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	totalMatches := len(lines)

	// Strip the ref prefix from each line (git grep outputs "ref:path:line:content")
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
		result.WriteString(fmt.Sprintf("\n... %d more matches not shown. Use max_results=%d or narrow the path.", totalMatches-maxResults, totalMatches))
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

// diffChunkMaxChars is the approximate maximum size per get_diff page.
// Sized to stay well within typical LLM context windows while providing
// enough content for meaningful review.
const diffChunkMaxChars = 15000

func (t *ToolExecutor) getDiff(args map[string]interface{}) string {
	if t.RawDiffs == nil || len(t.RawDiffs) == 0 {
		return "No diffs available for this PR."
	}

	page := 1
	if v, ok := args["page"].(float64); ok {
		page = int(v)
	}
	if page < 1 {
		page = 1
	}

	// Sort paths for deterministic pagination
	paths := make([]string, 0, len(t.RawDiffs))
	for p := range t.RawDiffs {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Build chunks of complete files.
	// Each chunk packs as many complete file diffs as fit within diffChunkMaxChars.
	// If a single file exceeds the limit, it gets its own chunk (possibly split).
	type chunk struct {
		files []string
		text  string
	}
	var chunks []chunk
	var curFiles []string
	var curText strings.Builder

	for _, p := range paths {
		diff := t.RawDiffs[p]
		entry := fmt.Sprintf("=== %s ===\n%s\n\n", p, diff)

		if curText.Len() > 0 && curText.Len()+len(entry) > diffChunkMaxChars {
			// Current chunk is full — finalize it
			chunks = append(chunks, chunk{files: curFiles, text: curText.String()})
			curFiles = nil
			curText.Reset()
		}

		// If a single file exceeds the chunk limit, split it across chunks
		if len(entry) > diffChunkMaxChars && curText.Len() == 0 {
			lines := strings.Split(entry, "\n")
			var splitText strings.Builder
			splitText.WriteString(fmt.Sprintf("=== %s (continued) ===\n", p))
			for _, line := range lines {
				if splitText.Len()+len(line)+1 > diffChunkMaxChars && splitText.Len() > 0 {
					chunks = append(chunks, chunk{files: []string{p + " (partial)"}, text: splitText.String()})
					splitText.Reset()
					splitText.WriteString(fmt.Sprintf("=== %s (continued) ===\n", p))
				}
				splitText.WriteString(line + "\n")
			}
			if splitText.Len() > 0 {
				curFiles = []string{p}
				curText.Reset()
				curText.WriteString(splitText.String())
			}
			continue
		}

		curText.WriteString(entry)
		curFiles = append(curFiles, p)
	}
	if curText.Len() > 0 {
		chunks = append(chunks, chunk{files: curFiles, text: curText.String()})
	}

	if len(chunks) == 0 {
		return "No diffs available."
	}

	// Return the requested page
	pageIdx := page - 1
	if pageIdx >= len(chunks) {
		return fmt.Sprintf("No more pages. This PR has %d page(s) of diffs.", len(chunks))
	}

	c := chunks[pageIdx]
	var result strings.Builder
	result.WriteString(fmt.Sprintf("Page %d of %d", page, len(chunks)))
	result.WriteString(fmt.Sprintf(" (%d file(s): %s)\n", len(c.files), strings.Join(c.files, ", ")))
	result.WriteString(strings.Repeat("─", 40) + "\n")
	result.WriteString(c.text)
	if pageIdx < len(chunks)-1 {
		result.WriteString(fmt.Sprintf("\n... More files available. Use get_diff(page=%d) to continue.", page+1))
	} else {
		result.WriteString("\n(End of diffs)")
	}
	return result.String()
}

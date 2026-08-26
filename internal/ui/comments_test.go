package ui

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/git"
)

func TestParseDiffLine_ContextLine(t *testing.T) {
	// Delta/chroma format: "  5│  7│ some code"
	info := parseDiffLine("  5│  7│ some code")
	if info.leftLine != 5 {
		t.Errorf("leftLine = %d, want 5", info.leftLine)
	}
	if info.rightLine != 7 {
		t.Errorf("rightLine = %d, want 7", info.rightLine)
	}
	if info.side != "RIGHT" {
		t.Errorf("side = %q, want RIGHT", info.side)
	}
	if info.line != 7 {
		t.Errorf("line = %d, want 7", info.line)
	}
}

func TestParseDiffLine_AddedLine(t *testing.T) {
	// Added: blank old, number on new
	info := parseDiffLine("   │ 12│ added line")
	if info.leftLine != 0 {
		t.Errorf("leftLine = %d, want 0", info.leftLine)
	}
	if info.rightLine != 12 {
		t.Errorf("rightLine = %d, want 12", info.rightLine)
	}
	if info.side != "RIGHT" {
		t.Errorf("side = %q, want RIGHT", info.side)
	}
	if info.line != 12 {
		t.Errorf("line = %d, want 12", info.line)
	}
}

func TestParseDiffLine_RemovedLine(t *testing.T) {
	// Removed: number on old, blank new
	info := parseDiffLine("  8│   │ removed line")
	if info.leftLine != 8 {
		t.Errorf("leftLine = %d, want 8", info.leftLine)
	}
	if info.rightLine != 0 {
		t.Errorf("rightLine = %d, want 0", info.rightLine)
	}
	if info.side != "LEFT" {
		t.Errorf("side = %q, want LEFT", info.side)
	}
	if info.line != 8 {
		t.Errorf("line = %d, want 8", info.line)
	}
}

func TestParseDiffLine_NoMatch(t *testing.T) {
	info := parseDiffLine("@@ -1,3 +1,4 @@")
	if info.line != 0 {
		t.Errorf("line = %d, want 0 for hunk header", info.line)
	}
}

func TestInjectComments_LeftSideOnContextLine(t *testing.T) {
	// This test verifies the fix for the bug where LEFT-side comments
	// on context lines (unchanged lines) were never displayed.

	// Simulate a diff with a context line showing old=5, new=7
	styledDiff := "  5│  7│ unchanged line\n   │  8│ added line"

	m := &Model{
		comments: map[string][]git.ReviewComment{
			"file.go": {
				{
					ID:     1,
					Path:   "file.go",
					Line:   5,
					Side:   "LEFT",
					Body:   "comment on old side of context line",
					Author: "reviewer",
				},
			},
		},
	}
	m.diffViewport.SetWidth(80)

	result := m.injectComments(styledDiff, "file.go")

	if !strings.Contains(result, "comment on old side of context line") {
		t.Error("LEFT-side comment on context line should be displayed but was not found in output")
	}
	if !strings.Contains(result, "reviewer") {
		t.Error("comment author should be displayed")
	}
}

func TestInjectComments_RightSideOnContextLine(t *testing.T) {
	styledDiff := "  5│  7│ unchanged line"

	m := &Model{
		comments: map[string][]git.ReviewComment{
			"file.go": {
				{
					ID:     1,
					Path:   "file.go",
					Line:   7,
					Side:   "RIGHT",
					Body:   "comment on new side",
					Author: "alice",
				},
			},
		},
	}
	m.diffViewport.SetWidth(80)

	result := m.injectComments(styledDiff, "file.go")

	if !strings.Contains(result, "comment on new side") {
		t.Error("RIGHT-side comment on context line should be displayed")
	}
}

func TestInjectComments_BothSidesOnContextLine(t *testing.T) {
	// Both LEFT and RIGHT comments on the same context line
	styledDiff := "  5│  7│ unchanged line"

	m := &Model{
		comments: map[string][]git.ReviewComment{
			"file.go": {
				{
					ID:     1,
					Path:   "file.go",
					Line:   7,
					Side:   "RIGHT",
					Body:   "right side comment",
					Author: "alice",
				},
				{
					ID:     2,
					Path:   "file.go",
					Line:   5,
					Side:   "LEFT",
					Body:   "left side comment",
					Author: "bob",
				},
			},
		},
	}
	m.diffViewport.SetWidth(80)

	result := m.injectComments(styledDiff, "file.go")

	if !strings.Contains(result, "right side comment") {
		t.Error("RIGHT-side comment should be displayed")
	}
	if !strings.Contains(result, "left side comment") {
		t.Error("LEFT-side comment should be displayed")
	}
}

func TestInjectComments_LeftSideOnDeletionLine(t *testing.T) {
	// Deletion line: only old number present
	styledDiff := "  8│   │ deleted line"

	m := &Model{
		comments: map[string][]git.ReviewComment{
			"file.go": {
				{
					ID:     1,
					Path:   "file.go",
					Line:   8,
					Side:   "LEFT",
					Body:   "why was this removed?",
					Author: "reviewer",
				},
			},
		},
	}
	m.diffViewport.SetWidth(80)

	result := m.injectComments(styledDiff, "file.go")

	if !strings.Contains(result, "why was this removed?") {
		t.Error("LEFT-side comment on deletion line should be displayed")
	}
}

func TestInjectComments_NoComments(t *testing.T) {
	styledDiff := "  1│  1│ some code"
	m := &Model{
		comments: map[string][]git.ReviewComment{},
	}
	m.diffViewport.SetWidth(80)

	result := m.injectComments(styledDiff, "file.go")
	if result != styledDiff {
		t.Error("output should be unchanged when no comments exist")
	}
}

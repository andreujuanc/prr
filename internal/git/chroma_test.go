package git

import (
	"strings"
	"testing"
)

func TestParseUnifiedDiff_BasicHunk(t *testing.T) {
	raw := "diff --git a/main.go b/main.go\nindex abc1234..def5678 100644\n--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@\n package main\n \n+import \"fmt\"\n func main() {}"
	hunks := parseUnifiedDiff(raw)

	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}

	h := hunks[0]
	if !strings.HasPrefix(h.header, "@@") {
		t.Errorf("expected hunk header starting with @@, got %q", h.header)
	}

	if len(h.lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(h.lines))
	}

	// Check context line
	if h.lines[0].kind != diffContext {
		t.Errorf("line 0: expected context, got %d", h.lines[0].kind)
	}
	if h.lines[0].content != "package main" {
		t.Errorf("line 0: unexpected content %q", h.lines[0].content)
	}

	// Check added line
	if h.lines[2].kind != diffAdded {
		t.Errorf("line 2: expected added, got %d", h.lines[2].kind)
	}
	if h.lines[2].content != `import "fmt"` {
		t.Errorf("line 2: unexpected content %q", h.lines[2].content)
	}
}

func TestParseUnifiedDiff_MultipleHunks(t *testing.T) {
	raw := `diff --git a/f.go b/f.go
--- a/f.go
+++ b/f.go
@@ -1,2 +1,2 @@
-old line
+new line
@@ -10,2 +10,2 @@
-another old
+another new
`
	hunks := parseUnifiedDiff(raw)

	if len(hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(hunks))
	}

	if hunks[0].lines[0].kind != diffRemoved {
		t.Errorf("hunk 0, line 0: expected removed")
	}
	if hunks[0].lines[1].kind != diffAdded {
		t.Errorf("hunk 0, line 1: expected added")
	}
	if hunks[1].lines[0].kind != diffRemoved {
		t.Errorf("hunk 1, line 0: expected removed")
	}
}

func TestParseUnifiedDiff_LineNumbers(t *testing.T) {
	raw := `diff --git a/f.go b/f.go
--- a/f.go
+++ b/f.go
@@ -5,3 +5,4 @@
 context
-removed
+added1
+added2
 context2
`
	hunks := parseUnifiedDiff(raw)

	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}

	lines := hunks[0].lines
	// context: old=5, new=5
	if lines[0].oldNum != 5 || lines[0].newNum != 5 {
		t.Errorf("context line: expected old=5 new=5, got old=%d new=%d", lines[0].oldNum, lines[0].newNum)
	}
	// removed: old=6, new=0
	if lines[1].oldNum != 6 || lines[1].newNum != 0 {
		t.Errorf("removed line: expected old=6 new=0, got old=%d new=%d", lines[1].oldNum, lines[1].newNum)
	}
	// added1: old=0, new=6
	if lines[2].oldNum != 0 || lines[2].newNum != 6 {
		t.Errorf("added1: expected old=0 new=6, got old=%d new=%d", lines[2].oldNum, lines[2].newNum)
	}
	// added2: old=0, new=7
	if lines[3].oldNum != 0 || lines[3].newNum != 7 {
		t.Errorf("added2: expected old=0 new=7, got old=%d new=%d", lines[3].oldNum, lines[3].newNum)
	}
	// context2: old=7, new=8
	if lines[4].oldNum != 7 || lines[4].newNum != 8 {
		t.Errorf("context2: expected old=7 new=8, got old=%d new=%d", lines[4].oldNum, lines[4].newNum)
	}
}

func TestParseUnifiedDiff_Empty(t *testing.T) {
	hunks := parseUnifiedDiff("")
	if len(hunks) != 0 {
		t.Errorf("expected 0 hunks, got %d", len(hunks))
	}
}

func TestParseHunkHeader(t *testing.T) {
	tests := []struct {
		header  string
		wantOld int
		wantNew int
	}{
		{"@@ -1,3 +1,4 @@", 1, 1},
		{"@@ -10,5 +12,7 @@ func main()", 10, 12},
		{"@@ -1 +1 @@", 1, 1},
		{"@@ -100,0 +101,3 @@", 100, 101},
	}
	for _, tt := range tests {
		old, new := parseHunkHeader(tt.header)
		if old != tt.wantOld || new != tt.wantNew {
			t.Errorf("parseHunkHeader(%q) = (%d, %d), want (%d, %d)",
				tt.header, old, new, tt.wantOld, tt.wantNew)
		}
	}
}

func TestHexToRGB(t *testing.T) {
	tests := []struct {
		hex     string
		r, g, b uint8
	}{
		{"#FF0000", 255, 0, 0},
		{"#00FF00", 0, 255, 0},
		{"#0000FF", 0, 0, 255},
		{"#A6E3A1", 166, 227, 161},
		{"invalid", 0, 0, 0},
	}
	for _, tt := range tests {
		r, g, b := hexToRGB(tt.hex)
		if r != tt.r || g != tt.g || b != tt.b {
			t.Errorf("hexToRGB(%q) = (%d,%d,%d), want (%d,%d,%d)",
				tt.hex, r, g, b, tt.r, tt.g, tt.b)
		}
	}
}

func TestRenderChromaDiff_ProducesOutput(t *testing.T) {
	raw := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
 
+import "fmt"
`
	theme := DefaultDiffTheme()
	out, err := renderChromaDiff(raw, "main.go", theme, 80)
	if err != nil {
		t.Fatalf("renderChromaDiff error: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	// Should contain the file path
	if !strings.Contains(out, "main.go") {
		t.Error("output should contain file path")
	}
	// Should contain the added line content
	if !strings.Contains(out, "fmt") {
		t.Error("output should contain added line content")
	}
}

func TestRenderChromaDiff(t *testing.T) {
	rawDiff := "diff --git a/main.go b/main.go\nindex abc..def 100644\n--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,2 @@\n package main\n-func old() {}\n+func new() {}\n"
	theme := DefaultDiffTheme()

	out, err := renderChromaDiff(rawDiff, "main.go", theme, 80)
	if err != nil {
		t.Fatalf("renderChromaDiff: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	// Should contain file name
	if !strings.Contains(out, "main.go") {
		t.Error("output should contain file name")
	}
	// Should contain hunk header
	if !strings.Contains(out, "@@") {
		t.Error("output should contain hunk header")
	}
	// Should contain added/removed content
	if !strings.Contains(out, "new") {
		t.Error("output should contain added content")
	}
}

func TestRenderChromaDiff_SmallWidth(t *testing.T) {
	rawDiff := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n"
	theme := DefaultDiffTheme()

	// Width < 20 should fall back to 80
	out, err := renderChromaDiff(rawDiff, "x.go", theme, 5)
	if err != nil {
		t.Fatalf("renderChromaDiff: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestRenderChromaDiff_UnknownFileType(t *testing.T) {
	rawDiff := "diff --git a/data.xyz b/data.xyz\n--- a/data.xyz\n+++ b/data.xyz\n@@ -1 +1 @@\n-old\n+new\n"
	theme := DefaultDiffTheme()

	// Should fall back to fallback lexer
	out, err := renderChromaDiff(rawDiff, "data.xyz", theme, 80)
	if err != nil {
		t.Fatalf("renderChromaDiff: %v", err)
	}
	if !strings.Contains(out, "new") {
		t.Error("should still contain content with fallback lexer")
	}
}

func TestRenderChromaDiff_EmptyStyle(t *testing.T) {
	rawDiff := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n"
	theme := DefaultDiffTheme()
	theme.ChromaSyntaxStyle = "" // should fall back to monokai

	_, err := renderChromaDiff(rawDiff, "x.go", theme, 80)
	if err != nil {
		t.Fatalf("renderChromaDiff with empty style: %v", err)
	}
}

func TestFormatLineNumbers_AllKinds(t *testing.T) {
	theme := DefaultDiffTheme()

	// Added line: old should be blank, new should show number
	added := formatLineNumbers(diffLine{kind: diffAdded, newNum: 42}, theme)
	if !strings.Contains(added, "42") {
		t.Error("added line should show new line number")
	}

	// Removed line: old should show number, new should be blank
	removed := formatLineNumbers(diffLine{kind: diffRemoved, oldNum: 10}, theme)
	if !strings.Contains(removed, "10") {
		t.Error("removed line should show old line number")
	}

	// Context line: both numbers
	ctx := formatLineNumbers(diffLine{kind: diffContext, oldNum: 5, newNum: 5}, theme)
	if !strings.Contains(ctx, "5") {
		t.Error("context line should show line numbers")
	}
}

func TestGetChromaDiffWithContext(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	theme := DefaultDiffTheme()
	out, err := GetChromaDiffWithContext("main", "feature", "file.go", 3, theme, 80)
	if err != nil {
		t.Fatalf("GetChromaDiffWithContext: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(out, "file.go") {
		t.Error("output should contain file name")
	}
}

func TestGetChromaDiffWithContext_NoChanges(t *testing.T) {
	cleanup := setupTestRepo(t)
	defer cleanup()

	theme := DefaultDiffTheme()
	// Same branch = no diff
	out, err := GetChromaDiffWithContext("main", "main", "file.go", 3, theme, 80)
	if err != nil {
		t.Fatalf("GetChromaDiffWithContext: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty output for same branch, got %q", out)
	}
}

func TestAnsiColorize(t *testing.T) {
	result := ansiColorize("hello", "#FF0000")
	if !strings.Contains(result, "hello") {
		t.Error("should contain the text")
	}
	if !strings.Contains(result, "\x1b[38;2;255;0;0m") {
		t.Error("should contain red ANSI foreground")
	}
	if !strings.HasSuffix(result, "\x1b[0m") {
		t.Error("should end with reset")
	}
}

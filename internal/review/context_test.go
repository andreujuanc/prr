package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/security"
)

func TestAttachFileDiffs_PopulatesPerCall(t *testing.T) {
	calls := []ReviewCall{
		{Type: "individual", Files: []string{"a.go"}, AOIs: []security.AreaOfInterest{{File: "a.go", Line: 1}}},
		{Type: "grouped", Files: []string{"a.go", "b.go"}, AOIs: []security.AreaOfInterest{
			{File: "a.go", Line: 1},
			{File: "b.go", Line: 1},
		}},
	}
	rawDiffs := map[string]string{
		"a.go": "diff for a",
		"b.go": "diff for b",
		"c.go": "unused",
	}

	AttachFileDiffs(calls, rawDiffs)

	if got := calls[0].FileDiffs["a.go"]; got != "diff for a" {
		t.Errorf("call 0: a.go diff = %q, want %q", got, "diff for a")
	}
	if _, ok := calls[0].FileDiffs["b.go"]; ok {
		t.Errorf("call 0 should not carry b.go diff — not in its Files")
	}
	if got := calls[1].FileDiffs["a.go"]; got != "diff for a" {
		t.Errorf("call 1: a.go diff = %q, want %q", got, "diff for a")
	}
	if got := calls[1].FileDiffs["b.go"]; got != "diff for b" {
		t.Errorf("call 1: b.go diff = %q, want %q", got, "diff for b")
	}
}

func TestAttachFileDiffs_MissingFileSkippedSilently(t *testing.T) {
	calls := []ReviewCall{
		{Files: []string{"a.go", "missing.go"}, AOIs: []security.AreaOfInterest{{File: "a.go", Line: 1}}},
	}
	rawDiffs := map[string]string{"a.go": "diff for a"}

	AttachFileDiffs(calls, rawDiffs)

	if _, ok := calls[0].FileDiffs["missing.go"]; ok {
		t.Errorf("missing.go should not appear in FileDiffs")
	}
	if calls[0].FileDiffs["a.go"] != "diff for a" {
		t.Errorf("a.go diff should be present")
	}
}

func TestAttachFileDiffs_NoOpOnEmpty(t *testing.T) {
	// nil rawDiffs → no-op.
	calls := []ReviewCall{{Files: []string{"a.go"}}}
	AttachFileDiffs(calls, nil)
	if calls[0].FileDiffs != nil {
		t.Errorf("FileDiffs should remain nil when rawDiffs is empty")
	}
}

func TestAttachAOISources_SlicesAroundAOI(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"line1", "line2", "line3", "line4", "line5",
		"line6", "line7", "line8", "line9", "line10",
		"line11", "line12", "line13", "line14", "line15",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := []ReviewCall{
		{
			Type:  "individual",
			Files: []string{"f.go"},
			AOIs:  []security.AreaOfInterest{{File: "f.go", Line: 5, EndLine: 6}},
		},
	}
	AttachAOISources(calls, dir, 2)

	if len(calls[0].AOISources) != 1 {
		t.Fatalf("expected 1 AOI context, got %d", len(calls[0].AOISources))
	}
	ctx := calls[0].AOISources[0]
	// With context=2, AOI on lines 5-6, expect lines 3..8.
	for _, want := range []string{"line3", "line4", "line5", "line6", "line7", "line8"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("expected context to contain %q; got:\n%s", want, ctx)
		}
	}
	for _, unwanted := range []string{"line2", "line9"} {
		if strings.Contains(ctx, unwanted) {
			t.Errorf("did not expect context to contain %q; got:\n%s", unwanted, ctx)
		}
	}
}

func TestAttachAOISources_ClampsToFileStart(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{"line1", "line2", "line3", "line4"}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := []ReviewCall{{
		Files: []string{"f.go"},
		AOIs:  []security.AreaOfInterest{{File: "f.go", Line: 1, EndLine: 1}},
	}}
	AttachAOISources(calls, dir, 10)

	ctx := calls[0].AOISources[0]
	if !strings.Contains(ctx, "line1") || !strings.Contains(ctx, "line4") {
		t.Errorf("expected full file (line1..line4); got:\n%s", ctx)
	}
	// First line in the output should be line 1 (no underflow).
	if !strings.HasPrefix(strings.TrimLeft(ctx, " "), "1") {
		t.Errorf("expected output to start with line number 1; got:\n%s", ctx)
	}
}

func TestAttachAOISources_ClampsToFileEnd(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{"line1", "line2", "line3", "line4"}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := []ReviewCall{{
		Files: []string{"f.go"},
		AOIs:  []security.AreaOfInterest{{File: "f.go", Line: 4, EndLine: 4}},
	}}
	AttachAOISources(calls, dir, 10)

	ctx := calls[0].AOISources[0]
	if !strings.Contains(ctx, "line4") {
		t.Errorf("expected last line in context; got:\n%s", ctx)
	}
	// Ensure context didn't try to read past the end (no error, no
	// garbage).
	if strings.Contains(ctx, "line5") {
		t.Errorf("context should not contain non-existent line5; got:\n%s", ctx)
	}
}

func TestAttachAOISources_DedupReads(t *testing.T) {
	// Two AOIs in the same file should result in one disk read but
	// two distinct slices. The simplest observable signal of dedup
	// is correctness for both AOIs after a single AttachAOISources
	// call.
	dir := t.TempDir()
	body := strings.Join([]string{
		"line1", "line2", "line3", "line4", "line5",
		"line6", "line7", "line8", "line9", "line10",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := []ReviewCall{{
		Files: []string{"f.go"},
		AOIs: []security.AreaOfInterest{
			{File: "f.go", Line: 2, EndLine: 2},
			{File: "f.go", Line: 8, EndLine: 8},
		},
	}}
	AttachAOISources(calls, dir, 1)

	if len(calls[0].AOISources) != 2 {
		t.Fatalf("expected 2 contexts, got %d", len(calls[0].AOISources))
	}
	if !strings.Contains(calls[0].AOISources[0], "line2") ||
		strings.Contains(calls[0].AOISources[0], "line8") {
		t.Errorf("first context should be around line 2 only; got:\n%s", calls[0].AOISources[0])
	}
	if !strings.Contains(calls[0].AOISources[1], "line8") ||
		strings.Contains(calls[0].AOISources[1], "line2") {
		t.Errorf("second context should be around line 8 only; got:\n%s", calls[0].AOISources[1])
	}
}

func TestAttachAOISources_MissingFileLeavesEmpty(t *testing.T) {
	calls := []ReviewCall{{
		Files: []string{"missing.go"},
		AOIs:  []security.AreaOfInterest{{File: "missing.go", Line: 1}},
	}}
	AttachAOISources(calls, t.TempDir(), 5)

	if len(calls[0].AOISources) != 1 || calls[0].AOISources[0] != "" {
		t.Errorf("expected one empty context entry for unreadable file; got %#v", calls[0].AOISources)
	}
}

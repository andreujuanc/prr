package classify

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/aitesting"
)

// TestClassifyPrompt_NoToolNamesLeakIntoClaudeCode mirrors the leak-
// prevention tests in internal/ai and internal/security: catches future
// regressions where someone adds a prr-specific tool name (read_file,
// git_diff, etc.) to classify.md without rephrasing or adding the
// {{TOOLS}} placeholder. Currently classify.md doesn't mention tools
// at all — the classifier just inspects file paths — but if that
// changes, Claude Code prompts must not see prr's tool names.
func TestClassifyPrompt_NoToolNamesLeakIntoClaudeCode(t *testing.T) {
	resolved := ai.ResolveTools(classifyPrompt, aitesting.ClaudeCodeProvider{})
	var leaked []string
	for _, tn := range aitesting.PrrSpecificToolNames {
		if strings.Contains(resolved, tn) {
			leaked = append(leaked, tn)
		}
	}
	if len(leaked) > 0 {
		t.Errorf("classify.md leaked tool names into Claude Code resolve: %v\n"+
			"If you added a tool reference, either rephrase it to neutral prose "+
			"(\"search the codebase\", \"read the file\") or insert {{TOOLS}} to "+
			"inject the canonical tool block for harness providers.", leaked)
	}
}

func TestDimensionsForType(t *testing.T) {
	tests := []struct {
		ft       FileType
		wantMin  int
		mustHave []string
		mustNot  []string
	}{
		{
			ft:       FileTypeTest,
			wantMin:  2,
			mustHave: []string{"testing", "correctness"},
			mustNot:  []string{"test-coverage", "authentication", "authorization"},
		},
		{
			ft:       FileTypeHandler,
			wantMin:  5,
			mustHave: []string{"input-validation", "authentication", "authorization", "web-security", "error-handling", "observability", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:       FileTypeRepository,
			wantMin:  4,
			mustHave: []string{"data-integrity", "input-validation", "error-handling", "resource-management", "observability", "test-coverage"},
			mustNot:  []string{"testing", "web-security"},
		},
		{
			// SQL files have a deliberately different dimension set
			// from FileTypeRepository — pin the contract so the two
			// don't drift toward each other accidentally.
			ft:       FileTypeSQL,
			wantMin:  4,
			mustHave: []string{"data-integrity", "correctness", "performance", "design"},
			// SQL files have no calling-side concerns: no connection
			// lifecycle, no error wrapping, no input-validation (that's
			// the calling code's job). If these appear here, the
			// mapping has regressed toward FileTypeRepository.
			mustNot: []string{"testing", "web-security", "error-handling", "resource-management", "input-validation"},
		},
		{
			ft:       FileTypeModel,
			wantMin:  3,
			mustHave: []string{"api-design", "input-validation", "data-integrity", "test-coverage"},
			mustNot:  []string{"testing", "web-security", "observability"},
		},
		{
			ft:       FileTypeClient,
			wantMin:  4,
			mustHave: []string{"external-io", "error-handling", "resource-management", "observability", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:       FileTypeWorker,
			wantMin:  4,
			mustHave: []string{"concurrency", "error-handling", "resource-management", "observability", "test-coverage"},
			mustNot:  []string{"testing", "web-security"},
		},
		{
			ft:       FileTypeBusinessLogic,
			wantMin:  5,
			mustHave: []string{"correctness", "data-integrity", "error-handling", "design", "observability", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:       FileTypeInfrastructure,
			wantMin:  3,
			mustHave: []string{"configuration", "error-handling", "resource-management", "web-security", "observability", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:      FileTypeUnknown,
			wantMin: 10,
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.ft), func(t *testing.T) {
			dims := DimensionsForType(tt.ft)
			if len(dims) < tt.wantMin {
				t.Errorf("got %d dimensions, want at least %d: %v", len(dims), tt.wantMin, dims)
			}

			dimSet := make(map[string]bool, len(dims))
			for _, d := range dims {
				dimSet[d] = true
			}

			for _, must := range tt.mustHave {
				if !dimSet[must] {
					t.Errorf("missing required dimension %q in %v", must, dims)
				}
			}
			for _, mustNot := range tt.mustNot {
				if dimSet[mustNot] {
					t.Errorf("should not contain dimension %q in %v", mustNot, dims)
				}
			}
		})
	}
}

func TestIsValidFileType(t *testing.T) {
	valid := []FileType{
		FileTypeTest, FileTypeHandler, FileTypeRepository, FileTypeSQL,
		FileTypeModel, FileTypeClient, FileTypeWorker, FileTypeBusinessLogic,
		FileTypeInfrastructure, FileTypeUnknown,
	}
	for _, ft := range valid {
		if !isValidFileType(ft) {
			t.Errorf("isValidFileType(%q) = false, want true", ft)
		}
	}

	invalid := []FileType{"foo", "bar", "http-handler", ""}
	for _, ft := range invalid {
		if isValidFileType(ft) {
			t.Errorf("isValidFileType(%q) = true, want false", ft)
		}
	}
}

func TestParseResult(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []FileClassification
		wantErr bool
	}{
		{
			name: "clean JSON",
			raw:  `[{"file": "main.go", "type": "infrastructure"}, {"file": "handler.go", "type": "handler"}]`,
			want: []FileClassification{
				{File: "main.go", Type: FileTypeInfrastructure},
				{File: "handler.go", Type: FileTypeHandler},
			},
		},
		{
			name: "with markdown fences",
			raw:  "```json\n[{\"file\": \"foo_test.go\", \"type\": \"test\"}]\n```",
			want: []FileClassification{
				{File: "foo_test.go", Type: FileTypeTest},
			},
		},
		{
			name: "with leading prose",
			raw:  "Here are the results:\n[{\"file\": \"repo.go\", \"type\": \"repository\"}]",
			want: []FileClassification{
				{File: "repo.go", Type: FileTypeRepository},
			},
		},
		{
			name: "invalid type gets normalized to unknown",
			raw:  `[{"file": "weird.go", "type": "something-invalid"}]`,
			want: []FileClassification{
				{File: "weird.go", Type: FileTypeUnknown},
			},
		},
		{
			// Pin that "sql" round-trips through parsing without
			// being coerced to unknown — i.e. it's a first-class
			// member of AllFileTypes, not an alias for repository.
			name: "sql type is preserved",
			raw:  `[{"file": "migrations/0042_add_users.sql", "type": "sql"}]`,
			want: []FileClassification{
				{File: "migrations/0042_add_users.sql", Type: FileTypeSQL},
			},
		},
		{
			name:    "no JSON",
			raw:     "I cannot classify these files.",
			wantErr: true,
		},
		{
			name:    "empty response",
			raw:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseResult(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d results, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].File != tt.want[i].File {
					t.Errorf("result[%d].File = %q, want %q", i, got[i].File, tt.want[i].File)
				}
				if got[i].Type != tt.want[i].Type {
					t.Errorf("result[%d].Type = %q, want %q", i, got[i].Type, tt.want[i].Type)
				}
			}
		})
	}
}

func TestBuildBatches(t *testing.T) {
	const remainder = 20
	totalFiles := batchMaxFiles*2 + remainder

	var files []File
	for i := 0; i < totalFiles; i++ {
		files = append(files, File{
			Path:    fmt.Sprintf("file%d.go", i),
			Content: "package main",
		})
	}

	batches := buildBatches(files)

	wantBatches := 3
	if len(batches) != wantBatches {
		t.Fatalf("got %d batches, want %d", len(batches), wantBatches)
	}
	wantSizes := []int{batchMaxFiles, batchMaxFiles, remainder}
	for i, want := range wantSizes {
		if got := len(batches[i]); got != want {
			t.Errorf("batch %d: got %d files, want %d", i, got, want)
		}
	}
}

func TestBuildBatches_SingleBatch(t *testing.T) {
	files := []File{
		{Path: "a.go", Content: "package a"},
		{Path: "b.go", Content: "package b"},
	}

	batches := buildBatches(files)
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Errorf("batch 0: got %d files, want 2", len(batches[0]))
	}
}

func TestBuildBatches_Empty(t *testing.T) {
	batches := buildBatches(nil)
	if len(batches) != 0 {
		t.Errorf("got %d batches, want 0", len(batches))
	}
}

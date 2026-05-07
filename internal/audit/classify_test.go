package audit

import (
	"fmt"
	"testing"
)

func TestDimensionsForType(t *testing.T) {
	tests := []struct {
		ft       FileType
		wantMin  int // minimum number of dimensions
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
			mustHave: []string{"input-validation", "authentication", "authorization", "error-handling", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:       FileTypeRepository,
			wantMin:  4,
			mustHave: []string{"data-integrity", "input-validation", "error-handling", "resource-management", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:       FileTypeModel,
			wantMin:  3,
			mustHave: []string{"api-design", "input-validation", "data-integrity", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:       FileTypeClient,
			wantMin:  4,
			mustHave: []string{"external-io", "error-handling", "resource-management", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:       FileTypeWorker,
			wantMin:  4,
			mustHave: []string{"concurrency", "error-handling", "resource-management", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:       FileTypeBusinessLogic,
			wantMin:  5,
			mustHave: []string{"correctness", "data-integrity", "error-handling", "design", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:       FileTypeInfrastructure,
			wantMin:  3,
			mustHave: []string{"configuration", "error-handling", "resource-management", "test-coverage"},
			mustNot:  []string{"testing"},
		},
		{
			ft:      FileTypeUnknown,
			wantMin: 10, // should return all dimensions
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
		FileTypeTest, FileTypeHandler, FileTypeRepository, FileTypeModel,
		FileTypeClient, FileTypeWorker, FileTypeBusinessLogic,
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

func TestParseClassifyResult(t *testing.T) {
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
			got, err := parseClassifyResult(tt.raw)
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

func TestBuildClassifyBatches(t *testing.T) {
	// Create files to batch
	var files []AuditFile
	for i := 0; i < 120; i++ {
		files = append(files, AuditFile{
			Path:    fmt.Sprintf("file%d.go", i),
			Content: "package main",
		})
	}

	batches := buildClassifyBatches(files)

	// With classifyBatchMaxFiles=50, expect 3 batches: 50, 50, 20
	if len(batches) != 3 {
		t.Fatalf("got %d batches, want 3", len(batches))
	}
	if len(batches[0]) != 50 {
		t.Errorf("batch 0: got %d files, want 50", len(batches[0]))
	}
	if len(batches[1]) != 50 {
		t.Errorf("batch 1: got %d files, want 50", len(batches[1]))
	}
	if len(batches[2]) != 20 {
		t.Errorf("batch 2: got %d files, want 20", len(batches[2]))
	}
}

func TestBuildClassifyBatches_SingleBatch(t *testing.T) {
	files := []AuditFile{
		{Path: "a.go", Content: "package a"},
		{Path: "b.go", Content: "package b"},
	}

	batches := buildClassifyBatches(files)
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	if len(batches[0]) != 2 {
		t.Errorf("batch 0: got %d files, want 2", len(batches[0]))
	}
}

func TestBuildClassifyBatches_Empty(t *testing.T) {
	batches := buildClassifyBatches(nil)
	if len(batches) != 0 {
		t.Errorf("got %d batches, want 0", len(batches))
	}
}

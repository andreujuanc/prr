package review

import (
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

func TestBuildFindingsFromDeep_FieldMapping(t *testing.T) {
	deep := []state.DeepFinding{
		{
			FindingID:           "F-001",
			File:                "internal/foo.go",
			Lines:               "42",
			Severity:            "medium",
			Category:            state.MustParseCategory("correctness"),
			Title:               "Title",
			Description:         "Detail goes here",
			Suggestion:          "Do X",
			ConfidenceScore:     85,
			ConfidenceReasoning: "Saw it directly",
		},
	}

	got := BuildFindingsFromDeep(deep)
	if len(got) != 1 {
		t.Fatalf("len(got)=%d, want 1", len(got))
	}
	f := got[0]
	if f.File != "internal/foo.go" || f.Line != 42 {
		t.Errorf("file/line: got %s:%d", f.File, f.Line)
	}
	if f.Severity != "medium" || f.Category.String() != "correctness" {
		t.Errorf("severity/category: got %s / %s", f.Severity, f.Category)
	}
	if f.Title != "Title" || f.Detail != "Detail goes here" || f.Suggestion != "Do X" {
		t.Errorf("title/detail/suggestion mismatch: %+v", f)
	}
	if f.ConfidenceScore != 85 || f.ConfidenceReasoning != "Saw it directly" {
		t.Errorf("confidence mismatch: %d / %q", f.ConfidenceScore, f.ConfidenceReasoning)
	}
	if len(f.SourceIDs) != 1 || f.SourceIDs[0] != "F-001" {
		t.Errorf("source_ids: got %v", f.SourceIDs)
	}
}

func TestBuildFindingsFromDeep_LineRange(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"31", 31},
		{"31-33", 31},
		{"  42  ", 42},
		{"", 0},
		{"abc", 0},
	}
	for _, tt := range tests {
		got := firstLine(tt.in)
		if got != tt.want {
			t.Errorf("firstLine(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestBuildFindingsFromDeep_SortsBySeverity(t *testing.T) {
	deep := []state.DeepFinding{
		{FindingID: "F-1", Severity: "low", Title: "lo"},
		{FindingID: "F-2", Severity: "critical", Title: "crit"},
		{FindingID: "F-3", Severity: "medium", Title: "med"},
	}
	got := BuildFindingsFromDeep(deep)
	if got[0].Severity != "critical" || got[1].Severity != "medium" || got[2].Severity != "low" {
		t.Errorf("sort: got %s/%s/%s", got[0].Severity, got[1].Severity, got[2].Severity)
	}
}

func TestBuildFindingsFromDeep_EmptyInput(t *testing.T) {
	if got := BuildFindingsFromDeep(nil); len(got) != 0 {
		t.Errorf("len(got)=%d, want 0", len(got))
	}
}

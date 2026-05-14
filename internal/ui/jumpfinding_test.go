package ui

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

func TestJumpToFinding_PRLevel_FlashesNoFile(t *testing.T) {
	m := newTestModel(t)
	m.reviewFindings = []state.ReviewFinding{
		{Severity: "high", Category: "architecture", File: "", Line: 0, Title: "wide-scope"},
	}
	prevViewMode := m.viewMode

	cmd := m.jumpToFinding(0)
	if cmd != nil {
		t.Fatalf("PR-level finding should return nil cmd, got %T", cmd)
	}
	if m.flashMsg == "" || !strings.Contains(strings.ToLower(m.flashMsg), "pr-level") {
		t.Fatalf("expected flash message about PR-level finding, got %q", m.flashMsg)
	}
	if m.viewMode != prevViewMode {
		t.Fatalf("viewMode should be unchanged for PR-level finding")
	}
}

func TestJumpToFinding_FileLevel_OpensFileAtLineOne(t *testing.T) {
	m := newTestModel(t)
	m.reviewFindings = []state.ReviewFinding{
		{Severity: "high", Category: "bug", File: "internal/ui/model.go", Line: 0, Title: "x"},
	}

	cmd := m.jumpToFinding(0)
	if cmd == nil {
		t.Fatal("file-level finding should queue a diff fetch")
	}
	if m.selectedFile != "internal/ui/model.go" {
		t.Fatalf("selectedFile = %q, want internal/ui/model.go", m.selectedFile)
	}
	if m.pendingScrollLine != 1 {
		t.Fatalf("pendingScrollLine = %d, want 1 (file-level → first line)",
			m.pendingScrollLine)
	}
	if m.viewMode != viewModeFile {
		t.Fatalf("viewMode = %v, want viewModeFile", m.viewMode)
	}
}

func TestJumpToFinding_Locatable_OpensFileAtLine(t *testing.T) {
	m := newTestModel(t)
	m.reviewFindings = []state.ReviewFinding{
		{Severity: "high", Category: "bug", File: "internal/ui/model.go", Line: 42, Title: "x"},
	}

	cmd := m.jumpToFinding(0)
	if cmd == nil {
		t.Fatal("locatable finding should queue a diff fetch")
	}
	if m.selectedFile != "internal/ui/model.go" {
		t.Fatalf("selectedFile = %q, want internal/ui/model.go", m.selectedFile)
	}
	if m.pendingScrollLine != 42 {
		t.Fatalf("pendingScrollLine = %d, want 42", m.pendingScrollLine)
	}
}

func TestJumpToFinding_OutOfRangeIdx_NoOp(t *testing.T) {
	m := newTestModel(t)
	m.reviewFindings = nil
	if cmd := m.jumpToFinding(0); cmd != nil {
		t.Fatalf("empty findings + idx=0 should return nil, got %T", cmd)
	}
	if m.flashMsg != "" {
		t.Fatalf("no flash should fire for out-of-range idx, got %q", m.flashMsg)
	}
}

package audit

import (
	"testing"
)

func TestTruncate_Short(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTruncate_Exact(t *testing.T) {
	if got := truncate("hello", 5); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTruncate_Long(t *testing.T) {
	got := truncate("hello world", 6)
	if got != "hello…" {
		t.Errorf("expected 'hello…', got %q", got)
	}
}

func TestTruncate_Empty(t *testing.T) {
	if got := truncate("", 5); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func makeProgressUI() *ProgressUI {
	return &ProgressUI{
		phases: []phaseInfo{
			{name: "phase1", label: "Filter", status: "waiting"},
			{name: "phase2", label: "AOI Scan", status: "waiting"},
			{name: "phase3", label: "Deep Review", status: "waiting"},
			{name: "phase4", label: "Synthesis", status: "waiting"},
		},
	}
}

func TestUpdateProgress_ActivatesPhase(t *testing.T) {
	m := makeProgressUI()
	m.updateProgress("phase1", "starting")

	if m.phases[0].status != "active" {
		t.Errorf("expected phase1 active, got %q", m.phases[0].status)
	}
	if m.phases[0].detail != "starting" {
		t.Errorf("expected detail 'starting', got %q", m.phases[0].detail)
	}
}

func TestUpdateProgress_MarksPreviousDone(t *testing.T) {
	m := makeProgressUI()
	m.updateProgress("phase1", "go")
	m.updateProgress("phase2", "scanning")

	if m.phases[0].status != "done" {
		t.Errorf("expected phase1 done, got %q", m.phases[0].status)
	}
	if m.phases[1].status != "active" {
		t.Errorf("expected phase2 active, got %q", m.phases[1].status)
	}
}

func TestUpdateProgress_ParsesPhase1Files(t *testing.T) {
	m := makeProgressUI()
	m.updateProgress("phase1", "Phase 1 complete: 42 files to audit")

	if m.totalFiles != 42 {
		t.Errorf("expected totalFiles 42, got %d", m.totalFiles)
	}
}

func TestUpdateProgress_ParsesPhase2ScanProgress(t *testing.T) {
	m := makeProgressUI()
	m.updateProgress("phase2", "AOI scan 3/5 complete")

	if m.scannedFiles != 3 {
		t.Errorf("expected scannedFiles 3, got %d", m.scannedFiles)
	}
	if m.totalFiles != 5 {
		t.Errorf("expected totalFiles 5, got %d", m.totalFiles)
	}
}

func TestUpdateProgress_ParsesPhase3Reviews(t *testing.T) {
	m := makeProgressUI()
	m.updateProgress("phase3", "Executing 10 review calls...")

	if m.totalReviews != 10 {
		t.Errorf("expected totalReviews 10, got %d", m.totalReviews)
	}
}

func TestUpdateProgress_ParsesPhase3Progress(t *testing.T) {
	m := makeProgressUI()
	m.updateProgress("phase3", "review 7/12")

	if m.doneReviews != 7 {
		t.Errorf("expected doneReviews 7, got %d", m.doneReviews)
	}
	if m.totalReviews != 12 {
		t.Errorf("expected totalReviews 12, got %d", m.totalReviews)
	}
}

func TestActiveProgress_NoActivePhase(t *testing.T) {
	m := makeProgressUI()
	if got := m.activeProgress(); got != 0 {
		t.Errorf("expected 0, got %f", got)
	}
}

func TestActiveProgress_Phase2(t *testing.T) {
	m := makeProgressUI()
	m.phases[1].status = "active"
	m.totalFiles = 10
	m.scannedFiles = 5

	if got := m.activeProgress(); got != 0.5 {
		t.Errorf("expected 0.5, got %f", got)
	}
}

func TestActiveProgress_Phase3(t *testing.T) {
	m := makeProgressUI()
	m.phases[2].status = "active"
	m.totalReviews = 4
	m.doneReviews = 3

	if got := m.activeProgress(); got != 0.75 {
		t.Errorf("expected 0.75, got %f", got)
	}
}

func TestActiveProgress_Phase2_ZeroTotal(t *testing.T) {
	m := makeProgressUI()
	m.phases[1].status = "active"
	m.totalFiles = 0

	if got := m.activeProgress(); got != 0 {
		t.Errorf("expected 0 for zero total, got %f", got)
	}
}

package ui

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/progress"
)

func TestReviewPhaseTracker_StartActivatesNothing(t *testing.T) {
	var tr reviewPhaseTracker
	tr.Start(defaultReviewPhases())
	if !tr.IsActive() {
		t.Fatal("tracker should be active after Start")
	}
	for _, p := range tr.phases {
		if p.Status != progress.PhaseWaiting {
			t.Fatalf("phase %q should start waiting, got %v", p.Def.Name, p.Status)
		}
	}
}

func TestReviewPhaseTracker_ActivateAdvancesEarlierPhases(t *testing.T) {
	var tr reviewPhaseTracker
	tr.Start(defaultReviewPhases())
	tr.Activate("phase1") // skip fetch/discovery/classify/aoi

	for _, name := range []string{"fetch", "discovery", "classify", "aoi"} {
		idx := tr.phaseIndex(name)
		if tr.phases[idx].Status != progress.PhaseDone {
			t.Fatalf("phase %q should be done after Activate(phase1), got %v",
				name, tr.phases[idx].Status)
		}
	}
	if tr.phases[tr.phaseIndex("phase1")].Status != progress.PhaseActive {
		t.Fatal("phase1 should be active")
	}
	for _, name := range []string{"recheck", "phase2"} {
		idx := tr.phaseIndex(name)
		if tr.phases[idx].Status != progress.PhaseWaiting {
			t.Fatalf("phase %q should remain waiting, got %v",
				name, tr.phases[idx].Status)
		}
	}
}

func TestReviewPhaseTracker_ActivateIsIdempotent(t *testing.T) {
	var tr reviewPhaseTracker
	tr.Start(defaultReviewPhases())
	tr.Activate("aoi")
	tr.Activate("aoi")
	tr.Activate("aoi")
	if tr.phases[tr.phaseIndex("aoi")].Status != progress.PhaseActive {
		t.Fatal("repeated Activate should keep the phase active")
	}
}

func TestReviewPhaseTracker_ResetClearsEverything(t *testing.T) {
	var tr reviewPhaseTracker
	tr.Start(defaultReviewPhases())
	tr.Activate("aoi")
	tr.SetCounter("aoi_total", 10)
	tr.Reset()
	if tr.IsActive() {
		t.Fatal("tracker should not be active after Reset")
	}
	if tr.phases != nil {
		t.Fatal("phases should be nil after Reset")
	}
	if tr.state != nil {
		t.Fatal("state should be nil after Reset")
	}
}

func TestReviewPhaseTracker_SetDetailUpdatesLastWriteWins(t *testing.T) {
	var tr reviewPhaseTracker
	tr.Start(defaultReviewPhases())
	tr.SetDetail("phase1", "first")
	tr.SetDetail("phase1", "second")
	if got := tr.phases[tr.phaseIndex("phase1")].Detail; got != "second" {
		t.Fatalf("detail = %q, want %q", got, "second")
	}
}

func TestReviewPhaseTracker_FailMarksTerminal(t *testing.T) {
	var tr reviewPhaseTracker
	tr.Start(defaultReviewPhases())
	tr.Activate("aoi")
	tr.Fail("aoi")
	if tr.phases[tr.phaseIndex("aoi")].Status != progress.PhaseError {
		t.Fatal("phase should be failed")
	}
}

func TestRenderReviewProgressView_InactiveReturnsEmpty(t *testing.T) {
	m := Model{}
	if got := m.renderReviewProgressView(60); got != "" {
		t.Fatalf("inactive tracker should produce empty output, got %q", got)
	}
}

func TestRenderReviewProgressView_RendersAllPhases(t *testing.T) {
	m := newTestModel(t)
	m.reviewProgress.Start(defaultReviewPhases())
	m.reviewProgress.Activate("phase1")
	m.reviewProgress.SetCounter("batches_total", 10)
	m.reviewProgress.SetCounter("batches_done", 3)
	m.reviewProgress.SetDetail("phase1", "internal/ui/model.go")

	out := m.renderReviewProgressView(120)
	if out == "" {
		t.Fatal("expected non-empty output for active tracker")
	}
	if !strings.Contains(stripANSI(out), "Deep Review") {
		t.Fatalf("expected Deep Review label, got: %s", stripANSI(out))
	}
	if !strings.Contains(stripANSI(out), "3/10") {
		t.Fatalf("expected counter 3/10, got: %s", stripANSI(out))
	}
	if !strings.Contains(stripANSI(out), "internal/ui/model.go") {
		t.Fatalf("expected active detail, got: %s", stripANSI(out))
	}
	// Counter and detail must NOT appear on waiting phases.
	lines := strings.SplitSeq(stripANSI(out), "\n")
	for line := range lines {
		if strings.Contains(line, "Synthesis") && strings.Contains(line, "10") {
			t.Fatalf("waiting phase should not show counter: %q", line)
		}
	}
}

func TestReviewPhaseTracker_FailActiveMarksAndSetsDetail(t *testing.T) {
	var tr reviewPhaseTracker
	tr.Start(defaultReviewPhases())
	tr.Activate("phase1")
	tr.FailActive("context deadline exceeded")

	got := tr.phases[tr.phaseIndex("phase1")]
	if got.Status != progress.PhaseError {
		t.Fatalf("expected phase1 failed, got %v", got.Status)
	}
	if got.Detail != "context deadline exceeded" {
		t.Fatalf("expected error reason on phase detail, got %q", got.Detail)
	}
}

func TestReviewPhaseTracker_FailActiveFallsBackToFirstWaiting(t *testing.T) {
	// No phase has been activated yet — FailActive should fail the
	// first waiting phase rather than swallow the error.
	var tr reviewPhaseTracker
	tr.Start(defaultReviewPhases())
	tr.FailActive("setup failed")

	first := tr.phases[0]
	if first.Status != progress.PhaseError {
		t.Fatalf("expected first phase failed when none was active, got %v",
			first.Status)
	}
}

func TestRenderReviewProgressView_TruncatesLongDetail(t *testing.T) {
	m := newTestModel(t)
	m.reviewProgress.Start(defaultReviewPhases())
	m.reviewProgress.Activate("phase1")
	longDetail := strings.Repeat("very-deeply-nested-path/", 20) + "model.go"
	m.reviewProgress.SetDetail("phase1", longDetail)

	out := m.renderReviewProgressView(80)
	for line := range strings.SplitSeq(out, "\n") {
		if len(stripANSI(line)) > 100 {
			t.Fatalf("line exceeded reasonable width: len=%d %q",
				len(stripANSI(line)), stripANSI(line))
		}
	}
}

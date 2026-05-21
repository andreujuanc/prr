package progress

import (
	"testing"
	"time"
)

// TestParseBatchEvent_LifecycleTransitions exercises one batch row
// from init → active → stream → done. Each stage updates the
// in-memory BatchState — the Batches panel reads the same struct to
// draw the row.
func TestParseBatchEvent_LifecycleTransitions(t *testing.T) {
	s := &State{}
	before := time.Now()

	if !ParseBatchEvent(s, `Batch 3: init label="internal/ui" files=4 kind=general`) {
		t.Fatal("init: expected match, got false")
	}
	b := s.Batches[2]
	if b == nil {
		t.Fatal("init: batch 2 (1-based 3) not populated")
	}
	if b.Label != "internal/ui" || b.Files != 4 || b.Kind != "general" {
		t.Errorf("init: got %+v, want label=internal/ui files=4 kind=general", b)
	}
	if b.Status != BatchQueued {
		t.Errorf("init: status = %q, want %q", b.Status, BatchQueued)
	}
	if !b.StartedAt.IsZero() {
		t.Errorf("init: StartedAt should be zero, got %v", b.StartedAt)
	}

	if !ParseBatchEvent(s, "Batch 3: active") {
		t.Fatal("active: expected match")
	}
	if b.Status != BatchActive {
		t.Errorf("active: status = %q, want %q", b.Status, BatchActive)
	}
	if b.StartedAt.Before(before) || b.StartedAt.IsZero() {
		t.Errorf("active: StartedAt not set; got %v", b.StartedAt)
	}

	if !ParseBatchEvent(s, "Batch 3: stream bytes=1024") {
		t.Fatal("stream: expected match")
	}
	if b.Bytes != 1024 {
		t.Errorf("stream: bytes = %d, want 1024", b.Bytes)
	}

	if !ParseBatchEvent(s, "Batch 3: done") {
		t.Fatal("done: expected match")
	}
	if b.Status != BatchDone {
		t.Errorf("done: status = %q, want %q", b.Status, BatchDone)
	}
	if b.EndedAt.Before(before) || b.EndedAt.IsZero() {
		t.Errorf("done: EndedAt not set; got %v", b.EndedAt)
	}
}

// TestParseBatchEvent_LabelWithSpacesAndBrackets ensures the quoted
// label survives spaces, brackets, and slashes — labels like
// "auth/injection [critical]" come straight from the AOI router.
func TestParseBatchEvent_LabelWithSpacesAndBrackets(t *testing.T) {
	s := &State{}
	ok := ParseBatchEvent(s, `Batch 1: init label="auth/injection [critical]" files=2 kind=aoi-driven`)
	if !ok {
		t.Fatal("expected match")
	}
	b := s.Batches[0]
	if b == nil {
		t.Fatal("batch 0 not populated")
	}
	if b.Label != "auth/injection [critical]" {
		t.Errorf("label = %q, want %q", b.Label, "auth/injection [critical]")
	}
	if b.Kind != "aoi-driven" {
		t.Errorf("kind = %q, want aoi-driven", b.Kind)
	}
}

// TestParseBatchEvent_CachedSkipsActive pins the cached path: cached
// batches never go through active, so EndedAt must still get set so
// the recent-completions tail can sort them.
func TestParseBatchEvent_CachedSkipsActive(t *testing.T) {
	s := &State{}
	ParseBatchEvent(s, `Batch 1: init label="x" files=1 kind=general`)
	ParseBatchEvent(s, "Batch 1: cached")
	b := s.Batches[0]
	if b.Status != BatchCached {
		t.Errorf("status = %q, want %q", b.Status, BatchCached)
	}
	if b.EndedAt.IsZero() {
		t.Error("cached: EndedAt should be set so tail-sort works")
	}
}

// TestParseBatchEvent_Failed pins failed-row creation. EndedAt is set
// so failed rows sort into the recent-completions tail.
func TestParseBatchEvent_Failed(t *testing.T) {
	s := &State{}
	ParseBatchEvent(s, `Batch 5: init label="boom" files=1 kind=aoi-driven`)
	ParseBatchEvent(s, "Batch 5: active")
	ParseBatchEvent(s, "Batch 5: failed")
	b := s.Batches[4]
	if b.Status != BatchFailed {
		t.Errorf("status = %q, want %q", b.Status, BatchFailed)
	}
	if b.EndedAt.IsZero() {
		t.Error("failed: EndedAt should be set")
	}
}

// TestParseBatchEvent_UnitToken pins the optional `unit=findings`
// token that recheck batches send. Deep-review batches omit it and
// must still parse cleanly with an empty Unit (rendered as "files").
func TestParseBatchEvent_UnitToken(t *testing.T) {
	s := &State{}

	// Recheck-shape: unit=findings present.
	ParseBatchEvent(s, `Batch 1: init label="correctness" files=3 kind=general unit=findings`)
	if got := s.Batches[0].Unit; got != "findings" {
		t.Errorf("recheck batch: Unit = %q, want %q", got, "findings")
	}
	if got := s.Batches[0].Files; got != 3 {
		t.Errorf("recheck batch: Files = %d, want 3", got)
	}

	// Deep-review-shape: no unit token. Unit stays empty so fmtCount
	// falls back to "files".
	ParseBatchEvent(s, `Batch 2: init label="internal/ui" files=4 kind=general`)
	if got := s.Batches[1].Unit; got != "" {
		t.Errorf("deep-review batch: Unit = %q, want empty", got)
	}
}

// TestFmtCount_DefaultUnit pins that an empty Unit renders as "files"
// (legacy shape) and that "findings" renders the findings noun. Tests
// both plural (n>=2) and singular (n==1) branches so column width
// stays constant.
func TestFmtCount_DefaultUnit(t *testing.T) {
	cases := []struct {
		n    int
		unit string
		want string
	}{
		{2, "", " 2 files"},
		{1, "", " 1 file "},
		{3, "findings", " 3 findings"},
		{1, "findings", " 1 finding "},
	}
	for _, c := range cases {
		if got := fmtCount(c.n, c.unit); got != c.want {
			t.Errorf("fmtCount(%d, %q) = %q, want %q", c.n, c.unit, got, c.want)
		}
	}
}

// TestParseBatchEvent_NonBatchMessage returns false for any message
// not starting with "Batch ".
func TestParseBatchEvent_NonBatchMessage(t *testing.T) {
	s := &State{}
	for _, m := range []string{
		"Initialized 5 batches (3 AOI-driven, 2 general)",
		"AOI scan 4/5",
		"synthesis received 1200",
		"",
		"Batchmanship",
	} {
		if ParseBatchEvent(s, m) {
			t.Errorf("expected no match for %q", m)
		}
	}
	if len(s.Batches) != 0 {
		t.Errorf("unexpected batches populated: %+v", s.Batches)
	}
}

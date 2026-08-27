package review

import (
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

// countBatchedFindings is the invariant every batching test needs: the
// split must not lose or duplicate a finding. A dismiss pass that drops
// a finding on the floor silently keeps it in the report unjudged.
func countBatchedFindings(batches [][]state.DeepFinding) int {
	n := 0
	for _, b := range batches {
		n += len(b)
	}
	return n
}

func TestSplitFindingsForDismiss_SeverityDecidesBatchSize(t *testing.T) {
	// 4 critical/high + 4 low, all one category. With focusSize=2 and
	// bulkSize=4 that is 2 focused batches + 1 bulk batch.
	var findings []state.DeepFinding
	for i := 0; i < 2; i++ {
		findings = append(findings, makeFinding("", "a.go", "1", "correctness", "crit", "critical"))
		findings = append(findings, makeFinding("", "a.go", "1", "correctness", "high", "high"))
	}
	for i := 0; i < 4; i++ {
		findings = append(findings, makeFinding("", "a.go", "1", "correctness", "low", "low"))
	}

	batches := splitFindingsForDismiss(findings, 2, 4, "high")

	if got := countBatchedFindings(batches); got != len(findings) {
		t.Fatalf("batching lost or duplicated findings: got %d, want %d", got, len(findings))
	}
	if len(batches) != 3 {
		t.Fatalf("batches = %d, want 3 (2 focused of 2 + 1 bulk of 4): %v", len(batches), batches)
	}

	// Focused batches lead, so the judgements that matter start first
	// rather than queueing behind bulk work.
	for _, f := range batches[0] {
		if severityRank(f.Severity) > severityRank("high") {
			t.Errorf("first batch must be focused, found %q", f.Severity)
		}
	}
	if len(batches[2]) != 4 {
		t.Errorf("bulk batch size = %d, want 4", len(batches[2]))
	}
}

// The severity split must not merge findings from different categories
// into one batch — the dismiss prompt is written per-category.
func TestSplitFindingsForDismiss_KeepsCategoriesSeparate(t *testing.T) {
	findings := []state.DeepFinding{
		makeFinding("", "a.go", "1", "correctness", "x", "low"),
		makeFinding("", "b.go", "2", "performance", "y", "low"),
	}
	batches := splitFindingsForDismiss(findings, 5, 20, "high")

	if len(batches) != 2 {
		t.Fatalf("batches = %d, want 2 (one per category)", len(batches))
	}
	for _, b := range batches {
		cat := b[0].Category
		for _, f := range b {
			if f.Category != cat {
				t.Errorf("batch mixes categories %q and %q", cat, f.Category)
			}
		}
	}
}

// All-one-band inputs must not produce an empty batch from the other
// band — an empty batch would be a wasted LLM call.
func TestSplitFindingsForDismiss_NoEmptyBatches(t *testing.T) {
	for _, sev := range []string{"critical", "nit"} {
		findings := []state.DeepFinding{
			makeFinding("", "a.go", "1", "correctness", "x", sev),
		}
		batches := splitFindingsForDismiss(findings, 5, 20, "high")
		if len(batches) != 1 {
			t.Errorf("severity %q: batches = %d, want 1", sev, len(batches))
		}
		for i, b := range batches {
			if len(b) == 0 {
				t.Errorf("severity %q: batch %d is empty", sev, i)
			}
		}
	}
}

func TestSplitFindingsForDismiss_Empty(t *testing.T) {
	if got := splitFindingsForDismiss(nil, 5, 20, "high"); len(got) != 0 {
		t.Errorf("nil findings produced %d batches, want 0", len(got))
	}
}

// The whole point of Item 3: the previous flat size of 3 made recheck
// one of the pipeline's largest strong-model consumers. This pins that
// the defaults cut the call count for a finding-rich, low-severity set,
// which is the shape that was expensive.
func TestSplitFindingsForDismiss_DefaultsCutCallsOnLowSeverityBulk(t *testing.T) {
	var findings []state.DeepFinding
	for i := 0; i < 60; i++ {
		findings = append(findings, makeFinding("", "a.go", "1", "correctness", "nit", "nit"))
	}

	oldStyle := splitFindingsByCategoryChunked(findings, 3)
	newStyle := splitFindingsForDismiss(findings,
		defaultDismissFocusBatchSize, defaultDismissBulkBatchSize, defaultDismissFocusSeverity)

	if countBatchedFindings(newStyle) != len(findings) {
		t.Fatalf("batching lost findings: %d vs %d", countBatchedFindings(newStyle), len(findings))
	}
	if len(newStyle) >= len(oldStyle) {
		t.Errorf("defaults did not reduce dismiss calls: %d batches vs %d at the old flat size of 3",
			len(newStyle), len(oldStyle))
	}
}

// Critical findings must NOT be swept into large batches by the
// defaults — the cost saving is meant to come out of low-severity
// findings only.
func TestSplitFindingsForDismiss_DefaultsKeepCriticalFocused(t *testing.T) {
	var findings []state.DeepFinding
	for i := 0; i < 20; i++ {
		findings = append(findings, makeFinding("", "a.go", "1", "correctness", "crit", "critical"))
	}
	batches := splitFindingsForDismiss(findings,
		defaultDismissFocusBatchSize, defaultDismissBulkBatchSize, defaultDismissFocusSeverity)

	for i, b := range batches {
		if len(b) > defaultDismissFocusBatchSize {
			t.Errorf("batch %d has %d critical findings, exceeds focus size %d",
				i, len(b), defaultDismissFocusBatchSize)
		}
	}
}

package ui

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

// TestSubmitModal_NoReview_NotOK verifies that the modal contract returns
// false when there is no review payload at all — bug 2 prerequisite.
func TestSubmitModal_NoReview_NotOK(t *testing.T) {
	m := newTestModel(t)
	m.reviewState = &state.State{}

	content, ok := m.renderSubmitReviewModal()
	if ok {
		t.Fatalf("expected ok=false with no Review; got ok=true content=%q", content)
	}
	if content != "" {
		t.Fatalf("expected empty content when ok=false, got %q", content)
	}
}

// TestSubmitModal_StructuredReview_OK verifies the happy path still
// renders unchanged with structured review present.
func TestSubmitModal_StructuredReview_OK(t *testing.T) {
	m := newTestModel(t)
	m.reviewState.Review = &state.AIReview{
		Structured: &state.ReviewOutput{
			Summary: "All good",
			Verdict: "approve",
		},
	}

	content, ok := m.renderSubmitReviewModal()
	if !ok {
		t.Fatalf("expected ok=true with structured review, got false")
	}
	if !strings.Contains(content, "APPROVED") {
		t.Fatalf("expected APPROVED verdict label in content, got: %s", content)
	}
	if !strings.Contains(content, "All good") {
		t.Fatalf("expected summary in content, got: %s", content)
	}
}

// TestSubmitModal_DeepOnly_OK is the bug-2 fix: a review that has only
// deep findings (no Structured) should still render a usable modal via
// the synthetic-review fallback rather than an empty box.
func TestSubmitModal_DeepOnly_OK(t *testing.T) {
	m := newTestModel(t)
	m.reviewState.Review = &state.AIReview{
		// Structured intentionally nil — pre-fix this produced an empty
		// modal.
	}
	m.reviewState.SetDeepFindings([]state.DeepFinding{
		{
			Severity:    "high",
			Category:    "security",
			File:        "foo.go",
			Lines:       "42",
			Title:       "Unvalidated input",
			Description: "User input flows into SQL",
			Suggestion:  "use a parameterized query",
		},
	})

	content, ok := m.renderSubmitReviewModal()
	if !ok {
		t.Fatalf("expected ok=true with deep-only findings (synthetic fallback), got false")
	}
	if content == "" {
		t.Fatal("expected non-empty content when ok=true")
	}
	// Synthetic review uses verdict "comment".
	if !strings.Contains(content, "COMMENT") {
		t.Fatalf("expected COMMENT verdict label from synthetic fallback, got: %s", content)
	}
}

// TestSubmitModal_DeepOnly_NoFindings_NotOK confirms the contract still
// returns false when there is literally nothing to submit.
func TestSubmitModal_DeepOnly_NoFindings_NotOK(t *testing.T) {
	m := newTestModel(t)
	m.reviewState.Review = &state.AIReview{}

	_, ok := m.renderSubmitReviewModal()
	if ok {
		t.Fatal("expected ok=false with no Structured and no deep findings")
	}
}

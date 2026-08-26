package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/andreujuanc/prr/internal/state"
)

// runCmd executes a tea.Cmd to extract whatever tea.Msg it produces.
// Returns nil when cmd is nil (a no-op success).
//
// The bubbletea program drives Cmds for us in production; tests have to
// drive them manually. Without this, AIChatDoneMsg's returned cmd (which
// triggers the async review render) silently drops on the floor in tests
// and the post-render contract goes unverified.
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// TestPRReviewFlow_SkipSynthesis_PopulatesReviewTab is the end-to-end
// regression test for the user-reported "review finished but Review tab
// is empty" bug. It walks the full state transition the production code
// runs through when a PR-level review completes with only deep findings:
//
//  1. triggerAIReview (PR-level) → aiPanelTab=tabReview, tracker active
//  2. AIChatDoneMsg arrives with DeepFindings, no Review (SkipSynthesis)
//  3. The success handler must:
//     - persist DeepFindings to reviewState
//     - leave aiPanelTab=tabReview
//     - kick off renderActiveAIView (Cmd → ReviewRenderedMsg)
//  4. ReviewRenderedMsg handler must populate reviewFindings and write
//     content into reviewViewport.
//
// Any link in that chain breaks the user-visible behavior.
func TestPRReviewFlow_SkipSynthesis_PopulatesReviewTab(t *testing.T) {
	m := newTestModel(t)
	m.aiStreaming = true
	m.aiPanelTab = tabReview
	m.reviewProgress.Start(defaultReviewPhases())

	deep := []state.DeepFinding{
		{
			AOIID:       "A1",
			FindingID:   "F-001",
			File:        "internal/ui/model.go",
			Lines:       "42",
			Severity:    "high",
			Category:    "security",
			Title:       "Unvalidated input",
			Description: "Input flows into SQL without sanitization",
			Suggestion:  "Use a parameterized query",
		},
	}

	// Step 1: AIChatDoneMsg success arrives.
	updated, cmd := m.Update(AIChatDoneMsg{
		Review:       nil, // SkipSynthesis path
		DeepFindings: deep,
	})
	m = updated.(Model)

	if m.aiStreaming {
		t.Fatalf("aiStreaming should be false after success done msg")
	}
	if m.aiPanelTab != tabReview {
		t.Fatalf("aiPanelTab = %d, want tabReview (%d)", m.aiPanelTab, tabReview)
	}
	if m.reviewProgress.IsActive() {
		t.Fatalf("reviewProgress should be reset after success done msg")
	}
	if len(m.reviewState.GetDeepFindings()) != 1 {
		t.Fatalf("DeepFindings should be persisted to reviewState, got %d",
			len(m.reviewState.GetDeepFindings()))
	}

	// Step 2: drive the Cmd returned from AIChatDoneMsg (which is what
	// renderActiveAIView produces — a func that returns ReviewRenderedMsg).
	msg := runCmd(cmd)
	if msg == nil {
		t.Fatalf("AIChatDoneMsg with deep findings should return a non-nil Cmd " +
			"to render the review")
	}
	rendered, ok := msg.(ReviewRenderedMsg)
	if !ok {
		t.Fatalf("expected ReviewRenderedMsg from the rendering Cmd, got %T (%v)", msg, msg)
	}
	if rendered.Content == "" {
		t.Fatal("ReviewRenderedMsg.Content should be non-empty when DeepFindings exist")
	}
	if len(rendered.Findings) == 0 {
		t.Fatal("ReviewRenderedMsg.Findings should be non-empty when DeepFindings exist")
	}

	// Step 3: deliver the ReviewRenderedMsg to the model.
	updated, _ = m.Update(rendered)
	m = updated.(Model)

	if len(m.reviewFindings) == 0 {
		t.Fatal("m.reviewFindings should be populated after ReviewRenderedMsg")
	}
	if m.aiReviewRendered == "" {
		t.Fatal("m.aiReviewRendered should be cached after ReviewRenderedMsg")
	}
	if got := m.reviewViewport.View(); !strings.Contains(got, "Unvalidated input") {
		t.Fatalf("reviewViewport content should contain the finding title; got:\n%s", got)
	}
}

// TestPRReviewFlow_NoFindings_LeavesReviewTabActive verifies the edge
// case the audit flagged: a review that completes with zero deep
// findings shouldn't fall through to the "regular chat" branch and
// silently leave the Review tab blank.
//
// The pipeline is responsible for stamping state.LastReview at end-of-
// run (see recordReviewMeta in internal/review/pipeline.go); the test
// pre-stamps it to isolate the TUI behavior under the contract.
func TestPRReviewFlow_NoFindings_LeavesReviewTabActive(t *testing.T) {
	m := newTestModel(t)
	m.aiStreaming = true
	m.aiPanelTab = tabReview
	m.reviewProgress.Start(defaultReviewPhases())

	// Simulate the pipeline having stamped LastReview before sending
	// the AIChatDoneMsg upstream. Without this the test is asserting
	// against a contract the pipeline owns, not the TUI.
	m.reviewState.SetLastReview(&state.ReviewMeta{
		Verdict:        "approve",
		Summary:        "No findings — PR looks clean.",
		FindingsCount:  0,
		DismissedCount: 0,
	})

	updated, cmd := m.Update(AIChatDoneMsg{
		Review:       nil,
		DeepFindings: nil,
	})
	m = updated.(Model)

	if m.aiPanelTab != tabReview {
		t.Fatalf("aiPanelTab = %d, want tabReview — a no-findings result "+
			"must NOT silently switch the user back to chat", m.aiPanelTab)
	}
	if !m.hasReview() {
		t.Fatal("hasReview() must be true after a clean review so the " +
			"Review tab doesn't render the misleading 'No review yet' message")
	}
	// Drive the rendering Cmd, if any. The clean-PR path writes the
	// placeholder synchronously and returns nil; we tolerate either.
	if cmd != nil {
		if msg := runCmd(cmd); msg != nil {
			updated, _ = m.Update(msg)
			m = updated.(Model)
		}
	}
	got := m.reviewViewport.View()
	if !strings.Contains(got, "No findings") && !strings.Contains(got, "looks clean") {
		t.Fatalf("reviewViewport should show a clean-PR message, got:\n%s", got)
	}
}

// TestPRReviewFlow_ErrorStampsLastReview verifies the error path now
// persists a LastReview marker with Error set, so a reopen of the PR
// shows "review failed: <reason>" instead of the misleading "no
// review yet" placeholder. This is the regression the user surfaced
// with PR 16: AOI calls failed the 20% threshold, the run aborted,
// and no proof-of-attempt survived in state.
func TestPRReviewFlow_ErrorStampsLastReview(t *testing.T) {
	m := newTestModel(t)
	m.aiStreaming = true
	m.aiPanelTab = tabReview
	m.reviewProgress.Start(defaultReviewPhases())
	m.reviewProgress.Activate("phase1")

	updated, _ := m.Update(AIChatDoneMsg{Err: errSimulated})
	m = updated.(Model)

	if m.reviewState.LastReview == nil {
		t.Fatal("error path must stamp state.LastReview so the failure " +
			"survives the TUI exit")
	}
	if got := m.reviewState.LastReview.Error; got != errSimulated.Error() {
		t.Fatalf("LastReview.Error = %q, want %q", got, errSimulated.Error())
	}
	if m.aiPanelTab != tabReview {
		t.Fatalf("error path must keep user on Review tab, got %d", m.aiPanelTab)
	}
}

// TestPRReviewFlow_ErrorWithPersistedFindings checks that when the
// pipeline errors after persisting partial deep findings, the user
// still lands on the Review tab with those findings rendered — not
// stuck on a frozen failure frame with no way to see what was found.
func TestPRReviewFlow_ErrorWithPersistedFindings(t *testing.T) {
	m := newTestModel(t)
	m.aiStreaming = true
	m.aiPanelTab = tabReview
	m.reviewProgress.Start(defaultReviewPhases())
	m.reviewProgress.Activate("phase1")

	// Simulate the pipeline having streamed some findings into state
	// before the error fired (via AppendDeepFindings).
	m.reviewState.SetDeepFindings([]state.DeepFinding{
		{
			AOIID: "A1", FindingID: "F-001",
			File: "internal/ui/model.go", Lines: "10",
			Severity: "medium", Category: "bug",
			Title:       "Partial finding",
			Description: "Streamed in before the error",
		},
	})

	updated, cmd := m.Update(AIChatDoneMsg{Err: errSimulated})
	m = updated.(Model)

	if m.aiPanelTab != tabReview {
		t.Fatalf("error path with persisted findings must keep aiPanelTab on review, got %d", m.aiPanelTab)
	}
	if cmd == nil {
		t.Fatal("expected a render Cmd to display the persisted findings")
	}
	// Drive the render Cmd → ReviewRenderedMsg → second Update.
	msg := runCmd(cmd)
	rendered, ok := msg.(ReviewRenderedMsg)
	if !ok {
		t.Fatalf("expected ReviewRenderedMsg from render Cmd, got %T", msg)
	}
	updated, _ = m.Update(rendered)
	m = updated.(Model)

	if !strings.Contains(m.reviewViewport.View(), "Partial finding") {
		t.Fatalf("persisted finding should be visible in Review tab; got:\n%s",
			m.reviewViewport.View())
	}
}

// errSimulated is a stand-in error for the failure-path test.
var errSimulated = simulatedError("simulated pipeline failure")

type simulatedError string

func (e simulatedError) Error() string { return string(e) }

// TestPRLoad_WithCachedReview_FocusesReviewTab verifies that when a PR
// is loaded and the persisted state already carries a review (cached
// from a previous session), the Review tab is auto-focused — the
// user shouldn't have to hunt for findings they previously generated.
func TestPRLoad_WithCachedReview_FocusesReviewTab(t *testing.T) {
	m := newTestModel(t)
	// Simulate a fresh launch landing on the Chat tab (some users prefer
	// to alt-tab between Chat and other tabs; we should still snap back
	// to Review when a cached review is loaded).
	m.aiPanelTab = tabChat

	// Build a state that carries a cached review.
	st := testState()
	st.Review = &state.AIReview{
		Summary: "Cached review from earlier run",
		Structured: &state.ReviewOutput{
			Verdict: "comment",
			Summary: "Cached review from earlier run",
		},
	}

	updated, _ := m.Update(DiffHashedMsg{
		State:    st,
		RawDiffs: testDiffs(),
	})
	m = updated.(Model)

	if m.aiPanelTab != tabReview {
		t.Fatalf("aiPanelTab = %d, want tabReview — cached review on load "+
			"must focus the Review tab", m.aiPanelTab)
	}
}

// TestPRLoad_WithoutCachedReview_LeavesTabAlone verifies the contract
// only fires when there's actually something to show. A fresh PR with
// no review shouldn't override an explicit user tab choice.
func TestPRLoad_WithoutCachedReview_LeavesTabAlone(t *testing.T) {
	m := newTestModel(t)
	m.aiPanelTab = tabChat

	st := testState() // no Review, no DeepFindings

	updated, _ := m.Update(DiffHashedMsg{
		State:    st,
		RawDiffs: testDiffs(),
	})
	m = updated.(Model)

	if m.aiPanelTab != tabChat {
		t.Fatalf("aiPanelTab = %d, want tabChat (unchanged) — a fresh PR "+
			"with no cached review should not silently switch tabs",
			m.aiPanelTab)
	}
}

package review

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// scriptedClient lets each test arrange a sequence of responses. The
// nth call returns responses[n]. errs[n], if non-nil, replaces the
// response with an error. Beyond the script length the client returns
// an error to make over-calling visible.
type scriptedClient struct {
	mu        sync.Mutex
	responses []string
	errs      []error
	calls     int
}

func (s *scriptedClient) ChatStream(_ context.Context, _ string, _ []ai.Message, _ func(string)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	if i >= len(s.responses) {
		return "", errors.New("scripted client: unexpected extra call")
	}
	if i < len(s.errs) && s.errs[i] != nil {
		return "", s.errs[i]
	}
	return s.responses[i], nil
}

func (s *scriptedClient) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// makeRawWithBadCategory constructs a rawDeepReviewResult with one
// finding whose Category is off-list. The fixture title is unique so
// assertions can identify it post-correction.
func makeRawWithBadCategory(badSlug string) *rawDeepReviewResult {
	rf := rawDeepFinding{
		DeepFinding: state.DeepFinding{
			AOIID:    "aoi-1",
			File:     "x.go",
			Lines:    "10",
			Severity: "high",
			Title:    "off-list-finding",
		},
		Category: badSlug,
		Status:   "finding",
	}
	return &rawDeepReviewResult{
		Type:     "individual",
		Findings: []rawDeepFinding{rf},
	}
}

func dummyCall() ReviewCall {
	return ReviewCall{
		Type: "individual",
		AOIs: []security.AreaOfInterest{{ID: "aoi-1", File: "x.go", Line: 10}},
	}
}

// TestVerifyAndCorrectCategory_HappyPath: model picks a bad category,
// corrector turn replies with a valid one, convertRawToTyped accepts.
func TestVerifyAndCorrectCategory_HappyPath(t *testing.T) {
	client := &scriptedClient{
		responses: []string{
			`{"corrections": [{"index": 0, "category": "correctness", "subcategory": "off-by-one"}]}`,
		},
	}
	raw := makeRawWithBadCategory("vibes")

	out := verifyAndCorrectCategory(
		context.Background(), client, dummyCall(),
		0, "system", nil, "original-raw", raw,
	)

	if client.callCount() != 1 {
		t.Fatalf("expected 1 corrector call, got %d", client.callCount())
	}
	if out.Findings[0].Category != "correctness" {
		t.Fatalf("category not corrected: %q", out.Findings[0].Category)
	}
	if out.Findings[0].Subcategory != "off-by-one" {
		t.Fatalf("subcategory not updated: %q", out.Findings[0].Subcategory)
	}

	typed, dropped := convertRawToTyped(dummyCall(), out)
	if len(dropped) != 0 {
		t.Fatalf("expected 0 drops; got %d", len(dropped))
	}
	if len(typed.Findings) != 1 {
		t.Fatalf("expected 1 typed finding; got %d", len(typed.Findings))
	}
	if typed.Findings[0].Category != state.MustParseCategory("correctness") {
		t.Fatalf("typed Category: %q", typed.Findings[0].Category)
	}
}

// TestVerifyAndCorrectCategory_SecondAttemptFixes: model picks a bad
// category, first correction still bad, second correction good.
func TestVerifyAndCorrectCategory_SecondAttemptFixes(t *testing.T) {
	client := &scriptedClient{
		responses: []string{
			`{"corrections": [{"index": 0, "category": "still-bad"}]}`,
			`{"corrections": [{"index": 0, "category": "correctness"}]}`,
		},
	}
	raw := makeRawWithBadCategory("vibes")

	out := verifyAndCorrectCategory(
		context.Background(), client, dummyCall(),
		0, "system", nil, "original-raw", raw,
	)

	if client.callCount() != 2 {
		t.Fatalf("expected 2 corrector calls (one per attempt), got %d", client.callCount())
	}
	if out.Findings[0].Category != "correctness" {
		t.Fatalf("category not corrected after 2 attempts: %q", out.Findings[0].Category)
	}
	_, dropped := convertRawToTyped(dummyCall(), out)
	if len(dropped) != 0 {
		t.Fatalf("expected 0 drops; got %d", len(dropped))
	}
}

// TestVerifyAndCorrectCategory_StillBadAfterLoop: every attempt keeps
// the category off-list. convertRawToTyped drops the finding and
// reports it via DroppedFinding.
func TestVerifyAndCorrectCategory_StillBadAfterLoop(t *testing.T) {
	client := &scriptedClient{
		responses: []string{
			`{"corrections": [{"index": 0, "category": "still-bad-1"}]}`,
			`{"corrections": [{"index": 0, "category": "still-bad-2"}]}`,
		},
	}
	raw := makeRawWithBadCategory("vibes")

	out := verifyAndCorrectCategory(
		context.Background(), client, dummyCall(),
		0, "system", nil, "original-raw", raw,
	)

	if client.callCount() != maxCategoryCorrectorAttempts {
		t.Fatalf("expected %d attempts, got %d", maxCategoryCorrectorAttempts, client.callCount())
	}
	typed, dropped := convertRawToTyped(dummyCall(), out)
	if len(typed.Findings) != 0 {
		t.Fatalf("expected the bad finding dropped, got %d kept", len(typed.Findings))
	}
	if len(dropped) != 1 {
		t.Fatalf("expected 1 drop; got %d", len(dropped))
	}
	if dropped[0].Title != "off-list-finding" {
		t.Fatalf("dropped finding title: %q", dropped[0].Title)
	}
}

// TestVerifyAndCorrectCategory_Withdraw: model withdraws the bad
// finding via the corrector. The finding is removed from rawResult
// before convertRawToTyped runs.
func TestVerifyAndCorrectCategory_Withdraw(t *testing.T) {
	client := &scriptedClient{
		responses: []string{
			`{"corrections": [{"index": 0, "withdraw": true, "reason": "no listed category fits"}]}`,
		},
	}
	raw := makeRawWithBadCategory("vibes")

	out := verifyAndCorrectCategory(
		context.Background(), client, dummyCall(),
		0, "system", nil, "original-raw", raw,
	)

	if len(out.Findings) != 0 {
		t.Fatalf("expected withdrawal to remove the finding; got %d kept", len(out.Findings))
	}
}

// TestVerifyAndCorrectCategory_TransientThenSuccess: first attempt
// gets a 503; RetryTransient retries and succeeds. Confirms the
// corrector inherits the existing retry contract.
func TestVerifyAndCorrectCategory_TransientThenSuccess(t *testing.T) {
	client := &scriptedClient{
		responses: []string{
			"",
			`{"corrections": [{"index": 0, "category": "correctness"}]}`,
		},
		errs: []error{
			errors.New("upstream 503 service unavailable"),
			nil,
		},
	}
	raw := makeRawWithBadCategory("vibes")

	out := verifyAndCorrectCategory(
		context.Background(), client, dummyCall(),
		0, "system", nil, "original-raw", raw,
	)

	if client.callCount() != 2 {
		t.Fatalf("expected retry after transient: 2 calls, got %d", client.callCount())
	}
	if out.Findings[0].Category != "correctness" {
		t.Fatalf("category not corrected after transient retry: %q", out.Findings[0].Category)
	}
}

// TestVerifyAndCorrectCategory_NoBadCategories_NoCall: when all
// findings have valid categories, the corrector never calls the LLM.
func TestVerifyAndCorrectCategory_NoBadCategories_NoCall(t *testing.T) {
	client := &scriptedClient{responses: []string{}}
	raw := &rawDeepReviewResult{
		Type: "individual",
		Findings: []rawDeepFinding{
			{
				DeepFinding: state.DeepFinding{Title: "good", File: "x.go"},
				Category:    "correctness",
				Status:      "finding",
			},
		},
	}

	verifyAndCorrectCategory(
		context.Background(), client, dummyCall(),
		0, "system", nil, "original-raw", raw,
	)

	if client.callCount() != 0 {
		t.Fatalf("corrector should not call when no bad categories; got %d calls", client.callCount())
	}
}

// TestBuildCategoryCorrectorMessage_IncludesAllListedFindings: the
// corrector message must reference every bad-category index by number
// and include the allowed-category list.
func TestBuildCategoryCorrectorMessage_IncludesAllListedFindings(t *testing.T) {
	findings := []rawDeepFinding{
		{DeepFinding: state.DeepFinding{Title: "first"}, Category: "vibes"},
		{DeepFinding: state.DeepFinding{Title: "second"}, Category: "energy"},
	}
	msg := buildCategoryCorrectorMessage(findings, []int{0, 1})

	if !strings.Contains(msg, "index 0") || !strings.Contains(msg, "index 1") {
		t.Fatalf("message should reference both bad indexes; got:\n%s", msg)
	}
	if !strings.Contains(msg, "first") || !strings.Contains(msg, "second") {
		t.Fatalf("message should mention both finding titles; got:\n%s", msg)
	}
	if !strings.Contains(msg, "correctness") {
		t.Fatalf("message should list known categories (correctness is real); got:\n%s", msg)
	}
}

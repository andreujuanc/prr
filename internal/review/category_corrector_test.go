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
		DeepReviewResult: state.DeepReviewResult{Type: "individual"},
		Findings:         []rawDeepFinding{rf},
	}
}

func dummyCall() ReviewCall {
	return ReviewCall{
		Type: "individual",
		AOIs: []security.AreaOfInterest{{ID: "aoi-1", File: "x.go", Line: 10}},
	}
}

func dummyCC(client ai.Client) correctorContext {
	return correctorContext{
		client:       client,
		call:         dummyCall(),
		systemPrompt: "system",
		originalRaw:  "original-raw",
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

	out := verifyAndCorrectCategory(context.Background(), dummyCC(client), raw)

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

	out := verifyAndCorrectCategory(context.Background(), dummyCC(client), raw)

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

	out := verifyAndCorrectCategory(context.Background(), dummyCC(client), raw)

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

	out := verifyAndCorrectCategory(context.Background(), dummyCC(client), raw)

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

	out := verifyAndCorrectCategory(context.Background(), dummyCC(client), raw)

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
		DeepReviewResult: state.DeepReviewResult{Type: "individual"},
		Findings: []rawDeepFinding{
			{
				DeepFinding: state.DeepFinding{Title: "good", File: "x.go"},
				Category:    "correctness",
				Status:      "finding",
			},
		},
	}

	verifyAndCorrectCategory(context.Background(), dummyCC(client), raw)

	if client.callCount() != 0 {
		t.Fatalf("corrector should not call when no bad categories; got %d calls", client.callCount())
	}
}

// TestConvertRawToTyped_PreservesEmbeddedFields guards the embed
// refactor — non-Findings fields (CrossCutting, Dismissals,
// RawOutput, CacheKey, Subcategory) must survive convertRawToTyped.
func TestConvertRawToTyped_PreservesEmbeddedFields(t *testing.T) {
	raw := &rawDeepReviewResult{
		DeepReviewResult: state.DeepReviewResult{
			Type:         "grouped",
			Category:     "correctness",
			Subcategory:  "off-by-one",
			CrossCutting: "watch for shared mutable state",
			CacheKey:     "deadbeef",
			RawOutput:    []byte(`{"x":1}`),
			Dismissals: []state.DeepDismissal{
				{AOIID: "aoi-1", File: "x.go", Rationale: "false positive"},
			},
		},
		Findings: []rawDeepFinding{
			{
				DeepFinding: state.DeepFinding{Title: "good", File: "x.go"},
				Category:    "correctness",
				Status:      "finding",
			},
		},
	}
	out, dropped := convertRawToTyped(dummyCall(), raw)
	if len(dropped) != 0 {
		t.Fatalf("unexpected drops: %v", dropped)
	}
	if out.Type != "grouped" || out.Category != "correctness" || out.Subcategory != "off-by-one" {
		t.Errorf("identity fields lost: %+v", out)
	}
	if out.CrossCutting != "watch for shared mutable state" {
		t.Errorf("CrossCutting lost: %q", out.CrossCutting)
	}
	if out.CacheKey != "deadbeef" {
		t.Errorf("CacheKey lost: %q", out.CacheKey)
	}
	if string(out.RawOutput) != `{"x":1}` {
		t.Errorf("RawOutput lost: %s", out.RawOutput)
	}
	if len(out.Dismissals) != 1 || out.Dismissals[0].AOIID != "aoi-1" {
		t.Errorf("Dismissals lost: %+v", out.Dismissals)
	}
	if len(out.Findings) != 1 || out.Findings[0].Title != "good" {
		t.Errorf("Findings dropped: %+v", out.Findings)
	}
}

// TestVerifyAndCorrectCategory_TypoSkipsLLM: a one-edit typo
// ("correctnes") is fixed locally; the LLM corrector never runs.
func TestVerifyAndCorrectCategory_TypoSkipsLLM(t *testing.T) {
	client := &scriptedClient{responses: []string{}}
	raw := makeRawWithBadCategory("correctnes")

	out := verifyAndCorrectCategory(context.Background(), dummyCC(client), raw)

	if client.callCount() != 0 {
		t.Fatalf("typo should be fixed without an LLM call; got %d calls", client.callCount())
	}
	if out.Findings[0].Category != "correctness" {
		t.Fatalf("typo not fixed: %q", out.Findings[0].Category)
	}
}

// TestNearestSlug_Boundaries covers the distance/length thresholds
// directly so they don't drift silently.
func TestNearestSlug_Boundaries(t *testing.T) {
	allowed := []string{"correctness", "performance", "design", "testing"}

	if s, ok := nearestSlug("correctnes", allowed); !ok || s != "correctness" {
		t.Errorf("distance-1 typo should match: got %q ok=%v", s, ok)
	}
	if s, ok := nearestSlug("performanc", allowed); !ok || s != "performance" {
		t.Errorf("distance-1 truncation should match: got %q ok=%v", s, ok)
	}
	if _, ok := nearestSlug("shitposting", allowed); ok {
		t.Errorf("far-off slug should not match")
	}
	// Short slugs only accept distance 1, not 2 — too risky for false matches.
	if _, ok := nearestSlug("dsgn", allowed); ok {
		t.Errorf("short slug at distance 2 should not match")
	}
}

// TestBuildCategoryCorrectorMessage_IncludesAllListedFindings: on
// attempt 1 the message must reference every bad-category index and
// include the allowed-category list. On attempt ≥2 the list is
// omitted (already in prior turn) but the indexes still appear.
func TestBuildCategoryCorrectorMessage_IncludesAllListedFindings(t *testing.T) {
	findings := []rawDeepFinding{
		{DeepFinding: state.DeepFinding{Title: "first"}, Category: "vibes"},
		{DeepFinding: state.DeepFinding{Title: "second"}, Category: "energy"},
	}

	first := buildCategoryCorrectorMessage(findings, []int{0, 1}, 1)
	if !strings.Contains(first, "index 0") || !strings.Contains(first, "index 1") {
		t.Fatalf("attempt 1 should reference both bad indexes; got:\n%s", first)
	}
	if !strings.Contains(first, "first") || !strings.Contains(first, "second") {
		t.Fatalf("attempt 1 should mention both finding titles; got:\n%s", first)
	}
	if !strings.Contains(first, "correctness") {
		t.Fatalf("attempt 1 should list known categories; got:\n%s", first)
	}

	second := buildCategoryCorrectorMessage(findings, []int{0, 1}, 2)
	if !strings.Contains(second, "index 0") || !strings.Contains(second, "index 1") {
		t.Fatalf("attempt 2 should still reference bad indexes; got:\n%s", second)
	}
	if strings.Contains(second, "Allowed categories:") {
		t.Fatalf("attempt 2 should not re-list allowed categories; got:\n%s", second)
	}
	if len(second) >= len(first) {
		t.Fatalf("attempt 2 should be shorter than attempt 1 (saved tokens); got %d vs %d bytes", len(second), len(first))
	}
}

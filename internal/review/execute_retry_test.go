package review

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// stubClient is a tiny ai.Client driven by indexed response/error
// queues. Same pattern as classify/security tests — kept local so the
// review package has no extra dependencies.
type stubClient struct {
	responses []string
	errors    []error
	calls     int32
}

func (s *stubClient) ChatStream(_ context.Context, _ string, _ []ai.Message, _ func(string)) (string, error) {
	i := int(atomic.AddInt32(&s.calls, 1)) - 1
	var resp string
	var err error
	if i < len(s.responses) {
		resp = s.responses[i]
	}
	if i < len(s.errors) {
		err = s.errors[i]
	}
	return resp, err
}

func (s *stubClient) CallCount() int { return int(atomic.LoadInt32(&s.calls)) }

var _ ai.Client = (*stubClient)(nil)

// validFindingResponse is a complete individual-call response shape.
// Keep tiny so tests are easy to read; the structure must match
// ParseDeepReviewResult's individual-shape unmarshal target.
const validFindingResponse = `{
  "aoi_id": "x-go-1",
  "status": "finding",
  "file": "x.go",
  "lines": "10-12",
  "severity": "high",
  "category": "correctness",
  "subcategory": "off-by-one",
  "dimension": "correctness",
  "title": "Boundary error",
  "description": "loop runs one extra iteration",
  "evidence": "for i <= len(arr)",
  "trigger": "always when called",
  "suggestion": "use < instead of <="
}`

// buildAOI / buildIndivCall produce a minimal but well-formed
// ReviewCall for use with RunReviewCalls. The shape matches what
// router.go would emit in production.
func buildAOI() security.AreaOfInterest {
	return security.AreaOfInterest{
		ID:          "x-go-1",
		File:        "x.go",
		Line:        10,
		Category:    "correctness",
		Subcategory: "off-by-one",
		Urgency:     "individual",
		Concern:     "loop boundary",
		Context:     "iterates past end",
	}
}

func buildIndivCall() ReviewCall {
	return ReviewCall{
		Type:        "individual",
		Category:    "correctness",
		Subcategory: "off-by-one",
		AOIs:        []security.AreaOfInterest{buildAOI()},
		Files:       []string{"x.go"},
	}
}

// ── runReviewCallWithRetry ─────────────────────────────────────────────

func TestRunReviewCallWithRetry_RetriesTransient(t *testing.T) {
	// Transient error on the first attempt → retry must catch it.
	// Without retry, this call's findings disappear and the user has
	// no signal that anything went wrong beyond a generic
	// "Review N/M failed".
	client := &stubClient{
		errors:    []error{errors.New("503 service unavailable")},
		responses: []string{"", validFindingResponse},
	}
	opts := ExecuteOptions{Mode: ModeAudit}

	result, err := runReviewCallWithRetry(context.Background(), client, buildIndivCall(), opts, 0)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("expected 2 LLM calls (1 fail + 1 retry); got %d", client.CallCount())
	}
	if result == nil || len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding after retry; got %+v", result)
	}
	if result.Findings[0].Title != "Boundary error" {
		t.Errorf("finding title not parsed correctly: %q", result.Findings[0].Title)
	}
}

func TestRunReviewCallWithRetry_DoesNotRetryParseErrors(t *testing.T) {
	// Parse failure (model emits prose instead of JSON). Retrying
	// the same prompt won't fix the model's behavior — just doubles
	// the (very expensive) deep-review token spend.
	client := &stubClient{
		responses: []string{"I cannot provide that review."},
	}
	opts := ExecuteOptions{Mode: ModeAudit}

	_, err := runReviewCallWithRetry(context.Background(), client, buildIndivCall(), opts, 0)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !errors.Is(err, errReviewParse) {
		t.Errorf("expected errReviewParse sentinel; got: %v", err)
	}
	if client.CallCount() != 1 {
		t.Errorf("parse errors must NOT retry; got %d calls", client.CallCount())
	}
}

func TestRunReviewCallWithRetry_DoesNotRetryCanceled(t *testing.T) {
	// Cancelled context: caller gave up. Retry would burn a call
	// against a dead context and probably fail anyway.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &stubClient{
		errors: []error{context.Canceled},
	}
	opts := ExecuteOptions{Mode: ModeAudit}

	_, err := runReviewCallWithRetry(ctx, client, buildIndivCall(), opts, 0)
	if err == nil {
		t.Fatal("expected cancellation to propagate")
	}
	if client.CallCount() != 1 {
		t.Errorf("cancelled context must not retry; got %d calls", client.CallCount())
	}
}

func TestRunReviewCallWithRetry_BothAttemptsFailTransiently(t *testing.T) {
	// Two transient errors → surface the SECOND error (the retry).
	// The caller treats this as a failed call and increments Failed.
	client := &stubClient{
		errors: []error{
			errors.New("502 bad gateway"),
			errors.New("503 service unavailable"),
		},
	}
	opts := ExecuteOptions{Mode: ModeAudit}

	_, err := runReviewCallWithRetry(context.Background(), client, buildIndivCall(), opts, 0)
	if err == nil {
		t.Fatal("expected error after both attempts failed")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("retry error should surface; got: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("expected exactly 2 calls; got %d", client.CallCount())
	}
}

// ── Sentinel wrapping ──────────────────────────────────────────────────

func TestParseDeepReviewResult_WrapsParseErrorsWithSentinel(t *testing.T) {
	// Pin that errors.Is(err, errReviewParse) works for both shapes
	// of parse failure. Both retry AND the cache-poisoning fix
	// depend on this sentinel — a typo'd error wrap would silently
	// re-introduce the original bug.
	cases := []struct {
		name string
		raw  string
	}{
		{"no JSON delimiter", "Sorry, I cannot review this code."},
		{"malformed JSON", "{not valid json at all}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDeepReviewResult(buildIndivCall(), tc.raw)
			if err == nil {
				t.Fatal("expected parse error")
			}
			if !errors.Is(err, errReviewParse) {
				t.Errorf("expected errReviewParse sentinel; got: %v", err)
			}
		})
	}
}

func TestParseDeepReviewResult_NoErrorOnValidResponse(t *testing.T) {
	// Sanity: a well-formed response returns nil error.
	result, err := ParseDeepReviewResult(buildIndivCall(), validFindingResponse)
	if err != nil {
		t.Fatalf("valid response should not error; got: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Errorf("expected 1 finding; got %d", len(result.Findings))
	}
}

// ── Cache poisoning fix ────────────────────────────────────────────────
//
// THE BUG: parse failures previously produced an empty result that
// was written to the cache. Future runs of the same AOI hit the
// cache → "we already reviewed this, no findings" forever. The fix
// is to skip the cache write on parse error; this test pins it.

func TestRunReviewCalls_ParseFailureDoesNotPoisonCache(t *testing.T) {
	// First run: LLM returns garbage that fails to parse.
	// The cache MUST NOT receive an empty result for this AOI.
	client := &stubClient{
		responses: []string{"I cannot help with that."},
	}

	var (
		cacheGets, cacheSets int
		storedResult         *state.DeepReviewResult
	)
	opts := ExecuteOptions{
		Mode:           ModeAudit,
		MaxConcurrency: 1,
		CacheGet: func(_ string) *state.DeepReviewResult {
			cacheGets++
			return nil
		},
		CacheSet: func(_ string, r *state.DeepReviewResult) {
			cacheSets++
			storedResult = r
		},
	}

	exec, err := RunReviewCalls(context.Background(), client, []ReviewCall{buildIndivCall()}, opts)
	// With 1/1 failure, RunReviewCalls returns an "all failed" error
	// (Commit B aggregate-fail). The cache-poisoning fix is the
	// load-bearing assertion here: exec is still returned alongside
	// the error so we can verify cacheSets == 0.
	if err == nil {
		t.Fatal("expected aggregate-fail error for 1/1 failure")
	}
	if exec.Failed != 1 {
		t.Errorf("expected Failed=1 (the parse failure); got %d", exec.Failed)
	}
	if cacheSets != 0 {
		t.Errorf("cache must NOT be written on parse failure (was poisoning the cache); got %d sets, stored=%+v",
			cacheSets, storedResult)
	}
	// Cache lookup is fine — we just don't write back the empty.
	if cacheGets != 1 {
		t.Errorf("expected 1 cache lookup, got %d", cacheGets)
	}
}

func TestRunReviewCalls_SuccessfulParseStillCaches(t *testing.T) {
	// Sanity: the cache-poisoning fix doesn't accidentally disable
	// caching for SUCCESSFUL responses.
	client := &stubClient{
		responses: []string{validFindingResponse},
	}

	var cacheSets int
	var storedResult *state.DeepReviewResult
	opts := ExecuteOptions{
		Mode:           ModeAudit,
		MaxConcurrency: 1,
		CacheGet:       func(_ string) *state.DeepReviewResult { return nil },
		CacheSet: func(_ string, r *state.DeepReviewResult) {
			cacheSets++
			storedResult = r
		},
	}

	_, err := RunReviewCalls(context.Background(), client, []ReviewCall{buildIndivCall()}, opts)
	if err != nil {
		t.Fatalf("RunReviewCalls: %v", err)
	}

	if cacheSets != 1 {
		t.Errorf("expected 1 cache write on success; got %d", cacheSets)
	}
	if storedResult == nil || len(storedResult.Findings) != 1 {
		t.Errorf("cached result should have the parsed finding; got %+v", storedResult)
	}
}

func TestRunReviewCalls_TransientErrorDoesNotPoisonCache(t *testing.T) {
	// A pure transport error (both attempts) should also NOT cache.
	// The fix protects against transient failures the same way it
	// protects against parse failures.
	transient := errors.New("503 service unavailable")
	client := &stubClient{
		errors: []error{transient, transient}, // attempt + retry both fail
	}

	var cacheSets int
	opts := ExecuteOptions{
		Mode:           ModeAudit,
		MaxConcurrency: 1,
		CacheGet:       func(_ string) *state.DeepReviewResult { return nil },
		CacheSet:       func(_ string, _ *state.DeepReviewResult) { cacheSets++ },
	}

	exec, err := RunReviewCalls(context.Background(), client, []ReviewCall{buildIndivCall()}, opts)
	// 1/1 failure → aggregate-fail error returned. Assert cache
	// behavior via exec, which is returned alongside the error.
	if err == nil {
		t.Fatal("expected aggregate-fail error for 1/1 failure")
	}
	if exec.Failed != 1 {
		t.Errorf("expected Failed=1; got %d", exec.Failed)
	}
	if cacheSets != 0 {
		t.Errorf("transient errors must not cache; got %d sets", cacheSets)
	}
}

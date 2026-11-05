package review

import (
	"context"
	"errors"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// truncatingClient behaves like stubClient but reports every call as
// stopped on the provider's turn limit, the way ai.Agent does when
// claude-code returns an error_max_turns result.
type truncatingClient struct{ stubClient }

func (c *truncatingClient) ChatStream(ctx context.Context, sys string, msgs []ai.Message, onToken func(string)) (string, error) {
	ai.MarkTruncated(ctx)
	return c.stubClient.ChatStream(ctx, sys, msgs, onToken)
}

// A turn-limited call whose partial output does not parse must fail
// once, without a retry: the retry would run into the same cap and
// double the cost for the same result.
func TestRunReviewCallWithRetry_TruncatedUnparseableIsNotRetried(t *testing.T) {
	client := &truncatingClient{stubClient{
		responses: []string{"Let me look at the caller next…", validFindingResponse},
	}}

	_, err := runReviewCallWithRetry(context.Background(), client, buildIndivCall(), ExecuteOptions{Mode: ModeAudit}, 0)
	if !errors.Is(err, errReviewTruncated) {
		t.Fatalf("err = %v, want errReviewTruncated", err)
	}
	if !errors.Is(err, errReviewParse) {
		t.Errorf("err = %v, should still wrap errReviewParse", err)
	}
	if n := client.CallCount(); n != 1 {
		t.Errorf("expected 1 call (no retry), got %d", n)
	}
}

// A turn-limited call whose output still parses keeps its findings for
// this run but is not cached, so raising max_turns takes effect on the
// next run without --no-cache.
func TestRunReviewCalls_TruncatedParsedKeepsFindingsButSkipsCache(t *testing.T) {
	client := &truncatingClient{stubClient{responses: []string{validFindingResponse}}}

	var cacheSets int
	opts := ExecuteOptions{
		Mode:           ModeAudit,
		MaxConcurrency: 1,
		CacheGet:       func(_ string) *state.DeepReviewResult { return nil },
		CacheSet:       func(_ string, _ *state.DeepReviewResult) { cacheSets++ },
	}

	exec, err := RunReviewCalls(context.Background(), client, []ReviewCall{buildIndivCall()}, opts)
	if err != nil {
		t.Fatalf("RunReviewCalls: %v", err)
	}
	if len(exec.Findings) != 1 {
		t.Errorf("partial findings should be kept; got %d", len(exec.Findings))
	}
	if cacheSets != 0 {
		t.Errorf("partial result must not be cached; got %d sets", cacheSets)
	}
}

// Untruncated parse failures keep the existing retry-once behaviour.
func TestRunReviewCallWithRetry_UntruncatedParseFailureStillRetries(t *testing.T) {
	client := &stubClient{responses: []string{"prose, no JSON", validFindingResponse}}

	if _, err := runReviewCallWithRetry(context.Background(), client, buildIndivCall(), ExecuteOptions{Mode: ModeAudit}, 0); err != nil {
		t.Fatalf("retry should have recovered: %v", err)
	}
	if n := client.CallCount(); n != 2 {
		t.Errorf("expected 2 calls (attempt + retry), got %d", n)
	}
}

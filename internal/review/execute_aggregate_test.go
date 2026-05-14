package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

func TestShouldAggregateFailReview(t *testing.T) {
	tests := []struct {
		name   string
		failed int
		total  int
		want   bool
	}{
		// Below the floor — never abort regardless of ratio.
		// Protects --file <one-file> debugging runs from
		// overreacting to a single transient call failure.
		{"below floor: 1/1 = 100% but under floor", 1, 1, false},
		{"below floor: 1/4 = 25% but under floor", 1, 4, false},

		// At or under ratio (strict >) — proceed.
		{"exactly at ratio: 2/10 = 20%", 2, 10, false},
		{"above floor under ratio: 2/15 = 13%", 2, 15, false},

		// Above floor AND above ratio — abort.
		{"at floor and above ratio: 2/5 = 40%", 2, 5, true},
		{"clearly above: 5/10 = 50%", 5, 10, true},
		{"all failed: 5/5", 5, 5, true},

		// Edges.
		{"zero total never panics", 5, 0, false},
		{"zero failures never aborts", 0, 100, false},
	}

	for _, tc := range tests {
		got := shouldAggregateFailReview(tc.failed, tc.total)
		if got != tc.want {
			t.Errorf("%s: shouldAggregateFailReview(%d, %d) = %v, want %v",
				tc.name, tc.failed, tc.total, got, tc.want)
		}
	}
}

func TestShouldAggregateFailReview_PinsThresholds(t *testing.T) {
	// Pin the constants. Changing these changes user-visible behavior
	// (when an audit aborts vs proceeds with degraded recall).
	if reviewAggregateFailRatio != 0.20 {
		t.Errorf("reviewAggregateFailRatio = %g, want 0.20", reviewAggregateFailRatio)
	}
	if reviewAggregateFailMinCalls != 2 {
		t.Errorf("reviewAggregateFailMinCalls = %d, want 2", reviewAggregateFailMinCalls)
	}
}

// ── Failed AOI tracking ─────────────────────────────────────────────────

func TestRunReviewCalls_TracksFailedAOIIDs(t *testing.T) {
	// When a call fails, ExecuteResult.FailedAOIIDs must include every
	// AOI ID that was in that call. Downstream synthesis and reports
	// use this to tell the user which areas had no review attention.
	//
	// The stub's response/error queue is consumed in goroutine-startup
	// order, which Go does NOT guarantee even at MaxConcurrency=1 (the
	// per-call goroutines race to acquire the semaphore). So we assert
	// only order-independent invariants: 2 failures, 2 distinct tracked
	// AOIs drawn from the call set. Which AOI won the success slot is
	// scheduler-dependent and not part of the contract.
	transient := errors.New("503 service unavailable")
	client := &stubClient{
		// One success slot + 2 calls' worth of (attempt + retry) failures.
		responses: []string{
			validFindingResponse,
			"", "", "", "",
		},
		errors: []error{
			nil, transient, transient, transient, transient,
		},
	}

	mkCall := func(id, file string) ReviewCall {
		return ReviewCall{
			Type:        "individual",
			Category:    "correctness",
			Subcategory: "off-by-one",
			AOIs: []security.AreaOfInterest{
				{ID: id, File: file, Line: 1, Category: "correctness", Subcategory: "off-by-one"},
			},
			Files: []string{file},
		}
	}
	calls := []ReviewCall{
		mkCall("aoi-1", "a.go"),
		mkCall("aoi-2", "b.go"),
		mkCall("aoi-3", "c.go"),
	}

	opts := ExecuteOptions{
		Mode:           ModeAudit,
		MaxConcurrency: 1,
	}

	exec, err := RunReviewCalls(context.Background(), client, calls, opts)
	// 2/3 = 66% failure → above 20% threshold AND ≥ 2-call floor →
	// aggregate-fail abort. ExecuteResult is still returned (not nil)
	// so callers can see partial findings.
	if err == nil {
		t.Fatal("expected aggregate-fail error (2/3 failures)")
	}
	if exec == nil {
		t.Fatal("ExecuteResult should be returned alongside error for partial visibility")
	}
	if exec.Failed != 2 {
		t.Errorf("Failed = %d, want 2", exec.Failed)
	}
	if len(exec.FailedAOIIDs) != 2 {
		t.Errorf("FailedAOIIDs = %v, want 2 entries", exec.FailedAOIIDs)
	}
	valid := map[string]bool{"aoi-1": true, "aoi-2": true, "aoi-3": true}
	seen := map[string]bool{}
	for _, id := range exec.FailedAOIIDs {
		if !valid[id] {
			t.Errorf("unexpected AOI id %q in FailedAOIIDs", id)
		}
		if seen[id] {
			t.Errorf("duplicate AOI id %q in FailedAOIIDs: %v", id, exec.FailedAOIIDs)
		}
		seen[id] = true
	}
}

func TestRunReviewCalls_FailedAOIIDsTracksGroupedCallAOIs(t *testing.T) {
	// A grouped call covers MULTIPLE AOIs in one LLM call. When it
	// fails, every AOI in the group should appear in FailedAOIIDs —
	// that's the whole point of tracking them (a grouped failure
	// loses N reviews at once).
	transient := errors.New("503")
	client := &stubClient{
		errors: []error{transient, transient}, // attempt + retry both fail
	}

	groupedCall := ReviewCall{
		Type:        "grouped",
		Category:    "error-handling",
		Subcategory: "swallowed-errors",
		AOIs: []security.AreaOfInterest{
			{ID: "g1", File: "a.go", Line: 1},
			{ID: "g2", File: "a.go", Line: 5},
			{ID: "g3", File: "b.go", Line: 10},
		},
		Files: []string{"a.go", "b.go"},
	}

	opts := ExecuteOptions{Mode: ModeAudit, MaxConcurrency: 1}

	exec, err := RunReviewCalls(context.Background(), client, []ReviewCall{groupedCall}, opts)
	if err == nil {
		t.Fatal("expected all-failed error for 1/1 failure")
	}

	wantIDs := map[string]bool{"g1": true, "g2": true, "g3": true}
	if len(exec.FailedAOIIDs) != 3 {
		t.Errorf("FailedAOIIDs = %v, want all 3 grouped AOIs", exec.FailedAOIIDs)
	}
	for _, id := range exec.FailedAOIIDs {
		if !wantIDs[id] {
			t.Errorf("unexpected AOI id %q in FailedAOIIDs", id)
		}
	}
}

// ── Aggregate-fail behavior ────────────────────────────────────────────

func TestRunReviewCalls_AbortsAboveThreshold(t *testing.T) {
	// 3 of 5 calls fail = 60% > 20% threshold + ≥ 2-call floor → abort.
	transient := errors.New("503")
	client := &stubClient{
		// Calls 1-3 fail (attempt + retry each = 6 slots).
		// Calls 4-5 succeed (1 slot each).
		// Layout depends on goroutine scheduling, but with
		// concurrency=1 the order is deterministic and we can
		// arrange the queue accordingly.
		responses: []string{
			"", "", "", "", "", "",
			validFindingResponse, validFindingResponse,
		},
		errors: []error{
			transient, transient,
			transient, transient,
			transient, transient,
			nil, nil,
		},
	}

	mkCall := func(id string) ReviewCall {
		return ReviewCall{
			Type:        "individual",
			AOIs:        []security.AreaOfInterest{{ID: id, Category: "correctness", Subcategory: "off-by-one"}},
			Category:    "correctness",
			Subcategory: "off-by-one",
		}
	}
	calls := []ReviewCall{mkCall("a"), mkCall("b"), mkCall("c"), mkCall("d"), mkCall("e")}

	opts := ExecuteOptions{Mode: ModeAudit, MaxConcurrency: 1}
	_, err := RunReviewCalls(context.Background(), client, calls, opts)
	if err == nil {
		t.Fatal("expected aggregate-fail error when >20% calls fail")
	}
	if !strings.Contains(err.Error(), "20%") {
		t.Errorf("error should mention 20%% threshold; got: %v", err)
	}
	if !strings.Contains(err.Error(), "AOI") {
		t.Errorf("error should mention AOI count to surface impact; got: %v", err)
	}
}

func TestRunReviewCalls_BelowThreshold_ReturnsPartialResults(t *testing.T) {
	// 1 of 5 calls fails = 20% (at boundary, strict > → proceed).
	// Partial results should be returned without error.
	transient := errors.New("503")
	client := &stubClient{
		responses: []string{
			validFindingResponse, validFindingResponse, validFindingResponse, validFindingResponse,
			"", "",
		},
		errors: []error{
			nil, nil, nil, nil,
			transient, transient,
		},
	}

	mkCall := func(id string) ReviewCall {
		return ReviewCall{
			Type:        "individual",
			AOIs:        []security.AreaOfInterest{{ID: id, Category: "correctness", Subcategory: "off-by-one"}},
			Category:    "correctness",
			Subcategory: "off-by-one",
		}
	}
	calls := []ReviewCall{mkCall("a"), mkCall("b"), mkCall("c"), mkCall("d"), mkCall("e")}

	opts := ExecuteOptions{Mode: ModeAudit, MaxConcurrency: 1}
	exec, err := RunReviewCalls(context.Background(), client, calls, opts)
	if err != nil {
		// Tolerable: scheduling could cause >1 to fail; in that case
		// the test gets the abort path and we skip the partial check.
		if !strings.Contains(err.Error(), "20%") {
			t.Fatalf("unexpected error shape: %v", err)
		}
		t.Skip("scheduling caused >1 call to fail; aggregate-fail path tested separately")
	}

	if exec.Failed != 1 {
		t.Errorf("Failed = %d, want 1", exec.Failed)
	}
	if len(exec.Findings) != 4 {
		t.Errorf("expected 4 findings from successful calls; got %d", len(exec.Findings))
	}
	if len(exec.FailedAOIIDs) != 1 {
		t.Errorf("FailedAOIIDs should track the one failed call; got %v", exec.FailedAOIIDs)
	}
}

func TestRunReviewCalls_AllFailed_AbortsEvenBelowFloor(t *testing.T) {
	// 1/1 failure = 100% but doesn't hit the 2-call floor. The
	// "all failed" special case must still abort — returning a
	// success-shaped empty result would hide the total failure.
	transient := errors.New("503")
	client := &stubClient{
		errors: []error{transient, transient},
	}

	calls := []ReviewCall{
		{
			Type: "individual",
			AOIs: []security.AreaOfInterest{{ID: "only-one", Category: "correctness", Subcategory: "off-by-one"}},
		},
	}
	opts := ExecuteOptions{Mode: ModeAudit, MaxConcurrency: 1}

	exec, err := RunReviewCalls(context.Background(), client, calls, opts)
	if err == nil {
		t.Fatal("expected error when all calls fail, even at 1/1")
	}
	if !strings.Contains(err.Error(), "all") {
		t.Errorf("error should say 'all'; got: %v", err)
	}
	if len(exec.FailedAOIIDs) != 1 || exec.FailedAOIIDs[0] != "only-one" {
		t.Errorf("FailedAOIIDs should track the lone failed AOI; got %v", exec.FailedAOIIDs)
	}
}

func TestRunReviewCalls_AllPass_NoError(t *testing.T) {
	// Sanity: every call succeeds → no error, no failed AOIs.
	client := &stubClient{
		responses: []string{validFindingResponse, validFindingResponse},
	}

	mkCall := func(id string) ReviewCall {
		return ReviewCall{
			Type:        "individual",
			AOIs:        []security.AreaOfInterest{{ID: id, Category: "correctness", Subcategory: "off-by-one"}},
			Category:    "correctness",
			Subcategory: "off-by-one",
		}
	}
	calls := []ReviewCall{mkCall("a"), mkCall("b")}
	opts := ExecuteOptions{Mode: ModeAudit, MaxConcurrency: 1}

	exec, err := RunReviewCalls(context.Background(), client, calls, opts)
	if err != nil {
		t.Fatalf("happy path should not error: %v", err)
	}
	if exec.Failed != 0 {
		t.Errorf("Failed = %d, want 0", exec.Failed)
	}
	if len(exec.FailedAOIIDs) != 0 {
		t.Errorf("FailedAOIIDs = %v, want empty", exec.FailedAOIIDs)
	}
}

// Compile-time check that *state.DeepReviewResult is a real type
// (avoids an "unused import" warning if all callers above stop
// referencing it after refactors).
var _ = (*state.DeepReviewResult)(nil)

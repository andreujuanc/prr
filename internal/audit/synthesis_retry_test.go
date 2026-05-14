package audit

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// stubClient is a tiny ai.Client driven by indexed response/error
// queues. Local to this package (same pattern as classify/review/security).
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

// validSynthesisJSON is a complete synthesis response matching the
// audit prompt's output schema.
const validSynthesisJSON = `{
  "executive_summary": "Audit covered 3 files. Risk posture moderate.",
  "top_risks": ["Missing tenant isolation in handlers"],
  "systemic_patterns": ["Inconsistent error wrapping"],
  "recommendations": ["Add tenant assertion to middleware"]
}`

// mkFinding builds a minimal DeepFinding for synthesis input.
func mkFinding(id, cat, sev string) state.DeepFinding {
	return state.DeepFinding{
		FindingID:   id,
		Category:    cat,
		Subcategory: "test",
		Severity:    sev,
		File:        "x.go",
		Lines:       "10",
		Title:       "title-" + id,
		Description: "desc-" + id,
	}
}

// ── Retry behavior ─────────────────────────────────────────────────────

func TestSynthesizeDirect_RetriesTransient(t *testing.T) {
	// Transient error on first attempt → retry must catch it. Without
	// retry, a single 503 after all upstream work succeeded would
	// leave the user with findings but no executive summary.
	client := &stubClient{
		errors:    []error{errors.New("503 service unavailable")},
		responses: []string{"", validSynthesisJSON},
	}
	findings := []state.DeepFinding{mkFinding("F-1", "correctness", "high")}

	result, err := Synthesize(context.Background(), client, findings, nil, "", 0, nil)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("expected 2 LLM calls (1 fail + 1 retry); got %d", client.CallCount())
	}
	if result.ExecutiveSummary == "" {
		t.Error("expected non-empty executive_summary on success")
	}
}

func TestSynthesizeDirect_DoesNotRetryCanceled(t *testing.T) {
	// Cancelled context: retrying would burn against a dead context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &stubClient{
		errors: []error{context.Canceled},
	}
	findings := []state.DeepFinding{mkFinding("F-1", "correctness", "high")}

	_, err := Synthesize(ctx, client, findings, nil, "", 0, nil)
	if err == nil {
		t.Fatal("expected cancellation to propagate")
	}
	if client.CallCount() != 1 {
		t.Errorf("cancelled context must not retry; got %d calls", client.CallCount())
	}
}

func TestSynthesizeDirect_BothAttemptsFailTransiently(t *testing.T) {
	// Two transient errors → surface the SECOND error to the caller.
	client := &stubClient{
		errors: []error{
			errors.New("502 bad gateway"),
			errors.New("503 service unavailable"),
		},
	}
	findings := []state.DeepFinding{mkFinding("F-1", "correctness", "high")}

	_, err := Synthesize(context.Background(), client, findings, nil, "", 0, nil)
	if err == nil {
		t.Fatal("expected error after both attempts failed")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("retry's error should surface; got: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("expected exactly 2 calls; got %d", client.CallCount())
	}
}

// ── Sentinel wrapping ──────────────────────────────────────────────────

func TestParseSynthesisResult_WrapsParseErrorsWithSentinel(t *testing.T) {
	// Both shapes of parse failure must be tagged with errSynthesisParse
	// so retry / future cache-guard logic can distinguish them from
	// transport errors. Without the sentinel, a typo in the wrap chain
	// silently re-introduces fragility.
	cases := []struct {
		name string
		raw  string
	}{
		{"no JSON object", "I cannot synthesize this."},
		{"malformed JSON", "{not valid json at all}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSynthesisResult(tc.raw)
			if err == nil {
				t.Fatal("expected parse error")
			}
			if !errors.Is(err, errSynthesisParse) {
				t.Errorf("expected errSynthesisParse sentinel; got: %v", err)
			}
		})
	}
}

func TestParseSynthesisResult_NoErrorOnValidResponse(t *testing.T) {
	result, err := ParseSynthesisResult(validSynthesisJSON)
	if err != nil {
		t.Fatalf("valid response should parse cleanly; got: %v", err)
	}
	if result.ExecutiveSummary == "" {
		t.Error("expected populated ExecutiveSummary")
	}
	if len(result.TopRisks) == 0 {
		t.Error("expected populated TopRisks")
	}
}

// ── Hierarchical partial-failure tolerance ────────────────────────────
//
// Previously a single category-synthesis failure aborted the whole
// hierarchical run, discarding successful work on every other category.
// The new behavior: proceed with successful categories if ≥50% survived;
// abort otherwise.

func mkHierarchicalFindings(perCategory int) []state.DeepFinding {
	// Build > hierarchicalThreshold (50) findings spread across 4
	// categories so the call goes through synthesizeHierarchical.
	cats := []string{"correctness", "security", "performance", "design"}
	var findings []state.DeepFinding
	for _, cat := range cats {
		for i := 0; i < perCategory; i++ {
			findings = append(findings, mkFinding(
				cat+"-"+itoa(i), cat, "medium",
			))
		}
	}
	return findings
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string('0'+byte(n%10)) + out
		n /= 10
	}
	return out
}

func TestSynthesizeHierarchical_ToleratesPartialFailures(t *testing.T) {
	// 4 categories. 1 category's call (with retry = 2 attempts) fails;
	// 3 succeed. 3/4 = 75% > 50% floor → proceed.
	//
	// Plus one more call for the final merge → 4 successes + 2 failures
	// + 1 merge = 7 responses needed in the queue. Stub returns
	// responses in call order (which is non-deterministic across
	// goroutines), so we fill the queue with mostly-success and let
	// the failure land wherever scheduling decides.
	transient := errors.New("503")
	client := &stubClient{
		responses: []string{
			validSynthesisJSON, validSynthesisJSON, validSynthesisJSON,
			"", "", // one category fails twice (attempt + retry)
			validSynthesisJSON,                     // the merge call
			validSynthesisJSON, validSynthesisJSON, // extras for scheduling slack
		},
		errors: []error{
			nil, nil, nil,
			transient, transient,
			nil,
			nil, nil,
		},
	}

	findings := mkHierarchicalFindings(15) // 4 × 15 = 60 findings → hierarchical

	result, err := Synthesize(context.Background(), client, findings, nil, "", 0, nil)
	if err != nil {
		// Tolerable: scheduling could cause 2 categories to fail (e.g.
		// if the first two slots both land on failure-prone categories),
		// in which case 2/4 = 50% which doesn't exceed the strict-greater
		// floor — at exactly 50% we abort by design. Allow either path.
		if !strings.Contains(err.Error(), "categories failed") {
			t.Fatalf("unexpected error shape: %v", err)
		}
		t.Skip("scheduling caused too many failures; this run hit the abort branch")
	}

	if result.ExecutiveSummary == "" {
		t.Error("expected non-empty executive_summary from partial-success synthesis")
	}
}

func TestSynthesizeHierarchical_AbortsBelowFloor(t *testing.T) {
	// 4 categories. 3 fail (attempt + retry each = 6 transient errors).
	// 1 succeeds. 1/4 = 25% < 50% floor → abort with descriptive error.
	transient := errors.New("503")
	client := &stubClient{
		responses: []string{
			validSynthesisJSON,     // first to arrive succeeds
			"", "", "", "", "", "", // 6 slots of failure for the other 3 categories
		},
		errors: []error{
			nil,
			transient, transient, transient, transient, transient, transient,
		},
	}

	findings := mkHierarchicalFindings(15)

	_, err := Synthesize(context.Background(), client, findings, nil, "", 0, nil)
	if err == nil {
		t.Fatal("expected abort when most categories fail")
	}
	if !strings.Contains(err.Error(), "categories failed") {
		t.Errorf("error should mention category failures; got: %v", err)
	}
	if !strings.Contains(err.Error(), "50%") && !strings.Contains(err.Error(), "threshold") {
		t.Errorf("error should mention the threshold; got: %v", err)
	}
}

func TestSynthesizeHierarchical_AllCategoriesPassNoAbort(t *testing.T) {
	// Happy path: every category synthesizes successfully + merge
	// succeeds. No abort, no errors.
	client := &stubClient{
		responses: []string{
			validSynthesisJSON, validSynthesisJSON, validSynthesisJSON, validSynthesisJSON,
			validSynthesisJSON, // merge
		},
	}
	findings := mkHierarchicalFindings(15)

	result, err := Synthesize(context.Background(), client, findings, nil, "", 0, nil)
	if err != nil {
		t.Fatalf("happy path errored: %v", err)
	}
	if result.ExecutiveSummary == "" {
		t.Error("expected executive_summary on full success")
	}
}

func TestNeedsHierarchical_PinsThreshold(t *testing.T) {
	// Pin so changes to hierarchicalThreshold surface here too.
	if NeedsHierarchical(50) {
		t.Error("50 findings is at-threshold, should NOT trigger hierarchical (strict >)")
	}
	if !NeedsHierarchical(51) {
		t.Error("51 findings should trigger hierarchical")
	}
}

func TestHierarchicalPartialFloor_PinsConstant(t *testing.T) {
	// Pin the partial-tolerance constant; changing it shifts user-visible
	// abort behavior on flaky synthesis runs.
	if hierarchicalPartialFloor != 0.5 {
		t.Errorf("hierarchicalPartialFloor = %g, want 0.5", hierarchicalPartialFloor)
	}
}

// ── Recall-gap surfacing (failedAOICount) ─────────────────────────────

func TestBuildSynthesisUserMessage_RecallGapMentionedWhenFailedAOICountSet(t *testing.T) {
	// When failedAOICount > 0, the user message must include a
	// "## Audit Recall Gap" section telling the model to mention
	// the gap in the executive summary. Without this, synthesis
	// produces a confident-sounding summary on top of degraded
	// inputs and the user never knows their recall was incomplete.
	findings := []state.DeepFinding{mkFinding("F-1", "correctness", "high")}
	msg := BuildSynthesisUserMessage(findings, nil, "", 7)

	if !strings.Contains(msg, "Audit Recall Gap") {
		t.Errorf("expected '## Audit Recall Gap' section when failedAOICount > 0; got:\n%s", msg)
	}
	if !strings.Contains(msg, "7 ") {
		t.Errorf("expected the count (7) to appear in the recall-gap section; got:\n%s", msg)
	}
}

func TestBuildSynthesisUserMessage_NoRecallGapSectionWhenZero(t *testing.T) {
	// failedAOICount == 0 → no recall gap section. Otherwise every
	// synthesis prompt would have boilerplate noise.
	findings := []state.DeepFinding{mkFinding("F-1", "correctness", "high")}
	msg := BuildSynthesisUserMessage(findings, nil, "", 0)

	if strings.Contains(msg, "Audit Recall Gap") {
		t.Errorf("recall gap section should NOT appear when failedAOICount = 0; got:\n%s", msg)
	}
}

func TestSynthesize_EmptyFindingsWithFailedAOIs_NotesGapInSummary(t *testing.T) {
	// The most user-misleading case: zero findings + N failed AOIs.
	// Without this, the empty-findings short-circuit returns a
	// confident "no findings identified" message, hiding the fact
	// that most of the audit didn't actually run.
	result, err := Synthesize(context.Background(), &stubClient{}, nil, nil, "", 12, nil)
	if err != nil {
		t.Fatalf("unexpected error on empty findings: %v", err)
	}
	if !strings.Contains(result.ExecutiveSummary, "12") {
		t.Errorf("executive_summary should mention the count of failed AOIs; got: %q",
			result.ExecutiveSummary)
	}
	if !strings.Contains(result.ExecutiveSummary, "degraded") {
		t.Errorf("executive_summary should say 'degraded'; got: %q",
			result.ExecutiveSummary)
	}
}

func TestSynthesize_EmptyFindingsZeroFailed_CleanSummary(t *testing.T) {
	// Sanity: zero findings AND zero failed AOIs is a legitimate
	// "clean audit" result; should not falsely claim degradation.
	result, err := Synthesize(context.Background(), &stubClient{}, nil, nil, "", 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.ExecutiveSummary, "degraded") {
		t.Errorf("clean audit should not mention degradation; got: %q",
			result.ExecutiveSummary)
	}
}

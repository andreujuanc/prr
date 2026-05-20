package security

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
)

// stubClient is a minimal ai.Client that drives ChatStream responses
// from queues indexed by call number. Same pattern as classify's
// retry tests — duplicated here rather than shared because the two
// packages have no other reason to depend on each other.
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

// ── scanBatchWithRetry ─────────────────────────────────────────────────

func TestScanBatchWithRetry_RetriesTransient(t *testing.T) {
	// Transient error → retry. Without retry, this batch loses AOIs
	// for every file it covers (8-15 files typically) and those
	// files get NO deep review attention in Phase 3.
	client := &stubClient{
		errors: []error{errors.New("connection reset by peer")},
		responses: []string{
			"", // first call: errored
			`[{"file": "a.go", "areas": []}]`,
		},
	}
	batch := aoiBatch{
		label: "all-cats",
		files: []string{"a.go"},
		diffs: "=== a.go ===\npackage a\n",
	}

	got, err := scanBatchWithRetry(context.Background(), client, batch, nil, true)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("expected 2 LLM calls (1 fail + 1 retry); got %d", client.CallCount())
	}
	if len(got) != 1 || got[0].File != "a.go" {
		t.Errorf("unexpected results: %+v", got)
	}
}

func TestScanBatchWithRetry_DoesNotRetryParseErrors(t *testing.T) {
	// Parse failure (model emitted prose instead of JSON) won't be
	// fixed by re-running the same prompt. Retry would just double
	// the token spend.
	client := &stubClient{
		responses: []string{"I cannot help with this request."},
	}
	batch := aoiBatch{
		label: "all-cats",
		files: []string{"a.go"},
		diffs: "=== a.go ===\npackage a\n",
	}

	_, err := scanBatchWithRetry(context.Background(), client, batch, nil, true)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !errors.Is(err, errAOIParse) {
		t.Errorf("expected errAOIParse sentinel; got: %v", err)
	}
	if client.CallCount() != 1 {
		t.Errorf("parse errors must NOT retry; got %d calls", client.CallCount())
	}
}

func TestScanBatchWithRetry_DoesNotRetryEmptyResponse(t *testing.T) {
	// An empty response is a parse-shape failure (the model returned
	// nothing parseable). Re-running won't make a silent model
	// suddenly start emitting JSON — short-circuit.
	client := &stubClient{
		responses: []string{""},
	}
	batch := aoiBatch{
		label: "all-cats",
		files: []string{"a.go"},
		diffs: "=== a.go ===\npackage a\n",
	}

	_, err := scanBatchWithRetry(context.Background(), client, batch, nil, true)
	if err == nil {
		t.Fatal("expected empty-response error")
	}
	if !errors.Is(err, errAOIParse) {
		t.Errorf("empty response should be tagged errAOIParse; got: %v", err)
	}
	if client.CallCount() != 1 {
		t.Errorf("empty response should not trigger retry; got %d calls", client.CallCount())
	}
}

func TestScanBatchWithRetry_DoesNotRetryCanceled(t *testing.T) {
	// Cancelled context means parent gave up. Retry would (a) burn
	// against a dead context anyway and (b) waste a call.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &stubClient{
		errors: []error{context.Canceled},
	}
	batch := aoiBatch{
		label: "all-cats",
		files: []string{"a.go"},
		diffs: "=== a.go ===\npackage a\n",
	}

	_, err := scanBatchWithRetry(ctx, client, batch, nil, true)
	if err == nil {
		t.Fatal("expected cancellation to surface")
	}
	if client.CallCount() != 1 {
		t.Errorf("canceled context must not retry; got %d calls", client.CallCount())
	}
}

func TestScanBatchWithRetry_BothAttemptsFailTransiently(t *testing.T) {
	// Two transient failures → surface the second error. The caller
	// (ScanAreasOfInterestClassified) collects this in batchErrors
	// and either logs a warning (partial) or returns an error (all).
	client := &stubClient{
		errors: []error{
			errors.New("502 bad gateway"),
			errors.New("503 service unavailable"),
		},
	}
	batch := aoiBatch{
		label: "all-cats",
		files: []string{"a.go"},
		diffs: "=== a.go ===\npackage a\n",
	}

	_, err := scanBatchWithRetry(context.Background(), client, batch, nil, true)
	if err == nil {
		t.Fatal("expected error after both attempts failed")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected SECOND error to surface (the retry), got: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("expected exactly 2 calls; got %d", client.CallCount())
	}
}

// ── Sentinel error wrapping ────────────────────────────────────────────

func TestParseAOIResult_WrapsParseErrorsWithSentinel(t *testing.T) {
	// Pin that errors.Is(err, errAOIParse) works for both shapes of
	// parse failure. scanBatchWithRetry depends on this to know when
	// retry would be wasteful.
	cases := []struct {
		name string
		raw  string
	}{
		{"no array", "Sorry, I cannot scan these files."},
		{"bad JSON", "[{not valid json at all}]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAOIResult(tc.raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, errAOIParse) {
				t.Errorf("expected errAOIParse sentinel; got: %v", err)
			}
		})
	}
}

// ── Silent-drop detection ──────────────────────────────────────────────

func TestScanAreasOfInterestClassified_WarnsOnLLMDroppedFiles(t *testing.T) {
	// LLM returns 1 of 2 input files. The dropped file gets NO AOIs
	// (it's just absent from results). The user must be told so they
	// know Phase 3's recall is reduced for that file.
	client := &stubClient{
		responses: []string{
			`[{"file": "a.go", "areas": [{"id": "a-go-1", "line": 1, "category": "correctness", "subcategory": "off-by-one", "urgency": "grouped", "concern": "x", "context": "y", "dimensions": ["correctness"]}]}]`,
		},
	}

	rawDiffs := map[string]string{
		"a.go": "=== a.go ===\npackage a\n",
		"b.go": "=== b.go ===\npackage b\n", // LLM will silently drop this
	}

	var progress []string
	report, err := ScanAreasOfInterestClassified(
		context.Background(), client, rawDiffs, nil, nil,
		func(s string) { progress = append(progress, s) },
		nil, true,
	)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report == nil {
		t.Fatal("expected report")
	}

	var sawDropWarning bool
	for _, p := range progress {
		if strings.Contains(p, "silently dropped") {
			sawDropWarning = true
			break
		}
	}
	if !sawDropWarning {
		t.Errorf("expected drop warning in progress log; got: %v", progress)
	}
}

func TestScanAreasOfInterestClassified_NoDropWarningWhenAllReturned(t *testing.T) {
	// Happy path: every input file appears in output → no drop warning.
	client := &stubClient{
		responses: []string{
			`[{"file": "a.go", "areas": []}, {"file": "b.go", "areas": []}]`,
		},
	}

	rawDiffs := map[string]string{
		"a.go": "=== a.go ===\npackage a\n",
		"b.go": "=== b.go ===\npackage b\n",
	}

	var progress []string
	_, err := ScanAreasOfInterestClassified(
		context.Background(), client, rawDiffs, nil, nil,
		func(s string) { progress = append(progress, s) },
		nil, true,
	)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	for _, p := range progress {
		if strings.Contains(p, "silently dropped") {
			t.Errorf("no drop should be reported when LLM returned everything: %q", p)
		}
	}
}

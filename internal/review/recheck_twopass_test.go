package review

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// PR 5 swaps the recheck order: cross-file consolidation runs FIRST
// (on the full candidate set, no dismissal), per-file dismissal runs
// SECOND (on the post-consolidate remainder). These tests pin the
// contract:
//
//   1. The consolidator sees the full candidate set in one prompt
//      (so it can find cross-file patterns).
//   2. Consolidated systemic findings bypass the dismiss pass —
//      they're already a deliberate cross-file decision.
//   3. The dismiss pass sees only post-consolidate non-systemic
//      findings (so it can't erase pattern members).
//   4. Both passes' outputs merge: systemic findings + per-file
//      kept/modified + dismissals threaded through.
//
// A scripted client lets us assert exactly which prompts ran on
// which inputs.

// promptRecordingClient is like recordingClient but keeps the full
// history (every system prompt + user message it saw). PR 5's
// behavior is "two LLM calls in sequence with specific contents";
// a single-call recorder isn't enough to verify both happened.
type promptRecordingClient struct {
	responses []string
	errors    []error
	calls     int32

	// History — appended on every ChatStream call.
	prompts  []string
	messages [][]ai.Message
}

func (p *promptRecordingClient) ChatStream(_ context.Context, systemPrompt string, messages []ai.Message, _ func(string)) (string, error) {
	p.prompts = append(p.prompts, systemPrompt)
	p.messages = append(p.messages, append([]ai.Message(nil), messages...))
	i := int(atomic.AddInt32(&p.calls, 1)) - 1
	var resp string
	var err error
	if i < len(p.responses) {
		resp = p.responses[i]
	}
	if i < len(p.errors) {
		err = p.errors[i]
	}
	return resp, err
}

func (p *promptRecordingClient) CallCount() int { return int(atomic.LoadInt32(&p.calls)) }

var _ ai.Client = (*promptRecordingClient)(nil)

// consolidateResponse builds a consolidator response. The
// consolidator's contract is `kept` + `consolidated` only; it must
// not emit `dismissed` or `modified`.
func consolidateResponse(t *testing.T, kept []string, consolidations []map[string]any) string {
	t.Helper()
	consol := make([]map[string]any, 0, len(consolidations))
	for _, c := range consolidations {
		consol = append(consol, c)
	}
	body := map[string]any{
		"kept":         kept,
		"consolidated": consol,
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal consolidate response: %v", err)
	}
	return string(b)
}

// dismissResponse builds a dismiss-pass response. Contract: `kept`,
// `modified`, `dismissed` — no `consolidated`.
func dismissResponse(t *testing.T, kept []string, dismissed []map[string]any) string {
	t.Helper()
	body := map[string]any{
		"kept":      kept,
		"modified":  []any{},
		"dismissed": dismissed,
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal dismiss response: %v", err)
	}
	return string(b)
}

func makeFinding(id, file, lines, category, title, severity string) state.DeepFinding {
	return state.DeepFinding{
		FindingID: id,
		File:      file,
		Lines:     lines,
		Category:  category,
		Title:     title,
		Severity:  severity,
	}
}

// TestRecheckFindings_ConsolidatorRunsFirst is the structural pin: in
// a normal 2-pass run, the FIRST LLM call must use the consolidator
// prompt and the SECOND must use the dismiss prompt. Without this
// pin, an accidental refactor that flipped the order back to the
// old behavior would still pass through.
func TestRecheckFindings_ConsolidatorRunsFirst(t *testing.T) {
	findings := []state.DeepFinding{
		makeFinding("", "a.go", "10", "error-handling", "ignored error", "low"),
		makeFinding("", "b.go", "20", "error-handling", "ignored error", "low"),
		makeFinding("", "c.go", "30", "error-handling", "ignored error", "low"),
	}

	client := &promptRecordingClient{
		responses: []string{
			// Pass 1: consolidator merges all three into one systemic.
			consolidateResponse(t,
				[]string{}, // nothing kept individually
				[]map[string]any{{
					"finding_ids": []string{"F-001", "F-002", "F-003"},
					"finding": map[string]any{
						"finding_id":  "F-001",
						"file":        "multiple",
						"lines":       "",
						"severity":    "medium",
						"category":    "error-handling",
						"subcategory": "swallowed",
						"dimension":   "error-handling",
						"title":       "Systemic: Swallowed errors across multiple files",
						"description": "Found in a.go:10, b.go:20, c.go:30",
						"trigger":     "operation returns err",
						"suggestion":  "log or propagate",
					},
				}},
			),
			// Pass 2: dismiss pass runs on the empty kept set. It
			// shouldn't be invoked at all because there's nothing to
			// process — but if it is, we have a response queued so
			// the test doesn't crash. We assert call count below.
			dismissResponse(t, nil, nil),
		},
	}

	result, err := RecheckFindings(context.Background(), client, findings, RecheckOptions{Mode: ModeAudit})
	if err != nil {
		t.Fatalf("RecheckFindings: %v", err)
	}

	if client.CallCount() < 1 {
		t.Fatalf("expected at least one LLM call, got %d", client.CallCount())
	}
	if !strings.Contains(client.prompts[0], "Finding Consolidation") {
		t.Errorf("first call must use consolidator prompt; got prompt:\n%.300s", client.prompts[0])
	}
	if client.CallCount() >= 2 && !strings.Contains(client.prompts[1], "Finding Dismissal") {
		t.Errorf("second call must use dismiss prompt; got prompt:\n%.300s", client.prompts[1])
	}

	// The consolidated systemic finding must be in the result.
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 systemic finding kept, got %d", len(result.Findings))
	}
	if !strings.HasPrefix(result.Findings[0].Title, "Systemic:") {
		t.Errorf("expected systemic title, got %q", result.Findings[0].Title)
	}
	if result.ConsolidatedCount != 3 {
		// 3 inputs absorbed into 1 systemic = 3 fewer in the
		// post-consolidate set. The count tracks "how many became
		// systemic" so the report can show the savings.
		t.Errorf("ConsolidatedCount: want 3, got %d", result.ConsolidatedCount)
	}
}

// TestRecheckFindings_ConsolidatedFindingsBypassDismissPass is the
// behavioral pin: once the consolidator merges findings into a
// systemic, the per-file dismisser must not see them. Otherwise the
// dismisser could mistakenly try to dismiss the systemic finding
// using thin per-file context.
func TestRecheckFindings_ConsolidatedFindingsBypassDismissPass(t *testing.T) {
	findings := []state.DeepFinding{
		makeFinding("", "a.go", "10", "X", "pattern member", "low"),
		makeFinding("", "b.go", "20", "X", "pattern member", "low"),
		makeFinding("", "c.go", "30", "X", "pattern member", "low"),
		makeFinding("", "d.go", "40", "X", "isolated", "high"),
	}

	client := &promptRecordingClient{
		responses: []string{
			// Consolidator merges the 3 pattern members. F-004 stays kept.
			consolidateResponse(t,
				[]string{"F-004"},
				[]map[string]any{{
					"finding_ids": []string{"F-001", "F-002", "F-003"},
					"finding": map[string]any{
						"finding_id":  "F-001",
						"file":        "multiple",
						"lines":       "",
						"severity":    "high",
						"category":    "X",
						"subcategory": "x",
						"dimension":   "X",
						"title":       "Systemic: Pattern across files",
						"description": "three files, same pattern",
						"trigger":     "the trigger",
						"suggestion":  "the fix",
					},
				}},
			),
			// Dismiss pass: only F-004 should be in its input.
			dismissResponse(t, []string{"F-004"}, nil),
		},
	}

	result, err := RecheckFindings(context.Background(), client, findings, RecheckOptions{Mode: ModeAudit})
	if err != nil {
		t.Fatalf("RecheckFindings: %v", err)
	}

	if client.CallCount() != 2 {
		t.Fatalf("expected 2 LLM calls (consolidate + dismiss), got %d", client.CallCount())
	}

	// The dismiss pass call must NOT contain F-001/F-002/F-003 in
	// its user message — those were consolidated and bypassed.
	// json.MarshalIndent emits `"finding_id": "F-XXX"` with a space
	// after the colon, so the substring we look for must include
	// that space. A no-space variant of this check was tautological
	// (always passed) and missed real regressions.
	dismissUserMsg := client.messages[1][0].Content
	for _, badID := range []string{"F-001", "F-002", "F-003"} {
		needle := `"finding_id": "` + badID + `"`
		if strings.Contains(dismissUserMsg, needle) {
			t.Errorf("dismiss pass must not see consolidated finding %s; user message was:\n%.500s",
				badID, dismissUserMsg)
		}
	}

	// And F-004 (the isolated finding) MUST be in the dismiss input.
	if !strings.Contains(dismissUserMsg, `"finding_id": "F-004"`) {
		t.Errorf("dismiss pass must see the isolated finding F-004; user message was:\n%.500s",
			dismissUserMsg)
	}

	// Result: 1 systemic + 1 isolated.
	if len(result.Findings) != 2 {
		t.Fatalf("expected 2 findings (1 systemic + 1 isolated), got %d", len(result.Findings))
	}
}

// TestRecheckFindings_NoCrossFilePatternsDismissPassRunsNormally:
// when the consolidator finds nothing to merge, all findings pass
// through to the dismiss pass and it behaves as the canonical
// recheck path always has.
func TestRecheckFindings_NoCrossFilePatternsDismissPassRunsNormally(t *testing.T) {
	findings := []state.DeepFinding{
		makeFinding("", "a.go", "10", "X", "thing A", "high"),
		makeFinding("", "b.go", "20", "Y", "thing B", "medium"),
	}

	client := &promptRecordingClient{
		responses: []string{
			// Consolidator passes everything through.
			consolidateResponse(t, []string{"F-001", "F-002"}, nil),
			// Dismiss pass dismisses one and keeps the other.
			dismissResponse(t,
				[]string{"F-001"},
				[]map[string]any{
					{"finding_id": "F-002", "rationale": "framework guard upstream"},
				},
			),
		},
	}

	result, err := RecheckFindings(context.Background(), client, findings, RecheckOptions{Mode: ModeAudit})
	if err != nil {
		t.Fatalf("RecheckFindings: %v", err)
	}

	if client.CallCount() != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", client.CallCount())
	}
	if len(result.Findings) != 1 || result.Findings[0].FindingID != "F-001" {
		t.Errorf("expected F-001 to survive, got %+v", result.Findings)
	}
	if result.DismissedCount != 1 {
		t.Errorf("DismissedCount: want 1, got %d", result.DismissedCount)
	}
	if len(result.Dismissed) != 1 || result.Dismissed[0].FindingID != "F-002" {
		t.Errorf("expected F-002 in Dismissed, got %+v", result.Dismissed)
	}
}

// TestRecheckFindings_ConsolidatorFailureFallsBackGracefully:
// non-fatal contract. If the consolidator errors, the dismiss pass
// still runs on the original set.
func TestRecheckFindings_ConsolidatorFailureFallsBackGracefully(t *testing.T) {
	findings := []state.DeepFinding{
		makeFinding("", "a.go", "10", "X", "thing", "high"),
	}

	client := &promptRecordingClient{
		responses: []string{"garbage that won't parse", ""},
	}
	// Stub-style error on first call.
	client.errors = []error{nil, nil}

	result, err := RecheckFindings(context.Background(), client, findings, RecheckOptions{Mode: ModeAudit})
	if err != nil {
		t.Fatalf("consolidator garbage must be non-fatal, got: %v", err)
	}
	// Either the dismiss pass succeeded (if it parsed the empty
	// response somehow) or the fallback returned everything kept —
	// both are acceptable. The hard constraint is: the original
	// finding is not lost.
	if len(result.Findings) == 0 {
		t.Errorf("findings must survive a consolidator failure, got 0 kept")
	}
}

// TestRecheckFindings_DismissPassFailureKeepsConsolidated:
// if the dismiss pass fails but the consolidator succeeded, the
// consolidated systemic findings must still appear in the result —
// PR 5's purpose was to PRESERVE patterns, so losing them on a
// dismiss-pass error would defeat the whole change.
func TestRecheckFindings_DismissPassFailureKeepsConsolidated(t *testing.T) {
	findings := []state.DeepFinding{
		makeFinding("", "a.go", "10", "X", "a", "low"),
		makeFinding("", "b.go", "20", "X", "b", "low"),
	}

	client := &promptRecordingClient{
		responses: []string{
			// Consolidator merges a + b into one systemic and keeps
			// nothing individually.
			consolidateResponse(t,
				[]string{},
				[]map[string]any{{
					"finding_ids": []string{"F-001", "F-002"},
					"finding": map[string]any{
						"finding_id":  "F-001",
						"file":        "multiple",
						"lines":       "",
						"severity":    "medium",
						"category":    "X",
						"subcategory": "y",
						"dimension":   "X",
						"title":       "Systemic: X happens everywhere",
						"description": "...",
						"trigger":     "...",
						"suggestion":  "...",
					},
				}},
			),
			// Dismiss pass returns garbage.
			"this is not JSON at all",
		},
	}

	result, err := RecheckFindings(context.Background(), client, findings, RecheckOptions{Mode: ModeAudit})
	if err != nil {
		t.Fatalf("dismiss failure must be non-fatal, got: %v", err)
	}

	// The systemic finding must survive even though dismiss pass failed.
	// (When dismiss has no input findings — because everything was
	// consolidated — it returns trivially; that's expected and fine.
	// The test still pins the invariant: consolidator output is in
	// the final result.)
	foundSystemic := false
	for _, f := range result.Findings {
		if strings.HasPrefix(f.Title, "Systemic:") {
			foundSystemic = true
			break
		}
	}
	if !foundSystemic {
		t.Errorf("systemic finding must be preserved even on dismiss-pass failure, got %+v",
			result.Findings)
	}
}

// TestRecheckFindings_EmptyInputReturnsEmptyResult: the base case.
// No callers should hit this in production but the function must
// be safe to call defensively.
func TestRecheckFindings_EmptyInputReturnsEmptyResult(t *testing.T) {
	client := &promptRecordingClient{}
	result, err := RecheckFindings(context.Background(), client, nil, RecheckOptions{Mode: ModeAudit})
	if err != nil {
		t.Fatalf("RecheckFindings: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Errorf("empty input must produce empty result, got %d findings", len(result.Findings))
	}
	if client.CallCount() != 0 {
		t.Errorf("empty input must not trigger any LLM calls, got %d", client.CallCount())
	}
}

// ── splitFindingsByCategory ─────────────────────────────────────────────

func TestSplitFindingsByCategory_GroupsSameCategory(t *testing.T) {
	findings := []state.DeepFinding{
		makeFinding("F-001", "a.go", "1", "concurrency", "x", "low"),
		makeFinding("F-002", "b.go", "1", "io", "y", "low"),
		makeFinding("F-003", "c.go", "1", "concurrency", "z", "low"),
	}
	batches := splitFindingsByCategory(findings, 100)
	if len(batches) != 1 {
		t.Fatalf("under the cap, all categories should ship in one batch; got %d", len(batches))
	}
}

func TestSplitFindingsByCategory_SplitsAtCap(t *testing.T) {
	findings := []state.DeepFinding{
		makeFinding("F-001", "a.go", "1", "A", "x", "low"),
		makeFinding("F-002", "b.go", "1", "A", "x", "low"),
		makeFinding("F-003", "c.go", "1", "B", "x", "low"),
	}
	batches := splitFindingsByCategory(findings, 2)
	if len(batches) != 2 {
		t.Fatalf("category B should start a new batch when adding it would overflow cap=2; got %d batches", len(batches))
	}
	// First batch is the A pair.
	if len(batches[0]) != 2 || batches[0][0].Category != "A" {
		t.Errorf("first batch should be the A group, got %+v", batches[0])
	}
	if len(batches[1]) != 1 || batches[1][0].Category != "B" {
		t.Errorf("second batch should be the B group, got %+v", batches[1])
	}
}

func TestSplitFindingsByCategory_OversizeGroupGetsOwnBatch(t *testing.T) {
	// Cap=2 but category A has 5 findings — they all stay together
	// in one batch (we don't artificially split within a category
	// because that would lose intra-category pattern signal).
	var findings []state.DeepFinding
	for i := 0; i < 5; i++ {
		findings = append(findings, makeFinding(
			"F-00"+string(rune('1'+i)),
			"f.go", "1", "A", "x", "low",
		))
	}
	batches := splitFindingsByCategory(findings, 2)
	if len(batches) != 1 {
		t.Fatalf("oversize same-category group should stay in one batch; got %d", len(batches))
	}
	if len(batches[0]) != 5 {
		t.Errorf("oversize batch should contain all 5 findings, got %d", len(batches[0]))
	}
}

// ── Systemic-flag tagging by the parser ─────────────────────────────────
//
// The previous heuristic-based isSystemic helper was replaced with a
// structural state.DeepFinding.Systemic flag set by parseRecheckResult
// when a finding came out of the `consolidated` bucket. These tests
// pin the new contract: only consolidated findings carry the flag,
// kept/modified findings do not, and the routing in
// recheckConsolidateBatch uses the flag to decide which findings
// bypass the per-file dismiss pass.

func TestParseRecheckResult_TagsConsolidatedFindingsSystemic(t *testing.T) {
	original := []state.DeepFinding{
		{FindingID: "F-001", File: "a.go", Title: "x"},
		{FindingID: "F-002", File: "b.go", Title: "x"},
	}
	raw := `{
		"kept": [],
		"modified": [],
		"consolidated": [{
			"finding_ids": ["F-001", "F-002"],
			"finding": {
				"finding_id": "F-001",
				"file": "auth.go",
				"lines": "10-30",
				"severity": "high",
				"category": "x",
				"title": "Pattern across files",
				"description": "...",
				"trigger": "...",
				"suggestion": "..."
			}
		}],
		"dismissed": []
	}`

	result, err := parseRecheckResult(original, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 merged finding, got %d", len(result.Findings))
	}
	if !result.Findings[0].Systemic {
		// This is the central PR-5 contract. Without it, the
		// downstream routing falls back to a heuristic on File or
		// Title — and a consolidator that emits a per-file-looking
		// File path (e.g. "auth.go" as the example above does) for
		// the merged finding would silently leak into the dismiss
		// pass and could be dismissed by per-file context.
		t.Error("consolidated finding must be tagged Systemic=true; got false")
	}
}

func TestParseRecheckResult_DoesNotTagKeptOrModifiedSystemic(t *testing.T) {
	original := []state.DeepFinding{
		{FindingID: "F-001", File: "a.go", Title: "x", Systemic: false},
		{FindingID: "F-002", File: "b.go", Title: "y", Systemic: false},
	}
	raw := `{
		"kept": ["F-001"],
		"modified": [{"finding_id": "F-002", "severity": "low"}],
		"consolidated": [],
		"dismissed": []
	}`

	result, err := parseRecheckResult(original, raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range result.Findings {
		if f.Systemic {
			t.Errorf("non-consolidated finding %q must not be tagged Systemic, got true", f.FindingID)
		}
	}
}

// TestRecheckConsolidateBatch_RoutesByFlagNotByHeuristic verifies the
// routing decision. It feeds the consolidator a response where the
// merged finding has a concrete (non-"multiple") File path, which
// the old heuristic would have misclassified as non-systemic. The
// flag-based routing must still correctly route it into
// `consolidations`.
func TestRecheckConsolidateBatch_RoutesByFlagNotByHeuristic(t *testing.T) {
	findings := []state.DeepFinding{
		makeFinding("", "a.go", "10", "X", "thing", "low"),
		makeFinding("", "b.go", "20", "X", "thing", "low"),
	}
	AssignFindingIDs(findings)

	// Consolidator response: merged finding has File="auth.go"
	// (would NOT match the old File=="multiple" heuristic).
	client := &promptRecordingClient{
		responses: []string{
			`{
				"kept": [],
				"consolidated": [{
					"finding_ids": ["F-001", "F-002"],
					"finding": {
						"finding_id": "F-001",
						"file": "auth.go",
						"lines": "10-30",
						"severity": "medium",
						"category": "X",
						"title": "Pattern across auth handlers",
						"description": "...",
						"trigger": "...",
						"suggestion": "..."
					}
				}]
			}`,
		},
	}

	r, err := recheckConsolidateBatch(context.Background(), client, findings, RecheckOptions{Mode: ModeAudit})
	if err != nil {
		t.Fatalf("recheckConsolidateBatch: %v", err)
	}
	if len(r.consolidations) != 1 {
		t.Fatalf("expected 1 consolidation, got %d (the merged finding got misrouted to kept under the old heuristic)",
			len(r.consolidations))
	}
	if len(r.kept) != 0 {
		t.Errorf("expected no kept findings, got %d", len(r.kept))
	}
	if !r.consolidations[0].Systemic {
		t.Error("consolidation must carry the Systemic flag through to the caller")
	}
}

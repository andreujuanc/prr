package review

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

// TestRunRecheck_EmitsEvenWithNoFindings pins the contract that
// RunRecheck pings the progress channel before returning, even when
// there's nothing to recheck. Without this, a clean PR with zero
// deep findings leaves the Recheck phase row in PhaseWaiting (gray)
// and users read that as "skipped" when it actually ran fine.
func TestRunRecheck_EmitsEvenWithNoFindings(t *testing.T) {
	var got []string
	onProgress := func(msg string) { got = append(got, msg) }

	kept, dismissals, changed := RunRecheck(
		context.Background(),
		nil, // client unused when findings is empty — early return
		nil, // no findings
		ModePR,
		"",
		onProgress,
		nil,
	)

	if len(kept) != 0 || dismissals != nil || changed {
		t.Errorf("empty-findings path: unexpected non-zero return values")
	}
	if len(got) == 0 {
		t.Fatal("RunRecheck must emit at least one progress event so the phase row activates")
	}
}

func TestAssignFindingIDs(t *testing.T) {
	findings := []state.DeepFinding{
		{File: "a.go", Title: "first"},
		{File: "b.go", Title: "second"},
		{File: "c.go", Title: "third"},
	}
	AssignFindingIDs(findings)

	if findings[0].FindingID != "F-001" {
		t.Errorf("expected F-001, got %s", findings[0].FindingID)
	}
	if findings[1].FindingID != "F-002" {
		t.Errorf("expected F-002, got %s", findings[1].FindingID)
	}
	if findings[2].FindingID != "F-003" {
		t.Errorf("expected F-003, got %s", findings[2].FindingID)
	}
}

func TestParseRecheckResult_KeepAll(t *testing.T) {
	findings := []state.DeepFinding{
		{FindingID: "F-001", File: "a.go", Severity: "high", Title: "Issue A"},
		{FindingID: "F-002", File: "b.go", Severity: "medium", Title: "Issue B"},
	}
	raw := `{"kept": ["F-001", "F-002"], "modified": [], "consolidated": [], "dismissed": []}`

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(result.Findings))
	}
	if result.DismissedCount != 0 {
		t.Errorf("expected 0 dismissed, got %d", result.DismissedCount)
	}
}

func TestParseRecheckResult_Dismiss(t *testing.T) {
	findings := []state.DeepFinding{
		{FindingID: "F-001", File: "a.go", Severity: "high", Title: "Real issue"},
		{FindingID: "F-002", File: "b.go", Severity: "low", Title: "False positive"},
	}
	raw := `{
		"kept": ["F-001"],
		"modified": [],
		"consolidated": [],
		"dismissed": [{"finding_id": "F-002", "rationale": "Not a real issue"}]
	}`

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result.Findings))
	}
	if result.Findings[0].FindingID != "F-001" {
		t.Errorf("expected F-001, got %s", result.Findings[0].FindingID)
	}
	if result.DismissedCount != 1 {
		t.Errorf("expected 1 dismissed, got %d", result.DismissedCount)
	}

	// Structured dismissal: every removed finding must carry its
	// original payload plus the rationale, not just a count.
	if len(result.Dismissed) != 1 {
		t.Fatalf("expected 1 dismissed record, got %d", len(result.Dismissed))
	}
	d := result.Dismissed[0]
	if d.FindingID != "F-002" {
		t.Errorf("expected dismissed FindingID F-002, got %q", d.FindingID)
	}
	if d.Rationale != "Not a real issue" {
		t.Errorf("expected rationale to be preserved, got %q", d.Rationale)
	}
	// Original finding payload must be captured so the report can
	// render what was dropped, not just an ID stub.
	if d.Finding.File != "b.go" {
		t.Errorf("expected original File preserved, got %q", d.Finding.File)
	}
	if d.Finding.Title != "False positive" {
		t.Errorf("expected original Title preserved, got %q", d.Finding.Title)
	}
	if d.Finding.Severity != "low" {
		t.Errorf("expected original Severity preserved, got %q", d.Finding.Severity)
	}
}

// TestParseRecheckResult_Dismiss_MultiplePreservesEachRationale checks
// that a batch of dismissals doesn't collapse rationales — each
// removed finding needs to surface its own reason in the report.
func TestParseRecheckResult_Dismiss_MultiplePreservesEachRationale(t *testing.T) {
	findings := []state.DeepFinding{
		{FindingID: "F-001", File: "a.go", Severity: "high", Title: "Keep"},
		{FindingID: "F-002", File: "b.go", Severity: "low", Title: "Drop one"},
		{FindingID: "F-003", File: "c.go", Severity: "low", Title: "Drop two"},
	}
	raw := `{
		"kept": ["F-001"],
		"modified": [],
		"consolidated": [],
		"dismissed": [
			{"finding_id": "F-002", "rationale": "test-only path, never runs in prod"},
			{"finding_id": "F-003", "rationale": "framework escapes input upstream"}
		]
	}`

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dismissed) != 2 {
		t.Fatalf("expected 2 dismissed records, got %d", len(result.Dismissed))
	}

	// Index the records by ID so the test isn't order-sensitive.
	got := make(map[string]state.DismissedRecord, len(result.Dismissed))
	for _, d := range result.Dismissed {
		got[d.FindingID] = d
	}

	if r, ok := got["F-002"]; !ok || r.Rationale != "test-only path, never runs in prod" {
		t.Errorf("F-002 rationale mismatch: %+v", r)
	}
	if r, ok := got["F-003"]; !ok || r.Rationale != "framework escapes input upstream" {
		t.Errorf("F-003 rationale mismatch: %+v", r)
	}
	if result.DismissedCount != 2 {
		t.Errorf("DismissedCount must equal len(Dismissed), got %d vs %d",
			result.DismissedCount, len(result.Dismissed))
	}
}

// TestParseRecheckResult_Dismiss_UnknownIDSkipped ensures the parser
// is robust to the LLM citing a dismissal for an ID that wasn't in
// the input — the unknown ID is logged and skipped rather than
// producing a record with a zero-value Finding (which would render
// nonsense in the report).
func TestParseRecheckResult_Dismiss_UnknownIDSkipped(t *testing.T) {
	findings := []state.DeepFinding{
		{FindingID: "F-001", File: "a.go", Severity: "high", Title: "Real"},
	}
	raw := `{
		"kept": ["F-001"],
		"modified": [],
		"consolidated": [],
		"dismissed": [{"finding_id": "F-999", "rationale": "phantom"}]
	}`

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dismissed) != 0 {
		t.Errorf("dismissal for unknown ID must be dropped, got %d records", len(result.Dismissed))
	}
	if result.DismissedCount != 0 {
		t.Errorf("DismissedCount must be 0 for unknown-ID dismissals, got %d", result.DismissedCount)
	}
	// And the real finding still passes through.
	if len(result.Findings) != 1 || result.Findings[0].FindingID != "F-001" {
		t.Errorf("expected F-001 to survive, got %+v", result.Findings)
	}
}

// TestDismissedRecord_RoundTripsJSON verifies the structured dismissal
// record survives marshal/unmarshal — recheck dismissals are persisted
// on state.State and round-tripped through state.Save / state.Load,
// so the JSON shape is part of the contract.
func TestDismissedRecord_RoundTripsJSON(t *testing.T) {
	original := state.DismissedRecord{
		FindingID: "F-007",
		Finding: state.DeepFinding{
			FindingID: "F-007",
			File:      "internal/auth/login.go",
			Lines:     "42-58",
			Severity:  "medium",
			Title:     "Missing rate limit",
			Trigger:   state.FindingTrigger{Repro: "user spams login"},
		},
		Rationale: "covered by upstream gateway rate limit",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded state.DismissedRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.FindingID != original.FindingID {
		t.Errorf("FindingID lost in round trip: %q vs %q", decoded.FindingID, original.FindingID)
	}
	if decoded.Rationale != original.Rationale {
		t.Errorf("Rationale lost: %q vs %q", decoded.Rationale, original.Rationale)
	}
	if decoded.Finding.File != original.Finding.File {
		t.Errorf("Finding.File lost: %q vs %q", decoded.Finding.File, original.Finding.File)
	}
	if decoded.Finding.Lines != original.Finding.Lines {
		t.Errorf("Finding.Lines lost: %q vs %q", decoded.Finding.Lines, original.Finding.Lines)
	}
	if decoded.Finding.Trigger != original.Finding.Trigger {
		t.Errorf("Finding.Trigger lost: %q vs %q", decoded.Finding.Trigger, original.Finding.Trigger)
	}
}

func TestParseRecheckResult_Modify(t *testing.T) {
	findings := []state.DeepFinding{
		{FindingID: "F-001", File: "a.go", Severity: "medium", Title: "Old title", Description: "Old desc"},
	}
	raw := `{
		"kept": [],
		"modified": [{"finding_id": "F-001", "severity": "high", "title": "Better title"}],
		"consolidated": [],
		"dismissed": []
	}`

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result.Findings))
	}
	f := result.Findings[0]
	if f.Severity != "high" {
		t.Errorf("expected severity high, got %s", f.Severity)
	}
	if f.Title != "Better title" {
		t.Errorf("expected 'Better title', got %s", f.Title)
	}
	if f.Description != "Old desc" {
		t.Errorf("description should be unchanged, got %s", f.Description)
	}
	if result.ModifiedCount != 1 {
		t.Errorf("expected 1 modified, got %d", result.ModifiedCount)
	}
}

func TestParseRecheckResult_Consolidate(t *testing.T) {
	findings := []state.DeepFinding{
		{FindingID: "F-001", File: "a.go", Severity: "medium", Title: "Missing validation"},
		{FindingID: "F-002", File: "b.go", Severity: "medium", Title: "Missing validation"},
		{FindingID: "F-003", File: "c.go", Severity: "high", Title: "Unrelated issue"},
	}
	raw := `{
		"kept": ["F-003"],
		"modified": [],
		"consolidated": [{
			"finding_ids": ["F-001", "F-002"],
			"finding": {
				"finding_id": "F-001",
				"file": "multiple",
				"lines": "",
				"severity": "high",
				"category": "input-validation",
				"title": "Systemic: Missing input validation",
				"description": "Found in a.go and b.go"
			}
		}],
		"dismissed": []
	}`

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("expected 2 findings (1 kept + 1 consolidated), got %d", len(result.Findings))
	}
	if result.ConsolidatedCount != 1 {
		t.Errorf("expected 1 consolidated (net reduction), got %d", result.ConsolidatedCount)
	}
}

func TestParseRecheckResult_MarkdownFences(t *testing.T) {
	findings := []state.DeepFinding{
		{FindingID: "F-001", File: "a.go", Severity: "high", Title: "Issue"},
	}
	raw := "```json\n{\"kept\": [\"F-001\"], \"modified\": [], \"consolidated\": [], \"dismissed\": []}\n```"

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result.Findings))
	}
}

func TestParseRecheckResult_UnreferencedFindings(t *testing.T) {
	findings := []state.DeepFinding{
		{FindingID: "F-001", File: "a.go", Title: "Referenced"},
		{FindingID: "F-002", File: "b.go", Title: "Unreferenced"},
	}
	// LLM forgot to mention F-002 — it should be kept (safety net)
	raw := `{"kept": ["F-001"], "modified": [], "consolidated": [], "dismissed": []}`

	result, err := parseRecheckResult(findings, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("expected 2 findings (1 kept + 1 unreferenced), got %d", len(result.Findings))
	}
}

func TestSplitFindingsByFile(t *testing.T) {
	findings := make([]state.DeepFinding, 12)
	for i := range findings {
		findings[i] = state.DeepFinding{
			FindingID: "F-" + string(rune('A'+i)),
			File:      string(rune('a' + i/4)), // 3 files, 4 findings each
		}
	}

	batches := splitFindingsByFile(findings, 5)
	if len(batches) != 3 {
		t.Errorf("expected 3 batches, got %d", len(batches))
	}
	// First batch: file 'a' (4 findings)
	// Second batch: file 'b' (4 findings)
	// Third batch: file 'c' (4 findings)
	for i, batch := range batches {
		if len(batch) != 4 {
			t.Errorf("batch %d: expected 4 findings, got %d", i, len(batch))
		}
	}
}

// ── OnProgress wiring ──────────────────────────────────────────────────
//
// Pin the contract that RecheckFindings fires OnProgress on every
// batch completion. Without per-batch ticks the TUI's recheck row
// would show "0/N" the entire phase and the user would see the
// black hole that motivated this feature.

// recheckProgressTick records each (done, total) tuple OnProgress
// receives. Safe under the goroutine concurrency inside RecheckFindings.
type recheckProgressTick struct {
	done, total int
}

func recordRecheckProgress() (*[]recheckProgressTick, func(done, total int)) {
	var mu sync.Mutex
	var ticks []recheckProgressTick
	return &ticks, func(done, total int) {
		mu.Lock()
		defer mu.Unlock()
		ticks = append(ticks, recheckProgressTick{done, total})
	}
}

// fakeAIClient lives in pipeline_persistence_test.go (same package).
// Both tests below use it via &fakeAIClient{Responder: ...}.

func TestRecheckFindings_OnProgress_SingleBatch(t *testing.T) {
	// Findings <= MaxFindingsPerBatch (50) take the single-call path.
	// Expect: emit(0, N) at start, emit(N, N) at end.
	findings := make([]state.DeepFinding, 5)
	for i := range findings {
		findings[i] = state.DeepFinding{File: fmt.Sprintf("f%d.go", i), Severity: "low", Title: "x"}
	}

	ticks, onProgress := recordRecheckProgress()
	_, err := RecheckFindings(context.Background(), &fakeAIClient{
		Responder: func(_, _ string) string {
			return `{"kept": ["F-001","F-002","F-003","F-004","F-005"], "modified":[], "consolidated":[], "dismissed":[]}`
		},
	}, findings, RecheckOptions{
		Mode:                ModePR,
		MaxFindingsPerBatch: 50,
		OnProgress:          onProgress,
	})
	if err != nil {
		t.Fatalf("RecheckFindings: %v", err)
	}

	if len(*ticks) < 2 {
		t.Fatalf("expected at least 2 progress ticks (start + end); got %d: %v", len(*ticks), *ticks)
	}
	if first := (*ticks)[0]; first != (recheckProgressTick{0, 5}) {
		t.Errorf("first tick = %+v, want (0, 5)", first)
	}
	last := (*ticks)[len(*ticks)-1]
	if last != (recheckProgressTick{5, 5}) {
		t.Errorf("last tick = %+v, want (5, 5)", last)
	}
}

func TestRecheckFindings_OnProgress_MultipleBatches(t *testing.T) {
	// Findings > MaxFindingsPerBatch trigger the parallel batched path.
	// Expect: emit(0, N) at start, then one emit per batch completion,
	// ending at (N, N).
	const total = 12
	findings := make([]state.DeepFinding, total)
	for i := range findings {
		// Span 4 files so splitFindingsByFile produces multiple batches
		// at MaxFindingsPerBatch=4.
		findings[i] = state.DeepFinding{
			File:     fmt.Sprintf("file%d.go", i/3),
			Severity: "low",
			Title:    "x",
		}
	}

	ticks, onProgress := recordRecheckProgress()
	_, err := RecheckFindings(context.Background(), &fakeAIClient{
		Responder: func(_, _ string) string {
			return `{"kept": [], "modified":[], "consolidated":[], "dismissed":[]}`
		},
	}, findings, RecheckOptions{
		Mode:                ModePR,
		MaxFindingsPerBatch: 4, // force multi-batch
		MaxConcurrency:      2,
		OnProgress:          onProgress,
	})
	if err != nil {
		t.Fatalf("RecheckFindings: %v", err)
	}

	// Must have at least: 1 start tick + 1 tick per batch.
	// Final tick must reach (total, total).
	if len(*ticks) < 2 {
		t.Fatalf("expected multiple progress ticks; got %d", len(*ticks))
	}
	if first := (*ticks)[0]; first != (recheckProgressTick{0, total}) {
		t.Errorf("first tick = %+v, want (0, %d)", first, total)
	}
	last := (*ticks)[len(*ticks)-1]
	if last.done != total || last.total != total {
		t.Errorf("final tick = %+v, want (%d, %d)", last, total, total)
	}
}

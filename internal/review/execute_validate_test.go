package review

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

func captureReviewLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

// ── isValidSeverity ────────────────────────────────────────────────────

func TestIsValidSeverity(t *testing.T) {
	// The severity vocabulary is load-bearing: severityRank sorts by
	// it, and the user reads findings in that order. Anything outside
	// the canonical set buries findings at position 4 (last). Pin the
	// set so adding a new severity is an explicit decision.
	valid := []string{"critical", "high", "medium", "low"}
	for _, s := range valid {
		if !isValidSeverity(s) {
			t.Errorf("isValidSeverity(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "info", "warning", "Critical", "HIGH", "trivial"}
	for _, s := range invalid {
		if isValidSeverity(s) {
			t.Errorf("isValidSeverity(%q) = true, want false", s)
		}
	}
}

// ── Per-finding field validation ───────────────────────────────────────

func TestValidateReviewResult_LogsMissingAOIID(t *testing.T) {
	buf := captureReviewLog(t)

	result := &state.DeepReviewResult{
		Type:        "individual",
		Category:    "correctness",
		Subcategory: "off-by-one",
		Findings: []state.DeepFinding{
			{AOIID: "", File: "x.go", Lines: "10", Severity: "high", Title: "x"},
		},
	}
	call := ReviewCall{Type: "individual", Category: "correctness", Subcategory: "off-by-one"}
	validateReviewResult(call, result)

	out := buf.String()
	if !strings.Contains(out, "without aoi_id") {
		t.Errorf("expected 'without aoi_id' log; got: %q", out)
	}
	if !strings.Contains(out, "x.go") {
		t.Errorf("log should include file path for grep-ability; got: %q", out)
	}
}

func TestValidateReviewResult_LogsMissingFile(t *testing.T) {
	buf := captureReviewLog(t)

	result := &state.DeepReviewResult{
		Findings: []state.DeepFinding{
			{AOIID: "a-1", File: "", Lines: "10", Severity: "high"},
		},
	}
	call := ReviewCall{Type: "individual", Category: "correctness", Subcategory: "off-by-one"}
	validateReviewResult(call, result)

	out := buf.String()
	if !strings.Contains(out, "without file path") {
		t.Errorf("expected 'without file path' log; got: %q", out)
	}
	if !strings.Contains(out, "a-1") {
		t.Errorf("log should include aoi_id; got: %q", out)
	}
}

func TestValidateReviewResult_LogsMissingLines(t *testing.T) {
	buf := captureReviewLog(t)

	result := &state.DeepReviewResult{
		Findings: []state.DeepFinding{
			{AOIID: "a-1", File: "x.go", Lines: "", Severity: "high"},
		},
	}
	call := ReviewCall{Type: "individual", Category: "correctness", Subcategory: "off-by-one"}
	validateReviewResult(call, result)

	out := buf.String()
	if !strings.Contains(out, "without lines") {
		t.Errorf("expected 'without lines' log; got: %q", out)
	}
}

func TestValidateReviewResult_LogsInvalidSeverity(t *testing.T) {
	buf := captureReviewLog(t)

	result := &state.DeepReviewResult{
		Findings: []state.DeepFinding{
			{AOIID: "a-1", File: "x.go", Lines: "10", Severity: "info"}, // not canonical
		},
	}
	call := ReviewCall{Type: "individual", Category: "correctness", Subcategory: "off-by-one"}
	validateReviewResult(call, result)

	out := buf.String()
	if !strings.Contains(out, "invalid severity") {
		t.Errorf("expected 'invalid severity' log; got: %q", out)
	}
	if !strings.Contains(out, `"info"`) {
		t.Errorf("log should quote the actual invalid value; got: %q", out)
	}
}

func TestValidateReviewResult_AcceptsValidFinding(t *testing.T) {
	buf := captureReviewLog(t)

	result := &state.DeepReviewResult{
		Findings: []state.DeepFinding{
			{AOIID: "a-1", File: "x.go", Lines: "10-12", Severity: "high", Title: "x"},
		},
	}
	call := ReviewCall{Type: "individual", Category: "correctness", Subcategory: "off-by-one"}
	validateReviewResult(call, result)

	out := buf.String()
	if out != "" {
		t.Errorf("valid finding should produce no log output; got: %q", out)
	}
}

// ── Grouped-call drop detection ────────────────────────────────────────

func TestValidateReviewResult_LogsGroupedCallDrops(t *testing.T) {
	buf := captureReviewLog(t)

	// Grouped call with 3 input AOIs. Response only addresses 2 of
	// them (one finding, one dismissal). The third silently vanishes —
	// log must surface it so the user can rerun or investigate.
	call := ReviewCall{
		Type:        "grouped",
		Category:    "error-handling",
		Subcategory: "swallowed-errors",
		AOIs: []security.AreaOfInterest{
			{ID: "g1"},
			{ID: "g2"},
			{ID: "g3"},
		},
	}
	result := &state.DeepReviewResult{
		Findings: []state.DeepFinding{
			{AOIID: "g1", File: "a.go", Lines: "10", Severity: "high"},
		},
		Dismissals: []state.DeepDismissal{
			{AOIID: "g2"},
		},
		// g3 silently absent
	}
	validateReviewResult(call, result)

	out := buf.String()
	if !strings.Contains(out, "dropped") {
		t.Errorf("expected 'dropped' log; got: %q", out)
	}
	if !strings.Contains(out, "g3") {
		t.Errorf("log should name the dropped AOI id; got: %q", out)
	}
	// The drop count must reflect reality. Without this, a future
	// change that mishandles partials (e.g. always logs "1 dropped")
	// would silently regress.
	if !strings.Contains(out, "1 of 3") {
		t.Errorf("log should report '1 of 3' for clarity; got: %q", out)
	}
}

func TestValidateReviewResult_AllGroupedAOIsAddressed_NoDropWarning(t *testing.T) {
	buf := captureReviewLog(t)

	call := ReviewCall{
		Type:        "grouped",
		Category:    "error-handling",
		Subcategory: "swallowed-errors",
		AOIs: []security.AreaOfInterest{
			{ID: "g1"}, {ID: "g2"},
		},
	}
	result := &state.DeepReviewResult{
		Findings: []state.DeepFinding{
			{AOIID: "g1", File: "a.go", Lines: "10", Severity: "high"},
		},
		Dismissals: []state.DeepDismissal{
			{AOIID: "g2"},
		},
	}
	validateReviewResult(call, result)

	out := buf.String()
	if strings.Contains(out, "dropped") {
		t.Errorf("no drop should be reported when all AOIs addressed; got: %q", out)
	}
}

func TestValidateReviewResult_IndividualCallsSkipDropCheck(t *testing.T) {
	// Individual calls have exactly 1 AOI by construction. If the
	// response is missing that AOI verdict, the call effectively
	// failed and would have surfaced as errReviewParse — running
	// drop detection here would just produce a noisy second log.
	buf := captureReviewLog(t)

	call := ReviewCall{
		Type:        "individual",
		Category:    "correctness",
		Subcategory: "off-by-one",
		AOIs:        []security.AreaOfInterest{{ID: "lonely-aoi"}},
	}
	// Empty result (no findings, no dismissals). Drop check would
	// say "1 of 1 dropped" if it ran — but we deliberately skip it.
	result := &state.DeepReviewResult{}
	validateReviewResult(call, result)

	out := buf.String()
	if strings.Contains(out, "dropped") {
		t.Errorf("individual calls should skip drop detection; got: %q", out)
	}
}

func TestValidateReviewResult_NilResultIsSafe(t *testing.T) {
	// Defensive: never panic on a nil result, even though current
	// callers always pass non-nil.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("validateReviewResult panicked on nil result: %v", r)
		}
	}()
	validateReviewResult(ReviewCall{Type: "individual"}, nil)
}

// ── Integration: doReviewCall pipes through validation ─────────────────

func TestDoReviewCall_ValidatesResultBeforeReturn(t *testing.T) {
	// Confirm validateReviewResult is wired into the per-call path
	// (not just exported as a helper). A response with an invalid
	// severity should produce both a result AND a log entry.
	buf := captureReviewLog(t)

	const badSeverityResponse = `{
  "aoi_id": "x-go-1",
  "status": "finding",
  "file": "x.go",
  "lines": "10",
  "severity": "trivial",
  "category": "correctness",
  "subcategory": "off-by-one",
  "title": "x",
  "description": "y",
  "evidence": "z",
  "trigger": "t",
  "suggestion": "s"
}`

	client := &stubClient{responses: []string{badSeverityResponse}}
	opts := ExecuteOptions{Mode: ModeAudit}

	result, err := doReviewCall(nil, client, buildIndivCall(), opts, 0) //nolint:staticcheck // nil ctx is fine for stub
	if err != nil {
		t.Fatalf("doReviewCall: %v", err)
	}
	if result == nil || len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding; got %+v", result)
	}

	if !strings.Contains(buf.String(), "invalid severity") {
		t.Errorf("validation should have logged the invalid severity; got: %q", buf.String())
	}
}

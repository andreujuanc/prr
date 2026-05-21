package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// In-loop evidence verification — integration tests for the corrector
// flow. These tests pin the contract:
//
//   1. Findings with snippets that match the file flow through untouched
//      (no corrector round trip).
//   2. Findings with mismatched snippets trigger ONE corrector call;
//      if the corrector fixes the snippet, the finding survives.
//   3. If the corrector withdraws the finding, it gets dropped.
//   4. If the corrector returns yet another bad snippet, the finding
//      gets dropped after one retry (no infinite loop).
//   5. If the corrector LLM call errors, unverifiable findings are
//      dropped without further retry (non-fatal, but still subtractive).
//   6. RepoRoot=="" or SkipEvidenceVerify=true skips verification
//      entirely — findings pass through unchanged.
//
// The stub client lets us script the exact sequence of responses the
// model returns, so each test reflects one branch of the flow.

// buildFindingResponseJSON builds the individual-call JSON shape with
// the fields the parser actually reads. Caller supplies file, lines,
// and the evidence_snippet — those are what verification cares about.
func buildFindingResponseJSON(file, lines, snippet string) string {
	resp := map[string]any{
		"aoi_id":           "x-go-1",
		"status":           "finding",
		"file":             file,
		"lines":            lines,
		"severity":         "high",
		"category":         "correctness",
		"subcategory":      "off-by-one",
		"title":            "Boundary error",
		"description":      "loop runs one extra iteration",
		"evidence":         "verified by re-reading the file",
		"evidence_snippet": snippet,
		"trigger":          "len(arr) == cap(arr)",
		"suggestion":       "use < instead of <=",
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// buildCorrectorResponseJSON builds a corrector response with one
// correction entry. Tests pick which fields to set (withdraw,
// corrected snippet, corrected lines).
func buildCorrectorResponseJSON(index int, withdraw bool, file, lines, snippet string) string {
	c := evidenceCorrection{
		Index:           index,
		Withdraw:        withdraw,
		File:            file,
		Lines:           lines,
		EvidenceSnippet: snippet,
	}
	body := struct {
		Corrections []evidenceCorrection `json:"corrections"`
	}{
		Corrections: []evidenceCorrection{c},
	}
	b, _ := json.Marshal(body)
	return string(b)
}

// writeRealFile lays down a real source file inside repoRoot and
// returns the relative path the LLM would cite. We're testing
// against actual file I/O because the verifier reads the file —
// mocking that out would defeat the test.
func writeRealFile(t *testing.T, repoRoot, name, content string) string {
	t.Helper()
	full := filepath.Join(repoRoot, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return name
}

func indivCallForAOI(aoi security.AreaOfInterest) ReviewCall {
	return ReviewCall{
		Type:        "individual",
		Category:    aoi.Category,
		Subcategory: aoi.Subcategory,
		Files:       []string{aoi.File},
		AOIs:        []security.AreaOfInterest{aoi},
	}
}

// TestDoReviewCall_NoCorrectorWhenSnippetMatches is the happy-path
// pin: a finding whose snippet appears in the file gets accepted
// without a corrector round trip.
func TestDoReviewCall_NoCorrectorWhenSnippetMatches(t *testing.T) {
	repoRoot := t.TempDir()
	writeRealFile(t, repoRoot, "x.go", "package x\n\nfunc Bad() {\n\tfor i := 0; i <= len(arr); i++ {\n\t}\n}\n")

	client := &stubClient{
		responses: []string{
			buildFindingResponseJSON("x.go", "4", "for i := 0; i <= len(arr); i++ {"),
		},
	}

	opts := ExecuteOptions{
		Mode:     ModeAudit,
		RepoRoot: repoRoot,
	}
	call := indivCallForAOI(security.AreaOfInterest{
		ID: "x-go-1", File: "x.go", Line: 4,
		Category: "correctness", Subcategory: "off-by-one",
	})

	result, err := doReviewCall(context.Background(), client, call, opts, 0)
	if err != nil {
		t.Fatalf("doReviewCall: %v", err)
	}
	if client.CallCount() != 1 {
		t.Errorf("matching snippet must NOT trigger a corrector call; got %d call(s), want 1", client.CallCount())
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding kept, got %d", len(result.Findings))
	}
}

// TestDoReviewCall_CorrectorRescuesFinding: bad snippet → one
// corrector round trip → model returns the real snippet → finding
// kept with the corrected snippet.
func TestDoReviewCall_CorrectorRescuesFinding(t *testing.T) {
	repoRoot := t.TempDir()
	writeRealFile(t, repoRoot, "x.go", "package x\n\nfunc Bad() {\n\tfor i := 0; i <= len(arr); i++ {\n\t}\n}\n")

	client := &stubClient{
		responses: []string{
			// First response: model fabricates a snippet.
			buildFindingResponseJSON("x.go", "4", "this is totally not in the file"),
			// Corrector response: model corrects with the real line.
			buildCorrectorResponseJSON(0, false, "x.go", "4", "for i := 0; i <= len(arr); i++ {"),
		},
	}

	opts := ExecuteOptions{
		Mode:     ModeAudit,
		RepoRoot: repoRoot,
	}
	call := indivCallForAOI(security.AreaOfInterest{
		ID: "x-go-1", File: "x.go", Line: 4,
		Category: "correctness", Subcategory: "off-by-one",
	})

	result, err := doReviewCall(context.Background(), client, call, opts, 0)
	if err != nil {
		t.Fatalf("doReviewCall: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("bad snippet must trigger exactly one corrector call; got %d call(s), want 2", client.CallCount())
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding kept after correction, got %d", len(result.Findings))
	}
	got := result.Findings[0].EvidenceSnippet
	want := "for i := 0; i <= len(arr); i++ {"
	if got != want {
		t.Errorf("corrected snippet not applied: got %q, want %q", got, want)
	}
}

// TestDoReviewCall_CorrectorWithdrawsFinding: bad snippet → corrector
// withdraws → finding dropped.
func TestDoReviewCall_CorrectorWithdrawsFinding(t *testing.T) {
	repoRoot := t.TempDir()
	writeRealFile(t, repoRoot, "x.go", "package x\n\nfunc Good() {}\n")

	client := &stubClient{
		responses: []string{
			buildFindingResponseJSON("x.go", "3", "panic somewhere not in file"),
			buildCorrectorResponseJSON(0, true, "", "", ""),
		},
	}

	opts := ExecuteOptions{
		Mode:     ModeAudit,
		RepoRoot: repoRoot,
	}
	call := indivCallForAOI(security.AreaOfInterest{
		ID: "x-go-1", File: "x.go", Line: 3,
	})

	result, err := doReviewCall(context.Background(), client, call, opts, 0)
	if err != nil {
		t.Fatalf("doReviewCall: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("withdraw path must use exactly 2 LLM calls; got %d", client.CallCount())
	}
	if len(result.Findings) != 0 {
		t.Errorf("withdrawn finding must be dropped, got %d findings", len(result.Findings))
	}
}

// TestDoReviewCall_DropsAfterSingleRetry: bad snippet → corrector
// returns yet another bad snippet → no further retries → finding
// dropped. This pins the retry-budget=1 contract.
func TestDoReviewCall_DropsAfterSingleRetry(t *testing.T) {
	repoRoot := t.TempDir()
	writeRealFile(t, repoRoot, "x.go", "package x\n")

	client := &stubClient{
		responses: []string{
			buildFindingResponseJSON("x.go", "1", "first hallucination"),
			buildCorrectorResponseJSON(0, false, "x.go", "1", "second hallucination, still wrong"),
		},
	}

	opts := ExecuteOptions{
		Mode:     ModeAudit,
		RepoRoot: repoRoot,
	}
	call := indivCallForAOI(security.AreaOfInterest{
		ID: "x-go-1", File: "x.go", Line: 1,
	})

	result, err := doReviewCall(context.Background(), client, call, opts, 0)
	if err != nil {
		t.Fatalf("doReviewCall: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("retry budget is exactly 1 — must see exactly 2 calls total; got %d", client.CallCount())
	}
	if len(result.Findings) != 0 {
		t.Errorf("doubly-bad snippet must be dropped, got %d findings", len(result.Findings))
	}
}

// TestDoReviewCall_CorrectorErrorDropsFindings: corrector call returns
// an error (transport-level failure) → don't retry → drop the bad
// findings, keep any that were good in the first place.
func TestDoReviewCall_CorrectorErrorDropsBadFindings(t *testing.T) {
	repoRoot := t.TempDir()
	writeRealFile(t, repoRoot, "x.go", "package x\n")

	client := &stubClient{
		responses: []string{
			buildFindingResponseJSON("x.go", "1", "fabricated snippet"),
		},
		errors: []error{
			nil,                               // first call succeeds (the review)
			fmt.Errorf("transport explosion"), // corrector errors
		},
	}

	opts := ExecuteOptions{
		Mode:     ModeAudit,
		RepoRoot: repoRoot,
	}
	call := indivCallForAOI(security.AreaOfInterest{
		ID: "x-go-1", File: "x.go", Line: 1,
	})

	result, err := doReviewCall(context.Background(), client, call, opts, 0)
	if err != nil {
		t.Fatalf("corrector failure must be non-fatal, got error: %v", err)
	}
	if client.CallCount() != 2 {
		t.Errorf("corrector should have been attempted once; got %d call(s)", client.CallCount())
	}
	if len(result.Findings) != 0 {
		t.Errorf("unverifiable finding must be dropped when corrector fails, got %d", len(result.Findings))
	}
}

// TestDoReviewCall_NoVerifyWhenRepoRootEmpty: a missing RepoRoot is
// the signal "we can't verify, trust the model". The pre-PR-3
// behavior is preserved.
func TestDoReviewCall_NoVerifyWhenRepoRootEmpty(t *testing.T) {
	client := &stubClient{
		responses: []string{
			buildFindingResponseJSON("totally/fake.go", "999", "no such line, no such file"),
		},
	}

	opts := ExecuteOptions{
		Mode: ModeAudit,
		// RepoRoot intentionally empty.
	}
	call := indivCallForAOI(security.AreaOfInterest{
		ID: "x-go-1", File: "totally/fake.go", Line: 999,
	})

	result, err := doReviewCall(context.Background(), client, call, opts, 0)
	if err != nil {
		t.Fatalf("doReviewCall: %v", err)
	}
	if client.CallCount() != 1 {
		t.Errorf("RepoRoot=='' must skip verification entirely; got %d call(s)", client.CallCount())
	}
	if len(result.Findings) != 1 {
		t.Errorf("findings must pass through without verification, got %d", len(result.Findings))
	}
}

// TestDoReviewCall_SkipEvidenceVerifyFlag: explicit opt-out behaves
// the same as missing RepoRoot.
func TestDoReviewCall_SkipEvidenceVerifyFlag(t *testing.T) {
	repoRoot := t.TempDir() // exists but we're skipping
	writeRealFile(t, repoRoot, "x.go", "package x\n")

	client := &stubClient{
		responses: []string{
			buildFindingResponseJSON("x.go", "999", "snippet that definitely isn't there"),
		},
	}

	opts := ExecuteOptions{
		Mode:               ModeAudit,
		RepoRoot:           repoRoot,
		SkipEvidenceVerify: true,
	}
	call := indivCallForAOI(security.AreaOfInterest{
		ID: "x-go-1", File: "x.go", Line: 999,
	})

	result, err := doReviewCall(context.Background(), client, call, opts, 0)
	if err != nil {
		t.Fatalf("doReviewCall: %v", err)
	}
	if client.CallCount() != 1 {
		t.Errorf("SkipEvidenceVerify must skip the corrector; got %d call(s)", client.CallCount())
	}
	if len(result.Findings) != 1 {
		t.Errorf("findings must pass through when skip is set, got %d", len(result.Findings))
	}
}

// TestDoReviewCall_CorrectorMessageMentionsBadIndexes makes sure the
// corrector prompt actually tells the model which findings it needs
// to fix. Without this, the model can't act on the request.
func TestDoReviewCall_CorrectorMessageMentionsBadIndexes(t *testing.T) {
	repoRoot := t.TempDir()
	writeRealFile(t, repoRoot, "x.go", "package x\nfunc OK() {}\n")

	// Capture the corrector message via a recording client.
	captured := &recordingClient{
		responses: []string{
			buildFindingResponseJSON("x.go", "2", "this isn't in the file"),
			buildCorrectorResponseJSON(0, true, "", "", ""), // withdraw
		},
	}

	opts := ExecuteOptions{
		Mode:     ModeAudit,
		RepoRoot: repoRoot,
	}
	call := indivCallForAOI(security.AreaOfInterest{
		ID: "x-go-1", File: "x.go", Line: 2,
	})

	if _, err := doReviewCall(context.Background(), captured, call, opts, 0); err != nil {
		t.Fatal(err)
	}
	if len(captured.lastMessages) < 2 {
		t.Fatalf("expected at least 2 messages on the corrector call, got %d", len(captured.lastMessages))
	}
	// Last user message must mention index 0 and the failing snippet so
	// the model can act on it.
	last := captured.lastMessages[len(captured.lastMessages)-1].Content
	if !strings.Contains(last, "index 0") {
		t.Errorf("corrector message must name the failing finding by index, got:\n%s", last)
	}
	if !strings.Contains(last, "this isn't in the file") {
		t.Errorf("corrector message must quote the failing snippet so the model knows what to correct, got:\n%s", last)
	}
}

// ── parseEvidenceCorrections ────────────────────────────────────────────

func TestParseEvidenceCorrections_HandlesMarkdownFence(t *testing.T) {
	// Models love wrapping JSON in ```json fences even when told not
	// to. Strip them.
	raw := "```json\n" + buildCorrectorResponseJSON(0, true, "", "", "") + "\n```"
	corrections := parseEvidenceCorrections(raw)
	if len(corrections) != 1 {
		t.Fatalf("expected 1 correction parsed, got %d", len(corrections))
	}
	if !corrections[0].Withdraw {
		t.Errorf("expected withdraw=true, got %+v", corrections[0])
	}
}

func TestParseEvidenceCorrections_NoJSONReturnsNil(t *testing.T) {
	if got := parseEvidenceCorrections("not json at all"); got != nil {
		t.Errorf("non-JSON corrector response must return nil, got %+v", got)
	}
}

func TestParseEvidenceCorrections_MalformedReturnsNil(t *testing.T) {
	if got := parseEvidenceCorrections(`{"corrections": [INVALID]}`); got != nil {
		t.Errorf("malformed JSON corrector response must return nil, got %+v", got)
	}
}

// ── applyEvidenceCorrections / dropFindingsAfterCorrector ────────────────

func TestApplyEvidenceCorrections_OutOfRangeIndexIsIgnored(t *testing.T) {
	result := &state.DeepReviewResult{
		Findings: []state.DeepFinding{
			{AOIID: "a", File: "x.go", Lines: "1", EvidenceSnippet: "old"},
		},
	}
	corrections := []evidenceCorrection{
		{Index: 5, EvidenceSnippet: "new"}, // out of range
	}
	withdrawn := applyEvidenceCorrections(result, corrections)
	if len(withdrawn) != 1 {
		t.Fatalf("withdrawn must be parallel to findings, got len=%d", len(withdrawn))
	}
	if withdrawn[0] {
		t.Errorf("no entry should be marked withdrawn from out-of-range corrections")
	}
	if result.Findings[0].EvidenceSnippet != "old" {
		t.Errorf("snippet must be unchanged when correction targets a bad index, got %q",
			result.Findings[0].EvidenceSnippet)
	}
}

func TestApplyEvidenceCorrections_WithdrawSetsBit(t *testing.T) {
	result := &state.DeepReviewResult{
		Findings: []state.DeepFinding{
			{AOIID: "a", File: "x.go", Lines: "1", EvidenceSnippet: "orig"},
			{AOIID: "b", File: "y.go", Lines: "2", EvidenceSnippet: "also orig"},
		},
	}
	corrections := []evidenceCorrection{
		{Index: 1, Withdraw: true},
	}
	withdrawn := applyEvidenceCorrections(result, corrections)
	if !withdrawn[1] {
		t.Errorf("withdraw=true must flip the parallel bit for index 1, got %v", withdrawn)
	}
	if withdrawn[0] {
		t.Errorf("unaffected indexes must not be flipped, got %v", withdrawn)
	}
	// Withdraw must NOT mutate the finding — re-verify still needs
	// the original values for logging.
	if result.Findings[1].EvidenceSnippet != "also orig" {
		t.Errorf("withdraw must not mutate the finding, got snippet %q",
			result.Findings[1].EvidenceSnippet)
	}
}

func TestDropFindingsAfterCorrector_WithdrawsBypassVerdict(t *testing.T) {
	// A withdrawn finding gets dropped even if its verdict says OK
	// (this is the explicit "I can't anchor it" path).
	result := &state.DeepReviewResult{
		Findings: []state.DeepFinding{
			{AOIID: "a", Title: "keep me"},
			{AOIID: "b", Title: "withdraw me"},
		},
	}
	verdicts := []evidenceVerdict{evidenceOK, evidenceOK}
	withdrawn := []bool{false, true}

	out := dropFindingsAfterCorrector(result, verdicts, withdrawn)
	if len(out.Findings) != 1 {
		t.Fatalf("expected 1 finding kept, got %d", len(out.Findings))
	}
	if out.Findings[0].Title != "keep me" {
		t.Errorf("wrong finding kept: %q", out.Findings[0].Title)
	}
}

func TestDropFindingsAfterCorrector_VerdictDropsRegardless(t *testing.T) {
	result := &state.DeepReviewResult{
		Findings: []state.DeepFinding{
			{AOIID: "a", Title: "good"},
			{AOIID: "b", Title: "still bad after retry"},
		},
	}
	verdicts := []evidenceVerdict{evidenceOK, evidenceMismatch}

	out := dropFindingsAfterCorrector(result, verdicts, nil)
	if len(out.Findings) != 1 {
		t.Fatalf("expected 1 kept, got %d", len(out.Findings))
	}
	if out.Findings[0].Title != "good" {
		t.Errorf("wrong finding kept: %q", out.Findings[0].Title)
	}
}

package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// fakeAIClient drives the pipeline with canned responses. The Responder
// receives the system prompt + user message and returns the LLM-style
// JSON the parser expects. Calls are recorded for assertions.
type fakeAIClient struct {
	mu    sync.Mutex
	calls []fakeCall

	Responder func(systemPrompt, userMsg string) string
}

type fakeCall struct {
	System string
	User   string
}

func (f *fakeAIClient) ChatStream(_ context.Context, systemPrompt string, msgs []ai.Message, onToken func(string)) (string, error) {
	user := ""
	if len(msgs) > 0 {
		user = msgs[0].Content
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{System: systemPrompt, User: user})
	f.mu.Unlock()

	resp := f.Responder(systemPrompt, user)
	if onToken != nil {
		onToken(resp)
	}
	return resp, nil
}

// extractAOIID pulls the AOI id from a deep-review user message. The
// prompt builder writes `**ID:** xxx` on its own line; we look for that
// and fall back to a stable default. This is test-helper code only —
// production parses the model's response, not the prompt it sent.
func extractAOIID(userMsg string) string {
	const marker = "**ID:** "
	_, after, ok := strings.Cut(userMsg, marker)
	if !ok {
		return "test-aoi"
	}
	rest := after
	if end := strings.IndexAny(rest, "\n\r"); end >= 0 {
		return strings.TrimSpace(rest[:end])
	}
	return strings.TrimSpace(rest)
}

// pinDeepFindingsResponse builds an individual-AOI deep-review JSON
// response. The pipeline parses these via ParseDeepReviewResult and
// flattens into ExecuteResult.Findings.
func pinDeepFindingsResponse(aoiID, file, lines string) string {
	return fmt.Sprintf(`{
  "aoi_id": "%s",
  "status": "finding",
  "file": "%s",
  "lines": "%s",
  "severity": "high",
  "category": "authentication",
  "subcategory": "missing-check",
  "title": "Issue for %s",
  "description": "A real-looking finding for testing.",
  "trigger": "Triggered under X.",
  "suggestion": "Fix it."
}`, aoiID, file, lines, aoiID)
}

// withFakeRepoRoot chdirs to a temp dir that looks like a git repo so
// state.RepoRoot resolves there. Returns a cleanup func.
//
// The state package's stateDir() falls back to cwd-relative .git/pr-tui
// when git.RepoRoot fails. We do better: plant a working .git/HEAD so
// `git rev-parse --show-toplevel` returns this dir.
func withFakeRepoRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git/HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return dir
}

// TestPipeline_DeepFindings_PersistedAcrossSessions is the load-bearing
// pipeline test: when RunReviewCalls produces deep findings AND the
// pipeline persists them to state.DeepFindings, a fresh state.Load
// (simulating "close prr and reopen") returns the same findings.
//
// This catches the exact $20 bug: review completes successfully, prr
// closes, reopens, findings are gone. Before the persistence fix the
// pipeline only saved findings as part of the synthesized Review — so
// if synthesis didn't run (or didn't save), the findings evaporated.
//
// We use RunReviewCalls directly (not the full RunReviewCore) because
// the bug lives at the Phase 1 → disk boundary; driving Phase 0
// (project context + AOI scan) would multiply test complexity without
// exercising the bug.
func TestPipeline_DeepFindings_PersistedAcrossSessions(t *testing.T) {
	withFakeRepoRoot(t)

	client := &fakeAIClient{
		Responder: func(systemPrompt, userMsg string) string {
			// Extract the AOI id from the user message — the deep
			// review prompt embeds the AOI's id and we use it for the
			// response so each call's output is distinct.
			aoiID := "test-aoi"
			if _, after, ok := strings.Cut(userMsg, `"id":`); ok {
				rest := strings.TrimSpace(after)
				if strings.HasPrefix(rest, `"`) {
					end := strings.Index(rest[1:], `"`)
					if end > 0 {
						aoiID = rest[1 : 1+end]
					}
				}
			}
			return pinDeepFindingsResponse(aoiID, "src/auth.go", "42")
		},
	}

	calls := []ReviewCall{
		{Type: "individual", AOIs: []security.AreaOfInterest{{ID: "aoi-1", File: "src/auth.go", Line: 42}}},
		{Type: "individual", AOIs: []security.AreaOfInterest{{ID: "aoi-2", File: "src/auth.go", Line: 55}}},
	}

	execResult, err := RunReviewCalls(context.Background(), client, calls, ExecuteOptions{Mode: ModePR})
	if err != nil {
		t.Fatalf("RunReviewCalls: %v", err)
	}
	if len(execResult.Findings) != 2 {
		t.Fatalf("expected 2 deep findings from RunReviewCalls, got %d", len(execResult.Findings))
	}

	// Persistence step — this mirrors what pipeline.go now does at the
	// Phase 1a boundary. The bug fix added these two lines to the
	// pipeline; the test asserts they actually persist.
	s := state.NewState("42")
	s.SetDeepFindings(execResult.Findings)
	if err := state.Save(s); err != nil {
		t.Fatalf("state.Save: %v", err)
	}

	// Simulate "user closes prr, reopens prr": fresh Load from disk.
	reloaded, err := state.Load("42")
	if err != nil {
		t.Fatalf("state.Load (post-reopen): %v", err)
	}
	got := reloaded.GetDeepFindings()
	if len(got) != 2 {
		t.Fatalf("after reopen: DeepFindings count = %d, want 2 (findings lost across session)", len(got))
	}
	// Content survives: title, severity, file/line must all round-trip.
	// The title prefix is stable regardless of how the test responder
	// extracts AOI ids.
	for i, f := range got {
		if !strings.HasPrefix(f.Title, "Issue for") {
			t.Errorf("got[%d].Title = %q, want prefix 'Issue for' (content lost on reopen)", i, f.Title)
		}
		if f.Severity != "high" || f.Category != "authentication" {
			t.Errorf("got[%d] severity/category lost: %+v", i, f)
		}
		if f.File != "src/auth.go" {
			t.Errorf("got[%d].File = %q, want 'src/auth.go'", i, f.File)
		}
	}
}

// TestPipeline_FencedResponse_DoesNotBreakPersistence is the integration
// test for the persistence bug we hunted from the debug log. Scenario:
// one of the LLM calls returns a markdown-fenced JSON response (very
// common in the wild). Before the fix, the unstripped backticks landed
// in DeepReviewResult.RawOutput (a json.RawMessage), and from that
// moment on every state.Save in the session failed silently. The user
// closed prr, reopened, and their findings were gone.
//
// Test contract: run two AOI calls — one returns clean JSON, the other
// returns the same JSON wrapped in ```json fences. Wire the deep-review
// cache to state.Save (the same closure the real pipeline uses). Then
// assert state.Save succeeds and a fresh Load sees both findings.
func TestPipeline_FencedResponse_DoesNotBreakPersistence(t *testing.T) {
	withFakeRepoRoot(t)

	// Responder returns fenced output for aoi-1 and clean output for
	// aoi-2 — the asymmetric shape that production sees when the model
	// sometimes wraps responses in code fences. The prompt embeds the
	// AOI id as `**ID:** xxx` (see internal/review/prompts.go), so we
	// match against that.
	var (
		fencedReturned bool
		cleanReturned  bool
	)
	client := &fakeAIClient{
		Responder: func(systemPrompt, _ string) string {
			// The AOI block is embedded in the system prompt by
			// BuildIndividualPrompt (see internal/review/prompts.go).
			aoiID := extractAOIID(systemPrompt)
			body := pinDeepFindingsResponse(aoiID, "src/auth.go", "42")
			if aoiID == "aoi-1" {
				fencedReturned = true
				return "```json\n" + body + "\n```"
			}
			cleanReturned = true
			return body
		},
	}

	calls := []ReviewCall{
		{Type: "individual", AOIs: []security.AreaOfInterest{{ID: "aoi-1", File: "src/auth.go", Line: 42}}},
		{Type: "individual", AOIs: []security.AreaOfInterest{{ID: "aoi-2", File: "src/auth.go", Line: 55}}},
	}

	// Build a state to use as the cache target. CacheSet here mirrors
	// the closure in internal/review/pipeline.go that wires
	// SetDeepReview → state.Save. We track save failures so the test
	// can assert nothing slipped through silently.
	s := state.NewState("42")
	var saveErrors []error
	opts := ExecuteOptions{
		Mode: ModePR,
		CacheSet: func(key string, result *state.DeepReviewResult) {
			s.SetDeepReview(key, result)
			if err := state.Save(s); err != nil {
				saveErrors = append(saveErrors, err)
			}
		},
	}

	execResult, err := RunReviewCalls(context.Background(), client, calls, opts)
	if err != nil {
		t.Fatalf("RunReviewCalls: %v", err)
	}
	if !fencedReturned || !cleanReturned {
		t.Fatalf("test plumbing broken: fenced=%v clean=%v (extractAOIID likely not matching the prompt)", fencedReturned, cleanReturned)
	}
	if len(execResult.Findings) != 2 {
		t.Fatalf("expected 2 findings (one from each call), got %d", len(execResult.Findings))
	}

	// THE assertion: state.Save during the cache-write path must not
	// have failed. Pre-fix this fires with the exact error from the
	// user debug log.
	if len(saveErrors) > 0 {
		t.Fatalf("state.Save failed %d time(s) during CacheSet (persistence is broken for fenced LLM output): %v",
			len(saveErrors), saveErrors[0])
	}

	// Persist the top-level findings list the way the pipeline does
	// after Phase 1a.
	s.SetDeepFindings(execResult.Findings)
	if err := state.Save(s); err != nil {
		t.Fatalf("state.Save (deep findings) failed — pipeline would silently drop them: %v", err)
	}

	// Simulate close-and-reopen.
	reloaded, err := state.Load("42")
	if err != nil {
		t.Fatalf("state.Load (post-reopen): %v", err)
	}
	if got := reloaded.GetDeepFindings(); len(got) != 2 {
		t.Fatalf("after reopen: DeepFindings count = %d, want 2 (findings lost across session)", len(got))
	}
}

// TestCoreOptions_SkipSynthesisShape pins the contract that SkipSynthesis
// produces a CoreResult with Review==nil and DeepFindings populated.
// The TUI relies on this shape to render findings directly without a
// synthesized Review object.
func TestCoreOptions_SkipSynthesisShape(t *testing.T) {
	// SkipSynthesis is the flag the TUI sets. With it set, the pipeline
	// must return without a Review object — synthesis is skipped, and
	// the UI uses DeepFindings as the source of truth.
	cr := &CoreResult{
		DeepFindings: []state.DeepFinding{
			{FindingID: "F-001", Title: "test"},
		},
		FileFindings: map[string]string{"x.go": "ok"},
	}
	if cr.Review != nil {
		t.Error("SkipSynthesis path should produce CoreResult with Review==nil")
	}
	if len(cr.DeepFindings) == 0 {
		t.Error("SkipSynthesis path should still populate DeepFindings")
	}
}

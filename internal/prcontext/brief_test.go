package prcontext

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/state"
)

// ── Fake ai.Client ──────────────────────────────────────────────────────

// fakeFastClient records inputs and returns a fixed output. Returning a
// canned summary lets us assert the brief is wrapped and persisted
// correctly without touching a real LLM.
type fakeFastClient struct {
	systemPrompt string
	userMessage  string
	calls        int
	response     string
	err          error
}

func (f *fakeFastClient) ChatStream(_ context.Context, system string, msgs []ai.Message, onToken func(string)) (string, error) {
	f.systemPrompt = system
	if len(msgs) > 0 {
		f.userMessage = msgs[0].Content
	}
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if onToken != nil {
		onToken(f.response)
	}
	return f.response, nil
}

// ── ghRunner injection ──────────────────────────────────────────────────

// withFakeGh swaps the package-level ghRunner for the duration of a test
// and restores it on cleanup.
func withFakeGh(t *testing.T, out string, err error) {
	t.Helper()
	prev := ghRunner
	ghRunner = func(args ...string) (string, error) {
		return out, err
	}
	t.Cleanup(func() { ghRunner = prev })
}

// samplePR returns a populated PullRequest stub for tests.
func samplePR() *git.PullRequest {
	return &git.PullRequest{
		Number:      42,
		Title:       "Add auth middleware",
		Body:        "Introduces shared auth checks across handlers.",
		BaseRefName: "main",
		HeadRefName: "feat/auth",
	}
}

// minimalGhJSON is the smallest valid gh-pr-view --json payload we expect.
const minimalGhJSON = `{"comments":[],"reviews":[],"labels":[],"statusCheckRollup":[]}`

// busyGhJSON has comments + reviews + CI, used to verify the input
// hash actually changes when content does.
const busyGhJSON = `{
  "comments":[{"id":"c1","updatedAt":"2026-05-10T10:00:00Z","body":"Looks risky around line 45"}],
  "reviews":[{"id":"r1","submittedAt":"2026-05-10T11:00:00Z","state":"COMMENTED","body":"questions about auth flow"}],
  "labels":[{"name":"security"},{"name":"needs-review"}],
  "statusCheckRollup":[{"name":"test","status":"COMPLETED","conclusion":"SUCCESS"}]
}`

// ── BuildPRBrief: happy path ────────────────────────────────────────────

func TestBuildPRBrief_HappyPath(t *testing.T) {
	withFakeGh(t, busyGhJSON, nil)
	client := &fakeFastClient{response: "PR adds auth middleware. One reviewer questioned the auth flow; CI is passing."}

	s := state.NewState("42")
	brief, err := BuildPRBrief(context.Background(), client, samplePR(), s, "", nil)
	if err != nil {
		t.Fatalf("BuildPRBrief: %v", err)
	}
	if brief == nil {
		t.Fatal("nil brief")
	}
	if !strings.Contains(brief.Summary, "## PR Brief") {
		t.Errorf("brief missing '## PR Brief' wrapper:\n%s", brief.Summary)
	}
	if !strings.Contains(brief.Summary, "auth middleware") {
		t.Errorf("brief missing canned content:\n%s", brief.Summary)
	}
	if brief.InputHash == "" {
		t.Error("InputHash empty after successful build")
	}
	if brief.FromCache {
		t.Error("FromCache should be false on a fresh build")
	}
	if client.calls != 1 {
		t.Errorf("expected 1 LLM call, got %d", client.calls)
	}
}

// ── Cache hit: same inputs → no LLM call ────────────────────────────────

func TestBuildPRBrief_CacheHit(t *testing.T) {
	withFakeGh(t, busyGhJSON, nil)
	client := &fakeFastClient{response: "first call summary"}

	s := state.NewState("42")
	first, err := BuildPRBrief(context.Background(), client, samplePR(), s, "", nil)
	if err != nil {
		t.Fatalf("first BuildPRBrief: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("expected 1 LLM call after first build, got %d", client.calls)
	}

	// Second call with the same inputs and the previous hash — should
	// hit cache and skip the LLM.
	second, err := BuildPRBrief(context.Background(), client, samplePR(), s, first.InputHash, nil)
	if err != nil {
		t.Fatalf("second BuildPRBrief: %v", err)
	}
	if !second.FromCache {
		t.Error("expected FromCache=true on cache hit")
	}
	if second.InputHash != first.InputHash {
		t.Errorf("hash should be stable across calls: first=%s second=%s", first.InputHash, second.InputHash)
	}
	if client.calls != 1 {
		t.Errorf("LLM called %d times on cache hit, want 1", client.calls)
	}
}

// ── Cache invalidation: comment changes → new hash → LLM call ──────────

func TestBuildPRBrief_InvalidatesWhenCommentChanges(t *testing.T) {
	client := &fakeFastClient{response: "summary"}
	s := state.NewState("42")

	// First call with the busy JSON.
	withFakeGh(t, busyGhJSON, nil)
	first, err := BuildPRBrief(context.Background(), client, samplePR(), s, "", nil)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}

	// Mutate the comment body in the gh response. Even though id and
	// updatedAt could theoretically be stale, the body-content hash in
	// our input hash should catch this.
	mutated := strings.Replace(busyGhJSON, "Looks risky around line 45", "Looks risky around line 99", 1)
	withFakeGh(t, mutated, nil)
	second, err := BuildPRBrief(context.Background(), client, samplePR(), s, first.InputHash, nil)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}

	if second.InputHash == first.InputHash {
		t.Error("hash should change when comment body changes")
	}
	if second.FromCache {
		t.Error("FromCache should be false when comment content changes")
	}
}

// ── Failure modes: gh failure returns empty brief, doesn't error ───────

func TestBuildPRBrief_GhFailureIsNonFatal(t *testing.T) {
	withFakeGh(t, "", fmt.Errorf("gh not authenticated"))
	client := &fakeFastClient{response: "should not be called"}

	s := state.NewState("42")
	brief, err := BuildPRBrief(context.Background(), client, samplePR(), s, "", nil)
	if err != nil {
		t.Fatalf("BuildPRBrief should not error on gh failure: %v", err)
	}
	if brief.Summary != "" {
		t.Errorf("expected empty summary on gh failure, got: %s", brief.Summary)
	}
	if client.calls != 0 {
		t.Errorf("LLM should not be called when gh failed, got %d calls", client.calls)
	}
}

// ── Failure modes: LLM failure returns empty brief with hash, no error ──

func TestBuildPRBrief_LLMFailureIsNonFatal(t *testing.T) {
	withFakeGh(t, busyGhJSON, nil)
	client := &fakeFastClient{err: fmt.Errorf("LLM rate-limited")}

	s := state.NewState("42")
	brief, err := BuildPRBrief(context.Background(), client, samplePR(), s, "", nil)
	if err != nil {
		t.Fatalf("BuildPRBrief should not error on LLM failure: %v", err)
	}
	if brief.Summary != "" {
		t.Errorf("expected empty summary on LLM failure, got: %s", brief.Summary)
	}
	if brief.InputHash == "" {
		t.Error("InputHash should still be populated so caller can persist a 'tried' marker if desired")
	}
}

// ── Nil-PR guard ────────────────────────────────────────────────────────

func TestBuildPRBrief_NilPR(t *testing.T) {
	client := &fakeFastClient{response: "shouldn't be called"}
	brief, err := BuildPRBrief(context.Background(), client, nil, nil, "", nil)
	if err != nil {
		t.Fatalf("nil PR should not error: %v", err)
	}
	if brief.Summary != "" {
		t.Errorf("expected empty brief for nil PR, got: %s", brief.Summary)
	}
	if client.calls != 0 {
		t.Errorf("LLM should not be called for nil PR, got %d calls", client.calls)
	}
}

// ── Nil-client guard ────────────────────────────────────────────────────

func TestBuildPRBrief_NilClient(t *testing.T) {
	withFakeGh(t, busyGhJSON, nil)
	// nil fast client — caller wants the hash but no LLM call.
	brief, err := BuildPRBrief(context.Background(), nil, samplePR(), state.NewState("42"), "", nil)
	if err != nil {
		t.Fatalf("nil client should not error: %v", err)
	}
	if brief.Summary != "" {
		t.Errorf("nil client should produce empty summary, got: %s", brief.Summary)
	}
	if brief.InputHash == "" {
		t.Error("InputHash should still be computed even when LLM is unavailable")
	}
}

// ── Hash stability: identical inputs produce identical hashes ──────────

func TestHashPRInputs_Stable(t *testing.T) {
	inputs1 := &rawInputs{
		pr:     samplePR(),
		prJSON: []byte(busyGhJSON),
	}
	inputs2 := &rawInputs{
		pr:     samplePR(),
		prJSON: []byte(busyGhJSON),
	}
	h1 := hashPRInputs(inputs1)
	h2 := hashPRInputs(inputs2)
	if h1 != h2 {
		t.Errorf("hashes differ for identical inputs:\n%s\n%s", h1, h2)
	}
	if h1 == "" {
		t.Error("hash should never be empty for non-nil inputs")
	}
}

// ── Hash sensitivity: label-only change should still flip the hash ─────

func TestHashPRInputs_LabelChangeFlipsHash(t *testing.T) {
	withoutLabel := strings.Replace(busyGhJSON, `{"name":"security"},`, "", 1)
	a := &rawInputs{pr: samplePR(), prJSON: []byte(busyGhJSON)}
	b := &rawInputs{pr: samplePR(), prJSON: []byte(withoutLabel)}
	if hashPRInputs(a) == hashPRInputs(b) {
		t.Error("hash should change when labels change")
	}
}

// ── wrapBrief omits the section when summary is blank ──────────────────

func TestWrapBrief_EmptyInput(t *testing.T) {
	if got := wrapBrief(""); got != "" {
		t.Errorf("empty input should produce empty output, got: %q", got)
	}
	if got := wrapBrief("   \n  "); got != "" {
		t.Errorf("whitespace-only input should produce empty output, got: %q", got)
	}
}

func TestWrapBrief_AddsHeading(t *testing.T) {
	got := wrapBrief("PR is fine. CI passing.")
	if !strings.HasPrefix(got, "## PR Brief\n\n") {
		t.Errorf("expected '## PR Brief\\n\\n' prefix, got: %q", got)
	}
}

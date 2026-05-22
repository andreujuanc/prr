package audit

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// ── Pure-logic helpers ───────────────────────────────────────────────────

func TestTrimToLines(t *testing.T) {
	in := "a\nb\nc\nd\ne\nf"
	if got := trimToLines(in, 3); got != "a\nb\nc" {
		t.Errorf("trimToLines = %q, want %q", got, "a\nb\nc")
	}
	if got := trimToLines(in, 100); got != in {
		t.Errorf("trimToLines on short input should pass through")
	}
	if got := trimToLines(in, 0); got != "" {
		t.Errorf("trimToLines(_, 0) should return empty")
	}
}

func TestBuildBoundaryExcerpts_DeterministicOrder(t *testing.T) {
	files := map[string]string{
		"z.go": "z body\nline2",
		"a.go": "a body\nline2",
		"m.go": "m body\nline2",
	}
	got := buildBoundaryExcerpts(files, 5, 10)
	if len(got) != 3 {
		t.Fatalf("got %d excerpts, want 3", len(got))
	}
	if got[0].Path != "a.go" || got[1].Path != "m.go" || got[2].Path != "z.go" {
		t.Errorf("expected alphabetical order; got %v", []string{got[0].Path, got[1].Path, got[2].Path})
	}
}

func TestBuildBoundaryExcerpts_RespectsLineCap(t *testing.T) {
	files := map[string]string{"big.go": "1\n2\n3\n4\n5\n6\n7\n8"}
	got := buildBoundaryExcerpts(files, 3, 10)
	if got[0].Header != "1\n2\n3" {
		t.Errorf("excerpt = %q, want first 3 lines", got[0].Header)
	}
}

func TestBuildBoundaryExcerpts_DropsEmptyFiles(t *testing.T) {
	files := map[string]string{"a.go": "body", "b.go": ""}
	got := buildBoundaryExcerpts(files, 5, 10)
	if len(got) != 1 || got[0].Path != "a.go" {
		t.Errorf("empty files should be dropped; got %v", got)
	}
}

func TestBuildBoundaryExcerpts_CapsTotalFiles(t *testing.T) {
	files := map[string]string{
		"a.go": "x", "b.go": "x", "c.go": "x", "d.go": "x",
	}
	got := buildBoundaryExcerpts(files, 5, 2)
	if len(got) != 2 || got[0].Path != "a.go" || got[1].Path != "b.go" {
		t.Errorf("expected first 2 in alphabetical order; got %v", got)
	}
}

func TestHashBoundaryInputs_StableAndSensitive(t *testing.T) {
	exA := []boundaryExcerpt{{Path: "a.go", Header: "h1"}}
	exB := []boundaryExcerpt{{Path: "a.go", Header: "h2"}}
	model := &state.RuntimeModel{AuthModel: "x"}

	if hashBoundaryInputs(exA, model, "") != hashBoundaryInputs(exA, model, "") {
		t.Error("hash must be stable across calls")
	}
	if hashBoundaryInputs(exA, model, "") == hashBoundaryInputs(exB, model, "") {
		t.Error("different excerpts must produce different hashes")
	}
	other := &state.RuntimeModel{AuthModel: "y"}
	if hashBoundaryInputs(exA, model, "") == hashBoundaryInputs(exA, other, "") {
		t.Error("runtime model contributes to the hash")
	}
	if hashBoundaryInputs(exA, nil, "") == hashBoundaryInputs(exA, model, "") {
		t.Error("nil model vs present model must differ")
	}
	// New fix-commit → new bug-priors → cache must invalidate.
	if hashBoundaryInputs(exA, model, "") == hashBoundaryInputs(exA, model, "abc") {
		t.Error("bug-priors must contribute to the boundary hash")
	}
}

func TestNormalizeBoundaries(t *testing.T) {
	in := []state.Boundary{
		{Kind: "HTTP", File: " handler.go ", Description: "  desc "},
		{Kind: "", File: "x.go"},  // no kind → dropped
		{Kind: "queue", File: ""}, // no file → dropped
		{Kind: "Queue", File: "q.go"},
	}
	out := normalizeBoundaries(in)
	if len(out) != 2 {
		t.Fatalf("got %d boundaries, want 2", len(out))
	}
	if out[0].Kind != "http" || out[0].File != "handler.go" || out[0].Description != "desc" {
		t.Errorf("first boundary not normalized: %+v", out[0])
	}
	if out[1].Kind != "queue" {
		t.Errorf("second boundary kind not lowercased: %+v", out[1])
	}
}

func TestExtractJSONArray_Bare(t *testing.T) {
	raw := `[{"kind":"http","file":"a.go","description":"x"}]`
	if got := extractJSONArray(raw); got != raw {
		t.Errorf("bare JSON should pass through; got %q", got)
	}
}

func TestExtractJSONArray_WithPreamble(t *testing.T) {
	raw := "Here's the inventory:\n" + `[{"kind":"http","file":"a.go","description":"x"}]` + "\nDone."
	want := `[{"kind":"http","file":"a.go","description":"x"}]`
	if got := extractJSONArray(raw); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractJSONArray_FencedBlock(t *testing.T) {
	raw := "```json\n[{\"kind\":\"http\"}]\n```"
	want := `[{"kind":"http"}]`
	if got := extractJSONArray(raw); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractJSONArray_BracketInString(t *testing.T) {
	// A literal `]` inside a string must not close the array early.
	raw := `[{"description": "matches ] in text"}, {"k": "v"}]`
	if got := extractJSONArray(raw); got != raw {
		t.Errorf("bracket-in-string should not split: got %q", got)
	}
}

func TestExtractJSONArray_NoArray(t *testing.T) {
	if got := extractJSONArray("nothing here"); got != "" {
		t.Errorf("no array → empty; got %q", got)
	}
}

// ── Defense AOI synthesis ────────────────────────────────────────────────

func TestSynthesizeBoundaryAOIs_HTTPProducesThreeAOIs(t *testing.T) {
	got := SynthesizeBoundaryAOIs([]state.Boundary{
		{Kind: "http", File: "handler.go", Symbol: "POST/admin/users", Description: "admin creation"},
	})
	if len(got) != 3 {
		t.Fatalf("http boundary should yield 3 AOIs, got %d", len(got))
	}
	wantCategories := map[string]bool{"input-validation": false, "authorization": false, "error-handling": false}
	for _, aoi := range got {
		wantCategories[aoi.Category.String()] = true
		if aoi.Subcategory != "boundary-coverage" {
			t.Errorf("expected subcategory 'boundary-coverage', got %q", aoi.Subcategory)
		}
		if aoi.File != "handler.go" {
			t.Errorf("AOI file = %q, want handler.go", aoi.File)
		}
		if aoi.Urgency != "grouped" {
			t.Errorf("expected urgency 'grouped', got %q", aoi.Urgency)
		}
		if !strings.Contains(aoi.Context, "admin creation") {
			t.Errorf("context missing description: %q", aoi.Context)
		}
	}
	for cat, found := range wantCategories {
		if !found {
			t.Errorf("missing http AOI for category %q", cat)
		}
	}
}

func TestSynthesizeBoundaryAOIs_QueueProducesThreeAOIs(t *testing.T) {
	got := SynthesizeBoundaryAOIs([]state.Boundary{
		{Kind: "queue", File: "consumer.go", Description: "SNS topic"},
	})
	if len(got) != 3 {
		t.Fatalf("queue boundary should yield 3 AOIs, got %d", len(got))
	}
	concerns := make(map[string]bool, 3)
	for _, aoi := range got {
		concerns[aoi.Concern] = true
	}
	// Must include the per-record-isolation concern unique to queues.
	hasIsolation := false
	for c := range concerns {
		if strings.Contains(c, "poison-pill") || strings.Contains(c, "per-record") || strings.Contains(c, "isolated") {
			hasIsolation = true
		}
	}
	if !hasIsolation {
		t.Error("queue AOIs should include the per-record-isolation question")
	}
}

func TestSynthesizeBoundaryAOIs_ScheduledProducesConcurrencyAOI(t *testing.T) {
	got := SynthesizeBoundaryAOIs([]state.Boundary{
		{Kind: "scheduled", File: "cron.go", Description: "daily job"},
	})
	hasConcurrency := false
	for _, aoi := range got {
		if aoi.Category == "concurrency" {
			hasConcurrency = true
		}
	}
	if !hasConcurrency {
		t.Error("scheduled boundary should yield a concurrency AOI")
	}
}

func TestSynthesizeBoundaryAOIs_UnknownKindFallsBack(t *testing.T) {
	got := SynthesizeBoundaryAOIs([]state.Boundary{
		{Kind: "obscure-kind", File: "x.go"},
	})
	if len(got) == 0 {
		t.Fatal("unknown kind should still produce a minimum result-discipline AOI")
	}
	if got[0].Category != "error-handling" {
		t.Errorf("fallback should default to error-handling; got %q", got[0].Category)
	}
}

func TestSynthesizeBoundaryAOIs_IDsAreUnique(t *testing.T) {
	got := SynthesizeBoundaryAOIs([]state.Boundary{
		{Kind: "http", File: "a.go", Symbol: "routeA"},
		{Kind: "http", File: "a.go", Symbol: "routeB"},
		{Kind: "queue", File: "a.go", Symbol: "topic1"},
	})
	seen := make(map[string]int, len(got))
	for _, aoi := range got {
		seen[aoi.ID]++
		if seen[aoi.ID] > 1 {
			t.Errorf("duplicate AOI id: %q", aoi.ID)
		}
	}
}

// Regression: two HTTP boundaries in the same file with empty Symbol
// (the LLM couldn't identify route handlers for either) used to
// collide on the file+kind+symbol hash. MergeBoundaryAOIs then
// silently dropped the second boundary's defense AOIs. Lines and
// Description are now mixed into the hash so distinct boundaries
// produce distinct ids even without a Symbol.
func TestSynthesizeBoundaryAOIs_EmptySymbolBoundariesDoNotCollide(t *testing.T) {
	got := SynthesizeBoundaryAOIs([]state.Boundary{
		{Kind: "http", File: "api.go", Lines: "10-30", Description: "POST /users"},
		{Kind: "http", File: "api.go", Lines: "50-70", Description: "GET /users"},
	})
	// Each http boundary expands to 3 defense AOIs → 6 total. If the
	// IDs collide, MergeBoundaryAOIs would dedup down to 3.
	if len(got) != 6 {
		t.Fatalf("got %d AOIs, want 6 (3 per boundary)", len(got))
	}
	seen := make(map[string]struct{}, len(got))
	for _, aoi := range got {
		if _, dup := seen[aoi.ID]; dup {
			t.Errorf("duplicate AOI id across boundaries with empty Symbol: %q", aoi.ID)
		}
		seen[aoi.ID] = struct{}{}
	}
}

// Even more degenerate: two boundaries with empty Symbol AND empty
// Description — distinguished only by Lines. They should still get
// distinct ids.
func TestSynthesizeBoundaryAOIs_DifferentLinesEnoughForUniqueness(t *testing.T) {
	got := SynthesizeBoundaryAOIs([]state.Boundary{
		{Kind: "http", File: "api.go", Lines: "10-30"},
		{Kind: "http", File: "api.go", Lines: "50-70"},
	})
	if len(got) != 6 {
		t.Fatalf("got %d AOIs, want 6", len(got))
	}
}

func TestSynthesizeBoundaryAOIs_IDsAreStable(t *testing.T) {
	// Same boundary produces same id across calls — important for
	// caching and cross-referencing.
	b := state.Boundary{Kind: "http", File: "a.go", Symbol: "createUser"}
	first := SynthesizeBoundaryAOIs([]state.Boundary{b})
	second := SynthesizeBoundaryAOIs([]state.Boundary{b})
	if len(first) != len(second) {
		t.Fatalf("AOI count mismatch: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("AOI[%d] id changed: %q vs %q", i, first[i].ID, second[i].ID)
		}
	}
}

func TestSynthesizeBoundaryAOIs_AppliesLineHints(t *testing.T) {
	got := SynthesizeBoundaryAOIs([]state.Boundary{
		{Kind: "http", File: "a.go", Lines: "42-58"},
	})
	for _, aoi := range got {
		if aoi.Line != 42 {
			t.Errorf("AOI Line = %d, want 42 (from boundary Lines)", aoi.Line)
		}
		if aoi.EndLine != 58 {
			t.Errorf("AOI EndLine = %d, want 58", aoi.EndLine)
		}
	}
}

func TestSynthesizeBoundaryAOIs_MissingLinesDefaultsToOne(t *testing.T) {
	got := SynthesizeBoundaryAOIs([]state.Boundary{
		{Kind: "http", File: "a.go"},
	})
	if got[0].Line != 1 {
		t.Errorf("AOI Line = %d, want 1 (default)", got[0].Line)
	}
	if got[0].EndLine != 0 {
		t.Errorf("AOI EndLine = %d, want 0 (single-line range)", got[0].EndLine)
	}
}

// ── MergeBoundaryAOIs ────────────────────────────────────────────────────

func TestMergeBoundaryAOIs_AppendsToExistingFile(t *testing.T) {
	results := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: []security.AreaOfInterest{{ID: "existing", File: "a.go"}}},
	}
	merged := MergeBoundaryAOIs(results, []security.AreaOfInterest{
		{ID: "boundary-http-authz-aaa", File: "a.go"},
	})
	if len(merged) != 1 {
		t.Fatalf("expected 1 file result, got %d", len(merged))
	}
	if len(merged[0].AreasOfInterest) != 2 {
		t.Errorf("expected 2 AOIs in a.go, got %d", len(merged[0].AreasOfInterest))
	}
}

func TestMergeBoundaryAOIs_CreatesNewFileWhenMissing(t *testing.T) {
	results := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: []security.AreaOfInterest{{ID: "existing", File: "a.go"}}},
	}
	merged := MergeBoundaryAOIs(results, []security.AreaOfInterest{
		{ID: "boundary-http-authz-aaa", File: "b.go"},
	})
	if len(merged) != 2 {
		t.Fatalf("expected 2 file results, got %d", len(merged))
	}
	files := []string{merged[0].File, merged[1].File}
	if !reflect.DeepEqual(files, []string{"a.go", "b.go"}) {
		t.Errorf("file order = %v, want [a.go b.go]", files)
	}
}

func TestMergeBoundaryAOIs_Idempotent(t *testing.T) {
	results := []security.AOIScanResult{}
	bAOIs := []security.AreaOfInterest{{ID: "boundary-http-authz-aaa", File: "a.go"}}
	first := MergeBoundaryAOIs(results, bAOIs)
	second := MergeBoundaryAOIs(first, bAOIs)
	if len(second) != 1 || len(second[0].AreasOfInterest) != 1 {
		t.Errorf("idempotent merge produced %d files / %d AOIs, want 1/1",
			len(second), len(second[0].AreasOfInterest))
	}
}

func TestMergeBoundaryAOIs_EmptyInputUnchanged(t *testing.T) {
	results := []security.AOIScanResult{{File: "a.go"}}
	merged := MergeBoundaryAOIs(results, nil)
	if !reflect.DeepEqual(merged, results) {
		t.Errorf("empty boundary AOIs should not change input")
	}
}

// ── Stub-client integration ─────────────────────────────────────────────

type boundaryStubClient struct {
	response     string
	err          error
	systemPrompt string
	userContent  string
}

func (c *boundaryStubClient) ChatStream(_ context.Context, sys string, msgs []ai.Message, _ func(string)) (string, error) {
	c.systemPrompt = sys
	if len(msgs) > 0 {
		c.userContent = msgs[0].Content
	}
	return c.response, c.err
}

func TestDiscoverBoundaries_ParsesValidResponse(t *testing.T) {
	client := &boundaryStubClient{response: `[
		{"kind": "HTTP", "file": "handler.go", "lines": "10-50", "symbol": "createUser", "description": "POST /users"},
		{"kind": "Queue", "file": "consumer.go", "symbol": "payments-topic", "description": "SNS payments"}
	]`}
	files := map[string]string{
		"handler.go":  "package main\nfunc createUser() {}",
		"consumer.go": "package main\nfunc handleMessage() {}",
	}

	res, err := DiscoverBoundaries(context.Background(), client, files, nil, "", "", nil)
	if err != nil {
		t.Fatalf("DiscoverBoundaries: %v", err)
	}
	if len(res.Boundaries) != 2 {
		t.Fatalf("got %d boundaries, want 2", len(res.Boundaries))
	}
	if res.Boundaries[0].Kind != "http" {
		t.Errorf("kind should be lowercased; got %q", res.Boundaries[0].Kind)
	}
	if res.InputHash == "" {
		t.Error("expected non-empty input hash")
	}
	if !strings.Contains(client.systemPrompt, "externally-reachable surfaces") {
		t.Errorf("system prompt missing expected content")
	}
	if !strings.Contains(client.userContent, "handler.go") {
		t.Errorf("user content missing file path")
	}
}

func TestDiscoverBoundaries_RuntimeModelIncluded(t *testing.T) {
	client := &boundaryStubClient{response: `[]`}
	model := &state.RuntimeModel{AuthModel: "Gateway authorizer"}
	_, err := DiscoverBoundaries(context.Background(), client, map[string]string{"a.go": "x\ny"}, model, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.userContent, "Gateway authorizer") {
		t.Errorf("runtime model not embedded in user message")
	}
}

func TestDiscoverBoundaries_CacheHit(t *testing.T) {
	client := &boundaryStubClient{response: "should not be called"}
	files := map[string]string{"a.go": "body\nline2"}

	excerpts := buildBoundaryExcerpts(files, 80, 200)
	wantHash := hashBoundaryInputs(excerpts, nil, "")

	res, err := DiscoverBoundaries(context.Background(), client, files, nil, "", wantHash, nil)
	if err != nil {
		t.Fatalf("DiscoverBoundaries: %v", err)
	}
	if !res.FromCache {
		t.Error("expected cache hit on matching hash")
	}
	if client.userContent != "" {
		t.Error("expected no LLM call on cache hit")
	}
}

func TestDiscoverBoundaries_EmptyFilesShortCircuits(t *testing.T) {
	client := &boundaryStubClient{response: "should not be called"}
	res, err := DiscoverBoundaries(context.Background(), client, nil, nil, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Boundaries) != 0 {
		t.Errorf("empty input should yield empty inventory; got %d", len(res.Boundaries))
	}
	if client.userContent != "" {
		t.Error("expected no LLM call on empty input")
	}
}

func TestDiscoverBoundaries_LLMError(t *testing.T) {
	client := &boundaryStubClient{err: errors.New("model not found")}
	_, err := DiscoverBoundaries(context.Background(), client,
		map[string]string{"a.go": "body"}, nil, "", "", nil)
	if err == nil {
		t.Fatal("expected LLM error to surface")
	}
}

func TestDiscoverBoundaries_GarbageResponseFails(t *testing.T) {
	client := &boundaryStubClient{response: "I can't determine the boundaries."}
	_, err := DiscoverBoundaries(context.Background(), client,
		map[string]string{"a.go": "body"}, nil, "", "", nil)
	if err == nil {
		t.Fatal("expected parse error on prose-only response")
	}
}

func TestDiscoverBoundaries_NilClient(t *testing.T) {
	_, err := DiscoverBoundaries(context.Background(), nil,
		map[string]string{"a.go": "body"}, nil, "", "", nil)
	if err == nil {
		t.Fatal("expected error on nil client")
	}
}

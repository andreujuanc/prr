package review

import (
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
)

func TestIndividualCacheKey_PromptChangeInvalidates(t *testing.T) {
	aoi := security.AreaOfInterest{
		File: "foo.go", Line: 10,
		Category: "correctness", Concern: "test",
	}

	orig := ai.ReviewIndividualPrompt
	t.Cleanup(func() { ai.ReviewIndividualPrompt = orig })

	ai.ReviewIndividualPrompt = "prompt A"
	keyA := IndividualCacheKey("file body", aoi, nil, "")

	ai.ReviewIndividualPrompt = "prompt B"
	keyB := IndividualCacheKey("file body", aoi, nil, "")

	if keyA == keyB {
		t.Fatalf("expected different cache keys when the prompt changes, got %s for both", keyA)
	}
}

func TestGroupedCacheKey_PromptChangeInvalidates(t *testing.T) {
	aois := []security.AreaOfInterest{
		{File: "a.go", Line: 1, Category: "correctness"},
		{File: "b.go", Line: 2, Category: "correctness"},
	}

	orig := ai.ReviewGroupedPrompt
	t.Cleanup(func() { ai.ReviewGroupedPrompt = orig })

	ai.ReviewGroupedPrompt = "prompt A"
	keyA := GroupedCacheKey(aois, "", nil, "")

	ai.ReviewGroupedPrompt = "prompt B"
	keyB := GroupedCacheKey(aois, "", nil, "")

	if keyA == keyB {
		t.Fatalf("expected different cache keys when the prompt changes, got %s for both", keyA)
	}
}

func TestIndividualCacheKey_AOIChangeInvalidates(t *testing.T) {
	a1 := security.AreaOfInterest{File: "foo.go", Line: 10, Category: "correctness"}
	a2 := security.AreaOfInterest{File: "foo.go", Line: 11, Category: "correctness"}

	if IndividualCacheKey("body", a1, nil, "") == IndividualCacheKey("body", a2, nil, "") {
		t.Fatalf("expected different cache keys when the AOI changes")
	}
}

func TestIndividualCacheKey_FocusChangeInvalidates(t *testing.T) {
	aoi := security.AreaOfInterest{File: "foo.go", Line: 10, Category: "correctness"}

	keyA := IndividualCacheKey("body", aoi, []string{"correctness"}, "")
	keyB := IndividualCacheKey("body", aoi, []string{"security"}, "")

	if keyA == keyB {
		t.Fatalf("expected different cache keys when focus categories change")
	}
}

func TestIndividualCacheKey_FocusOrderStable(t *testing.T) {
	aoi := security.AreaOfInterest{File: "foo.go", Line: 10, Category: "correctness"}

	keyA := IndividualCacheKey("body", aoi, []string{"correctness", "security"}, "")
	keyB := IndividualCacheKey("body", aoi, []string{"security", "correctness"}, "")

	if keyA != keyB {
		t.Fatalf("expected identical keys regardless of focus category order, got %s vs %s", keyA, keyB)
	}
}

func TestGroupedCacheKey_AOIChangeInvalidates(t *testing.T) {
	base := []security.AreaOfInterest{
		{File: "a.go", Line: 1, Category: "correctness"},
		{File: "b.go", Line: 2, Category: "correctness"},
	}
	other := []security.AreaOfInterest{
		{File: "a.go", Line: 1, Category: "correctness"},
		{File: "b.go", Line: 3, Category: "correctness"}, // line differs
	}

	if GroupedCacheKey(base, "", nil, "") == GroupedCacheKey(other, "", nil, "") {
		t.Fatalf("expected different cache keys when any AOI in the group changes")
	}
}

func TestIndividualCacheKey_PriorsHashInvalidates(t *testing.T) {
	aoi := security.AreaOfInterest{File: "foo.go", Line: 10, Category: "correctness"}
	keyA := IndividualCacheKey("body", aoi, nil, "")
	keyB := IndividualCacheKey("body", aoi, nil, "abc123")
	if keyA == keyB {
		t.Fatalf("expected different cache keys when priorsHash differs")
	}
}

func TestIndividualCacheKey_PriorsHashStable(t *testing.T) {
	aoi := security.AreaOfInterest{File: "foo.go", Line: 10, Category: "correctness"}
	keyA := IndividualCacheKey("body", aoi, nil, "abc123")
	keyB := IndividualCacheKey("body", aoi, nil, "abc123")
	if keyA != keyB {
		t.Fatalf("expected identical keys for identical priorsHash, got %s vs %s", keyA, keyB)
	}
}

func TestGroupedCacheKey_PriorsHashInvalidates(t *testing.T) {
	aois := []security.AreaOfInterest{
		{File: "a.go", Line: 1, Category: "correctness"},
	}
	keyA := GroupedCacheKey(aois, "", nil, "")
	keyB := GroupedCacheKey(aois, "", nil, "abc123")
	if keyA == keyB {
		t.Fatalf("expected different cache keys when priorsHash differs")
	}
}

// ── Code-context invalidation ───────────────────────────────────────────

func TestIndividualCacheKey_CodeContextChangeInvalidates(t *testing.T) {
	aoi := security.AreaOfInterest{File: "foo.go", Line: 10, Category: "correctness"}
	keyA := IndividualCacheKey("diff A", aoi, nil, "")
	keyB := IndividualCacheKey("diff B", aoi, nil, "")
	if keyA == keyB {
		t.Fatalf("expected different cache keys when the code context changes")
	}
}

func TestGroupedCacheKey_CodeContextChangeInvalidates(t *testing.T) {
	aois := []security.AreaOfInterest{
		{File: "a.go", Line: 1, Category: "correctness"},
	}
	keyA := GroupedCacheKey(aois, "diff A", nil, "")
	keyB := GroupedCacheKey(aois, "diff B", nil, "")
	if keyA == keyB {
		t.Fatalf("expected different cache keys when the code context changes")
	}
}

// TestCacheKey_Deterministic guards the criteria component (folded into
// the key via scopedCriteria, which uses an internal map) against
// accidental nondeterminism — a flaky key would silently disable the
// review cache.
func TestCacheKey_Deterministic(t *testing.T) {
	aoi := security.AreaOfInterest{
		File: "billing/charge.go", Line: 45,
		Category: "financial", Subcategory: "money-arithmetic",
	}
	for i := 0; i < 5; i++ {
		if got := IndividualCacheKey("body", aoi, nil, ""); got != IndividualCacheKey("body", aoi, nil, "") {
			t.Fatalf("individual key not deterministic on iter %d: %s", i, got)
		}
	}
	aois := []security.AreaOfInterest{
		{File: "a.go", Line: 1, Category: "financial", Subcategory: "money-arithmetic"},
		{File: "b.go", Line: 2, Category: "web-security", Subcategory: "csrf-protection"},
	}
	for i := 0; i < 5; i++ {
		if got := GroupedCacheKey(aois, "body", nil, ""); got != GroupedCacheKey(aois, "body", nil, "") {
			t.Fatalf("grouped key not deterministic on iter %d: %s", i, got)
		}
	}
}

func TestComputeCacheKey_DiffChangeInvalidates(t *testing.T) {
	aoi := security.AreaOfInterest{File: "a.go", Line: 1, Category: "correctness"}
	callA := ReviewCall{
		Type:      "individual",
		AOIs:      []security.AreaOfInterest{aoi},
		Files:     []string{"a.go"},
		FileDiffs: map[string]string{"a.go": "diff A"},
	}
	callB := callA
	callB.FileDiffs = map[string]string{"a.go": "diff B"}

	if ComputeCacheKey(callA, nil, "") == ComputeCacheKey(callB, nil, "") {
		t.Fatalf("expected different cache keys when the file diff changes")
	}
}

func TestComputeCacheKey_AuditContextChangeInvalidates(t *testing.T) {
	aoi := security.AreaOfInterest{File: "a.go", Line: 1, Category: "correctness"}
	callA := ReviewCall{
		Type:       "individual",
		AOIs:       []security.AreaOfInterest{aoi},
		Files:      []string{"a.go"},
		AOISources: []string{"source A"},
	}
	callB := callA
	callB.AOISources = []string{"source B"}

	if ComputeCacheKey(callA, nil, "") == ComputeCacheKey(callB, nil, "") {
		t.Fatalf("expected different cache keys when the AOI source context changes")
	}
}

func TestCodeContextDigest_DeterministicAcrossFileDiffMapOrder(t *testing.T) {
	// Map iteration order is random; ensure the digest is stable.
	call := ReviewCall{
		FileDiffs: map[string]string{
			"a.go": "diff A",
			"b.go": "diff B",
			"c.go": "diff C",
		},
	}
	first := codeContextDigest(call)
	for i := 0; i < 20; i++ {
		if got := codeContextDigest(call); got != first {
			t.Fatalf("digest should be stable across iterations; got mismatch")
		}
	}
}

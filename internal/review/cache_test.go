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
	keyA := GroupedCacheKey(aois, nil, "")

	ai.ReviewGroupedPrompt = "prompt B"
	keyB := GroupedCacheKey(aois, nil, "")

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
		t.Fatalf("expected different cache keys when focus dimensions change")
	}
}

func TestIndividualCacheKey_FocusOrderStable(t *testing.T) {
	aoi := security.AreaOfInterest{File: "foo.go", Line: 10, Category: "correctness"}

	keyA := IndividualCacheKey("body", aoi, []string{"correctness", "security"}, "")
	keyB := IndividualCacheKey("body", aoi, []string{"security", "correctness"}, "")

	if keyA != keyB {
		t.Fatalf("expected identical keys regardless of focus dimension order, got %s vs %s", keyA, keyB)
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

	if GroupedCacheKey(base, nil, "") == GroupedCacheKey(other, nil, "") {
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
	keyA := GroupedCacheKey(aois, nil, "")
	keyB := GroupedCacheKey(aois, nil, "abc123")
	if keyA == keyB {
		t.Fatalf("expected different cache keys when priorsHash differs")
	}
}

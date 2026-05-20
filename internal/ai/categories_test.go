package ai

import (
	"testing"
)

func TestDimensionsLoaded(t *testing.T) {
	slugs := AllCategorySlugs()
	const expected = 21
	if len(slugs) != expected {
		t.Errorf("expected %d dimensions, got %d: %v", expected, len(slugs), slugs)
	}

	// Spot-check a few — including the newer observability and web-security dimensions
	for _, slug := range []string{"authentication", "correctness", "design", "performance", "testing", "cross-cutting", "observability", "web-security"} {
		if !CategoryExists(slug) {
			t.Errorf("dimension %q not found", slug)
		}
		content := GetCategory(slug)
		if len(content) < 100 {
			t.Errorf("dimension %q too short (%d bytes)", slug, len(content))
		}
	}
}

func TestGetCategories(t *testing.T) {
	result := GetCategories([]string{"authentication", "correctness"})
	if len(result) < 200 {
		t.Errorf("combined dimensions too short: %d bytes", len(result))
	}
	// Should contain content from both
	if !containsStr(result, "authentication") {
		t.Error("missing authentication content")
	}
	if !containsStr(result, "correctness") {
		t.Error("missing correctness content")
	}
}

func TestGetCategoriesSkipsUnknown(t *testing.T) {
	result := GetCategories([]string{"authentication", "nonexistent", "correctness"})
	if containsStr(result, "nonexistent") {
		t.Error("should skip unknown slugs")
	}
	if !containsStr(result, "authentication") {
		t.Error("should include known slugs")
	}
}

func TestAllCategories(t *testing.T) {
	all := AllCategories()
	if len(all) < 1000 {
		t.Errorf("all categorys too short: %d bytes", len(all))
	}
}

func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && contains(s, substr)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

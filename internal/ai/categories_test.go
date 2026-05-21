package ai

import (
	"strings"
	"testing"
)

func TestCategoriesLoaded(t *testing.T) {
	slugs := AllCategorySlugs()
	const expected = 21
	if len(slugs) != expected {
		t.Errorf("expected %d categories, got %d: %v", expected, len(slugs), slugs)
	}

	// Spot-check a few — including the newer observability and web-security categories
	for _, slug := range []string{"authentication", "correctness", "design", "performance", "testing", "cross-cutting", "observability", "web-security"} {
		if !CategoryExists(slug) {
			t.Errorf("category %q not found", slug)
		}
		content := GetCategory(slug)
		if len(content) < 100 {
			t.Errorf("category %q too short (%d bytes)", slug, len(content))
		}
	}
}

func TestGetCategories(t *testing.T) {
	result := GetCategories([]string{"authentication", "correctness"})
	if len(result) < 200 {
		t.Errorf("combined categories too short: %d bytes", len(result))
	}
	if !strings.Contains(result, "authentication") {
		t.Error("missing authentication content")
	}
	if !strings.Contains(result, "correctness") {
		t.Error("missing correctness content")
	}
}

func TestGetCategoriesSkipsUnknown(t *testing.T) {
	result := GetCategories([]string{"authentication", "nonexistent", "correctness"})
	if strings.Contains(result, "nonexistent") {
		t.Error("should skip unknown slugs")
	}
	if !strings.Contains(result, "authentication") {
		t.Error("should include known slugs")
	}
}

func TestAllCategories(t *testing.T) {
	all := AllCategories()
	if len(all) < 1000 {
		t.Errorf("all categories too short: %d bytes", len(all))
	}
}

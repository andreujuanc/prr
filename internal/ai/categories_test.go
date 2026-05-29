package ai

import (
	"strings"
	"testing"
)

func TestCategoriesLoaded(t *testing.T) {
	slugs := AllCategorySlugs()
	const expected = 23
	if len(slugs) != expected {
		t.Errorf("expected %d categories, got %d: %v", expected, len(slugs), slugs)
	}

	// Spot-check a few — including the newer observability and web-security categories
	for _, slug := range []string{"authentication", "correctness", "design", "performance", "testing", "cross-cutting", "observability", "web-security", "ai-slop"} {
		if !CategoryExists(slug) {
			t.Errorf("category %q not found", slug)
		}
		content := GetCategoryShapes([]string{slug})
		if len(content) < 100 {
			t.Errorf("category %q shapes too short (%d bytes)", slug, len(content))
		}
	}
}

func TestGetCategoryShapes(t *testing.T) {
	result := GetCategoryShapes([]string{"authentication", "correctness"})
	if len(result) < 200 {
		t.Errorf("combined category shapes too short: %d bytes", len(result))
	}
	if !strings.Contains(result, "authentication") {
		t.Error("missing authentication content")
	}
	if !strings.Contains(result, "correctness") {
		t.Error("missing correctness content")
	}
}

func TestGetCategoryShapesSkipsUnknown(t *testing.T) {
	result := GetCategoryShapes([]string{"authentication", "nonexistent", "correctness"})
	if strings.Contains(result, "nonexistent") {
		t.Error("should skip unknown slugs")
	}
	if !strings.Contains(result, "authentication") {
		t.Error("should include known slugs")
	}
}

func TestAllCategoryShapes(t *testing.T) {
	all := AllCategoryShapes()
	if len(all) < 1000 {
		t.Errorf("all category shapes too short: %d bytes", len(all))
	}
	// Shapes must not carry the deep-review-only Review criteria section.
	if strings.Contains(all, "## Review criteria") {
		t.Error("AllCategoryShapes should not include the Review criteria section")
	}
}

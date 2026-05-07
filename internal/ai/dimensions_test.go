package ai

import (
	"testing"
)

func TestDimensionsLoaded(t *testing.T) {
	slugs := AllDimensionSlugs()
	if len(slugs) != 19 {
		t.Errorf("expected 19 dimensions, got %d: %v", len(slugs), slugs)
	}

	// Spot-check a few
	for _, slug := range []string{"authentication", "correctness", "design", "performance", "testing", "cross-cutting"} {
		if !DimensionExists(slug) {
			t.Errorf("dimension %q not found", slug)
		}
		content := GetDimension(slug)
		if len(content) < 100 {
			t.Errorf("dimension %q too short (%d bytes)", slug, len(content))
		}
	}
}

func TestGetDimensions(t *testing.T) {
	result := GetDimensions([]string{"authentication", "correctness"})
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

func TestGetDimensionsSkipsUnknown(t *testing.T) {
	result := GetDimensions([]string{"authentication", "nonexistent", "correctness"})
	if containsStr(result, "nonexistent") {
		t.Error("should skip unknown slugs")
	}
	if !containsStr(result, "authentication") {
		t.Error("should include known slugs")
	}
}

func TestAllDimensions(t *testing.T) {
	all := AllDimensions()
	if len(all) < 1000 {
		t.Errorf("all dimensions too short: %d bytes", len(all))
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

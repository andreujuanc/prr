package ai

import (
	"strings"
	"testing"
)

// These tests drive the section/subcategory parser directly with fixture
// strings, independent of the embedded category files, so they pin the
// extraction contract regardless of how the real files evolve.

const bothSectionsFixture = `### EXAMPLE (category: "example")

One-paragraph description.

## Shapes or common patterns

**alpha** — first shape:
- shape bullet a1
- shape bullet a2

**beta** — second shape:
- shape bullet b1

## Review criteria

**alpha**:
- judge alpha
- defense for alpha

**beta**:
- judge beta
`

func TestSplitSectionsBoth(t *testing.T) {
	shapes, review, migrated := splitSections(bothSectionsFixture)
	if !migrated {
		t.Fatal("expected migrated=true for a file with a Shapes heading")
	}
	if !contains(shapes, "shape bullet a1") || !contains(shapes, "shape bullet b1") {
		t.Errorf("shapes missing bullets: %q", shapes)
	}
	if contains(shapes, "judge alpha") {
		t.Error("shapes leaked Review criteria content")
	}
	if !contains(review, "judge alpha") || !contains(review, "judge beta") {
		t.Errorf("review missing content: %q", review)
	}
	if contains(review, "shape bullet") {
		t.Error("review leaked Shapes content")
	}
}

func TestSplitSectionsShapesOnly(t *testing.T) {
	fixture := `### X (category: "x")

desc

## Shapes or common patterns

**only** — sole shape:
- bullet
`
	shapes, review, migrated := splitSections(fixture)
	if !migrated {
		t.Fatal("expected migrated=true")
	}
	if !contains(shapes, "bullet") {
		t.Errorf("shapes missing: %q", shapes)
	}
	if review != "" {
		t.Errorf("expected empty review, got %q", review)
	}
}

func TestSplitSectionsReviewOnly(t *testing.T) {
	fixture := `### X (category: "x")

desc

## Review criteria

**only**:
- judge it
`
	shapes, review, migrated := splitSections(fixture)
	if !migrated {
		t.Fatal("a file with a Review heading but no Shapes heading is still migrated")
	}
	if shapes != "" {
		t.Errorf("expected empty shapes, got %q", shapes)
	}
	if !contains(review, "judge it") {
		t.Errorf("review missing: %q", review)
	}
}

func TestSplitSectionsUnmigratedFallback(t *testing.T) {
	fixture := `### LEGACY (category: "legacy")

desc

#### Subcategories

**foo** — legacy shape:
- legacy bullet
`
	shapes, review, migrated := splitSections(fixture)
	if migrated {
		t.Fatal("a file with no Shapes heading must report migrated=false")
	}
	if !contains(shapes, "legacy bullet") || !contains(shapes, "#### Subcategories") {
		t.Errorf("unmigrated fallback should pass the whole post-header body as shapes: %q", shapes)
	}
	if contains(shapes, "### LEGACY") {
		t.Error("category header line should be stripped from fallback shapes")
	}
	if review != "" {
		t.Errorf("expected empty review for unmigrated file, got %q", review)
	}
}

func TestHeadingPrefixTolerance(t *testing.T) {
	// "## Shapes" (terse) and "## Review criteria or verdict guidance"
	// (extended) must both be recognized by prefix match.
	fixture := `### X (category: "x")

desc

## Shapes

**a** — s:
- sb

## Review criteria or verdict guidance

**a**:
- rb
`
	shapes, review, migrated := splitSections(fixture)
	if !migrated || !contains(shapes, "sb") || !contains(review, "rb") {
		t.Errorf("prefix-match failed: migrated=%v shapes=%q review=%q", migrated, shapes, review)
	}
}

func TestExtractSubcat(t *testing.T) {
	shapes, review, _ := splitSections(bothSectionsFixture)

	alpha := extractSubcat(shapes, "alpha")
	if !contains(alpha, "shape bullet a1") || !contains(alpha, "shape bullet a2") {
		t.Errorf("alpha shape block wrong: %q", alpha)
	}
	if contains(alpha, "shape bullet b1") {
		t.Error("alpha block bled into beta")
	}

	if got := extractSubcat(shapes, "missing"); got != "" {
		t.Errorf("missing subcat should return empty, got %q", got)
	}
	if got := extractSubcat("", "alpha"); got != "" {
		t.Errorf("empty section should return empty, got %q", got)
	}

	betaReview := extractSubcat(review, "beta")
	if !contains(betaReview, "judge beta") {
		t.Errorf("beta review block wrong: %q", betaReview)
	}
}

func TestGetShapeAndReviewCriteria(t *testing.T) {
	// Against the real embedded files: input-validation has an injection
	// subcategory in Shapes; Review criteria is empty post-migration.
	shape := GetShape("input-validation", "injection")
	if !contains(shape, "injection") {
		t.Errorf("expected injection shape, got %q", shape)
	}
	if got := GetReviewCriteria("input-validation", "injection"); got != "" {
		t.Errorf("expected empty review criteria during migration, got %q", got)
	}
	if got := GetShape("nonexistent", "x"); got != "" {
		t.Errorf("unknown category should return empty, got %q", got)
	}
}

// TestParsedCategoriesMatchSplit guards against init drift: every slug's
// precomputed parsedCategory must equal a fresh parse of its raw content.
func TestParsedCategoriesMatchSplit(t *testing.T) {
	for _, slug := range AllCategorySlugs() {
		pc, ok := parsedCategories[slug]
		if !ok {
			t.Errorf("%s: missing from parsedCategories", slug)
			continue
		}
		want := parseCategory(categories[slug])
		if pc != want {
			t.Errorf("%s: parsedCategories entry differs from fresh parse", slug)
		}
	}
}

// TestGetCategoryShapesUnmigratedFallback pins the defensive behavior: a
// category whose file lacks a `## Shapes` heading surfaces its whole body
// (header included) rather than vanishing. Driven via parsedCategories so
// it exercises the real GetCategoryShapes path without touching disk.
func TestGetCategoryShapesUnmigratedFallback(t *testing.T) {
	const slug = "__test_unmigrated__"
	const raw = `### LEGACY (category: "legacy")

desc

#### Subcategories

**gamma** — a pattern:
- gamma bullet`

	categories[slug] = raw
	parsedCategories[slug] = parseCategory(raw)
	categoryOrder = append(categoryOrder, slug)
	defer func() {
		delete(categories, slug)
		delete(parsedCategories, slug)
		categoryOrder = categoryOrder[:len(categoryOrder)-1]
	}()

	out := GetCategoryShapes([]string{slug})
	if !contains(out, "gamma bullet") {
		t.Errorf("unmigrated fallback dropped patterns: %q", out)
	}
	if !contains(out, "### LEGACY") {
		t.Errorf("unmigrated fallback should pass through the whole body incl. header: %q", out)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

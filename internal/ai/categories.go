package ai

import (
	"embed"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/state"
)

// categoryFS embeds all category partial files. Each file defines a
// single review category (subcategories + patterns) that gets composed
// at runtime into review and AOI prompts.
//
//go:embed prompts/categories/*.md
var categoryFS embed.FS

// categoryDir is the directory within the embed.FS.
const categoryDir = "prompts/categories"

// categories caches loaded category content keyed by slug (filename
// without .md). Populated on init.
var categories map[string]string

// categoryOrder is the canonical ordering of category slugs. Populated
// on init.
var categoryOrder []string

func init() {
	entries, err := categoryFS.ReadDir(categoryDir)
	if err != nil {
		panic(fmt.Sprintf("failed to read embedded categories directory: %v", err))
	}

	categories = make(map[string]string, len(entries))
	categoryOrder = make([]string, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		slug := strings.TrimSuffix(entry.Name(), ".md")
		content, err := categoryFS.ReadFile(filepath.Join(categoryDir, entry.Name()))
		if err != nil {
			panic(fmt.Sprintf("failed to read embedded category %s: %v", entry.Name(), err))
		}

		categories[slug] = strings.TrimSpace(string(content))
		categoryOrder = append(categoryOrder, slug)
	}

	sort.Strings(categoryOrder)

	state.SetCategoryValidator(CategoryExists)
}

// AllCategorySlugs returns the slugs of all available categories in
// alphabetical order.
func AllCategorySlugs() []string {
	result := make([]string, len(categoryOrder))
	copy(result, categoryOrder)
	return result
}

// CategoryExists returns true if the given slug corresponds to a
// loaded category.
func CategoryExists(slug string) bool {
	_, ok := categories[slug]
	return ok
}

// ── Shapes / Review-criteria split ──
//
// Each category file is split into a `## Shapes or common patterns`
// section (pattern lists consumed by the recall-biased AOI scan) and a
// `## Review criteria` section (verdict guidance consumed by the
// precision-biased deep review). The funcs below address each section,
// and individual subcategories within them, independently.
//
// As a defensive fallback, a file with no `## Shapes` heading is treated
// as Shapes-only (whole post-header body), so a future category that
// forgets the heading still surfaces in the AOI scan rather than
// vanishing silently.

const (
	shapesHeadingPrefix = "## Shapes"
	reviewHeadingPrefix = "## Review criteria"
)

// splitSections returns the Shapes body and Review-criteria body for a
// category's raw content. The category header + description (everything
// before the first `## ` section heading) is dropped — callers that want
// it use categoryHeader. Prefix-match on headings so trailing prose
// ("or common patterns") can change without breaking extraction.
//
// Unmigrated files (no Shapes heading) return the full post-header body
// as shapes and "" for review.
func splitSections(content string) (shapes, review string, migrated bool) {
	lines := strings.Split(content, "\n")
	var cur *strings.Builder
	var sb, rb strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, shapesHeadingPrefix):
			cur = &sb
			migrated = true
			continue
		case strings.HasPrefix(trimmed, reviewHeadingPrefix):
			cur = &rb
			migrated = true
			continue
		}
		if cur != nil {
			cur.WriteString(line)
			cur.WriteString("\n")
		}
	}
	if !migrated {
		// Whole body (minus the `### CATEGORY` header line) is Shapes.
		return stripCategoryHeader(content), "", false
	}
	return strings.TrimSpace(sb.String()), strings.TrimSpace(rb.String()), true
}

// stripCategoryHeader drops the leading `### CATEGORY` line so unmigrated
// content matches the post-header body that splitSections produces for
// migrated files.
func stripCategoryHeader(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "### ") {
		return strings.TrimSpace(strings.Join(lines[1:], "\n"))
	}
	return strings.TrimSpace(content)
}

// categoryHeader returns the `### CATEGORY` line + description (everything
// before the first `## ` section), so the Shapes prompt keeps the category
// title and one-paragraph framing it has today.
func categoryHeader(content string) string {
	lines := strings.Split(content, "\n")
	var hb strings.Builder
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			break
		}
		hb.WriteString(line)
		hb.WriteString("\n")
	}
	return strings.TrimSpace(hb.String())
}

// extractSubcat returns the `**slug**` bold block within a section body,
// from its `**slug**` line up to the next `**` block or end. Returns ""
// if not found.
func extractSubcat(section, subcat string) string {
	if section == "" || subcat == "" {
		return ""
	}
	lines := strings.Split(section, "\n")
	marker := "**" + subcat + "**"
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), marker) {
			start = i
			break
		}
	}
	if start == -1 {
		return ""
	}
	var bb strings.Builder
	for i := start; i < len(lines); i++ {
		if i > start && strings.HasPrefix(strings.TrimSpace(lines[i]), "**") {
			break
		}
		bb.WriteString(lines[i])
		bb.WriteString("\n")
	}
	return strings.TrimSpace(bb.String())
}

// GetCategoryShapes returns the Shapes sections (header + Shapes body) for
// the listed categories, concatenated. Unknown slugs are skipped with a
// log line.
func GetCategoryShapes(slugs []string) string {
	var parts []string
	for _, slug := range slugs {
		content, ok := categories[slug]
		if !ok {
			log.Printf("ai.GetCategoryShapes: unknown category slug %q — skipping", slug)
			continue
		}
		shapes, _, migrated := splitSections(content)
		if !migrated {
			// Defensive: a file missing the Shapes heading passes through
			// whole so its patterns still reach the AOI scan.
			parts = append(parts, content)
			continue
		}
		header := categoryHeader(content)
		parts = append(parts, strings.TrimSpace(header+"\n\n"+shapes))
	}
	return strings.Join(parts, "\n\n")
}

// AllCategoryShapes returns the Shapes sections across all categories in
// alphabetical order. Used by the AOI scan.
func AllCategoryShapes() string {
	return GetCategoryShapes(categoryOrder)
}

// GetShape returns one subcategory's block within a category's Shapes
// section. Returns "" if missing.
func GetShape(category, subcategory string) string {
	content, ok := categories[category]
	if !ok {
		return ""
	}
	shapes, _, _ := splitSections(content)
	return extractSubcat(shapes, subcategory)
}

// GetReviewCriteria returns one subcategory's block within a category's
// Review criteria section. Returns "" if missing (most subcategories
// during migration).
func GetReviewCriteria(category, subcategory string) string {
	content, ok := categories[category]
	if !ok {
		return ""
	}
	_, review, _ := splitSections(content)
	return extractSubcat(review, subcategory)
}

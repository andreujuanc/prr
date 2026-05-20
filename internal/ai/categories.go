package ai

import (
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
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
}

// GetCategory returns the content of a single category by slug.
// Returns empty string if the slug doesn't exist.
func GetCategory(slug string) string {
	return categories[slug]
}

// GetCategories returns the content of multiple categories concatenated,
// separated by double newlines. Unknown slugs are silently skipped.
func GetCategories(slugs []string) string {
	var parts []string
	for _, slug := range slugs {
		if content, ok := categories[slug]; ok {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// AllCategories returns all category content concatenated in
// alphabetical order, separated by double newlines.
func AllCategories() string {
	return GetCategories(categoryOrder)
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

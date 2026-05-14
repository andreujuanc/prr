package ai

import (
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// dimensionFS embeds all dimension partial files. Each file defines a single
// review dimension (category + subcategories + patterns). These are composed
// at runtime into review and AOI prompts.
//
//go:embed prompts/dimensions/*.md
var dimensionFS embed.FS

// dimensionDir is the directory within the embed.FS.
const dimensionDir = "prompts/dimensions"

// dimensions caches loaded dimension content keyed by slug (filename without .md).
// Populated on init.
var dimensions map[string]string

// dimensionOrder is the canonical ordering of dimension slugs.
// Populated on init.
var dimensionOrder []string

func init() {
	entries, err := dimensionFS.ReadDir(dimensionDir)
	if err != nil {
		panic(fmt.Sprintf("failed to read embedded dimensions directory: %v", err))
	}

	dimensions = make(map[string]string, len(entries))
	dimensionOrder = make([]string, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		slug := strings.TrimSuffix(entry.Name(), ".md")
		content, err := dimensionFS.ReadFile(filepath.Join(dimensionDir, entry.Name()))
		if err != nil {
			panic(fmt.Sprintf("failed to read embedded dimension %s: %v", entry.Name(), err))
		}

		dimensions[slug] = strings.TrimSpace(string(content))
		dimensionOrder = append(dimensionOrder, slug)
	}

	sort.Strings(dimensionOrder)
}

// GetDimension returns the content of a single dimension by slug.
// Returns empty string if the slug doesn't exist.
func GetDimension(slug string) string {
	return dimensions[slug]
}

// GetDimensions returns the content of multiple dimensions concatenated,
// separated by double newlines. Unknown slugs are silently skipped.
func GetDimensions(slugs []string) string {
	var parts []string
	for _, slug := range slugs {
		if content, ok := dimensions[slug]; ok {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// AllDimensions returns all dimension content concatenated in alphabetical
// order, separated by double newlines.
func AllDimensions() string {
	return GetDimensions(dimensionOrder)
}

// AllDimensionSlugs returns the slugs of all available dimensions in
// alphabetical order.
func AllDimensionSlugs() []string {
	result := make([]string, len(dimensionOrder))
	copy(result, dimensionOrder)
	return result
}

// DimensionExists returns true if the given slug corresponds to a loaded dimension.
func DimensionExists(slug string) bool {
	_, ok := dimensions[slug]
	return ok
}

package review

import "fmt"

// ReviewMode controls how the PR review pipeline routes files between
// the AOI deep review path and the blanket diff review (fallback)
// path. Values are an enum so adding modes later or flipping the
// default is a non-breaking change at the flag level.
type ReviewMode string

const (
	// ReviewModeFull reviews every file: AOI-flagged files go through
	// the AOI deep review path, files without AOIs go through the
	// fallback diff batches. This is the historical behavior and the
	// current default.
	ReviewModeFull ReviewMode = "full"

	// ReviewModeAOIOnly reviews only files with AOIs. Files without
	// AOIs are skipped entirely — no fallback batches. Trusts the
	// AOI scan as the sole signal for what is worth deep-reviewing.
	ReviewModeAOIOnly ReviewMode = "aoi-only"
)

// defaultReviewMode is the mode used when the user does not pass
// --review-mode. Keeping the default in a single named variable means
// flipping it later is a one-line code change rather than a breaking
// CLI flag rename.
var defaultReviewMode = ReviewModeFull

// ParseReviewMode validates and normalises a mode value from the CLI.
// Empty string returns the default. Unknown values return an error
// with the list of valid modes.
func ParseReviewMode(s string) (ReviewMode, error) {
	if s == "" {
		return defaultReviewMode, nil
	}
	switch ReviewMode(s) {
	case ReviewModeFull, ReviewModeAOIOnly:
		return ReviewMode(s), nil
	default:
		return "", fmt.Errorf("invalid review mode %q (valid: %s, %s)",
			s, ReviewModeFull, ReviewModeAOIOnly)
	}
}

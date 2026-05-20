package review

import (
	"encoding/json"

	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/state"
)

// MarshalResultJSON returns the JSON byte payload for a PR review.
// Shared between the headless `prr review` flow (--output flag and
// auto-persisted snapshot) and the TUI's snapshot save. Putting the
// shape here in the review package keeps "what a review snapshot
// looks like on disk" in one place rather than scattering it across
// callers.
//
// Inputs are taken individually (rather than a *PRReviewResult)
// because the TUI doesn't construct a PRReviewResult — it has the
// pieces from CoreResult. Both call sites end up sending the same
// shape, so the function takes the components.
//
// pr may be nil — Number and Title fields are omitted in that case.
func MarshalResultJSON(
	pr *git.PullRequest,
	filesReviewed int,
	structured *state.ReviewOutput,
	deepFindings []state.DeepFinding,
) ([]byte, error) {
	data := map[string]any{
		"files_reviewed": filesReviewed,
	}
	if pr != nil {
		data["pr_number"] = pr.Number
		data["pr_title"] = pr.Title
	}
	if structured != nil {
		data["review"] = structured
	}
	if len(deepFindings) > 0 {
		data["deep_findings"] = deepFindings
	}
	return json.MarshalIndent(data, "", "  ")
}

package security

import "github.com/andreujuanc/prr/internal/state"

// AreaOfInterest represents a code location identified by the AOI scanner
// that warrants deeper review. Each AOI is tagged with a category/subcategory
// from the review category taxonomy and an urgency level that controls
// how it is reviewed in Phase 3.
type AreaOfInterest struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"` // optional: range end

	// Category + Subcategory from the category taxonomy (e.g. "error-handling" / "swallowed-errors").
	Category    string `json:"category"`
	Subcategory string `json:"subcategory,omitempty"`

	// Urgency controls Phase 3 routing:
	//   "individual" — gets a dedicated deep review call
	//   "grouped"    — batched with other AOIs in the same subcategory
	// Empty string treated as "grouped" for backward compatibility.
	Urgency string `json:"urgency,omitempty"`

	// Categories lists every category this AOI touches beyond the
	// primary Category above (e.g. ["correctness", "error-handling"]).
	// Used for --focus filtering at Phase 3.
	Categories []string `json:"categories,omitempty"`

	// ID is a stable identifier for this AOI (e.g. "charge-go-float-currency").
	// Used for caching and cross-referencing between phases.
	ID string `json:"id,omitempty"`

	// Concern is a brief description of what the AOI scanner flagged.
	Concern string `json:"concern,omitempty"`

	// Context provides additional information about why this location matters.
	Context string `json:"context,omitempty"`

	// SiblingDeviation is set when this AOI was synthesized by Phase
	// 2.5 sibling clustering — it identifies the AOI as a 1-of-N
	// outlier and carries the conforming siblings so Phase 3 can
	// frame the review around "is this deviation intentional?". Nil
	// for AOIs produced by the regular scanner or by boundary
	// synthesis.
	SiblingDeviation *state.SiblingDeviation `json:"sibling_deviation,omitempty"`

	// Legacy fields — kept for backward compatibility with existing cached results.
	Snippet    string `json:"snippet,omitempty"`
	Reasoning  string `json:"reasoning,omitempty"`
	Confidence string `json:"confidence,omitempty"` // "high", "medium", "low"
}

// AOIScanResult holds the output of an AOI scan for a single file.
// Supports both the legacy format (areas_of_interest with snippets) and
// the new format (areas with category/subcategory/urgency).
type AOIScanResult struct {
	File            string           `json:"file"`
	AreasOfInterest []AreaOfInterest `json:"areas_of_interest"`

	// Areas is the new-format field name. During parsing, if "areas" is present
	// in the JSON it takes precedence over "areas_of_interest".
	Areas []AreaOfInterest `json:"areas,omitempty"`
}

// NormalizeAOIs merges Areas into AreasOfInterest (if Areas is populated)
// so downstream code can always read AreasOfInterest. Call after unmarshaling.
func (r *AOIScanResult) NormalizeAOIs() {
	if len(r.Areas) > 0 {
		r.AreasOfInterest = r.Areas
		r.Areas = nil
	}
}

// AOIReport is the complete result of scanning all changed files.
type AOIReport struct {
	Files          []AOIScanResult `json:"files"`
	TotalAOIs      int             `json:"total_aois"`
	SecurityDigest string          `json:"-"` // formatted text for injection into review prompts
}


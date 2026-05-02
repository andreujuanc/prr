package state

import (
	"sync"
)

// ReviewStatus represents the current review state of a file
type ReviewStatus string

const (
	StatusUnreviewed ReviewStatus = "unreviewed"
	StatusReviewed   ReviewStatus = "reviewed"
	StatusModified   ReviewStatus = "modified" // Represents a file that was reviewed but has new changes
)

// Message represents a single chat message in an AI conversation
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ── Structured review output ────────────────────────────────────────────

// ReviewOutput is the structured JSON output from a PR review.
// Both single-pass and multi-pass synthesis produce this format.
type ReviewOutput struct {
	Summary            string          `json:"summary"`
	Verdict            string          `json:"verdict"` // "approve", "request_changes", "comment"
	Findings           []ReviewFinding `json:"findings"`
	MissingTests       []string        `json:"missing_tests"`
	QuestionsForAuthor []string        `json:"questions_for_author"`
}

// ReviewFinding is a single finding from the structured review.
type ReviewFinding struct {
	Severity   string `json:"severity"` // "critical", "high", "medium", "low", "nit"
	Category   string `json:"category"` // "bug", "security", "performance", "testing", "style", "architecture", "docs"
	File       string `json:"file"`
	Line       int    `json:"line"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Suggestion string `json:"suggestion,omitempty"`
	Resolved   bool   `json:"resolved,omitempty"` // user-toggled or auto-resolved by task completion
}

// SeverityRank returns a numeric rank for sorting findings by severity
// (lower = more severe).
func (f ReviewFinding) SeverityRank() int {
	switch f.Severity {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	case "nit":
		return 4
	default:
		return 5
	}
}

// ── Legacy review storage ───────────────────────────────────────────────

// AIReview stores the result of an AI review for a file or the overall PR.
type AIReview struct {
	Summary  string `json:"summary"`            // rendered final review text (legacy: free-form markdown)
	Findings string `json:"findings,omitempty"` // per-batch raw findings (PR-level only)

	// Structured review output — populated by Phase 5+ review flows.
	// When present, the TUI renders this instead of the legacy Summary field.
	Structured *ReviewOutput `json:"structured,omitempty"`

	// DiffSnapshot records the DiffHash of each file at the time the review
	// was generated. Used to detect staleness when diffs change after a review.
	DiffSnapshot map[string]string `json:"diff_snapshot,omitempty"`
}

// FileState holds the review status and chat history for a specific file
type FileState struct {
	Status        ReviewStatus `json:"status"`
	DiffHash      string       `json:"diff_hash"`
	Chat          []Message    `json:"chat,omitempty"`
	Purpose       string       `json:"purpose,omitempty"`        // AI-generated description of what the file does
	BatchFindings string       `json:"batch_findings,omitempty"` // cached findings from PR-level batch review
}

// State represents the persisted review state for a single pull request
type State struct {
	mu sync.RWMutex

	PRNumber   string                `json:"pr_number"`
	GlobalChat []Message             `json:"global_chat,omitempty"`
	Review     *AIReview             `json:"review,omitempty"` // PR-level AI review
	Files      map[string]*FileState `json:"files"`
}

// NewState initializes a new empty state object for a PR
func NewState(prNumber string) *State {
	return &State{
		PRNumber: prNumber,
		Files:    make(map[string]*FileState),
	}
}

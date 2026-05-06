package state

import (
	"encoding/json"
	"fmt"
	"strings"
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
	CWE        string `json:"cwe,omitempty"`      // e.g. "CWE-89" — populated for security findings
	Resolved   bool   `json:"resolved,omitempty"` // user-toggled or auto-resolved by task completion

	// Revalidation — populated by the security revalidation pass (Phase 4).
	Revalidation *FindingRevalidation `json:"revalidation,omitempty"`
}

// FindingRevalidation holds the result of a security revalidation pass.
type FindingRevalidation struct {
	Verdict    string `json:"verdict"`    // "true-positive", "false-positive", "fixed", "uncertain"
	Reasoning  string `json:"reasoning"`
	Confidence string `json:"confidence"` // "high", "medium", "low"
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

	// SecurityDigest is the AOI pre-scan summary injected into review prompts.
	// Persisted so the TUI can display it even after the review is cached.
	SecurityDigest string `json:"security_digest,omitempty"`
}

// FileState holds the review status and chat history for a specific file
type FileState struct {
	Status           ReviewStatus    `json:"status"`
	DiffHash         string          `json:"diff_hash"`
	Chat             []Message       `json:"chat,omitempty"`
	Purpose          string          `json:"purpose,omitempty"`           // AI-generated description of what the file does
	BatchFindings    string          `json:"batch_findings,omitempty"`    // cached findings from PR-level batch review
	AOIResults       json.RawMessage `json:"aoi_results,omitempty"`      // cached AOI scan result (AOIScanResult JSON)
	AOIContextLines  int             `json:"aoi_context_lines,omitempty"` // context lines used when AOI was generated
}

// State represents the persisted review state for a single pull request
type State struct {
	mu sync.RWMutex

	PRNumber           string                `json:"pr_number"`
	GlobalChat         []Message             `json:"global_chat,omitempty"`
	Review             *AIReview             `json:"review,omitempty"` // PR-level AI review
	Files              map[string]*FileState `json:"files"`
	ProjectContext     string                `json:"project_context,omitempty"`      // cached project briefing
	ProjectContextHash string                `json:"project_context_hash,omitempty"` // hash of inputs used to generate it
}

// NewState initializes a new empty state object for a PR
func NewState(prNumber string) *State {
	return &State{
		PRNumber: prNumber,
		Files:    make(map[string]*FileState),
	}
}

// ── Thread-safe field accessors ─────────────────────────────────────────
// These must be used by background goroutines (review, AOI scan) instead of
// directly mutating FileState fields, because the Bubble Tea main loop reads
// the same fields for rendering.

// SetAOIResults stores AOI scan results for a file along with the context
// lines used to generate them. Creates the FileState if it doesn't exist.
func (s *State) SetAOIResults(path string, data json.RawMessage, contextLines int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs, ok := s.Files[path]
	if !ok {
		fs = &FileState{Status: StatusUnreviewed}
		s.Files[path] = fs
	}
	fs.AOIResults = data
	fs.AOIContextLines = contextLines
}

// GetAOIResults returns the cached AOI results for a file, or nil.
// Also returns the context lines used when the results were generated.
func (s *State) GetAOIResults(path string) (json.RawMessage, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if fs, ok := s.Files[path]; ok {
		return fs.AOIResults, fs.AOIContextLines
	}
	return nil, 0
}

// SetBatchFindings stores the batch review purpose and findings for a file.
func (s *State) SetBatchFindings(path, purpose, findings string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs, ok := s.Files[path]
	if !ok {
		fs = &FileState{Status: StatusUnreviewed}
		s.Files[path] = fs
	}
	fs.Purpose = purpose
	fs.BatchFindings = findings
}

// GetBatchFindings returns the cached purpose and findings for a file.
func (s *State) GetBatchFindings(path string) (purpose, findings string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if fs, ok := s.Files[path]; ok {
		return fs.Purpose, fs.BatchFindings
	}
	return "", ""
}

// ClearAllCaches clears all per-file cached data (batch findings, AOI results)
// and the PR-level review. Used by forceReReview.
func (s *State) ClearAllCaches() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, fs := range s.Files {
		fs.BatchFindings = ""
		fs.Purpose = ""
		fs.AOIResults = nil
		fs.AOIContextLines = 0
	}
	s.Review = nil
}

// HasCachedBatch reports whether all files in the given paths have cached findings.
func (s *State) HasCachedBatch(paths []string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range paths {
		fs, ok := s.Files[p]
		if !ok || fs.Purpose == "" {
			return false
		}
	}
	return true
}

// CollectCachedFindings reassembles per-file findings from cache.
func (s *State) CollectCachedFindings(paths []string) (combined string, fileFindings map[string]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var sb strings.Builder
	fileFindings = make(map[string]string)
	for _, f := range paths {
		fs := s.Files[f]
		if fs != nil && fs.BatchFindings != "" {
			sb.WriteString(fmt.Sprintf("### %s\nPurpose: %s\n%s\n\n", f, fs.Purpose, fs.BatchFindings))
			fileFindings[f] = fs.BatchFindings
		}
	}
	return sb.String(), fileFindings
}

// HasFile reports whether a file exists in the state.
func (s *State) HasFile(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.Files[path]
	return ok
}

// SetProjectContext stores a cached project context and its input hash.
func (s *State) SetProjectContext(summary, inputHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ProjectContext = summary
	s.ProjectContextHash = inputHash
}

// GetProjectContext returns the cached project context and its input hash.
func (s *State) GetProjectContext() (summary, inputHash string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ProjectContext, s.ProjectContextHash
}

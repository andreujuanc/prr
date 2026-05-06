package security

// AreaOfInterest represents a security-sensitive code location identified
// by the lightweight AOI scanner. These are injected into the main review
// prompts so the AI knows where to apply extra scrutiny.
type AreaOfInterest struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	EndLine    int    `json:"end_line,omitempty"` // optional: range end
	Category   string `json:"category"`           // e.g. "user-input", "sql", "exec", "auth", "crypto", "secrets", "deserialization", "file-access", "network", "redirect"
	Snippet    string `json:"snippet"`            // the relevant code fragment
	Reasoning  string `json:"reasoning"`          // why this is security-sensitive
	Confidence string `json:"confidence"`         // "high", "medium", "low"
}

// AOIScanResult holds the output of an AOI scan for a single file.
type AOIScanResult struct {
	File            string           `json:"file"`
	AreasOfInterest []AreaOfInterest `json:"areas_of_interest"`
	RiskLevel       string           `json:"risk_level"` // "critical", "high", "medium", "low", "none"
	RiskSummary     string           `json:"risk_summary"`
}

// AOIReport is the complete result of scanning all changed files.
type AOIReport struct {
	Files          []AOIScanResult `json:"files"`
	OverallRisk    string          `json:"overall_risk"` // highest risk across all files
	TotalAOIs      int             `json:"total_aois"`
	HighRiskFiles  []string        `json:"high_risk_files"` // files rated critical or high
	SecurityDigest string          `json:"-"`               // formatted text for injection into review prompts
}

// Revalidation holds the result of a security revalidation pass on a finding.
type Revalidation struct {
	Verdict    string `json:"verdict"`    // "true-positive", "false-positive", "fixed", "uncertain"
	Reasoning  string `json:"reasoning"`  // why this verdict was chosen
	Confidence string `json:"confidence"` // "high", "medium", "low"
	CWE        string `json:"cwe,omitempty"`
}

// SecuritySummary aggregates security metrics for a PR review.
type SecuritySummary struct {
	TotalFindings    int            `json:"total_findings"`
	BySeverity       map[string]int `json:"by_severity"`       // critical/high/medium/low counts
	ByCWE            map[string]int `json:"by_cwe,omitempty"`  // CWE-ID -> count
	HighRiskFiles    []string       `json:"high_risk_files"`   // files with critical/high findings
	AOIsCovered      int            `json:"aois_covered"`      // how many AOIs led to findings
	AOIsTotal        int            `json:"aois_total"`        // total AOIs identified
	RevalidatedCount int            `json:"revalidated_count"` // findings that were revalidated
	TruePositives    int            `json:"true_positives"`    // confirmed true positives
	FalsePositives   int            `json:"false_positives"`   // confirmed false positives
}

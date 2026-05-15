package state

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
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
	Severity string `json:"severity"` // "critical", "high", "medium", "low", "nit"

	// Confidence is the legacy string band ("high"|"medium"|"low") kept
	// for backward compatibility with cached state and older prompts.
	// New code should read ConfidenceScore; render via ConfidenceBand().
	Confidence string `json:"confidence,omitempty"`

	// ConfidenceScore is the 0-100 certainty that the finding is real,
	// independent of its severity. Severity = "how bad if real";
	// ConfidenceScore = "how sure I am it's real". 0 means unknown
	// (e.g., a finding loaded from legacy state that only had the
	// string band).
	ConfidenceScore int `json:"confidence_score,omitempty"`

	// ConfidenceReasoning is a short one-line justification for the
	// score. Concrete signals like "missing-trace" or
	// "defenses-not-checked" are appended by downstream validators
	// (see commits 4 + 5 in the audit-quality plan).
	ConfidenceReasoning string `json:"confidence_reasoning,omitempty"`

	Category   string `json:"category"` // "bug", "security", "performance", "testing", "style", "architecture", "docs"
	File       string `json:"file"`
	Line       int    `json:"line"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Suggestion string `json:"suggestion,omitempty"`
	CWE        string `json:"cwe,omitempty"`      // e.g. "CWE-89" — populated for security findings
	Resolved   bool   `json:"resolved,omitempty"` // user-toggled or auto-resolved by task completion

	// SourceIDs lists the deep-finding IDs (e.g. "F-001", "F-007") this
	// synthesis finding derives from. One synthesis finding can cite
	// multiple deep findings when synthesis consolidates a systemic
	// pattern across files. Consumers should dereference these against
	// the top-level `deep_findings` list for evidence / trigger / per-
	// site file:line ranges — synthesis findings keep only a single
	// representative file/line and a short narrative.
	//
	// Empty/omitted when synthesis ran without deep findings as input
	// (single-pass review path).
	SourceIDs []string `json:"source_ids,omitempty"`

	// Revalidation — populated by the security revalidation pass (Phase 4).
	Revalidation *FindingRevalidation `json:"revalidation,omitempty"`
}

// RuntimeModel captures the codebase's runtime shape so Phase 3 review
// can ground findings in the project's actual entry points, validation
// sites, and error-handling discipline. Produced once per audit by
// Phase 0.5 and injected verbatim into every Phase 3 prompt.
//
// The fields are intentionally narrow and prose-based — they encode
// the same answers a senior engineer would give a new hire on their
// first day. Downstream review prompts reference these answers when
// judging whether a finding traces through the runtime model or
// contradicts it.
type RuntimeModel struct {
	// AuthModel describes who guards what — gateway authorizers,
	// middleware, in-handler checks. 1-2 sentences.
	AuthModel string `json:"auth_model,omitempty"`

	// ValidationSites lists where user/network input gets validated
	// before reaching business logic. Short strings, one per layer
	// or location.
	ValidationSites []string `json:"validation_sites,omitempty"`

	// EntryPoints enumerates the externally-reachable surfaces. Each
	// entry classifies its kind, retry model, batching model, and
	// where validation happens. Empty list means "no entry points
	// identified" (could be a library/CLI-only repo).
	EntryPoints []RuntimeEntryPoint `json:"entry_points,omitempty"`

	// ResultDiscipline describes how errors propagate — Result types,
	// exception handling, error wrapping, sentinel values. One line.
	ResultDiscipline string `json:"result_discipline,omitempty"`

	// Invariants lists the load-bearing assumptions the codebase
	// relies on but doesn't necessarily enforce in every call site
	// (e.g., "all IDs are UUID v4", "amounts are stored in minor
	// units", "the inbox table is append-only"). Short statements.
	Invariants []string `json:"invariants,omitempty"`
}

// RuntimeEntryPoint describes one externally-reachable surface.
type RuntimeEntryPoint struct {
	// Kind classifies the surface: "http", "queue", "scheduled",
	// "cli", "rpc", "webhook", "other".
	Kind string `json:"kind"`

	// RetryModel describes who retries on failure and what triggers
	// retry (e.g., "API Gateway does not retry — caller's job",
	// "SNS retries with exponential backoff per record").
	RetryModel string `json:"retry_model,omitempty"`

	// BatchModel describes batching: single-record vs. batched, and
	// whether one bad record fails the batch.
	BatchModel string `json:"batch_model,omitempty"`

	// ValidationAt is one of "boundary", "handler", "both", "none".
	// Where in the call chain inputs get validated.
	ValidationAt string `json:"validation_at,omitempty"`
}

// Boundary is one concrete externally-reachable surface located in a
// specific file. It is the persisted output of Phase 1.5 (boundary
// discovery). Each boundary seeds 1-3 defense-coverage AOIs for
// Phase 3 so the audit can guarantee at least one review pass at
// every boundary regardless of what the AOI scanner caught on its
// own.
//
// Boundary differs from RuntimeEntryPoint: RuntimeEntryPoint
// describes the codebase's *classes* of entry points abstractly
// ("HTTP routes use schema validation at the boundary"); Boundary
// names a specific surface at a specific path so review can be
// targeted.
type Boundary struct {
	// Kind matches RuntimeEntryPoint.Kind. One of: "http", "queue",
	// "scheduled", "cli", "rpc", "webhook", "other".
	Kind string `json:"kind"`

	// File is the path holding the boundary declaration (e.g. the
	// route file, queue subscription, scheduled job).
	File string `json:"file"`

	// Lines is the optional line range hint within File. Best-effort
	// from the LLM's read of the file header.
	Lines string `json:"lines,omitempty"`

	// Symbol is the boundary's identifier (route name, handler
	// function, subscription topic) when one is identifiable. Used
	// to anchor the synthesized AOIs to specific code.
	Symbol string `json:"symbol,omitempty"`

	// Description is a one-line free-form explanation: "POST
	// /admin/users — admin creation handler", "SNS subscription to
	// payment-events topic", "scheduled daily reconciliation".
	Description string `json:"description"`
}

// IsZero reports whether the model carries no information.
func (m *RuntimeModel) IsZero() bool {
	if m == nil {
		return true
	}
	return m.AuthModel == "" &&
		len(m.ValidationSites) == 0 &&
		len(m.EntryPoints) == 0 &&
		m.ResultDiscipline == "" &&
		len(m.Invariants) == 0
}

// Render formats the model for injection into a Phase 3 prompt under a
// `## Runtime Model` section. Returns the empty string when the model
// is zero so callers can skip the section entirely.
//
// The output is compact (well under 1KB on a typical repo) so it
// doesn't crowd out the rest of the prompt. Each field gets a labeled
// line; empty fields are omitted.
func (m *RuntimeModel) Render() string {
	if m.IsZero() {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Runtime Model\n\n")
	b.WriteString("A finding that contradicts the runtime model is a strong signal of a bug. ")
	b.WriteString("A finding that doesn't trace through the runtime model is a strong false-positive candidate.\n\n")

	if m.AuthModel != "" {
		b.WriteString("**Auth model:** ")
		b.WriteString(strings.TrimSpace(m.AuthModel))
		b.WriteString("\n\n")
	}
	if len(m.ValidationSites) > 0 {
		b.WriteString("**Validation sites:**\n")
		for _, s := range m.ValidationSites {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s\n", s)
		}
		b.WriteString("\n")
	}
	if len(m.EntryPoints) > 0 {
		b.WriteString("**Entry points:**\n")
		for _, ep := range m.EntryPoints {
			kind := strings.TrimSpace(ep.Kind)
			if kind == "" {
				kind = "other"
			}
			fmt.Fprintf(&b, "- `%s`", kind)
			if v := strings.TrimSpace(ep.ValidationAt); v != "" {
				fmt.Fprintf(&b, " — validation at %s", v)
			}
			if r := strings.TrimSpace(ep.RetryModel); r != "" {
				fmt.Fprintf(&b, "; retries: %s", r)
			}
			if bm := strings.TrimSpace(ep.BatchModel); bm != "" {
				fmt.Fprintf(&b, "; batching: %s", bm)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if m.ResultDiscipline != "" {
		b.WriteString("**Result discipline:** ")
		b.WriteString(strings.TrimSpace(m.ResultDiscipline))
		b.WriteString("\n\n")
	}
	if len(m.Invariants) > 0 {
		b.WriteString("**Invariants:**\n")
		for _, inv := range m.Invariants {
			inv = strings.TrimSpace(inv)
			if inv == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s\n", inv)
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// FindingRevalidation holds the result of a security revalidation pass.
type FindingRevalidation struct {
	Verdict    string `json:"verdict"` // "true-positive", "false-positive", "fixed", "uncertain"
	Reasoning  string `json:"reasoning"`
	Confidence string `json:"confidence"` // "high", "medium", "low"
}

// ConfidenceBand returns a coarse band derived from ConfidenceScore for
// UIs that still want a "high"|"medium"|"low" label. When
// ConfidenceScore is zero (unknown), the legacy Confidence string is
// returned so old cached findings still render with a band.
//
// Bands: >=80 high, 50-79 medium, <50 low.
func (f ReviewFinding) ConfidenceBand() string {
	if f.ConfidenceScore == 0 {
		return f.Confidence
	}
	switch {
	case f.ConfidenceScore >= 80:
		return "high"
	case f.ConfidenceScore >= 50:
		return "medium"
	default:
		return "low"
	}
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

	// DeepFindings from AOI-driven review calls (structured findings with severity, category, etc.)
	DeepFindings []DeepFinding `json:"deep_findings,omitempty"`
}

// FileState holds the review status and chat history for a specific file
type FileState struct {
	Status          ReviewStatus    `json:"status"`
	DiffHash        string          `json:"diff_hash"`
	Chat            []Message       `json:"chat,omitempty"`
	Purpose         string          `json:"purpose,omitempty"`           // AI-generated description of what the file does
	BatchFindings   string          `json:"batch_findings,omitempty"`    // cached findings from PR-level batch review
	AOIResults      json.RawMessage `json:"aoi_results,omitempty"`       // cached AOI scan result (AOIScanResult JSON)
	AOIContextLines int             `json:"aoi_context_lines,omitempty"` // context lines used when AOI was generated
	FileType        string          `json:"file_type,omitempty"`         // cached file classification (e.g. "handler", "test")
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
	PRBrief            string                `json:"pr_brief,omitempty"`             // cached PR-specific briefing (comments, prior reviews, CI)
	PRBriefHash        string                `json:"pr_brief_hash,omitempty"`        // hash of inputs used to generate the PR brief

	// RuntimeModel is the structured codebase shape produced by Phase
	// 0.5 — auth model, validation sites, entry points, result
	// discipline, invariants. Injected into every Phase 3 prompt so
	// the reviewer can ground findings in "what this codebase looks
	// like at runtime". RuntimeModelHash carries the hash of inputs
	// used to produce it.
	RuntimeModel     *RuntimeModel `json:"runtime_model,omitempty"`
	RuntimeModelHash string        `json:"runtime_model_hash,omitempty"`

	// BoundaryInventory is the Phase 1.5 list of externally-reachable
	// surfaces (HTTP routes, queue consumers, schedulers, storage
	// triggers, CAS-shaped DB writes). Each entry seeds 1-3 defense-
	// coverage AOIs synthesized before Phase 3 so every boundary is
	// guaranteed to be reviewed for the standard defense questions
	// (schema validation, error handling, authorization, per-record
	// isolation, result discipline).
	BoundaryInventory     []Boundary `json:"boundary_inventory,omitempty"`
	BoundaryInventoryHash string     `json:"boundary_inventory_hash,omitempty"`

	// DeepReviews caches Phase 3 deep review results. Keyed by a hash of the
	// review inputs (file content + AOI content + focus dimensions for individual;
	// all AOI content + focus dimensions for grouped).
	DeepReviews map[string]*DeepReviewResult `json:"deep_reviews,omitempty"`

	// RecheckCache caches Phase 3b recheck output by hash of the input
	// findings + project context + mode. The value is a serialized
	// []DeepFinding (the cleaned, deduplicated set).
	RecheckCache map[string]json.RawMessage `json:"recheck_cache,omitempty"`

	// SynthesisCache caches Phase 4 synthesis output by hash of the input
	// findings + cross-cutting + project context. The value is a serialized
	// SynthesisResult (audit-package type, opaque to this package).
	SynthesisCache map[string]json.RawMessage `json:"synthesis_cache,omitempty"`

	// DeepFindings is the top-level persisted list of Phase 1 + Phase 1c
	// findings, independent of the synthesized Review object. Populated
	// incrementally as each batch completes so a crash, cancellation, or
	// failed-synthesis still leaves the user with their findings on
	// reopen. The Review tab reads this when state.Review is nil — which
	// is the default in TUI mode where synthesis is skipped.
	DeepFindings []DeepFinding `json:"deep_findings,omitempty"`

	// RecheckDismissals records every finding the Phase 3b recheck pass
	// removed, along with the LLM's rationale. Persisted so the audit
	// report can show users WHY a finding disappeared, and so prompt
	// tuning can be measured (compare dismissal counts and rationales
	// across runs). Replaces the previous "DismissedCount only" output,
	// which routed rationales to log.Printf and lost them.
	RecheckDismissals []DismissedRecord `json:"recheck_dismissals,omitempty"`

	// LastReview records that a review run completed against this PR,
	// regardless of whether it produced any findings. Without this,
	// "review ran and found nothing" is indistinguishable from "no
	// review has ever been attempted" — both leave Review/DeepFindings
	// empty and force the TUI to fall back to "no review yet" prompts.
	//
	// Set by the pipeline at the end of a successful run (or partial
	// run that produced any artifacts). Cleared by ClearForFreshReview
	// when the user forces a re-review.
	LastReview *ReviewMeta `json:"last_review,omitempty"`
}

// ReviewMeta is the proof-of-run record stored on State.LastReview.
// It lets the TUI distinguish:
//
//   - "review ran, found N findings"          (LastReview != nil, FindingsCount > 0)
//   - "review ran, clean PR"                  (LastReview != nil, FindingsCount == 0, Error == "")
//   - "review attempted, failed mid-flight"   (LastReview != nil, Error != "")
//   - "no review has been run yet"            (LastReview == nil)
//
// The pipeline is responsible for populating success metadata; the
// TUI is responsible for stamping the Error field on AIChatDoneMsg
// failure paths so the next session shows what went wrong instead of
// "no review yet."
type ReviewMeta struct {
	// CompletedAt is the wall-clock time the run finished.
	CompletedAt time.Time `json:"completed_at"`

	// Verdict is the structured-review verdict (or a synthesised
	// "approve"/"comment" when synthesis was skipped).
	// Empty for failure runs (use Error to disambiguate).
	Verdict string `json:"verdict,omitempty"`

	// Summary is a one-line description for the Review tab when no
	// per-finding renderer is available (e.g. clean PR).
	Summary string `json:"summary,omitempty"`

	// FindingsCount is the number of findings that survived recheck
	// (i.e. the length of DeepFindings or Review.Structured.Findings
	// at end of run). Zero is a valid value — clean PR.
	FindingsCount int `json:"findings_count"`

	// DismissedCount is the number of findings recheck removed. Useful
	// when the user wants to confirm "the model did look at things and
	// decide they were fine."
	DismissedCount int `json:"dismissed_count"`

	// Error captures a failure message when the run aborted partway
	// through (e.g. ">20% of AOI calls failed", upstream HTTP error,
	// JSON parse error). Empty on successful runs. Persisted so the
	// user can see what failed on reopen rather than losing the
	// context after the TUI exits.
	Error string `json:"error,omitempty"`
}

// DismissedRecord is one finding that recheck removed, with the
// rationale the model gave for removing it. The original finding is
// kept so the report can render it inline next to the rationale —
// users need to see what got dismissed, not just that something did.
type DismissedRecord struct {
	FindingID string      `json:"finding_id"`
	Finding   DeepFinding `json:"finding"`
	Rationale string      `json:"rationale"`
}

// DeepReviewResult stores the cached output of a Phase 3 review call.
type DeepReviewResult struct {
	// Type is "individual" or "grouped".
	Type string `json:"type"`

	// CacheKey is the hash used to look up this result.
	CacheKey string `json:"cache_key"`

	// Category and Subcategory identify the concern area.
	Category    string `json:"category"`
	Subcategory string `json:"subcategory,omitempty"`

	// RawOutput is the LLM's JSON response (unparsed for flexibility).
	RawOutput json.RawMessage `json:"raw_output"`

	// Findings extracted from the LLM output.
	Findings []DeepFinding `json:"findings,omitempty"`

	// Dismissals extracted from the LLM output.
	Dismissals []DeepDismissal `json:"dismissals,omitempty"`

	// CrossCutting observation (grouped reviews only).
	CrossCutting string `json:"cross_cutting,omitempty"`
}

// FindingTrigger is the concrete scenario that exercises a finding.
// Repro is the input or request to send (e.g., a curl command, a
// payload, an API call). Observable is what the caller sees when the
// bug fires (status code, returned value, side effect). Both are
// short strings — the prompt asks for the smallest concrete thing
// that distinguishes "real bug" from "theoretical concern".
type FindingTrigger struct {
	Repro      string `json:"repro,omitempty"`
	Observable string `json:"observable,omitempty"`
}

// IsZero reports whether the trigger carries no information.
func (t FindingTrigger) IsZero() bool {
	return t.Repro == "" && t.Observable == ""
}

// UnmarshalJSON accepts either the structured object form or a legacy
// string (which becomes Repro with Observable empty). Older cached
// state and the previous prompt schema both produced strings; the
// new prompt schema produces objects.
func (t *FindingTrigger) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		t.Repro = s
		return nil
	}
	type alias FindingTrigger
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*t = FindingTrigger(a)
	return nil
}

// TraceHop is one step of the end-to-end trace the reviewer walked
// before flagging a finding. The 3-hop minimum (suspect line →
// caller → boundary) is enforced for critical/high severity by the
// confidence-penalty rule in applyConfidencePenalties.
//
// Role is one of:
//   - "suspect"  — the cited line itself; the alleged bug location.
//   - "caller"   — the immediate function/handler that invokes the
//     suspect code.
//   - "boundary" — the next system boundary the value reaches
//     (transport: HTTP response, RPC reply, message send;
//     persistence: any write that may CAS, versioned column, or
//     conditional update; trust: input from network, file, env,
//     message body). The runtime model from Phase 0.5 anchors which
//     boundaries exist in this codebase.
//
// Findings can include additional hops between caller and boundary
// when the data flow passes through layered helpers; the validator
// only requires that at least 3 hops are present.
type TraceHop struct {
	Role     string `json:"role"`               // "suspect" | "caller" | "boundary" | free-form
	File     string `json:"file,omitempty"`     // path of the hop's file
	Lines    string `json:"lines,omitempty"`    // line range within the file
	Evidence string `json:"evidence,omitempty"` // 1-line summary of what was confirmed at this hop
}

// DeepFinding is a confirmed issue from Phase 3 review.
type DeepFinding struct {
	FindingID   string `json:"finding_id,omitempty"` // assigned before recheck (e.g. "F-001")
	AOIID       string `json:"aoi_id"`
	File        string `json:"file"`
	Lines       string `json:"lines"`
	Severity    string `json:"severity"` // "critical", "high", "medium", "low", "nit"
	Category    string `json:"category"`
	Subcategory string `json:"subcategory,omitempty"`
	Dimension   string `json:"dimension"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Evidence    string `json:"evidence,omitempty"` // what was verified and found (tool-backed)
	// EvidenceSnippet is 1-3 verbatim lines from File at Lines that
	// prove the finding. Phase 3's prompt requires it, and the
	// in-loop verifier matches this against the file before the
	// finding is accepted — anchoring every claim to text that
	// actually exists at the cited location. Findings whose snippet
	// doesn't match get one refinement round trip and, if still
	// unmatched, are dropped.
	EvidenceSnippet string         `json:"evidence_snippet,omitempty"`
	Trigger         FindingTrigger `json:"trigger"`
	Suggestion      string         `json:"suggestion,omitempty"`

	// ConfidenceScore (0-100) is the model's certainty that the
	// finding is real, independent of severity. Downstream validators
	// (commits 4 + 5 in the audit-quality plan) subtract from this
	// score when required evidence is missing without touching
	// severity. 0 means unknown (legacy data).
	ConfidenceScore int `json:"confidence_score,omitempty"`

	// ConfidenceReasoning is a short justification. Validators append
	// concrete tags (e.g., "missing-trace", "defenses-not-checked")
	// so the reviewer can see why the score moved.
	ConfidenceReasoning string `json:"confidence_reasoning,omitempty"`

	// Trace is the end-to-end path the reviewer walked from the
	// suspect line to the next system boundary. The Phase 3 prompt
	// asks for at least three hops for findings at severity
	// critical/high so a snippet-in-isolation flag can't survive at
	// severe severity without the reviewer showing their work.
	// Findings at lower severity (medium/low/nit) don't require a
	// trace.
	Trace []TraceHop `json:"trace,omitempty"`

	// DefensesChecked lists the canonical defense layers the
	// reviewer inspected before flagging this finding. Each entry
	// is a short tag from a fixed vocabulary the Phase 3 prompts
	// enumerate (boundary-authz, handler-guard, conditional-write,
	// idempotency-key, schema-validation, framework-escape,
	// result-discipline, native-limit) or a free-form
	// `other:<tag>` for cases outside the list.
	//
	// For findings whose category is in the "required" set
	// (authorization, concurrency, input-validation, external-io),
	// an empty list triggers a confidence penalty in
	// ApplyConfidencePenalties — the reviewer didn't show which
	// defenses they ruled out, so we can't trust the finding as
	// fully as one that does. Other categories leave this
	// optional.
	DefensesChecked []string `json:"defenses_checked,omitempty"`

	// Systemic is set by the recheck parser when a finding came out
	// of the `consolidated` bucket — i.e. it represents a cross-file
	// pattern merged from multiple per-file findings, not an
	// individual issue. The two-pass recheck pipeline uses this to
	// route systemic findings around the per-file dismiss pass
	// (their context is the multi-file aggregation, not any single
	// file). Was previously inferred via an `isSystemic` heuristic
	// (File=="multiple" / Title prefix); the flag is authoritative
	// because the producer sets it explicitly.
	Systemic bool `json:"systemic,omitempty"`
}

// DeepDismissal is a dismissed AOI from Phase 3 review.
type DeepDismissal struct {
	AOIID     string `json:"aoi_id"`
	Evidence  string `json:"evidence,omitempty"`
	Rationale string `json:"rationale"`
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

// SetFileType stores the classification type for a file.
func (s *State) SetFileType(path, fileType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs, ok := s.Files[path]
	if !ok {
		fs = &FileState{Status: StatusUnreviewed}
		s.Files[path] = fs
	}
	fs.FileType = fileType
}

// GetFileType returns the cached classification type for a file, or empty string.
func (s *State) GetFileType(path string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if fs, ok := s.Files[path]; ok {
		return fs.FileType
	}
	return ""
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
		fs.FileType = ""
	}
	s.Review = nil
	s.DeepReviews = nil
	s.DeepFindings = nil
	s.RecheckDismissals = nil
	s.RecheckCache = nil
	s.SynthesisCache = nil
	s.ProjectContext = ""
	s.ProjectContextHash = ""
	s.PRBrief = ""
	s.PRBriefHash = ""
	s.RuntimeModel = nil
	s.RuntimeModelHash = ""
	s.BoundaryInventory = nil
	s.BoundaryInventoryHash = ""
	s.LastReview = nil
}

// SetLastReview records that a review run completed against this PR,
// regardless of whether it produced findings. Pipeline calls this at
// end-of-run; the TUI reads it to distinguish "review ran (maybe
// nothing found)" from "never reviewed."
//
// Pass nil to clear (e.g. fresh re-review). Otherwise CompletedAt is
// auto-stamped if the caller leaves it zero.
func (s *State) SetLastReview(meta *ReviewMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if meta != nil && meta.CompletedAt.IsZero() {
		meta.CompletedAt = time.Now()
	}
	s.LastReview = meta
}

// HasReviewArtifact returns true when this PR's state shows any
// evidence that a review has been attempted — a synthesized Review,
// persisted deep findings, OR a LastReview marker. The marker is what
// makes "clean PR" honest: zero findings is a result, not absence.
func (s *State) HasReviewArtifact() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Review != nil {
		return true
	}
	if len(s.DeepFindings) > 0 {
		return true
	}
	return s.LastReview != nil
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

// CountCachedBatchFindings returns how many of the given paths have a
// non-empty BatchFindings entry in state. Holds the read lock for the
// entire iteration so the count is consistent against concurrent
// writers — callers that build their own loops via HasFile + direct
// Files map access would race.
func (s *State) CountCachedBatchFindings(paths []string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, p := range paths {
		if fs, ok := s.Files[p]; ok && fs != nil && fs.BatchFindings != "" {
			count++
		}
	}
	return count
}

// SetDeepFindings replaces the persisted top-level deep findings.
// Used by the pipeline at recheck boundaries (replace) and on load
// migration. For incremental append during Phase 1, use AppendDeepFindings.
func (s *State) SetDeepFindings(findings []DeepFinding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DeepFindings = append([]DeepFinding(nil), findings...)
}

// AppendDeepFindings adds findings to the top-level list. Holds the
// write lock for the whole append so concurrent batch goroutines don't
// drop entries via read-then-write races.
func (s *State) AppendDeepFindings(findings []DeepFinding) {
	if len(findings) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DeepFindings = append(s.DeepFindings, findings...)
}

// GetDeepFindings returns a copy of the persisted deep findings.
// Returning a copy keeps callers from racing with concurrent writers.
func (s *State) GetDeepFindings() []DeepFinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.DeepFindings) == 0 {
		return nil
	}
	out := make([]DeepFinding, len(s.DeepFindings))
	copy(out, s.DeepFindings)
	return out
}

// ClearDeepFindings empties the persisted list. Used when the pipeline
// starts a fresh review run and wants to discard stale incremental
// findings before accumulating new ones.
func (s *State) ClearDeepFindings() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DeepFindings = nil
}

// SetRecheckDismissals replaces the persisted dismissal log. Called
// once per recheck run with the full list — recheck dismissals are
// produced as a batch, not incrementally, so a full overwrite is the
// right shape (vs. AppendDeepFindings which streams during Phase 3).
func (s *State) SetRecheckDismissals(dismissals []DismissedRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(dismissals) == 0 {
		s.RecheckDismissals = nil
		return
	}
	s.RecheckDismissals = append([]DismissedRecord(nil), dismissals...)
}

// GetRecheckDismissals returns a copy of the persisted dismissal log.
func (s *State) GetRecheckDismissals() []DismissedRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.RecheckDismissals) == 0 {
		return nil
	}
	out := make([]DismissedRecord, len(s.RecheckDismissals))
	copy(out, s.RecheckDismissals)
	return out
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

// SetPRBrief stores a cached PR-specific briefing (summary of comments,
// prior AI reviews, CI status) and its input hash. Mirrors the
// SetProjectContext API so callers in internal/prcontext can use it the
// same way internal/project uses ProjectContext.
func (s *State) SetPRBrief(brief, inputHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PRBrief = brief
	s.PRBriefHash = inputHash
}

// GetPRBrief returns the cached PR brief and its input hash.
func (s *State) GetPRBrief() (brief, inputHash string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.PRBrief, s.PRBriefHash
}

// ClearPRBrief invalidates the cached PR brief. Use when external state
// (e.g. the prior AI review) changes in a way that should force
// regeneration even if the input hash hasn't moved.
func (s *State) ClearPRBrief() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PRBrief = ""
	s.PRBriefHash = ""
}

// SetRuntimeModel stores the discovered runtime model and the hash of
// the inputs used to produce it. Mirrors SetProjectContext.
func (s *State) SetRuntimeModel(model *RuntimeModel, inputHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RuntimeModel = model
	s.RuntimeModelHash = inputHash
}

// GetRuntimeModel returns the cached runtime model and its input hash.
// The returned pointer is the live one held by State; treat it as
// read-only.
func (s *State) GetRuntimeModel() (model *RuntimeModel, inputHash string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.RuntimeModel, s.RuntimeModelHash
}

// SetBoundaryInventory stores the Phase 1.5 boundary inventory and
// its input hash.
func (s *State) SetBoundaryInventory(boundaries []Boundary, inputHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BoundaryInventory = boundaries
	s.BoundaryInventoryHash = inputHash
}

// GetBoundaryInventory returns the cached boundary inventory and its
// input hash. The returned slice is the live one held by State;
// treat it as read-only.
func (s *State) GetBoundaryInventory() (boundaries []Boundary, inputHash string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.BoundaryInventory, s.BoundaryInventoryHash
}

// SetDeepReview stores a Phase 3 deep review result by cache key.
func (s *State) SetDeepReview(key string, result *DeepReviewResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DeepReviews == nil {
		s.DeepReviews = make(map[string]*DeepReviewResult)
	}
	result.CacheKey = key
	s.DeepReviews[key] = result
}

// GetDeepReview returns a cached Phase 3 result by key, or nil.
func (s *State) GetDeepReview(key string) *DeepReviewResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.DeepReviews == nil {
		return nil
	}
	return s.DeepReviews[key]
}

// SetRecheckCache stores a recheck output (serialized JSON) by cache key.
func (s *State) SetRecheckCache(key string, raw json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RecheckCache == nil {
		s.RecheckCache = make(map[string]json.RawMessage)
	}
	s.RecheckCache[key] = raw
}

// GetRecheckCache returns a cached recheck result by key, or nil.
func (s *State) GetRecheckCache(key string) json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.RecheckCache == nil {
		return nil
	}
	return s.RecheckCache[key]
}

// SetSynthesisCache stores a synthesis output (serialized JSON) by cache key.
func (s *State) SetSynthesisCache(key string, raw json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SynthesisCache == nil {
		s.SynthesisCache = make(map[string]json.RawMessage)
	}
	s.SynthesisCache[key] = raw
}

// GetSynthesisCache returns a cached synthesis result by key, or nil.
func (s *State) GetSynthesisCache(key string) json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.SynthesisCache == nil {
		return nil
	}
	return s.SynthesisCache[key]
}

// ClearDeepReviews removes all cached Phase 3 results.
func (s *State) ClearDeepReviews() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DeepReviews = nil
}

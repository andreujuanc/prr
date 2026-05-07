package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// SynthesisResult holds the output of Phase 4 synthesis.
type SynthesisResult struct {
	// ExecutiveSummary is a 2-3 paragraph overview of audit findings.
	ExecutiveSummary string `json:"executive_summary"`

	// TopRisks are the most critical issues identified, ranked.
	TopRisks []string `json:"top_risks"`

	// SystemicPatterns are recurring issues across the codebase.
	SystemicPatterns []string `json:"systemic_patterns"`

	// Recommendations are prioritized action items.
	Recommendations []string `json:"recommendations"`

	// RawOutput is the full LLM response.
	RawOutput string `json:"-"`
}

// hierarchicalThreshold is the number of findings above which we split
// synthesis into per-category passes followed by a final merge.
const hierarchicalThreshold = 50

// Synthesize runs Phase 4: takes all findings and produces an executive summary.
// onToken is called for streaming output (can be nil).
func Synthesize(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	crossCutting []string,
	projectContext string,
	onToken func(string),
) (*SynthesisResult, error) {
	if len(findings) == 0 {
		return &SynthesisResult{
			ExecutiveSummary: "No findings were identified during the audit.",
		}, nil
	}

	if len(findings) > hierarchicalThreshold {
		return synthesizeHierarchical(ctx, client, findings, crossCutting, projectContext, onToken)
	}

	return synthesizeDirect(ctx, client, findings, crossCutting, projectContext, onToken)
}

// synthesizeDirect sends all findings in a single LLM call.
func synthesizeDirect(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	crossCutting []string,
	projectContext string,
	onToken func(string),
) (*SynthesisResult, error) {
	userMsg := BuildSynthesisUserMessage(findings, crossCutting, projectContext)

	messages := []ai.Message{
		{Role: "user", Content: userMsg},
	}

	raw, err := client.ChatStream(ctx, ai.AuditSynthesisPrompt, messages, onToken)
	if err != nil {
		return nil, fmt.Errorf("synthesis LLM call: %w", err)
	}

	result, err := ParseSynthesisResult(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing synthesis result: %w", err)
	}
	result.RawOutput = raw
	return result, nil
}

// synthesizeHierarchical splits findings by category, synthesizes each
// category separately, then merges the category summaries into a final result.
func synthesizeHierarchical(
	ctx context.Context,
	client ai.Client,
	findings []state.DeepFinding,
	crossCutting []string,
	projectContext string,
	onToken func(string),
) (*SynthesisResult, error) {
	// Group findings by category.
	byCategory := make(map[string][]state.DeepFinding)
	for _, f := range findings {
		cat := f.Category
		if cat == "" {
			cat = "uncategorized"
		}
		byCategory[cat] = append(byCategory[cat], f)
	}

	// Synthesize each category.
	var categorySummaries []string
	for cat, catFindings := range byCategory {
		catResult, err := synthesizeDirect(ctx, client, catFindings, nil, projectContext, nil)
		if err != nil {
			return nil, fmt.Errorf("category %q synthesis: %w", cat, err)
		}
		categorySummaries = append(categorySummaries,
			fmt.Sprintf("## Category: %s (%d findings)\n%s", cat, len(catFindings), catResult.ExecutiveSummary))
	}

	// Final merge: use category summaries as input.
	mergeInput := fmt.Sprintf("The following are per-category summaries from a large audit with %d total findings.\n\n%s",
		len(findings), strings.Join(categorySummaries, "\n\n"))

	if len(crossCutting) > 0 {
		mergeInput += "\n\n## Cross-Cutting Observations\n" + strings.Join(crossCutting, "\n- ")
	}

	messages := []ai.Message{
		{Role: "user", Content: mergeInput},
	}

	raw, err := client.ChatStream(ctx, ai.AuditSynthesisPrompt, messages, onToken)
	if err != nil {
		return nil, fmt.Errorf("final synthesis merge: %w", err)
	}

	result, err := ParseSynthesisResult(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing final synthesis: %w", err)
	}
	result.RawOutput = raw
	return result, nil
}

// BuildSynthesisUserMessage formats findings and context into the user message
// sent to the LLM for synthesis.
func BuildSynthesisUserMessage(findings []state.DeepFinding, crossCutting []string, projectContext string) string {
	var sb strings.Builder

	if projectContext != "" {
		sb.WriteString("## Project Context\n")
		sb.WriteString(projectContext)
		sb.WriteString("\n\n")
	}

	// Group by severity for structured presentation.
	bySeverity := map[string][]state.DeepFinding{}
	severityOrder := []string{"critical", "high", "medium", "low"}
	for _, f := range findings {
		sev := f.Severity
		if sev == "" {
			sev = "low"
		}
		bySeverity[sev] = append(bySeverity[sev], f)
	}

	sb.WriteString(fmt.Sprintf("## Audit Findings (%d total)\n\n", len(findings)))

	for _, sev := range severityOrder {
		group := bySeverity[sev]
		if len(group) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("### %s (%d)\n", strings.ToUpper(sev), len(group)))
		for _, f := range group {
			sb.WriteString(fmt.Sprintf("- **%s** [%s/%s] %s:%s — %s\n",
				f.Title, f.Category, f.Subcategory, f.File, f.Lines, f.Description))
		}
		sb.WriteString("\n")
	}

	if len(crossCutting) > 0 {
		sb.WriteString("## Cross-Cutting Observations\n")
		for _, obs := range crossCutting {
			sb.WriteString("- " + obs + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Produce the JSON executive summary now.")
	return sb.String()
}

// ParseSynthesisResult extracts a SynthesisResult from the LLM's raw response.
func ParseSynthesisResult(raw string) (*SynthesisResult, error) {
	s := strings.TrimSpace(raw)

	// Strip markdown fences.
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	// Find JSON object start.
	jsonStart := strings.Index(s, "{")
	if jsonStart == -1 {
		return nil, fmt.Errorf("no JSON object found in synthesis response")
	}
	s = s[jsonStart:]

	var result SynthesisResult
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil, fmt.Errorf("unmarshaling synthesis JSON: %w", err)
	}

	return &result, nil
}

// NeedsHierarchical reports whether the given finding count exceeds the
// hierarchical synthesis threshold.
func NeedsHierarchical(findingCount int) bool {
	return findingCount > hierarchicalThreshold
}

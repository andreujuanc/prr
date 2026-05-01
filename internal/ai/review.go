package ai

import (
	"encoding/json"
	"log"
	"sort"
	"strings"

	"prr/internal/state"
)

// ParseReviewOutput parses a raw AI response into a structured ReviewOutput.
// It handles common LLM quirks: markdown code fences, leading/trailing prose,
// and minor JSON formatting issues.
// Returns nil if parsing fails.
func ParseReviewOutput(raw string) *state.ReviewOutput {
	s := extractJSON(raw)
	if s == "" {
		return nil
	}

	var out state.ReviewOutput
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		log.Printf("Warning: failed to parse review JSON: %v", err)
		return nil
	}

	// Validate minimum structure
	if out.Summary == "" && out.Verdict == "" {
		return nil
	}

	// Normalize verdict
	out.Verdict = normalizeVerdict(out.Verdict)

	// Sort findings by severity
	sort.Slice(out.Findings, func(i, j int) bool {
		return out.Findings[i].SeverityRank() < out.Findings[j].SeverityRank()
	})

	// Ensure arrays are non-nil for clean JSON
	if out.Findings == nil {
		out.Findings = []state.ReviewFinding{}
	}
	if out.MissingTests == nil {
		out.MissingTests = []string{}
	}
	if out.QuestionsForAuthor == nil {
		out.QuestionsForAuthor = []string{}
	}

	return &out
}

// extractJSON extracts a JSON object from raw text that may contain
// markdown code fences, leading prose, or trailing commentary.
func extractJSON(raw string) string {
	s := strings.TrimSpace(raw)

	// Strip markdown code fences
	if strings.HasPrefix(s, "```") {
		// Remove opening fence (```json or ```)
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		// Remove closing fence
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	// If it starts with {, try as-is
	if strings.HasPrefix(s, "{") {
		return s
	}

	// Try to find a JSON object in the text
	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}

	// Find the matching closing brace by counting nesting
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		if escape {
			escape = false
			continue
		}
		c := s[i]
		switch {
		case c == '\\' && inString:
			escape = true
		case c == '"':
			inString = !inString
		case c == '{' && !inString:
			depth++
		case c == '}' && !inString:
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}

	// Fallback: just return from { to end
	return s[start:]
}

// normalizeVerdict normalizes verdict strings to canonical values.
func normalizeVerdict(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	switch {
	case strings.Contains(v, "approve") && !strings.Contains(v, "request"):
		return "approve"
	case strings.Contains(v, "request_changes") || strings.Contains(v, "request changes"):
		return "request_changes"
	case strings.Contains(v, "comment"):
		return "comment"
	default:
		return v
	}
}

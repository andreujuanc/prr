package ai

import (
	"encoding/json"
	"log"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/state"
)

// ParseReviewOutput parses a raw AI response into a structured ReviewOutput.
// It handles common LLM quirks: markdown code fences, leading/trailing prose,
// and minor JSON formatting issues.
// Returns nil if parsing fails.
func ParseReviewOutput(raw string) *state.ReviewOutput {
	s := extractJSON(raw)
	if s == "" {
		log.Printf("ParseReviewOutput: extractJSON returned empty (raw len=%d)", len(raw))
		return nil
	}

	var out state.ReviewOutput
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		log.Printf("Warning: failed to parse review JSON (extracted len=%d): %v", len(s), err)
		if len(s) > 200 {
			log.Printf("  extracted first 200: %s", s[:200])
			log.Printf("  extracted last 200: %s", s[len(s)-200:])
		} else {
			log.Printf("  extracted: %s", s)
		}
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

	// Try to extract from a markdown code fence first (handles fences
	// anywhere in the text, not just at the start).
	if fenced := extractFromCodeFence(s); fenced != "" {
		return fenced
	}

	// Collect all top-level JSON objects in the text. The synthesis
	// agent may emit prose across multiple tool-calling rounds before
	// producing the final JSON, so the text can contain multiple {...}
	// blocks. We want the LAST well-formed one (the final answer).
	candidates := findAllJSONObjects(s)
	if len(candidates) == 0 {
		// Fallback: find first { to end
		start := strings.Index(s, "{")
		if start == -1 {
			return ""
		}
		return s[start:]
	}

	// Return the last candidate — most likely to be the final output.
	return candidates[len(candidates)-1]
}

// extractFromCodeFence looks for a ```json ... ``` (or ``` ... ```) fence
// anywhere in the text and returns the content inside the last fence found.
func extractFromCodeFence(s string) string {
	// Find the last code fence block — the final answer is most likely
	// in the last fence when multiple rounds of prose are present.
	lastOpen := strings.LastIndex(s, "```json")
	if lastOpen == -1 {
		lastOpen = strings.LastIndex(s, "```{")
		if lastOpen == -1 {
			// Check if the whole string is a single fenced block
			if !strings.HasPrefix(s, "```") {
				return ""
			}
			lastOpen = 0
		}
	}

	sub := s[lastOpen:]
	// Skip the opening fence line
	nl := strings.Index(sub, "\n")
	if nl == -1 {
		return ""
	}
	sub = sub[nl+1:]

	// Find the closing fence. The JSON content itself may contain
	// embedded code fences (e.g. in "suggestion" fields), so we look
	// for a ``` that appears at the start of a line (after a newline).
	closeIdx := -1
	searchFrom := 0
	for searchFrom < len(sub) {
		idx := strings.Index(sub[searchFrom:], "```")
		if idx == -1 {
			break
		}
		pos := searchFrom + idx
		// Accept if it's at the very start of sub or preceded by a newline
		if pos == 0 || sub[pos-1] == '\n' {
			closeIdx = pos
			break
		}
		searchFrom = pos + 3
	}

	if closeIdx == -1 {
		// No closing fence — use the rest
		return strings.TrimSpace(sub)
	}
	return strings.TrimSpace(sub[:closeIdx])
}

// findAllJSONObjects finds all top-level balanced {...} objects in s.
func findAllJSONObjects(s string) []string {
	var results []string
	i := 0
	for i < len(s) {
		// Find next opening brace
		start := strings.IndexByte(s[i:], '{')
		if start == -1 {
			break
		}
		start += i

		// Try to find the matching close brace
		depth := 0
		inString := false
		escape := false
		found := false
		for j := start; j < len(s); j++ {
			if escape {
				escape = false
				continue
			}
			c := s[j]
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
					results = append(results, s[start:j+1])
					i = j + 1
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			break
		}
	}
	return results
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

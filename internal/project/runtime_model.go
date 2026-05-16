package project

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

//go:embed prompts/runtime_model.md
var runtimeModelSystemPrompt string

// RuntimeModelResult holds the output of a runtime model discovery pass.
type RuntimeModelResult struct {
	// Model is the parsed structured shape of the codebase. Nil when
	// loaded from cache (caller already holds the cached value) or
	// when no inputs were available.
	Model *state.RuntimeModel

	// InputHash is a SHA-256 hash of the inputs used to generate the
	// model. Used for cache invalidation by callers.
	InputHash string

	// FromCache indicates the cached hash matched current inputs;
	// Model is nil in that case and the caller should use the cached
	// value it already holds.
	FromCache bool
}

// DiscoverRuntimeModel produces a structured runtime model for the
// codebase. It re-uses the same input set (docs, AI configs, manifests,
// dir tree) as project context discovery so the cache invalidation
// rules match: edit a doc or move a directory and both invalidate
// together. The project summary is folded into the prompt input so the
// runtime model can ground its claims in the briefing's architecture
// section.
//
// If cachedHash matches the current inputs, returns Model=nil with
// FromCache=true so the caller can reuse the already-cached value
// without paying for the LLM call. Same contract as Discover.
//
// On LLM or parse failure returns a non-nil error and the caller
// should treat the result as advisory — the runtime model is a
// quality enhancement, not a blocking input.
func DiscoverRuntimeModel(
	ctx context.Context,
	client ai.Client,
	repoRoot string,
	projectSummary string,
	cachedHash string,
	onProgress func(string),
) (*RuntimeModelResult, error) {
	if onProgress == nil {
		onProgress = func(string) {}
	}
	if client == nil {
		return nil, fmt.Errorf("nil ai.Client")
	}

	onProgress("Discovering runtime model...")

	inputs, err := gatherInputs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("gathering inputs: %w", err)
	}

	inputHash := hashRuntimeModelInputs(inputs, projectSummary)

	if cachedHash != "" && cachedHash == inputHash {
		onProgress("Runtime model unchanged (cache hit)")
		return &RuntimeModelResult{InputHash: inputHash, FromCache: true}, nil
	}

	onProgress("Summarizing runtime model...")
	model, err := summarizeRuntimeModel(ctx, client, inputs, projectSummary)
	if err != nil {
		return nil, fmt.Errorf("runtime model summarization: %w", err)
	}

	onProgress("Runtime model ready")
	return &RuntimeModelResult{
		Model:     model,
		InputHash: inputHash,
	}, nil
}

// hashRuntimeModelInputs hashes the inputs used to generate the runtime
// model. Includes the project summary AND the runtime model prompt in
// the hash so any of the three rolls the cache:
//
//   - the project briefing changes (different summary → potentially
//     different runtime model)
//   - the embedded prompt changes (different rules → different output
//     even on the same inputs)
//   - the underlying inputs change (docs, manifests, dir tree)
func hashRuntimeModelInputs(inputs *discoveredInputs, projectSummary string) string {
	base := hashInputs(inputs)
	h := sha256.New()
	h.Write([]byte(base))
	h.Write([]byte{0})
	h.Write([]byte(projectSummary))
	h.Write([]byte{0})
	promptHash := sha256.Sum256([]byte(runtimeModelSystemPrompt))
	h.Write(promptHash[:])
	return fmt.Sprintf("%x", h.Sum(nil))
}

// summarizeRuntimeModel calls the LLM to produce a structured runtime
// model from project inputs + the prose briefing. Output is strict
// JSON; parse failures are returned as errors rather than swallowed.
func summarizeRuntimeModel(
	ctx context.Context,
	client ai.Client,
	inputs *discoveredInputs,
	projectSummary string,
) (*state.RuntimeModel, error) {
	var user strings.Builder

	if projectSummary != "" {
		user.WriteString("=== Project Briefing ===\n")
		user.WriteString(strings.TrimSpace(projectSummary))
		user.WriteString("\n\n")
	}

	if len(inputs.manifests) > 0 {
		for _, name := range sortedKeys(inputs.manifests) {
			fmt.Fprintf(&user, "=== %s ===\n%s\n\n", name, strings.TrimSpace(inputs.manifests[name]))
		}
	}

	if inputs.dirTree != "" {
		user.WriteString("=== Directory Structure ===\n")
		user.WriteString(inputs.dirTree)
		user.WriteString("\n")
	}

	messages := []ai.Message{
		{Role: "user", Content: user.String()},
	}

	// Retry transient HTTP errors. The runtime-model card is optional
	// context for Phase 0.5; falling through after retries is fine.
	raw, err := ai.RetryTransient(ctx, 3, "project-runtime-model", func(ctx context.Context) (string, error) {
		return client.ChatStream(ctx, runtimeModelSystemPrompt, messages, nil)
	})
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	js := extractJSONObject(raw)
	if js == "" {
		return nil, fmt.Errorf("no JSON object in LLM response (len=%d)", len(raw))
	}

	var model state.RuntimeModel
	if err := json.Unmarshal([]byte(js), &model); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	normalizeRuntimeModel(&model)
	return &model, nil
}

// extractJSONObject pulls the first balanced `{...}` substring out of
// raw LLM output. Tolerates markdown fences and surrounding prose.
// Returns "" if no plausible object is present.
func extractJSONObject(raw string) string {
	s := strings.TrimSpace(raw)

	// Strip a leading fenced block if present.
	if strings.HasPrefix(s, "```") {
		// Drop the opening fence line.
		if idx := strings.IndexByte(s, '\n'); idx != -1 {
			s = s[idx+1:]
		}
		if i := strings.LastIndex(s, "```"); i != -1 {
			s = s[:i]
		}
	}

	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}

	depth := 0
	inStr := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if escape {
				escape = false
				continue
			}
			switch c {
			case '\\':
				escape = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// normalizeRuntimeModel cleans up minor LLM quirks: lowercases the
// `kind` and `validation_at` enum values so downstream consumers can
// switch on them, drops entry-points with no kind, and trims whitespace.
func normalizeRuntimeModel(m *state.RuntimeModel) {
	m.AuthModel = strings.TrimSpace(m.AuthModel)
	m.ResultDiscipline = strings.TrimSpace(m.ResultDiscipline)

	m.ValidationSites = trimNonEmpty(m.ValidationSites)
	m.Invariants = trimNonEmpty(m.Invariants)

	cleaned := m.EntryPoints[:0]
	for _, ep := range m.EntryPoints {
		ep.Kind = strings.ToLower(strings.TrimSpace(ep.Kind))
		ep.ValidationAt = strings.ToLower(strings.TrimSpace(ep.ValidationAt))
		ep.RetryModel = strings.TrimSpace(ep.RetryModel)
		ep.BatchModel = strings.TrimSpace(ep.BatchModel)
		if ep.Kind == "" {
			continue
		}
		cleaned = append(cleaned, ep)
	}
	m.EntryPoints = cleaned
}

// trimNonEmpty returns the slice with each element trimmed and empty
// elements dropped. The result is nil when nothing survives so it
// serializes as a missing key (omitempty) rather than `[]`.
func trimNonEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

package ai

import (
	_ "embed"
	"strings"
)

// All prompts are embedded from prompts/*.md. Do not hardcode multi-line
// prompts in Go source — edit the markdown files instead.

// toolsBlock is the canonical "Tools available" section injected into
// any prompt containing the {{TOOLS}} placeholder when the provider
// drives prr's tool loop. Providers that run their own internal tool
// loop (e.g. Claude Code) get an empty substitution — their native
// tools are used instead. See ResolveTools.
//
//go:embed prompts/tools.md
var toolsBlock string

// toolsPlaceholder marks where the canonical tool listing goes in each
// prompt. Must appear on its own line — the surrounding heading and
// intro line live inside tools.md, not in the consuming prompts.
const toolsPlaceholder = "{{TOOLS}}"

// ResolveTools substitutes {{TOOLS}} in a prompt. When the provider
// runs its own internal tool loop, the placeholder is replaced with an
// empty string. Otherwise the canonical tool block is injected.
func ResolveTools(raw string, p Provider) string {
	if p == nil || !p.Capabilities().RunsOwnToolLoop {
		return strings.ReplaceAll(raw, toolsPlaceholder, strings.TrimRight(toolsBlock, "\n"))
	}
	return strings.ReplaceAll(raw, toolsPlaceholder, "")
}

// ReviewPRSystemPrompt is the high-quality, agent-driven review prompt
// used as the base for single-pass PR review.
//
//go:embed prompts/review_pr.md
var ReviewPRSystemPrompt string

// ReviewFilePrompt is the system prompt used when reviewing a single file's diff.
//
//go:embed prompts/review_file.md
var ReviewFilePrompt string

// ReviewBatchPrompt is the system prompt for reviewing a batch of files
// during multi-pass PR review. Phase 1: RECALL mode — report everything.
//
//go:embed prompts/review_batch.md
var ReviewBatchPrompt string

// ReviewSynthesisPrompt is the system prompt for the final synthesis pass.
// Phase 2: FILTER mode — verify, deduplicate, prioritize.
// Output is structured JSON matching ReviewOutput schema.
//
//go:embed prompts/review_synthesis.md
var ReviewSynthesisPrompt string

// ChatPrompt is the system prompt for general follow-up questions.
//
//go:embed prompts/chat.md
var ChatPrompt string

// ReviewIndividualPrompt is the base system prompt for individual AOI deep review.
// Composed with mode preamble, project context, AOI details, and dimensions at runtime.
//
//go:embed prompts/review_individual.md
var ReviewIndividualPrompt string

// ReviewGroupedPrompt is the base system prompt for grouped subcategory review.
// Composed with mode preamble, project context, AOI list, and dimensions at runtime.
//
//go:embed prompts/review_grouped.md
var ReviewGroupedPrompt string

// AuditSynthesisPrompt is the system prompt for Phase 4 audit synthesis.
// It instructs the LLM to produce a structured executive summary from findings.
//
//go:embed prompts/audit_synthesis.md
var AuditSynthesisPrompt string

// RecheckPrompt is the system prompt for the finding recheck/deduplication phase.
// It instructs the LLM to filter, deduplicate, consolidate, and correct findings.
//
//go:embed prompts/recheck.md
var RecheckPrompt string

// ReviewPRPrompt is the system prompt for single-pass PR review.
// Combines the embedded review instructions with structured JSON output
// requirements and tool workflow guidance. The {{TOOLS}} placeholder is
// resolved at request time against the active provider.
var ReviewPRPrompt = ReviewPRSystemPrompt + `

{{TOOLS}}

## Workflow

1. Read the diffs for all changed files.
2. Read base/head files for surrounding context, especially on refactors.
3. Find callers and related code before flagging.
4. Consult the PR Brief in the PR Context section above for prior comments, prior AI reviews, and CI status — do not re-raise resolved points or restate prior findings.

## Output Format

You MUST return ONLY a JSON object matching this exact schema — no prose before or after:

` + "```json" + `
{
  "summary": "one paragraph capturing what the PR does and overall quality",
  "verdict": "approve | request_changes | comment",
  "findings": [
    {
      "severity": "critical | high | medium | low | nit",
      "category": "bug | security | performance | testing | style | architecture | docs",
      "file": "path/to/file.go",
      "line": 42,
      "title": "short title",
      "detail": "what's wrong and why it matters",
      "suggestion": "smallest change that resolves the issue, matching the codebase's existing patterns; code snippet preferred",
      "cwe": "CWE-XXX (for security findings only, omit for non-security)"
    }
  ],
  "missing_tests": ["behaviors that should be tested but aren't"],
  "questions_for_author": ["genuine ambiguities, not rhetorical"]
}
` + "```" + `

Guidelines:
- "findings" array MUST be sorted by severity: critical first, nit last
- Every finding MUST include file and line
- "suggestion" may be empty string if no concrete fix is obvious
- "suggestion" scope is absolute: do NOT propose new utilities, helper functions, abstractions, refactors of adjacent code, or pattern changes not already in the codebase. Fix the issue, nothing more
- "missing_tests": populate when the PR adds new behavior without test coverage. Don't leave empty out of caution — listing missing tests is the job, not scope creep
- "questions_for_author": populate when something is genuinely uncertain or needs author input. Don't leave empty out of caution
- If the PR is clean, return verdict "approve" with an empty findings array
- Return ONLY the JSON — no markdown, no prose, no explanation`

package ai

import _ "embed"

// All prompts are embedded from prompts/*.md. Do not hardcode multi-line
// prompts in Go source — edit the markdown files instead.

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
// requirements and tool workflow guidance.
var ReviewPRPrompt = ReviewPRSystemPrompt + `

You have access to tools — use them proactively to understand the code:
- git_diff: Get the unified diffs for changed files.
- read_file: Read any file from the PR branch (after changes). Supports pagination.
- read_base_file: Read a file from the base branch (before changes).
- grep: Search for patterns across the codebase (regex). Find callers, usages, related code.
- list_dir: List directory contents to understand the project structure.
- gh_pr_checks: Check CI status for the PR.
- gh_pr_comments: Read existing review comments.

## Workflow

1. Use git_diff to read the diffs for all changed files
2. Use read_file and read_base_file to examine files when you need more context
3. Use grep to find callers, usages, and related code
4. Use gh_pr_checks to check CI status
5. Use gh_pr_comments to read existing review comments

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
      "suggestion": "concrete fix, code snippet preferred",
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
- "missing_tests" and "questions_for_author" may be empty arrays
- If the PR is clean, return verdict "approve" with an empty findings array
- Return ONLY the JSON — no markdown, no prose, no explanation`

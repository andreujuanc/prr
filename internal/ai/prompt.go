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
- read_file / read_base_file: read a file at the PR head / base.
- grep / glob / list_dir: search and navigate the tree.
- git_diff: unified diffs for changed files.
- git_log / git_show / git_blame: history and authorship when intent is unclear.
- gh_pr_view / gh_pr_files / gh_pr_checks: PR metadata, file list, CI status.
- gh_pr_comments: existing review comments. Do not re-raise resolved issues.
- gh_issue_view: linked issues referenced in the PR body.
- get_review: the latest prior AI review of this PR, if one exists.

## Workflow

1. Read the diffs for all changed files via git_diff.
2. Use read_file / read_base_file for surrounding context, especially on refactors.
3. Use grep to find callers and related code before flagging.
4. Check gh_pr_comments and get_review to avoid re-raising resolved issues.
5. Check gh_pr_checks to know whether CI surfaced anything you should focus on.

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

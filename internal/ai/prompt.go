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
//
// Idempotent — calling on an already-resolved string is a no-op
// (no placeholders left to substitute).
func ResolveTools(raw string, p Provider) string {
	if p == nil || !p.Capabilities().RunsOwnToolLoop {
		return strings.ReplaceAll(raw, toolsPlaceholder, strings.TrimRight(toolsBlock, "\n"))
	}
	return strings.ReplaceAll(raw, toolsPlaceholder, "")
}

// ResolveToolsForClient resolves {{TOOLS}} against the client's
// underlying provider. For *Agent clients this matches what
// Agent.ChatStream does internally; other Client types pass through
// unchanged.
//
// Call this at the same site that invokes client.ChatStream when you
// also need to hand the resolved prompt to a debug hook. Agent's
// internal resolve mutates only a local copy of its parameter — the
// caller's variable retains the placeholder unless explicitly resolved
// here, which is why --debug output was showing literal "{{TOOLS}}".
func ResolveToolsForClient(client Client, systemPrompt string) string {
	if a, ok := client.(*Agent); ok {
		return ResolveTools(systemPrompt, a.provider)
	}
	return systemPrompt
}

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
// Composed with mode preamble, project context, AOI details, and categories at runtime.
//
//go:embed prompts/review_individual.md
var ReviewIndividualPrompt string

// ReviewGroupedPrompt is the base system prompt for grouped subcategory review.
// Composed with mode preamble, project context, AOI list, and categories at runtime.
//
//go:embed prompts/review_grouped.md
var ReviewGroupedPrompt string

// AuditSynthesisPrompt is the system prompt for Phase 4 audit synthesis.
// It instructs the LLM to produce a structured executive summary from findings.
//
//go:embed prompts/audit_synthesis.md
var AuditSynthesisPrompt string

// RecheckPrompt is the system prompt for the all-in-one finding
// recheck phase. Retained for backward compatibility and for any
// callers that want a single-shot recheck; the modern pipeline uses
// the split RecheckConsolidatePrompt + RecheckDismissPrompt below.
//
//go:embed prompts/recheck.md
var RecheckPrompt string

// RecheckConsolidatePrompt is the first pass of the split recheck
// pipeline. Cross-file consolidation only — no dismissal, no
// modification of individual findings. Runs on the full candidate
// set so it can see patterns BEFORE per-file dismissal erases their
// members.
//
//go:embed prompts/recheck_consolidate.md
var RecheckConsolidatePrompt string

// RecheckDismissPrompt is the second pass of the split recheck
// pipeline. Per-file dismissal, dedup, severity adjustment, and
// description refinement. Runs after consolidation, so by the time
// it sees a finding the cross-file pattern check is done.
//
//go:embed prompts/recheck_dismiss.md
var RecheckDismissPrompt string

package ai

// PrrSpecificToolNames is the canonical list of tool names that exist
// only in prr's tool harness. Providers that run their own internal
// tool loop (Claude Code) don't have these tools by these names, so
// model-facing strings sent to them must not reference any of these.
//
// Two leak-prevention test families scan against this list:
//
//   - Embedded prompt leak test (internal/ai/prompt_test.go) — scans
//     every .md system prompt resolved for a Claude-Code-shaped
//     provider, asserts none of these names remain.
//   - Runtime hint leak test (internal/ai/prompt_test.go) — same, but
//     against AllHints (the registry of dynamic model-facing strings
//     built in Go).
//
// Sibling packages (internal/security, internal/audit) reuse this list
// via internal/aitesting.PrrSpecificToolNames to avoid duplication.
//
// `grep` and `glob` are deliberately omitted — they double as generic
// English/Unix words and would produce false positives. Only
// underscore-joined identifiers and the gh_*/get_review names are
// unambiguously prr-specific.
var PrrSpecificToolNames = []string{
	"read_file",
	"read_base_file",
	"git_diff",
	"git_log",
	"git_show",
	"git_blame",
	"list_dir",
	"gh_pr_view",
	"gh_pr_files",
	"gh_pr_checks",
	"gh_pr_comments",
	"gh_pr_diff",
	"gh_issue_view",
	"get_review",
}

package ai

// This file holds every model-facing runtime hint string — fragments
// that get appended to or composed into user messages outside the
// embedded prompt files.
//
// Why centralize them:
//
// The leak-prevention test in prompt_test.go scans every embedded prompt
// for prr-specific tool names (read_file, git_diff, gh_pr_*, etc.) and
// fails if any appear in the Claude-Code-resolved variant. That test
// catches the .md files but cannot reach Go string literals built at
// runtime via fmt.Sprintf / strings.Builder. Hints centralized here ARE
// covered by the leak test (it iterates AllHints below) — so any
// regression that adds a prr tool name to a model-facing string fails CI.
//
// Convention: any new fragment that becomes part of a user message must
// be declared here as a Hint* constant AND registered in AllHints. If
// you find yourself reaching for an inline literal like
//   allDiffs.WriteString("use git_diff to fetch ...")
// pause — that string ends up in the model's view too, and Claude Code
// won't have git_diff. Add a Hint* constant instead.

// HintDiffTruncated is appended to a diff that has been capped to
// MaxDiffLines. Format args: (limit int, omittedLines int, pathList string).
const HintDiffTruncated = "\n\n... (diff truncated at %d lines — %d more lines omitted)" +
	"\nThe full diff is available — fetch it for these paths if you need the rest: %s"

// HintPROverview is the trailing line of the PR-overview user message,
// nudging the model to fetch per-file diffs as needed.
const HintPROverview = "\nFetch the per-file diffs as needed for any files you want to review.\n"

// HintLargeFileReview is the user message used when a single file's
// diff is too large to inline. Format args: (path string, diffLines int).
const HintLargeFileReview = "Please review the changes to `%s`. The diff is large (%d lines), " +
	"so fetch it for this path using pagination as needed."

// AllHints is the registry the leak test iterates. Any new Hint*
// constant must be added here so the leak test covers it. If you forget,
// the runtime-string leak coverage silently degrades.
var AllHints = []string{
	HintDiffTruncated,
	HintPROverview,
	HintLargeFileReview,
}

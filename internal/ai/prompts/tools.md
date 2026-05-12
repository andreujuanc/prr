## Tools available

Use these proactively — verify findings against the actual code before reporting:

- `read_file` / `read_base_file`: read a file at the PR head / base.
- `grep`: regex search across the codebase. Find callers, usages, type definitions.
- `glob`: find file paths by pattern (e.g. `internal/**/*_test.go`).
- `list_dir`: list a directory's contents.
- `git_diff`: unified diff for any file/range.
- `git_log` / `git_show` / `git_blame`: history and authorship.
- `gh_pr_view` / `gh_pr_files` / `gh_pr_checks` / `gh_pr_comments` / `gh_pr_diff`: PR metadata, file list, CI status, prior review comments, full diff.
- `gh_issue_view`: linked issues referenced in the PR body.
- `get_review`: the latest prior AI review of this PR, if one exists.

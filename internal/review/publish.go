package review

import (
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/state"
)

// PostStructuredReview submits the synthesized findings as a single
// GitHub PR review (event=COMMENT) with one inline comment per finding
// that has a valid file and line. Findings without a target line are
// summarized in the review body.
//
// Mirrors the TUI's batch-submit path (publishBatchReview) so headless
// and interactive runs produce equivalent reviews on GitHub.
func PostStructuredReview(prNumber, commitSHA string, findings []state.ReviewFinding) (posted int, err error) {
	comments := make([]git.ReviewFindingComment, 0, len(findings))
	for _, f := range findings {
		if f.File != "" && f.Line > 0 {
			comments = append(comments, git.ReviewFindingComment{
				Path: f.File,
				Line: f.Line,
				Body: formatFindingMarkdown(f),
				Side: "RIGHT",
			})
		}
	}
	body := formatBatchBody(findings)
	if err := git.SubmitReviewWithFindings(prNumber, commitSHA, body, comments); err != nil {
		return 0, err
	}
	return len(comments), nil
}

func formatFindingMarkdown(f state.ReviewFinding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**[%s/%s] %s**\n\n", f.Severity, f.Category, f.Title)
	b.WriteString(f.Detail + "\n")
	if f.Suggestion != "" {
		fmt.Fprintf(&b, "\n> **Suggestion:** %s\n", f.Suggestion)
	}
	b.WriteString("\n---\n_Posted by [prr](https://github.com/andreujuanc/prr) AI review_")
	return b.String()
}

func formatBatchBody(findings []state.ReviewFinding) string {
	counts := severityCounts(findings)
	files := uniqueFindingFiles(findings)
	var b strings.Builder
	b.WriteString("## AI Review Summary\n\n")
	fmt.Fprintf(&b, "**%d findings** across %d files: %s\n\n", len(findings), len(files), counts)
	b.WriteString("---\n_Posted by [prr](https://github.com/andreujuanc/prr) AI review_")
	return b.String()
}

func severityCounts(findings []state.ReviewFinding) string {
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low", "nit"} {
		if n, ok := counts[sev]; ok {
			label := sev
			if sev == "medium" {
				label = "med"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	return strings.Join(parts, " · ")
}

func uniqueFindingFiles(findings []state.ReviewFinding) []string {
	seen := make(map[string]bool)
	var files []string
	for _, f := range findings {
		if f.File != "" && !seen[f.File] {
			seen[f.File] = true
			files = append(files, f.File)
		}
	}
	return files
}

package review

import (
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/state"
)

// PostStructuredReview submits the synthesized review as a single
// GitHub PR review (event=COMMENT) with one inline comment per finding
// that has a valid file and line. The review body carries the summary
// paragraph, severity counts, missing tests, and questions for the
// author — the narrative context that doesn't anchor to a line.
//
// Mirrors the TUI's batch-submit path (publishBatchReview) so headless
// and interactive runs produce equivalent reviews on GitHub.
func PostStructuredReview(prNumber, commitSHA string, sr *state.ReviewOutput) (posted int, err error) {
	if sr == nil {
		return 0, fmt.Errorf("nil ReviewOutput")
	}
	comments := make([]git.ReviewFindingComment, 0, len(sr.Findings))
	for _, f := range sr.Findings {
		if f.File != "" && f.Line > 0 {
			comments = append(comments, git.ReviewFindingComment{
				Path: f.File,
				Line: f.Line,
				Body: formatFindingMarkdown(f),
				Side: "RIGHT",
			})
		}
	}
	body := formatBatchBody(sr)
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

func formatBatchBody(sr *state.ReviewOutput) string {
	counts := severityCounts(sr.Findings)
	files := uniqueFindingFiles(sr.Findings)
	var b strings.Builder
	b.WriteString("## AI Review Summary\n\n")
	if sr.Summary != "" {
		b.WriteString(sr.Summary)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "**%d findings** across %d files: %s\n", len(sr.Findings), len(files), counts)
	if len(sr.MissingTests) > 0 {
		b.WriteString("\n### Missing tests\n")
		for _, t := range sr.MissingTests {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}
	if len(sr.QuestionsForAuthor) > 0 {
		b.WriteString("\n### Questions for the author\n")
		for _, q := range sr.QuestionsForAuthor {
			fmt.Fprintf(&b, "- %s\n", q)
		}
	}
	b.WriteString("\n---\n_Posted by [prr](https://github.com/andreujuanc/prr) AI review_")
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

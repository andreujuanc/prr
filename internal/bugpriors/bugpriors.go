// Package bugpriors mines recent fix-shaped commit messages from the
// repository's git log and renders them as a short prompt section.
//
// The goal is to give the deep-review prompt a codebase-specific
// prior: instead of hunting for generic bug patterns, the reviewer
// learns the actual failure classes this repo has shipped. The output
// is plain text ready to splice into a prompt; Extract returns "" on
// any error (missing git, shallow clone, no commits) so a bug-priors
// miss never fails a review.
package bugpriors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// DefaultLookback is the number of commits the extractor scans by
// default. Roughly two weeks of activity for a single-author
// codebase, long enough to surface recurring bug classes without
// burying the most-recent failures.
const DefaultLookback = 30

// Default cap on rendered priors. Beyond this the oldest matching
// subjects are dropped first so the most-recent failures dominate.
const maxRendered = 20

// Hash returns a deterministic identifier for rendered priors,
// suitable for folding into cache keys. Empty input → empty string,
// which the cache helpers treat as "no priors" (legacy-equivalent).
func Hash(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// subjectPrefixRE matches the leading conventional-commit-style
// prefix on subjects worth scanning ("fix:", "bug(scope):",
// "audit:", "review:", "revert:"). Case-insensitive. The scope
// component is optional.
var subjectPrefixRE = regexp.MustCompile(`(?i)^(fix|bug|audit|review|revert)(\([^)]*\))?:`)

// bodyKeywordsRE matches keywords that strongly suggest a fix-shaped
// commit even when the subject doesn't carry a conventional prefix
// (e.g., "audit: close cache-key gaps + persist sibling clusters").
// Tuned to cover the bug classes prr has actually shipped.
var bodyKeywordsRE = regexp.MustCompile(`(?i)\b(gap|collision|leak|race|swallow|panic|stale|off.?by|wrong|broken|hang|timeout|deadlock|nil|crash|regression|invalidat|truncat|drift|silent)\b`)

// normaliseRE collapses whitespace and strips the conventional prefix
// for dedup purposes. Two subjects normalise-equal when they describe
// the same underlying bug class under different wording.
var normaliseRE = regexp.MustCompile(`\s+`)

// gitTimeout caps how long we'll wait for git log to return. The
// command is local and cheap; a long wait almost certainly means git
// is missing or stuck, and either way we'd rather return empty.
const gitTimeout = 5 * time.Second

// Extract runs `git log -n <lookback> --pretty=%s` in repoRoot,
// filters to fix-shaped subjects, dedupes, caps, and returns a
// rendered prompt section. Returns "" on any failure or when no
// matches survive — callers should treat empty as "no priors
// available" and skip injection.
//
// Calls never propagate git errors: missing git binary, shallow
// clone, no commits, non-zero exit — all return ("", nil).
func Extract(repoRoot string, lookback int) (string, error) {
	subjects, err := readSubjects(repoRoot, lookback)
	if err != nil {
		return "", nil
	}
	if len(subjects) == 0 {
		return "", nil
	}
	filtered := filterFixShaped(subjects)
	if len(filtered) == 0 {
		return "", nil
	}
	deduped := dedupe(filtered)
	if len(deduped) > maxRendered {
		// Keep the most-recent — first half of the slice is newest
		// because `git log` orders most-recent-first.
		deduped = deduped[:maxRendered]
	}
	return render(deduped), nil
}

// readSubjects shells out to git and returns one subject per line.
func readSubjects(repoRoot string, lookback int) ([]string, error) {
	if lookback <= 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "log", fmt.Sprintf("-n%d", lookback), "--pretty=%s")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	lines := strings.Split(raw, "\n")
	subjects := make([]string, 0, len(lines))
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if s != "" {
			subjects = append(subjects, s)
		}
	}
	return subjects, nil
}

// filterFixShaped keeps subjects that look like bug fixes: either a
// fix-shaped conventional-commit prefix, OR a bug-keyword anywhere in
// the subject (so "audit: close cache-key gaps" survives even though
// the prefix is "audit:", not "fix:").
func filterFixShaped(subjects []string) []string {
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		if subjectPrefixRE.MatchString(s) || bodyKeywordsRE.MatchString(s) {
			out = append(out, s)
		}
	}
	return out
}

// dedupe drops subjects whose normalised form is already seen. The
// first occurrence (most-recent thanks to git log order) wins.
func dedupe(subjects []string) []string {
	seen := make(map[string]struct{}, len(subjects))
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		key := normalise(s)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}

// normalise strips the conventional-commit prefix and collapses
// whitespace, lowercased, for dedup. Two subjects that describe the
// same underlying bug class collapse to the same key here.
func normalise(s string) string {
	s = subjectPrefixRE.ReplaceAllString(s, "")
	s = normaliseRE.ReplaceAllString(s, " ")
	return strings.ToLower(strings.TrimSpace(s))
}

// render formats the filtered subjects as a markdown section ready
// to splice into a prompt. The trailing paragraph is fixed text;
// only the bullet list varies per repo.
func render(subjects []string) string {
	var sb strings.Builder
	sb.WriteString("## Known failure modes in this codebase\n\n")
	sb.WriteString("Based on recent fix-shaped commits, this codebase has shipped:\n\n")
	for _, s := range subjects {
		sb.WriteString("- ")
		sb.WriteString(s)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString("When reviewing this PR, give extra weight to AOIs that touch the\n")
	sb.WriteString("areas these fixes describe (identifier generation, cache key\n")
	sb.WriteString("construction, range/threshold math, call-site timeout/heartbeat,\n")
	sb.WriteString("data integrity at persistence/transport boundaries). A repeated\n")
	sb.WriteString("bug class in the log is a strong prior that the next instance is\n")
	sb.WriteString("in this PR.\n")
	return sb.String()
}

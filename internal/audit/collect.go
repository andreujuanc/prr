package audit

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/config"
)

// CollectStats summarizes what CollectFiles saw and dropped. Exposed so
// callers can surface drop reasons in --debug and warn the user about
// untracked files that look like local tooling output.
type CollectStats struct {
	// TotalListed is the count of paths returned by `git ls-files`
	// (tracked + untracked-non-ignored) before any filter ran.
	TotalListed int

	// Included is the count of paths that survived all filters.
	// Equal to len(files) returned by CollectFiles.
	Included int

	// Tracked / Untracked break Included down by git origin.
	Tracked   int
	Untracked int

	// Exclusion counts, ordered by the precedence applied in
	// partitionFiles. Each path increments exactly one of these (or
	// makes it into Included).
	ExcludedReview int // standard review exclusions (lock/vendor/generated/etc.)
	ExcludedAudit  int // audit-specific exclusions (docs/build/IDE/etc.)
	ExcludedCustom int // user-provided --exclude patterns

	// ForceIncluded counts paths that matched --include and bypassed
	// exclusion. Subset of Included.
	ForceIncluded int

	// UntrackedTransients lists untracked files that survived
	// filtering AND look like locally generated artifacts (logs,
	// debug dumps, state snapshots). The caller surfaces a warning
	// suggesting .gitignore updates — we do NOT auto-exclude because
	// a legitimately-named file could match these patterns.
	UntrackedTransients []string
}

// CollectFiles returns the list of files to audit after applying all
// filters, plus a CollectStats breakdown of what was dropped and why.
// Runs two `git ls-files` queries so we can distinguish tracked from
// untracked origin (needed for the transient-file warning).
func CollectFiles(repoRoot string, excludePatterns, includePatterns []string) ([]string, CollectStats, error) {
	tracked, err := gitLsFiles(repoRoot, "--cached")
	if err != nil {
		return nil, CollectStats{}, fmt.Errorf("listing tracked files: %w", err)
	}
	untracked, err := gitLsFiles(repoRoot, "--others", "--exclude-standard")
	if err != nil {
		return nil, CollectStats{}, fmt.Errorf("listing untracked files: %w", err)
	}

	files, stats := partitionFiles(tracked, untracked, excludePatterns, includePatterns)
	return files, stats, nil
}

// partitionFiles is the pure filter pipeline behind CollectFiles. Split
// out so tests can pin filter precedence and stat accounting without
// spinning up a temp git repo.
//
// Precedence (first match wins):
//  1. --include force-overrides exclusion
//  2. config.ShouldExcludeFromReview (standard exclusions)
//  3. audit-specific patterns (auditExcludePatterns)
//  4. user --exclude patterns
func partitionFiles(tracked, untracked, excludePatterns, includePatterns []string) ([]string, CollectStats) {
	stats := CollectStats{
		TotalListed: len(tracked) + len(untracked),
	}

	untrackedSet := make(map[string]struct{}, len(untracked))
	for _, p := range untracked {
		untrackedSet[p] = struct{}{}
	}

	all := make([]string, 0, len(tracked)+len(untracked))
	all = append(all, tracked...)
	all = append(all, untracked...)

	var result []string
	for _, path := range all {
		_, isUntracked := untrackedSet[path]

		if ShouldForceInclude(path, includePatterns) {
			stats.ForceIncluded++
			result = append(result, path)
			if isUntracked {
				stats.Untracked++
				if IsTransientUntracked(path) {
					stats.UntrackedTransients = append(stats.UntrackedTransients, path)
				}
			} else {
				stats.Tracked++
			}
			continue
		}

		if config.ShouldExcludeFromReview(path) {
			stats.ExcludedReview++
			continue
		}
		if matchesAuditPatterns(path, auditExcludePatterns) {
			stats.ExcludedAudit++
			continue
		}
		if matchesAuditPatterns(path, excludePatterns) {
			stats.ExcludedCustom++
			continue
		}

		result = append(result, path)
		if isUntracked {
			stats.Untracked++
			if IsTransientUntracked(path) {
				stats.UntrackedTransients = append(stats.UntrackedTransients, path)
			}
		} else {
			stats.Tracked++
		}
	}

	sort.Strings(result)
	sort.Strings(stats.UntrackedTransients)
	stats.Included = len(result)
	return result, stats
}

// recentLookbackDefault is the number of recent commits scanned to
// compute the recency ordering when no explicit --audit-recent N is
// set. Bounded so the git log call stays cheap on big repos.
const recentLookbackDefault = 200

// OrderFilesByRecency reorders files so paths touched in the last
// `lookback` commits come first (most-recent first), with the rest
// preserved in stable order behind them. Paths not touched recently
// are not dropped. When lookback ≤ 0, lookback falls back to
// recentLookbackDefault.
//
// Best-effort: a `git log` failure is non-fatal — the original order
// is returned and the caller logs the warning.
func OrderFilesByRecency(repoRoot string, files []string, lookback int) ([]string, error) {
	if len(files) == 0 {
		return files, nil
	}
	if lookback <= 0 {
		lookback = recentLookbackDefault
	}
	recent, err := gitRecentlyTouchedFiles(repoRoot, lookback)
	if err != nil {
		return files, fmt.Errorf("recency reorder: %w", err)
	}
	return reorderByRecency(files, recent), nil
}

// RestrictToRecent returns the subset of files that were touched in
// the last `lookback` commits, in most-recent-first order. Files not
// touched in the window are dropped entirely. When lookback ≤ 0,
// returns files unchanged (no restriction).
//
// Best-effort: a `git log` failure is non-fatal — returns the input
// list unchanged.
func RestrictToRecent(repoRoot string, files []string, lookback int) ([]string, error) {
	if lookback <= 0 || len(files) == 0 {
		return files, nil
	}
	recent, err := gitRecentlyTouchedFiles(repoRoot, lookback)
	if err != nil {
		return files, fmt.Errorf("recency restrict: %w", err)
	}
	return restrictToRecent(files, recent), nil
}

// reorderByRecency is the pure half of OrderFilesByRecency, exposed
// for unit testing so we don't need a temp git repo. files is the
// input list (any order). recent is the recently-touched file list in
// most-recent-first order. The result puts every path that appears in
// both lists at the front (preserving recent's order) and the rest in
// the input's original order.
func reorderByRecency(files, recent []string) []string {
	if len(recent) == 0 || len(files) == 0 {
		return files
	}

	inFiles := make(map[string]struct{}, len(files))
	for _, f := range files {
		inFiles[f] = struct{}{}
	}

	hot := make([]string, 0, len(recent))
	seen := make(map[string]struct{}, len(recent))
	for _, r := range recent {
		if _, ok := inFiles[r]; !ok {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		hot = append(hot, r)
	}

	cold := make([]string, 0, len(files)-len(hot))
	for _, f := range files {
		if _, isHot := seen[f]; !isHot {
			cold = append(cold, f)
		}
	}
	return append(hot, cold...)
}

// restrictToRecent is the pure half of RestrictToRecent. Returns
// files in most-recent-first order, dropping anything not in recent.
func restrictToRecent(files, recent []string) []string {
	if len(recent) == 0 {
		return nil
	}
	inFiles := make(map[string]struct{}, len(files))
	for _, f := range files {
		inFiles[f] = struct{}{}
	}
	out := make([]string, 0, len(recent))
	seen := make(map[string]struct{}, len(recent))
	for _, r := range recent {
		if _, ok := inFiles[r]; !ok {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// gitRecentlyTouchedFiles returns paths touched in the last `n`
// commits, ordered most-recent-first, with duplicates removed.
// Uses `git log -n <n> --name-only --pretty=format:` so the output
// is one path per line, no commit metadata.
func gitRecentlyTouchedFiles(repoRoot string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	cmd := exec.Command("git", "log", fmt.Sprintf("-n%d", n), "--name-only", "--pretty=format:")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log -n%d --name-only: %w", n, err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}

	seen := make(map[string]struct{}, 64)
	var paths []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p := filepath.ToSlash(line)
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	return paths, nil
}

// gitLsFiles runs `git ls-files` with the given args and returns
// slash-normalized paths. Empty lines are dropped.
func gitLsFiles(repoRoot string, args ...string) ([]string, error) {
	fullArgs := append([]string{"ls-files"}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files %s: %w", strings.Join(args, " "), err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	lines := strings.Split(raw, "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		files = append(files, filepath.ToSlash(line))
	}
	return files, nil
}

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

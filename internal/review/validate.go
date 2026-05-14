package review

import (
	"sort"
	"strconv"
	"strings"

	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/state"
)

// HunkRange is the half-open line range [Start, End) covered by one
// diff hunk on the new side of the file. The validator uses these to
// snap a finding's line to the nearest hunk when the AI hallucinates
// a line number outside the actual diff.
type HunkRange struct {
	Start int // first line in the hunk on the new file
	End   int // one past last line
}

// DroppedFinding records a finding that was removed during validation,
// along with the reason. Surfaced through the progress reporter so
// users can see "12 valid · 2 dropped (no file)" instead of silent
// truncation.
type DroppedFinding struct {
	Title  string
	File   string
	Reason string // "empty title", "file not in PR", "synthesis garbage", etc.
}

// validSeverities is the canonical severity enum. Anything else is
// silently normalised to "low" rather than dropped — severity drift
// is the most common AI deviation and the finding itself is usually
// still useful.
var validSeverities = map[string]bool{
	"critical": true, "high": true, "medium": true, "low": true, "nit": true,
}

// validCategories is the canonical category enum. Unknown values
// normalise to "style".
var validCategories = map[string]bool{
	"bug": true, "security": true, "performance": true, "testing": true,
	"style": true, "architecture": true, "docs": true,
}

// ValidateAndNormalize trims, normalises, and drops malformed findings
// from a structured review so the TUI doesn't have to defend against
// hallucinated file paths, blank titles, unknown severities, or line
// numbers that miss the diff entirely.
//
// Rules (in order):
//   - Severity lowercased; unknown → "low" (warn, don't drop).
//   - Category lowercased; unknown → "style" (warn, don't drop).
//   - Title trimmed; empty → drop.
//   - File trimmed of leading "./"; resolved against prFiles
//     (case-sensitive). Not-in-PR → drop.
//   - Line: when out of every hunk for that file, snap to the
//     nearest hunk's start line (warn, don't drop). When no hunks
//     are known for the file, leave Line as-is.
//   - PR-level findings (File == "") are preserved — they're a
//     valid shape, not a defect.
//
// Returns the cleaned review (mutated in place) and a slice of
// dropped-finding reports. When in is nil, returns (nil, nil).
func ValidateAndNormalize(
	in *state.ReviewOutput,
	prFiles []git.PRFile,
	hunkRanges map[string][]HunkRange,
) (*state.ReviewOutput, []DroppedFinding) {
	if in == nil {
		return nil, nil
	}

	prFileSet := make(map[string]bool, len(prFiles))
	for _, f := range prFiles {
		prFileSet[f.Path] = true
	}

	kept := in.Findings[:0]
	var dropped []DroppedFinding

	for _, f := range in.Findings {
		f.Severity = normaliseSeverity(f.Severity)
		f.Category = normaliseCategory(f.Category)
		f.Title = strings.TrimSpace(f.Title)
		f.Detail = strings.TrimSpace(f.Detail)
		f.Suggestion = strings.TrimSpace(f.Suggestion)
		f.File = normaliseFilePath(f.File)

		if f.Title == "" {
			dropped = append(dropped, DroppedFinding{
				Title: f.Title, File: f.File, Reason: "empty title",
			})
			continue
		}
		if f.File != "" && !prFileSet[f.File] {
			dropped = append(dropped, DroppedFinding{
				Title: f.Title, File: f.File, Reason: "file not in PR",
			})
			continue
		}
		if f.File != "" && f.Line > 0 {
			f.Line = snapLineToHunk(f.Line, hunkRanges[f.File])
		}
		if f.Line < 0 {
			f.Line = 0
		}

		kept = append(kept, f)
	}

	in.Findings = kept
	return in, dropped
}

// normaliseSeverity lowercases the input and clamps to the enum. Unknown
// values become "low" — preserving the finding under the safest bucket
// rather than dropping it for a label mismatch.
func normaliseSeverity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if validSeverities[s] {
		return s
	}
	return "low"
}

func normaliseCategory(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if validCategories[c] {
		return c
	}
	return "style"
}

// normaliseFilePath strips the leading "./" some models emit so paths
// match what's in PRFile.Path. Does not resolve symlinks or absolute
// paths — out of scope for a validator.
func normaliseFilePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	return p
}

// snapLineToHunk returns line unchanged if it falls within any hunk in
// hunks; otherwise returns the start of the hunk nearest to line. When
// hunks is empty, returns line unchanged (no information to snap to).
func snapLineToHunk(line int, hunks []HunkRange) int {
	if len(hunks) == 0 {
		return line
	}
	for _, h := range hunks {
		if line >= h.Start && line < h.End {
			return line
		}
	}
	// Out of every hunk — snap to whichever hunk's start is closest.
	bestIdx := 0
	bestDist := abs(line - hunks[0].Start)
	for i := 1; i < len(hunks); i++ {
		d := abs(line - hunks[i].Start)
		if d < bestDist {
			bestIdx = i
			bestDist = d
		}
	}
	return hunks[bestIdx].Start
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ParseHunkRanges extracts new-side hunk ranges from a unified diff
// patch text (the "@@ -a,b +c,d @@" headers). Used by the pipeline to
// build the hunkRanges map for ValidateAndNormalize.
//
// Tolerant: unparseable hunk headers are skipped. An empty result
// disables snap-to-hunk for that file (validator leaves Line alone).
func ParseHunkRanges(patch string) []HunkRange {
	var out []HunkRange
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}
		// Expected shape: "@@ -A,B +C,D @@ ..." or "@@ -A +C @@"
		// (when B/D == 1, GNU diff omits the count).
		plus := strings.Index(line, "+")
		if plus < 0 {
			continue
		}
		rest := line[plus+1:]
		end := strings.Index(rest, " ")
		if end < 0 {
			continue
		}
		spec := rest[:end]
		var start, count int
		if comma := strings.Index(spec, ","); comma >= 0 {
			a, errA := strconv.Atoi(spec[:comma])
			b, errB := strconv.Atoi(spec[comma+1:])
			if errA != nil || errB != nil {
				continue
			}
			start, count = a, b
		} else {
			a, err := strconv.Atoi(spec)
			if err != nil {
				continue
			}
			start, count = a, 1
		}
		if count <= 0 {
			continue
		}
		out = append(out, HunkRange{Start: start, End: start + count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

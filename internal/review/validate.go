package review

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
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

// ValidateAndNormalize trims, normalises, and drops malformed findings
// from a structured review so the TUI doesn't have to defend against
// hallucinated file paths, blank titles, unknown severities, or line
// numbers that miss the diff entirely.
//
// Category is not touched here — the state.Category type validates at
// JSON unmarshal time. Anything that reaches this point is already a
// known slug or the zero Category.
//
// Rules (in order):
//   - Severity lowercased; unknown → "low" (warn, don't drop).
//   - Title trimmed; empty → drop.
//   - File trimmed of leading "./"; resolved against prFiles
//     (case-sensitive). Not-in-PR → drop.
//   - Line: when out of every hunk for that file, snap to the
//     nearest hunk's start line (warn, don't drop). When no hunks
//     are known for the file, leave Line as-is.
//   - PR-level findings (File == "") are preserved.
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

// Confidence penalties applied by ApplyConfidencePenalties to deep
// findings whose evidence record is incomplete. The numbers come from
// the audit-quality plan (commits 4 + 5). Each penalty is subtracted
// from ConfidenceScore (floor 0) and a tag is appended to
// ConfidenceReasoning so the reviewer can see why the score moved.
const (
	// missingTracePenalty is subtracted from ConfidenceScore when a
	// critical/high finding lacks a 3-hop trace. Severity is NOT
	// changed — the bug's impact doesn't depend on whether the model
	// documented the trace; only certainty does.
	missingTracePenalty = 30

	// minTraceHops is the minimum trace length the model must provide
	// for critical/high severity. Below this, the missing-trace
	// penalty fires.
	minTraceHops = 3

	// defensesNotCheckedPenalty is subtracted from ConfidenceScore
	// when a finding in a required category (authorization,
	// concurrency, input-validation, external-io) lists no
	// defenses_checked entries. Severity is unchanged — the bug's
	// impact doesn't depend on whether the model showed which
	// defense layers it ruled out, only our certainty does.
	defensesNotCheckedPenalty = 25
)

// requiredDefensesCategories enumerates the finding categories where
// `defenses_checked` is mandatory.
//
// `error-handling` is deliberately excluded — defense classes there
// are inconsistent enough that requiring them adds more friction than
// signal (per the audit-quality plan).
var requiredDefensesCategories = map[state.Category]bool{
	state.Category("authorization"):    true,
	state.Category("concurrency"):      true,
	state.Category("input-validation"): true,
	state.Category("external-io"):      true,
}

// systemicMinSites is the minimum number of distinct files an
// AffectedSites list must cover before a finding qualifies as
// "Systemic:". Below this, the consolidator was either over-eager
// or the pattern is too narrow to justify the systemic framing.
const systemicMinSites = 3

// systemicTitlePrefix is the title prefix the consolidator uses on
// merged-systemic findings. The gate drops it when demoting.
const systemicTitlePrefix = "Systemic:"

// ApplySystemicGate enforces the "≥3 distinct call sites with
// distinct files" rule on findings flagged Systemic. When a
// consolidated finding ships without enough sites:
//
//   - clears the Systemic flag,
//   - strips a leading "Systemic:" from the title,
//   - rewrites the file field to the first affected site when the
//     finding's File is "multiple" (a marker the consolidator emits
//     for systemic findings — making it a real path lets the audit
//     report render a useful location).
//
// Severity and confidence are NOT touched here; the consolidator may
// have justifiably raised severity on a real systemic pattern that
// just happens to be smaller than 3 sites. Demoting only the framing
// is conservative.
//
// Operates in place and returns the slice for chained use.
func ApplySystemicGate(findings []state.DeepFinding) []state.DeepFinding {
	for i := range findings {
		f := &findings[i]
		if !f.Systemic {
			continue
		}
		if hasDistinctSites(f.AffectedSites, systemicMinSites) {
			continue
		}
		f.Systemic = false
		f.Title = strings.TrimSpace(strings.TrimPrefix(f.Title, systemicTitlePrefix))
		if f.File == "multiple" && len(f.AffectedSites) > 0 {
			f.File = f.AffectedSites[0].File
			if f.Lines == "" {
				f.Lines = f.AffectedSites[0].Lines
			}
		}
	}
	return findings
}

// hasDistinctSites reports whether sites contains at least minDistinct
// entries pointing at distinct files. The plan's framing is "≥3 with
// distinct callers"; "distinct files" is the conservative
// deterministic check (callers are LLM-named and hard to verify).
func hasDistinctSites(sites []state.SiteRef, minDistinct int) bool {
	if len(sites) < minDistinct {
		return false
	}
	seen := make(map[string]struct{}, len(sites))
	for _, s := range sites {
		f := strings.TrimSpace(s.File)
		if f == "" {
			continue
		}
		seen[f] = struct{}{}
		if len(seen) >= minDistinct {
			return true
		}
	}
	return false
}

// TestCoverageHint summarises the result of a test-suite cross-check
// for one finding's cited file/symbol. Injected into the recheck
// dismiss prompt as a one-line annotation per finding so the model
// can downgrade or dismiss findings the existing test suite should
// have caught.
type TestCoverageHint string

const (
	// TestCoverageMissing means the cited file has no companion
	// test file at any of the looked-up locations.
	TestCoverageMissing TestCoverageHint = "no_test_file"

	// TestCoverageExistsButNotCovering means a test file exists for
	// the cited file (either same-dir _test.* or sibling
	// .spec.*/.test.*) but its content does not reference the cited
	// symbol. Synthesis can list this finding under missing_tests.
	TestCoverageExistsButNotCovering TestCoverageHint = "test_exists_but_not_covering"

	// TestCoverageExistsAndCovers means a test file exists AND
	// references the cited symbol. The recheck model should ask
	// "did the test fail? if not, the finding is probably wrong."
	TestCoverageExistsAndCovers TestCoverageHint = "test_exists_and_covers"
)

// String returns the hint as its tag string for prompt injection.
func (h TestCoverageHint) String() string { return string(h) }

// CheckTestCoverage runs a cheap filesystem grep for each finding to
// classify how the existing test suite relates to the cited code. It
// is deliberately heuristic — finding a test file that references
// the cited symbol doesn't prove the test would catch the bug, only
// that the recheck pass should ASK the question.
//
// repoRoot must be the absolute path containing the cited files; an
// empty repoRoot returns an empty map (the recheck pass then runs
// without test hints, which is the pre-commit-10 behavior).
//
// Symbol extraction is intentionally narrow: the cited file's base
// name minus extension. This works across languages without a
// language-specific parser: "internal/auth/login.go" yields "login",
// and we grep for "login" in the test files. Imperfect but cheap;
// false-positive matches just shift the hint from no_test_file to
// test_exists_but_not_covering, which is still informative.
func CheckTestCoverage(repoRoot string, findings []state.DeepFinding) map[string]TestCoverageHint {
	if repoRoot == "" || len(findings) == 0 {
		return nil
	}

	root, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil
	}
	defer root.Close()

	out := make(map[string]TestCoverageHint, len(findings))
	for _, f := range findings {
		if f.FindingID == "" || f.File == "" {
			continue
		}
		out[f.FindingID] = classifyTestCoverage(root, f.File)
	}
	return out
}

// classifyTestCoverage walks the candidate test file paths for one
// finding and returns the appropriate hint. Stops at the first
// file that references the cited symbol.
//
// Match is case-insensitive because the cited symbol comes from the
// file's base name (typically lowercase: "login.go") but the actual
// function or class identifier often has different casing
// (CamelCase "Login" in Go, camelCase "login" in JS, snake_case
// "log_in" in Python). Lowercasing both sides is cheap and removes a
// large class of false negatives.
func classifyTestCoverage(root *os.Root, citedFile string) TestCoverageHint {
	candidates := candidateTestPaths(citedFile)
	if len(candidates) == 0 {
		return TestCoverageMissing
	}
	symbol := strings.ToLower(citedSymbol(citedFile))

	var sawAnyTestFile bool
	for _, p := range candidates {
		body, ok := readBoundedFile(root, p, 256*1024)
		if !ok {
			continue
		}
		sawAnyTestFile = true
		if symbol != "" && strings.Contains(strings.ToLower(body), symbol) {
			return TestCoverageExistsAndCovers
		}
	}
	if sawAnyTestFile {
		return TestCoverageExistsButNotCovering
	}
	return TestCoverageMissing
}

// candidateTestPaths returns the set of paths to check for tests of
// citedFile. Multi-language by design:
//
//   - same-dir <base>_test.<ext>  (Go convention)
//   - same-dir <base>.test.<ext>   (JS/TS test runners)
//   - same-dir <base>.spec.<ext>   (JS/TS spec runners)
//
// Paths are returned as repo-relative; the caller's os.Root resolves
// against repoRoot. Duplicates are dropped.
func candidateTestPaths(citedFile string) []string {
	citedFile = strings.TrimSpace(citedFile)
	if citedFile == "" {
		return nil
	}
	dir, file := filepath.Split(citedFile)
	ext := filepath.Ext(file)
	base := strings.TrimSuffix(file, ext)

	seen := make(map[string]struct{}, 6)
	var out []string
	add := func(p string) {
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}

	// Go-style: foo_test.go
	if ext != "" {
		add(filepath.ToSlash(filepath.Join(dir, base+"_test"+ext)))
	}
	// JS/TS-style: foo.test.ts / foo.spec.ts. Also Python: foo_test.py
	// (covered by the first form). The same patterns apply to .js,
	// .jsx, .tsx, etc.; we use the cited file's own extension.
	if ext != "" {
		add(filepath.ToSlash(filepath.Join(dir, base+".test"+ext)))
		add(filepath.ToSlash(filepath.Join(dir, base+".spec"+ext)))
	}
	return out
}

// citedSymbol returns the cited file's base name without extension.
// Used as a coarse heuristic for whether a test file "covers" the
// finding: if the test contains a string match of the base name, we
// assume it touches related code.
func citedSymbol(citedFile string) string {
	_, file := filepath.Split(citedFile)
	ext := filepath.Ext(file)
	return strings.TrimSuffix(file, ext)
}

// readBoundedFile reads the contents of relPath through root, capped
// at maxBytes. Returns "", false when the file is absent or
// unreadable. Existence-only checks should use root.Stat directly;
// this helper exists for "exists AND I need to grep its content".
func readBoundedFile(root *os.Root, relPath string, maxBytes int) (string, bool) {
	f, err := root.Open(relPath)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, maxBytes)
	n, err := f.Read(buf)
	// io.EOF is the expected sentinel for files smaller than maxBytes.
	// Any other error means we got an incomplete read and the heuristic
	// would lie: returning ("", false) keeps test-coverage checks honest.
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false
	}
	return string(buf[:n]), true
}

// ApplyConfidencePenalties walks deep findings and adjusts confidence
// scores when required evidence is missing. Currently enforces:
//
//   - missing-trace: critical/high finding without ≥3 trace hops
//     (commit 4 in the audit-quality plan).
//   - defenses-not-checked: required-category finding with no
//     `defenses_checked` entries (commit 5).
//
// Severity is never modified. Confidence is the right axis: severity
// = "how bad if real", confidence = "how sure I am it's real".
// Missing evidence doesn't make the bug less bad; it makes us less
// sure it's a bug. The reviewer can still sort/filter by confidence
// without losing the signal that, if real, the bug is critical.
//
// Operates in place on the provided slice and returns the modified
// findings for chained use.
func ApplyConfidencePenalties(findings []state.DeepFinding) []state.DeepFinding {
	for i := range findings {
		if traceRequired(findings[i].Severity) && !hasValidTrace(findings[i].Trace) {
			applyConfidencePenalty(&findings[i], missingTracePenalty, "missing-trace")
		}
		if defensesRequired(findings[i].Category) && len(findings[i].DefensesChecked) == 0 {
			applyConfidencePenalty(&findings[i], defensesNotCheckedPenalty, "defenses-not-checked")
		}
	}
	return findings
}

// defensesRequired reports whether the defenses_checked field is
// mandatory for a finding's category.
func defensesRequired(category state.Category) bool {
	return requiredDefensesCategories[category]
}

// traceRequired reports whether the 3-hop trace rule applies to a
// finding's severity. Only critical/high — local-scope bugs at lower
// severities don't need a boundary-reaching trace.
func traceRequired(severity string) bool {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "high":
		return true
	default:
		return false
	}
}

// hasValidTrace reports whether the trace meets the minimum-hops
// requirement. A hop with an empty Role is treated as a missing hop
// — the model has to label what each step represents.
func hasValidTrace(hops []state.TraceHop) bool {
	if len(hops) < minTraceHops {
		return false
	}
	valid := 0
	for _, h := range hops {
		if strings.TrimSpace(h.Role) != "" {
			valid++
		}
	}
	return valid >= minTraceHops
}

// applyConfidencePenalty subtracts amount from the finding's confidence
// score (floor 0) and appends tag to ConfidenceReasoning so the
// reviewer can see what triggered the move. Multiple penalties on the
// same finding accumulate via repeated calls — tags are
// comma-separated.
func applyConfidencePenalty(f *state.DeepFinding, amount int, tag string) {
	f.ConfidenceScore -= amount
	if f.ConfidenceScore < 0 {
		f.ConfidenceScore = 0
	}
	if f.ConfidenceReasoning == "" {
		f.ConfidenceReasoning = tag
		return
	}
	// Avoid appending duplicates if the same finding flows through
	// the validator more than once.
	if strings.Contains(f.ConfidenceReasoning, tag) {
		return
	}
	f.ConfidenceReasoning = f.ConfidenceReasoning + "; " + tag
}

// ParseHunkRanges extracts new-side hunk ranges from a unified diff
// patch text (the "@@ -a,b +c,d @@" headers). Used by the pipeline to
// build the hunkRanges map for ValidateAndNormalize.
//
// Tolerant: unparseable hunk headers are skipped. An empty result
// disables snap-to-hunk for that file (validator leaves Line alone).
func ParseHunkRanges(patch string) []HunkRange {
	var out []HunkRange
	malformed := 0
	for line := range strings.SplitSeq(patch, "\n") {
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}
		// Expected shape: "@@ -A,B +C,D @@ ..." or "@@ -A +C @@"
		// (when B/D == 1, GNU diff omits the count).
		_, after, ok := strings.Cut(line, "+")
		if !ok {
			malformed++
			continue
		}
		rest := after
		before, _, ok := strings.Cut(rest, " ")
		if !ok {
			malformed++
			continue
		}
		spec := before
		var start, count int
		if before, after, ok := strings.Cut(spec, ","); ok {
			a, errA := strconv.Atoi(before)
			b, errB := strconv.Atoi(after)
			if errA != nil || errB != nil {
				malformed++
				continue
			}
			start, count = a, b
		} else {
			a, err := strconv.Atoi(spec)
			if err != nil {
				malformed++
				continue
			}
			start, count = a, 1
		}
		if count <= 0 {
			// Deletion-only hunks legitimately have a new-side count of
			// 0 (e.g. "@@ -10,5 +9,0 @@"). They have no new-side lines
			// for a finding to land on, so they don't contribute to
			// the snap-to-hunk set — but they aren't malformed either,
			// so don't bump the counter.
			continue
		}
		out = append(out, HunkRange{Start: start, End: start + count})
	}
	if malformed > 0 {
		// Without this log, malformed hunk headers silently disabled the
		// snap-to-hunk check for the file and any downstream confusion
		// (findings landing outside hunks) had no breadcrumb back to
		// the diff.
		log.Printf("review/validate: ParseHunkRanges skipped %d malformed hunk header(s)", malformed)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

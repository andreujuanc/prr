package review

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/andreujuanc/prr/internal/state"
)

// Evidence verification: each finding from Phase 3 must carry a
// verbatim 1-3 line snippet from the file at its cited lines. The
// pipeline matches the snippet against the file before accepting the
// finding — anchoring every claim to text that actually exists.
//
// Matching is fuzzy on whitespace because:
//   - LLMs frequently normalize tabs to spaces and collapse runs of
//     whitespace, even when told to copy verbatim.
//   - Both git diffs and on-disk files vary in line-ending and
//     indent normalization across providers and tools.
//
// Matching is NOT fuzzy on content: characters other than whitespace
// must appear in the same order. A paraphrased snippet ("the err is
// ignored") will not match the real line (`_ = pipe.Close()`).
//
// Out of the line range we accept a ±10 line tolerance: the model
// often cites "line 45" when the actual offending code is on 44 or
// 46. Stricter than this turns whitespace nudges into mismatches;
// looser is just "search the whole file" which defeats the purpose.

const (
	// evidenceLineTolerance is the +/- window around the finding's
	// cited line range in which the snippet must appear. Lines
	// outside this window are ignored. The model regularly cites
	// the start of a block when the offending line is a few lines
	// in or out; 10 is generous enough to cover that without
	// erasing the value of the line cite.
	evidenceLineTolerance = 10

	// evidenceFileSizeLimit caps the file size we'll read to verify
	// a snippet. Above this, we trust the model — the verifier
	// becomes a cost liability on giant generated files. 1 MiB is
	// well above any sane source file size.
	evidenceFileSizeLimit = 1 << 20

	// evidenceMaxSnippetLines caps how many lines we'll treat as a
	// valid snippet. The prompt says 1-3; allow a small grace
	// margin (5) before we declare a snippet malformed. Beyond this
	// the "snippet" is really a paraphrase or a quoted block, and
	// matching becomes meaningless.
	evidenceMaxSnippetLines = 5
)

// evidenceVerdict is the outcome of matching a snippet against a file.
type evidenceVerdict int

const (
	// evidenceOK: snippet matched the file within the tolerance
	// window, OR the finding had no snippet (we don't punish missing
	// snippets here — that's a prompt-compliance issue, not a
	// truthfulness issue; recheck handles missing-snippet findings
	// via the safety net rather than dropping them outright).
	evidenceOK evidenceVerdict = iota

	// evidenceMismatch: snippet did not match anywhere within the
	// tolerance window. Finding needs the corrector pass.
	evidenceMismatch

	// evidenceFileMissing: cited file doesn't exist on disk. The
	// model hallucinated a path. No retry will fix this — drop.
	evidenceFileMissing

	// evidenceFileUnreadable: file exists but can't be read (too
	// large, permission error, etc.). We can't verify so we accept
	// — verifying an unreadable file would punish the model for our
	// infrastructure.
	evidenceFileUnreadable
)

// verifyEvidence checks one finding's snippet against the cited file.
// repoRoot is the absolute path the finding's File is relative to.
// Returns evidenceOK for findings with no snippet (verification is
// only meaningful when the model provided one).
func verifyEvidence(repoRoot string, f state.DeepFinding) evidenceVerdict {
	if strings.TrimSpace(f.EvidenceSnippet) == "" {
		// No snippet to verify. The prompt asks for one but absent
		// snippets are a separate concern from snippet truthfulness.
		// Recheck still gets to dismiss findings whose evidence is
		// generic; this verifier specifically targets hallucination.
		return evidenceOK
	}
	if f.File == "" {
		return evidenceFileMissing
	}

	abs := f.File
	if !filepath.IsAbs(abs) && repoRoot != "" {
		abs = filepath.Join(repoRoot, f.File)
	}

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return evidenceFileMissing
		}
		return evidenceFileUnreadable
	}
	if info.Size() > evidenceFileSizeLimit {
		return evidenceFileUnreadable
	}

	fh, err := os.Open(abs)
	if err != nil {
		return evidenceFileUnreadable
	}
	defer fh.Close()

	lines, err := readAllLines(fh)
	if err != nil {
		return evidenceFileUnreadable
	}

	start, end := parseFindingLineRange(f.Lines)
	if start <= 0 {
		// No usable line range — search the whole file. This is
		// generous, but the model might be right about the bug
		// while wrong about the format.
		start, end = 1, len(lines)
	}
	if end < start {
		end = start
	}

	// Apply ±tolerance window, clamped to file bounds.
	windowStart := start - evidenceLineTolerance
	if windowStart < 1 {
		windowStart = 1
	}
	windowEnd := end + evidenceLineTolerance
	if windowEnd > len(lines) {
		windowEnd = len(lines)
	}
	if windowStart > len(lines) {
		// Cited line is past EOF — definitely hallucinated.
		return evidenceMismatch
	}

	// Whitespace-normalize the snippet and the file window into
	// flat strings, then look for the snippet as a substring of
	// the window. This is stricter than anchoring on one long
	// token: a paraphrase like "the error from Close() is ignored"
	// won't accidentally match `_ = stream.Close()` because the
	// FULL phrase isn't in the window — only the function name is.
	//
	// We join the window's lines with single spaces so a multi-line
	// snippet stays matchable across the line break.
	needle := normalizeWhitespace(f.EvidenceSnippet)
	if needle == "" {
		// Snippet collapsed to nothing — pure whitespace. Treat as
		// missing snippet rather than a hallucination.
		return evidenceOK
	}
	haystack := normalizeWhitespace(strings.Join(lines[windowStart-1:windowEnd], "\n"))
	if strings.Contains(haystack, needle) {
		return evidenceOK
	}
	return evidenceMismatch
}

// normalizeWhitespace collapses any run of whitespace (tabs, spaces,
// newlines, CR) into a single space, then trims. This is what makes
// matching robust to LLM whitespace drift — the most common
// "paraphrased" snippet is one where indentation was normalized.
func normalizeWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// readAllLines slurps the file into a []string, preserving content
// but stripping line terminators. Caller must close the reader.
func readAllLines(r interface{ Read(p []byte) (int, error) }) ([]string, error) {
	var lines []string
	scanner := bufio.NewScanner(adaptReader(r))
	// Allow long lines — minified bundles or generated code can be
	// huge; the default 64KiB is not enough.
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// adaptReader narrows the interface so bufio.NewScanner accepts the
// stripped-down reader we pass in. Internal helper.
type readerAdapter struct {
	r interface{ Read(p []byte) (int, error) }
}

func (a readerAdapter) Read(p []byte) (int, error) { return a.r.Read(p) }

func adaptReader(r interface{ Read(p []byte) (int, error) }) readerAdapter {
	return readerAdapter{r: r}
}

// parseFindingLineRange extracts the (start, end) line range from
// strings like "45", "45-62", "L45-L62", or "45,62". Returns (0, 0)
// for unparseable input — the caller then falls back to a full-file
// search.
func parseFindingLineRange(s string) (start, end int) {
	if s == "" {
		return 0, 0
	}
	s = strings.TrimSpace(s)
	// Strip leading L if present (e.g. "L45-L62"); the prompts use
	// numeric ranges but some models prepend L by habit.
	s = strings.ReplaceAll(s, "L", "")
	s = strings.ReplaceAll(s, "l", "")

	// Try "start-end".
	if i := strings.IndexAny(s, "-–—"); i > 0 {
		a, errA := strconv.Atoi(strings.TrimSpace(s[:i]))
		b, errB := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		if errA == nil && errB == nil {
			return a, b
		}
	}
	// Try comma form "start,end".
	if i := strings.Index(s, ","); i > 0 {
		a, errA := strconv.Atoi(strings.TrimSpace(s[:i]))
		b, errB := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		if errA == nil && errB == nil {
			return a, b
		}
	}
	// Single number.
	if n, err := strconv.Atoi(s); err == nil {
		return n, n
	}
	return 0, 0
}

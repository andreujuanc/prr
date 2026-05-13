package audit

import (
	"errors"
	"io/fs"
	"os"

	"github.com/andreujuanc/prr/internal/classify"
)

// loadOutcome categorizes what happened when loadAuditFile tried to
// ingest a path. Every outcome except `loadedOK` means the file is
// excluded from the audit — but for different reasons that we want
// to count separately so the user can see what fell out.
type loadOutcome int

const (
	loadedOK         loadOutcome = iota // file content loaded successfully
	skippedSymlink                      // symlink — refused to follow
	skippedTooLarge                     // exceeded MaxFileBytes cap
	skippedBinary                       // IsBinary heuristic matched
	skippedEmpty                        // zero-byte content
	skippedNotFound                     // ENOENT — likely race with `git rm`
	loadErrored                         // unexpected read error (permissions, IO, etc.)
)

// loadResult is the per-file outcome of loadAuditFile.
type loadResult struct {
	File    classify.File // populated only when Outcome == loadedOK
	Outcome loadOutcome
	Size    int64 // file size in bytes — meaningful for skippedTooLarge warnings
	Err     error // populated only when Outcome == loadErrored
}

// loadAuditFile applies the per-file guards Phase 1 needs in order.
// Returns a loadResult; the caller increments outcome counters and
// decides what to do about aggregate read failures.
//
// Guard order matters:
//  1. Lstat → reject symlinks before reading anything (a symlink to
//     /etc/passwd would otherwise be silently ingested)
//  2. Stat size → reject oversized files BEFORE reading (don't load
//     a 50MB JSON into memory just to skip it)
//  3. ReadFile → ENOENT is benign (race with `git rm`); other errors
//     bubble up so the caller can apply aggregate-fail logic
//  4. IsBinary → byte-level check on the actual content
//  5. Empty check → zero-byte files have no audit value
func loadAuditFile(absPath, relPath string, maxBytes int64) loadResult {
	info, err := os.Lstat(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return loadResult{Outcome: skippedNotFound}
		}
		return loadResult{Outcome: loadErrored, Err: err}
	}

	// Symlinks: skip entirely. Following them risks ingesting files
	// outside the repo or creating cycles. Users who really need a
	// symlinked file audited can resolve it explicitly.
	if info.Mode()&os.ModeSymlink != 0 {
		return loadResult{Outcome: skippedSymlink}
	}

	if !info.Mode().IsRegular() {
		// Devices, pipes, sockets — not auditable. Treat as symlink-equivalent
		// for counter purposes; rare enough that a dedicated counter would
		// be overkill.
		return loadResult{Outcome: skippedSymlink}
	}

	size := info.Size()
	if maxBytes > 0 && size > maxBytes {
		return loadResult{Outcome: skippedTooLarge, Size: size}
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Race: file existed at lstat time, gone by ReadFile.
			return loadResult{Outcome: skippedNotFound}
		}
		return loadResult{Outcome: loadErrored, Err: err}
	}

	if len(content) == 0 {
		return loadResult{Outcome: skippedEmpty}
	}

	if IsBinary(content) {
		return loadResult{Outcome: skippedBinary, Size: int64(len(content))}
	}

	return loadResult{
		File:    classify.File{Path: relPath, Content: string(content)},
		Outcome: loadedOK,
		Size:    int64(len(content)),
	}
}

// Aggregate-fail thresholds for Phase 1 file reads.
//
// A handful of transient read errors (permission denied on one file,
// a file removed mid-run, etc.) shouldn't kill an audit of 500 files.
// But if 40% of reads fail, something is structurally wrong (wrong
// working directory, broken FS, etc.) and silently degrading would
// hide it. The threshold + floor strikes that balance.
const (
	// aggregateFailRatio is the failure ratio above which Phase 1 aborts.
	aggregateFailRatio = 0.20

	// aggregateFailMinFailures is the absolute floor. Without it, "2 of
	// 5 files failed = 40%" on tiny audits would overreact to one or
	// two flaky reads.
	aggregateFailMinFailures = 3
)

// shouldAggregateFail reports whether the (errored, attempted) ratio
// crosses the abort threshold.
func shouldAggregateFail(errored, attempted int) bool {
	if errored < aggregateFailMinFailures {
		return false
	}
	if attempted <= 0 {
		return false
	}
	return float64(errored)/float64(attempted) > aggregateFailRatio
}

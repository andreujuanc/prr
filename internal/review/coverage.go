package review

import (
	"sort"

	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

// BuildCoverage aggregates per-file coverage from the inputs the
// pipeline already has at synthesis time. Pure function — no I/O,
// no LLM call, no mutation of its arguments. Returns nil when there
// is nothing to report (empty inputs).
//
// Inputs:
//   - aoiScan: per-file AOI scan results (Phase 2 output).
//   - findings: emitted findings, each carrying File.
//   - dismissals: emitted dismissals, each carrying File +
//     ConfidenceScore.
//   - failedAOIIDs: AOI IDs whose Phase 3 review call errored.
//     Mapped back to file via aoiScan so the "failed" count is
//     per-file even though the input is per-AOI.
//   - filesInScope: every file the audit / review considered (diff
//     files for review, classified-then-filtered files for audit).
//     Files appearing here but with zero AOIs become orphans.
//   - skippedFiles: files the user-selected review mode intentionally
//     left unreviewed (e.g. --review-mode=aoi-only skipping files
//     with no AOIs). Surfaced on the coverage so the reader can tell
//     "skipped on purpose" from "left out by accident".
//
// The dismiss-confidence average uses an integer mean, skipping
// dismissals with ConfidenceScore == 0 (the "unknown" sentinel for
// older cached state). A file whose dismissals all have unknown
// confidence reports AvgDismissConf == 0; treat that as "no signal"
// rather than "low confidence".
func BuildCoverage(
	aoiScan []security.AOIScanResult,
	findings []state.DeepFinding,
	dismissals []state.DeepDismissal,
	failedAOIIDs []string,
	filesInScope []string,
	skippedFiles []string,
) *state.ReviewCoverage {
	if len(aoiScan) == 0 && len(filesInScope) == 0 && len(findings) == 0 && len(dismissals) == 0 && len(skippedFiles) == 0 {
		return nil
	}

	type bucket struct {
		aois               int
		findings           int
		dismissals         int
		failed             int
		dismissConfSum     int
		dismissConfCount   int
		maxFindingSeverity string
	}
	buckets := make(map[string]*bucket)
	get := func(f string) *bucket {
		if f == "" {
			return nil
		}
		b, ok := buckets[f]
		if !ok {
			b = &bucket{}
			buckets[f] = b
		}
		return b
	}

	scopeSet := make(map[string]struct{}, len(filesInScope))
	for _, f := range filesInScope {
		if f == "" {
			continue
		}
		scopeSet[f] = struct{}{}
		// Seed buckets for in-scope files so orphans surface even
		// when nothing else touched them.
		_ = get(f)
	}

	// AOI scan counts — also map AOI ID → file so failed AOIs can be
	// attributed back to a file.
	aoiToFile := make(map[string]string)
	for _, fr := range aoiScan {
		b := get(fr.File)
		if b == nil {
			continue
		}
		b.aois += len(fr.AreasOfInterest)
		for _, aoi := range fr.AreasOfInterest {
			if aoi.ID != "" {
				aoiToFile[aoi.ID] = fr.File
			}
		}
	}

	for _, f := range findings {
		// Systemic findings represent cross-file patterns synthesised
		// from multiple per-file findings — they don't attribute to a
		// single file (their File field is the "multiple" sentinel).
		// Counting them per-file would distort the coverage view.
		if f.Systemic {
			continue
		}
		b := get(f.File)
		if b == nil {
			continue
		}
		b.findings++
		// severityRank treats "" the same as "nit" (both rank 4), so
		// "first finding seen wins" until a strictly higher one
		// shows up. Explicitly handle the empty seed so a single
		// nit finding still surfaces as the max severity rather
		// than leaving the string blank in the JSON output.
		if b.maxFindingSeverity == "" || severityRank(f.Severity) < severityRank(b.maxFindingSeverity) {
			b.maxFindingSeverity = f.Severity
		}
	}

	for _, d := range dismissals {
		b := get(d.File)
		if b == nil {
			continue
		}
		b.dismissals++
		if d.ConfidenceScore > 0 {
			b.dismissConfSum += d.ConfidenceScore
			b.dismissConfCount++
		}
	}

	for _, id := range failedAOIIDs {
		file, ok := aoiToFile[id]
		if !ok {
			continue
		}
		b := get(file)
		if b == nil {
			continue
		}
		b.failed++
	}

	files := make([]state.FileCoverage, 0, len(buckets))
	var orphans []string
	for path, b := range buckets {
		_, inScope := scopeSet[path]
		if b.aois == 0 && b.findings == 0 && b.dismissals == 0 && b.failed == 0 {
			if inScope {
				orphans = append(orphans, path)
			}
			continue
		}
		fc := state.FileCoverage{
			File:               path,
			AOIsScanned:        b.aois,
			Findings:           b.findings,
			Dismissals:         b.dismissals,
			Failed:             b.failed,
			MaxFindingSeverity: b.maxFindingSeverity,
		}
		if b.dismissConfCount > 0 {
			fc.AvgDismissConf = b.dismissConfSum / b.dismissConfCount
		}
		files = append(files, fc)
	}

	// Deterministic order: findings first (highest severity first),
	// then dismissals-only files, then by path.
	sort.SliceStable(files, func(i, j int) bool {
		if (files[i].Findings > 0) != (files[j].Findings > 0) {
			return files[i].Findings > 0
		}
		if files[i].Findings > 0 && files[j].Findings > 0 {
			ri := severityRank(files[i].MaxFindingSeverity)
			rj := severityRank(files[j].MaxFindingSeverity)
			if ri != rj {
				return ri < rj
			}
		}
		return files[i].File < files[j].File
	})
	sort.Strings(orphans)

	// Files the review mode intentionally skipped are not "orphans"
	// (which are unintended) — surface them separately so the reader
	// can see "skipped on purpose" distinct from "scanner missed it."
	// Sort for stable output and dedupe in case the caller passed
	// duplicates.
	var skipped []string
	if len(skippedFiles) > 0 {
		seen := make(map[string]bool, len(skippedFiles))
		for _, f := range skippedFiles {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			skipped = append(skipped, f)
		}
		sort.Strings(skipped)
	}

	cov := &state.ReviewCoverage{
		Files:        files,
		FilesInScope: len(scopeSet),
		OrphanFiles:  orphans,
		SkippedFiles: skipped,
	}
	for _, fc := range files {
		if fc.AOIsScanned > 0 {
			cov.FilesWithAOIs++
		}
		if fc.Findings > 0 || fc.Dismissals > 0 || fc.Failed > 0 {
			cov.FilesReviewed++
		}
	}
	return cov
}

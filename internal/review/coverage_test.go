package review

import (
	"testing"

	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

func TestBuildCoverage_PerFileCounts(t *testing.T) {
	aoiScan := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: []security.AreaOfInterest{
			{ID: "a1"}, {ID: "a2"}, {ID: "a3"},
		}},
		{File: "b.go", AreasOfInterest: []security.AreaOfInterest{
			{ID: "b1"}, {ID: "b2"},
		}},
	}
	findings := []state.DeepFinding{
		{File: "a.go", Severity: "high"},
	}
	dismissals := []state.DeepDismissal{
		{File: "a.go", ConfidenceScore: 80},
		{File: "a.go", ConfidenceScore: 90},
		{File: "b.go", ConfidenceScore: 70},
		{File: "b.go", ConfidenceScore: 60},
	}

	cov := BuildCoverage(aoiScan, findings, dismissals, nil, []string{"a.go", "b.go"}, nil)
	if cov == nil {
		t.Fatal("expected non-nil coverage")
	}
	if cov.FilesInScope != 2 {
		t.Errorf("FilesInScope: want 2, got %d", cov.FilesInScope)
	}
	if cov.FilesWithAOIs != 2 {
		t.Errorf("FilesWithAOIs: want 2, got %d", cov.FilesWithAOIs)
	}
	if len(cov.Files) != 2 {
		t.Fatalf("Files: want 2, got %d", len(cov.Files))
	}
	// a.go must come first (has findings).
	if cov.Files[0].File != "a.go" {
		t.Errorf("expected a.go first (has findings); got %q", cov.Files[0].File)
	}
	if cov.Files[0].Findings != 1 || cov.Files[0].MaxFindingSeverity != "high" {
		t.Errorf("a.go: findings=%d sev=%q", cov.Files[0].Findings, cov.Files[0].MaxFindingSeverity)
	}
	if cov.Files[0].AvgDismissConf != 85 {
		t.Errorf("a.go avg dismiss conf: want 85, got %d", cov.Files[0].AvgDismissConf)
	}
	if cov.Files[1].File != "b.go" || cov.Files[1].Findings != 0 {
		t.Errorf("b.go should have no findings; got file=%q findings=%d", cov.Files[1].File, cov.Files[1].Findings)
	}
	if cov.Files[1].AvgDismissConf != 65 {
		t.Errorf("b.go avg dismiss conf: want 65, got %d", cov.Files[1].AvgDismissConf)
	}
}

func TestBuildCoverage_OrphanFiles(t *testing.T) {
	aoiScan := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: []security.AreaOfInterest{{ID: "a1"}}},
	}
	// c.go is in scope but produced no AOIs → orphan.
	cov := BuildCoverage(aoiScan, nil, nil, nil, []string{"a.go", "b.go", "c.go"}, nil)
	if cov == nil {
		t.Fatal("expected non-nil coverage")
	}
	if len(cov.OrphanFiles) != 2 {
		t.Fatalf("expected 2 orphans (b.go + c.go), got %v", cov.OrphanFiles)
	}
	// Orphan list must be sorted for stable output.
	if cov.OrphanFiles[0] != "b.go" || cov.OrphanFiles[1] != "c.go" {
		t.Errorf("orphans not sorted: %v", cov.OrphanFiles)
	}
	if cov.FilesInScope != 3 {
		t.Errorf("FilesInScope: want 3, got %d", cov.FilesInScope)
	}
	if cov.FilesWithAOIs != 1 {
		t.Errorf("FilesWithAOIs: want 1, got %d", cov.FilesWithAOIs)
	}
}

func TestBuildCoverage_FailedAOIsAttributedToFile(t *testing.T) {
	aoiScan := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: []security.AreaOfInterest{
			{ID: "a1"}, {ID: "a2"},
		}},
		{File: "b.go", AreasOfInterest: []security.AreaOfInterest{
			{ID: "b1"},
		}},
	}
	cov := BuildCoverage(aoiScan, nil, nil, []string{"a1", "b1"}, []string{"a.go", "b.go"}, nil)
	if cov == nil {
		t.Fatal("expected non-nil coverage")
	}
	var aFile, bFile state.FileCoverage
	for _, fc := range cov.Files {
		switch fc.File {
		case "a.go":
			aFile = fc
		case "b.go":
			bFile = fc
		}
	}
	if aFile.Failed != 1 {
		t.Errorf("a.go failed: want 1, got %d", aFile.Failed)
	}
	if bFile.Failed != 1 {
		t.Errorf("b.go failed: want 1, got %d", bFile.Failed)
	}
}

func TestBuildCoverage_SystemicFindingCreditsEachAffectedFile(t *testing.T) {
	// Systemic findings have File == "multiple" but their AffectedSites
	// list the real files. The recheck pass already consolidated the
	// constituent per-file findings away, so without per-affected-site
	// credit, files with a real systemic concern would look untouched.
	aoiScan := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: []security.AreaOfInterest{{ID: "a1"}}},
		{File: "b.go", AreasOfInterest: []security.AreaOfInterest{{ID: "b1"}}},
	}
	findings := []state.DeepFinding{{
		File:     "multiple",
		Severity: "high",
		Systemic: true,
		AffectedSites: []state.SiteRef{
			{File: "a.go"},
			{File: "b.go"},
		},
	}}

	cov := BuildCoverage(aoiScan, findings, nil, nil, []string{"a.go", "b.go"}, nil)
	if cov == nil || len(cov.Files) != 2 {
		t.Fatalf("expected 2 files in coverage; got %+v", cov)
	}
	byFile := map[string]*state.FileCoverage{}
	for i := range cov.Files {
		byFile[cov.Files[i].File] = &cov.Files[i]
	}
	for _, name := range []string{"a.go", "b.go"} {
		f := byFile[name]
		if f == nil {
			t.Fatalf("missing coverage row for %s", name)
		}
		if f.Findings != 1 {
			t.Errorf("%s: findings = %d, want 1", name, f.Findings)
		}
		if f.MaxFindingSeverity != "high" {
			t.Errorf("%s: max severity = %q, want high", name, f.MaxFindingSeverity)
		}
	}
}

func TestBuildCoverage_EmptyInputsReturnsNil(t *testing.T) {
	cov := BuildCoverage(nil, nil, nil, nil, nil, nil)
	if cov != nil {
		t.Errorf("expected nil coverage on empty inputs, got %+v", cov)
	}
}

func TestBuildCoverage_UnknownDismissConfidenceSkipped(t *testing.T) {
	aoiScan := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: []security.AreaOfInterest{{ID: "a1"}}},
	}
	dismissals := []state.DeepDismissal{
		{File: "a.go", ConfidenceScore: 0},  // legacy entry, no confidence
		{File: "a.go", ConfidenceScore: 80}, // counted
	}
	cov := BuildCoverage(aoiScan, nil, dismissals, nil, []string{"a.go"}, nil)
	if cov == nil || len(cov.Files) != 1 {
		t.Fatalf("expected 1 file in coverage, got %+v", cov)
	}
	if cov.Files[0].AvgDismissConf != 80 {
		t.Errorf("avg should skip zero-confidence entries; want 80, got %d", cov.Files[0].AvgDismissConf)
	}
}

func TestBuildCoverage_SortOrder(t *testing.T) {
	aoiScan := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: []security.AreaOfInterest{{ID: "a1"}}},
		{File: "b.go", AreasOfInterest: []security.AreaOfInterest{{ID: "b1"}}},
		{File: "c.go", AreasOfInterest: []security.AreaOfInterest{{ID: "c1"}}},
	}
	findings := []state.DeepFinding{
		{File: "c.go", Severity: "low"},
		{File: "a.go", Severity: "high"},
	}
	dismissals := []state.DeepDismissal{
		{File: "b.go", ConfidenceScore: 90},
	}
	cov := BuildCoverage(aoiScan, findings, dismissals, nil, []string{"a.go", "b.go", "c.go"}, nil)
	if cov == nil || len(cov.Files) != 3 {
		t.Fatalf("expected 3 files, got %+v", cov)
	}
	// a.go (high) → c.go (low) → b.go (dismissals only)
	wantOrder := []string{"a.go", "c.go", "b.go"}
	for i, want := range wantOrder {
		if cov.Files[i].File != want {
			t.Errorf("Files[%d]: want %q, got %q (full order: %v)", i, want, cov.Files[i].File, fileOrder(cov.Files))
		}
	}
}

func TestBuildCoverage_SystemicFindingsExcluded(t *testing.T) {
	aoiScan := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: []security.AreaOfInterest{{ID: "a1"}}},
	}
	findings := []state.DeepFinding{
		// Systemic finding emerges from synthesis with File="multiple";
		// counting it per-file would inflate a.go's findings count
		// AND create a phantom "multiple" row.
		{File: "multiple", Severity: "high", Systemic: true},
		{File: "a.go", Severity: "medium"},
	}
	cov := BuildCoverage(aoiScan, findings, nil, nil, []string{"a.go"}, nil)
	if cov == nil {
		t.Fatal("expected non-nil coverage")
	}
	for _, fc := range cov.Files {
		if fc.File == "multiple" {
			t.Errorf("systemic finding leaked into per-file rows: %+v", fc)
		}
	}
	// a.go should show its real, non-systemic finding count.
	if len(cov.Files) != 1 || cov.Files[0].File != "a.go" || cov.Files[0].Findings != 1 {
		t.Errorf("a.go: want 1 finding, got %+v", cov.Files)
	}
}

func TestBuildCoverage_MaxFindingSeverityForNitFinding(t *testing.T) {
	aoiScan := []security.AOIScanResult{
		{File: "a.go", AreasOfInterest: []security.AreaOfInterest{{ID: "a1"}}},
	}
	findings := []state.DeepFinding{{File: "a.go", Severity: "nit"}}
	cov := BuildCoverage(aoiScan, findings, nil, nil, []string{"a.go"}, nil)
	if cov == nil || len(cov.Files) != 1 {
		t.Fatalf("expected 1 file in coverage, got %+v", cov)
	}
	if cov.Files[0].MaxFindingSeverity != "nit" {
		t.Errorf("MaxFindingSeverity for nit finding: want %q, got %q",
			"nit", cov.Files[0].MaxFindingSeverity)
	}
}

// ── Skipped files (--review-mode=aoi-only) ──────────────────────────────

func TestBuildCoverage_SkippedFilesSurfaced(t *testing.T) {
	cov := BuildCoverage(nil, nil, nil, nil, nil, []string{"c.go", "b.go", "a.go"})
	if cov == nil {
		t.Fatal("expected non-nil coverage when skipped files are present")
	}
	want := []string{"a.go", "b.go", "c.go"} // sorted
	if len(cov.SkippedFiles) != len(want) {
		t.Fatalf("SkippedFiles: want %d entries, got %d (%v)", len(want), len(cov.SkippedFiles), cov.SkippedFiles)
	}
	for i, p := range want {
		if cov.SkippedFiles[i] != p {
			t.Errorf("SkippedFiles[%d]: want %q, got %q", i, p, cov.SkippedFiles[i])
		}
	}
}

func TestBuildCoverage_SkippedFilesDedup(t *testing.T) {
	cov := BuildCoverage(nil, nil, nil, nil, nil, []string{"a.go", "a.go", "b.go", "a.go", ""})
	if cov == nil {
		t.Fatal("expected non-nil coverage")
	}
	want := []string{"a.go", "b.go"}
	if len(cov.SkippedFiles) != len(want) {
		t.Fatalf("SkippedFiles: want %d, got %d (%v)", len(want), len(cov.SkippedFiles), cov.SkippedFiles)
	}
	for i, p := range want {
		if cov.SkippedFiles[i] != p {
			t.Errorf("SkippedFiles[%d]: want %q, got %q", i, p, cov.SkippedFiles[i])
		}
	}
}

func TestBuildCoverage_SkippedAndOrphansDistinct(t *testing.T) {
	// File present in scope with zero AOIs → orphan.
	// File listed in skippedFiles but not in scope → still surfaces.
	cov := BuildCoverage(
		nil,
		nil,
		nil,
		nil,
		[]string{"orphan.go"},
		[]string{"skipped.go"},
	)
	if cov == nil {
		t.Fatal("expected non-nil coverage")
	}
	if len(cov.OrphanFiles) != 1 || cov.OrphanFiles[0] != "orphan.go" {
		t.Errorf("OrphanFiles: want [orphan.go], got %v", cov.OrphanFiles)
	}
	if len(cov.SkippedFiles) != 1 || cov.SkippedFiles[0] != "skipped.go" {
		t.Errorf("SkippedFiles: want [skipped.go], got %v", cov.SkippedFiles)
	}
}

func fileOrder(files []state.FileCoverage) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.File
	}
	return out
}

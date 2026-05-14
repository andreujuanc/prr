package security

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

// captureLog redirects log output to a buffer for assertion. Restores
// the original writer on test exit.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

// ── validateAOIs ────────────────────────────────────────────────────────

func TestValidateAOIs_LogsMissingCategory(t *testing.T) {
	buf := captureLog(t)

	results := []AOIScanResult{
		{
			File: "a.go",
			AreasOfInterest: []AreaOfInterest{
				{ID: "a-go-1", Line: 10, Category: ""},
			},
		},
	}
	validateAOIs(results)

	out := buf.String()
	if !strings.Contains(out, "missing category") {
		t.Errorf("expected 'missing category' log; got: %q", out)
	}
	if !strings.Contains(out, "a.go") {
		t.Errorf("log should name the file; got: %q", out)
	}
	if !strings.Contains(out, "a-go-1") {
		t.Errorf("log should name the AOI id for grep-ability; got: %q", out)
	}
}

func TestValidateAOIs_LogsOutOfTaxonomyCategory(t *testing.T) {
	buf := captureLog(t)

	results := []AOIScanResult{
		{
			File: "x.go",
			AreasOfInterest: []AreaOfInterest{
				{ID: "x-go-1", Line: 5, Category: "shitposting"},
			},
		},
	}
	validateAOIs(results)

	out := buf.String()
	if !strings.Contains(out, "out-of-taxonomy") {
		t.Errorf("expected 'out-of-taxonomy' log; got: %q", out)
	}
	if !strings.Contains(out, `"shitposting"`) {
		t.Errorf("log should include the invalid category verbatim; got: %q", out)
	}
}

func TestValidateAOIs_AcceptsValidCategory(t *testing.T) {
	buf := captureLog(t)

	// "error-handling" is a real dimension in /workspace/internal/ai/prompts/dimensions
	results := []AOIScanResult{
		{
			File: "a.go",
			AreasOfInterest: []AreaOfInterest{
				{ID: "a-1", Line: 1, Category: "error-handling"},
			},
		},
	}
	validateAOIs(results)

	out := buf.String()
	if strings.Contains(out, "out-of-taxonomy") || strings.Contains(out, "missing category") {
		t.Errorf("valid category should not produce a category warning; got: %q", out)
	}
}

func TestValidateAOIs_LogsUnknownDimension(t *testing.T) {
	buf := captureLog(t)

	results := []AOIScanResult{
		{
			File: "y.go",
			AreasOfInterest: []AreaOfInterest{
				{
					ID:         "y-1",
					Line:       2,
					Category:   "correctness", // valid
					Dimensions: []string{"correctness", "made-up-dim"},
				},
			},
		},
	}
	validateAOIs(results)

	out := buf.String()
	if !strings.Contains(out, "unknown dimension") {
		t.Errorf("expected 'unknown dimension' log; got: %q", out)
	}
	if !strings.Contains(out, `"made-up-dim"`) {
		t.Errorf("log should include the invalid dimension verbatim; got: %q", out)
	}
	// The valid dimension shouldn't appear in the warning text — verifies
	// we don't log every dimension.
	if strings.Contains(out, `"correctness"`) {
		t.Errorf("valid dimension should not appear in warning logs; got: %q", out)
	}
}

func TestValidateAOIs_LogsDuplicateIDsWithinFile(t *testing.T) {
	buf := captureLog(t)

	// Duplicate IDs within the SAME file corrupt the cache and any
	// cross-references. The validator must catch them once (on the
	// 2nd occurrence) without spamming for triples.
	results := []AOIScanResult{
		{
			File: "a.go",
			AreasOfInterest: []AreaOfInterest{
				{ID: "dupe-id", Line: 1, Category: "correctness"},
				{ID: "dupe-id", Line: 2, Category: "correctness"},
				{ID: "dupe-id", Line: 3, Category: "correctness"}, // triple shouldn't double-log
				{ID: "unique-id", Line: 4, Category: "correctness"},
			},
		},
	}
	validateAOIs(results)

	out := buf.String()
	dupeCount := strings.Count(out, "duplicate AOI id")
	if dupeCount != 1 {
		t.Errorf("expected exactly 1 'duplicate AOI id' log line (no spam for triples); got %d:\n%s",
			dupeCount, out)
	}
}

func TestValidateAOIs_DuplicateIDsAcrossFilesAreFine(t *testing.T) {
	buf := captureLog(t)

	// Same ID in TWO different files is fine — IDs are file-scoped.
	// A duplicate warning here would be a false positive.
	results := []AOIScanResult{
		{File: "a.go", AreasOfInterest: []AreaOfInterest{{ID: "shared-1", Line: 1, Category: "correctness"}}},
		{File: "b.go", AreasOfInterest: []AreaOfInterest{{ID: "shared-1", Line: 1, Category: "correctness"}}},
	}
	validateAOIs(results)

	out := buf.String()
	if strings.Contains(out, "duplicate AOI id") {
		t.Errorf("same id across files should not warn (file-scoped); got: %q", out)
	}
}

// ── Empty-audit warning ────────────────────────────────────────────────

func TestScanAreasOfInterestClassified_WarnsOnZeroAOIsInAuditMode(t *testing.T) {
	// In audit mode, returning zero AOIs across all batches is almost
	// always a broken model or a prompt regression — real codebases
	// have something worth flagging. We must surface this clearly.
	client := &stubClient{
		responses: []string{
			`[{"file": "a.go", "areas": []}]`, // valid response, but empty AOIs
		},
	}
	rawDiffs := map[string]string{"a.go": "=== a.go ===\npackage a\n"}

	var progress []string
	report, err := ScanAreasOfInterestClassified(
		context.Background(), client, rawDiffs, nil, nil,
		func(s string) { progress = append(progress, s) },
		nil, true, // auditMode = true
	)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.TotalAOIs != 0 {
		t.Fatalf("test setup wrong; want 0 AOIs, got %d", report.TotalAOIs)
	}

	var sawWarning bool
	for _, p := range progress {
		if strings.Contains(p, "0 AOIs") {
			sawWarning = true
			break
		}
	}
	if !sawWarning {
		t.Errorf("expected empty-audit warning in onProgress; got: %v", progress)
	}
}

func TestScanAreasOfInterestClassified_NoEmptyWarningInPRMode(t *testing.T) {
	// PR mode is different: a clean diff legitimately produces zero
	// AOIs (the changes are fine). The warning is audit-mode-only.
	client := &stubClient{
		responses: []string{`[{"file": "a.go", "areas": []}]`},
	}
	rawDiffs := map[string]string{"a.go": "=== a.go ===\npackage a\n"}

	var progress []string
	_, err := ScanAreasOfInterestClassified(
		context.Background(), client, rawDiffs, nil, nil,
		func(s string) { progress = append(progress, s) },
		nil, false, // auditMode = false
	)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	for _, p := range progress {
		if strings.Contains(p, "0 AOIs") {
			t.Errorf("PR mode should not warn on zero AOIs (clean diff is valid); got: %q", p)
		}
	}
}

func TestScanAreasOfInterestClassified_NoEmptyWarningWhenAOIsFound(t *testing.T) {
	// Sanity: when AOIs are returned, no empty-audit warning.
	client := &stubClient{
		responses: []string{
			`[{"file": "a.go", "areas": [{"id": "a-1", "line": 1, "category": "correctness", "subcategory": "off-by-one", "urgency": "grouped", "concern": "x", "context": "y", "dimensions": ["correctness"]}]}]`,
		},
	}
	rawDiffs := map[string]string{"a.go": "=== a.go ===\npackage a\n"}

	var progress []string
	report, err := ScanAreasOfInterestClassified(
		context.Background(), client, rawDiffs, nil, nil,
		func(s string) { progress = append(progress, s) },
		nil, true,
	)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.TotalAOIs == 0 {
		t.Fatal("test setup: expected at least 1 AOI")
	}

	for _, p := range progress {
		if strings.Contains(p, "0 AOIs") {
			t.Errorf("should not warn when AOIs were found; got: %q", p)
		}
	}
}

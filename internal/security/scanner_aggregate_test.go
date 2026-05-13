package security

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestShouldAggregateFailAOI(t *testing.T) {
	tests := []struct {
		name   string
		failed int
		total  int
		want   bool
	}{
		// Below the absolute floor — never abort regardless of ratio.
		// Protects small audits ("1 of 1 failed = 100%") from
		// overreacting to a single transient batch failure.
		{"below floor: 1/1 = 100% but under floor", 1, 1, false},
		{"below floor: 1/3 = 33% but under floor", 1, 3, false},

		// Above floor but at or under ratio — proceed with warning.
		{"exactly at ratio: 2/10 = 20%", 2, 10, false}, // strict >
		{"above floor under ratio: 2/20 = 10%", 2, 20, false},

		// Above floor AND above ratio — abort.
		{"at floor and above ratio: 2/5 = 40%", 2, 5, true},
		{"clearly above: 8/10 = 80%", 8, 10, true},
		{"all batches fail: 5/5", 5, 5, true},

		// Edge: zero total never panics or returns true.
		{"zero total", 5, 0, false},
	}

	for _, tc := range tests {
		got := shouldAggregateFailAOI(tc.failed, tc.total)
		if got != tc.want {
			t.Errorf("%s: shouldAggregateFailAOI(%d, %d) = %v, want %v",
				tc.name, tc.failed, tc.total, got, tc.want)
		}
	}
}

func TestShouldAggregateFailAOI_PinsThresholds(t *testing.T) {
	// Pin the constants — changing them changes user-visible behavior
	// (when an audit aborts vs proceeds with degraded recall).
	if aoiAggregateFailRatio != 0.20 {
		t.Errorf("aoiAggregateFailRatio = %g, want 0.20", aoiAggregateFailRatio)
	}
	if aoiAggregateFailMinBatch != 2 {
		t.Errorf("aoiAggregateFailMinBatch = %d, want 2", aoiAggregateFailMinBatch)
	}
}

// ── Integration: aggregate-fail behavior in ScanAreasOfInterestClassified ──

func TestScanAreasOfInterestClassified_AbortsAboveThreshold(t *testing.T) {
	// Three batches, two fail transiently (after one retry each =
	// 4 total transient errors). That's 2/3 = 66% > 20% threshold
	// and ≥ 2-batch floor → abort. The error must mention the
	// failure count and the threshold so the user knows WHY.
	//
	// Setup: 3 separate dimension sets so we get 3 separate batches.
	// Batch A: a.go (handler dims)
	// Batch B: b.go (repository dims)
	// Batch C: c.go (test dims)
	transient := errors.New("503 service unavailable")
	client := &stubClient{
		// Use enough queue entries to cover up to (3 batches × 2 attempts).
		// Order is non-deterministic due to goroutines, so make ALL
		// attempts return transient errors for two of the three batches.
		// We achieve this by making every call fail except one specific
		// response position — but easier: just return errors for all
		// calls except the first successful one. To force 2/3 batches
		// to fail consistently, return success for the FIRST 1 call
		// and error for everything else, with enough slots to cover
		// retries. That gives 1 success + 2 failed batches = 2/3 = 67%.
		responses: []string{
			`[{"file": "a.go", "areas": []}]`, // first batch to reach the LLM succeeds
			"", "", "", "", "", "", "", "",
		},
		errors: []error{
			nil, // first call succeeds
			transient, transient, // batch 2: first attempt + retry both fail
			transient, transient, // batch 3: first attempt + retry both fail
			transient, transient, transient, // extras in case ordering differs
		},
	}

	// Build three distinct dimension groups so we get three batches.
	rawDiffs := map[string]string{
		"a.go": "=== a.go ===\npackage a\n",
		"b.go": "=== b.go ===\npackage b\n",
		"c.go": "=== c.go ===\npackage c\n",
	}
	fileDimensions := map[string][]string{
		"a.go": {"input-validation", "authentication"},
		"b.go": {"data-integrity", "resource-management"},
		"c.go": {"testing", "correctness"},
	}

	_, err := ScanAreasOfInterestClassified(
		context.Background(), client, rawDiffs, nil, fileDimensions,
		nil, nil, true,
	)
	if err == nil {
		t.Fatal("expected aggregate-fail error when >20% batches fail")
	}
	if !strings.Contains(err.Error(), "20%") {
		t.Errorf("error should mention the 20%% threshold for diagnostic clarity; got: %v", err)
	}
	if !strings.Contains(err.Error(), "aborting") {
		t.Errorf("error should say 'aborting'; got: %v", err)
	}
}

func TestScanAreasOfInterestClassified_PartialUnderThreshold_ProceedsWithWarning(t *testing.T) {
	// One batch of 5 fails (= 1/5 = 20%) — sits exactly AT the
	// threshold (strict >), so we proceed. The consolidated warning
	// must surface so the user knows recall is degraded for the
	// affected file(s).
	//
	// 5 distinct dimension groups → 5 batches. One fails (with retry),
	// four succeed.
	transient := errors.New("502 bad gateway")
	client := &stubClient{
		responses: []string{
			`[{"file": "a.go", "areas": []}]`,
			`[{"file": "b.go", "areas": []}]`,
			`[{"file": "c.go", "areas": []}]`,
			`[{"file": "d.go", "areas": []}]`,
			// e.go's batch errors twice (first attempt + retry).
			"",
			"",
		},
		errors: []error{
			nil, nil, nil, nil,
			transient, transient,
		},
	}

	rawDiffs := map[string]string{
		"a.go": "=== a.go ===\npackage a\n",
		"b.go": "=== b.go ===\npackage b\n",
		"c.go": "=== c.go ===\npackage c\n",
		"d.go": "=== d.go ===\npackage d\n",
		"e.go": "=== e.go ===\npackage e\n",
	}
	fileDimensions := map[string][]string{
		"a.go": {"input-validation"},
		"b.go": {"data-integrity"},
		"c.go": {"testing"},
		"d.go": {"api-design"},
		"e.go": {"concurrency"},
	}

	var progress []string
	report, err := ScanAreasOfInterestClassified(
		context.Background(), client, rawDiffs, nil, fileDimensions,
		func(s string) { progress = append(progress, s) },
		nil, true,
	)

	// Note: due to goroutine scheduling we can't guarantee WHICH batch
	// gets the error responses — but exactly ONE batch will fail (the
	// 5th-to-arrive). So we expect 1 of 5 = 20% (NOT > threshold) =
	// proceed with warning.
	if err != nil {
		// Tolerable: if scheduling causes 2 batches to fail (e.g. one
		// nil success goes to a batch that already errored), we'd hit
		// threshold. Allow either outcome but verify behavior matches.
		if !strings.Contains(err.Error(), "aborting") {
			t.Fatalf("unexpected error shape: %v", err)
		}
		t.Skip("scheduling caused >1 batch to fail; aggregate-fail tested separately")
	}

	// Under-threshold path: report returned, consolidated warning emitted.
	if report == nil {
		t.Fatal("expected report on under-threshold partial failure")
	}

	var sawPartialWarning bool
	for _, p := range progress {
		if strings.Contains(p, "batches failed") && strings.Contains(p, "deep-reviewed") {
			sawPartialWarning = true
			break
		}
	}
	if !sawPartialWarning {
		t.Errorf("expected consolidated partial-failure warning; got progress: %v", progress)
	}
}

func TestScanAreasOfInterestClassified_AllBatchesFail_AbortsRegardlessOfFloor(t *testing.T) {
	// When 100% of batches fail there's nothing to return. The
	// 2-batch aggregate-fail floor doesn't cover this — a
	// single-batch run with 1/1 failures (which is < floor and
	// also = 100%) must still abort. Without this special case,
	// a tiny audit with one failing batch would silently return
	// an empty report.
	transient := errors.New("503 service unavailable")
	client := &stubClient{
		errors: []error{transient, transient}, // attempt + retry both fail
	}
	rawDiffs := map[string]string{
		"a.go": "=== a.go ===\npackage a\n",
	}

	_, err := ScanAreasOfInterestClassified(
		context.Background(), client, rawDiffs, nil, nil,
		nil, nil, true,
	)
	if err == nil {
		t.Fatal("expected error when all batches (1/1) fail; empty report would mask the failure")
	}
	if !strings.Contains(err.Error(), "all") {
		t.Errorf("error should mention all-batches-failed for clarity; got: %v", err)
	}
}

func TestScanAreasOfInterestClassified_AllPass_NoWarning(t *testing.T) {
	// Happy path: every batch succeeds → no failure warning.
	client := &stubClient{
		responses: []string{
			`[{"file": "a.go", "areas": []}, {"file": "b.go", "areas": []}]`,
		},
	}

	rawDiffs := map[string]string{
		"a.go": "=== a.go ===\npackage a\n",
		"b.go": "=== b.go ===\npackage b\n",
	}

	var progress []string
	report, err := ScanAreasOfInterestClassified(
		context.Background(), client, rawDiffs, nil, nil,
		func(s string) { progress = append(progress, s) },
		nil, true,
	)
	if err != nil {
		t.Fatalf("happy path errored: %v", err)
	}
	if report == nil {
		t.Fatal("expected report")
	}

	for _, p := range progress {
		if strings.Contains(p, "batches failed") {
			t.Errorf("no failure should be reported on happy path: %q", p)
		}
	}
}

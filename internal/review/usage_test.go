package review

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/ai"
)

func TestPhaseUsage_TotalSumsEveryPhase(t *testing.T) {
	u := PhaseUsage{
		Discovery: ai.TokenUsage{InputTokens: 1, OutputTokens: 10, Calls: 1},
		Classify:  ai.TokenUsage{InputTokens: 2, OutputTokens: 20, Calls: 2},
		AOI:       ai.TokenUsage{InputTokens: 4, OutputTokens: 40, Calls: 4},
		Review:    ai.TokenUsage{InputTokens: 8, OutputTokens: 80, Calls: 8, TruncatedCalls: 3},
		Recheck:   ai.TokenUsage{InputTokens: 16, OutputTokens: 160, Calls: 16},
		Synth:     ai.TokenUsage{InputTokens: 32, OutputTokens: 320, Calls: 32},
	}
	got := u.Total()

	// Powers of two: any phase dropped from the sum shows up as a
	// specific missing bit rather than a plausible-looking wrong total.
	if got.InputTokens != 63 {
		t.Errorf("InputTokens = %d, want 63 (a phase is missing from Total)", got.InputTokens)
	}
	if got.OutputTokens != 630 {
		t.Errorf("OutputTokens = %d, want 630", got.OutputTokens)
	}
	if got.Calls != 63 {
		t.Errorf("Calls = %d, want 63", got.Calls)
	}
	if got.TruncatedCalls != 3 {
		t.Errorf("TruncatedCalls = %d, want 3", got.TruncatedCalls)
	}
}

// ReportedCostUSD was dropped by the previous Total() implementation,
// so a provider that reports cost natively totalled to $0.
func TestPhaseUsage_TotalCarriesReportedCost(t *testing.T) {
	u := PhaseUsage{
		AOI:    ai.TokenUsage{ReportedCostUSD: 0.25},
		Review: ai.TokenUsage{ReportedCostUSD: 0.75},
	}
	if got := u.Total().ReportedCostUSD; got != 1.0 {
		t.Errorf("ReportedCostUSD = %v, want 1.0", got)
	}
}

// The fast/strong split is the point of the per-phase breakdown: on the
// default config both refs are the same model, so only this split shows
// how much of a run the pre-scan lane accounts for.
func TestPhaseUsage_FastStrongSplit(t *testing.T) {
	u := PhaseUsage{
		Discovery: ai.TokenUsage{Calls: 1},
		Classify:  ai.TokenUsage{Calls: 2},
		AOI:       ai.TokenUsage{Calls: 4},
		Review:    ai.TokenUsage{Calls: 8},
		Recheck:   ai.TokenUsage{Calls: 16},
		Synth:     ai.TokenUsage{Calls: 32},
	}
	if got := u.FastPhases().Calls; got != 7 {
		t.Errorf("FastPhases calls = %d, want 7 (discovery+classify+aoi)", got)
	}
	if got := u.StrongPhases().Calls; got != 56 {
		t.Errorf("StrongPhases calls = %d, want 56 (review+recheck+synth)", got)
	}
	if fast, strong, total := u.FastPhases().Calls, u.StrongPhases().Calls, u.Total().Calls; fast+strong != total {
		t.Errorf("lanes must partition the total: %d + %d != %d", fast, strong, total)
	}
}

// A phase that never ran must not print a row of zeros — that would
// read as "discovery ran and cost nothing", which is a different claim
// from "discovery was served from cache".
func TestPhaseUsage_FormatSummaryOmitsUnusedPhases(t *testing.T) {
	u := PhaseUsage{Review: ai.TokenUsage{InputTokens: 5000, OutputTokens: 1000, Calls: 4}}
	out := u.FormatSummary()

	if !strings.Contains(out, "review") {
		t.Errorf("summary missing the phase that ran:\n%s", out)
	}
	for _, absent := range []string{"discovery", "classify", "aoi", "recheck", "synthesis"} {
		if strings.Contains(out, absent) {
			t.Errorf("summary lists phase %q that made no calls:\n%s", absent, out)
		}
	}
	if !strings.Contains(out, "TOTAL") {
		t.Errorf("summary missing TOTAL row:\n%s", out)
	}
}

func TestPhaseUsage_FormatSummaryFlagsTruncation(t *testing.T) {
	u := PhaseUsage{Review: ai.TokenUsage{Calls: 4, TruncatedCalls: 2}}
	if out := u.FormatSummary(); !strings.Contains(out, "truncated") {
		t.Errorf("truncated calls must be visible in the summary:\n%s", out)
	}
}

func TestPhaseUsage_MarshalMapShape(t *testing.T) {
	u := PhaseUsage{
		AOI:    ai.TokenUsage{InputTokens: 100, OutputTokens: 10, Calls: 2},
		Review: ai.TokenUsage{InputTokens: 900, OutputTokens: 90, Calls: 3},
	}
	m := u.MarshalMap()

	if _, ok := m["discovery"]; ok {
		t.Error("MarshalMap included a phase that made no calls")
	}
	total, ok := m["total"].(map[string]any)
	if !ok {
		t.Fatalf("total missing or wrong type: %#v", m["total"])
	}
	if total["calls"] != 5 {
		t.Errorf("total calls = %v, want 5", total["calls"])
	}

	// Must survive a real marshal — the map is written to disk.
	if _, err := json.Marshal(m); err != nil {
		t.Fatalf("MarshalMap output is not JSON-serialisable: %v", err)
	}
}

// A fully-cached run spends nothing, and the snapshot must not claim a
// spend that didn't happen.
func TestMarshalResultJSON_OmitsUsageWhenNothingSpent(t *testing.T) {
	b, err := MarshalResultJSON(nil, 3, nil, nil, PhaseUsage{})
	if err != nil {
		t.Fatalf("MarshalResultJSON: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["usage"]; ok {
		t.Errorf("usage present for a zero-spend run: %s", b)
	}
}

func TestMarshalResultJSON_IncludesUsageWhenSpent(t *testing.T) {
	u := PhaseUsage{Review: ai.TokenUsage{InputTokens: 10, Calls: 1}}
	b, err := MarshalResultJSON(nil, 3, nil, nil, u)
	if err != nil {
		t.Fatalf("MarshalResultJSON: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["usage"]; !ok {
		t.Errorf("usage missing for a run that made calls: %s", b)
	}
}

// Cache reads must be visible in the summary. Without them the only
// input figure on screen is InputTokens, which is uncached input plus
// cache-creation -- so a cold-cache run looks like a cost regression and
// a warm one looks nearly free. Both readings are wrong.
func TestPhaseUsage_FormatSummaryShowsCacheReads(t *testing.T) {
	u := PhaseUsage{Review: ai.TokenUsage{
		InputTokens: 8000, CacheHits: 190000, OutputTokens: 4000, Calls: 3,
	}}
	out := u.FormatSummary()

	if !strings.Contains(out, "cache") {
		t.Errorf("summary hides cache reads:\n%s", out)
	}
	// 190000/1000 = 190 -- the dominant figure must actually be rendered,
	// not just the column header.
	if !strings.Contains(out, "190") {
		t.Errorf("cache-read value not rendered:\n%s", out)
	}
	// The input column must not be labelled as plain "in": it includes
	// cache-creation tokens and calling it "in" is what caused a wrong
	// conclusion once already.
	if strings.Contains(out, "k in ") {
		t.Errorf("input column mislabelled as \"in\" (it is input+cache-creation):\n%s", out)
	}
}

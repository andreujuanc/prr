package review

import (
	"fmt"

	"github.com/andreujuanc/prr/internal/ai"
)

// PhaseUsage holds per-phase token usage for cost reporting. Shared by
// the PR-review and audit pipelines — audit leaves Discovery/Classify
// zero (it has no PR brief and classifies inside its own AOI phase).
//
// Phases are snapshotted in pipeline order via ai.SnapshotUsage, which
// resets the client's counters on read. That is what lets Discovery,
// Classify and AOI report separately despite sharing one fast client:
// they run sequentially, so each snapshot drains only its own phase.
// Snapshotting out of order, or twice for one phase, silently attributes
// tokens to the wrong bucket.
type PhaseUsage struct {
	Discovery ai.TokenUsage // Phase 0: project context + PR brief
	Classify  ai.TokenUsage // Phase 0b: per-file classification
	AOI       ai.TokenUsage // Phase 2: AOI pre-scan
	Review    ai.TokenUsage // Phase 3: deep review
	Recheck   ai.TokenUsage // Phase 3b: recheck / dedup
	Synth     ai.TokenUsage // Phase 4: synthesis
}

// phases returns the per-phase entries in pipeline order, for
// aggregation and rendering. Keeping one ordered list here means adding
// a phase touches this function and nothing else.
func (u PhaseUsage) phases() []struct {
	Name  string
	Usage ai.TokenUsage
} {
	return []struct {
		Name  string
		Usage ai.TokenUsage
	}{
		{"discovery", u.Discovery},
		{"classify", u.Classify},
		{"aoi", u.AOI},
		{"review", u.Review},
		{"recheck", u.Recheck},
		{"synthesis", u.Synth},
	}
}

// Total returns aggregate usage across all phases.
func (u PhaseUsage) Total() ai.TokenUsage {
	var t ai.TokenUsage
	for _, p := range u.phases() {
		t.InputTokens += p.Usage.InputTokens
		t.OutputTokens += p.Usage.OutputTokens
		t.CacheHits += p.Usage.CacheHits
		t.ReportedCostUSD += p.Usage.ReportedCostUSD
		t.Calls += p.Usage.Calls
		t.TruncatedCalls += p.Usage.TruncatedCalls
	}
	return t
}

// FastPhases returns the phases billed to the fast model, aggregated.
// Splitting fast from strong is what makes the fast lane's share of a
// run visible — on a default config both models are the same, so a
// single total hides it entirely.
func (u PhaseUsage) FastPhases() ai.TokenUsage {
	return sumUsage(u.Discovery, u.Classify, u.AOI)
}

// StrongPhases returns the phases billed to the strong model, aggregated.
func (u PhaseUsage) StrongPhases() ai.TokenUsage {
	return sumUsage(u.Review, u.Recheck, u.Synth)
}

func sumUsage(us ...ai.TokenUsage) ai.TokenUsage {
	var t ai.TokenUsage
	for _, u := range us {
		t.InputTokens += u.InputTokens
		t.OutputTokens += u.OutputTokens
		t.CacheHits += u.CacheHits
		t.ReportedCostUSD += u.ReportedCostUSD
		t.Calls += u.Calls
		t.TruncatedCalls += u.TruncatedCalls
	}
	return t
}

// FormatSummary renders a one-line-per-phase usage breakdown, plus a
// total. Phases with no calls are omitted — a cached run that skipped
// discovery should not print a row of zeros implying it ran.
//
// Calls lead each row deliberately: token counts say how much a run
// cost, but the call count is the number that explains why, and it is
// the one this pipeline's tuning acts on.
//
// The two input columns are separate on purpose, and the header says
// "in+cw" rather than "in" because ai.TokenUsage.InputTokens is
// uncached input PLUS cache-creation tokens (see the claude-code
// provider, which sums them). It is not the total context the model
// read. Any change to the prompt prefix — a config flag, a system-prompt
// edit — forces the cache to be rewritten and makes that column spike,
// which reads as a cost regression when it is really a cold cache.
// "cache" is the cache-READ half, billed at 0.1x, and on a warm run it
// carries the bulk of the context. Judge steady-state cost from cache +
// out; compare in+cw only between runs whose cache was equally warm.
func (u PhaseUsage) FormatSummary() string {
	var out string
	for _, p := range u.phases() {
		if p.Usage.Calls == 0 && p.Usage.InputTokens == 0 {
			continue
		}
		row := fmt.Sprintf("  %-10s %3d calls  %6dk in+cw  %6dk cache  %5dk out",
			p.Name, p.Usage.Calls,
			p.Usage.InputTokens/1000, p.Usage.CacheHits/1000, p.Usage.OutputTokens/1000)
		if p.Usage.TruncatedCalls > 0 {
			row += fmt.Sprintf("  (%d truncated)", p.Usage.TruncatedCalls)
		}
		out += row + "\n"
	}
	t := u.Total()
	out += fmt.Sprintf("  %-10s %3d calls  %6dk in+cw  %6dk cache  %5dk out",
		"TOTAL", t.Calls, t.InputTokens/1000, t.CacheHits/1000, t.OutputTokens/1000)
	return out
}

// MarshalMap renders PhaseUsage for JSON output: one entry per phase
// that actually ran, plus a total. Built explicitly rather than via
// struct tags so the on-disk shape stays snake_case and stable
// independently of the Go field names.
func (u PhaseUsage) MarshalMap() map[string]any {
	out := make(map[string]any, len(u.phases())+1)
	for _, p := range u.phases() {
		if p.Usage.Calls == 0 && p.Usage.InputTokens == 0 {
			continue
		}
		out[p.Name] = usageMap(p.Usage)
	}
	out["total"] = usageMap(u.Total())
	return out
}

func usageMap(u ai.TokenUsage) map[string]any {
	m := map[string]any{
		"calls":         u.Calls,
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
	}
	if u.CacheHits > 0 {
		m["cache_hits"] = u.CacheHits
	}
	if u.ReportedCostUSD > 0 {
		m["reported_cost_usd"] = u.ReportedCostUSD
	}
	if u.TruncatedCalls > 0 {
		m["truncated_calls"] = u.TruncatedCalls
	}
	return m
}

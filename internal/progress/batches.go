package progress

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ParseBatchEvent updates s.Batches from a (phase, message) pair when
// the message matches one of the per-batch lifecycle shapes emitted by
// the review and audit pipelines:
//
//	Batch K: init label="..." files=N kind=aoi-driven|general
//	Batch K: active
//	Batch K: stream bytes=N
//	Batch K: done|cached|failed
//
// K is the 1-based batch number on the wire; stored as 0-based Index.
// Returns true when the message was a batch event (matched or
// unparseable but recognized prefix), so callers can short-circuit
// their own parsers.
func ParseBatchEvent(s *State, message string) bool {
	if !strings.HasPrefix(message, "Batch ") {
		return false
	}
	// "Batch K: payload"
	rest, ok := strings.CutPrefix(message, "Batch ")
	if !ok {
		return false
	}
	colon := strings.Index(rest, ": ")
	if colon < 0 {
		return false
	}
	var oneBased int
	if _, err := fmt.Sscanf(rest[:colon], "%d", &oneBased); err != nil {
		return false
	}
	if oneBased < 1 {
		return false
	}
	idx := oneBased - 1
	payload := rest[colon+2:]

	if s.Batches == nil {
		s.Batches = make(map[int]*BatchState)
	}
	b := s.Batches[idx]
	if b == nil {
		b = &BatchState{Index: idx, Status: BatchQueued}
		s.Batches[idx] = b
	}

	switch {
	case strings.HasPrefix(payload, "init "):
		// init label="<...>" files=N kind=<...> [unit=<...>]
		// Label is quoted so labels can contain spaces or brackets.
		// unit token is optional — recheck emits it ("findings"),
		// deep review doesn't (defaults to "files").
		init := strings.TrimPrefix(payload, "init ")
		label, rest := scanQuotedLabel(init)
		b.Label = label
		var files int
		var kind string
		if n, _ := fmt.Sscanf(rest, " files=%d kind=%s", &files, &kind); n == 2 {
			b.Files = files
			b.Kind = kind
		}
		if i := strings.Index(rest, "unit="); i >= 0 {
			u := strings.TrimSpace(rest[i+len("unit="):])
			if sp := strings.IndexAny(u, " \t"); sp >= 0 {
				u = u[:sp]
			}
			b.Unit = u
		}
		if b.Status == "" {
			b.Status = BatchQueued
		}
		return true

	case payload == "active":
		b.Status = BatchActive
		if b.StartedAt.IsZero() {
			b.StartedAt = time.Now()
		}
		return true

	case strings.HasPrefix(payload, "stream bytes="):
		var n int
		if _, err := fmt.Sscanf(payload, "stream bytes=%d", &n); err == nil {
			b.Bytes = n
		}
		return true

	case payload == "done" || strings.HasPrefix(payload, "done findings="):
		b.Status = BatchDone
		b.EndedAt = time.Now()
		if n, ok := scanTrailingFindings(payload); ok {
			b.Findings = n
		}
		return true

	case payload == "cached" || strings.HasPrefix(payload, "cached findings="):
		b.Status = BatchCached
		// Cached batches skip the active phase and end immediately.
		// Set EndedAt so the recent-completions tail can sort them
		// correctly relative to fresh-done rows.
		if b.EndedAt.IsZero() {
			b.EndedAt = time.Now()
		}
		if n, ok := scanTrailingFindings(payload); ok {
			b.Findings = n
		}
		return true

	case payload == "failed":
		b.Status = BatchFailed
		b.EndedAt = time.Now()
		return true
	}

	return false
}

// isBatchPhase reports whether the given phase name is in
// cfg.BatchPhases. Used by applyEvent to decide whether to clear
// the Batches map on a phase transition.
func (cfg Config) isBatchPhase(name string) bool {
	for _, p := range cfg.BatchPhases {
		if p == name {
			return true
		}
	}
	return false
}

// BatchPanelActive reports whether the panel should render now: at
// least one of cfg.BatchPhases is in PhaseActive status. Used by the
// TUI's View() to gate the section.
func (cfg Config) BatchPanelActive(phases []phaseInfo) bool {
	if len(cfg.BatchPhases) == 0 || len(phases) == 0 {
		return false
	}
	allow := make(map[string]struct{}, len(cfg.BatchPhases))
	for _, name := range cfg.BatchPhases {
		allow[name] = struct{}{}
	}
	for _, p := range phases {
		if p.Status != PhaseActive {
			continue
		}
		if _, ok := allow[p.Def.Name]; ok {
			return true
		}
	}
	return false
}

// BatchPanelOptions configures RenderBatchesPanel.
type BatchPanelOptions struct {
	// MaxActiveRows caps the number of active batches rendered as full
	// detail rows. Overflow becomes a single "+N more active" line.
	// Defaults to 10 when <= 0.
	MaxActiveRows int

	// RecentTail caps the number of recently-finished rows rendered
	// below the active ones. Defaults to 10 when <= 0 — same cap as
	// MaxActiveRows so the panel feels symmetric.
	RecentTail int

	// Animation is a monotonic tick counter used to cycle the
	// indeterminate dotted bar for active batches that haven't
	// streamed any bytes yet. Pass int(elapsed / 100ms) so the
	// animation advances at a steady 10 fps.
	Animation int

	// Now overrides the wall-clock used to compute per-row elapsed
	// timers. Zero defaults to time.Now() — tests pin this so
	// snapshot output is deterministic.
	Now time.Time
}

// BatchByteEstimate seeds the per-batch progress fraction when the
// real per-call answer length is unknown. Median observed responses
// land around ~3k chars; 4000 keeps the bar honest (it fills slower
// than reality and never claims 100% before the terminal event).
const BatchByteEstimate = 4000

// Panel styles. Kept close to the existing TUI palette (sPhaseOn
// blue, sPhaseDone green) so the panel doesn't fight the phase list
// above it.
var (
	bpHeader = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Bold(true)
	bpSubtle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))
	bpDone   = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
	bpFail   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8"))
	bpActive = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA"))
)

// RenderBatchesPanel renders the per-batch panel block:
//
//	Batches  active 4 · done 11 · cached 2 · failed 0 · queued 21
//	  ▶  injection/sql [critical]    2f   0:07  ███░░░░░  28%
//	  ▶  internal/ui                 4f   0:05  ··•·····  streaming
//	  +6 more active
//	  ✓  internal/git                5f   0:14
//	  ✓  internal/config             1f   0:08  cached
//
// Returns "" when no batches have been seen yet.
func RenderBatchesPanel(s *State, opts BatchPanelOptions) string {
	if s == nil || len(s.Batches) == 0 {
		return ""
	}
	if opts.MaxActiveRows <= 0 {
		opts.MaxActiveRows = 10
	}
	if opts.RecentTail <= 0 {
		opts.RecentTail = 10
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	var active, queued, doneTail []*BatchState
	var doneN, cachedN, failedN int
	for _, b := range s.Batches {
		switch b.Status {
		case BatchActive:
			active = append(active, b)
		case BatchQueued, "":
			queued = append(queued, b)
		case BatchDone:
			doneN++
			doneTail = append(doneTail, b)
		case BatchCached:
			cachedN++
			doneTail = append(doneTail, b)
		case BatchFailed:
			failedN++
			doneTail = append(doneTail, b)
		}
	}

	// Active: longest-running first (so the row that's been waiting
	// the most surfaces to the top; cached batches that flipped
	// instantly aren't active so they're not in this list).
	sort.SliceStable(active, func(i, j int) bool {
		return active[i].StartedAt.Before(active[j].StartedAt)
	})
	// Recent completions: most-recent first, capped at RecentTail.
	// finishedOverflow mirrors the +N more active line below so the
	// reader knows the tail is bounded.
	sort.SliceStable(doneTail, func(i, j int) bool {
		return doneTail[i].EndedAt.After(doneTail[j].EndedAt)
	})
	finishedOverflow := 0
	if len(doneTail) > opts.RecentTail {
		finishedOverflow = len(doneTail) - opts.RecentTail
		doneTail = doneTail[:opts.RecentTail]
	}

	var b strings.Builder
	// Header line.
	parts := []string{
		fmt.Sprintf("active %d", len(active)),
		fmt.Sprintf("done %d", doneN),
	}
	if cachedN > 0 {
		parts = append(parts, fmt.Sprintf("cached %d", cachedN))
	}
	if failedN > 0 {
		parts = append(parts, fmt.Sprintf("failed %d", failedN))
	}
	parts = append(parts, fmt.Sprintf("queued %d", len(queued)))
	b.WriteString("  ")
	b.WriteString(bpHeader.Render("Batches"))
	b.WriteString("  ")
	b.WriteString(bpSubtle.Render(strings.Join(parts, " · ")))
	b.WriteString("\n")

	// Active rows.
	visible := active
	overflow := 0
	if len(visible) > opts.MaxActiveRows {
		overflow = len(visible) - opts.MaxActiveRows
		visible = visible[:opts.MaxActiveRows]
	}
	for _, ba := range visible {
		b.WriteString("    ")
		b.WriteString(renderActiveRow(ba, opts, now))
		b.WriteString("\n")
	}
	if overflow > 0 {
		b.WriteString("    ")
		b.WriteString(bpSubtle.Render(fmt.Sprintf("+%d more active", overflow)))
		b.WriteString("\n")
	}

	// Recent completions tail.
	for _, dt := range doneTail {
		b.WriteString("    ")
		b.WriteString(renderFinishedRow(dt))
		b.WriteString("\n")
	}
	if finishedOverflow > 0 {
		b.WriteString("    ")
		b.WriteString(bpSubtle.Render(fmt.Sprintf("+%d more finished", finishedOverflow)))
		b.WriteString("\n")
	}

	return b.String()
}

// renderActiveRow draws one in-flight batch row.
func renderActiveRow(b *BatchState, opts BatchPanelOptions, now time.Time) string {
	elapsed := time.Duration(0)
	if !b.StartedAt.IsZero() {
		elapsed = now.Sub(b.StartedAt)
	}
	bar, status := batchBar(b.Bytes, BatchByteEstimate, opts.Animation)
	return fmt.Sprintf("%s  %s  %s  %s  %s  %s",
		bpActive.Render("▶"),
		truncateLabel(b.Label, 38),
		bpSubtle.Render(fmtCount(b.Files, b.Unit)),
		bpSubtle.Render(fmtElapsed(elapsed)),
		bar,
		bpSubtle.Render(status),
	)
}

// renderFinishedRow draws one done/cached/failed row in the tail.
func renderFinishedRow(b *BatchState) string {
	icon := bpDone.Render("✓")
	suffix := ""
	switch b.Status {
	case BatchCached:
		suffix = "cached"
	case BatchFailed:
		icon = bpFail.Render("✗")
		suffix = "failed"
	}
	dur := time.Duration(0)
	if !b.StartedAt.IsZero() && !b.EndedAt.IsZero() {
		dur = b.EndedAt.Sub(b.StartedAt)
	}
	row := fmt.Sprintf("%s  %s  %s  %s",
		icon,
		bpSubtle.Render(truncateLabel(b.Label, 38)),
		bpSubtle.Render(fmtCount(b.Files, b.Unit)),
		bpSubtle.Render(fmtElapsed(dur)),
	)
	// Findings annotation. Recheck rows use the "findings" column
	// for their input size already (Unit=="findings"), so skipping it
	// there avoids "3 findings  3f" double-printing the same number.
	// Deep-review rows show the output count of findings the call
	// produced — that's the answer to "what did this call deliver?".
	if b.Findings > 0 && b.Unit != "findings" {
		row += "  " + bpSubtle.Render(fmt.Sprintf("→ %d findings", b.Findings))
	}
	if suffix != "" {
		row += "  " + bpSubtle.Render(suffix)
	}
	return row
}

// batchBar returns (bar, status) for an active batch. When the
// per-batch byte counter is non-zero, the bar fills proportionally;
// otherwise it shows an animated dotted indeterminate strip.
func batchBar(bytes, estimate, animation int) (bar, status string) {
	const width = 10
	if bytes > 0 && estimate > 0 {
		pct := float64(bytes) / float64(estimate)
		if pct >= 0.95 {
			pct = 0.95
		}
		filled := int(pct * float64(width))
		if filled < 1 {
			filled = 1
		}
		return strings.Repeat("█", filled) + strings.Repeat("░", width-filled),
			fmt.Sprintf("%d%%", int(pct*100))
	}
	// Indeterminate: a single dot bounces across the strip.
	pos := animation % (width * 2)
	if pos >= width {
		pos = (width * 2) - pos - 1
	}
	var s strings.Builder
	for i := 0; i < width; i++ {
		if i == pos {
			s.WriteRune('•')
		} else {
			s.WriteRune('·')
		}
	}
	return s.String(), "streaming"
}

func truncateLabel(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		// Left-pad with spaces so the column lines up.
		return s + strings.Repeat(" ", n-len(r))
	}
	return string(r[:n-1]) + "…"
}

// fmtCount renders the per-row count column with a noun unit,
// right-padded so subsequent columns line up regardless of singular
// vs plural. Empty unit defaults to "files" — that's the deep-review
// shape, which doesn't bother sending an explicit unit token. For
// recheck rows the unit is "findings".
//
// Pluralization: when n == 1 the noun is shown without trailing "s"
// (and left-padded with a space so column width stays constant). Note
// that "findings" only ever appears in batches of >=2 in practice,
// but the singular branch is wired for correctness.
func fmtCount(n int, unit string) string {
	if unit == "" {
		unit = "files"
	}
	singular := strings.TrimSuffix(unit, "s")
	width := len(unit)
	noun := unit
	if n == 1 {
		// Pad singular form to plural width so adjacent rows line up.
		noun = singular + strings.Repeat(" ", width-len(singular))
	}
	return fmt.Sprintf("%2d %s", n, noun)
}

func fmtElapsed(d time.Duration) string {
	if d <= 0 {
		return "0:00"
	}
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// scanTrailingFindings extracts the integer after `findings=` from a
// terminal-event payload like "done findings=3" or "cached findings=0".
// Returns (0, false) when the token is absent or malformed.
func scanTrailingFindings(payload string) (int, bool) {
	i := strings.Index(payload, "findings=")
	if i < 0 {
		return 0, false
	}
	tail := payload[i+len("findings="):]
	var n int
	if _, err := fmt.Sscanf(tail, "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

// scanQuotedLabel reads a "...."-quoted label from the start of s and
// returns (label, rest). Returns ("", s) when no quoted label is
// present. Doesn't try to handle escape sequences — labels are simple
// directory paths and category strings that never contain quotes.
func scanQuotedLabel(s string) (label, rest string) {
	if !strings.HasPrefix(s, `label="`) {
		return "", s
	}
	tail := strings.TrimPrefix(s, `label="`)
	end := strings.Index(tail, `"`)
	if end < 0 {
		return "", s
	}
	return tail[:end], tail[end+1:]
}

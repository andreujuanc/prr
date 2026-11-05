package ai

import (
	"context"
	"sync/atomic"
)

// TruncationProbe records whether any LLM call made under a context
// stopped on its provider's turn limit (StopMaxTurns).
//
// Client.ChatStream returns only text, so a caller cannot tell a
// cut-short answer from a finished one. A per-call probe carried on the
// context gives that signal to the one caller that needs it without
// changing the Client interface for every other caller, and stays safe
// when a single client serves many concurrent calls (each call gets its
// own probe).
type TruncationProbe struct {
	truncated atomic.Bool
}

type truncationProbeKey struct{}

// WithTruncationProbe returns a child context carrying a fresh probe.
// Every ChatStream made with that context (or a descendant) marks the
// probe if it stops on the turn limit.
func WithTruncationProbe(ctx context.Context) (context.Context, *TruncationProbe) {
	p := &TruncationProbe{}
	return context.WithValue(ctx, truncationProbeKey{}, p), p
}

// Truncated reports whether any call under the probe's context hit the
// turn limit. Safe on a nil probe.
func (p *TruncationProbe) Truncated() bool {
	return p != nil && p.truncated.Load()
}

// MarkTruncated flags the probe on ctx, if there is one. Agent.ChatStream
// calls it on StopMaxTurns; a Client that bypasses Agent can call it too.
func MarkTruncated(ctx context.Context) {
	if p, ok := ctx.Value(truncationProbeKey{}).(*TruncationProbe); ok {
		p.truncated.Store(true)
	}
}

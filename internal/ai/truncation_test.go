package ai

import (
	"context"
	"testing"
)

func TestTruncationProbe_AgentMarksOnMaxTurns(t *testing.T) {
	p := &mockProvider{responses: []*ChatResponse{{
		Content:    []ContentBlock{TextBlock{Text: "partial"}},
		StopReason: StopMaxTurns,
	}}}
	agent := NewAgent(p, nil)

	ctx, probe := WithTruncationProbe(context.Background())
	if _, err := agent.ChatStream(ctx, "sys", []Message{{Role: "user", Content: "go"}}, nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if !probe.Truncated() {
		t.Error("probe not marked after a StopMaxTurns response")
	}
}

func TestTruncationProbe_AgentLeavesCleanStopUnmarked(t *testing.T) {
	p := &mockProvider{responses: []*ChatResponse{{
		Content:    []ContentBlock{TextBlock{Text: "done"}},
		StopReason: StopEndTurn,
	}}}
	agent := NewAgent(p, nil)

	ctx, probe := WithTruncationProbe(context.Background())
	if _, err := agent.ChatStream(ctx, "sys", []Message{{Role: "user", Content: "go"}}, nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if probe.Truncated() {
		t.Error("probe marked after a clean end_turn response")
	}
}

// Calls made without a probe must keep working — every caller except
// the deep-review executor runs without one.
func TestTruncationProbe_NoProbeIsSafe(t *testing.T) {
	MarkTruncated(context.Background())
	var nilProbe *TruncationProbe
	if nilProbe.Truncated() {
		t.Error("nil probe must report not truncated")
	}
}

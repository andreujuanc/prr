package ai

import (
	"context"
	"testing"
)

// Tests pinning the thoughtSignature handling in parseSSEStream. The
// signature is opaque continuity state that Gemini requires us to echo
// back on every subsequent functionCall part — if we drop or fail to
// reattach it, the next turn fails with "Function call is missing a
// thought signature."
//
// Three observed shapes:
//   (1) signature in the same SSE part as the thinking text
//   (2) signature in a trailing empty-text part after the thinking
//   (3) functionCall part with no embedded signature, but a prior
//       ThinkingBlock carries one — the ToolUse must inherit it.

// TestThoughtSignature_SamePart pins case (1): signature arrives on the
// same SSE part as the thinking text.
func TestThoughtSignature_SamePart(t *testing.T) {
	resp := sseEvent(ssePartSpec{
		Text:             "thinking out loud",
		Thought:          new(true),
		ThoughtSignature: "sig-same",
	})

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)
	chResp, err := provider.Chat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "go"}}},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	tb := findThinkingBlock(t, chResp.Content)
	if tb.Text != "thinking out loud" {
		t.Errorf("text = %q, want %q", tb.Text, "thinking out loud")
	}
	if tb.Signature != "sig-same" {
		t.Errorf("signature = %q, want %q", tb.Signature, "sig-same")
	}
}

// TestThoughtSignature_TrailingEmptyPart pins case (2): the signature
// arrives in a separate part with empty text, immediately after the
// thinking content. The reattachment logic (gemini.go ~530-549) must
// fold that signature onto the prior ThinkingBlock.
func TestThoughtSignature_TrailingEmptyPart(t *testing.T) {
	resp := sseEvent(
		ssePartSpec{Text: "first thought", Thought: new(true)},
		ssePartSpec{Thought: new(true), ThoughtSignature: "sig-late"},
	)

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)
	chResp, err := provider.Chat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "go"}}},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	tb := findThinkingBlock(t, chResp.Content)
	if tb.Text != "first thought" {
		t.Errorf("text = %q, want %q", tb.Text, "first thought")
	}
	if tb.Signature != "sig-late" {
		t.Errorf("signature = %q, want %q (must inherit from trailing part)", tb.Signature, "sig-late")
	}
}

// TestThoughtSignature_ToolUseInheritsFromPriorThinking pins case (3):
// when Gemini emits a functionCall without an embedded signature but a
// prior ThinkingBlock carried one, the ToolUseBlock must inherit it.
// Without inheritance, the next outbound turn drops the signature and
// the API rejects the request.
func TestThoughtSignature_ToolUseInheritsFromPriorThinking(t *testing.T) {
	resp := sseEvent(
		ssePartSpec{Text: "let me look it up", Thought: new(true), ThoughtSignature: "sig-prev"},
		ssePartSpec{FunctionCall: &geminiFunctionCall{
			Name: "read_file",
			Args: map[string]any{"path": "main.go"},
		}},
	)

	srv := newTestServer([]string{resp})
	defer srv.Close()

	provider := newTestProvider(srv.URL)
	chResp, err := provider.Chat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "go"}}},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var tub *ToolUseBlock
	for _, b := range chResp.Content {
		if t2, ok := b.(ToolUseBlock); ok {
			tub = &t2
			break
		}
	}
	if tub == nil {
		t.Fatalf("no ToolUseBlock in response (blocks=%d)", len(chResp.Content))
	}
	if tub.Signature != "sig-prev" {
		t.Errorf("ToolUseBlock.Signature = %q, want %q (must inherit from prior ThinkingBlock)",
			tub.Signature, "sig-prev")
	}
}

func findThinkingBlock(t *testing.T, blocks []ContentBlock) ThinkingBlock {
	t.Helper()
	for _, b := range blocks {
		if tb, ok := b.(ThinkingBlock); ok {
			return tb
		}
	}
	t.Fatalf("no ThinkingBlock in response (blocks=%d)", len(blocks))
	return ThinkingBlock{}
}

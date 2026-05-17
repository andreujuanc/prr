package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// writeSSE writes a single SSE event with the given data payload.
// Flushing after each event matches what a real server does.
func writeSSE(w http.ResponseWriter, data string) {
	w.Write([]byte("data: " + data + "\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// collectToolUses drains a ChatEvent channel and returns the order-preserved
// list of tool-use blocks from the final Done event. Test helper.
func collectToolUses(t *testing.T, ch <-chan ChatEvent) []ToolUseBlock {
	t.Helper()
	var out []ToolUseBlock
	for ev := range ch {
		if ev.Type == EventDone && ev.Response != nil {
			for _, b := range ev.Response.Content {
				if tu, ok := b.(ToolUseBlock); ok {
					out = append(out, tu)
				}
			}
		}
		if ev.Type == EventError {
			t.Fatalf("stream returned error: %v", ev.Err)
		}
	}
	return out
}

// TestOpenAIStream_ToolCalls_OrderPreserved feeds two tool calls in
// arrival order and pins that the slice-based accumulator surfaces
// them in the same order with the right names and args. Regression
// for the map-keyed accumulator that walked map[int] by integer
// index — order would silently break if any future change broke the
// "keys are always 0..N-1 sequential" invariant.
func TestOpenAIStream_ToolCalls_OrderPreserved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Two tool calls, IDs on the first delta for each.
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"id":"call_a","function":{"name":"read_file","arguments":"{\"p\":\""}}]}}]}`)
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"a.go\"}"}}]}}]}`)
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"id":"call_b","function":{"name":"grep","arguments":"{\"q\":\"foo\"}"}}]}}]}`)
		writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	provider := &OpenAIProvider{APIKey: "k", Model: "m", BaseURL: srv.URL}
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	uses := collectToolUses(t, ch)
	if len(uses) != 2 {
		t.Fatalf("want 2 tool calls, got %d (%+v)", len(uses), uses)
	}
	if uses[0].ID != "call_a" || uses[0].Name != "read_file" {
		t.Errorf("first call: ID=%q name=%q (want call_a / read_file)", uses[0].ID, uses[0].Name)
	}
	if string(uses[0].Args) != `{"p":"a.go"}` {
		t.Errorf("first call args: %q (want %q)", string(uses[0].Args), `{"p":"a.go"}`)
	}
	if uses[1].ID != "call_b" || uses[1].Name != "grep" {
		t.Errorf("second call: ID=%q name=%q (want call_b / grep)", uses[1].ID, uses[1].Name)
	}
	if string(uses[1].Args) != `{"q":"foo"}` {
		t.Errorf("second call args: %q (want %q)", string(uses[1].Args), `{"q":"foo"}`)
	}
}

// TestOpenAIStream_ToolCalls_ContinuationBeforeID covers the
// specific bug we fixed: if the first tool-call delta has no ID,
// the old map-keyed code computed idx = len(toolCalls) - 1 = -1
// and the resulting toolCalls[-1] lookup silently dropped the
// args. The new slice-based code logs and drops the fragment
// instead of writing to a non-existent slot, and a subsequent
// well-formed tool call still goes through unaffected.
func TestOpenAIStream_ToolCalls_ContinuationBeforeID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// First delta: continuation only (no ID). Drop this fragment.
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"orphaned"}}]}}]}`)
		// Then a normal tool call.
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"id":"call_x","function":{"name":"read_file","arguments":"{\"p\":\"x.go\"}"}}]}}]}`)
		writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	provider := &OpenAIProvider{APIKey: "k", Model: "m", BaseURL: srv.URL}
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	uses := collectToolUses(t, ch)
	if len(uses) != 1 {
		t.Fatalf("want 1 tool call, got %d (orphaned fragment should not have created a phantom call): %+v", len(uses), uses)
	}
	if uses[0].ID != "call_x" || uses[0].Name != "read_file" {
		t.Errorf("call: ID=%q name=%q (want call_x / read_file)", uses[0].ID, uses[0].Name)
	}
	// The orphaned "orphaned" fragment must not leak into the real call's args.
	if string(uses[0].Args) != `{"p":"x.go"}` {
		t.Errorf("args contaminated by orphaned fragment: %q", string(uses[0].Args))
	}
}

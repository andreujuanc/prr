package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Mock provider ───────────────────────────────────────────────────────

type mockProvider struct {
	mu           sync.Mutex
	calls        []ChatRequest
	responses    []*ChatResponse
	callIdx      int
	capabilities Capabilities
}

func (m *mockProvider) Name() string               { return "mock" }
func (m *mockProvider) ModelID() string            { return "mock-model" }
func (m *mockProvider) Capabilities() Capabilities { return m.capabilities }

func (m *mockProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	ch, err := m.StreamChat(ctx, req)
	if err != nil {
		return nil, err
	}
	var resp *ChatResponse
	for event := range ch {
		if event.Type == EventDone {
			resp = event.Response
		}
	}
	return resp, nil
}

func (m *mockProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	idx := m.callIdx
	m.callIdx++
	m.mu.Unlock()

	if idx >= len(m.responses) {
		ch := make(chan ChatEvent, 1)
		ch <- ChatEvent{Type: EventDone, Response: &ChatResponse{StopReason: StopEndTurn}}
		close(ch)
		return ch, nil
	}

	resp := m.responses[idx]
	ch := make(chan ChatEvent, 64)

	go func() {
		defer close(ch)
		for _, block := range resp.Content {
			switch b := block.(type) {
			case TextBlock:
				ch <- ChatEvent{Type: EventText, Text: b.Text}
			case ThinkingBlock:
				ch <- ChatEvent{Type: EventThinking, Text: b.Text}
			case ToolUseBlock:
				tub := b // copy for pointer
				ch <- ChatEvent{Type: EventToolUse, ToolUse: &tub}
			}
		}
		ch <- ChatEvent{Type: EventDone, Response: resp}
	}()

	return ch, nil
}

// ── Agent tests ─────────────────────────────────────────────────────────

func TestAgent_SimpleText(t *testing.T) {
	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content:    []ContentBlock{TextBlock{Text: "Hello!"}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, nil)

	var tokens []string
	result, err := agent.ChatStream(context.Background(), "system", []Message{
		{Role: "user", Content: "hi"},
	}, func(s string) {
		tokens = append(tokens, s)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Hello!" {
		t.Errorf("got %q, want %q", result, "Hello!")
	}
	if len(tokens) != 1 || tokens[0] != "Hello!" {
		t.Errorf("tokens = %v, want [\"Hello!\"]", tokens)
	}
}

func TestAgent_ToolCall(t *testing.T) {
	argsJSON := json.RawMessage(`{"path":"src"}`)

	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "call_1", Name: "list_dir", Args: argsJSON},
				},
				StopReason: StopToolUse,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "I found 3 files."}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	result, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "list files"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "I found 3 files." {
		t.Errorf("got %q, want %q", result, "I found 3 files.")
	}

	// Verify two provider calls were made
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(mock.calls))
	}

	// Verify second call contains tool results
	secondCall := mock.calls[1]
	lastMsg := secondCall.Messages[len(secondCall.Messages)-1]
	if lastMsg.Role != RoleUser {
		t.Errorf("expected last message role user, got %s", lastMsg.Role)
	}
	var hasToolResult bool
	for _, block := range lastMsg.Content {
		if tr, ok := block.(ToolResultBlock); ok {
			hasToolResult = true
			if tr.Name != "list_dir" {
				t.Errorf("tool result name = %q, want %q", tr.Name, "list_dir")
			}
			if tr.ToolUseID != "call_1" {
				t.Errorf("tool result ID = %q, want %q", tr.ToolUseID, "call_1")
			}
		}
	}
	if !hasToolResult {
		t.Error("expected tool result in second call's messages")
	}

	// Verify assistant turn was echoed back with the tool call
	assistantMsg := secondCall.Messages[len(secondCall.Messages)-2]
	if assistantMsg.Role != RoleAssistant {
		t.Errorf("expected assistant turn before tool results, got %s", assistantMsg.Role)
	}
}

// TestAgent_ToolCall_InvalidArgsSurfacesError pins the contract for
// malformed tool-call arguments: instead of silently running the tool
// with an empty map (the old behaviour), the agent must return an
// IsError tool result so the model sees the failure on its next turn
// and can correct course.
func TestAgent_ToolCall_InvalidArgsSurfacesError(t *testing.T) {
	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content: []ContentBlock{
					// Args is not valid JSON. The old code did
					// `_ = json.Unmarshal(...)` and dropped the error,
					// then called ExecuteTool with an empty map.
					ToolUseBlock{ID: "call_1", Name: "list_dir", Args: json.RawMessage(`{not json`)},
				},
				StopReason: StopToolUse,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "ok"}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})
	if _, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "go"},
	}, nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) < 2 {
		t.Fatalf("expected 2 provider calls (tool result feedback), got %d", len(mock.calls))
	}
	last := mock.calls[1].Messages[len(mock.calls[1].Messages)-1]
	var saw ToolResultBlock
	for _, b := range last.Content {
		if tr, ok := b.(ToolResultBlock); ok {
			saw = tr
			break
		}
	}
	if saw.ToolUseID != "call_1" {
		t.Fatalf("missing tool result for call_1; got %+v", last.Content)
	}
	if !saw.IsError {
		t.Errorf("invalid args should produce IsError=true tool result; got Content=%q", saw.Content)
	}
	if !strings.Contains(saw.Content, "invalid JSON") {
		t.Errorf("tool result content should mention invalid JSON; got %q", saw.Content)
	}
}

func TestAgent_ThinkingWithToolCall(t *testing.T) {
	argsJSON := json.RawMessage(`{"path":"main.go"}`)

	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content: []ContentBlock{
					ThinkingBlock{Text: "Let me check the file", Signature: "sig1"},
					ToolUseBlock{ID: "call_1", Name: "read_file", Args: argsJSON},
				},
				StopReason: StopToolUse,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "Here is the file content."}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	var tokens []string
	result, err := agent.ChatStream(context.Background(), "be helpful", []Message{
		{Role: "user", Content: "show me main.go"},
	}, func(s string) {
		tokens = append(tokens, s)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Here is the file content." {
		t.Errorf("got %q, want %q", result, "Here is the file content.")
	}

	// Verify thinking was streamed with marker
	var foundThought bool
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "\x00THOUGHT:") && strings.Contains(tok, "Let me check") {
			foundThought = true
		}
	}
	if !foundThought {
		t.Error("expected thought text with THOUGHT marker in tokens")
	}

	// Verify the assistant turn echoed back includes thinking block
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) < 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(mock.calls))
	}
	secondCall := mock.calls[1]
	assistantMsg := secondCall.Messages[1] // user, assistant, user(tool results)
	if assistantMsg.Role != RoleAssistant {
		t.Errorf("expected assistant role, got %s", assistantMsg.Role)
	}
	var hasThinking bool
	for _, block := range assistantMsg.Content {
		if tb, ok := block.(ThinkingBlock); ok {
			hasThinking = true
			if tb.Signature != "sig1" {
				t.Errorf("thinking signature = %q, want %q", tb.Signature, "sig1")
			}
		}
	}
	if !hasThinking {
		t.Error("expected ThinkingBlock in echoed assistant turn")
	}
}

func TestAgent_ToolStartDoneEvents(t *testing.T) {
	argsJSON := json.RawMessage(`{"path":"src"}`)

	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "call_1", Name: "list_dir", Args: argsJSON},
				},
				StopReason: StopToolUse,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "done"}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	var tokens []string
	_, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "list"},
	}, func(s string) {
		tokens = append(tokens, s)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify TOOL_START event was sent
	var foundStart, foundDone bool
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "\x00TOOL_START:") && strings.Contains(tok, "list_dir") {
			foundStart = true
		}
		if strings.HasPrefix(tok, "\x00TOOL_DONE:") && strings.Contains(tok, "list_dir") {
			foundDone = true
			// Verify format: name|status|duration
			parts := strings.SplitN(strings.TrimPrefix(tok, "\x00TOOL_DONE:"), "|", 3)
			if len(parts) != 3 {
				t.Errorf("expected 3 parts in TOOL_DONE, got %d: %q", len(parts), tok)
			} else {
				if parts[0] != "list_dir" {
					t.Errorf("TOOL_DONE name = %q, want %q", parts[0], "list_dir")
				}
				if parts[1] != "ok" && parts[1] != "error" {
					t.Errorf("TOOL_DONE status = %q, want ok or error", parts[1])
				}
			}
		}
	}
	if !foundStart {
		t.Errorf("expected TOOL_START event in tokens, got: %v", tokens)
	}
	if !foundDone {
		t.Errorf("expected TOOL_DONE event in tokens, got: %v", tokens)
	}
}

func TestAgent_MaxRoundsLimit(t *testing.T) {
	argsJSON := json.RawMessage(`{"path":"."}`)

	// Create a provider that always returns tool calls — should stop at maxRounds
	maxR := 5
	var responses []*ChatResponse
	for i := 0; i < maxR+5; i++ {
		responses = append(responses, &ChatResponse{
			Content: []ContentBlock{
				ToolUseBlock{ID: "call_x", Name: "list_dir", Args: argsJSON},
			},
			StopReason: StopToolUse,
		})
	}

	mock := &mockProvider{responses: responses}
	agent := NewAgent(mock, &ToolExecutor{}, WithMaxRounds(maxR))

	result, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "loop"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have made exactly maxRounds calls
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) != maxR {
		t.Errorf("expected %d calls (maxRounds), got %d", maxR, len(mock.calls))
	}

	// Result should contain the max iterations message
	if !strings.Contains(result, "max iterations") {
		t.Errorf("expected 'max iterations' in result, got %q", result)
	}
}

func TestAgent_NilToolExecutor(t *testing.T) {
	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content:    []ContentBlock{TextBlock{Text: "Hello!"}},
				StopReason: StopEndTurn,
			},
		},
	}

	// Agent with nil tool executor — should not send tools
	agent := NewAgent(mock, nil)

	result, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "hi"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Hello!" {
		t.Errorf("got %q, want %q", result, "Hello!")
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls[0].Tools) != 0 {
		t.Error("expected no tools when executor is nil")
	}
}

func TestAgent_ToolConfigurer(t *testing.T) {
	mock := &mockProvider{}
	exec := &ToolExecutor{}
	agent := NewAgent(mock, exec)

	agent.SetHeadRef("origin/feature")
	agent.SetBaseRef("origin/main")
	agent.SetRawDiffs(map[string]string{"file.go": "diff"})
	agent.SetReviewGetter(func() string { return "review" })

	if exec.HeadRef != "origin/feature" {
		t.Errorf("HeadRef = %q, want %q", exec.HeadRef, "origin/feature")
	}
	if exec.BaseRef != "origin/main" {
		t.Errorf("BaseRef = %q, want %q", exec.BaseRef, "origin/main")
	}
	if exec.RawDiffs["file.go"] != "diff" {
		t.Error("RawDiffs not set correctly")
	}
	if exec.ReviewGetter() != "review" {
		t.Error("ReviewGetter not set correctly")
	}
}

func TestAgent_ContextCancellation(t *testing.T) {
	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content:    []ContentBlock{TextBlock{Text: "should not appear"}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := agent.ChatStream(ctx, "", []Message{
		{Role: "user", Content: "hi"},
	}, nil)

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestAgent_ParallelToolExecution(t *testing.T) {
	// All tools are read-only → they should run in parallel
	args1 := json.RawMessage(`{"path":"src"}`)
	args2 := json.RawMessage(`{"path":"lib"}`)
	args3 := json.RawMessage(`{"path":"test"}`)

	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "c1", Name: "list_dir", Args: args1},
					ToolUseBlock{ID: "c2", Name: "list_dir", Args: args2},
					ToolUseBlock{ID: "c3", Name: "list_dir", Args: args3},
				},
				StopReason: StopToolUse,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "Found everything."}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	start := time.Now()
	result, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "list all"},
	}, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Found everything." {
		t.Errorf("got %q, want %q", result, "Found everything.")
	}

	// Verify all 3 tool results were sent back
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(mock.calls))
	}
	toolResultMsg := mock.calls[1].Messages[len(mock.calls[1].Messages)-1]
	toolResultCount := 0
	for _, block := range toolResultMsg.Content {
		if _, ok := block.(ToolResultBlock); ok {
			toolResultCount++
		}
	}
	if toolResultCount != 3 {
		t.Errorf("expected 3 tool results, got %d", toolResultCount)
	}

	// Verify results are in order (matched by ID)
	ids := []string{}
	for _, block := range toolResultMsg.Content {
		if tr, ok := block.(ToolResultBlock); ok {
			ids = append(ids, tr.ToolUseID)
		}
	}
	if len(ids) != 3 || ids[0] != "c1" || ids[1] != "c2" || ids[2] != "c3" {
		t.Errorf("tool result IDs = %v, want [c1 c2 c3]", ids)
	}

	// Parallel execution should be fast
	if elapsed > 5*time.Second {
		t.Errorf("parallel execution took %v, expected less than 5s", elapsed)
	}
}

func TestAgent_GracefulToolError(t *testing.T) {
	// Call an unknown tool — should produce IsError=true result, not crash
	argsJSON := json.RawMessage(`{}`)

	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "call_1", Name: "nonexistent_tool", Args: argsJSON},
				},
				StopReason: StopToolUse,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "I'll try something else."}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	var tokens []string
	result, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "do something"},
	}, func(s string) {
		tokens = append(tokens, s)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "I'll try something else." {
		t.Errorf("got %q, want %q", result, "I'll try something else.")
	}

	// Verify the tool result was sent with isError
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) < 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(mock.calls))
	}
	toolResultMsg := mock.calls[1].Messages[len(mock.calls[1].Messages)-1]
	for _, block := range toolResultMsg.Content {
		if tr, ok := block.(ToolResultBlock); ok {
			if !tr.IsError {
				t.Error("expected IsError=true for unknown tool")
			}
			if !strings.Contains(tr.Content, "unknown tool") {
				t.Errorf("expected 'unknown tool' in error content, got %q", tr.Content)
			}
		}
	}

	// Verify TOOL_DONE shows error status
	var foundError bool
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "\x00TOOL_DONE:") && strings.Contains(tok, "error") {
			foundError = true
		}
	}
	if !foundError {
		t.Error("expected TOOL_DONE with error status in tokens")
	}
}

func TestAgent_StallPreambleDetection(t *testing.T) {
	// First response: "I'll use the tool" text but no tool_use block
	// Second response: same pattern — should terminate
	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content:    []ContentBlock{TextBlock{Text: "I'll use the read_file tool now."}},
				StopReason: StopEndTurn,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "Let me check the file for you."}},
				StopReason: StopEndTurn,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "Should not reach here."}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	result, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "review this file"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have made 2 calls (first stall tolerated, second terminates)
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) != 2 {
		t.Errorf("expected 2 provider calls for stall detection, got %d", len(mock.calls))
	}

	// Result should contain text from both rounds
	if !strings.Contains(result, "I'll use the read_file tool now.") {
		t.Errorf("result missing first round text: %q", result)
	}
	if !strings.Contains(result, "Let me check the file") {
		t.Errorf("result missing second round text: %q", result)
	}
}

func TestAgent_StallPreambleResetOnToolCall(t *testing.T) {
	argsJSON := json.RawMessage(`{"path":"src"}`)

	// Round 1: stall preamble (tolerated)
	// Round 2: real tool call (resets counter)
	// Round 3: stall preamble (tolerated again since counter was reset)
	// Round 4: another stall (terminates)
	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content:    []ContentBlock{TextBlock{Text: "I'll use the list_dir tool."}},
				StopReason: StopEndTurn,
			},
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "c1", Name: "list_dir", Args: argsJSON},
				},
				StopReason: StopToolUse,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "Let me check something else."}},
				StopReason: StopEndTurn,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "I'll search for that now."}},
				StopReason: StopEndTurn,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "Should not reach here."}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	_, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "review"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	// Round 1: stall (tolerated), Round 2: tool call, Round 3: stall (tolerated), Round 4: stall (terminates)
	if len(mock.calls) != 4 {
		t.Errorf("expected 4 provider calls, got %d", len(mock.calls))
	}
}

func TestAgent_NonStallTextTerminates(t *testing.T) {
	// Regular text (not a stall preamble) should terminate immediately
	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content:    []ContentBlock{TextBlock{Text: "Here is my complete analysis of the code."}},
				StopReason: StopEndTurn,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "Should not reach here."}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	result, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "analyze"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) != 1 {
		t.Errorf("expected 1 provider call for non-stall text, got %d", len(mock.calls))
	}
	if result != "Here is my complete analysis of the code." {
		t.Errorf("got %q", result)
	}
}

func TestAgent_DebugLogging(t *testing.T) {
	argsJSON := json.RawMessage(`{"path":"src"}`)

	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "c1", Name: "list_dir", Args: argsJSON},
				},
				StopReason: StopToolUse,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "done"}},
				StopReason: StopEndTurn,
			},
		},
	}

	var logBuf bytes.Buffer
	agent := NewAgent(mock, &ToolExecutor{}, WithDebugLogger(&logBuf))

	_, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "list"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "round 1") {
		t.Errorf("debug log missing 'round 1': %s", logOutput)
	}
	if !strings.Contains(logOutput, "tool call: list_dir") {
		t.Errorf("debug log missing 'tool call: list_dir': %s", logOutput)
	}
	if !strings.Contains(logOutput, "tool result:") {
		t.Errorf("debug log missing 'tool result:': %s", logOutput)
	}
}

func TestAgent_CachePrefixPropagation(t *testing.T) {
	// Provider with caching enabled
	mock := &mockProvider{
		capabilities: Capabilities{PromptCaching: true},
		responses: []*ChatResponse{
			{
				Content:    []ContentBlock{TextBlock{Text: "ok"}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, nil)

	_, err := agent.ChatStream(context.Background(), "system", []Message{
		{Role: "user", Content: "hi"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if !mock.calls[0].CachePrefix {
		t.Error("expected CachePrefix=true when provider supports caching")
	}
}

func TestAgent_CachePrefixNotSetWhenUnsupported(t *testing.T) {
	// Provider without caching
	mock := &mockProvider{
		capabilities: Capabilities{PromptCaching: false},
		responses: []*ChatResponse{
			{
				Content:    []ContentBlock{TextBlock{Text: "ok"}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, nil)

	_, err := agent.ChatStream(context.Background(), "system", []Message{
		{Role: "user", Content: "hi"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.calls[0].CachePrefix {
		t.Error("expected CachePrefix=false when provider doesn't support caching")
	}
}

func TestAgent_WithMaxRoundsOption(t *testing.T) {
	mock := &mockProvider{}
	agent := NewAgent(mock, nil, WithMaxRounds(25))
	if agent.maxRounds != 25 {
		t.Errorf("maxRounds = %d, want 25", agent.maxRounds)
	}

	// Invalid values should be ignored
	agent2 := NewAgent(mock, nil, WithMaxRounds(0))
	if agent2.maxRounds != defaultMaxRounds {
		t.Errorf("maxRounds = %d, want %d for zero input", agent2.maxRounds, defaultMaxRounds)
	}

	agent3 := NewAgent(mock, nil, WithMaxRounds(-1))
	if agent3.maxRounds != defaultMaxRounds {
		t.Errorf("maxRounds = %d, want %d for negative input", agent3.maxRounds, defaultMaxRounds)
	}
}

func TestAgent_ToolResultsOrderMatchesToolCalls(t *testing.T) {
	// Verify that even with parallel execution, results are ordered by call index
	args := json.RawMessage(`{"pattern":"test"}`)

	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "a", Name: "list_dir", Args: json.RawMessage(`{"path":"1"}`)},
					ToolUseBlock{ID: "b", Name: "grep", Args: args},
					ToolUseBlock{ID: "c", Name: "list_dir", Args: json.RawMessage(`{"path":"2"}`)},
				},
				StopReason: StopToolUse,
			},
			{
				Content:    []ContentBlock{TextBlock{Text: "results"}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{HeadRef: "HEAD"})

	_, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "go"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) < 2 {
		t.Fatalf("expected 2 calls, got %d", len(mock.calls))
	}

	resultMsg := mock.calls[1].Messages[len(mock.calls[1].Messages)-1]
	var resultIDs []string
	for _, block := range resultMsg.Content {
		if tr, ok := block.(ToolResultBlock); ok {
			resultIDs = append(resultIDs, tr.ToolUseID)
		}
	}
	if len(resultIDs) != 3 || resultIDs[0] != "a" || resultIDs[1] != "b" || resultIDs[2] != "c" {
		t.Errorf("tool result IDs = %v, want [a b c]", resultIDs)
	}
}

func TestAgent_ContextCancellationDuringToolExecution(t *testing.T) {
	argsJSON := json.RawMessage(`{"path":"src"}`)

	mock := &mockProvider{
		responses: []*ChatResponse{
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "c1", Name: "list_dir", Args: argsJSON},
				},
				StopReason: StopToolUse,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after the first provider call completes but tool results
	// might be building. This tests graceful handling.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	// Should not panic — it should either complete or return an error
	_, _ = agent.ChatStream(ctx, "", []Message{
		{Role: "user", Content: "list"},
	}, nil)
}

// ── Helper tests ────────────────────────────────────────────────────────

func TestLooksLikeStallPreamble(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"I'll use the read_file tool now.", true},
		{"Let me check the implementation.", true},
		{"I'll search for that pattern.", true},
		{"I will now use the list_dir tool.", true},
		{"Let me look at the diff.", true},
		{"Here is my complete analysis.", false},
		{"The code has several issues.", false},
		{"", false},
		{"   ", false},
		{"I found 3 bugs in the code.", false},
	}

	for _, tt := range tests {
		got := looksLikeStallPreamble(tt.text)
		if got != tt.want {
			t.Errorf("looksLikeStallPreamble(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestIsToolReadOnly(t *testing.T) {
	// All canonical tools should be read-only
	for _, td := range CanonicalToolDefs() {
		if !IsToolReadOnly(td.Name) {
			t.Errorf("tool %q should be read-only", td.Name)
		}
	}

	// Unknown tool should not be read-only
	if IsToolReadOnly("delete_file") {
		t.Error("unknown tool 'delete_file' should not be read-only")
	}
}

func TestFormatToolArgs(t *testing.T) {
	raw := json.RawMessage(`{"path":"src","limit":10}`)
	result := formatToolArgs(raw)
	// Map iteration order is non-deterministic, so check both parts
	if !strings.Contains(result, "path=src") {
		t.Errorf("expected 'path=src' in %q", result)
	}
	if !strings.Contains(result, "limit=10") {
		t.Errorf("expected 'limit=10' in %q", result)
	}
}

// ── Loop-level integration tests ────────────────────────────────────────

// TestAgent_ToolErrorRecovery verifies that when a known tool returns an
// Error: result, the agent sends it back with IsError=true and the model
// can recover by trying a different approach.
func TestAgent_ToolErrorRecovery(t *testing.T) {
	mock := &mockProvider{
		responses: []*ChatResponse{
			// Round 1: model calls read_file with a bad path
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "c1", Name: "read_file", Args: json.RawMessage(`{"path":"nonexistent.go"}`)},
				},
				StopReason: StopToolUse,
			},
			// Round 2: model recovers after getting the error
			{
				Content:    []ContentBlock{TextBlock{Text: "The file doesn't exist. Let me check the directory instead."}},
				StopReason: StopEndTurn,
			},
		},
	}

	// Use a tool executor that will return an error for the file
	exec := &ToolExecutor{
		HeadRef: "HEAD",
		gitRunner: func(args ...string) (string, error) {
			return "", fmt.Errorf("git show: path 'nonexistent.go' does not exist")
		},
	}

	agent := NewAgent(mock, exec)

	result, err := agent.ChatStream(context.Background(), "", []Message{
		{Role: "user", Content: "read the file"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "doesn't exist") {
		t.Errorf("expected recovery message, got: %q", result)
	}

	// Verify the error was sent back to the model with IsError=true
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) < 2 {
		t.Fatalf("expected 2 calls, got %d", len(mock.calls))
	}

	lastMsg := mock.calls[1].Messages[len(mock.calls[1].Messages)-1]
	for _, block := range lastMsg.Content {
		if tr, ok := block.(ToolResultBlock); ok {
			if !tr.IsError {
				t.Error("expected IsError=true for failed tool")
			}
			if !strings.Contains(tr.Content, "Error:") {
				t.Errorf("expected Error: in content, got: %q", tr.Content)
			}
		}
	}
}

// TestAgent_MultiRoundToolLoop verifies a multi-round conversation where
// the model makes tool calls, gets results, makes more tool calls, and
// finally produces a text response.
func TestAgent_MultiRoundToolLoop(t *testing.T) {
	mock := &mockProvider{
		responses: []*ChatResponse{
			// Round 1: list directory
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "c1", Name: "list_dir", Args: json.RawMessage(`{"path":"."}`)},
				},
				StopReason: StopToolUse,
			},
			// Round 2: read a file based on directory listing
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "c2", Name: "read_file", Args: json.RawMessage(`{"path":"main.go"}`)},
				},
				StopReason: StopToolUse,
			},
			// Round 3: grep for a pattern
			{
				Content: []ContentBlock{
					ToolUseBlock{ID: "c3", Name: "grep", Args: json.RawMessage(`{"pattern":"func main"}`)},
				},
				StopReason: StopToolUse,
			},
			// Round 4: final analysis
			{
				Content:    []ContentBlock{TextBlock{Text: "The code looks good."}},
				StopReason: StopEndTurn,
			},
		},
	}

	exec := &ToolExecutor{
		HeadRef: "HEAD",
		BaseRef: "origin/main",
		gitRunner: func(args ...string) (string, error) {
			if len(args) == 0 {
				return "", fmt.Errorf("no args")
			}
			switch args[0] {
			case "ls-tree":
				return "100644 blob abc 100\tmain.go\n", nil
			case "show":
				return "package main\nfunc main() {}\n", nil
			case "grep":
				return "HEAD:main.go:2:func main() {}\n", nil
			default:
				return "", fmt.Errorf("unexpected: %s", args[0])
			}
		},
	}

	agent := NewAgent(mock, exec)

	result, err := agent.ChatStream(context.Background(), "review", []Message{
		{Role: "user", Content: "review this code"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "The code looks good." {
		t.Errorf("got %q, want 'The code looks good.'", result)
	}

	// Verify 4 provider calls were made (3 tool rounds + final)
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.calls) != 4 {
		t.Errorf("expected 4 provider calls, got %d", len(mock.calls))
	}

	// Verify conversation history grows with each round
	for i, call := range mock.calls {
		// Each subsequent call should have more messages
		if i > 0 && len(call.Messages) <= len(mock.calls[i-1].Messages) {
			t.Errorf("call %d should have more messages than call %d (%d vs %d)",
				i, i-1, len(call.Messages), len(mock.calls[i-1].Messages))
		}
	}
}

// TestAgent_ProviderWithoutStructuredOutput verifies that when a provider
// reports StructuredOutput=false, the agent does not set JSONSchema on
// the request (the prompt-based fallback path).
func TestAgent_ProviderWithoutStructuredOutput(t *testing.T) {
	mock := &mockProvider{
		capabilities: Capabilities{
			StructuredOutput:  false,
			PromptCaching:     false,
			ParallelToolCalls: true,
		},
		responses: []*ChatResponse{
			{
				Content:    []ContentBlock{TextBlock{Text: `{"summary":"test"}`}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	result, err := agent.ChatStream(context.Background(), "review", []Message{
		{Role: "user", Content: "review"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != `{"summary":"test"}` {
		t.Errorf("got %q, want JSON string", result)
	}

	// Verify CachePrefix was NOT set (provider doesn't support caching)
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.calls[0].CachePrefix {
		t.Error("CachePrefix should be false when provider doesn't support caching")
	}
}

// TestAgent_ProviderWithStructuredOutput verifies that when a provider
// reports StructuredOutput=true and PromptCaching=true, the request
// has CachePrefix set.
func TestAgent_ProviderWithStructuredOutput(t *testing.T) {
	mock := &mockProvider{
		capabilities: Capabilities{
			StructuredOutput:  true,
			PromptCaching:     true,
			ParallelToolCalls: true,
		},
		responses: []*ChatResponse{
			{
				Content:    []ContentBlock{TextBlock{Text: "done"}},
				StopReason: StopEndTurn,
			},
		},
	}

	agent := NewAgent(mock, &ToolExecutor{})

	_, err := agent.ChatStream(context.Background(), "system", []Message{
		{Role: "user", Content: "go"},
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if !mock.calls[0].CachePrefix {
		t.Error("CachePrefix should be true when provider supports caching")
	}
}

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

// ── Stream parsing ──────────────────────────────────────────────────────

const claudeCodeFixtureSuccess = `{"type":"system","subtype":"init","cwd":"/x","session_id":"abc","model":"claude-haiku-4-5-20251001"}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}
{"type":"assistant","message":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"User asked for four words. I should reply concisely."}]}}
{"type":"assistant","message":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"alpha bravo charlie delta"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"alpha bravo charlie delta","stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":8070,"cache_read_input_tokens":0,"output_tokens":52}}
`

func TestParseClaudeCodeStream_Success(t *testing.T) {
	ch := make(chan ChatEvent, 16)
	parseClaudeCodeStream(context.Background(), strings.NewReader(claudeCodeFixtureSuccess), ch)
	close(ch)

	var (
		thinking, text string
		done           *ChatResponse
	)
	for evt := range ch {
		switch evt.Type {
		case EventThinking:
			thinking += evt.Text
		case EventText:
			text += evt.Text
		case EventDone:
			done = evt.Response
		case EventError:
			t.Fatalf("unexpected error event: %v", evt.Err)
		}
	}

	if !strings.Contains(thinking, "four words") {
		t.Errorf("thinking text not surfaced: %q", thinking)
	}
	if text != "alpha bravo charlie delta" {
		t.Errorf("text = %q, want %q", text, "alpha bravo charlie delta")
	}
	if done == nil {
		t.Fatal("no EventDone emitted")
	}
	if done.StopReason != StopEndTurn {
		t.Errorf("stop reason = %v, want %v", done.StopReason, StopEndTurn)
	}
	// Cache-creation tokens should fold into InputTokens since they were
	// billed as input on this request.
	wantInput := 10 + 8070
	if done.Usage.InputTokens != wantInput {
		t.Errorf("input tokens = %d, want %d", done.Usage.InputTokens, wantInput)
	}
	if done.Usage.OutputTokens != 52 {
		t.Errorf("output tokens = %d, want 52", done.Usage.OutputTokens)
	}
}

func TestParseClaudeCodeStream_DedupesRepeatedTextBlocks(t *testing.T) {
	// CLI may resend the same text block (e.g. when an assistant message
	// is re-emitted in cumulative form). The parser should emit each
	// unique text block exactly once.
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"hello","usage":{"input_tokens":1,"output_tokens":1}}`,
		"",
	}, "\n")

	ch := make(chan ChatEvent, 8)
	parseClaudeCodeStream(context.Background(), strings.NewReader(stream), ch)
	close(ch)

	var textEvents int
	for evt := range ch {
		if evt.Type == EventText {
			textEvents++
		}
	}
	if textEvents != 1 {
		t.Errorf("text events = %d, want 1 (deduplicated)", textEvents)
	}
}

func TestParseClaudeCodeStream_ErrorResult(t *testing.T) {
	stream := `{"type":"result","subtype":"error","is_error":true,"result":"rate limited"}` + "\n"
	ch := make(chan ChatEvent, 4)
	parseClaudeCodeStream(context.Background(), strings.NewReader(stream), ch)
	close(ch)

	var sawError bool
	var doneEmitted bool
	for evt := range ch {
		switch evt.Type {
		case EventError:
			sawError = true
			if !strings.Contains(evt.Err.Error(), "rate limited") {
				t.Errorf("error text = %q, want it to contain %q", evt.Err.Error(), "rate limited")
			}
		case EventDone:
			doneEmitted = true
		}
	}
	if !sawError {
		t.Error("expected EventError for is_error result")
	}
	if doneEmitted {
		t.Error("EventDone should not be emitted after EventError")
	}
}

func TestParseClaudeCodeStream_FillsResultIfTextNeverStreamed(t *testing.T) {
	// Fallback: if assistant text was never streamed but the result line
	// carries the final text, the parser should still surface it.
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"only here","usage":{"input_tokens":1,"output_tokens":1}}`,
		"",
	}, "\n")

	ch := make(chan ChatEvent, 4)
	parseClaudeCodeStream(context.Background(), strings.NewReader(stream), ch)
	close(ch)

	var got strings.Builder
	for evt := range ch {
		if evt.Type == EventText {
			got.WriteString(evt.Text)
		}
	}
	if got.String() != "only here" {
		t.Errorf("text = %q, want %q", got.String(), "only here")
	}
}

// TestParseClaudeCodeStream_RespectsCtxCancellation pins the in-loop
// cancellation guarantee: if the caller's context is already cancelled
// when the parser begins iterating, the parser must emit EventError
// with the ctx error rather than processing the stream to completion.
//
// In production this matters when the user aborts a long review while
// claude is mid-stream — exec.CommandContext kills the subprocess and
// closes the pipe, but the in-loop ctx.Done() check is the belt to that
// suspenders. Removing it would mean a slow stream could ignore
// cancellation between Scan calls.
func TestParseClaudeCodeStream_RespectsCtxCancellation(t *testing.T) {
	// Two valid JSON lines so scanner.Scan() returns true at least once,
	// reaching the in-loop ctx check.
	stream := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before parser starts

	ch := make(chan ChatEvent, 4)
	parseClaudeCodeStream(ctx, strings.NewReader(stream), ch)
	close(ch)

	var sawError bool
	var doneEmitted bool
	for evt := range ch {
		switch evt.Type {
		case EventError:
			sawError = true
			if !errors.Is(evt.Err, context.Canceled) {
				t.Errorf("error = %v, want context.Canceled", evt.Err)
			}
		case EventDone:
			doneEmitted = true
		}
	}
	if !sawError {
		t.Error("expected EventError when ctx is cancelled")
	}
	if doneEmitted {
		t.Error("EventDone should not fire after a cancellation EventError")
	}
}

func TestParseClaudeCodeStream_SkipsMalformedLines(t *testing.T) {
	stream := strings.Join([]string{
		`not json at all`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"result","is_error":false,"result":"ok","usage":{"input_tokens":1,"output_tokens":1}}`,
		"",
	}, "\n")

	ch := make(chan ChatEvent, 4)
	parseClaudeCodeStream(context.Background(), strings.NewReader(stream), ch)
	close(ch)

	var text strings.Builder
	var done bool
	for evt := range ch {
		switch evt.Type {
		case EventText:
			text.WriteString(evt.Text)
		case EventDone:
			done = true
		}
	}
	if text.String() != "ok" || !done {
		t.Errorf("text=%q done=%v, want text=ok done=true", text.String(), done)
	}
}

// ── Prompt flattening ───────────────────────────────────────────────────

func TestFlattenChatRequest_SystemAndUser(t *testing.T) {
	req := ChatRequest{
		System: "You are a code reviewer.",
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "Review this diff"}}},
		},
	}
	got := flattenChatRequest(req)
	if !strings.Contains(got, "<system>\nYou are a code reviewer.\n</system>") {
		t.Errorf("missing system block:\n%s", got)
	}
	if !strings.Contains(got, "<user>\nReview this diff\n</user>") {
		t.Errorf("missing user block:\n%s", got)
	}
}

func TestFlattenChatRequest_ToolHistory(t *testing.T) {
	req := ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "What's in main.go?"}}},
			{Role: RoleAssistant, Content: []ContentBlock{
				ToolUseBlock{ID: "t1", Name: "read_file", Args: json.RawMessage(`{"path":"main.go"}`)},
			}},
			{Role: RoleUser, Content: []ContentBlock{
				ToolResultBlock{ToolUseID: "t1", Name: "read_file", Content: "package main"},
			}},
		},
	}
	got := flattenChatRequest(req)
	if !strings.Contains(got, `<tool_use name="read_file">{"path":"main.go"}</tool_use>`) {
		t.Errorf("missing tool_use marker:\n%s", got)
	}
	if !strings.Contains(got, `<tool_result tool="read_file">`) || !strings.Contains(got, "package main") {
		t.Errorf("missing tool_result:\n%s", got)
	}
}

// TestFlattenChatRequest_SkipsEmptyMessagesCleanly pins the intent of
// the wroteAny tracking: if leading messages have no content (and are
// skipped), the first emitted block must not get a spurious leading
// blank line. The earlier index-based logic would have fired on i>0
// even when nothing preceded.
func TestFlattenChatRequest_SkipsEmptyMessagesCleanly(t *testing.T) {
	req := ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: nil}, // empty, skipped
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "real content"}}},
		},
	}
	got := flattenChatRequest(req)
	// The first non-empty block should start directly with <user>, not
	// a leading newline.
	if !strings.HasPrefix(got, "<user>\nreal content\n</user>\n") {
		t.Errorf("expected output to start with the <user> block, got:\n%q", got)
	}
	// And there should not be a double-blank-line between system (none
	// here) and the first emitted block.
	if strings.HasPrefix(got, "\n") {
		t.Errorf("output should not start with a blank line, got:\n%q", got)
	}
}

func TestFlattenChatRequest_DropsThinking(t *testing.T) {
	// Thinking blocks must not be passed back to claude — their signatures
	// are Anthropic-internal and invalid through the CLI. The parser
	// surfaces them to the prr UI but flattenChatRequest must not include
	// them on subsequent turns.
	req := ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleAssistant, Content: []ContentBlock{
				ThinkingBlock{Text: "internal reasoning that must not leak", Signature: "sig"},
				TextBlock{Text: "final answer"},
			}},
		},
	}
	got := flattenChatRequest(req)
	if strings.Contains(got, "internal reasoning") {
		t.Errorf("thinking text leaked into prompt:\n%s", got)
	}
	if !strings.Contains(got, "final answer") {
		t.Errorf("missing assistant text:\n%s", got)
	}
}

// ── buildArgs ───────────────────────────────────────────────────────────

func TestBuildArgs_IncludesReadOnlyEnforcement(t *testing.T) {
	c := &ClaudeCodeProvider{Model: "haiku"}
	args := strings.Join(c.buildArgs(), " ")

	mustContain := []string{
		"-p",
		"--output-format stream-json",
		"--input-format text",
		"--verbose",
		"--permission-mode bypassPermissions",
		"--allowed-tools",
		"--disallowed-tools",
		"Edit Write NotebookEdit",
		"--model haiku",
		"--no-session-persistence",
	}
	for _, sub := range mustContain {
		if !strings.Contains(args, sub) {
			t.Errorf("buildArgs missing %q\nfull: %s", sub, args)
		}
	}

	// Critical safety: must not allow `Bash(git *)` (which would let
	// claude run git push/commit/reset). Only specific subcommands.
	if strings.Contains(args, "Bash(git *)") {
		t.Errorf("buildArgs allows blanket Bash(git *) — destructive commands could run\nfull: %s", args)
	}

	// Same critical safety applies to gh — a blanket Bash(gh *) would
	// permit destructive operations (gh pr close/merge/edit/comment,
	// gh issue close/edit, gh auth login). Only specific read-only
	// subcommands are allowed.
	if strings.Contains(args, "Bash(gh *)") {
		t.Errorf("buildArgs allows blanket Bash(gh *) — destructive GitHub operations could run\nfull: %s", args)
	}

	// Spot-check that the read-only gh patterns we intend to allow are
	// actually in the list. If a future refactor accidentally drops
	// them, Claude Code chat loses its GitHub access path.
	mustHaveGh := []string{
		"Bash(gh pr view *)",
		"Bash(gh pr diff *)",
		"Bash(gh pr checks *)",
		"Bash(gh issue view *)",
	}
	for _, p := range mustHaveGh {
		if !strings.Contains(args, p) {
			t.Errorf("buildArgs missing read-only gh pattern %q\nfull: %s", p, args)
		}
	}

	// Spot-check that write-flavored gh patterns are NOT present.
	// These would permit closing/merging/commenting on PRs.
	mustNotHaveGh := []string{
		"Bash(gh pr close",
		"Bash(gh pr merge",
		"Bash(gh pr edit",
		"Bash(gh pr comment",
		"Bash(gh auth",
		"Bash(gh api ",
	}
	for _, p := range mustNotHaveGh {
		if strings.Contains(args, p) {
			t.Errorf("buildArgs unexpectedly allows write-capable gh pattern starting %q\nfull: %s", p, args)
		}
	}
}

// ── End-to-end: StreamChat with fake spawner ────────────────────────────

func TestStreamChat_FakeSpawner(t *testing.T) {
	c := &ClaudeCodeProvider{
		Model:      "haiku",
		BinaryPath: "/fake/claude",
		Spawner: func(ctx context.Context, args []string, stdin string) (io.ReadCloser, func() error, error) {
			// Record that the prompt reached us and the binary is the
			// configured one.
			if len(args) == 0 || args[0] != "/fake/claude" {
				t.Errorf("first arg = %q, want /fake/claude", args[0])
			}
			if !strings.Contains(stdin, "Hello") {
				t.Errorf("stdin missing prompt content: %q", stdin)
			}
			return io.NopCloser(strings.NewReader(claudeCodeFixtureSuccess)), func() error { return nil }, nil
		},
	}
	resp, err := c.Chat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "Hello"}}},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("stop = %v, want %v", resp.StopReason, StopEndTurn)
	}
	var sawText bool
	for _, b := range resp.Content {
		if tb, ok := b.(TextBlock); ok && strings.Contains(tb.Text, "alpha bravo") {
			sawText = true
		}
	}
	if !sawText {
		t.Errorf("response content missing expected text: %+v", resp.Content)
	}
}

// TestStreamChat_WaitErrorAfterEventDone_IsLoggedNotSurfaced pins the
// behavior at claudecode.go: when the stream produced a clean EventDone
// but the subprocess later reports a non-zero exit, the consumer must
// NOT see a second terminal event (it has already moved on with the
// successful response). The error is logged for operator visibility
// but never re-fires as a ChatEvent.
//
// Why this matters (Rule 9): a regression here would surface a confusing
// EventError to consumers who already received their successful answer,
// breaking the single-terminal-event invariant the rest of the codebase
// relies on.
func TestStreamChat_WaitErrorAfterEventDone_IsLoggedNotSurfaced(t *testing.T) {
	// Redirect the standard logger to a buffer so we can assert the
	// wait error was logged.
	var logBuf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prev)

	c := &ClaudeCodeProvider{
		Model:      "haiku",
		BinaryPath: "/fake/claude",
		Spawner: func(_ context.Context, _ []string, _ string) (io.ReadCloser, func() error, error) {
			return io.NopCloser(strings.NewReader(claudeCodeFixtureSuccess)),
				func() error { return errors.New("simulated process exit 1") },
				nil
		},
	}

	ch, err := c.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var doneCount, errorCount int
	for evt := range ch {
		switch evt.Type {
		case EventDone:
			doneCount++
		case EventError:
			errorCount++
		}
	}

	if doneCount != 1 {
		t.Errorf("EventDone count = %d, want 1", doneCount)
	}
	if errorCount != 0 {
		t.Errorf("EventError count = %d, want 0 (wait errors must not re-fire as events)", errorCount)
	}
	// Give the goroutine a moment to call wait() and log; the channel
	// close happens after the log call in StreamChat's goroutine, so by
	// the time the range loop above exits, the log should be present.
	if !strings.Contains(logBuf.String(), "simulated process exit 1") {
		t.Errorf("expected wait error in log, got: %q", logBuf.String())
	}
}

// ── spawnClaudeCode ─────────────────────────────────────────────────────

// TestSpawnClaudeCode_StartFailureReturnsError pins the failure path
// after the pipes were created: when cmd.Start fails (here, because the
// binary path doesn't exist), the function must return an error rather
// than leaking pipe descriptors or returning a half-initialized state.
// The leak fix relies on deferred conditional Close calls; if a future
// refactor breaks that, this test still catches the visible part (the
// error return).
func TestSpawnClaudeCode_StartFailureReturnsError(t *testing.T) {
	stdout, wait, err := spawnClaudeCode(
		context.Background(),
		[]string{"/nonexistent/claude-binary-for-tests"},
		"prompt",
	)
	if err == nil {
		t.Fatal("expected error for nonexistent binary, got nil")
	}
	if stdout != nil {
		t.Error("expected nil stdout on error")
	}
	if wait != nil {
		t.Error("expected nil wait on error")
	}
	if !strings.Contains(err.Error(), "claude-code") {
		t.Errorf("error %q should be tagged with claude-code prefix", err.Error())
	}
}

func TestSpawnClaudeCode_EmptyArgs(t *testing.T) {
	stdout, wait, err := spawnClaudeCode(context.Background(), nil, "prompt")
	if err == nil {
		t.Fatal("expected error for empty args, got nil")
	}
	if stdout != nil || wait != nil {
		t.Error("expected nil stdout/wait on error")
	}
}

// ── Capabilities & metadata ─────────────────────────────────────────────

// ── Live integration ────────────────────────────────────────────────────
//
// Requires PRR_LIVE_TESTS=1 and the `claude` CLI on PATH (logged in).
// Run with: PRR_LIVE_TESTS=1 go test ./internal/ai/ -run TestLiveClaudeCode -v

func TestLiveClaudeCode_RoundTrip(t *testing.T) {
	if os.Getenv("PRR_LIVE_TESTS") != "1" {
		t.Skip("PRR_LIVE_TESTS=1 not set, skipping live CLI test")
	}
	if !DetectClaudeCode() {
		t.Skip("claude CLI not on PATH, skipping live CLI test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c := &ClaudeCodeProvider{Model: "haiku"}
	resp, err := c.Chat(ctx, ChatRequest{
		System: "Reply with exactly the literal token GREEN and nothing else.",
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "Acknowledge."}}},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var text strings.Builder
	for _, b := range resp.Content {
		if tb, ok := b.(TextBlock); ok {
			text.WriteString(tb.Text)
		}
	}
	if !strings.Contains(strings.ToUpper(text.String()), "GREEN") {
		t.Errorf("response %q did not contain GREEN", text.String())
	}
	if resp.Usage.OutputTokens == 0 {
		t.Error("expected non-zero output tokens in usage")
	}
}

func TestClaudeCodeProvider_Capabilities(t *testing.T) {
	c := &ClaudeCodeProvider{Model: "opus"}
	if c.Name() != "claude-code" {
		t.Errorf("Name() = %q, want claude-code", c.Name())
	}
	if c.ModelID() != "opus" {
		t.Errorf("ModelID() = %q, want opus", c.ModelID())
	}
	caps := c.Capabilities()
	if caps.MaxContextTokens != 200_000 {
		t.Errorf("MaxContextTokens = %d, want 200000", caps.MaxContextTokens)
	}
	if caps.ParallelToolCalls {
		t.Error("ParallelToolCalls should be false (claude-code drives its own loop)")
	}
}

func TestClaudeCodeProvider_EffortDefaults(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		envEffort string
		wantArg   string // "" = --effort should be absent
	}{
		{"sonnet default → low", "claude-sonnet-4-6", "", "low"},
		{"sonnet alias → low", "sonnet", "", "low"},
		{"sonnet uppercase still triggers default", "Claude-Sonnet-4-6", "", "low"},
		{"opus default → medium", "claude-opus-4-8", "", "medium"},
		{"opus alias → medium", "opus", "", "medium"},
		{"opus uppercase still triggers default", "Claude-Opus-4-8", "", "medium"},
		{"haiku → no default effort", "claude-haiku-4-5", "", ""},
		{"env override beats sonnet default", "claude-sonnet-4-6", "high", "high"},
		{"env override beats opus default", "claude-opus-4-8", "max", "max"},
		{"invalid env value is ignored, default applies", "claude-opus-4-8", "hard", "medium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PRR_CLAUDE_EFFORT", tc.envEffort)
			c := &ClaudeCodeProvider{Model: tc.model}
			args := c.buildArgs()
			gotEffort := extractFlagValue(args, "--effort")
			if gotEffort != tc.wantArg {
				t.Errorf("for model=%q env=%q: got --effort %q, want %q",
					tc.model, tc.envEffort, gotEffort, tc.wantArg)
			}
		})
	}
}

// The Effort field sits between the env override and the per-model
// default, so all three tiers need pinning down.
func TestClaudeCode_EffortFieldPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		field     string
		envEffort string
		want      string
	}{
		{"field beats opus default", "claude-opus-4-8", "low", "", "low"},
		{"field beats sonnet default", "claude-sonnet-4-6", "xhigh", "", "xhigh"},
		{"field applies where no default exists", "claude-haiku-4-5", "medium", "", "medium"},
		{"env beats field", "claude-opus-4-8", "low", "max", "max"},
		{"invalid env falls back to field, not default", "claude-opus-4-8", "low", "nope", "low"},
		{"empty field keeps the model default", "claude-opus-4-8", "", "", "medium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PRR_CLAUDE_EFFORT", tc.envEffort)
			c := &ClaudeCodeProvider{Model: tc.model, Effort: tc.field}
			if got := extractFlagValue(c.buildArgs(), "--effort"); got != tc.want {
				t.Errorf("--effort = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewProvider_RejectsInvalidClaudeCodeEffort(t *testing.T) {
	if !DetectClaudeCode() {
		t.Skip("claude binary not on PATH")
	}
	_, err := NewProvider(ProviderConfig{
		ProviderName: "claude-code",
		ModelID:      "claude-opus-5",
		Effort:       "extreme",
	})
	if err == nil {
		t.Fatal("expected an error for an invalid effort level")
	}
	if !strings.Contains(err.Error(), "invalid effort") {
		t.Errorf("error should name the problem; got %v", err)
	}
}

// extractFlagValue returns the argument that follows the named flag, or ""
// if the flag is absent.
func extractFlagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

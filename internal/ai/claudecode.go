package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"

	"github.com/andreujuanc/prr/internal/config"
)

func init() {
	// Register the keyless-availability detector so config.ConfiguredProviders
	// surfaces "claude-code" automatically when the CLI is on PATH.
	config.KeylessProviderAvailable["claude-code"] = DetectClaudeCode
}

// ── Detection ───────────────────────────────────────────────────────────

// claudeCodeBinaryName is the executable looked up on PATH.
const claudeCodeBinaryName = "claude"

var (
	claudeCodeDetectOnce sync.Once
	claudeCodeDetected   bool
	claudeCodeBinaryPath string
)

// DetectClaudeCode returns true if the `claude` CLI is on PATH.
// The result is cached for the process lifetime.
func DetectClaudeCode() bool {
	claudeCodeDetectOnce.Do(func() {
		path, err := exec.LookPath(claudeCodeBinaryName)
		if err == nil && path != "" {
			claudeCodeDetected = true
			claudeCodeBinaryPath = path
		}
	})
	return claudeCodeDetected
}

// ClaudeCodeBinaryPath returns the resolved path to the `claude` binary,
// or "" if not detected. DetectClaudeCode must be called first.
func ClaudeCodeBinaryPath() string {
	DetectClaudeCode()
	return claudeCodeBinaryPath
}

// ── Read-only tool allowlist ────────────────────────────────────────────
//
// Patterns are passed verbatim to `claude --allowed-tools`. We deliberately
// list specific subcommands rather than blanket patterns like
// `Bash(git *)` or `Bash(gh *)` — those would permit destructive
// operations (push, commit, reset, pull, rebase, stash for git; close,
// merge, edit, comment for gh). Edit/Write/NotebookEdit are also
// explicitly denied via claudeCodeDisallowedTools.

var claudeCodeAllowedTools = []string{
	"Read", "Grep", "Glob",

	// Read-only git subcommands.
	"Bash(git log *)",
	"Bash(git show *)",
	"Bash(git diff *)",
	"Bash(git status*)",
	"Bash(git blame *)",
	"Bash(git ls-files*)",
	"Bash(git ls-tree*)",
	"Bash(git cat-file *)",
	"Bash(git rev-parse *)",
	"Bash(git rev-list *)",
	"Bash(git grep *)",
	"Bash(git describe *)",
	"Bash(git config --get *)",
	"Bash(git branch)",
	"Bash(git tag)",

	// Read-only gh (GitHub CLI) subcommands. Lets Claude Code answer
	// ad-hoc questions about PR state, CI, comments, issues — closing
	// the chat-time and edge-case gaps the pre-computed PR Brief
	// doesn't reach. NO blanket Bash(gh *) — that would permit close,
	// merge, edit, comment, and other write operations.
	"Bash(gh pr view *)",
	"Bash(gh pr diff *)",
	"Bash(gh pr checks *)",
	"Bash(gh pr list *)",
	"Bash(gh pr status*)",
	"Bash(gh issue view *)",
	"Bash(gh issue list *)",
	"Bash(gh repo view *)",
	"Bash(gh search *)",

	// Read-only utilities.
	"Bash(rg *)",
	"Bash(grep *)",
	"Bash(find *)",
	"Bash(ls *)",
	"Bash(cat *)",
	"Bash(head *)",
	"Bash(tail *)",
	"Bash(wc *)",
	"Bash(file *)",
}

var claudeCodeDisallowedTools = []string{
	"Edit", "Write", "NotebookEdit",
}

// ── Provider ────────────────────────────────────────────────────────────

// ClaudeCodeProvider implements Provider by shelling out to the `claude`
// CLI in -p (print) mode. Each StreamChat invocation spawns a fresh
// subprocess; Claude Code drives its own internal tool loop with a
// curated read-only toolset (see claudeCodeAllowedTools), and we expose
// only the final text/thinking content to the prr Agent layer.
//
// Auth is handled entirely by Claude Code (subscription, OAuth keychain,
// or ANTHROPIC_API_KEY) — prr does not need an API key for this provider.
type ClaudeCodeProvider struct {
	// Model is a Claude Code model name or alias ("opus", "sonnet",
	// "haiku", "claude-opus-4-7", etc.).
	Model string

	// BinaryPath optionally overrides the resolved claude binary path
	// (useful for tests). Empty means "use whatever DetectClaudeCode found".
	BinaryPath string

	// WorkDir is the directory passed via --add-dir. Empty means "do not
	// pass --add-dir" (Claude Code defaults to its cwd).
	WorkDir string

	// ExtraArgs are additional flags appended after the standard set.
	// Used by tests to inject overrides.
	ExtraArgs []string

	// Spawner is the function used to start the subprocess. Tests
	// override this to run a fake binary. Defaults to spawnClaudeCode.
	Spawner func(ctx context.Context, args []string, stdin string) (io.ReadCloser, func() error, error)
}

func (c *ClaudeCodeProvider) Name() string    { return "claude-code" }
func (c *ClaudeCodeProvider) ModelID() string { return c.Model }

func (c *ClaudeCodeProvider) Capabilities() Capabilities {
	return Capabilities{
		PromptCaching:     true,  // Claude Code does its own caching internally
		StructuredOutput:  false, // not exposed via the CLI
		ParallelToolCalls: false, // tools run inside Claude Code, not via prr's loop
		MaxContextTokens:  200_000,
		RunsOwnToolLoop:   true, // Claude Code uses its own Read/Grep/Glob/Bash
	}
}

// resolveBinary returns the path to the claude binary, preferring an
// explicitly configured path over the PATH-based detection.
func (c *ClaudeCodeProvider) resolveBinary() (string, error) {
	if c.BinaryPath != "" {
		return c.BinaryPath, nil
	}
	if path := ClaudeCodeBinaryPath(); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("claude-code: %q not found on PATH", claudeCodeBinaryName)
}

// buildArgs returns the CLI args (excluding the binary itself).
func (c *ClaudeCodeProvider) buildArgs() []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "text",
		"--verbose",
		"--permission-mode", "bypassPermissions",
		"--allowed-tools", strings.Join(claudeCodeAllowedTools, " "),
		"--disallowed-tools", strings.Join(claudeCodeDisallowedTools, " "),
		"--no-session-persistence",
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.WorkDir != "" {
		args = append(args, "--add-dir", c.WorkDir)
	}
	args = append(args, c.ExtraArgs...)
	return args
}

// spawnClaudeCode is the default Spawner. It starts the binary, writes the
// prompt to stdin, and returns stdout for the caller to read. The returned
// "wait" function blocks until the process exits and surfaces non-zero
// exits as errors (with stderr included).
//
// If any step after a pipe is created fails, the already-opened pipe(s)
// are closed before returning so we don't leak file descriptors.
func spawnClaudeCode(ctx context.Context, args []string, stdin string) (io.ReadCloser, func() error, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("claude-code: empty args")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	// cmd.Env left nil — exec.Cmd inherits the parent process environment
	// by default, which is what we want so claude can read its keychain /
	// ANTHROPIC_API_KEY / HOME / PATH.

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("claude-code: stdout pipe: %w", err)
	}
	// Close pipes if we return an error after creating them. Flipped to
	// false on the success path so the caller takes ownership.
	success := false
	defer func() {
		if !success {
			stdoutPipe.Close()
		}
	}()

	stderrBuf := &strings.Builder{}
	cmd.Stderr = newCappedWriter(stderrBuf, 16*1024)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("claude-code: stdin pipe: %w", err)
	}
	defer func() {
		if !success {
			stdinPipe.Close()
		}
	}()

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("claude-code: start: %w", err)
	}
	success = true

	// Write prompt asynchronously and close stdin so the CLI sees EOF.
	// If the write or close fails the subprocess will see a short/no
	// prompt and exit with an error from Wait — but without a log
	// line here that root cause is invisible. Surface it so support
	// triage doesn't end up chasing a confused stderr trail.
	go func() {
		if _, err := io.WriteString(stdinPipe, stdin); err != nil {
			log.Printf("claude-code: stdin write: %v", err)
		}
		if err := stdinPipe.Close(); err != nil {
			log.Printf("claude-code: stdin close: %v", err)
		}
	}()

	wait := func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("claude-code: %w (stderr: %s)", err, strings.TrimSpace(stderrBuf.String()))
		}
		return nil
	}
	return stdoutPipe, wait, nil
}

// cappedWriter discards writes once the wrapped builder reaches `limit` bytes.
type cappedWriter struct {
	buf   *strings.Builder
	limit int
}

func newCappedWriter(buf *strings.Builder, limit int) *cappedWriter {
	return &cappedWriter{buf: buf, limit: limit}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.limit {
		return len(p), nil
	}
	remaining := w.limit - w.buf.Len()
	if len(p) <= remaining {
		w.buf.Write(p)
	} else {
		w.buf.Write(p[:remaining])
	}
	return len(p), nil
}

// ── Chat / StreamChat ───────────────────────────────────────────────────

// Chat performs a non-streaming request by collecting StreamChat events.
func (c *ClaudeCodeProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	ch, err := c.StreamChat(ctx, req)
	if err != nil {
		return nil, err
	}
	var resp *ChatResponse
	for event := range ch {
		switch event.Type {
		case EventError:
			return nil, event.Err
		case EventDone:
			resp = event.Response
		}
	}
	if resp == nil {
		return nil, fmt.Errorf("claude-code: no response received")
	}
	return resp, nil
}

// StreamChat spawns `claude -p` and translates its stream-json output into
// canonical ChatEvents. Tool calls are *not* surfaced to the agent layer —
// Claude Code resolves them internally with its read-only toolset.
func (c *ClaudeCodeProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	bin, err := c.resolveBinary()
	if err != nil {
		return nil, err
	}

	prompt := flattenChatRequest(req)
	args := append([]string{bin}, c.buildArgs()...)

	spawn := c.Spawner
	if spawn == nil {
		spawn = spawnClaudeCode
	}

	stdout, wait, err := spawn(ctx, args, prompt)
	if err != nil {
		return nil, err
	}

	ch := make(chan ChatEvent, 64)
	go func() {
		defer close(ch)
		defer stdout.Close()
		parseClaudeCodeStream(ctx, stdout, ch)
		// parseClaudeCodeStream always emits a terminal event (EventDone
		// or EventError) before returning, so the consumer has already
		// received its result by this point. We still call wait() to
		// reap the process and surface any non-zero exit in the logs;
		// we do not emit an additional ChatEvent (that would double-fire
		// for the consumer who has already moved on).
		if err := wait(); err != nil {
			log.Printf("claude-code: process exit: %v", err)
		}
	}()
	return ch, nil
}

// ── Prompt flattening ───────────────────────────────────────────────────

// flattenChatRequest renders a ChatRequest as a single text prompt suitable
// for `claude -p` stdin. The system prompt and conversation history are
// inlined with explicit role markers; previous tool-use/tool-result blocks
// are rendered as XML-ish history so Claude has continuity context.
//
// We do not try to re-drive prr's tool loop through Claude — Claude Code
// runs its own loop. Tool history is included only so the model can see
// what was already retrieved in earlier turns of the prr conversation.
func flattenChatRequest(req ChatRequest) string {
	var b strings.Builder

	if strings.TrimSpace(req.System) != "" {
		b.WriteString("<system>\n")
		b.WriteString(strings.TrimRight(req.System, "\n"))
		b.WriteString("\n</system>\n\n")
	}

	// wroteAny tracks whether we've already emitted a message block so
	// we know when to insert a blank-line separator. Index-based logic
	// would over-fire when leading messages are skipped (empty content).
	wroteAny := false
	for _, msg := range req.Messages {
		role := "user"
		if msg.Role == RoleAssistant {
			role = "assistant"
		}

		// Skip empty messages
		if len(msg.Content) == 0 {
			continue
		}

		if wroteAny {
			b.WriteString("\n")
		}
		wroteAny = true
		fmt.Fprintf(&b, "<%s>\n", role)
		for _, block := range msg.Content {
			switch v := block.(type) {
			case TextBlock:
				if t := strings.TrimRight(v.Text, "\n"); t != "" {
					b.WriteString(t)
					b.WriteString("\n")
				}
			case ThinkingBlock:
				// Drop thinking text from the prompt — it's noise for the
				// next turn and we can't echo Anthropic-side signatures
				// through the CLI anyway.
			case ToolUseBlock:
				fmt.Fprintf(&b, "<tool_use name=%q>%s</tool_use>\n", v.Name, string(v.Args))
			case ToolResultBlock:
				marker := "result"
				if v.IsError {
					marker = "error"
				}
				fmt.Fprintf(&b, "<tool_%s tool=%q>\n%s\n</tool_%s>\n", marker, v.Name, v.Content, marker)
			}
		}
		fmt.Fprintf(&b, "</%s>\n", role)
	}

	return b.String()
}

// ── Stream parsing ──────────────────────────────────────────────────────

// claudeCodeStreamLine is the union of event types emitted on stdout when
// `--output-format stream-json --verbose` is set. We only decode the
// fields we actually use.
type claudeCodeStreamLine struct {
	Type    string                 `json:"type"`
	Subtype string                 `json:"subtype,omitempty"`
	Message *claudeCodeMessage     `json:"message,omitempty"`
	Result  string                 `json:"result,omitempty"`
	IsError bool                   `json:"is_error,omitempty"`
	Usage   *claudeCodeResultUsage `json:"usage,omitempty"`
}

type claudeCodeMessage struct {
	Content []claudeCodeContent `json:"content"`
}

type claudeCodeContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

type claudeCodeResultUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// seenKey returns a FNV-64 hash of (prefix, text), suitable as a map key
// for the dedup set in parseClaudeCodeStream. Hashing avoids storing the
// full text as a map key — for long answers that would mean keys of tens
// of KB each. Collision probability is astronomically low for the few
// hundred unique blocks we'd ever see per stream.
func seenKey(prefix, text string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(prefix))
	h.Write([]byte(text))
	return h.Sum64()
}

// parseClaudeCodeStream reads JSON-lines from r and emits ChatEvents.
// It tracks emitted text blocks to assemble a final ChatResponse for the
// EventDone event when the `result` line arrives.
func parseClaudeCodeStream(ctx context.Context, r io.Reader, ch chan<- ChatEvent) {
	var blocks []ContentBlock
	var usage TokenUsage
	doneEmitted := false
	// Track text blocks emitted per assistant message id to dedupe
	// repeated content (Claude Code re-emits the cumulative message
	// when streaming `--include-partial-messages` is off, but with
	// `--verbose` alone each `assistant` event is final per-block).
	seen := map[uint64]bool{}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			ch <- ChatEvent{Type: EventError, Err: ctx.Err()}
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}

		var evt claudeCodeStreamLine
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			// Malformed line — log and continue, the CLI occasionally
			// emits status lines we don't recognise.
			log.Printf("claude-code: skip malformed line: %v", err)
			continue
		}

		switch evt.Type {
		case "assistant":
			if evt.Message == nil {
				continue
			}
			for _, c := range evt.Message.Content {
				switch c.Type {
				case "text":
					if c.Text == "" {
						continue
					}
					key := seenKey("text:", c.Text)
					if seen[key] {
						continue
					}
					seen[key] = true
					blocks = append(blocks, TextBlock{Text: c.Text})
					ch <- ChatEvent{Type: EventText, Text: c.Text}
				case "thinking":
					if c.Thinking == "" {
						continue
					}
					key := seenKey("thinking:", c.Thinking)
					if seen[key] {
						continue
					}
					seen[key] = true
					blocks = append(blocks, ThinkingBlock{Text: c.Thinking})
					ch <- ChatEvent{Type: EventThinking, Text: c.Thinking}
				}
			}
		case "result":
			if evt.Usage != nil {
				usage = TokenUsage{
					InputTokens:  evt.Usage.InputTokens + evt.Usage.CacheCreationInputTokens,
					OutputTokens: evt.Usage.OutputTokens,
					CacheHits:    evt.Usage.CacheReadInputTokens,
				}
			}
			if evt.IsError {
				msg := evt.Result
				if msg == "" {
					msg = "claude-code: result reported error"
				}
				ch <- ChatEvent{Type: EventError, Err: fmt.Errorf("%s", msg)}
				return
			}
			// If the result text is non-empty and we never saw it as an
			// `assistant` text block (e.g. CLI compacted output), include
			// it so the caller still receives the final answer.
			if evt.Result != "" && !seen[seenKey("text:", evt.Result)] {
				blocks = append(blocks, TextBlock{Text: evt.Result})
				ch <- ChatEvent{Type: EventText, Text: evt.Result}
			}
			ch <- ChatEvent{
				Type: EventDone,
				Response: &ChatResponse{
					Content:    blocks,
					StopReason: StopEndTurn,
					Usage:      usage,
				},
			}
			doneEmitted = true
			return
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- ChatEvent{Type: EventError, Err: fmt.Errorf("claude-code: stream read: %w", err)}
		return
	}

	if !doneEmitted {
		// Stream ended without a `result` line — surface a synthetic Done
		// so callers don't hang. This usually indicates a CLI crash and
		// the wait() error will be logged separately.
		ch <- ChatEvent{
			Type: EventDone,
			Response: &ChatResponse{
				Content:    blocks,
				StopReason: StopError,
				Usage:      usage,
			},
		}
	}
}

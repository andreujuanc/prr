package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/andreujuanc/prr/internal/config"
)

func init() {
	// Register the keyless-availability detector so config.ConfiguredProviders
	// surfaces "opencode" automatically when the CLI is on PATH or at the
	// standard install location.
	config.KeylessProviderAvailable["opencode"] = DetectOpenCode
}

// ── Detection ───────────────────────────────────────────────────────────

const openCodeBinaryName = "opencode"

// openCodeStandardInstallPath is opencode's default installer location.
// We check it explicitly because users who installed via the official
// installer often haven't reloaded their shell to pick up the PATH entry.
var openCodeStandardInstallPath = filepath.Join(os.Getenv("HOME"), ".opencode", "bin", openCodeBinaryName)

var (
	openCodeDetectOnce sync.Once
	openCodeDetected   bool
	openCodeBinaryPath string
)

// DetectOpenCode returns true if the `opencode` CLI is available. Checks
// PATH first, then ~/.opencode/bin/opencode. Result cached for the
// process lifetime.
func DetectOpenCode() bool {
	openCodeDetectOnce.Do(func() {
		if path, err := exec.LookPath(openCodeBinaryName); err == nil && path != "" {
			openCodeDetected = true
			openCodeBinaryPath = path
			return
		}
		if info, err := os.Stat(openCodeStandardInstallPath); err == nil && info.Mode()&0o111 != 0 {
			openCodeDetected = true
			openCodeBinaryPath = openCodeStandardInstallPath
		}
	})
	return openCodeDetected
}

// OpenCodeBinaryPath returns the resolved path to the opencode binary,
// or "" if not detected. DetectOpenCode must be called first.
func OpenCodeBinaryPath() string {
	DetectOpenCode()
	return openCodeBinaryPath
}

// ── Provider ────────────────────────────────────────────────────────────

// OpenCodeProvider runs the local `opencode` CLI as a subprocess and
// translates its JSON event stream into ChatEvents.
//
// Like ClaudeCodeProvider, opencode runs its own internal tool loop —
// PRR does not surface tools to its own loop; the model uses opencode's
// native toolset. Token usage and per-call cost are taken directly from
// opencode's `step_finish` event (no shadow pricing needed; opencode
// reports cost natively for every provider it routes to).
type OpenCodeProvider struct {
	// Model is the full opencode model identifier, in the form
	// "provider/model-id" (e.g., "opencode/big-pickle",
	// "github-copilot/claude-opus-4.6"). opencode's --model flag wants
	// the provider/model form; if Model omits the slash, opencode will
	// reject the call. Callers wire this from cfg.ModelID — when the
	// PRR side strips the provider prefix (e.g., "opencode/<X>" →
	// "<X>"), we re-attach a default prefix below; otherwise pass
	// through.
	Model string

	// BinaryPath optionally overrides the resolved opencode binary path
	// (useful for tests). Empty means "use DetectOpenCode".
	BinaryPath string

	// ExtraArgs are appended after the standard arg set. Tests use this
	// to inject overrides.
	ExtraArgs []string

	// Spawner is the function used to start the subprocess. Tests
	// override this to run a fake binary. Defaults to spawnOpenCode.
	Spawner func(ctx context.Context, args []string, prompt string) (io.ReadCloser, func() error, error)
}

func (c *OpenCodeProvider) Name() string    { return "opencode" }
func (c *OpenCodeProvider) ModelID() string { return c.Model }

func (c *OpenCodeProvider) Capabilities() Capabilities {
	return Capabilities{
		PromptCaching:     true,  // opencode does its own caching internally
		StructuredOutput:  false, // not exposed via the CLI
		ParallelToolCalls: false,
		MaxContextTokens:  200_000, // varies by routed model; safe upper-end default
		RunsOwnToolLoop:   true,    // opencode runs its own loop with native tools
	}
}

func (c *OpenCodeProvider) resolveBinary() (string, error) {
	if c.BinaryPath != "" {
		return c.BinaryPath, nil
	}
	if path := OpenCodeBinaryPath(); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("opencode: %q not found on PATH or at %s", openCodeBinaryName, openCodeStandardInstallPath)
}

// resolveModel normalizes the model string for opencode's --model flag.
// opencode expects "provider/model-id"; PRR sometimes splits the provider
// out into a separate field, leaving Model as just the bare model id. For
// those calls we cannot guess the provider, so the caller must pass a
// full provider/model string. This function just validates the shape.
func (c *OpenCodeProvider) resolveModel() (string, error) {
	if c.Model == "" {
		return "", fmt.Errorf("opencode: empty model id — opencode requires a 'provider/model-id' string")
	}
	if !strings.Contains(c.Model, "/") {
		return "", fmt.Errorf("opencode: model %q is missing a provider prefix — opencode needs the full 'provider/model-id' form (e.g. 'opencode/big-pickle', 'github-copilot/claude-opus-4.6')", c.Model)
	}
	return c.Model, nil
}

// buildArgs returns the CLI args (excluding the binary itself and the
// trailing positional prompt). Honors PRR_OPENCODE_VARIANT to pass
// opencode's --variant flag (provider-specific reasoning-effort knob:
// "minimal", "low", "medium", "high", "max" for GPT-5-shaped models).
// Empty / unset means "use opencode's default for the routed model."
func (c *OpenCodeProvider) buildArgs(model string) []string {
	args := []string{
		"run",
		"--format", "json",
		"--model", model,
	}
	if v := strings.TrimSpace(os.Getenv("PRR_OPENCODE_VARIANT")); v != "" {
		args = append(args, "--variant", v)
	}
	args = append(args, c.ExtraArgs...)
	return args
}

// spawnOpenCode starts the binary with the prompt as a single positional
// argument and returns stdout for the caller to read. opencode's `run`
// subcommand takes the prompt as positional args; for AOI-scan-sized
// inputs (~6KB) this is well below any OS arg-length limit.
func spawnOpenCode(ctx context.Context, args []string, prompt string) (io.ReadCloser, func() error, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("opencode: empty args")
	}
	full := append(append([]string{}, args...), prompt)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("opencode: stdout pipe: %w", err)
	}
	success := false
	defer func() {
		if !success {
			stdoutPipe.Close()
		}
	}()

	stderrBuf := &strings.Builder{}
	cmd.Stderr = newCappedWriter(stderrBuf, 16*1024)

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("opencode: start: %w", err)
	}
	success = true

	wait := func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("opencode: %w (stderr: %s)", err, strings.TrimSpace(stderrBuf.String()))
		}
		return nil
	}
	return stdoutPipe, wait, nil
}

// ── Chat / StreamChat ───────────────────────────────────────────────────

// Chat performs a non-streaming request by collecting StreamChat events.
func (c *OpenCodeProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
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
		return nil, fmt.Errorf("opencode: no response received")
	}
	return resp, nil
}

// StreamChat spawns `opencode run --format json --model X <prompt>` and
// translates the JSON event stream into canonical ChatEvents.
func (c *OpenCodeProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	bin, err := c.resolveBinary()
	if err != nil {
		return nil, err
	}
	model, err := c.resolveModel()
	if err != nil {
		return nil, err
	}

	prompt := flattenChatRequest(req) // shared with claudecode; same XML-ish shape
	args := append([]string{bin}, c.buildArgs(model)...)

	spawn := c.Spawner
	if spawn == nil {
		spawn = spawnOpenCode
	}

	stdout, wait, err := spawn(ctx, args, prompt)
	if err != nil {
		return nil, err
	}

	ch := make(chan ChatEvent, 64)
	go func() {
		defer close(ch)
		defer stdout.Close()
		parseOpenCodeStream(ctx, stdout, ch)
		if err := wait(); err != nil {
			log.Printf("opencode: process exit: %v", err)
		}
	}()
	return ch, nil
}

// ── Stream parsing ──────────────────────────────────────────────────────

// openCodeEvent is the union of event types emitted by `opencode run
// --format json`. We decode only the fields we use.
type openCodeEvent struct {
	Type string          `json:"type"`
	Part openCodeEventPart `json:"part"`
}

type openCodeEventPart struct {
	Type   string                `json:"type"`
	Text   string                `json:"text,omitempty"`
	Reason string                `json:"reason,omitempty"`
	Tokens *openCodeEventTokens  `json:"tokens,omitempty"`
	Cost   *float64              `json:"cost,omitempty"`
	Error  string                `json:"error,omitempty"`
}

type openCodeEventTokens struct {
	Total     int               `json:"total"`
	Input     int               `json:"input"`
	Output    int               `json:"output"`
	Reasoning int               `json:"reasoning"`
	Cache     openCodeEventCache `json:"cache"`
}

type openCodeEventCache struct {
	Write int `json:"write"`
	Read  int `json:"read"`
}

// parseOpenCodeStream reads JSON-lines from r and emits ChatEvents. The
// final event is always EventDone (on stop) or EventError (on failure or
// missing terminal event).
func parseOpenCodeStream(ctx context.Context, r io.Reader, ch chan<- ChatEvent) {
	var blocks []ContentBlock
	var usage TokenUsage
	doneEmitted := false

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

		var evt openCodeEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			log.Printf("opencode: skip malformed line: %v", err)
			continue
		}

		switch evt.Type {
		case "text":
			if evt.Part.Text == "" {
				continue
			}
			blocks = append(blocks, TextBlock{Text: evt.Part.Text})
			ch <- ChatEvent{Type: EventText, Text: evt.Part.Text}

		case "step_finish":
			if evt.Part.Tokens != nil {
				usage = TokenUsage{
					InputTokens:  evt.Part.Tokens.Input,
					OutputTokens: evt.Part.Tokens.Output + evt.Part.Tokens.Reasoning,
					CacheHits:    evt.Part.Tokens.Cache.Read,
				}
			}
			if evt.Part.Cost != nil {
				usage.ReportedCostUSD = *evt.Part.Cost
			}
			stop := StopEndTurn
			switch evt.Part.Reason {
			case "stop", "end_turn", "":
				stop = StopEndTurn
			case "tool_use":
				stop = StopToolUse
			case "max_tokens", "length":
				stop = StopMaxTokens
			case "error":
				msg := evt.Part.Error
				if msg == "" {
					msg = "opencode: step_finish reported error"
				}
				ch <- ChatEvent{Type: EventError, Err: fmt.Errorf("%s", msg)}
				return
			default:
				stop = StopEndTurn
			}
			ch <- ChatEvent{
				Type: EventDone,
				Response: &ChatResponse{
					Content:    blocks,
					StopReason: stop,
					Usage:      usage,
				},
			}
			doneEmitted = true
			return

		case "error":
			msg := evt.Part.Error
			if msg == "" {
				msg = "opencode: stream emitted error event"
			}
			ch <- ChatEvent{Type: EventError, Err: fmt.Errorf("%s", msg)}
			return
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- ChatEvent{Type: EventError, Err: fmt.Errorf("opencode: stream read: %w", err)}
		return
	}

	if !doneEmitted {
		// Stream ended without a step_finish — surface a synthetic Done
		// so the caller doesn't hang. wait() will surface the cause
		// separately.
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

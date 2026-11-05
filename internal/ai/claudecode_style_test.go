package ai

import (
	"context"
	"strings"
	"testing"
)

func TestOutputStyleMismatch(t *testing.T) {
	if got := outputStyleMismatch(claudeCodeOutputStyle); got != "" {
		t.Errorf("matching style should not warn, got %q", got)
	}
	// Names are case-sensitive in Claude Code; a lowercase match is a
	// silent fallback to Default and must warn.
	if got := outputStyleMismatch("default"); !strings.Contains(got, `"default"`) {
		t.Errorf("mismatched style should warn naming it, got %q", got)
	}
	if got := outputStyleMismatch(""); !strings.Contains(got, "did not report") {
		t.Errorf("missing style should warn about the CLI version, got %q", got)
	}
}

// The pinned style name must be what actually goes into --settings.
func TestClaudeCode_SettingsCarryPinnedStyle(t *testing.T) {
	c := &ClaudeCodeProvider{Model: "claude-opus-5"}
	got := extractFlagValue(c.buildArgs(), "--settings")
	if !strings.Contains(got, `"outputStyle":"`+claudeCodeOutputStyle+`"`) {
		t.Errorf("--settings = %q, want outputStyle %q", got, claudeCodeOutputStyle)
	}
}

// The system/init event carries output_style; parsing it must not
// disturb the rest of the stream.
func TestParseClaudeCodeStream_InitEventDoesNotDisturbStream(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"system","subtype":"init","output_style":"Concise","model":"claude-opus-5"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"result","subtype":"success","result":"ok","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n")
	ch := make(chan ChatEvent, 32)
	parseClaudeCodeStream(context.Background(), strings.NewReader(lines), ch)
	close(ch)

	var text strings.Builder
	done := false
	for ev := range ch {
		switch ev.Type {
		case EventText:
			text.WriteString(ev.Text)
		case EventDone:
			done = true
		case EventError:
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}
	if !done || text.String() != "ok" {
		t.Errorf("done=%v text=%q, want done and %q", done, text.String(), "ok")
	}
}

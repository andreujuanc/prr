package audit

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewDebugWriter_Disabled(t *testing.T) {
	d := NewDebugWriter(false)
	if d.Enabled() {
		t.Error("expected disabled writer")
	}
}

func TestNewDebugWriter_Enabled(t *testing.T) {
	d := NewDebugWriter(true)
	if !d.Enabled() {
		t.Error("expected enabled writer")
	}
}

func TestDebugWriter_DisabledNoOutput(t *testing.T) {
	var buf bytes.Buffer
	d := NewDebugWriter(false)
	d.SetOutput(&buf)

	d.Phase("test")
	d.Section("test")
	d.Text("hello %s", "world")
	d.Prompt("sys", "user")
	d.Response("raw")
	d.Separator()

	if buf.Len() != 0 {
		t.Errorf("expected no output when disabled, got %q", buf.String())
	}
}

func TestDebugWriter_Phase(t *testing.T) {
	var buf bytes.Buffer
	d := NewDebugWriter(true)
	d.SetOutput(&buf)

	d.Phase("Phase 1")
	out := buf.String()
	if !strings.Contains(out, "Phase 1") {
		t.Error("expected phase name in output")
	}
	if !strings.Contains(out, "═") {
		t.Error("expected separator characters")
	}
}

func TestDebugWriter_Section(t *testing.T) {
	var buf bytes.Buffer
	d := NewDebugWriter(true)
	d.SetOutput(&buf)

	d.Section("My Section")
	out := buf.String()
	if !strings.Contains(out, "My Section") {
		t.Error("expected section name in output")
	}
	if !strings.Contains(out, "───") {
		t.Error("expected section separator")
	}
}

func TestDebugWriter_Text(t *testing.T) {
	var buf bytes.Buffer
	d := NewDebugWriter(true)
	d.SetOutput(&buf)

	d.Text("count: %d", 42)
	if !strings.Contains(buf.String(), "count: 42") {
		t.Error("expected formatted text in output")
	}
}

func TestDebugWriter_Prompt(t *testing.T) {
	var buf bytes.Buffer
	d := NewDebugWriter(true)
	d.SetOutput(&buf)

	d.Prompt("system prompt", "user message")
	out := buf.String()
	if !strings.Contains(out, "system prompt") {
		t.Error("expected system prompt in output")
	}
	if !strings.Contains(out, "user message") {
		t.Error("expected user message in output")
	}
}

func TestDebugWriter_Response(t *testing.T) {
	var buf bytes.Buffer
	d := NewDebugWriter(true)
	d.SetOutput(&buf)

	d.Response("raw response")
	out := buf.String()
	if !strings.Contains(out, "raw response") {
		t.Error("expected raw response in output")
	}
	if !strings.Contains(out, "Raw Response") {
		t.Error("expected Raw Response header")
	}
}

func TestDebugWriter_Separator(t *testing.T) {
	var buf bytes.Buffer
	d := NewDebugWriter(true)
	d.SetOutput(&buf)

	d.Separator()
	if !strings.Contains(buf.String(), "─") {
		t.Error("expected separator characters")
	}
}

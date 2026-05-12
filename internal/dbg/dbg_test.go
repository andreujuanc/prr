package dbg

import (
	"bytes"
	"strings"
	"testing"
)

func TestNew_Disabled(t *testing.T) {
	d := New(false)
	if d.Enabled() {
		t.Error("expected disabled writer")
	}
}

func TestNew_Enabled(t *testing.T) {
	d := New(true)
	if !d.Enabled() {
		t.Error("expected enabled writer")
	}
}

func TestWriter_NilSafeEnabled(t *testing.T) {
	// Nil *Writer must be safe to call Enabled() on so callers can
	// guard expensive formatting without nil-checking first.
	var d *Writer
	if d.Enabled() {
		t.Error("nil Writer should report disabled")
	}
}

func TestWriter_DisabledNoOutput(t *testing.T) {
	var buf bytes.Buffer
	d := New(false)
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

func TestWriter_Phase(t *testing.T) {
	var buf bytes.Buffer
	d := New(true)
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

func TestWriter_Section(t *testing.T) {
	var buf bytes.Buffer
	d := New(true)
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

func TestWriter_Text(t *testing.T) {
	var buf bytes.Buffer
	d := New(true)
	d.SetOutput(&buf)

	d.Text("count: %d", 42)
	if !strings.Contains(buf.String(), "count: 42") {
		t.Error("expected formatted text in output")
	}
}

func TestWriter_Prompt(t *testing.T) {
	var buf bytes.Buffer
	d := New(true)
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

func TestWriter_Response(t *testing.T) {
	var buf bytes.Buffer
	d := New(true)
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

func TestWriter_Separator(t *testing.T) {
	var buf bytes.Buffer
	d := New(true)
	d.SetOutput(&buf)

	d.Separator()
	if !strings.Contains(buf.String(), "─") {
		t.Error("expected separator characters")
	}
}

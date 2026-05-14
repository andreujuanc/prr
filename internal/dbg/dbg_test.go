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

func TestWriter_Prompt_CompactSkipsSystemPrompt(t *testing.T) {
	// Default (compact) mode skips the system prompt — it's static
	// .md content and reprinting it on every LLM call is noise.
	var buf bytes.Buffer
	d := New(true)
	d.SetOutput(&buf)

	d.Prompt("system prompt body", "user message")
	out := buf.String()
	if strings.Contains(out, "system prompt body") {
		t.Errorf("compact mode should skip system prompt; got:\n%s", out)
	}
	if !strings.Contains(out, "user message") {
		t.Errorf("user message should always print; got:\n%s", out)
	}
	if strings.Contains(out, "─── System Prompt ───") {
		t.Errorf("compact mode should not print the System Prompt section header; got:\n%s", out)
	}
	if !strings.Contains(out, "─── User Message ───") {
		t.Errorf("user message section header should always print; got:\n%s", out)
	}
}

func TestWriter_Prompt_VerboseIncludesSystemPrompt(t *testing.T) {
	var buf bytes.Buffer
	d := New(true)
	d.SetVerbose(true)
	d.SetOutput(&buf)

	d.Prompt("system prompt body", "user message")
	out := buf.String()
	if !strings.Contains(out, "system prompt body") {
		t.Errorf("verbose mode should include system prompt; got:\n%s", out)
	}
	if !strings.Contains(out, "user message") {
		t.Errorf("user message missing; got:\n%s", out)
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

// ── File-block eliding ─────────────────────────────────────────────────

func TestPrompt_ElidesEmbeddedFileBlock_ByDefault(t *testing.T) {
	// AOI-scanner-style prompt: `=== <path> ===\n<content>\n\n`.
	userMsg := `Scan these 2 file(s) for areas of interest:

=== src/auth.go ===
package auth

func Authenticate(user, pw string) bool {
    return user == "admin" && pw == "letmein"
}

=== src/handler.go ===
package handler

import "net/http"

func Login(w http.ResponseWriter, r *http.Request) {
    user := r.URL.Query().Get("user")
    pw := r.URL.Query().Get("pw")
    if Authenticate(user, pw) {
        w.WriteHeader(http.StatusOK)
    }
}
`
	var buf bytes.Buffer
	d := New(true)
	d.SetOutput(&buf)
	d.Prompt("sys", userMsg)
	out := buf.String()

	// File paths must still appear — user needs to trace what was sent.
	if !strings.Contains(out, "=== src/auth.go ===") {
		t.Errorf("expected file header to survive eliding; got:\n%s", out)
	}
	if !strings.Contains(out, "=== src/handler.go ===") {
		t.Errorf("expected second file header; got:\n%s", out)
	}
	// Content must NOT appear — eliding is the whole point.
	if strings.Contains(out, "letmein") {
		t.Errorf("expected file content to be elided; got:\n%s", out)
	}
	if strings.Contains(out, "WriteHeader") {
		t.Errorf("expected file content to be elided; got:\n%s", out)
	}
	// Line count annotation should be there.
	if !strings.Contains(out, "lines elided") {
		t.Errorf("expected '[N lines elided]' annotation; got:\n%s", out)
	}
}

func TestPrompt_VerbosePreservesFileContent(t *testing.T) {
	// SetVerbose(true) disables eliding so users can inspect what the
	// LLM actually saw, with full content.
	userMsg := `=== src/auth.go ===
secret content here
more content
even more content
`
	var buf bytes.Buffer
	d := New(true)
	d.SetVerbose(true)
	d.SetOutput(&buf)
	d.Prompt("sys", userMsg)
	out := buf.String()

	if !strings.Contains(out, "secret content here") {
		t.Errorf("verbose mode should preserve file content; got:\n%s", out)
	}
	if strings.Contains(out, "lines elided") {
		t.Errorf("verbose mode should not elide; got:\n%s", out)
	}
}

func TestElideFileBlocks_PassesThroughWithoutMarkers(t *testing.T) {
	// Prompts without `=== path ===` markers should be returned
	// verbatim — no false eliding.
	input := `You are a senior code reviewer.

Process: read, verify, report.

Output JSON only.`
	if got := elideFileBlocks(input); got != input {
		t.Errorf("elideFileBlocks should pass through prompts without markers; got diff")
	}
}

func TestElideFileBlocks_SmallBlocksPassUnchanged(t *testing.T) {
	// A file block with ≤3 content lines isn't worth eliding —
	// collapsing it gains nothing and loses signal.
	input := "=== src/tiny.go ===\nline 1\nline 2\n\n"
	got := elideFileBlocks(input)
	if !strings.Contains(got, "line 1") || !strings.Contains(got, "line 2") {
		t.Errorf("small block should pass through unchanged; got:\n%s", got)
	}
	if strings.Contains(got, "lines elided") {
		t.Errorf("small block should not show elision annotation; got:\n%s", got)
	}
}

func TestElideFileBlocks_MultipleAdjacentBlocks(t *testing.T) {
	// Three back-to-back blocks must all be elided independently;
	// the boundaries between them must be preserved.
	input := strings.Join([]string{
		"=== a.go ===",
		"line a1", "line a2", "line a3", "line a4", "line a5",
		"",
		"=== b.go ===",
		"line b1", "line b2", "line b3", "line b4", "line b5",
		"",
		"=== c.go ===",
		"line c1", "line c2", "line c3", "line c4", "line c5",
	}, "\n")
	got := elideFileBlocks(input)

	// All three paths preserved.
	for _, p := range []string{"=== a.go ===", "=== b.go ===", "=== c.go ==="} {
		if !strings.Contains(got, p) {
			t.Errorf("missing header %q in:\n%s", p, got)
		}
	}
	// No raw content from any block.
	for _, c := range []string{"line a3", "line b3", "line c3"} {
		if strings.Contains(got, c) {
			t.Errorf("expected %q to be elided; got:\n%s", c, got)
		}
	}
}

func TestElideFileBlocks_IsNotFooledByPlainEqualsLines(t *testing.T) {
	// A non-path "===" line shouldn't be treated as a marker. Only
	// `=== <non-empty> ===` qualifies.
	input := "===\nhello\n===\nworld\n"
	if got := elideFileBlocks(input); got != input {
		t.Errorf("non-marker '===' lines should pass through; got:\n%s", got)
	}
}

package review

import (
	"strings"
	"testing"
)

type sample struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func TestUnmarshalLLMResponse_StrictPasses(t *testing.T) {
	// Happy-path JSON should not be touched.
	data := []byte(`{"title":"hi","body":"there"}`)
	var s sample
	if err := unmarshalLLMResponse(data, &s); err != nil {
		t.Fatalf("strict parse should succeed, got %v", err)
	}
	if s.Title != "hi" || s.Body != "there" {
		t.Fatalf("got %+v", s)
	}
}

func TestUnmarshalLLMResponse_LiteralTabInString(t *testing.T) {
	// The exact failure mode that aborted PR 16 reviews in production:
	// Gemini emitted a literal tab inside a string value instead of \t.
	data := []byte("{\"title\":\"hi\",\"body\":\"some\tcode\"}")
	var s sample
	if err := unmarshalLLMResponse(data, &s); err != nil {
		t.Fatalf("lenient parse should rescue literal tab; got %v", err)
	}
	if s.Body != "some\tcode" {
		t.Fatalf("body = %q, want %q", s.Body, "some\tcode")
	}
}

func TestUnmarshalLLMResponse_LiteralNewlineInString(t *testing.T) {
	data := []byte("{\"title\":\"hi\",\"body\":\"line1\nline2\"}")
	var s sample
	if err := unmarshalLLMResponse(data, &s); err != nil {
		t.Fatalf("lenient parse should rescue literal newline; got %v", err)
	}
	if s.Body != "line1\nline2" {
		t.Fatalf("body = %q", s.Body)
	}
}

func TestUnmarshalLLMResponse_LiteralCRInString(t *testing.T) {
	data := []byte("{\"title\":\"hi\",\"body\":\"a\rb\"}")
	var s sample
	if err := unmarshalLLMResponse(data, &s); err != nil {
		t.Fatalf("got %v", err)
	}
	if s.Body != "a\rb" {
		t.Fatalf("body = %q", s.Body)
	}
}

func TestUnmarshalLLMResponse_PreservesEscapedQuote(t *testing.T) {
	// "she said \"hello\"" must keep its escaped quotes; control-char
	// repair must not break properly escaped sequences.
	data := []byte(`{"title":"x","body":"she said \"hello\""}`)
	var s sample
	if err := unmarshalLLMResponse(data, &s); err != nil {
		t.Fatalf("got %v", err)
	}
	if s.Body != `she said "hello"` {
		t.Fatalf("body = %q", s.Body)
	}
}

func TestUnmarshalLLMResponse_PreservesEscapedBackslash(t *testing.T) {
	data := []byte(`{"title":"x","body":"path\\to\\file"}`)
	var s sample
	if err := unmarshalLLMResponse(data, &s); err != nil {
		t.Fatalf("got %v", err)
	}
	if s.Body != `path\to\file` {
		t.Fatalf("body = %q", s.Body)
	}
}

func TestUnmarshalLLMResponse_ControlCharsOutsideStringIgnored(t *testing.T) {
	// Whitespace control chars between fields are valid JSON (RFC 8259);
	// our repair must not touch them.
	data := []byte("{\n  \"title\": \"hi\",\n\t\"body\": \"there\"\n}")
	var s sample
	if err := unmarshalLLMResponse(data, &s); err != nil {
		t.Fatalf("got %v", err)
	}
	if s.Title != "hi" || s.Body != "there" {
		t.Fatalf("got %+v", s)
	}
}

func TestUnmarshalLLMResponse_NonReparirableErrorBubblesUp(t *testing.T) {
	// Truncated JSON — not something we can fix. The original error
	// must surface so a genuine bug isn't masked by silent repair.
	data := []byte(`{"title": "hi", "body":`)
	var s sample
	err := unmarshalLLMResponse(data, &s)
	if err == nil {
		t.Fatal("expected error on truncated JSON")
	}
	if !strings.Contains(err.Error(), "unexpected") &&
		!strings.Contains(err.Error(), "EOF") {
		t.Fatalf("expected truncation error, got %v", err)
	}
}

func TestEscapeControlCharsInStrings_Idempotent(t *testing.T) {
	// Well-formed JSON with already-escaped sequences should round-trip
	// unchanged.
	cases := []string{
		`{}`,
		`{"a":"b"}`,
		`{"a":"b\nc"}`,
		`{"a":"b\\nc"}`, // literal "b\nc" in the string value
		`[1,2,3]`,
		`{"nested":{"x":"y"}}`,
	}
	for _, in := range cases {
		out := escapeControlCharsInStrings([]byte(in))
		if string(out) != in {
			t.Errorf("idempotency broken for %q\n got: %q", in, string(out))
		}
	}
}

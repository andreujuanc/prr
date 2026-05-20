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

func TestExtractLastJSONValue_Single(t *testing.T) {
	in := []byte(`{"a":1,"b":"x"}`)
	out, err := extractLastJSONValue(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("expected unchanged single JSON, got %q", string(out))
	}
}

func TestExtractLastJSONValue_TwoBlocksWithProse(t *testing.T) {
	// Mimics Sonnet-under-Claude-Code: draft 1, prose, draft 2.
	in := []byte("{\"draft\":1}\n```\nLet me read the relevant files to investigate this concern.\nI have everything I need to make a definitive assessment.\n```json\n{\"draft\":2,\"refined\":true}\n```\n")
	out, err := extractLastJSONValue(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != `{"draft":2,"refined":true}` {
		t.Errorf("expected last JSON object; got %q", string(out))
	}
}

func TestExtractLastJSONValue_FencedSingle(t *testing.T) {
	in := []byte("```json\n{\"a\":1}\n```\n")
	out, err := extractLastJSONValue(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != `{"a":1}` {
		t.Errorf("expected inner JSON; got %q", string(out))
	}
}

func TestExtractLastJSONValue_BracesInsideStrings(t *testing.T) {
	// The literal "{}" inside a string value must NOT confuse the
	// parser. Decoder is structure-aware so this is the bar.
	in := []byte(`{"msg":"see {ok} or {error}"}`)
	out, err := extractLastJSONValue(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("expected unchanged; got %q", string(out))
	}
}

func TestExtractLastJSONValue_TrailingBackticks(t *testing.T) {
	in := []byte("{\"a\":1}\n```")
	out, err := extractLastJSONValue(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != `{"a":1}` {
		t.Errorf("expected JSON without trailing fence; got %q", string(out))
	}
}

func TestExtractLastJSONValue_Array(t *testing.T) {
	in := []byte(`prose [1,2,3] more`)
	out, err := extractLastJSONValue(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != `[1,2,3]` {
		t.Errorf("expected array; got %q", string(out))
	}
}

func TestExtractLastJSONValue_NoJSON(t *testing.T) {
	_, err := extractLastJSONValue([]byte("just prose with no JSON"))
	if err == nil {
		t.Error("expected error on pure prose input; got nil")
	}
}

func TestExtractLastJSONValue_PartialThenValid(t *testing.T) {
	// A truncated/broken JSON followed by a valid one — return the valid one.
	in := []byte(`{"oops":  garbage  } {"good":true}`)
	out, err := extractLastJSONValue(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != `{"good":true}` {
		t.Errorf("expected the valid trailing JSON; got %q", string(out))
	}
}

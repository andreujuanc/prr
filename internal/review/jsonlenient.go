package review

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// unmarshalLLMResponse is the unmarshaler used for LLM-emitted JSON
// where strict RFC-8259 conformance can't be assumed. Gemini Flash in
// particular sometimes emits raw control characters (tab, newline,
// carriage return, etc.) inside string values when the content it's
// quoting is itself source code — instead of escaping them to "\t",
// "\n", "\r" the way the spec requires.
//
// Go's encoding/json strictly rejects such input with:
//
//	"invalid character '\t' in string literal"
//
// which then trips the pipeline's "abort if >20% of AOI calls failed"
// safety threshold and aborts the whole run. That's a high cost for
// what's just a quoting bug in the model's output.
//
// This function preserves strict semantics for well-formed input
// (calls json.Unmarshal directly, no allocation in the happy path)
// and only falls back to escaping control chars when strict parse
// reports the kind of error we know how to fix.
//
// We do NOT silently swallow other parse errors — schema mismatches,
// truncated JSON, missing brackets, etc. continue to surface as
// errors so genuine bugs aren't hidden.
func unmarshalLLMResponse(data []byte, v any) error {
	err := json.Unmarshal(data, v)
	if err == nil {
		return nil
	}
	if !isControlCharStringError(err) {
		return err
	}
	repaired := escapeControlCharsInStrings(data)
	if bytes.Equal(repaired, data) {
		// Nothing to repair — return the original error.
		return err
	}
	return json.Unmarshal(repaired, v)
}

// isControlCharStringError reports whether err is the specific
// json.SyntaxError that escapeControlCharsInStrings can fix.
//
// json.SyntaxError doesn't expose an enum, only a message string;
// we match the prefix the standard library uses ("invalid character
// 'X' in string literal" or "in string escape code"). Cheap and
// future-proof enough — the messages haven't changed since 1.0.
func isControlCharStringError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Two related shapes the std lib emits for the relevant cases.
	return strContains(msg, "in string literal") ||
		strContains(msg, "in string escape code")
}

// strContains avoids importing strings for a single Contains; this
// keeps jsonlenient.go free of "strings" while bytes is already in
// scope for escapeControlCharsInStrings.
func strContains(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// previewForLog returns the first n bytes of raw rendered as a
// printable one-line string suitable for inclusion in a log message:
// newlines collapse to spaces, control bytes are escaped \xNN, and
// the string is suffixed with "…" when truncated. Used at LLM-JSON
// parse-failure sites so we can see what the model actually returned
// instead of guessing from a one-character error message.
func previewForLog(raw []byte, n int) string {
	truncated := false
	data := raw
	if len(data) > n {
		data = data[:n]
		truncated = true
	}
	var out bytes.Buffer
	out.Grow(len(data) + 8)
	for _, c := range data {
		switch {
		case c == '\n' || c == '\r':
			out.WriteByte(' ')
		case c < 0x20 || c == 0x7f:
			out.WriteString(fmt.Sprintf("\\x%02x", c))
		default:
			out.WriteByte(c)
		}
	}
	if truncated {
		out.WriteString("…")
	}
	return out.String()
}

// escapeControlCharsInStrings walks the JSON byte stream and replaces
// literal U+0000..U+001F characters that appear inside string values
// with their JSON-escape equivalents.
//
// The walker is a small state machine:
//
//	inString — true between an unescaped " and its matching "
//	escaped  — true for one character immediately after a \
//
// Characters outside strings are passed through unchanged so braces,
// brackets, commas, etc. are preserved exactly. Characters inside
// strings that are already part of an escape sequence (the byte
// after a backslash) are also passed through — we only rewrite raw
// control bytes that the model emitted unescaped.
//
// Idempotent for valid JSON: a well-formed document has no raw
// control bytes inside strings, so the output equals the input.
func escapeControlCharsInStrings(in []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(in))

	inString := false
	escaped := false

	for _, c := range in {
		if !inString {
			if c == '"' {
				inString = true
			}
			out.WriteByte(c)
			continue
		}
		// Inside a string.
		if escaped {
			// Whatever came after the backslash is part of an escape
			// sequence; preserve it verbatim.
			escaped = false
			out.WriteByte(c)
			continue
		}
		switch c {
		case '\\':
			escaped = true
			out.WriteByte(c)
		case '"':
			inString = false
			out.WriteByte(c)
		case '\n':
			out.WriteString(`\n`)
		case '\t':
			out.WriteString(`\t`)
		case '\r':
			out.WriteString(`\r`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		default:
			if c < 0x20 {
				// Other control chars get the \uXXXX form.
				out.WriteString(fmt.Sprintf(`\u%04x`, c))
			} else {
				out.WriteByte(c)
			}
		}
	}
	return out.Bytes()
}

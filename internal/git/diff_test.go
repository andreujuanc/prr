package git

import "testing"

func TestSanitizeANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no escapes", "hello world", "hello world"},
		{"preserves SGR color", "\x1b[31mred\x1b[0m", "\x1b[31mred\x1b[0m"},
		{"strips cursor up", "\x1b[2Ahello", "hello"},
		{"strips cursor down", "\x1b[3Bhello", "hello"},
		{"strips cursor forward", "\x1b[1Chello", "hello"},
		{"strips cursor positioning", "\x1b[10;20Hhello", "hello"},
		{"strips alt screen enable", "\x1b[?1049hhello", "hello"},
		{"strips alt screen disable", "\x1b[?1049lhello", "hello"},
		{"strips hide cursor", "\x1b[?25lhello", "hello"},
		{"strips OSC title", "\x1b]0;My Title\x07hello", "hello"},
		{"strips clear screen", "\x1b[2Jhello", "hello"},
		{"mixed: strip bad, keep SGR", "\x1b[2A\x1b[31mred\x1b[0m\x1b[?25l", "\x1b[31mred\x1b[0m"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeANSI(tt.input); got != tt.want {
				t.Errorf("sanitizeANSI() = %q, want %q", got, tt.want)
			}
		})
	}
}

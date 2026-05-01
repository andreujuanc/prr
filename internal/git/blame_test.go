package git

import (
	"testing"
)

func TestParsePorcelainBlame_SingleCommit(t *testing.T) {
	input := `abcdef1234567890abcdef1234567890abcdef12 1 1 3
author Jane Doe
author-mail <jane@example.com>
author-time 1700000000
author-tz +0000
committer Jane Doe
committer-mail <jane@example.com>
committer-time 1700000000
committer-tz +0000
summary Initial commit
filename main.go
	package main
abcdef1234567890abcdef1234567890abcdef12 2 2
	
abcdef1234567890abcdef1234567890abcdef12 3 3
	import "fmt"`
	result := parsePorcelainBlame(input)

	if len(result) != 3 {
		t.Fatalf("expected 3 blame lines, got %d", len(result))
	}

	for i := 1; i <= 3; i++ {
		bl, ok := result[i]
		if !ok {
			t.Errorf("missing blame for line %d", i)
			continue
		}
		if bl.Author != "Jane Doe" {
			t.Errorf("line %d: expected author 'Jane Doe', got '%s'", i, bl.Author)
		}
		if bl.Date != "2023-11-14" {
			t.Errorf("line %d: expected date '2023-11-14', got '%s'", i, bl.Date)
		}
	}
}

func TestParsePorcelainBlame_MultipleCommits(t *testing.T) {
	input := `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 1 1 1
author Alice
author-mail <alice@example.com>
author-time 1700000000
author-tz +0000
committer Alice
committer-mail <alice@example.com>
committer-time 1700000000
committer-tz +0000
summary First commit
filename main.go
	line one
bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 2 2 1
author Bob
author-mail <bob@example.com>
author-time 1700100000
author-tz +0000
committer Bob
committer-mail <bob@example.com>
committer-time 1700100000
committer-tz +0000
summary Second commit
filename main.go
	line two
`
	result := parsePorcelainBlame(input)

	if len(result) != 2 {
		t.Fatalf("expected 2 blame lines, got %d", len(result))
	}

	if result[1].Author != "Alice" {
		t.Errorf("line 1: expected 'Alice', got '%s'", result[1].Author)
	}
	if result[2].Author != "Bob" {
		t.Errorf("line 2: expected 'Bob', got '%s'", result[2].Author)
	}
}

func TestParsePorcelainBlame_ContinuationLines(t *testing.T) {
	// Commit appears first at line 1 with full metadata, then at line 2
	// as a continuation (no metadata). Both should get blame info.
	input := `cccccccccccccccccccccccccccccccccccccccc 1 1 2
author Carol
author-mail <carol@example.com>
author-time 1700000000
author-tz +0000
committer Carol
committer-mail <carol@example.com>
committer-time 1700000000
committer-tz +0000
summary Add feature
filename app.go
	func main() {
cccccccccccccccccccccccccccccccccccccccc 2 2
	}
`
	result := parsePorcelainBlame(input)

	if len(result) != 2 {
		t.Fatalf("expected 2 blame lines, got %d", len(result))
	}

	for i := 1; i <= 2; i++ {
		bl, ok := result[i]
		if !ok {
			t.Fatalf("missing blame for line %d — continuation lines not cached", i)
		}
		if bl.Author != "Carol" {
			t.Errorf("line %d: expected 'Carol', got '%s'", i, bl.Author)
		}
	}
}

func TestParsePorcelainBlame_EmptyInput(t *testing.T) {
	result := parsePorcelainBlame("")
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}
}

func TestIsAllHex(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"abc123", true},
		{"ABCDEF", true},
		{"0000000000000000000000000000000000000000", true},
		{"xyz", false},
		{"abc12g", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isAllHex(tt.input); got != tt.want {
			t.Errorf("isAllHex(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestTruncateAuthor(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Alice", "Alice"},
		{"Short Name", "Short Name"},
		{"Exactly15Chars!", "Exactly15Chars!"},
		{"This Name Is Way Too Long", "This Name Is W\u2026"},
	}
	for _, tt := range tests {
		if got := truncateAuthor(tt.input); got != tt.want {
			t.Errorf("truncateAuthor(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

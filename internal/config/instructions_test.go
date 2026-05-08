package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupRepoWithInstructions creates a temp directory with a .git dir
// and optionally an instructions file. It changes the working directory
// to the repo root and returns a cleanup function.
func setupRepoWithInstructions(t *testing.T, instructionsPath, content string) string {
	t.Helper()
	root := t.TempDir()

	// Create fake .git directory so findRepoRoot works
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	if instructionsPath != "" {
		fullPath := filepath.Join(root, instructionsPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Change working directory to the repo root
	origDir, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	return root
}

func TestLoadCustomInstructions_PrrInstructionsFile(t *testing.T) {
	content := "Review carefully for security issues."
	setupRepoWithInstructions(t, ".prr/instructions.md", content)

	got := LoadCustomInstructions()
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestLoadCustomInstructions_GitHubFallback(t *testing.T) {
	content := "Focus on performance."
	setupRepoWithInstructions(t, ".github/prr-instructions.md", content)

	got := LoadCustomInstructions()
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestLoadCustomInstructions_PrrTakesPriority(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0755)

	// Create both files
	os.MkdirAll(filepath.Join(root, ".prr"), 0755)
	os.WriteFile(filepath.Join(root, ".prr/instructions.md"), []byte("prr wins"), 0644)
	os.MkdirAll(filepath.Join(root, ".github"), 0755)
	os.WriteFile(filepath.Join(root, ".github/prr-instructions.md"), []byte("github loses"), 0644)

	origDir, _ := os.Getwd()
	os.Chdir(root)
	defer os.Chdir(origDir)

	got := LoadCustomInstructions()
	if got != "prr wins" {
		t.Errorf("got %q, want %q", got, "prr wins")
	}
}

func TestLoadCustomInstructions_NoInstructionsFile(t *testing.T) {
	// Repo with .git but no instruction files
	setupRepoWithInstructions(t, "", "")

	got := LoadCustomInstructions()
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestLoadCustomInstructions_TruncatesAt4000Bytes(t *testing.T) {
	// Create content larger than 4000 bytes
	content := strings.Repeat("a", 5000)
	setupRepoWithInstructions(t, ".prr/instructions.md", content)

	got := LoadCustomInstructions()
	if len(got) > maxInstructionBytes {
		t.Errorf("content length = %d, want <= %d", len(got), maxInstructionBytes)
	}
	if len(got) != maxInstructionBytes {
		t.Errorf("content length = %d, want exactly %d for ASCII", len(got), maxInstructionBytes)
	}
}

func TestLoadCustomInstructions_TruncatesAtRuneBoundary(t *testing.T) {
	// Create content with multi-byte UTF-8 characters (emoji = 4 bytes each)
	// Place them so the 4000 byte cut would split a character
	prefix := strings.Repeat("a", 3998) // 3998 bytes
	content := prefix + "🎉🎉"            // 3998 + 8 = 4006 bytes

	setupRepoWithInstructions(t, ".prr/instructions.md", content)

	got := LoadCustomInstructions()

	// Must not exceed the limit
	if len(got) > maxInstructionBytes {
		t.Errorf("content length = %d, exceeds max %d", len(got), maxInstructionBytes)
	}

	// Slicing at byte 4000 lands 2 bytes into the first emoji (a 4-byte rune).
	// The implementation should trim back invalid trailing bytes, leaving us
	// with just the 3998-byte ASCII prefix + the first complete emoji (4 bytes) = won't fit,
	// so we expect exactly 3998 bytes (the last valid rune boundary before 4000).
	// Actually: 3998 + 4 = 4002 > 4000, so trimming from 4000 removes the 2
	// partial emoji bytes → 3998.
	if len(got) != 3998 {
		t.Errorf("expected truncation to 3998 bytes (last valid rune boundary), got %d", len(got))
	}

	// Result must be a valid prefix of the original content
	if !strings.HasPrefix(content, got) {
		t.Errorf("truncated content is not a prefix of original")
	}

	// The result should equal just the ASCII prefix (no partial emoji bytes)
	if got != prefix {
		t.Errorf("expected result to be the ASCII prefix only")
	}
}

func TestLoadCustomInstructions_SubdirectoryFindsRoot(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0755)
	os.MkdirAll(filepath.Join(root, ".prr"), 0755)
	os.WriteFile(filepath.Join(root, ".prr/instructions.md"), []byte("found from subdir"), 0644)

	// Create a subdirectory and chdir into it
	subdir := filepath.Join(root, "src", "pkg")
	os.MkdirAll(subdir, 0755)

	origDir, _ := os.Getwd()
	os.Chdir(subdir)
	defer os.Chdir(origDir)

	got := LoadCustomInstructions()
	if got != "found from subdir" {
		t.Errorf("got %q, want %q", got, "found from subdir")
	}
}

package git

import (
	"strings"
	"testing"
)

func TestShouldSkipForAI_BinaryDiffMarker(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		diff   string
		skip   bool
		reason SkipReason
	}{
		{
			name: "new binary file",
			path: "assets/logo.dat",
			diff: "diff --git a/assets/logo.dat b/assets/logo.dat\n" +
				"new file mode 100644\n" +
				"index 0000000..abc1234\n" +
				"Binary files /dev/null and b/assets/logo.dat differ\n",
			skip:   true,
			reason: SkipBinary,
		},
		{
			name: "modified binary file",
			path: "data/blob.bin",
			diff: "diff --git a/data/blob.bin b/data/blob.bin\n" +
				"index abc1234..def5678 100644\n" +
				"Binary files a/data/blob.bin and b/data/blob.bin differ\n",
			skip:   true,
			reason: SkipBinary,
		},
		{
			name: "git binary patch",
			path: "icon.dat",
			diff: "diff --git a/icon.dat b/icon.dat\n" +
				"index abc..def 100644\n" +
				"\nGIT binary patch\n" +
				"literal 1234\nabcdef\n",
			skip:   true,
			reason: SkipBinary,
		},
		{
			name: "normal text diff",
			path: "main.go",
			diff: "diff --git a/main.go b/main.go\n" +
				"--- a/main.go\n+++ b/main.go\n" +
				"@@ -1,3 +1,4 @@\n+package main\n",
			skip:   false,
			reason: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skip, reason := ShouldSkipForAI(tt.path, tt.diff)
			if skip != tt.skip {
				t.Errorf("skip = %v, want %v", skip, tt.skip)
			}
			if reason != tt.reason {
				t.Errorf("reason = %q, want %q", reason, tt.reason)
			}
		})
	}
}

func TestShouldSkipForAI_ExtensionBlocklist(t *testing.T) {
	extensions := []string{
		".png", ".jpg", ".jpeg", ".gif", ".ico", ".webp",
		".woff", ".woff2", ".ttf", ".otf", ".eot",
		".zip", ".tar", ".gz", ".exe", ".dll", ".so", ".wasm",
		".mp3", ".mp4", ".pdf", ".sqlite", ".db",
	}

	normalDiff := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n"

	for _, ext := range extensions {
		t.Run(ext, func(t *testing.T) {
			skip, reason := ShouldSkipForAI("file"+ext, normalDiff)
			if !skip {
				t.Errorf("expected skip for extension %s", ext)
			}
			if reason != SkipBinary {
				t.Errorf("reason = %q, want %q", reason, SkipBinary)
			}
		})
	}
}

func TestShouldSkipForAI_ExtensionCaseInsensitive(t *testing.T) {
	normalDiff := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n"

	skip, reason := ShouldSkipForAI("image.PNG", normalDiff)
	if !skip {
		t.Error("expected skip for .PNG (uppercase)")
	}
	if reason != SkipBinary {
		t.Errorf("reason = %q, want %q", reason, SkipBinary)
	}
}

func TestShouldSkipForAI_GeneratedFiles(t *testing.T) {
	lockFiles := []string{
		"package-lock.json",
		"yarn.lock",
		"pnpm-lock.yaml",
		"go.sum",
		"Cargo.lock",
		"Gemfile.lock",
		"composer.lock",
		"poetry.lock",
		"Pipfile.lock",
		"bun.lockb",
	}

	normalDiff := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n"

	for _, name := range lockFiles {
		t.Run(name, func(t *testing.T) {
			skip, reason := ShouldSkipForAI(name, normalDiff)
			if !skip {
				t.Errorf("expected skip for %s", name)
			}
			if reason != SkipGenerated {
				t.Errorf("reason = %q, want %q", reason, SkipGenerated)
			}
		})
	}
}

func TestShouldSkipForAI_GeneratedFilesInSubdir(t *testing.T) {
	normalDiff := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n"

	skip, reason := ShouldSkipForAI("frontend/package-lock.json", normalDiff)
	if !skip {
		t.Error("expected skip for package-lock.json in subdirectory")
	}
	if reason != SkipGenerated {
		t.Errorf("reason = %q, want %q", reason, SkipGenerated)
	}
}

func TestShouldSkipForAI_GeneratedSuffixes(t *testing.T) {
	suffixes := []struct {
		path string
	}{
		{"dist/app.min.js"},
		{"css/styles.min.css"},
		{"api/service.pb.go"},
		{"internal/model.generated.go"},
		{"pkg/types.gen.go"},
		{"src/api.gen.ts"},
		{"assets/vendor.bundle.js"},
	}

	normalDiff := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n"

	for _, tt := range suffixes {
		t.Run(tt.path, func(t *testing.T) {
			skip, reason := ShouldSkipForAI(tt.path, normalDiff)
			if !skip {
				t.Errorf("expected skip for %s", tt.path)
			}
			if reason != SkipGenerated {
				t.Errorf("reason = %q, want %q", reason, SkipGenerated)
			}
		})
	}
}

func TestShouldSkipForAI_LargeDiff(t *testing.T) {
	// Build a diff just over the threshold
	largeDiff := "diff --git a/big.go b/big.go\n" + strings.Repeat("+line\n", maxDiffSizeForAI/6+1)

	if len(largeDiff) <= maxDiffSizeForAI {
		t.Fatal("test setup: diff should exceed maxDiffSizeForAI")
	}

	skip, reason := ShouldSkipForAI("big.go", largeDiff)
	if !skip {
		t.Error("expected skip for large diff")
	}
	if reason != SkipLarge {
		t.Errorf("reason = %q, want %q", reason, SkipLarge)
	}
}

func TestShouldSkipForAI_NormalFile(t *testing.T) {
	normalFiles := []string{
		"main.go",
		"src/app.ts",
		"README.md",
		"Makefile",
		"docker-compose.yml",
		"internal/handler/auth.go",
		".gitignore",
		"assets/style.css",
		"logo.svg", // SVG is text, should not be skipped
	}

	normalDiff := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n"

	for _, path := range normalFiles {
		t.Run(path, func(t *testing.T) {
			skip, _ := ShouldSkipForAI(path, normalDiff)
			if skip {
				t.Errorf("did not expect skip for %s", path)
			}
		})
	}
}

func TestShouldSkipForAI_BelowSizeThreshold(t *testing.T) {
	// A diff just under the threshold should not be skipped
	diff := "diff --git a/f b/f\n" + strings.Repeat("+x\n", 1000)
	if len(diff) >= maxDiffSizeForAI {
		t.Fatal("test setup: diff should be under threshold")
	}

	skip, _ := ShouldSkipForAI("normal.go", diff)
	if skip {
		t.Error("did not expect skip for diff under size threshold")
	}
}

func TestShouldSkipForAI_PriorityOrder(t *testing.T) {
	// A .png file with a large diff should report SkipBinary (extension hit first),
	// not SkipLarge.
	largeDiff := strings.Repeat("x", maxDiffSizeForAI+1)

	_, reason := ShouldSkipForAI("photo.png", largeDiff)
	if reason != SkipBinary {
		t.Errorf("expected SkipBinary for .png regardless of size, got %q", reason)
	}
}

func TestIsBinaryDiff(t *testing.T) {
	tests := []struct {
		name   string
		diff   string
		binary bool
	}{
		{
			name:   "empty diff",
			diff:   "",
			binary: false,
		},
		{
			name:   "normal diff",
			diff:   "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n",
			binary: false,
		},
		{
			name:   "binary marker at start of line",
			diff:   "diff --git a/f b/f\nBinary files /dev/null and b/f differ\n",
			binary: true,
		},
		{
			name:   "binary marker mid-diff",
			diff:   "diff --git a/f b/f\nindex abc..def 100644\nBinary files a/f and b/f differ\n",
			binary: true,
		},
		{
			name:   "git binary patch format",
			diff:   "diff --git a/f b/f\n\nGIT binary patch\nliteral 100\nabc\n",
			binary: true,
		},
		{
			name:   "text containing word binary but not marker",
			diff:   "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-// binary search\n+// binary search v2\n",
			binary: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBinaryDiff(tt.diff); got != tt.binary {
				t.Errorf("isBinaryDiff() = %v, want %v", got, tt.binary)
			}
		})
	}
}

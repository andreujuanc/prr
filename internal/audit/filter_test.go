package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShouldExcludeFromAudit(t *testing.T) {
	tests := []struct {
		path    string
		exclude bool
	}{
		// Standard review exclusions (delegated to config.ShouldExcludeFromReview)
		{"go.sum", true},
		{"vendor/lib/foo.go", true},
		{"node_modules/react/index.js", true},
		{"foo.pb.go", true},

		// Audit-specific: test files
		{"internal/auth/handler_test.go", true},
		{"src/utils.test.ts", true},
		{"src/utils.spec.js", true},
		{"src/Component.test.tsx", true},
		{"tests/test_auth.py", true},

		// Audit-specific: test infrastructure
		{"testdata/fixtures/input.json", true},
		{"src/__tests__/utils.js", true},
		{"src/__mocks__/api.js", true},

		// Audit-specific: generated
		{"types.d.ts", true},

		// Audit-specific: docs
		{"README.md", true},
		{"CHANGELOG.md", true},
		{"docs/guide.txt", true},
		{"LICENSE", true},

		// Audit-specific: build artifacts
		{"dist/bundle.js", true},
		{"build/output.js", true},

		// Audit-specific: IDE
		{".vscode/settings.json", true},
		{".idea/workspace.xml", true},

		// Audit-specific: config
		{".gitignore", true},
		{".editorconfig", true},
		{"tsconfig.json", true},

		// SHOULD KEEP: source code
		{"internal/auth/handler.go", false},
		{"src/utils.ts", false},
		{"main.go", false},
		{"cmd/server/main.go", false},
		{"Dockerfile", false},
		{"nginx.conf", false},
		{"webpack.config.js", false},

		// SHOULD KEEP: config with logic
		{"docker-compose.yml", false},

		// SHOULD KEEP: CSS/HTML
		{"styles.css", false},
		{"index.html", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := ShouldExcludeFromAudit(tt.path)
			if got != tt.exclude {
				t.Errorf("ShouldExcludeFromAudit(%q) = %v, want %v", tt.path, got, tt.exclude)
			}
		})
	}
}

func TestShouldForceInclude(t *testing.T) {
	patterns := []string{"*.d.ts", "testdata/golden/*"}

	if !ShouldForceInclude("types.d.ts", patterns) {
		t.Error("should force include *.d.ts")
	}
	if ShouldForceInclude("main.go", patterns) {
		t.Error("should not force include main.go")
	}
	if ShouldForceInclude("main.go", nil) {
		t.Error("should not force include with nil patterns")
	}
}

func TestLoadExcludeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exclude")
	content := "# comment\n\n*.log\ntmp/**\n  \n# another comment\nsecrets/*\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	patterns, err := LoadExcludeFile(path)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"*.log", "tmp/**", "secrets/*"}
	if len(patterns) != len(want) {
		t.Fatalf("got %d patterns, want %d: %v", len(patterns), len(want), patterns)
	}
	for i, p := range patterns {
		if p != want[i] {
			t.Errorf("pattern %d: got %q, want %q", i, p, want[i])
		}
	}
}

func TestLoadExcludeFile_NonExistent(t *testing.T) {
	patterns, err := LoadExcludeFile("/nonexistent/path")
	if err != nil {
		t.Errorf("should return nil error for missing file, got %v", err)
	}
	if patterns != nil {
		t.Errorf("should return nil patterns for missing file, got %v", patterns)
	}
}

func TestCustomExclusions(t *testing.T) {
	custom := []string{"*.log", "internal/legacy/**"}

	if !ShouldExcludeFromAuditWithCustom("app.log", custom) {
		t.Error("should exclude *.log with custom pattern")
	}
	if !ShouldExcludeFromAuditWithCustom("internal/legacy/old.go", custom) {
		t.Error("should exclude internal/legacy/** with custom pattern")
	}
	if ShouldExcludeFromAuditWithCustom("internal/auth/handler.go", custom) {
		t.Error("should not exclude source code")
	}
}

func TestMatchesAuditPatterns_Comments(t *testing.T) {
	patterns := []string{"# this is a comment", "", "*.log"}
	if matchesAuditPatterns("foo.go", patterns) {
		t.Error("should not match non-log file")
	}
	if !matchesAuditPatterns("app.log", patterns) {
		t.Error("should match .log file")
	}
}

func TestMatchesAuditPatterns_DirectoryGlob(t *testing.T) {
	patterns := []string{"vendor/**"}
	if !matchesAuditPatterns("vendor/lib/foo.go", patterns) {
		t.Error("should match vendor subdirectory")
	}
	if matchesAuditPatterns("src/vendor.go", patterns) {
		t.Error("should not match file named vendor.go")
	}
}

func TestMatchesAuditPatterns_NestedDirectoryGlob(t *testing.T) {
	patterns := []string{"__tests__/**"}
	if !matchesAuditPatterns("src/__tests__/foo.test.js", patterns) {
		t.Error("should match nested __tests__ directory")
	}
	if matchesAuditPatterns("src/main.go", patterns) {
		t.Error("should not match unrelated path")
	}
}

func TestMatchesAuditPatterns_Empty(t *testing.T) {
	if matchesAuditPatterns("foo.go", nil) {
		t.Error("nil patterns should match nothing")
	}
	if matchesAuditPatterns("foo.go", []string{}) {
		t.Error("empty patterns should match nothing")
	}
}

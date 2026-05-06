package config

import "testing"

func TestShouldExcludeFromReview(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		exclude bool
	}{
		// Lock files — exact base name matches
		{"go.sum", "go.sum", true},
		{"package-lock.json", "package-lock.json", true},
		{"yarn.lock", "yarn.lock", true},
		{"pnpm-lock.yaml", "pnpm-lock.yaml", true},
		{"Gemfile.lock", "Gemfile.lock", true},
		{"Pipfile.lock", "Pipfile.lock", true},
		{"poetry.lock", "poetry.lock", true},
		{"composer.lock", "composer.lock", true},
		{"Cargo.lock", "Cargo.lock", true},

		// Lock files nested in directories — should still match by base name
		{"nested go.sum", "vendor/go.sum", true},
		{"nested package-lock", "frontend/package-lock.json", true},

		// Vendored dependencies
		{"vendor top-level", "vendor/foo/bar.go", true},
		{"vendor nested deep", "vendor/github.com/pkg/errors/errors.go", true},
		{"node_modules", "node_modules/lodash/index.js", true},

		// Generated code
		{"protobuf generated", "api/types.pb.go", true},
		{"generated.go suffix", "models/user.generated.go", true},
		{"gen.go suffix", "models/user.gen.go", true},
		{"_generated.go suffix", "models/user_generated.go", true},

		// Minified/bundled assets
		{"minified JS", "dist/app.min.js", true},
		{"minified CSS", "dist/style.min.css", true},
		{"bundled JS", "dist/app.bundle.js", true},

		// Binary/image files
		{"svg", "assets/logo.svg", true},
		{"png", "assets/icon.png", true},
		{"jpg", "photos/pic.jpg", true},
		{"jpeg", "photos/pic.jpeg", true},
		{"gif", "assets/anim.gif", true},
		{"ico", "public/favicon.ico", true},
		{"woff", "fonts/custom.woff", true},
		{"woff2", "fonts/custom.woff2", true},
		{"ttf", "fonts/custom.ttf", true},
		{"eot", "fonts/custom.eot", true},

		// Normal source files — should NOT be excluded
		{"go source", "main.go", false},
		{"go test", "main_test.go", false},
		{"typescript", "src/app.tsx", false},
		{"python", "script.py", false},
		{"markdown", "README.md", false},
		{"json config", "config.json", false},
		{"yaml", ".github/workflows/ci.yml", false},

		// Edge cases: names that partially resemble excluded patterns
		{"not a lock file", "my-lock.json", false},
		{"lockfile in name", "lockfile.go", false},
		{"vendor in non-path prefix", "not-vendor/foo.go", false},
		{"file named vendor.go", "vendor.go", false},
		{"gen in name but not suffix", "generator.go", false},
		{"min in name but not .min.js", "admin.js", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldExcludeFromReview(tt.path)
			if got != tt.exclude {
				t.Errorf("ShouldExcludeFromReview(%q) = %v, want %v", tt.path, got, tt.exclude)
			}
		})
	}
}

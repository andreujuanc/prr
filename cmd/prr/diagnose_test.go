package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDumpGoroutines_WritesAReadableFile pins the diagnostic surface
// the SIGUSR1 handler depends on: dumpGoroutines must create a file
// in the target dir and the file must contain at least one "goroutine"
// header (output of runtime.Stack always starts each entry that way).
func TestDumpGoroutines_WritesAReadableFile(t *testing.T) {
	dir := t.TempDir()
	if err := dumpGoroutines(dir); err != nil {
		t.Fatalf("dumpGoroutines: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read tempdir: %v", err)
	}
	var dumpPath string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "goroutines-") && strings.HasSuffix(e.Name(), ".txt") {
			dumpPath = filepath.Join(dir, e.Name())
			break
		}
	}
	if dumpPath == "" {
		t.Fatalf("no goroutines-*.txt file produced in %s", dir)
	}

	body, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if !strings.Contains(string(body), "goroutine ") {
		t.Errorf("dump should contain 'goroutine ' header; got first 200 bytes:\n%s",
			string(body[:min(len(body), 200)]))
	}
	// The test goroutine itself should appear in the dump.
	if !strings.Contains(string(body), "TestDumpGoroutines_WritesAReadableFile") {
		t.Errorf("dump should mention the current test function")
	}
}

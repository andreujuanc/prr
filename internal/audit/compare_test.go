package audit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

func f(file, category, subcat, title, severity string) state.DeepFinding {
	return state.DeepFinding{
		File:        file,
		Category:    category,
		Subcategory: subcat,
		Title:       title,
		Severity:    severity,
	}
}

func TestCompareFindings_NoPrevious(t *testing.T) {
	current := []state.DeepFinding{
		f("a.go", "bug", "", "nil deref", "high"),
		f("b.go", "security", "sql", "injection", "critical"),
	}
	result := CompareFindings(current, nil)
	if len(result.New) != 2 {
		t.Fatalf("expected 2 new, got %d", len(result.New))
	}
	if len(result.Resolved) != 0 {
		t.Fatalf("expected 0 resolved, got %d", len(result.Resolved))
	}
	if len(result.Persistent) != 0 {
		t.Fatalf("expected 0 persistent, got %d", len(result.Persistent))
	}
}

func TestCompareFindings_AllResolved(t *testing.T) {
	previous := []state.DeepFinding{
		f("a.go", "bug", "", "nil deref", "high"),
		f("b.go", "security", "sql", "injection", "critical"),
	}
	result := CompareFindings(nil, previous)
	if len(result.New) != 0 {
		t.Fatalf("expected 0 new, got %d", len(result.New))
	}
	if len(result.Resolved) != 2 {
		t.Fatalf("expected 2 resolved, got %d", len(result.Resolved))
	}
	if len(result.Persistent) != 0 {
		t.Fatalf("expected 0 persistent, got %d", len(result.Persistent))
	}
}

func TestCompareFindings_Mixed(t *testing.T) {
	previous := []state.DeepFinding{
		f("a.go", "bug", "", "nil deref", "high"),
		f("b.go", "security", "sql", "injection", "critical"),
		f("c.go", "perf", "", "slow loop", "medium"),
	}
	current := []state.DeepFinding{
		f("a.go", "bug", "", "nil deref", "high"),       // persistent
		f("d.go", "style", "", "naming", "low"),          // new
		f("b.go", "security", "sql", "injection", "high"), // persistent (severity changed)
	}
	result := CompareFindings(current, previous)
	if len(result.New) != 1 {
		t.Fatalf("expected 1 new, got %d", len(result.New))
	}
	if result.New[0].File != "d.go" {
		t.Fatalf("expected new finding in d.go, got %s", result.New[0].File)
	}
	if len(result.Resolved) != 1 {
		t.Fatalf("expected 1 resolved, got %d", len(result.Resolved))
	}
	if result.Resolved[0].File != "c.go" {
		t.Fatalf("expected resolved finding in c.go, got %s", result.Resolved[0].File)
	}
	if len(result.Persistent) != 2 {
		t.Fatalf("expected 2 persistent, got %d", len(result.Persistent))
	}
}

func TestCompareFindings_SeverityChange(t *testing.T) {
	previous := []state.DeepFinding{
		f("a.go", "bug", "", "nil deref", "medium"),
	}
	current := []state.DeepFinding{
		f("a.go", "bug", "", "nil deref", "critical"),
	}
	result := CompareFindings(current, previous)
	if len(result.Persistent) != 1 {
		t.Fatalf("expected 1 persistent, got %d", len(result.Persistent))
	}
	if result.Persistent[0].Severity != "critical" {
		t.Fatalf("expected current severity 'critical', got %s", result.Persistent[0].Severity)
	}
}

func TestFormatComparison(t *testing.T) {
	cr := CompareResult{
		New:        make([]state.DeepFinding, 3),
		Resolved:   make([]state.DeepFinding, 2),
		Persistent: make([]state.DeepFinding, 10),
	}
	want := "3 new findings, 2 resolved, 10 persistent"
	if got := cr.FormatComparison(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSnapshot_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	// Create .git/pr-tui structure
	gitDir := filepath.Join(tmp, ".git", "pr-tui")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	findings := []state.DeepFinding{
		f("a.go", "bug", "", "nil deref", "high"),
	}
	if err := SaveSnapshot(tmp, findings); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := LoadSnapshot(tmp)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Title != "nil deref" {
		t.Fatalf("unexpected loaded: %+v", loaded)
	}
}

func TestLoadSnapshot_NoFile(t *testing.T) {
	tmp := t.TempDir()
	loaded, err := LoadSnapshot(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded != nil {
		t.Fatalf("expected nil, got %+v", loaded)
	}
}

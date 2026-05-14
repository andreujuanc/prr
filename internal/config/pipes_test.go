package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andreujuanc/prr/internal/pipe"
)

func TestLoadPipeTargets_ValidConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".config", "prr")
	os.MkdirAll(dir, 0755)

	cfg := map[string]interface{}{
		"provider": "gemini",
		"api_key":  "key",
		"pipes": []pipe.Target{
			{Name: "jira", Command: "jira-pipe", Args: []string{"--project", "FOO"}, Format: "json"},
			{Name: "slack", Command: "slack-notify", Format: "markdown"},
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(filepath.Join(dir, "config.json"), data, 0600)

	targets := LoadPipeTargets()
	if len(targets) != 2 {
		t.Fatalf("expected 2 pipe targets, got %d", len(targets))
	}
	if targets[0].Name != "jira" {
		t.Errorf("first target name = %q, want %q", targets[0].Name, "jira")
	}
	if targets[0].Command != "jira-pipe" {
		t.Errorf("first target command = %q, want %q", targets[0].Command, "jira-pipe")
	}
	if len(targets[0].Args) != 2 || targets[0].Args[0] != "--project" {
		t.Errorf("first target args = %v, want [--project FOO]", targets[0].Args)
	}
	if targets[1].Name != "slack" {
		t.Errorf("second target name = %q, want %q", targets[1].Name, "slack")
	}
}

func TestLoadPipeTargets_NoPipes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".config", "prr")
	os.MkdirAll(dir, 0755)

	cfg := map[string]interface{}{
		"provider": "gemini",
		"api_key":  "key",
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(filepath.Join(dir, "config.json"), data, 0600)

	targets := LoadPipeTargets()
	if targets != nil {
		t.Errorf("expected nil, got %v", targets)
	}
}

func TestLoadPipeTargets_MissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	targets := LoadPipeTargets()
	if targets != nil {
		t.Errorf("expected nil for missing file, got %v", targets)
	}
}

func TestLoadPipeTargets_InvalidJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".config", "prr")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("{bad"), 0600)

	targets := LoadPipeTargets()
	if targets != nil {
		t.Errorf("expected nil for invalid JSON, got %v", targets)
	}
}

package ai

import (
	"context"
	"testing"
)

// mockUsageClient implements Client and UsageReporter for testing.
type mockUsageClient struct {
	usage TokenUsage
}

func (m *mockUsageClient) ChatStream(_ context.Context, _ string, _ []Message, _ func(string)) (string, error) {
	return "", nil
}

func (m *mockUsageClient) Usage() TokenUsage { return m.usage }
func (m *mockUsageClient) ResetUsage()       { m.usage = TokenUsage{} }

// mockPlainClient implements Client but NOT UsageReporter.
type mockPlainClient struct{}

func (m *mockPlainClient) ChatStream(_ context.Context, _ string, _ []Message, _ func(string)) (string, error) {
	return "", nil
}

func TestSnapshotUsage(t *testing.T) {
	t.Run("with UsageReporter", func(t *testing.T) {
		c := &mockUsageClient{usage: TokenUsage{InputTokens: 100, OutputTokens: 50, CacheHits: 5}}
		u := SnapshotUsage(c)
		if u.InputTokens != 100 || u.OutputTokens != 50 || u.CacheHits != 5 {
			t.Errorf("expected {100, 50, 5}, got %+v", u)
		}
		// Should be reset after snapshot
		u2 := SnapshotUsage(c)
		if u2.InputTokens != 0 || u2.OutputTokens != 0 {
			t.Errorf("expected zeroed usage after reset, got %+v", u2)
		}
	})

	t.Run("without UsageReporter", func(t *testing.T) {
		c := &mockPlainClient{}
		u := SnapshotUsage(c)
		if u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheHits != 0 {
			t.Errorf("expected zero usage for non-reporter client, got %+v", u)
		}
	})
}

func TestStripMarkdownFences(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no fences", `{"key": "value"}`, `{"key": "value"}`},
		{"json fence", "```json\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"plain fence", "```\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"with whitespace", "  ```json\n{\"key\": \"value\"}\n```  ", `{"key": "value"}`},
		{"nested backticks", "```json\n{\"code\": \"```\"}\n```", `{"code": "` + "```" + `"}`},
		{"empty content", "```json\n\n```", ""},
		{"no newline after opening", "```json```", "```json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripMarkdownFences(tt.input)
			if got != tt.want {
				t.Errorf("StripMarkdownFences(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

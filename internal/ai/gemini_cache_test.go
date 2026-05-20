package ai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLive_GeminiCache_CreateUseDelete is the verification test for the
// context-caching plumbing in gemini.go. It hits the real Gemini API.
//
// Run with:
//
//	PRR_LIVE_TESTS=1 go test ./internal/ai/ -run TestLive_GeminiCache -v
//
// The test:
//  1. Creates a context cache containing a large system instruction + the
//     canonical tool definitions. The system instruction is padded above
//     the documented per-model minimum (1024 tokens for Flash).
//  2. Sends a generateContent request that references the cache handle,
//     omitting the cached fields from the request body.
//  3. Asserts that `cachedContentTokenCount` in the response's
//     usageMetadata is non-zero — proves the cache was actually read.
//  4. Deletes the cache.
//
// Any failure here means the plumbing doesn't match Gemini's API. Don't
// build higher-level features on top of caching until this test passes.
func TestLive_GeminiCache_CreateUseDelete(t *testing.T) {
	key := skipWithoutAPIKey(t)

	model := os.Getenv("PRR_MODEL")
	if model == "" {
		// Flash-lite has the lowest minimum-cache-size (1024 tokens)
		// per the docs. Picking it keeps the padding small.
		model = "gemini-3.1-flash-lite"
	}

	provider := &GeminiProvider{APIKey: key, Model: model}

	// Build a system instruction comfortably above Flash's 1024-token
	// minimum. Roughly 4 chars per token, so ~6000 chars => ~1500
	// tokens. Repeat the project-context-style boilerplate to fill it.
	var sb strings.Builder
	sb.WriteString("You are a senior code reviewer for a Go repository named prr. ")
	sb.WriteString("prr is a terminal-based AI-powered PR reviewer built with Bubble Tea. ")
	sb.WriteString("When asked, you must answer accurately and concisely. ")
	for i := 0; i < 40; i++ {
		sb.WriteString("The project conventions favor short error messages, no swallowed errors, ")
		sb.WriteString("language-agnostic prompts that do not assume a specific language, and ")
		sb.WriteString("tight functions with no unused parameters or premature abstractions. ")
	}
	systemPrompt := sb.String()
	t.Logf("system instruction: %d bytes", len(systemPrompt))

	// Use the canonical tool definitions so we exercise the same
	// translation path as the production code.
	tools := CanonicalToolDefs()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	handle, err := provider.CreateContextCache(ctx, systemPrompt, tools, 5*time.Minute)
	if err != nil {
		t.Fatalf("CreateContextCache: %v", err)
	}
	if !strings.HasPrefix(handle, "cachedContents/") {
		t.Errorf("handle %q does not start with cachedContents/ — Gemini docs say it should", handle)
	}
	t.Logf("cache created: %s", handle)

	// Best-effort cleanup even if the assertions below fail.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := provider.DeleteContextCache(cleanupCtx, handle); err != nil {
			t.Logf("DeleteContextCache cleanup error (non-fatal): %v", err)
		}
	})

	// Now exercise the cache: send a tiny user message and assert the
	// response usage shows the cached prefix was actually read.
	req := ChatRequest{
		Messages: []ProviderMessage{
			{
				Role:    RoleUser,
				Content: []ContentBlock{TextBlock{Text: "In one word, name a programming language."}},
			},
		},
		CachedContent: handle,
		// Intentionally omit System and Tools — they're in the cache.
	}

	resp, err := provider.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat with cached content: %v", err)
	}
	if resp.Usage.CacheHits == 0 {
		t.Errorf("expected non-zero CacheHits in usage; got %+v — cache handle may not have been resolved by the server", resp.Usage)
	} else {
		t.Logf("cache hit confirmed: %d cached input tokens (of %d total input tokens)", resp.Usage.CacheHits, resp.Usage.InputTokens)
	}

	if len(resp.Content) == 0 {
		t.Error("empty response content")
	}
	for _, b := range resp.Content {
		if tb, ok := b.(TextBlock); ok && tb.Text != "" {
			t.Logf("response text: %q", strings.TrimSpace(tb.Text))
			break
		}
	}
}

// TestLive_GeminiCache_ToolsOnly checks whether caching the canonical tool
// definitions WITHOUT a system instruction reaches Gemini Flash's 1024-token
// minimum cache size. The deep-review pipeline currently caches tools only
// (system-prompt caching needs a separate prompt-builder refactor); if this
// test fails with INVALID_ARGUMENT, the MVP wiring degrades to uncached.
//
// Run with:
//
//	PRR_LIVE_TESTS=1 go test ./internal/ai/ -run TestLive_GeminiCache_ToolsOnly -v
func TestLive_GeminiCache_ToolsOnly(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: "gemini-3.1-flash-lite"}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := CanonicalToolDefs()
	handle, err := provider.CreateContextCache(ctx, "", tools, 5*time.Minute)
	if err != nil {
		// This is the expected failure mode if tool defs alone fall
		// below the minimum — log and skip the rest of the assertions
		// so we can see the diagnostic without failing CI.
		t.Logf("tools-only cache rejected (expected if tools < 1024 tokens): %v", err)
		t.Skip("cache create failed for tools-only payload")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = provider.DeleteContextCache(cleanupCtx, handle)
	})

	t.Logf("tools-only cache created: %s", handle)

	// Use it
	req := ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "Reply with the single word OK."}}},
		},
		CachedContent: handle,
	}
	resp, err := provider.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat with tools-only cache: %v", err)
	}
	t.Logf("tools-only cache hit: %d cached / %d total input tokens", resp.Usage.CacheHits, resp.Usage.InputTokens)
	if resp.Usage.CacheHits == 0 {
		t.Errorf("expected CacheHits > 0; got 0 — cache may not be resolving")
	}
}

// TestLive_GeminiCache_DeleteUnknown verifies that deleting a non-existent
// cache surfaces an HTTP error (not a panic, not a silent success).
//
// Run with:
//
//	PRR_LIVE_TESTS=1 go test ./internal/ai/ -run TestLive_GeminiCache_DeleteUnknown -v
func TestLive_GeminiCache_DeleteUnknown(t *testing.T) {
	key := skipWithoutAPIKey(t)

	provider := &GeminiProvider{APIKey: key, Model: "gemini-3.1-flash-lite"}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := provider.DeleteContextCache(ctx, "cachedContents/does-not-exist-xyz123")
	if err == nil {
		t.Error("expected error deleting a non-existent cache; got nil")
	} else {
		t.Logf("expected error: %v", err)
	}
}

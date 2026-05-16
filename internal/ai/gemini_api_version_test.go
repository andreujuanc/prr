package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestGeminiAPIVersion_DefaultsToV1Beta pins that an unset APIVersion
// uses v1beta in the request URL — the historical default.
func TestGeminiAPIVersion_DefaultsToV1Beta(t *testing.T) {
	g := &GeminiProvider{Model: "test-model"}
	got := g.resolveURL()
	if !strings.Contains(got, "/v1beta/") {
		t.Errorf("resolveURL = %q, expected /v1beta/ when APIVersion is unset", got)
	}
}

// TestGeminiAPIVersion_HonorsV1 pins that setting APIVersion=v1 lands
// in the URL, so callers can flip to the stable endpoint without
// editing the SDK.
func TestGeminiAPIVersion_HonorsV1(t *testing.T) {
	g := &GeminiProvider{Model: "test-model", APIVersion: "v1"}
	got := g.resolveURL()
	if !strings.Contains(got, "/v1/") {
		t.Errorf("resolveURL = %q, expected /v1/ when APIVersion=v1", got)
	}
	if strings.Contains(got, "/v1beta/") {
		t.Errorf("resolveURL = %q, must not contain /v1beta/ when APIVersion=v1", got)
	}
}

// TestGeminiAPIVersion_BaseURLWins pins that an explicit BaseURL
// override takes precedence over APIVersion. Used by tests and by
// callers pointing at a proxy or staging endpoint. Exercises the full
// HTTP path against a httptest.Server so a regression in
// doHTTPRequest's URL handling would be caught here.
func TestGeminiAPIVersion_BaseURLWins(t *testing.T) {
	var capturedPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	provider := &GeminiProvider{
		APIKey:     "test-key",
		Model:      "test-model",
		BaseURL:    srv.URL,
		APIVersion: "v999", // would be invalid; must be ignored when BaseURL is set
	}
	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range ch {
	}

	path, _ := capturedPath.Load().(string)
	if strings.Contains(path, "v999") {
		t.Errorf("path = %q, APIVersion must be ignored when BaseURL is set", path)
	}
	if !strings.Contains(path, "/models/test-model:streamGenerateContent") {
		t.Errorf("path = %q, expected /models/<model>:streamGenerateContent", path)
	}
}

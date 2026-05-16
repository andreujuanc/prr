package ai

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestGeminiTranslation_BlobBlockToInlineData pins that a BlobBlock in
// a user message becomes a parts[].inlineData entry with the right
// MIME type and base64-encoded data in the outbound request.
func TestGeminiTranslation_BlobBlockToInlineData(t *testing.T) {
	provider := &GeminiProvider{APIKey: "k", Model: "m"}
	raw := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'} // PNG magic
	native := provider.toNativeRequest(ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{
				TextBlock{Text: "describe this"},
				BlobBlock{Data: raw, MimeType: "image/png"},
			}},
		},
	})

	body, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	bs := string(body)
	if !strings.Contains(bs, `"inlineData"`) {
		t.Errorf("body missing inlineData: %s", bs)
	}
	if !strings.Contains(bs, `"mimeType":"image/png"`) {
		t.Errorf("body missing mimeType: %s", bs)
	}
	wantData := base64.StdEncoding.EncodeToString(raw)
	if !strings.Contains(bs, wantData) {
		t.Errorf("body missing base64 data %q: %s", wantData, bs)
	}
}

// TestOpenAITranslation_BlobBlockToImageURL pins the OpenAI side:
// BlobBlock becomes a multi-part content array with an image_url
// using a base64 data URI. Plain-text-only requests still serialise
// as a string (back-compat).
func TestOpenAITranslation_BlobBlockToImageURL(t *testing.T) {
	o := &OpenAIProvider{APIKey: "k", Model: "m"}
	raw := []byte("hello")
	native := o.toNativeRequest(ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{
				TextBlock{Text: "what is this"},
				BlobBlock{Data: raw, MimeType: "image/jpeg"},
			}},
		},
	})

	body, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	bs := string(body)
	if !strings.Contains(bs, `"image_url"`) {
		t.Errorf("body missing image_url: %s", bs)
	}
	wantPrefix := "data:image/jpeg;base64,"
	if !strings.Contains(bs, wantPrefix) {
		t.Errorf("body missing %q: %s", wantPrefix, bs)
	}
	wantData := base64.StdEncoding.EncodeToString(raw)
	if !strings.Contains(bs, wantData) {
		t.Errorf("body missing base64 data %q: %s", wantData, bs)
	}
}

// TestOpenAITranslation_TextOnlyKeepsStringContent pins that the
// back-compat path is preserved: a text-only user message still
// produces `"content": "..."` (a string), not a one-element array.
// Some OpenAI-compatible servers (older Copilot endpoints, in
// particular) are picky about this.
func TestOpenAITranslation_TextOnlyKeepsStringContent(t *testing.T) {
	o := &OpenAIProvider{APIKey: "k", Model: "m"}
	native := o.toNativeRequest(ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	body, _ := json.Marshal(native)
	bs := string(body)
	if !strings.Contains(bs, `"content":"hi"`) {
		t.Errorf("expected string content, got: %s", bs)
	}
}

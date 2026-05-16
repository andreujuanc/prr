package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGeminiConvertToolParam_ArrayOfObjects pins that a tool parameter
// shaped like array<object> round-trips through the Gemini schema
// converter with its nested Properties and Required intact. The
// earlier converter dropped both, silently sending an array of
// untyped objects to the API.
func TestGeminiConvertToolParam_ArrayOfObjects(t *testing.T) {
	p := ToolParam{
		Type:        "array",
		Description: "list of edits",
		Items: &ToolParam{
			Type: "object",
			Properties: map[string]ToolParam{
				"path": {Type: "string", Description: "file path"},
				"line": {Type: "integer"},
			},
			Required: []string{"path"},
		},
	}
	got := convertToolParam(p)

	if got.Type != "ARRAY" {
		t.Errorf("Type = %q, want ARRAY", got.Type)
	}
	if got.Items == nil {
		t.Fatal("Items must not be nil")
	}
	if got.Items.Type != "OBJECT" {
		t.Errorf("Items.Type = %q, want OBJECT", got.Items.Type)
	}
	if len(got.Items.Properties) != 2 {
		t.Fatalf("Items.Properties = %d entries, want 2", len(got.Items.Properties))
	}
	if got.Items.Properties["path"].Type != "STRING" {
		t.Errorf("Items.Properties[path].Type = %q, want STRING", got.Items.Properties["path"].Type)
	}
	if len(got.Items.Required) != 1 || got.Items.Required[0] != "path" {
		t.Errorf("Items.Required = %v, want [path]", got.Items.Required)
	}
}

// TestOpenAIToolParamsToJSON_ArrayOfObjects pins the same shape on the
// OpenAI side: the JSON body must contain a properly typed nested
// objects schema, not a stub `{"type":"array","items":{"type":"object"}}`.
func TestOpenAIToolParamsToJSON_ArrayOfObjects(t *testing.T) {
	o := &OpenAIProvider{}
	params := ToolParams{
		Type: "object",
		Properties: map[string]ToolParam{
			"edits": {
				Type: "array",
				Items: &ToolParam{
					Type: "object",
					Properties: map[string]ToolParam{
						"path": {Type: "string"},
						"line": {Type: "integer"},
					},
					Required: []string{"path"},
				},
			},
		},
		Required: []string{"edits"},
	}
	raw := o.toolParamsToJSON(params)

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, raw)
	}
	props, _ := got["properties"].(map[string]any)
	edits, _ := props["edits"].(map[string]any)
	items, _ := edits["items"].(map[string]any)
	if items["type"] != "object" {
		t.Errorf("items.type = %v, want object", items["type"])
	}
	itemProps, ok := items["properties"].(map[string]any)
	if !ok || len(itemProps) != 2 {
		t.Errorf("items.properties missing or wrong size: %v", items["properties"])
	}
	req, ok := items["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "path" {
		t.Errorf("items.required = %v, want [path]", items["required"])
	}
}

// TestValidateToolDef_EnumOnNonStringFails pins that Enum on a
// non-string parameter is caught. The Gemini and OpenAI schemas can
// represent only string-valued enums via ToolParam.Enum; attaching
// one to a number field silently drops the constraint.
func TestValidateToolDef_EnumOnNonStringFails(t *testing.T) {
	td := ToolDef{
		Name: "bad",
		Parameters: ToolParams{
			Type: "object",
			Properties: map[string]ToolParam{
				"count": {Type: "integer", Enum: []string{"1", "2"}},
			},
		},
	}
	err := ValidateToolDef(td)
	if err == nil {
		t.Fatal("expected error for enum on integer param")
	}
	if !strings.Contains(err.Error(), "Enum") || !strings.Contains(err.Error(), "string") {
		t.Errorf("error should mention Enum and string requirement, got: %v", err)
	}
}

// TestValidateToolDef_EnumOnStringPasses guards the happy path so the
// validator doesn't false-positive on the common case.
func TestValidateToolDef_EnumOnStringPasses(t *testing.T) {
	td := ToolDef{
		Name: "ok",
		Parameters: ToolParams{
			Type: "object",
			Properties: map[string]ToolParam{
				"mode": {Type: "string", Enum: []string{"read", "write"}},
			},
		},
	}
	if err := ValidateToolDef(td); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestValidateToolDef_CanonicalDefsAreClean asserts every tool emitted
// by the canonical prr tool registry is structurally valid today —
// guards against drift if a new tool def adds a bogus enum.
func TestValidateToolDef_CanonicalDefsAreClean(t *testing.T) {
	for _, td := range CanonicalToolDefs() {
		if err := ValidateToolDef(td); err != nil {
			t.Errorf("CanonicalToolDefs[%s]: %v", td.Name, err)
		}
	}
}

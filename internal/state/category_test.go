package state

import (
	"encoding/json"
	"strings"
	"testing"
)

func withValidator(t *testing.T, valid ...string) {
	t.Helper()
	set := make(map[string]bool, len(valid))
	for _, s := range valid {
		set[s] = true
	}
	prev := categoryValidator
	SetCategoryValidator(func(s string) bool { return set[s] })
	t.Cleanup(func() { SetCategoryValidator(prev) })
}

func TestParseCategory_KnownSlug(t *testing.T) {
	withValidator(t, "readability")
	c, err := ParseCategory("readability")
	if err != nil {
		t.Fatalf("ParseCategory readability: %v", err)
	}
	if c != "readability" {
		t.Fatalf("got %q", c)
	}
}

func TestParseCategory_Empty(t *testing.T) {
	withValidator(t, "readability")
	c, err := ParseCategory("")
	if err != nil {
		t.Fatalf("empty should be legal: %v", err)
	}
	if !c.IsZero() {
		t.Fatalf("expected zero, got %q", c)
	}
}

func TestParseCategory_TrimAndLowercase(t *testing.T) {
	withValidator(t, "readability")
	c, err := ParseCategory("  READABILITY  ")
	if err != nil {
		t.Fatalf("expected normalization to succeed: %v", err)
	}
	if c != "readability" {
		t.Fatalf("got %q", c)
	}
}

func TestParseCategory_Unknown(t *testing.T) {
	withValidator(t, "readability")
	_, err := ParseCategory("not-a-real-category")
	if err == nil {
		t.Fatal("expected error on unknown slug")
	}
	if !strings.Contains(err.Error(), "not-a-real-category") {
		t.Fatalf("expected error to mention the bad slug, got: %v", err)
	}
}

func TestParseCategory_NoValidator_Permissive(t *testing.T) {
	prev := categoryValidator
	SetCategoryValidator(nil)
	t.Cleanup(func() { SetCategoryValidator(prev) })

	c, err := ParseCategory("anything-goes")
	if err != nil {
		t.Fatalf("expected permissive accept when validator nil: %v", err)
	}
	if c != "anything-goes" {
		t.Fatalf("got %q", c)
	}
}

func TestCategory_UnmarshalJSON_Valid(t *testing.T) {
	withValidator(t, "readability")
	var c Category
	if err := json.Unmarshal([]byte(`"readability"`), &c); err != nil {
		t.Fatalf("unmarshal valid: %v", err)
	}
	if c != "readability" {
		t.Fatalf("got %q", c)
	}
}

func TestCategory_UnmarshalJSON_Invalid(t *testing.T) {
	withValidator(t, "readability")
	var c Category
	if err := json.Unmarshal([]byte(`"shitposting"`), &c); err == nil {
		t.Fatal("expected error on unknown slug")
	}
}

func TestCategory_UnmarshalJSON_EmptyAllowed(t *testing.T) {
	withValidator(t, "readability")
	var c Category
	if err := json.Unmarshal([]byte(`""`), &c); err != nil {
		t.Fatalf("empty should unmarshal: %v", err)
	}
	if !c.IsZero() {
		t.Fatalf("expected zero, got %q", c)
	}
}

func TestCategory_MarshalJSON(t *testing.T) {
	withValidator(t, "readability")
	c, _ := ParseCategory("readability")
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `"readability"` {
		t.Fatalf("got %s", b)
	}
}

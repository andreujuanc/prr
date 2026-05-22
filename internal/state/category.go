package state

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Category is a category slug validated at the JSON boundary against
// the set registered by SetCategoryValidator. The empty Category is
// legal — it represents "not yet assigned" in pipeline plumbing.
type Category string

// categoryValidator returns true when the slug is known. A nil
// validator accepts every non-empty slug; that mode exists for unit
// tests of packages that import state without importing ai.
var categoryValidator func(string) bool

// SetCategoryValidator registers the slug-validity check. Package ai's
// init wires its CategoryExists in. Must not be called concurrently
// with parsing.
func SetCategoryValidator(f func(string) bool) {
	categoryValidator = f
}

// ParseCategory promotes a raw string to a Category, lowercasing and
// trimming first. Empty input returns the zero Category and no error.
// Non-empty slugs rejected by the registered validator return an
// error.
func ParseCategory(s string) (Category, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return Category(""), nil
	}
	if categoryValidator != nil && !categoryValidator(s) {
		return Category(""), fmt.Errorf("%q is not a known category", s)
	}
	return Category(s), nil
}

// MustParseCategory panics on invalid input. Use for compile-time
// constants and tests only.
func MustParseCategory(s string) Category {
	c, err := ParseCategory(s)
	if err != nil {
		panic(err)
	}
	return c
}

func (c Category) String() string { return string(c) }

func (c Category) IsZero() bool { return c == "" }

func (c Category) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(c))
}

// UnmarshalJSON is the validation boundary: an unknown slug surfaces
// as a parse error on the enclosing struct.
func (c *Category) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := ParseCategory(s)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

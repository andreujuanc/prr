package review

import (
	"strings"
	"testing"
)

func TestParseReviewMode_Defaults(t *testing.T) {
	got, err := ParseReviewMode("")
	if err != nil {
		t.Fatalf("empty string should not error, got %v", err)
	}
	if got != defaultReviewMode {
		t.Errorf("empty string should yield default (%s), got %s", defaultReviewMode, got)
	}
}

func TestParseReviewMode_Full(t *testing.T) {
	got, err := ParseReviewMode("full")
	if err != nil {
		t.Fatalf("full should be valid, got %v", err)
	}
	if got != ReviewModeFull {
		t.Errorf("expected ReviewModeFull, got %s", got)
	}
}

func TestParseReviewMode_AOIOnly(t *testing.T) {
	got, err := ParseReviewMode("aoi-only")
	if err != nil {
		t.Fatalf("aoi-only should be valid, got %v", err)
	}
	if got != ReviewModeAOIOnly {
		t.Errorf("expected ReviewModeAOIOnly, got %s", got)
	}
}

func TestParseReviewMode_Unknown(t *testing.T) {
	_, err := ParseReviewMode("strict")
	if err == nil {
		t.Fatal("unknown mode should error")
	}
	// The error message should list valid modes so the user knows
	// what to try.
	msg := err.Error()
	if !strings.Contains(msg, "full") || !strings.Contains(msg, "aoi-only") {
		t.Errorf("error should list valid modes, got: %s", msg)
	}
}

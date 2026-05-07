package review

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/security"
)

func TestSeverityRank(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"critical", 0},
		{"high", 1},
		{"medium", 2},
		{"low", 3},
		{"unknown", 4},
		{"", 4},
	}
	for _, tt := range tests {
		if got := severityRank(tt.input); got != tt.want {
			t.Errorf("severityRank(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestUserMessage_PR(t *testing.T) {
	msg := userMessage(ModePR)
	if !strings.Contains(msg, "PR") {
		t.Error("expected PR-specific message")
	}
}

func TestUserMessage_Audit(t *testing.T) {
	msg := userMessage(ModeAudit)
	if strings.Contains(msg, "PR") {
		t.Error("audit message should not mention PR")
	}
	if !strings.Contains(msg, "tools") {
		t.Error("expected audit message to mention tools")
	}
}

func TestSerializeAOI(t *testing.T) {
	aoi := security.AreaOfInterest{
		File:        "handler.go",
		Line:        10,
		EndLine:     20,
		Category:    "security",
		Subcategory: "sql-injection",
		Concern:     "user input in query",
		Context:     "HTTP handler",
	}
	got := serializeAOI(aoi)
	want := "handler.go:10-20:security/sql-injection:user input in query:HTTP handler"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSerializeFocus_Empty(t *testing.T) {
	if got := serializeFocus(nil); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestSerializeFocus_Sorted(t *testing.T) {
	got := serializeFocus([]string{"security", "correctness", "api"})
	want := "api,correctness,security"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSerializeFocus_DoesNotMutateInput(t *testing.T) {
	input := []string{"z", "a", "m"}
	serializeFocus(input)
	if input[0] != "z" || input[1] != "a" || input[2] != "m" {
		t.Error("serializeFocus should not mutate the input slice")
	}
}

func TestFormatSummary_NoAOIs(t *testing.T) {
	r := &RouteResult{TotalAOIs: 0}
	got := r.FormatSummary()
	if got != "No areas of interest found." {
		t.Errorf("got %q", got)
	}
}

func TestFormatSummary_WithAOIs(t *testing.T) {
	r := &RouteResult{
		TotalAOIs:        10,
		IndividualCount:  3,
		SubcategoryCount: 4,
		Individual: []ReviewCall{
			{Type: "individual"},
			{Type: "individual"},
			{Type: "individual"},
		},
		Grouped: []ReviewCall{
			{Type: "grouped"},
			{Type: "grouped"},
		},
	}
	got := r.FormatSummary()
	if !strings.Contains(got, "10 AOIs") {
		t.Error("expected total AOIs")
	}
	if !strings.Contains(got, "3 individual") {
		t.Error("expected individual count")
	}
	if !strings.Contains(got, "2 grouped") {
		t.Error("expected grouped count")
	}
	if !strings.Contains(got, "5 total call") {
		t.Error("expected total calls (3+2=5)")
	}
}

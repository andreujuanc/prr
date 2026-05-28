package review

import (
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

func TestParseMinSeverity(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"high", "high", false},
		{"HIGH", "high", false},
		{"  critical  ", "critical", false},
		{"nit", "nit", false},
		{"bogus", "", true},
		{"warning", "", true},
	}
	for _, c := range cases {
		got, err := ParseMinSeverity(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseMinSeverity(%q): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMinSeverity(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseMinSeverity(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFilterByMinSeverity(t *testing.T) {
	findings := []state.DeepFinding{
		{FindingID: "F-1", Severity: "critical"},
		{FindingID: "F-2", Severity: "high"},
		{FindingID: "F-3", Severity: "medium"},
		{FindingID: "F-4", Severity: "low"},
		{FindingID: "F-5", Severity: "nit"},
		{FindingID: "F-6", Severity: ""}, // ranks as nit
	}

	t.Run("empty min keeps all", func(t *testing.T) {
		kept, dropped := FilterByMinSeverity(findings, "")
		if dropped != 0 || len(kept) != len(findings) {
			t.Fatalf("empty min: kept %d dropped %d, want kept %d dropped 0", len(kept), dropped, len(findings))
		}
	})

	t.Run("high keeps critical and high", func(t *testing.T) {
		kept, dropped := FilterByMinSeverity(findings, "high")
		if len(kept) != 2 || dropped != 4 {
			t.Fatalf("min high: kept %d dropped %d, want kept 2 dropped 4", len(kept), dropped)
		}
		for _, f := range kept {
			if f.Severity != "critical" && f.Severity != "high" {
				t.Errorf("min high kept unexpected severity %q", f.Severity)
			}
		}
	})

	t.Run("low drops nit and empty", func(t *testing.T) {
		kept, dropped := FilterByMinSeverity(findings, "low")
		// critical, high, medium, low kept; nit and "" dropped.
		if len(kept) != 4 || dropped != 2 {
			t.Fatalf("min low: kept %d dropped %d, want kept 4 dropped 2", len(kept), dropped)
		}
	})

	t.Run("nit keeps all including empty severity", func(t *testing.T) {
		kept, dropped := FilterByMinSeverity(findings, "nit")
		if len(kept) != len(findings) || dropped != 0 {
			t.Fatalf("min nit: kept %d dropped %d, want kept %d dropped 0", len(kept), dropped, len(findings))
		}
	})

	t.Run("critical keeps only critical", func(t *testing.T) {
		kept, dropped := FilterByMinSeverity(findings, "critical")
		if len(kept) != 1 || dropped != 5 || kept[0].Severity != "critical" {
			t.Fatalf("min critical: kept %d dropped %d, want kept 1 dropped 5", len(kept), dropped)
		}
	})
}

package audit

import (
	"strings"
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

func TestRenderCategoryChart_Empty(t *testing.T) {
	result := RenderCategoryChart(nil)
	if result != "" {
		t.Errorf("expected empty string for nil findings, got %q", result)
	}
}

func TestRenderCategoryChart_SingleCategory(t *testing.T) {
	findings := []state.DeepFinding{
		{Category: "security"},
		{Category: "security"},
	}
	result := RenderCategoryChart(findings)
	if !strings.Contains(result, "security") {
		t.Error("expected output to contain category name")
	}
	if !strings.Contains(result, "2") {
		t.Error("expected output to contain count")
	}
	if !strings.Contains(result, "█") {
		t.Error("expected output to contain bar character")
	}
}

func TestRenderCategoryChart_MultipleCategories(t *testing.T) {
	findings := []state.DeepFinding{
		{Category: "security"},
		{Category: "security"},
		{Category: "security"},
		{Category: "performance"},
		{Category: "style"},
	}
	result := RenderCategoryChart(findings)
	if !strings.Contains(result, "security") {
		t.Error("expected security category")
	}
	if !strings.Contains(result, "performance") {
		t.Error("expected performance category")
	}
	if !strings.Contains(result, "style") {
		t.Error("expected style category")
	}
	// Security (3) should appear before performance (1)
	secIdx := strings.Index(result, "security")
	perfIdx := strings.Index(result, "performance")
	if secIdx > perfIdx {
		t.Error("expected categories sorted by count descending")
	}
}

func TestRenderSeverityBar_Empty(t *testing.T) {
	result := RenderSeverityBar(nil)
	if result != "" {
		t.Errorf("expected empty string for nil findings, got %q", result)
	}
}

func TestRenderSeverityBar_SingleSeverity(t *testing.T) {
	findings := []state.DeepFinding{
		{Severity: "high"},
		{Severity: "high"},
	}
	result := RenderSeverityBar(findings)
	if !strings.Contains(result, "2 high") {
		t.Error("expected legend with count and severity")
	}
}

func TestRenderSeverityBar_MultipleSeverities(t *testing.T) {
	findings := []state.DeepFinding{
		{Severity: "critical"},
		{Severity: "high"},
		{Severity: "high"},
		{Severity: "medium"},
		{Severity: "low"},
	}
	result := RenderSeverityBar(findings)
	if !strings.Contains(result, "1 critical") {
		t.Error("expected critical in legend")
	}
	if !strings.Contains(result, "2 high") {
		t.Error("expected high in legend")
	}
	if !strings.Contains(result, "1 medium") {
		t.Error("expected medium in legend")
	}
	if !strings.Contains(result, "1 low") {
		t.Error("expected low in legend")
	}
}

func TestRenderSeverityBar_EmptySeverityDefaultsToLow(t *testing.T) {
	findings := []state.DeepFinding{
		{Severity: ""},
	}
	result := RenderSeverityBar(findings)
	if !strings.Contains(result, "1 low") {
		t.Error("expected empty severity to default to low")
	}
}

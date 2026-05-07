package audit

import (
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/andreujuanc/prr/internal/config"
)

// ModelSelection holds the user's model choices from the interactive prompt.
type ModelSelection struct {
	ReviewModel string
	AOIModel    string
}

// PromptModels shows two interactive select prompts for choosing the review
// and AOI models. The current config values are pre-selected as defaults.
// Returns the user's choices or an error if the prompt was cancelled.
func PromptModels(cfg *config.Config) (*ModelSelection, error) {
	bench, _ := config.LoadBenchmarkResults()

	reviewModels := config.ReviewModels(cfg.Provider)
	aoiModels := config.AOIModels(cfg.Provider)

	if len(reviewModels) == 0 {
		return nil, fmt.Errorf("no review models available for provider %q", cfg.Provider)
	}
	if len(aoiModels) == 0 {
		return nil, fmt.Errorf("no AOI models available for provider %q", cfg.Provider)
	}

	// Build review model options
	reviewOpts := make([]huh.Option[string], len(reviewModels))
	for i, m := range reviewModels {
		label := formatModelLabel(m, bench)
		reviewOpts[i] = huh.NewOption(label, m.ID)
	}

	// Build AOI model options
	aoiOpts := make([]huh.Option[string], len(aoiModels))
	for i, m := range aoiModels {
		label := formatAOIModelLabel(m, bench)
		aoiOpts[i] = huh.NewOption(label, m.ID)
	}

	// Defaults from config
	reviewDefault := cfg.Model
	if reviewDefault == "" {
		reviewDefault = reviewModels[0].ID
	}
	aoiDefault := cfg.AOIModel
	if aoiDefault == "" {
		aoiDefault = aoiModels[0].ID
	}

	var selection ModelSelection
	selection.ReviewModel = reviewDefault
	selection.AOIModel = aoiDefault

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Review Model").
				Description("Deep review model for Phase 3 analysis").
				Options(reviewOpts...).
				Value(&selection.ReviewModel),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("AOI Model").
				Description("Fast/cheap model for Phase 2 pre-scan").
				Options(aoiOpts...).
				Value(&selection.AOIModel),
		),
	).WithTheme(huh.ThemeCatppuccin())

	if err := form.Run(); err != nil {
		return nil, fmt.Errorf("model selection cancelled: %w", err)
	}

	return &selection, nil
}

// formatModelLabel builds a display label for a review model.
func formatModelLabel(m config.KnownModel, bench *config.BenchmarkResults) string {
	label := m.Label
	if m.Thinking {
		label += "  [thinking]"
	}
	label += fmt.Sprintf("  %s  $%.2f/$%.2f per 1M tok",
		m.SpeedIcon(), m.InputPricePer1M, m.OutputPricePer1M)
	return label
}

// formatAOIModelLabel builds a display label for an AOI model with benchmark data.
func formatAOIModelLabel(m config.KnownModel, bench *config.BenchmarkResults) string {
	label := m.Label

	// Show benchmark data if available
	if bm := bench.GetModelBenchmark(m.ID); bm != nil {
		label += fmt.Sprintf("  %.0f%% recall  %.1fs  $%.3f/scan",
			bm.RecallPct, float64(bm.LatencyMs)/1000, bm.CostPerScan)
	} else {
		label += fmt.Sprintf("  %s  $%.2f/$%.2f per 1M tok",
			m.SpeedIcon(), m.InputPricePer1M, m.OutputPricePer1M)
	}

	return label
}

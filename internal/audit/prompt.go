package audit

import (
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/andreujuanc/prr/internal/config"
)

// ModelSelection holds the user's model choices from the interactive prompt.
type ModelSelection struct {
	StrongModel string // "provider/model-id" ref for deep review
	FastModel   string // "provider/model-id" ref for AOI pre-scan
}

// PromptModels shows interactive select prompts for choosing the
// review model and AOI model. Returns the user's choices or an error if cancelled.
func PromptModels(cfg *config.Config) (*ModelSelection, error) {
	bench, _ := config.LoadBenchmarkResults()

	providers := cfg.ConfiguredProviders()

	reviewModels := config.ReviewModels(providers...)
	aoiModels := config.AOIModels(providers...)

	if len(reviewModels) == 0 {
		return nil, fmt.Errorf("no review models available")
	}
	if len(aoiModels) == 0 {
		return nil, fmt.Errorf("no AOI models available")
	}

	// Build review model options (provider/model-id as value)
	reviewOpts := make([]huh.Option[string], len(reviewModels))
	for i, m := range reviewModels {
		label := formatModelLabel(m, bench)
		reviewOpts[i] = huh.NewOption(label, m.Provider+"/"+m.ID)
	}

	// Build AOI model options
	aoiOpts := make([]huh.Option[string], len(aoiModels))
	for i, m := range aoiModels {
		label := formatAOIModelLabel(m, bench)
		aoiOpts[i] = huh.NewOption(label, m.Provider+"/"+m.ID)
	}

	// Defaults from config — validate they exist in the options list
	reviewDefault := cfg.StrongModel
	if !hasOption(reviewOpts, reviewDefault) {
		reviewDefault = reviewOpts[0].Value
	}
	aoiDefault := cfg.FastModel
	if !hasOption(aoiOpts, aoiDefault) {
		aoiDefault = aoiOpts[0].Value
	}

	var selection ModelSelection
	selection.StrongModel = reviewDefault
	selection.FastModel = aoiDefault

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Strong Model").
				Description("Deep review model for analysis and synthesis").
				Options(reviewOpts...).
				Value(&selection.StrongModel),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Fast Model").
				Description("Fast/cheap model for discovery and AOI pre-scan").
				Options(aoiOpts...).
				Value(&selection.FastModel),
		),
	).WithTheme(huh.ThemeCatppuccin())

	if err := form.Run(); err != nil {
		return nil, fmt.Errorf("model selection cancelled: %w", err)
	}

	return &selection, nil
}

// hasOption checks if a value exists in a list of huh options.
func hasOption(opts []huh.Option[string], val string) bool {
	for _, o := range opts {
		if o.Value == val {
			return true
		}
	}
	return false
}

// formatModelLabel builds a display label for a review model.
func formatModelLabel(m config.KnownModel, bench *config.BenchmarkResults) string {
	thinking := ""
	if m.Thinking {
		thinking = "[thinking]"
	}
	return fmt.Sprintf("%-16s %-24s %-10s %s  $%.2f/$%.2f per 1M tok",
		"["+m.Provider+"]", m.Label, thinking, m.SpeedIcon(), m.InputPricePer1M, m.OutputPricePer1M)
}

// formatAOIModelLabel builds a display label for an AOI model with benchmark data.
func formatAOIModelLabel(m config.KnownModel, bench *config.BenchmarkResults) string {
	// Show benchmark data if available
	if bm := bench.GetModelBenchmark(m.ID); bm != nil {
		return fmt.Sprintf("%-16s %-24s %.0f%% recall  %.1fs  $%.3f/scan",
			"["+m.Provider+"]", m.Label, bm.RecallPct, float64(bm.LatencyMs)/1000, bm.CostPerScan)
	}
	return fmt.Sprintf("%-16s %-24s %s  $%.2f/$%.2f per 1M tok",
		"["+m.Provider+"]", m.Label, m.SpeedIcon(), m.InputPricePer1M, m.OutputPricePer1M)
}

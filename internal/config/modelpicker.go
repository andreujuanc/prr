package config

import (
	"fmt"
	"os"
	"strings"

	"charm.land/huh/v2"
)

// Interactive model selection shared by the `prr audit` and
// `prr review` commands. Lives here (rather than in a separate package)
// so it can directly use ConfiguredProviders / ReviewModels / AOIModels
// without exposing internals — and so both commands stay symmetrical.
//
// This is the only place in internal/config that depends on a TUI
// library. If a future caller needs a non-interactive flow, prefer to
// add a SelectFromArgs that skips the prompt rather than re-implement
// the env-var precedence or validation rules.

// ModelSelection holds the user's strong / fast model choices.
type ModelSelection struct {
	StrongModel string // "provider/model-id" ref used for deep review
	FastModel   string // "provider/model-id" ref used for AOI pre-scan
}

// ResolveModels resolves which strong / fast models a command should
// use, applies the choice to cfg (StrongModel / FastModel), and returns
// the parsed refs for downstream callers (API-key checks, model
// profile lookups).
//
// Precedence:
//
//  1. PRR_REVIEW_MODEL + PRR_AOI_MODEL env vars — non-interactive.
//     Both must be set; either may be a bare model ID or a full
//     "provider/model-id" ref. Bare IDs are resolved against
//     cfg.Providers (so CI environments can pin a model without
//     knowing which provider it lives under).
//  2. Interactive picker (huh form). cfg.StrongModel / cfg.FastModel
//     are used as the pre-selected defaults when they're in the
//     options list — so a user who has chosen models before sees
//     their previous choice highlighted.
//
// Returns an error if no review or AOI models are available, if the
// selected ref is malformed, or if the selected provider has no
// API key configured (and isn't a keyless provider like claude-code).
// darkCatppuccin pins huh's Catppuccin palette to its dark variant.
// huh v2 themes are a function of isDark, resolved from a
// tea.BackgroundColorMsg that huh never asks for — its Init only
// requests window size — so the flag stays false and the light palette
// renders on a dark terminal. prr's built-in themes are all dark.
var darkCatppuccin = huh.ThemeFunc(func(bool) *huh.Styles {
	return huh.ThemeCatppuccin(true)
})

func (c *Config) ResolveModels() (strongRef, fastRef ModelRef, err error) {
	selection, err := c.selectModels()
	if err != nil {
		return strongRef, fastRef, err
	}

	strongRef, err = ParseModelRef(selection.StrongModel)
	if err != nil {
		return strongRef, fastRef, fmt.Errorf("invalid review model: %w", err)
	}
	fastRef, err = ParseModelRef(selection.FastModel)
	if err != nil {
		return strongRef, fastRef, fmt.Errorf("invalid AOI model: %w", err)
	}

	if c.APIKeyFor(strongRef.Provider) == "" && !IsKeylessProvider(strongRef.Provider) {
		return strongRef, fastRef, fmt.Errorf("no API key for provider %q (used by review model %q)", strongRef.Provider, selection.StrongModel)
	}
	if c.APIKeyFor(fastRef.Provider) == "" && !IsKeylessProvider(fastRef.Provider) {
		return strongRef, fastRef, fmt.Errorf("no API key for provider %q (used by AOI model %q)", fastRef.Provider, selection.FastModel)
	}

	c.StrongModel = selection.StrongModel
	c.FastModel = selection.FastModel
	return strongRef, fastRef, nil
}

// selectModels returns a ModelSelection from env vars or the
// interactive picker.
func (c *Config) selectModels() (*ModelSelection, error) {
	envReview := os.Getenv("PRR_REVIEW_MODEL")
	envAOI := os.Getenv("PRR_AOI_MODEL")
	if envReview != "" && envAOI != "" {
		reviewRef := envReview
		if !strings.Contains(reviewRef, "/") {
			reviewRef = c.resolveModelProvider(envReview)
		}
		aoiRef := envAOI
		if !strings.Contains(aoiRef, "/") {
			aoiRef = c.resolveModelProvider(envAOI)
		}
		return &ModelSelection{StrongModel: reviewRef, FastModel: aoiRef}, nil
	}
	return c.promptModels()
}

// promptModels shows the interactive picker. Pre-selects
// cfg.StrongModel / cfg.FastModel as defaults when they're in the
// candidate list.
func (c *Config) promptModels() (*ModelSelection, error) {
	bench, _ := LoadBenchmarkResults("aoi")
	providers := c.ConfiguredProviders()

	reviewModels := ReviewModels(providers...)
	aoiModels := AOIModels(providers...)

	if len(reviewModels) == 0 {
		return nil, fmt.Errorf("no review models available")
	}
	if len(aoiModels) == 0 {
		return nil, fmt.Errorf("no AOI models available")
	}

	reviewOpts := make([]huh.Option[string], len(reviewModels))
	for i, m := range reviewModels {
		reviewOpts[i] = huh.NewOption(formatModelLabel(m, bench), m.Provider+"/"+m.ID)
	}

	aoiOpts := make([]huh.Option[string], len(aoiModels))
	for i, m := range aoiModels {
		aoiOpts[i] = huh.NewOption(formatAOIModelLabel(m, bench), m.Provider+"/"+m.ID)
	}

	reviewDefault := c.StrongModel
	if !hasOption(reviewOpts, reviewDefault) {
		reviewDefault = reviewOpts[0].Value
	}
	aoiDefault := c.FastModel
	if !hasOption(aoiOpts, aoiDefault) {
		aoiDefault = aoiOpts[0].Value
	}

	selection := ModelSelection{
		StrongModel: reviewDefault,
		FastModel:   aoiDefault,
	}

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
	).WithTheme(darkCatppuccin)

	if err := form.Run(); err != nil {
		return nil, fmt.Errorf("model selection cancelled: %w", err)
	}

	return &selection, nil
}

// resolveModelProvider resolves a bare model ID to "provider/model"
// format, preferring a provider the user has actually configured with
// an API key — or, for keyless providers like claude-code, one that's
// detected on PATH.
func (c *Config) resolveModelProvider(modelID string) string {
	for provName, pc := range c.Providers {
		if pc.APIKey == "" && !IsKeylessProvider(provName) {
			continue
		}
		if _, ok := GetKnownModelForProvider(provName, modelID); ok {
			return provName + "/" + modelID
		}
	}
	if km, ok := GetKnownModel(modelID); ok {
		return km.Provider + "/" + modelID
	}
	return modelID
}

func hasOption(opts []huh.Option[string], val string) bool {
	for _, o := range opts {
		if o.Value == val {
			return true
		}
	}
	return false
}

func formatModelLabel(m KnownModel, bench *BenchmarkResults) string {
	thinking := ""
	if m.Thinking {
		thinking = "[thinking]"
	}
	return fmt.Sprintf("%-16s %-24s %-10s %s  $%.2f/$%.2f per 1M tok",
		"["+m.Provider+"]", m.Label, thinking, m.SpeedIcon(), m.InputPricePer1M, m.OutputPricePer1M)
}

func formatAOIModelLabel(m KnownModel, bench *BenchmarkResults) string {
	if bm := bench.GetModelBenchmark(m.ID); bm != nil {
		return fmt.Sprintf("%-16s %-24s %.0f%% recall  %.1fs  $%.3f/scan",
			"["+m.Provider+"]", m.Label, bm.RecallPct, float64(bm.LatencyMs)/1000, bm.CostPerScan)
	}
	return fmt.Sprintf("%-16s %-24s %s  $%.2f/$%.2f per 1M tok",
		"["+m.Provider+"]", m.Label, m.SpeedIcon(), m.InputPricePer1M, m.OutputPricePer1M)
}

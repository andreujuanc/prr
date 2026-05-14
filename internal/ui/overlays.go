package ui

import (
	"fmt"
	"log"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ── PR Picker ───────────────────────────────────────────────────────────

// renderPRPicker renders the pull request selection overlay.
func (m Model) renderPRPicker() string {
	width := 64
	var b strings.Builder

	b.WriteString(styleAccentBlueBold.Render("  SELECT PULL REQUEST"))
	b.WriteString("\n")

	if m.prPickerLoading {
		b.WriteString("\n")
		b.WriteString("  " + m.spinner.View() + " " + styleTextSecondary.Render("Fetching open pull requests..."))
		b.WriteString("\n")
		return b.String()
	}

	if m.prPickerError != "" {
		b.WriteString("\n")
		b.WriteString("  " + styleAccentRed.Render(m.prPickerError))
		b.WriteString("\n\n")
		b.WriteString(styleTextMuted.Render("  Press q to quit"))
		return b.String()
	}

	b.WriteString("\n")

	// Clamp visible items to fit the terminal (header=2, footer=2, border=2)
	maxVisible := m.height - 8
	if maxVisible < 5 {
		maxVisible = 5
	}
	total := len(m.prPickerItems)

	// Compute visible window centered on cursor
	start := 0
	end := total
	if total > maxVisible {
		start = m.prPickerCursor - maxVisible/2
		if start < 0 {
			start = 0
		}
		end = start + maxVisible
		if end > total {
			end = total
			start = end - maxVisible
		}
	}

	if start > 0 {
		b.WriteString(styleTextMuted.Render("  ...") + "\n")
	}

	for i := start; i < end; i++ {
		pr := m.prPickerItems[i]
		isSelected := i == m.prPickerCursor

		marker := "  "
		if isSelected {
			marker = styleAccentBlueBold.Render("> ")
		}

		num := fmt.Sprintf("#%-4d", pr.Number)
		title := pr.Title
		maxTitle := width - 16 // room for marker + #num + author
		if maxTitle < 20 {
			maxTitle = 20
		}
		titleRunes := []rune(title)
		if len(titleRunes) > maxTitle {
			title = string(titleRunes[:maxTitle-3]) + "..."
		}
		author := pr.Author.Login

		var line string
		if isSelected {
			line = fmt.Sprintf("%s%s %s %s", marker,
				styleAccentBlueBold.Render(num),
				styleTextPrimary.Bold(true).Render(title),
				styleTextMuted.Render("("+author+")"))
		} else {
			line = fmt.Sprintf("%s%s %s %s", marker,
				styleTextSecondary.Render(num),
				styleTextSecondary.Render(title),
				styleTextMuted.Render("("+author+")"))
		}
		line = truncateToWidth(line, width)
		b.WriteString(line + "\n")
	}

	if end < total {
		b.WriteString(styleTextMuted.Render("  ...") + "\n")
	}

	b.WriteString("\n")
	b.WriteString(styleTextMuted.Render("  Enter select  q quit"))

	return b.String()
}

// ── Model Picker ────────────────────────────────────────────────────────

// modelPickerItem represents a selectable model in the picker.
type modelPickerItem struct {
	id       string // model ID (bare, e.g. "gemini-3.1-flash-lite")
	provider string // provider name (e.g. "gemini", "github-copilot")
	label    string // display label (short human-friendly name)
	thinking bool   // whether the model supports thinking
	price    string // formatted price tag (e.g. "$0.15/$0.60 per 1M tok")
	speed    string // speed icon (e.g. "⚡", "●", "◐")

	// Benchmark data (from ~/.config/prr/benchmark.json)
	hasBenchmark bool
	recallPct    float64 // overall recall %
	latencyMs    int     // scan latency in ms
	costPerScan  float64 // USD per scan
}

// modelRef returns the "provider/model-id" reference string.
func (m modelPickerItem) modelRef() string {
	return m.provider + "/" + m.id
}

// pickerSection groups model items under a heading.
type pickerSection struct {
	title string
	items []modelPickerItem
}

// enrichWithBenchmark loads benchmark data and populates picker items.
func enrichWithBenchmark(items []modelPickerItem) []modelPickerItem {
	bench, err := config.LoadBenchmarkResults()
	if err != nil || bench == nil {
		return items
	}
	for i := range items {
		if bm := bench.GetModelBenchmark(items[i].id); bm != nil {
			items[i].hasBenchmark = true
			items[i].recallPct = bm.RecallPct
			items[i].latencyMs = bm.LatencyMs
			items[i].costPerScan = bm.CostPerScan
		}
	}
	return items
}

// availableModels returns the review models, filtered to configured providers.
func availableModels(providers []string) []modelPickerItem {
	models := config.ReviewModels(providers...)
	items := make([]modelPickerItem, len(models))
	for i, m := range models {
		items[i] = modelPickerItem{id: m.ID, provider: m.Provider, label: m.Label, thinking: m.Thinking, price: m.PriceTag(), speed: m.SpeedIcon()}
	}
	return items
}

// availableAOIModels returns the AOI-suitable models, filtered to configured providers.
func availableAOIModels(providers []string) []modelPickerItem {
	models := config.AOIModels(providers...)
	items := make([]modelPickerItem, len(models))
	for i, m := range models {
		items[i] = modelPickerItem{id: m.ID, provider: m.Provider, label: m.Label, thinking: m.Thinking, price: m.PriceTag(), speed: m.SpeedIcon()}
	}
	return enrichWithBenchmark(items)
}

// switchModel attempts to switch the AI client to the given model.
// modelRef is "provider/model-id" format. Returns the new model name on success.
// Persists the choice to config.
func (m *Model) switchModel(modelRef string) string {
	switcher, ok := m.aiClient.(ai.ModelSwitcher)
	if !ok {
		return m.aiModelName
	}

	ref, err := config.ParseModelRef(modelRef)
	if err != nil {
		return m.aiModelName
	}

	models, _ := config.LoadModels()
	mcfg := config.GetModelConfig(models, ref.ModelID)

	// Resolve API key for the target provider. Keyless providers
	// (claude-code) don't need a key in config — their CLI handles auth.
	cfg, err := config.Load()
	if err != nil {
		return m.aiModelName
	}
	apiKey := cfg.APIKeyFor(ref.Provider)
	if apiKey == "" && !config.IsKeylessProvider(ref.Provider) {
		m.flashMsg = "No API key configured for provider " + ref.Provider
		return m.aiModelName
	}
	pc := cfg.ProviderConfigFor(ref.Provider)

	if err := switcher.SwitchModel(ai.ProviderConfig{
		ProviderName:    ref.Provider,
		ModelID:         ref.ModelID,
		APIKey:          apiKey,
		BaseURL:         pc.BaseURL,
		MaxOutputTokens: mcfg.MaxOutputTokens,
		Temperature:     mcfg.Temperature,
		ThinkingBudget:  mcfg.ThinkingBudget.Review,
	}); err != nil {
		return m.aiModelName
	}

	m.aiModelName = ref.ModelID

	// Persist to config so the choice survives restarts
	cfg.StrongModel = modelRef
	if err := config.Save(cfg); err != nil {
		log.Printf("Warning: failed to persist model selection: %v", err)
		m.flashMsg = "Warning: model changed but could not save to config"
	}

	return ref.ModelID
}

// switchAOIModel attempts to switch the AOI client to the given model.
// modelRef is "provider/model-id" format.
func (m *Model) switchAOIModel(modelRef string) string {
	if m.aoiClient == nil {
		return m.aoiModelName
	}

	switcher, ok := m.aoiClient.(ai.ModelSwitcher)
	if !ok {
		return m.aoiModelName
	}

	ref, err := config.ParseModelRef(modelRef)
	if err != nil {
		return m.aoiModelName
	}

	// Load model config for fast-mode tuning
	models, _ := config.LoadModels()
	mcfg := config.GetModelConfig(models, ref.ModelID)

	// Resolve API key for the target provider. Keyless providers
	// (claude-code) don't need a key in config — their CLI handles auth.
	cfg, err := config.Load()
	if err != nil {
		return m.aoiModelName
	}
	apiKey := cfg.APIKeyFor(ref.Provider)
	if apiKey == "" && !config.IsKeylessProvider(ref.Provider) {
		m.flashMsg = "No API key configured for provider " + ref.Provider
		return m.aoiModelName
	}
	pc := cfg.ProviderConfigFor(ref.Provider)

	if err := switcher.SwitchModel(ai.ProviderConfig{
		ProviderName:    ref.Provider,
		ModelID:         ref.ModelID,
		APIKey:          apiKey,
		BaseURL:         pc.BaseURL,
		MaxOutputTokens: mcfg.MaxOutputTokens,
		Temperature:     mcfg.Temperature,
		ThinkingBudget:  mcfg.ThinkingBudget.Fast,
	}); err != nil {
		return m.aoiModelName
	}

	m.aoiModelName = ref.ModelID
	m.aoiContextLines = mcfg.ResolvedAOIContextLines()

	// Persist to config
	cfg.FastModel = modelRef
	if err := config.Save(cfg); err != nil {
		log.Printf("Warning: failed to persist AOI model selection: %v", err)
		m.flashMsg = "Warning: AOI model changed but could not save to config"
	}

	return ref.ModelID
}

// modelPickerSections returns the combined list of picker sections,
// filtered to only show models from providers the user has configured.
func (m Model) modelPickerSections() []pickerSection {
	// Load config to determine which providers have API keys
	var providers []string
	if cfg, err := config.Load(); err == nil {
		providers = cfg.ConfiguredProviders()
	}

	sections := []pickerSection{
		{title: "STRONG MODEL (review)", items: availableModels(providers)},
	}
	if m.aoiClient != nil {
		sections = append(sections, pickerSection{
			title: "FAST MODEL (discovery/AOI)",
			items: availableAOIModels(providers),
		})
	}
	return sections
}

// modelPickerTotalItems returns the total number of items across all sections.
func modelPickerTotalItems(sections []pickerSection) int {
	n := 0
	for _, s := range sections {
		n += len(s.items)
	}
	return n
}

// modelPickerItemAt resolves a flat cursor index to (section, item-within-section).
func modelPickerItemAt(sections []pickerSection, cursor int) (section int, item int) {
	offset := 0
	for si, s := range sections {
		if cursor < offset+len(s.items) {
			return si, cursor - offset
		}
		offset += len(s.items)
	}
	// Shouldn't happen, clamp to last
	if len(sections) == 0 {
		return 0, 0
	}
	last := sections[len(sections)-1]
	return len(sections) - 1, len(last.items) - 1
}

// renderModelPicker renders the model selection overlay with review + AOI sections.
func (m Model) renderModelPicker() string {
	sections := m.modelPickerSections()

	width := 60
	var b strings.Builder

	globalIdx := 0
	for si, section := range sections {
		if si > 0 {
			b.WriteString("\n")
		}
		b.WriteString(styleAccentBlueBold.Render("  " + section.title))
		b.WriteString("\n\n")

		for _, model := range section.items {
			isSelected := globalIdx == m.modelPickerCursor
			isCurrent := (si == 0 && model.id == m.aiModelName) ||
				(si == 1 && model.id == m.aoiModelName)

			marker := "  "
			if isSelected {
				marker = styleAccentBlueBold.Render("> ")
			}

			providerTag := styleTextMuted.Render("[" + model.provider + "] ")
			name := model.label
			if isSelected {
				name = styleTextPrimary.Bold(true).Render(name)
			} else {
				name = styleTextSecondary.Render(name)
			}

			suffix := ""
			if model.thinking {
				suffix = styleTextMuted.Render(" [thinking]")
			}
			if isCurrent {
				suffix += styleAccentGreen.Render(" ●")
			}

			// Benchmark or static metadata
			meta := ""
			if model.hasBenchmark {
				meta = styleTextMuted.Render(fmt.Sprintf("  %.0f%% recall  %.1fs  $%.3f/scan",
					model.recallPct, float64(model.latencyMs)/1000, model.costPerScan))
			} else if model.speed != "" || model.price != "" {
				parts := []string{}
				if model.speed != "" {
					parts = append(parts, model.speed)
				}
				if model.price != "" {
					parts = append(parts, model.price)
				}
				meta = styleTextMuted.Render("  " + strings.Join(parts, " "))
			}

			line := fmt.Sprintf("%s%s%s%s%s", marker, providerTag, name, suffix, meta)
			line = truncateToWidth(line, width)
			b.WriteString(line + "\n")
			globalIdx++
		}
	}

	b.WriteString("\n")
	b.WriteString(styleTextMuted.Render("  Enter select  Esc cancel"))

	return b.String()
}

// ── Help Modal ──────────────────────────────────────────────────────────

type helpBinding struct {
	key  string
	desc string
}

type helpSection struct {
	title    string
	bindings []helpBinding
}

func (m Model) helpSections() []helpSection {
	sections := []helpSection{
		{
			title: "GLOBAL",
			bindings: []helpBinding{
				{"j/k", "Navigate / move cursor"},
				{"Tab / S-Tab", "Cycle panes"},
				{"Ctrl+A", "Toggle AI panel"},
				{"Ctrl+B", "Toggle file panel"},
				{"a", "AI review (file or PR)"},
				{"A", "Force re-review (no cache)"},
				{"Ctrl+S", "Submit PR review to GitHub"},
				{"m", "Switch model"},
				{"T", "Switch theme"},
				{"?", "Toggle this help"},
				{"q", "Quit"},
			},
		},
		{
			title: "FILE LIST",
			bindings: []helpBinding{
				{"Enter", "Select file"},
				{"l/h", "Expand dir / go to parent"},
				{"Space", "Toggle reviewed status"},
				{"r", "Toggle hide reviewed"},
				{"n/p", "Next / prev unreviewed"},
				{"o", "Refresh PR from origin"},
			},
		},
		{
			title: "DIFF",
			bindings: []helpBinding{
				{"G/g", "Jump to bottom / top"},
				{"Ctrl+D/U", "Half-page down / up"},
				{"+/-", "More / less context"},
				{"Space", "Toggle reviewed status"},
				{"n/p", "Next / prev unreviewed"},
				{"c", "Comment on line"},
				{"b", "Toggle git blame"},
				{"Esc", "Back to review (from finding)"},
			},
		},
	}

	// AI panel section varies by tab
	if m.aiPanelTab == 0 {
		sections = append(sections, helpSection{
			title: "REVIEW",
			bindings: []helpBinding{
				{"Enter", "Jump to finding"},
				{"[ / ]", "Switch sub-tab"},
				{"Ctrl+S", "Submit review to GitHub"},
			},
		})
	} else {
		sections = append(sections, helpSection{
			title: "CHAT",
			bindings: []helpBinding{
				{"Enter", "Send message"},
				{"Ctrl+K", "Clear chat"},
				{"[ / ]", "Switch sub-tab"},
			},
		})
	}

	return sections
}

// renderHelpModal renders the full-screen help overlay.
func (m Model) renderHelpModal() string {
	sections := m.helpSections()

	maxWidth := m.width - 8
	if maxWidth < 40 {
		maxWidth = 40
	}
	if maxWidth > 60 {
		maxWidth = 60
	}

	var b strings.Builder

	b.WriteString(styleAccentBlueBold.Render("  KEYBOARD SHORTCUTS"))
	b.WriteString("\n")

	for _, section := range sections {
		b.WriteString("\n")
		b.WriteString(styleAccentYellowBold.Render("  " + section.title))
		b.WriteString("\n")

		for _, bind := range section.bindings {
			key := footerKeyStyle.Render(fmt.Sprintf("  %-14s", bind.key))
			desc := styleTextSecondary.Render(bind.desc)
			line := key + "  " + desc
			line = truncateToWidth(line, maxWidth)
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(styleTextMuted.Render("  Press ? or Esc to close"))

	return b.String()
}

// ── Error Modal ─────────────────────────────────────────────────────────

// renderErrorModal renders an error message as a dismissable overlay.
func (m Model) renderErrorModal() string {
	var b strings.Builder
	b.WriteString(styleAccentRed.Bold(true).Render("  ERROR"))
	b.WriteString("\n\n")
	b.WriteString(styleTextPrimary.Render("  " + strings.ReplaceAll(ansi.Strip(m.errorMsg), "\n", "\n  ")))
	b.WriteString("\n\n")
	b.WriteString(styleTextMuted.Render("  Press any key to dismiss"))
	return b.String()
}

// ── Submit Review Modal ─────────────────────────────────────────────────

// renderSubmitReviewModal renders the review submission confirmation overlay.
func (m Model) renderSubmitReviewModal() string {
	if m.reviewState == nil || m.reviewState.Review == nil || m.reviewState.Review.Structured == nil {
		return ""
	}

	verdict := m.reviewState.Review.Structured.Verdict
	verdictLabel, verdictStyle := formatVerdict(verdict)

	width := 50
	var b strings.Builder

	b.WriteString(styleAccentBlueBold.Render("  SUBMIT REVIEW TO GITHUB"))
	b.WriteString("\n\n")

	b.WriteString("  Verdict: ")
	b.WriteString(verdictStyle.Render(verdictLabel))
	b.WriteString("\n\n")

	summary := m.reviewState.Review.Structured.Summary
	if len(summary) > 200 {
		summary = summary[:197] + "..."
	}
	b.WriteString(wrapStyled(styleTextSecondary, "  "+summary, width-4))
	b.WriteString("\n\n")

	b.WriteString(styleTextMuted.Render("  This will submit a formal review on the PR."))
	b.WriteString("\n\n")

	for i, opt := range []string{"Submit", "Cancel"} {
		isSelected := i == m.submitReviewCursor
		marker := "  "
		if isSelected {
			marker = styleAccentBlueBold.Render("> ")
		}
		label := opt
		if isSelected {
			label = styleTextPrimary.Bold(true).Render(label)
		} else {
			label = styleTextSecondary.Render(label)
		}
		b.WriteString(marker + label + "\n")
	}

	return b.String()
}

// ── Theme Picker ────────────────────────────────────────────────────────

// renderThemePicker renders the theme selection overlay with a color swatch preview.
func (m Model) renderThemePicker() string {
	themes := BuiltinThemes()

	width := 52
	var b strings.Builder

	b.WriteString(styleAccentBlueBold.Render("  SELECT THEME"))
	b.WriteString("\n\n")

	for i, theme := range themes {
		isSelected := i == m.themePickerCursor
		isCurrent := theme.ID == m.themeBeforePicker

		marker := "  "
		if isSelected {
			marker = styleAccentBlueBold.Render("> ")
		}

		name := theme.Name
		if isSelected {
			name = styleTextPrimary.Bold(true).Render(name)
		} else {
			name = styleTextSecondary.Render(name)
		}

		// Color swatch: show theme accent colors as colored blocks
		swatch := lipgloss.NewStyle().Foreground(theme.AccentBlue).Render("\u2588") +
			lipgloss.NewStyle().Foreground(theme.AccentGreen).Render("\u2588") +
			lipgloss.NewStyle().Foreground(theme.AccentRed).Render("\u2588") +
			lipgloss.NewStyle().Foreground(theme.AccentYellow).Render("\u2588") +
			lipgloss.NewStyle().Foreground(theme.AccentMauve).Render("\u2588") +
			lipgloss.NewStyle().Foreground(theme.AccentPeach).Render("\u2588")

		suffix := ""
		if isCurrent {
			suffix = styleAccentGreen.Render(" *")
		}

		line := fmt.Sprintf("%s%s  %s%s", marker, swatch, name, suffix)
		line = truncateToWidth(line, width)
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	b.WriteString(styleTextMuted.Render("  j/k preview  Enter apply  Esc cancel"))

	return b.String()
}

// floatOverlay composites a small floating panel on top of the base view,
// positioned in the top-right area. Unlike centerOverlay, the base content
// remains visible so the user can preview theme changes in real time.
func floatOverlay(base, content string, screenWidth, screenHeight int) string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderFocus).
		Padding(0, 1)

	box := boxStyle.Render(content)
	boxLines := strings.Split(box, "\n")

	// Measure box width from rendered output
	boxW := 0
	for _, l := range boxLines {
		if w := ansi.StringWidth(l); w > boxW {
			boxW = w
		}
	}

	baseLines := strings.Split(base, "\n")
	// Pad base to fill screen height if needed
	for len(baseLines) < screenHeight {
		baseLines = append(baseLines, "")
	}

	// Position: top-right with a small margin
	startRow := 2
	startCol := screenWidth - boxW - 2
	if startCol < 0 {
		startCol = 0
	}

	// Composite box lines onto base
	for i, bline := range boxLines {
		row := startRow + i
		if row >= len(baseLines) {
			break
		}
		baseLine := baseLines[row]
		baseW := ansi.StringWidth(baseLine)

		// Build: left portion of base + box line
		var composed string
		if startCol <= baseW {
			composed = ansi.Truncate(baseLine, startCol, "") + bline
		} else {
			composed = baseLine +
				strings.Repeat(" ", startCol-baseW) + bline
		}
		baseLines[row] = composed
	}

	// Trim to screen height
	if len(baseLines) > screenHeight {
		baseLines = baseLines[:screenHeight]
	}

	return strings.Join(baseLines, "\n")
}
func centerOverlay(content string, screenWidth, screenHeight int) string {
	lines := strings.Split(content, "\n")

	// Compute content dimensions
	maxW := 0
	for _, l := range lines {
		w := ansi.StringWidth(l)
		if w > maxW {
			maxW = w
		}
	}

	// Add padding/border
	boxWidth := maxW + 4
	if boxWidth > screenWidth-4 {
		boxWidth = screenWidth - 4
	}

	// Vertical centering
	topPad := (screenHeight - len(lines) - 2) / 2
	if topPad < 1 {
		topPad = 1
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderFocus).
		Padding(0, 1).
		Width(boxWidth)

	box := boxStyle.Render(content)
	boxLines := strings.Split(box, "\n")

	var b strings.Builder
	// Pad top
	for i := 0; i < topPad; i++ {
		b.WriteString(strings.Repeat(" ", screenWidth) + "\n")
	}
	// Center horizontally
	for _, line := range boxLines {
		w := ansi.StringWidth(line)
		leftPad := (screenWidth - w) / 2
		if leftPad < 0 {
			leftPad = 0
		}
		b.WriteString(strings.Repeat(" ", leftPad) + line + "\n")
	}

	return b.String()
}

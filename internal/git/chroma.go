package git

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// GetChromaDiffWithContext renders a syntax-highlighted diff using chroma
// instead of the external delta binary. This is an experimental alternative
// renderer enabled via --chroma flag.
func GetChromaDiffWithContext(base, head, file string, contextLines int, theme DiffTheme, width int) (string, error) {
	raw, err := GetRawDiffWithContext(base, head, file, contextLines)
	if err != nil {
		return "", err
	}
	if raw == "" {
		return "", nil
	}

	return renderChromaDiff(raw, file, theme, width)
}

// renderChromaDiff takes a raw unified diff string and renders it with
// chroma syntax highlighting and ANSI colors.
func renderChromaDiff(rawDiff, filePath string, theme DiffTheme, width int) (string, error) {
	// Pick lexer based on file extension
	lexer := lexers.Match(filePath)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	// Pick chroma style
	styleName := theme.ChromaSyntaxStyle
	if styleName == "" {
		styleName = "monokai"
	}
	style := styles.Get(styleName)
	if style == nil {
		style = styles.Fallback
	}

	// Parse the unified diff into hunks
	hunks := parseUnifiedDiff(rawDiff)

	var b strings.Builder

	// File header — overline then file name (matches delta's "ol" decoration)
	sepWidth := width
	if sepWidth < 20 {
		sepWidth = 80
	}
	b.WriteString(ansiColorize(strings.Repeat("─", sepWidth), theme.SubtleColor))
	b.WriteString("\n")
	b.WriteString(ansiColorize(filePath, theme.AccentBlue))
	b.WriteString("\n")

	for _, hunk := range hunks {
		// Hunk header
		b.WriteString(ansiColorize(hunk.header, theme.AccentMauve))
		b.WriteString("\n")

		for _, line := range hunk.lines {
			// Line numbers: format as "old│new│ "
			lineNumStr := formatLineNumbers(line, theme)

			// Syntax-highlight the code content (strip the +/- prefix)
			code := line.content
			highlighted := highlightLine(lexer, style, code)

			// Apply diff background color
			var bgStart, bgEnd string
			switch line.kind {
			case diffAdded:
				bgStart = ansiTrueColorBg(theme.AddedBg)
				bgEnd = "\x1b[49m"
			case diffRemoved:
				bgStart = ansiTrueColorBg(theme.RemovedBg)
				bgEnd = "\x1b[49m"
			default:
				// Context lines: no background
			}

			b.WriteString(lineNumStr)
			b.WriteString(bgStart)
			b.WriteString(highlighted)
			b.WriteString(bgEnd)
			b.WriteString("\n")
		}
	}

	return b.String(), nil
}

// ── Diff parsing ───────────────────────────────────────────────────────

type diffLineKind int

const (
	diffContext diffLineKind = iota
	diffAdded
	diffRemoved
)

type diffLine struct {
	kind    diffLineKind
	content string // line content without the +/- prefix
	oldNum  int    // 0 if not applicable (added lines)
	newNum  int    // 0 if not applicable (removed lines)
}

type diffHunk struct {
	header string
	lines  []diffLine
}

// parseUnifiedDiff parses a raw unified diff into structured hunks.
func parseUnifiedDiff(raw string) []diffHunk {
	lines := strings.Split(raw, "\n")
	var hunks []diffHunk
	var current *diffHunk
	var oldLine, newLine int

	for _, line := range lines {
		// Skip diff metadata (--- / +++ / diff --git)
		if strings.HasPrefix(line, "diff --git") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "--- ") ||
			strings.HasPrefix(line, "+++ ") {
			continue
		}

		// Hunk header: @@ -old,count +new,count @@
		if strings.HasPrefix(line, "@@") {
			h := diffHunk{header: line}
			hunks = append(hunks, h)
			current = &hunks[len(hunks)-1]

			// Parse line numbers from header
			oldLine, newLine = parseHunkHeader(line)
			continue
		}

		if current == nil {
			continue
		}

		if after, ok := strings.CutPrefix(line, "+"); ok {
			current.lines = append(current.lines, diffLine{
				kind:    diffAdded,
				content: after,
				newNum:  newLine,
			})
			newLine++
		} else if after, ok := strings.CutPrefix(line, "-"); ok {
			current.lines = append(current.lines, diffLine{
				kind:    diffRemoved,
				content: after,
				oldNum:  oldLine,
			})
			oldLine++
		} else if strings.HasPrefix(line, " ") || line == "" {
			content := line
			if strings.HasPrefix(line, " ") {
				content = line[1:]
			}
			current.lines = append(current.lines, diffLine{
				kind:    diffContext,
				content: content,
				oldNum:  oldLine,
				newNum:  newLine,
			})
			oldLine++
			newLine++
		}
	}

	return hunks
}

// parseHunkHeader extracts starting line numbers from "@@ -old,count +new,count @@".
func parseHunkHeader(header string) (oldStart, newStart int) {
	// Find the range info between @@ markers
	parts := strings.SplitN(header, "@@", 3)
	if len(parts) < 2 {
		return 1, 1
	}
	rangePart := strings.TrimSpace(parts[1])

	// Parse -old,count +new,count
	var oldCount, newCount int
	n, _ := fmt.Sscanf(rangePart, "-%d,%d +%d,%d", &oldStart, &oldCount, &newStart, &newCount)
	if n < 2 {
		// Try without count: -old +new
		fmt.Sscanf(rangePart, "-%d +%d", &oldStart, &newStart)
	}
	return oldStart, newStart
}

// ── Rendering helpers ──────────────────────────────────────────────────

// formatLineNumbers formats the line number gutter for a diff line.
//
// Numbers use SubtleColor rather than SurfaceColor: SurfaceColor comes
// from the theme's overlay *background*, which is the darkest ink on
// screen and left the gutter hard to read. SubtleColor is the theme's
// dimmest text role, which is what a gutter actually is.
func formatLineNumbers(dl diffLine, theme DiffTheme) string {
	switch dl.kind {
	case diffAdded:
		old := "   "
		new := fmt.Sprintf("%3d", dl.newNum)
		return fmt.Sprintf("%s│%s│ ",
			ansiColorize(old, theme.SubtleColor),
			ansiColorize(new, theme.AccentGreen))
	case diffRemoved:
		old := fmt.Sprintf("%3d", dl.oldNum)
		new := "   "
		return fmt.Sprintf("%s│%s│ ",
			ansiColorize(old, theme.AccentRed),
			ansiColorize(new, theme.SubtleColor))
	default:
		old := fmt.Sprintf("%3d", dl.oldNum)
		new := fmt.Sprintf("%3d", dl.newNum)
		return fmt.Sprintf("%s│%s│ ",
			ansiColorize(old, theme.SubtleColor),
			ansiColorize(new, theme.SubtleColor))
	}
}

// highlightLine uses chroma to syntax-highlight a single line of code.
//
// Note: Tokenizing each line independently means multi-line constructs
// (block comments, heredocs, raw strings) may not highlight correctly
// across line boundaries. This is an acceptable trade-off for diff
// rendering where lines are interleaved with +/- markers.
func highlightLine(lexer chroma.Lexer, style *chroma.Style, code string) string {
	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}

	var b strings.Builder
	for _, token := range iterator.Tokens() {
		entry := style.Get(token.Type)
		if entry.IsZero() {
			b.WriteString(token.Value)
			continue
		}

		var parts []string
		if entry.Bold == chroma.Yes {
			parts = append(parts, "1")
		}
		if entry.Italic == chroma.Yes {
			parts = append(parts, "3")
		}
		if entry.Colour.IsSet() {
			r, g, bl := entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue()
			parts = append(parts, fmt.Sprintf("38;2;%d;%d;%d", r, g, bl))
		}

		if len(parts) > 0 {
			b.WriteString("\x1b[" + strings.Join(parts, ";") + "m")
			b.WriteString(token.Value)
			b.WriteString("\x1b[0m")
		} else {
			b.WriteString(token.Value)
		}
	}
	return b.String()
}

// ansiColorize wraps text in a truecolor foreground ANSI sequence.
// color should be a hex string like "#A6E3A1".
func ansiColorize(text, color string) string {
	r, g, b := hexToRGB(color)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", r, g, b, text)
}

// ansiTrueColorBg returns an ANSI escape for a truecolor background.
func ansiTrueColorBg(hex string) string {
	r, g, b := hexToRGB(hex)
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

// hexToRGB parses a "#RRGGBB" hex string into RGB components.
func hexToRGB(hex string) (r, g, b uint8) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0
	}
	fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b)
	return
}

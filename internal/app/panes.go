package app

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/worxbend/yc/internal/theme"
)

// canvasDarkenAmount is how far the app canvas is pulled below the palette's
// own background. Panes fill with Surface and sit on this darker ground, which
// is what makes them read as raised rather than as flat regions of one color.
const canvasDarkenAmount = 0.14

// paneSpec describes one framed region. Every widget in yc draws through
// renderPane, so the frame, the icon-bearing title, the surface fill, and the
// role-colored left rail are defined once and cannot drift between panes.
//
// The spec carries its palette and its gradient phase rather than reading them
// from a model, so a pane can be rendered - and golden-tested - without a
// Bubble Tea model in scope.
type paneSpec struct {
	palette theme.Palette
	icon    string
	title   string
	// content is pre-fitted to contentHeight lines of (width - 2 - 2*padding)
	// cells. renderPane does not reflow it; a widget that wants its own
	// per-segment styling has already applied it.
	content       string
	width         int
	contentHeight int
	padding       int
	// accent colors the title label and the left rail. It names a palette
	// role, which is what keeps every pane themeable.
	accent  string
	focused bool
	// phase rotates the focused rail and title gradient. It comes from the
	// shared frame clock and is zero when animation is off, which collapses
	// the effect to its static first frame without changing the layout.
	phase int
}

// canvasBackground is the ground the panes sit on.
func canvasBackground(palette theme.Palette) string {
	return theme.Darken(palette.Background, canvasDarkenAmount)
}

// renderPane builds an exact-size panel whose title occupies the top border
// row. Spending the border row on the title preserves vertical capacity: a
// separate heading line would cost a row of content on every pane at once.
func renderPane(spec paneSpec) string {
	if spec.width <= 0 || spec.contentHeight < 0 {
		return ""
	}
	if spec.accent == "" {
		spec.accent = spec.palette.Accent
	}
	if spec.padding < 0 {
		spec.padding = 0
	}
	canvas := canvasBackground(spec.palette)

	railColor := spec.accent
	if spec.focused {
		colors := theme.SeamlessGradient(spec.accent, paneGradientEnd(spec.palette, spec.accent), paneRailGradientSteps)
		if len(colors) > 0 {
			railColor = colors[spec.phase%len(colors)]
		}
	}
	title := paneTitleLine(spec, railColor, canvas)
	body := lipgloss.NewStyle().
		Width(clampMin(spec.width-2, 0)).
		Height(spec.contentHeight).
		Foreground(lipgloss.Color(spec.palette.Foreground)).
		Background(lipgloss.Color(spec.palette.Surface)).
		BorderStyle(lipgloss.NormalBorder()).
		BorderTop(false).
		BorderRight(true).
		BorderBottom(true).
		BorderLeft(true).
		BorderForeground(
			lipgloss.Color(spec.palette.Border),
			lipgloss.Color(spec.palette.Border),
			lipgloss.Color(spec.palette.Border),
			lipgloss.Color(railColor),
		).
		BorderBackground(lipgloss.Color(canvas)).
		Padding(0, spec.padding).
		Render(spec.content)
	return title + "\n" + body
}

// paneRailGradientSteps is the length of the focused rail's color cycle. It is
// long enough to read as motion and short enough that one phase step is a
// visible change rather than a shimmer nobody notices.
const paneRailGradientSteps = 12

// paneTitleLine draws "┌─ 💬 Chat ─────┐" on the canvas background, so the
// title reads as part of the gap between panes rather than as a content row.
func paneTitleLine(spec paneSpec, railColor, canvas string) string {
	width := spec.width
	if width <= 0 {
		return ""
	}
	if width == 1 {
		return paneStyledText("│", railColor, canvas, true)
	}

	innerWidth := width - 2
	label := strings.TrimSpace(strings.TrimSpace(spec.icon) + " " + strings.TrimSpace(spec.title))
	label = revealDisplayCells(label, clampMin(innerWidth-3, 0))
	prefix := "─ "
	if label == "" || innerWidth < 3 {
		prefix = ""
	}
	labelText := revealDisplayCells(prefix+label+" ", innerWidth)
	labelWidth := ansi.StringWidth(labelText)
	remainder := strings.Repeat("─", clampMin(innerWidth-labelWidth, 0))

	left := paneStyledText("┌", railColor, canvas, true)
	var styledLabel string
	if spec.focused {
		styledLabel = gradientForegroundText(
			labelText,
			spec.accent,
			paneGradientEnd(spec.palette, spec.accent),
			canvas,
			spec.phase,
			true,
		)
	} else {
		styledLabel = paneStyledText(labelText, spec.accent, canvas, true)
	}
	border := paneStyledText(remainder+"┐", spec.palette.Border, canvas, false)
	return left + styledLabel + border
}

// paneGradientEnd picks the far end of a focused pane's gradient: the first
// palette role that differs from the start color. A theme that reuses one hue
// for several roles degrades to a solid rail rather than to an invisible one.
func paneGradientEnd(palette theme.Palette, start string) string {
	for _, candidate := range []string{
		palette.Success,
		palette.Warning,
		palette.Accent,
		palette.Error,
		palette.Foreground,
	} {
		if candidate != "" && !strings.EqualFold(candidate, start) {
			return candidate
		}
	}
	return start
}

func paneStyledText(text, foreground, background string, bold bool) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(foreground)).
		Background(lipgloss.Color(background)).
		Bold(bold).
		Render(text)
}

// paneLineWriter accumulates styled segments into one exact-width pane row.
//
// Widgets that color parts of a line differently (the sidebar's unread count,
// the activity glyph, a theme swatch strip) cannot use fitLine, which is not
// ANSI-aware. This walks a width budget instead, truncating each segment to
// what is left and painting the remainder with the pane surface so no cell is
// left showing the terminal's own background.
type paneLineWriter struct {
	builder    strings.Builder
	background string
	width      int
	used       int
}

func newPaneLineWriter(width int, background string) *paneLineWriter {
	return &paneLineWriter{background: background, width: clampMin(width, 0)}
}

// write appends as much of text as the remaining budget allows.
func (w *paneLineWriter) write(text, foreground string, bold bool) {
	if w.used >= w.width || text == "" {
		return
	}
	text = revealDisplayCells(text, w.width-w.used)
	if text == "" {
		return
	}
	w.builder.WriteString(paneStyledText(text, foreground, w.background, bold))
	w.used += ansi.StringWidth(text)
}

// remaining reports the unwritten cell budget.
func (w *paneLineWriter) remaining() int {
	return clampMin(w.width-w.used, 0)
}

// String pads the row to its full width and returns it.
func (w *paneLineWriter) String() string {
	if pad := w.remaining(); pad > 0 {
		w.builder.WriteString(backgroundSpaces(pad, w.background))
		w.used = w.width
	}
	return w.builder.String()
}

// clampMin is the lower-bound clamp used throughout the layout math, where a
// negative intermediate width or height is a normal transient rather than an
// error worth reporting.
func clampMin(value, minimum int) int {
	if value < minimum {
		return minimum
	}
	return value
}

// flattenControlRunes replaces every control character with a space and drops
// bidirectional overrides, before anything measures the string.
//
// A tab is the reason this exists. uniseg reports it as zero cells, but lipgloss
// expands it to four spaces when the styled line is finally rendered, so a
// single tab in an API-supplied string - a chat body reaching the status line's
// notification summary, a broadcast title in the sidebar - makes the drawn row
// wider than the row the layout budgeted. The frame then wraps, gains a row, and
// scrolls the user's terminal on every repaint. Newlines are flattened for the
// same reason: these helpers all fit exactly one line.
//
// internal/render performs the equivalent guard on chat rows; this is the same
// guarantee for the chrome that internal/app draws itself. Stripping has to
// happen before measurement, because stripping afterward would desynchronize the
// width accounting from what is actually drawn.
func flattenControlRunes(value string) string {
	if value == "" || !strings.ContainsFunc(value, isUnsafeLineRune) {
		return value
	}
	return strings.Map(func(r rune) rune {
		switch {
		case isBidiOverride(r):
			return -1
		case unicode.IsControl(r):
			return ' '
		default:
			return r
		}
	}, value)
}

// isUnsafeLineRune reports a rune that must not reach a single-line fit.
func isUnsafeLineRune(r rune) bool {
	return unicode.IsControl(r) || isBidiOverride(r)
}

// isBidiOverride reports the explicit bidirectional formatting characters. They
// occupy no cells but reorder everything drawn after them, so one chatter could
// otherwise rewrite the visible text of the chrome around their own row.
func isBidiOverride(r rune) bool {
	switch r {
	case '\u200e', '\u200f': // LRM, RLM
		return true
	case '\u202a', '\u202b', '\u202c', '\u202d', '\u202e': // LRE, RLE, PDF, LRO, RLO
		return true
	case '\u2066', '\u2067', '\u2068', '\u2069': // LRI, RLI, FSI, PDI
		return true
	default:
		return false
	}
}

// fitLine truncates or right-pads plain text to exactly width display cells.
//
// It walks grapheme clusters and accumulates display width, so an emoji or a
// combining sequence is never split down the middle and a double-width cluster
// never overruns the pane by one cell. It must not be given pre-styled text:
// ANSI escapes have no display width and would be counted as clusters.
func fitLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = flattenControlRunes(value)
	var builder strings.Builder
	used := 0
	graphemes := uniseg.NewGraphemes(value)
	for graphemes.Next() {
		cluster := graphemes.Str()
		clusterWidth := ansi.StringWidth(cluster)
		if used+clusterWidth > width {
			break
		}
		builder.WriteString(cluster)
		used += clusterWidth
	}
	if used < width {
		builder.WriteString(strings.Repeat(" ", width-used))
	}
	return builder.String()
}

// fitBlock fits value to an exact width x height rectangle of plain text.
func fitBlock(value string, width, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	out := make([]string, 0, height)
	for i := range height {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out = append(out, fitLine(line, width))
	}
	return strings.Join(out, "\n")
}

// revealDisplayCells truncates value to at most cells display cells without
// padding, the counterpart to fitLine for text that will be padded later or
// concatenated with more styled segments.
func revealDisplayCells(value string, cells int) string {
	if cells <= 0 || value == "" {
		return ""
	}
	value = flattenControlRunes(value)
	var builder strings.Builder
	used := 0
	graphemes := uniseg.NewGraphemes(value)
	for graphemes.Next() {
		cluster := graphemes.Str()
		width := ansi.StringWidth(cluster)
		if used+width > cells {
			break
		}
		builder.WriteString(cluster)
		used += width
	}
	return builder.String()
}

// tailDisplayCells keeps the end of value visible instead of the start.
//
// The composer is append-only, so the caret is always at the end: truncating
// from the front is the only form that keeps the cursor on screen once a draft
// outgrows the input surface.
func tailDisplayCells(value string, cells int) string {
	if cells <= 0 || value == "" {
		return ""
	}
	value = flattenControlRunes(value)
	clusters := make([]string, 0, len(value))
	widths := make([]int, 0, len(value))
	graphemes := uniseg.NewGraphemes(value)
	for graphemes.Next() {
		cluster := graphemes.Str()
		clusters = append(clusters, cluster)
		widths = append(widths, ansi.StringWidth(cluster))
	}
	used := 0
	start := len(clusters)
	for index := len(clusters) - 1; index >= 0; index-- {
		if used+widths[index] > cells {
			break
		}
		used += widths[index]
		start = index
	}
	return strings.Join(clusters[start:], "")
}

// centeredPlainLine center-pads plain text to width. Callers style the padded
// result as a whole, so every cell carries a background instead of exposing
// transparent gaps after an ANSI reset.
func centeredPlainLine(text string, width int) string {
	textWidth := ansi.StringWidth(text)
	if textWidth >= width {
		return text
	}
	total := width - textWidth
	left := total / 2
	return strings.Repeat(" ", left) + text + strings.Repeat(" ", total-left)
}

// centeredBlockLines centers a multi-line block as one picture, giving every
// row the same leading offset.
//
// centeredFittedLine trims each line before centering it, which is right for a
// label carrying stray padding and wrong for pre-formatted art: it shifts each
// row by its own indent and shears the shape apart. Anything whose rows are
// meant to line up with each other belongs here instead.
func centeredBlockLines(lines []string, width int) []string {
	if width <= 0 || len(lines) == 0 {
		return nil
	}
	block := min(widestLine(lines), width)
	leading := strings.Repeat(" ", max((width-block)/2, 0))

	centered := make([]string, 0, len(lines))
	for _, line := range lines {
		centered = append(centered, fitLine(leading+fitLine(line, block), width))
	}
	return centered
}

// centeredFittedLine truncates text to width and then centers it.
func centeredFittedLine(text string, width int) string {
	return centeredPlainLine(strings.TrimRight(fitLine(text, width), " "), width)
}

// truncateDisplayWidth truncates without padding, for text placed inside a
// larger line that will be padded by its own writer.
func truncateDisplayWidth(value string, width int) string {
	return strings.TrimRight(fitLine(value, width), " ")
}

// widestLine reports the display width of the widest line in lines.
func widestLine(lines []string) int {
	widest := 0
	for _, line := range lines {
		widest = max(widest, ansi.StringWidth(line))
	}
	return widest
}

// backgroundStyledLine wraps plain text in an explicit background so it renders
// opaque. Never call it on already-styled content: an embedded ANSI reset ends
// the background part-way along the line and terminals then paint their own -
// frequently transparent - default for the remainder.
func backgroundStyledLine(text, background string) string {
	if text == "" || strings.TrimSpace(background) == "" {
		return text
	}
	return lipgloss.NewStyle().Background(lipgloss.Color(background)).Render(text)
}

// backgroundSpaces renders count blank cells painted with background.
func backgroundSpaces(count int, background string) string {
	if count <= 0 {
		return ""
	}
	return lipgloss.NewStyle().
		Background(lipgloss.Color(background)).
		Render(strings.Repeat(" ", count))
}

// visibleRows returns the height-row window of rows that scrollOffset selects,
// counting the offset from the bottom because chat is bottom-anchored: offset
// zero means "showing the newest rows".
func visibleRows(rows []string, height, scrollOffset int) []string {
	if height <= 0 || len(rows) == 0 {
		return nil
	}
	if len(rows) <= height {
		return rows
	}
	maxScroll := len(rows) - height
	scrollOffset = min(clampMin(scrollOffset, 0), maxScroll)
	end := len(rows) - scrollOffset
	return rows[clampMin(end-height, 0):end]
}

// padLines extends lines to exactly height entries of blank width-wide rows and
// truncates any excess, so a pane body always matches the height the layout
// reserved for it.
func padLines(lines []string, width, height int, background string) []string {
	for len(lines) < height {
		lines = append(lines, backgroundStyledLine(fitLine("", width), background))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// windowStart centers a selected index inside a height-row window, clamped to
// the ends of the list. Every scrolling list in yc (palette, theme page, target
// picker) uses it so they all scroll the same way.
func windowStart(selected, total, height int) int {
	if height <= 0 || total <= height {
		return 0
	}
	start := selected - height/2
	if start < 0 {
		return 0
	}
	if start+height > total {
		return total - height
	}
	return start
}

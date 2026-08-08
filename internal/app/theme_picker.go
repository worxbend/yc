package app

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/theme"
)

// The theme picker is a full-screen page rather than a strip docked under chat.
// A palette has to be judged on the whole terminal - the chat surface, the
// panes, the status bar, the canvas behind them - and a preview seen through a
// chat pane squeezed down to make room for the picker tells you nothing about
// the theme you are choosing.
//
// Moving the selection applies the palette immediately. View re-derives the
// terminal background from the live palette on every frame, so the terminal's
// own background follows the preview too.

const (
	// themeSwatchCell is one swatch column. Every entry draws the same number
	// of cells so the color columns line up row for row down the page.
	themeSwatchCell = "██"
	// themeSwatchEmptyCell marks an unset slot. A solid block would paint in
	// the terminal's default foreground and read as a real color; dots make
	// "nothing configured here" legible instead.
	themeSwatchEmptyCell = "··"
)

// themePickerState is everything the page draws.
type themePickerState struct {
	// Palette is the live preview: the selected entry's palette, already
	// applied to the model, so the whole page is drawn in the theme being
	// considered.
	Palette theme.Palette
	// Names is the selectable list, filtered by the overlay query.
	Names []string
	// ActiveName is the theme the session started with, marked "(active)" so
	// the live preview can never be mistaken for the configured choice.
	ActiveName string
	// Custom backs the "custom" entry's swatches.
	Custom   theme.Palette
	Selected int
	Phase    int
}

// renderThemePicker draws the full-screen page.
func renderThemePicker(width, height int, st themePickerState) string {
	width, height = clampMin(width, 1), clampMin(height, 1)
	canvas := canvasBackground(st.Palette)
	if height < 3 || width < 5 {
		lines := themePickerLines(width, height, canvas, st)
		return backgroundStyledLine(fitBlock(strings.Join(lines, "\n"), width, height), canvas)
	}
	contentHeight := height - 2
	lines := themePickerLines(clampMin(width-4, 1), contentHeight, st.Palette.Surface, st)
	return renderPane(paneSpec{
		palette:       st.Palette,
		icon:          "🎨",
		title:         "Themes",
		content:       strings.Join(lines, "\n"),
		width:         width,
		contentHeight: contentHeight,
		padding:       1,
		accent:        st.Palette.Accent,
		focused:       true,
		phase:         st.Phase,
	})
}

// themePickerLines lays the page out: a status header, the windowed entry list,
// and a key-hint footer. Lines are pre-styled against background because the
// swatches need per-segment color, which the pane body style cannot apply.
func themePickerLines(width, height int, background string, st themePickerState) []string {
	if height <= 0 || width <= 0 {
		return nil
	}
	names := st.Names
	selected := st.Selected
	if selected < 0 || selected >= len(names) {
		selected = 0
	}

	header := "Select a theme — the preview applies for this run"
	lines := []string{paneStyledText(fitLine(header, width), st.Palette.Muted, background, false)}
	if height == 1 {
		return lines
	}

	blank := paneStyledText(fitLine("", width), st.Palette.Muted, background, false)
	footer := ""
	if height >= 4 {
		footer = paneStyledText(
			fitLine("↑/↓ move · home/end jump · enter apply · esc cancel", width),
			st.Palette.Muted, background, false,
		)
	}
	if height >= 7 {
		lines = append(lines, blank)
	}

	listHeight := height - len(lines)
	if footer != "" {
		listHeight--
	}
	labelWidth := themeLabelWidth(names, st.ActiveName)
	start := windowStart(selected, len(names), listHeight)
	for i := start; i < len(names) && listHeight > 0; i++ {
		lines = append(lines, themePickerEntryLine(names[i], width, labelWidth, i == selected, background, st))
		listHeight--
	}
	for listHeight > 0 {
		lines = append(lines, blank)
		listHeight--
	}
	if footer != "" {
		lines = append(lines, footer)
	}
	return lines
}

// themePickerEntryLine draws one row: the selection marker, the name, and a
// swatch strip in that preset's own colors, so the list doubles as a palette
// comparison without having to select each entry in turn.
func themePickerEntryLine(name string, width, labelWidth int, selected bool, background string, st themePickerState) string {
	if width <= 0 {
		return ""
	}
	writer := newPaneLineWriter(width, background)

	prefix, prefixColor := "  ", st.Palette.Muted
	if selected {
		prefix, prefixColor = "❯ ", st.Palette.Accent
	}
	label := name
	if strings.EqualFold(name, st.ActiveName) {
		label += " (active)"
	}
	writer.write(prefix, prefixColor, selected)
	writer.write(fitLine(label, labelWidth), st.Palette.Foreground, selected)

	if writer.remaining() >= themeSwatchWidth()+2 {
		writer.write("  ", st.Palette.Muted, false)
		palette, _ := theme.ResolvePalette(name, st.Custom)
		for _, color := range themeSwatchColors(palette) {
			if strings.TrimSpace(color) == "" {
				writer.write(themeSwatchEmptyCell, st.Palette.Muted, false)
				continue
			}
			writer.write(themeSwatchCell, color, false)
		}
	}
	return writer.String()
}

// themeSwatchColors samples each palette in a fixed order so a swatch column
// means the same thing on every row.
func themeSwatchColors(palette theme.Palette) []string {
	return []string{
		palette.Accent,
		palette.Foreground,
		palette.Muted,
		palette.Success,
		palette.Warning,
		palette.Error,
		palette.Border,
	}
}

func themeSwatchWidth() int {
	return len(themeSwatchColors(theme.Palette{})) * ansi.StringWidth(themeSwatchCell)
}

// themeLabelWidth pads every name to a shared column so the swatch strips start
// at the same offset regardless of name length.
func themeLabelWidth(names []string, activeName string) int {
	widest := 0
	for _, name := range names {
		width := ansi.StringWidth(name)
		if strings.EqualFold(name, activeName) {
			width += ansi.StringWidth(" (active)")
		}
		widest = max(widest, width)
	}
	return widest
}

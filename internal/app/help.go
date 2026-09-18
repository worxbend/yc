package app

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/worxbend/yc/internal/theme"
)

// The help strip is generated from the keymap table rather than hand-written,
// which is the only reason a help footer stays true a year after it was
// written. Every line here comes in as text that internal/app/keymap.go
// produced from the same table the update loop dispatches on.

const (
	// helpTinyWidth and helpNarrowWidth are where the footer stops naming
	// keys in full and starts naming only the two that get someone unstuck.
	helpTinyWidth   = 20
	helpNarrowWidth = 38
	// helpSourceWidth is where there is room to name the chat source
	// (mock, live, fake) alongside the bindings.
	helpSourceWidth = 112
)

// helpState is everything the strip draws.
type helpState struct {
	Palette theme.Palette
	// Expanded is the `?` toggle.
	Expanded bool
	// Compact is the one-line footer, generated from the keymap table.
	Compact string
	// Groups are the expanded help rows, one per key group, in the order
	// help presents them.
	Groups []string
	// NarrowGroups is the fallback for a terminal too narrow for the
	// generated rows: the same keys, fewer of them.
	NarrowGroups []string
	// Source names where chat is coming from, e.g. "mock chat source".
	Source string
}

// renderHelp draws the strip at the height the layout reserved.
//
// Each row is built key-token-bright, description-muted rather than one flat
// color: a hint is a lookup ("what does r do"), not prose, and the key is the
// part an eye scanning the row is after.
func renderHelp(width, height int, st helpState) string {
	lines := helpLines(width, height, st)
	rows := make([]string, len(lines))
	for i, line := range lines {
		prefix := ""
		if i == 0 && width >= 6 {
			// The keyboard glyph marks the strip as the key legend without
			// spending a word on saying so.
			prefix = "⌨ "
			line = strings.TrimLeft(line, " ")
		}
		rows[i] = styleHelpLine(prefix, line, width, st.Palette)
	}
	return lipgloss.NewStyle().
		Width(width).
		Background(lipgloss.Color(st.Palette.Surface)).
		Render(strings.Join(rows, "\n"))
}

// styleHelpLine renders one row of "Keys: Description | Keys: Description"
// (or, in the collapsed forms, bare key tokens with no description) to exactly
// width cells, painted on the strip's own surface so no cell is left showing
// the terminal's default.
func styleHelpLine(prefix, line string, width int, palette theme.Palette) string {
	writer := newPaneLineWriter(width, palette.Surface)
	if prefix != "" {
		writer.write(prefix, palette.Accent, true)
	}
	for i, part := range strings.Split(line, " | ") {
		if i > 0 {
			writer.write(" | ", palette.Muted, false)
		}
		writeHelpEntry(writer, part, palette)
	}
	return writer.String()
}

// writeHelpEntry writes one "Keys: Description" entry, or - when there is no
// ": " to split on, as in the compact footer's bare key tokens - the whole
// entry as a key.
func writeHelpEntry(writer *paneLineWriter, part string, palette theme.Palette) {
	key, description, ok := strings.Cut(part, ": ")
	writer.write(key, palette.Foreground, true)
	if ok {
		writer.write(": ", palette.Muted, false)
		writer.write(description, palette.Muted, false)
	}
}

// helpLines picks the form that fits.
//
// The collapsed forms name ctrl+p and tab before anything else: the palette
// documents every other key, and tab is how someone who cannot type into chat
// discovers that focus is a thing.
func helpLines(width, height int, st helpState) []string {
	source := st.Source
	if source == "" {
		source = "chat source"
	}
	if !st.Expanded {
		switch {
		case width < helpTinyWidth:
			return []string{" ^p | tab"}
		case width < helpNarrowWidth:
			return []string{" ctrl+p palette | tab focus"}
		}
		line := st.Compact
		if width >= helpSourceWidth {
			line += " | " + source
		}
		return []string{line}
	}

	lines := st.Groups
	if width < helpNarrowWidth && len(st.NarrowGroups) > 0 {
		lines = st.NarrowGroups
	}
	// Truncate before appending the source, so it lands on the last row that
	// actually survives. Giving it a row of its own would cost a whole line of
	// bindings on a short terminal; hanging it off whichever group happens to
	// be last costs nothing.
	if len(lines) > height {
		lines = lines[:height]
	}
	if len(lines) > 0 && width >= helpNarrowWidth {
		appended := append([]string(nil), lines...)
		appended[len(appended)-1] += " | " + source
		lines = appended
	}
	return lines
}

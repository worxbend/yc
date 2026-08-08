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
func renderHelp(width, height int, st helpState) string {
	lines := helpLines(width, height, st)
	// The keyboard glyph marks the strip as the key legend without spending
	// a word on saying so.
	if len(lines) > 0 && width >= 6 {
		lines[0] = "⌨ " + strings.TrimLeft(lines[0], " ")
	}
	for i := range lines {
		lines[i] = fitLine(lines[i], width)
	}
	return lipgloss.NewStyle().
		Width(width).
		Foreground(lipgloss.Color(st.Palette.Muted)).
		Background(lipgloss.Color(st.Palette.Surface)).
		Render(strings.Join(lines, "\n"))
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

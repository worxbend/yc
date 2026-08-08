package render

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// textWidth is the terminal cell width of a string.
//
// Every width decision in this package goes through it. Chat text carries
// emoji, combining marks, CJK, and zero-width joiners, so len() and rune counts
// are both wrong, and a single wrong measurement misaligns every row below it.
//
// It measures with ansi.StringWidth rather than uniseg.StringWidth because
// lipgloss composes the panes with ansi.StringWidth, and the two disagree about
// U+FE0F emoji-presentation sequences - keycaps above all, which uniseg calls
// one cell and ansi calls two. Budgeting a row by one measurement and drawing it
// with the other wraps the row and doubles the height of the whole frame, so the
// package that draws must be the package that measures.
func textWidth(value string) int {
	return ansi.StringWidth(value)
}

// graphemeStrings splits a string into grapheme clusters. Clusters are the only
// safe unit for slicing user-visible text: a flag, a keycap, and a skin-toned
// ZWJ sequence are each several runes that must move together.
func graphemeStrings(value string) []string {
	graphemes := uniseg.NewGraphemes(value)
	out := make([]string, 0, len(value))
	for graphemes.Next() {
		out = append(out, graphemes.Str())
	}
	return out
}

// takeCells returns the longest prefix of value that fits in limit cells,
// breaking only between grapheme clusters.
func takeCells(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var builder strings.Builder
	used := 0
	for _, cluster := range graphemeStrings(value) {
		width := textWidth(cluster)
		if used+width > limit {
			break
		}
		builder.WriteString(cluster)
		used += width
	}
	return builder.String()
}

// truncateCells shortens value to limit cells, marking the cut with an ellipsis
// when there is room for one.
func truncateCells(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if textWidth(value) <= limit {
		return value
	}
	if limit <= 3 {
		return takeCells(value, limit)
	}
	return takeCells(value, limit-3) + "..."
}

// fitCells forces value to exactly width cells, truncating or right-padding as
// needed. It is what makes a fixed-WidthCells fragment honest: the reserved
// columns are filled whatever the text turns out to be.
func fitCells(value string, width int) string {
	if width <= 0 {
		return ""
	}
	out := value
	if textWidth(out) > width {
		out = truncateCells(out, width)
	}
	if used := textWidth(out); used < width {
		out += strings.Repeat(" ", width-used)
	}
	return out
}

// sanitizeUserText strips terminal control sequences from API-supplied text.
//
// Display names, message bodies, sticker alt text, and membership level names
// are all attacker-controlled strings that yc prints straight to a terminal
// that is frequently on stream. An embedded CSI sequence could move the cursor,
// repaint the screen, or set a hyperlink, so escape sequences are removed
// before anything is measured - stripping afterward would desynchronize the
// width accounting from what is actually drawn.
//
// Newlines survive because the wrapper treats them as explicit row breaks;
// every other C0/C1 control becomes a space so the surrounding words stay
// separated rather than silently joining. Bidirectional overrides are dropped
// outright: they are zero width, so removing them cannot disturb the layout
// math, and leaving them in would let one chatter reorder the visible text of
// the rows around their own.
func sanitizeUserText(value string) string {
	if value == "" {
		return ""
	}
	stripped := ansi.Strip(value)
	if !strings.ContainsFunc(stripped, isUnsafeRune) {
		return stripped
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case isBidiControl(r):
			return -1
		case unicode.IsControl(r):
			return ' '
		default:
			return r
		}
	}, stripped)
}

func isUnsafeRune(r rune) bool {
	if r == '\n' {
		return false
	}
	return unicode.IsControl(r) || isBidiControl(r)
}

// isBidiControl reports the explicit bidirectional formatting characters. They
// occupy no cells but change the visual order of everything drawn after them.
func isBidiControl(r rune) bool {
	switch r {
	case '\u200e', '\u200f':
		return true
	case '\u202a', '\u202b', '\u202c', '\u202d', '\u202e':
		return true
	case '\u2066', '\u2067', '\u2068', '\u2069':
		return true
	default:
		return false
	}
}

// compactWhitespace collapses runs of whitespace into single spaces.
func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func emptyFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/theme"
)

// plainLines strips styling and splits a rendered block into rows. Every layout
// assertion works on the plain form: the styling is what changes when a theme
// changes, and the geometry is what must not.
func plainLines(rendered string) []string {
	if rendered == "" {
		return nil
	}
	return strings.Split(ansi.Strip(rendered), "\n")
}

func TestFitLineIsGraphemeAndWidthAware(t *testing.T) {
	tests := []struct {
		name  string
		value string
		width int
		want  string
	}{
		{name: "pads short text", value: "hi", width: 5, want: "hi   "},
		{name: "exact width", value: "hello", width: 5, want: "hello"},
		{name: "truncates", value: "hello world", width: 5, want: "hello"},
		// A double-width cluster that would overrun by one cell is dropped
		// whole rather than split, which is what keeps a pane from bleeding
		// one column into its neighbor.
		{name: "keeps wide cluster whole", value: "a👍b", width: 2, want: "a "},
		{name: "fits wide cluster", value: "a👍b", width: 3, want: "a👍"},
		// A flag is one grapheme cluster made of two runes; slicing by rune
		// would emit half a flag.
		{name: "keeps flag cluster whole", value: "🇯🇵x", width: 1, want: " "},
		{name: "zero width", value: "hello", width: 0, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := fitLine(test.value, test.width)
			if got != test.want {
				t.Fatalf("fitLine(%q, %d) = %q, want %q", test.value, test.width, got, test.want)
			}
			if width := ansi.StringWidth(got); test.width > 0 && width != test.width {
				t.Fatalf("fitLine(%q, %d) width = %d, want %d", test.value, test.width, width, test.width)
			}
		})
	}
}

func TestTailDisplayCellsKeepsTheCaretEnd(t *testing.T) {
	// The composer is append-only, so the end of the draft is the part that
	// must survive truncation.
	if got := tailDisplayCells("hello world", 5); got != "world" {
		t.Fatalf("tailDisplayCells = %q, want %q", got, "world")
	}
	if got := tailDisplayCells("a👍", 1); got != "" {
		t.Fatalf("tailDisplayCells dropped only part of a wide cluster: %q", got)
	}
	if got := tailDisplayCells("a👍", 2); got != "👍" {
		t.Fatalf("tailDisplayCells = %q, want %q", got, "👍")
	}
}

func TestRenderPaneHasExactDimensions(t *testing.T) {
	palette := theme.DefaultPalette()
	const width, contentHeight = 30, 4
	rendered := renderPane(paneSpec{
		palette:       palette,
		icon:          "💬",
		title:         "Chat",
		content:       strings.Join([]string{"one", "two", "three", "four"}, "\n"),
		width:         width,
		contentHeight: contentHeight,
		padding:       1,
		accent:        palette.Accent,
	})

	lines := plainLines(rendered)
	// Title row, contentHeight body rows, and the bottom border.
	if want := contentHeight + 2; len(lines) != want {
		t.Fatalf("pane has %d rows, want %d", len(lines), want)
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != width {
			t.Fatalf("row %d width = %d, want %d (%q)", i, got, width, line)
		}
	}
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") {
		t.Fatalf("title row is not a border row: %q", lines[0])
	}
	if !strings.Contains(lines[0], "Chat") {
		t.Fatalf("title row omits the title: %q", lines[0])
	}
}

func TestRenderPaneTitleTruncatesRatherThanOverflowing(t *testing.T) {
	palette := theme.DefaultPalette()
	rendered := renderPane(paneSpec{
		palette:       palette,
		icon:          "💬",
		title:         strings.Repeat("very long title ", 10),
		content:       "",
		width:         20,
		contentHeight: 1,
	})
	for _, line := range plainLines(rendered) {
		if got := ansi.StringWidth(line); got != 20 {
			t.Fatalf("row width = %d, want 20 (%q)", got, line)
		}
	}
}

func TestRenderPaneDegradesAtImpossibleSizes(t *testing.T) {
	palette := theme.DefaultPalette()
	if got := renderPane(paneSpec{palette: palette, width: 0, contentHeight: 3}); got != "" {
		t.Fatalf("zero-width pane rendered %q, want empty", got)
	}
	// One column still produces a single styled cell rather than panicking.
	single := renderPane(paneSpec{palette: palette, width: 1, contentHeight: 0})
	if len(plainLines(single)) == 0 {
		t.Fatal("one-column pane rendered nothing")
	}
}

func TestPaneLineWriterNeverExceedsItsBudget(t *testing.T) {
	writer := newPaneLineWriter(10, "#000000")
	writer.write("abcdefgh", "#ffffff", false)
	writer.write("ijklmnop", "#ffffff", false)
	line := ansi.Strip(writer.String())
	if got := ansi.StringWidth(line); got != 10 {
		t.Fatalf("writer produced width %d, want 10 (%q)", got, line)
	}
	if line != "abcdefghij" {
		t.Fatalf("writer produced %q", line)
	}
}

func TestWindowStartCentersAndClamps(t *testing.T) {
	tests := []struct {
		name                    string
		selected, total, height int
		want                    int
	}{
		{name: "everything fits", selected: 3, total: 4, height: 10, want: 0},
		{name: "centers", selected: 10, total: 40, height: 6, want: 7},
		{name: "clamps to start", selected: 1, total: 40, height: 6, want: 0},
		{name: "clamps to end", selected: 39, total: 40, height: 6, want: 34},
		{name: "zero height", selected: 5, total: 40, height: 0, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := windowStart(test.selected, test.total, test.height); got != test.want {
				t.Fatalf("windowStart(%d, %d, %d) = %d, want %d",
					test.selected, test.total, test.height, got, test.want)
			}
		})
	}
}

func TestVisibleRowsIsBottomAnchored(t *testing.T) {
	rows := []string{"1", "2", "3", "4", "5"}
	if got := visibleRows(rows, 2, 0); strings.Join(got, "") != "45" {
		t.Fatalf("offset 0 = %v, want the newest rows", got)
	}
	if got := visibleRows(rows, 2, 2); strings.Join(got, "") != "23" {
		t.Fatalf("offset 2 = %v", got)
	}
	// An offset past the top clamps rather than returning nothing.
	if got := visibleRows(rows, 2, 99); strings.Join(got, "") != "12" {
		t.Fatalf("over-scroll = %v", got)
	}
}

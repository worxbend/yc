package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/theme"
)

func testHelpState() helpState {
	groups := make([]string, 0, len(keyGroupOrder))
	for _, group := range keyGroupOrder {
		groups = append(groups, helpGroupLine(group))
	}
	return helpState{
		Palette: theme.DefaultPalette(),
		Compact: compactHelpLine(),
		Groups:  groups,
		NarrowGroups: []string{
			" ctrl+p: commands",
			" i/esc | tab | jk",
			" ?: help | ctrl+c: quit",
		},
		Source: "mock chat source",
	}
}

func TestHelpHasExactDimensions(t *testing.T) {
	st := testHelpState()
	for _, width := range []int{10, 20, 38, 80, 120, 200} {
		for _, height := range []int{1, 2, 3, 4} {
			st.Expanded = height > 1
			lines := plainLines(renderHelp(width, height, st))
			if len(lines) > height {
				t.Fatalf("%dx%d rendered %d rows", width, height, len(lines))
			}
			for i, line := range lines {
				if got := ansi.StringWidth(line); got != width {
					t.Fatalf("%dx%d row %d is %d cells (%q)", width, height, i, got, line)
				}
			}
		}
	}
}

// The collapsed forms name ctrl+p and tab before anything else: the palette
// documents every other key, and tab is how someone who cannot type into chat
// discovers that focus is a thing.
func TestHelpDegradationKeepsTheEscapeHatches(t *testing.T) {
	st := testHelpState()
	tiny := ansiPlain(renderHelp(12, 1, st))
	if !strings.Contains(tiny, "^p") || !strings.Contains(tiny, "tab") {
		t.Fatalf("tiny help lost its escape hatches: %q", tiny)
	}
	narrow := ansiPlain(renderHelp(30, 1, st))
	if !strings.Contains(narrow, "ctrl+p") || !strings.Contains(narrow, "tab") {
		t.Fatalf("narrow help lost its escape hatches: %q", narrow)
	}
}

// The compact footer is generated from the keymap table, so it cannot advertise
// a key the keymap no longer has.
func TestHelpFooterComesFromTheKeymap(t *testing.T) {
	st := testHelpState()
	line := ansiPlain(renderHelp(120, 1, st))
	if !strings.Contains(line, "ctrl+p") {
		t.Fatalf("footer does not name the palette: %q", line)
	}
	if !strings.Contains(line, "mock chat source") {
		t.Fatalf("a wide footer does not name the chat source: %q", line)
	}
}

// Naming the chat source costs no row of its own: it hangs off whichever group
// line survives truncation, so a short terminal loses bindings rather than the
// answer to "where is this chat coming from".
func TestHelpSourceSurvivesTruncation(t *testing.T) {
	st := testHelpState()
	st.Expanded = true
	for _, height := range []int{1, 2, 3, 4} {
		lines := helpLines(120, height, st)
		if len(lines) != height {
			t.Fatalf("height %d produced %d rows", height, len(lines))
		}
		if !strings.Contains(lines[len(lines)-1], "mock chat source") {
			t.Fatalf("height %d lost the source: %q", height, lines[len(lines)-1])
		}
	}
}

func TestHelpMarksItselfAsTheKeyLegend(t *testing.T) {
	if line := ansiPlain(renderHelp(80, 1, testHelpState())); !strings.HasPrefix(line, "⌨") {
		t.Fatalf("help strip is not marked: %q", line)
	}
}

func ansiPlain(rendered string) string {
	lines := plainLines(rendered)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

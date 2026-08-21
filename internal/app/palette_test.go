package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/theme"
)

func testListOverlay() listOverlayState {
	return listOverlayState{
		Palette: theme.DefaultPalette(),
		Icon:    "⌘",
		Title:   "Command Palette",
		Accent:  theme.DefaultPalette().Accent,
		Header:  commandPaletteHeader(""),
		Items:   paletteCommandTitles(),
		Detail:  paletteCommandShortcut,
	}
}

func TestListOverlayHasExactDimensions(t *testing.T) {
	st := testListOverlay()
	for _, width := range []int{20, 60, 120} {
		lines := plainLines(renderListOverlay(width, dockedPane{height: 7, contentHeight: 5, framed: true}, st))
		if len(lines) != 7 {
			t.Fatalf("width %d rendered %d rows", width, len(lines))
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got != width {
				t.Fatalf("width %d row %d is %d cells (%q)", width, i, got, line)
			}
		}
	}
}

// The palette exists so a binding is not discoverable only by already knowing
// it, which means every row has to carry its key.
func TestCommandPaletteShowsShortcuts(t *testing.T) {
	st := testListOverlay()
	rendered := strings.Join(plainLines(renderListOverlay(100, dockedPane{height: 12, contentHeight: 10, framed: true}, st)), "\n")
	if !strings.Contains(rendered, "Open chat") || !strings.Contains(rendered, "space c") {
		t.Fatalf("palette row is missing its shortcut:\n%s", rendered)
	}
}

func TestListOverlayMarksTheSelection(t *testing.T) {
	st := testListOverlay()
	st.Selected = 2
	lines := plainLines(renderListOverlay(100, dockedPane{height: 12, contentHeight: 10, framed: true}, st))
	marked := 0
	for _, line := range lines {
		if strings.Contains(line, "❯") {
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("%d rows are marked as selected, want exactly 1", marked)
	}
}

func TestListOverlayWithNoMatchesSaysSo(t *testing.T) {
	st := testListOverlay()
	st.Items = nil
	st.Header = commandPaletteHeader("zzzz")
	rendered := strings.Join(plainLines(renderListOverlay(80, dockedPane{height: 7, contentHeight: 5, framed: true}, st)), "\n")
	if !strings.Contains(rendered, "no matches") {
		t.Fatalf("an empty result is silent:\n%s", rendered)
	}
	if !strings.Contains(rendered, "zzzz") {
		t.Fatalf("the query is not echoed:\n%s", rendered)
	}
}

// The typewriter reuses chat's own reveal machinery, so animation_mode=off
// produces a sequence that is already complete and the overlay appears at once
// with identical wording.
func TestPaletteRevealCompletesInstantlyWhenAnimationIsOff(t *testing.T) {
	lines := []string{" Command", "  Open chat", "  Close chat"}
	now := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)

	off := newPaletteReveal(lines, animation.ModeOff, now)
	if !off.Done() {
		t.Fatal("animation=off produced a running reveal")
	}
	st := testListOverlay()
	st.Reveal, st.RevealActive = off, true
	plain := strings.Join(listOverlayLines(60, 5, st), "\n")
	revealed := strings.Join(revealedOverlayLines(listOverlayLines(60, 5, st), 60, st), "\n")
	if plain != revealed {
		t.Fatal("a completed reveal changed the rendered lines")
	}

	fast := newPaletteReveal(lines, animation.ModeFast, now)
	if fast.Done() {
		t.Fatal("animation=fast produced an already-complete reveal")
	}
}

// A running reveal must not change the row count or the row widths: it only
// changes how much of each row has arrived.
func TestPaletteRevealPreservesGeometry(t *testing.T) {
	st := testListOverlay()
	st.Reveal = newPaletteReveal(listOverlayLines(60, 5, st), animation.ModeFast,
		time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC))
	st.RevealActive = true

	plain := listOverlayLines(60, 5, st)
	revealed := revealedOverlayLines(plain, 60, st)
	if len(plain) != len(revealed) {
		t.Fatalf("reveal changed the row count: %d vs %d", len(plain), len(revealed))
	}
	for i := range revealed {
		if got := ansi.StringWidth(revealed[i]); got != 60 {
			t.Fatalf("revealed row %d is %d cells", i, got)
		}
	}
}

func TestEmojiPickerHeaderEchoesTheQuery(t *testing.T) {
	if got := emojiPickerHeader(""); !strings.Contains(got, "type to search") {
		t.Fatalf("empty emoji header = %q", got)
	}
	if got := emojiPickerHeader("wave"); !strings.Contains(got, "wave") {
		t.Fatalf("emoji header does not echo the query: %q", got)
	}
}

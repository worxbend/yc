package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/theme"
)

func testThemePickerState() themePickerState {
	return themePickerState{
		Palette:    theme.DefaultPalette(),
		Names:      themePickerNames(),
		ActiveName: theme.DefaultPaletteName,
	}
}

func TestThemePickerFillsTheScreenExactly(t *testing.T) {
	st := testThemePickerState()
	sizes := []struct{ width, height int }{{1, 1}, {10, 4}, {40, 12}, {100, 30}, {200, 60}}
	for _, size := range sizes {
		lines := plainLines(renderThemePicker(size.width, size.height, st))
		if len(lines) != size.height {
			t.Fatalf("%dx%d rendered %d rows", size.width, size.height, len(lines))
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got != size.width {
				t.Fatalf("%dx%d row %d is %d cells (%q)", size.width, size.height, i, got, line)
			}
		}
	}
}

// The list doubles as a palette comparison: every row carries a swatch strip in
// that preset's own colors, so a theme can be judged without selecting it.
func TestThemePickerDrawsSwatchStrips(t *testing.T) {
	st := testThemePickerState()
	rendered := strings.Join(plainLines(renderThemePicker(120, 30, st)), "\n")
	if !strings.Contains(rendered, themeSwatchCell) {
		t.Fatalf("no swatches rendered:\n%s", rendered)
	}
	if !strings.Contains(rendered, "(active)") {
		t.Fatalf("the saved theme is not marked active:\n%s", rendered)
	}
}

// An unset custom slot must read as "nothing configured here" rather than
// painting a solid block in the terminal's default foreground, which would look
// like a real color.
func TestThemePickerMarksUnsetCustomSlots(t *testing.T) {
	st := testThemePickerState()
	st.Names = []string{"custom"}
	st.Custom = theme.Palette{}
	rendered := strings.Join(plainLines(renderThemePicker(120, 12, st)), "\n")
	if !strings.Contains(rendered, themeSwatchEmptyCell) {
		t.Fatalf("an empty custom palette drew solid swatches:\n%s", rendered)
	}
}

// The picker changes the running session and nothing else - no code path writes
// config.toml - so neither the header nor the footer may promise a save. The
// wording is pinned because "save" was there once and was a straightforward
// lie: the user restarted and lost the theme.
func TestThemePickerNeverPromisesToPersistTheChoice(t *testing.T) {
	st := testThemePickerState()
	rendered := strings.Join(plainLines(renderThemePicker(120, 20, st)), "\n")
	for _, promise := range []string{"save", "saved", "persist", "written"} {
		if strings.Contains(strings.ToLower(rendered), promise) {
			t.Fatalf("the theme picker claims %q, but it only changes the running session:\n%s",
				promise, rendered)
		}
	}
	if !strings.Contains(rendered, "for this run") {
		t.Fatalf("the header does not say the preview is session-only:\n%s", rendered)
	}
	if !strings.Contains(rendered, "enter apply") {
		t.Fatalf("the footer does not describe what enter actually does:\n%s", rendered)
	}
}

func TestThemePickerNamesEveryColumnConsistently(t *testing.T) {
	// The swatch order is fixed so a column means the same thing on every row.
	colors := themeSwatchColors(theme.DefaultPalette())
	if len(colors) != 7 {
		t.Fatalf("swatch strip has %d columns, want 7", len(colors))
	}
	if got := themeSwatchWidth(); got != len(colors)*ansi.StringWidth(themeSwatchCell) {
		t.Fatalf("themeSwatchWidth = %d", got)
	}
}

func TestThemeLabelWidthReservesRoomForTheActiveMarker(t *testing.T) {
	names := []string{"a", "claude"}
	plain := themeLabelWidth(names, "nothing")
	marked := themeLabelWidth(names, "claude")
	if marked <= plain {
		t.Fatalf("the active marker was not budgeted: %d vs %d", marked, plain)
	}
}

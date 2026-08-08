package app

import (
	"strings"
	"time"

	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/render"
	"github.com/worxbend/yc/internal/theme"
)

// The command palette is the discovery surface: every documented key also
// exists here as a searchable sentence, so a binding nobody remembers is still
// one ctrl+p away.
//
// The palette, the target picker, and the emoji picker are the same shape - a
// query line over a windowed list - so they share one renderer. Only the frame
// title, the accent role, and the per-row detail differ, which is what keeps
// three overlays from drifting into three slightly different list widgets.

// listOverlayState is everything a docked list overlay draws.
type listOverlayState struct {
	Palette theme.Palette
	// Icon, Title, and Accent identify the overlay in the pane frame.
	Icon   string
	Title  string
	Accent string
	// Header is the line above the list, already including the query.
	Header string
	// Items are the filtered rows, in the order the model's overlayItems
	// produced them, so the view and the key handler can never disagree
	// about what enter will commit.
	Items []string
	// Detail renders the right-hand column for a row - a key for a palette
	// command, a source for a target. Nil renders no detail.
	Detail   func(item string) string
	Selected int
	Phase    int

	// Reveal types the rows in. It is the shared animation.Sequence, so an
	// overlay opened with animation off is simply already complete and
	// appears instantly with identical wording and layout.
	Reveal       animation.Sequence
	RevealActive bool
}

// renderListOverlay draws one docked overlay.
func renderListOverlay(width, height, contentHeight int, framed bool, st listOverlayState) string {
	contentWidth := width
	if framed {
		contentWidth = clampMin(width-4, 1)
	}
	lines := listOverlayLines(contentWidth, contentHeight, st)
	lines = revealedOverlayLines(lines, contentWidth, st)
	content := strings.Join(lines, "\n")
	if !framed {
		return backgroundStyledLine(fitBlock(content, width, height), st.Palette.Surface)
	}
	accent := st.Accent
	if accent == "" {
		accent = st.Palette.Accent
	}
	return renderPane(paneSpec{
		palette:       st.Palette,
		icon:          st.Icon,
		title:         st.Title,
		content:       content,
		width:         width,
		contentHeight: contentHeight,
		padding:       1,
		accent:        accent,
		focused:       true,
		phase:         st.Phase,
	})
}

// listOverlayLines builds the plain body: the header and the windowed list.
func listOverlayLines(width, height int, st listOverlayState) []string {
	if height <= 0 {
		return nil
	}
	lines := []string{fitLine(st.Header, width)}
	if height == 1 {
		return lines
	}

	if len(st.Items) == 0 {
		lines = append(lines, fitLine("  no matches", width))
	} else {
		selected := st.Selected
		if selected < 0 || selected >= len(st.Items) {
			selected = 0
		}
		start := windowStart(selected, len(st.Items), height-1)
		for i := start; i < len(st.Items) && len(lines) < height; i++ {
			lines = append(lines, fitLine(listOverlayRow(st, st.Items[i], i == selected), width))
		}
	}
	for len(lines) < height {
		lines = append(lines, fitLine("", width))
	}
	return lines[:height]
}

func listOverlayRow(st listOverlayState, item string, selected bool) string {
	prefix := "  "
	if selected {
		prefix = "❯ "
	}
	row := prefix + item
	if st.Detail != nil {
		if detail := st.Detail(item); detail != "" {
			row += "  " + detail
		}
	}
	return row
}

// revealedOverlayLines replaces the plain body with the in-progress reveal
// frame while one is running.
//
// The sequence holds render.Row values, so the frame comes back as rows and is
// flattened here. Feeding the palette through the same machinery as chat means
// the typewriter honors the same queue bound, the same interval, and the same
// off switch, rather than being a second animation with its own rules.
func revealedOverlayLines(lines []string, width int, st listOverlayState) []string {
	if !st.RevealActive || st.Reveal.Done() {
		return lines
	}
	frame := st.Reveal.Frame()
	if len(frame) == 0 {
		return lines
	}
	revealed := make([]string, 0, len(lines))
	for index := range lines {
		if index >= len(frame) {
			revealed = append(revealed, fitLine("", width))
			continue
		}
		revealed = append(revealed, fitLine(frame[index].Plain(), width))
	}
	return revealed
}

// paletteLinesToRows converts plain overlay lines into reveal rows. Each line
// becomes a single text fragment, which is what makes the reveal type character
// by character rather than token by token.
func paletteLinesToRows(lines []string) []render.Row {
	rows := make([]render.Row, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, render.Row{Fragments: []render.Fragment{{
			Kind: render.FragmentText,
			Text: line,
		}}})
	}
	return rows
}

// newPaletteReveal starts the typewriter for a freshly rendered overlay body.
// A caller in ModeOff gets a sequence that is already complete.
func newPaletteReveal(lines []string, mode animation.Mode, now time.Time) animation.Sequence {
	cfg := animation.DefaultConfig()
	cfg.Mode = mode
	return animation.NewSequence(paletteLinesToRows(lines), cfg, now)
}

// commandPaletteHeader renders the palette's query line.
func commandPaletteHeader(query string) string {
	if strings.TrimSpace(query) == "" {
		return " Command"
	}
	return " Command: " + query
}

// emojiPickerHeader renders the emoji picker's query line. The picker needs no
// credentials and no network: YouTube exposes no per-message emote metadata, so
// the built-in Unicode catalog is the whole graphical vocabulary.
func emojiPickerHeader(query string) string {
	if strings.TrimSpace(query) == "" {
		return " Emoji — type to search, enter inserts"
	}
	return " Emoji: " + query
}

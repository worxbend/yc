package app

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/theme"
)

// Chrome text effects. internal/animation returns styled cells and owns no
// terminal I/O; this file is the only place those cells become escape
// sequences, and it always stamps an explicit background on them.
//
// Every effect preserves its label's display width on every frame, so an
// animated title can never reflow the pane it sits in, and animation_mode=off
// collapses each effect to its static first frame with identical wording and
// geometry.

// textEffectConfig builds a palette-aware config for one animated label. The
// colors and the app-wide animation mode come from one place so a theme change
// or animation=off applies everywhere without per-call plumbing; callers
// override Step, Width, Offset, and Bold for their own surface.
func textEffectConfig(palette theme.Palette, mode animation.Mode, effect animation.TextEffect) animation.TextConfig {
	return animation.TextConfig{
		Effect: effect,
		Mode:   mode,
		Base:   palette.Foreground,
		Accent: palette.Accent,
		Trail:  palette.Muted,
	}
}

// animatedText renders one frame of an effect as a styled string on
// background.
//
// Pass the label itself, truncated with revealDisplayCells rather than padded
// with fitLine, and pad the result with centeredEffectLine or paddedEffectLine.
// Animating a padded row would spread the effect across the blank cells
// instead of the words.
func animatedText(text string, cfg animation.TextConfig, elapsed time.Duration, background string) string {
	return renderTextCells(animation.TextFrame(text, cfg, elapsed), background)
}

// renderTextCells paints animation cells onto an explicit background. Each cell
// carries the background itself because a styled span ends in an ANSI reset:
// without it the terminal's own background shows through between colored runs.
func renderTextCells(cells []animation.TextCell, background string) string {
	var builder strings.Builder
	for _, cell := range cells {
		style := lipgloss.NewStyle().Background(lipgloss.Color(background)).Bold(cell.Bold)
		if cell.Foreground != "" {
			style = style.Foreground(lipgloss.Color(cell.Foreground))
		}
		builder.WriteString(style.Render(cell.Text))
	}
	return builder.String()
}

// centeredEffectLine centers already-styled effect output inside width and
// paints the padding with background. Padding a styled span with plain spaces
// would leave unstyled cells between the span's reset and the next escape.
func centeredEffectLine(styled string, width int, background string) string {
	pad := width - lipgloss.Width(styled)
	if pad <= 0 {
		return styled
	}
	left := pad / 2
	return backgroundSpaces(left, background) + styled + backgroundSpaces(pad-left, background)
}

// paddedEffectLine extends styled effect output to width with background cells,
// the left-aligned counterpart to centeredEffectLine. Rows sharing a pane with
// fitLine-padded static rows need it so the surface reaches the pane edge.
func paddedEffectLine(styled string, width int, background string) string {
	return styled + backgroundSpaces(width-lipgloss.Width(styled), background)
}

// pulseLabel blinks a label by prefixing a dot on the off beat rather than by
// blanking it. The word stays readable at every phase and, crucially, the
// segment's width changes by exactly one cell in a place the status bar has
// already budgeted for, so nothing downstream reflows.
func pulseLabel(label string, on bool) string {
	if on {
		return label
	}
	return "·" + label
}

// pulseOn derives the blink phase from the shared frame clock. The zero time -
// animation off, or before the first tick - reports the lit phase, so a static
// frame shows the label at full strength instead of half-faded.
func pulseOn(mode animation.Mode, now time.Time, interval time.Duration) bool {
	if mode == animation.ModeOff || now.IsZero() || interval <= 0 {
		return true
	}
	return (now.UnixNano()/int64(interval))%2 == 0
}

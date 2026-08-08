package app

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/theme"
)

// Gradient chrome: the tab bar, focused pane rails, and animating message
// rails all rotate a color cycle by a phase derived from the shared frame
// clock. Nothing here starts a ticker or reads the wall clock, so View stays
// pure and every frame is reproducible from the last animation.FrameMsg.

const (
	// gradientFrameMillis is how long one phase step lasts. 200ms is slow
	// enough to read as drift rather than flicker on a 10fps frame clock.
	gradientFrameMillis = 200
	// gradientReducedFrameMillis halves the apparent speed in reduced-motion
	// mode without changing the effect's wording or geometry.
	gradientReducedFrameMillis = 400
)

// gradientPhase converts the last frame time into an offset into a width-long
// color cycle.
//
// It returns 0 both when animation is off and before the first frame tick, so
// an animated surface and its static frame have identical geometry - the only
// difference is that the colors stop moving.
func gradientPhase(mode animation.Mode, lastFrameAt time.Time, width int) int {
	if width <= 0 || mode == animation.ModeOff || lastFrameAt.IsZero() {
		return 0
	}
	frameMillis := int64(gradientFrameMillis)
	if mode == animation.ModeReduced {
		frameMillis = gradientReducedFrameMillis
	}
	phase := int(lastFrameAt.UnixMilli()/frameMillis) % width
	if phase < 0 {
		phase += width
	}
	return phase
}

// gradientEndColor keeps decorative gradients visible on themes that reuse one
// color for several roles. A palette that is genuinely monochrome stays solid
// rather than being silently colorized: the absence of hue is a choice.
func gradientEndColor(palette theme.Palette) string {
	if !strings.EqualFold(palette.Accent, palette.Success) {
		return palette.Success
	}
	if !strings.EqualFold(palette.Accent, palette.Foreground) {
		return palette.Warning
	}
	return palette.Success
}

// gradientBackgroundLine paints value across a width-long background gradient,
// contrast-correcting the text color against each cell so the label stays
// legible everywhere the gradient goes. This is the tab bar.
func gradientBackgroundLine(value string, width int, start, end, preferredForeground, fallbackForeground string, phase int, bold bool) string {
	if width <= 0 {
		return ""
	}
	plain := fitLine(value, width)
	colors := theme.SeamlessGradient(start, end, width)
	if len(colors) == 0 {
		return plain
	}
	var builder strings.Builder
	cell := 0
	graphemes := uniseg.NewGraphemes(plain)
	for graphemes.Next() {
		cluster := graphemes.Str()
		color := colors[(cell+phase)%len(colors)]
		foreground := theme.ContrastCorrectedForeground(preferredForeground, color, fallbackForeground)
		builder.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(foreground)).
			Background(lipgloss.Color(color)).
			Bold(bold).
			Render(cluster))
		cell += ansi.StringWidth(cluster)
	}
	return builder.String()
}

// gradientForegroundText drifts a gradient through the glyphs of value while
// leaving the background flat. This is the focused pane title and the splash
// logo: the words carry the motion, the surface stays still.
func gradientForegroundText(value, start, end, background string, phase int, bold bool) string {
	width := ansi.StringWidth(value)
	if width <= 0 {
		return ""
	}
	colors := theme.SeamlessGradient(start, end, width)
	if len(colors) == 0 {
		return paneStyledText(value, start, background, bold)
	}
	var builder strings.Builder
	cell := 0
	graphemes := uniseg.NewGraphemes(value)
	for graphemes.Next() {
		cluster := graphemes.Str()
		color := colors[(cell+phase)%len(colors)]
		builder.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(color)).
			Background(lipgloss.Color(background)).
			Bold(bold).
			Render(cluster))
		cell += ansi.StringWidth(cluster)
	}
	return builder.String()
}

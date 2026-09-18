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

// gradientSpan is one run of a gradient-background line that keeps its own
// emphasis. The gradient itself still runs unbroken across every span, so the
// bar reads as one continuous surface; only the boldness marks which run is
// the active tab.
type gradientSpan struct {
	text string
	bold bool
}
type gradientSpans []gradientSpan

// joinPlain concatenates every span's text with no styling, for measuring and
// fitting the line as a whole before any per-span emphasis is applied.
func (spans gradientSpans) joinPlain() string {
	var builder strings.Builder
	for _, span := range spans {
		builder.WriteString(span.text)
	}
	return builder.String()
}

// width reports the pre-fit display width of every span combined.
func (spans gradientSpans) width() int {
	total := 0
	for _, span := range spans {
		total += ansi.StringWidth(span.text)
	}
	return total
}

// boldMask reports, per display cell of the pre-fit concatenation, whether
// that cell belongs to a bold span. fitLine only ever truncates from the
// right and never reorders clusters, so a cell index into the fitted line is
// also a valid index into this mask.
func (spans gradientSpans) boldMask() []bool {
	mask := make([]bool, 0, spans.width())
	for _, span := range spans {
		for range ansi.StringWidth(span.text) {
			mask = append(mask, span.bold)
		}
	}
	return mask
}

// gradientBackgroundSpans paints a line of spans across a width-long
// background gradient, contrast-correcting the text color against each cell so
// the label stays legible everywhere the gradient goes, while each span keeps
// its own emphasis. This is the tab bar: the active tab reads as active
// without breaking the bar's single continuous gradient.
func gradientBackgroundSpans(spans gradientSpans, width int, start, end, preferredForeground, fallbackForeground string, phase int) string {
	if width <= 0 {
		return ""
	}
	plain := fitLine(spans.joinPlain(), width)
	mask := spans.boldMask()
	colors := theme.SeamlessGradient(start, end, width)
	var builder strings.Builder
	cell := 0
	graphemes := uniseg.NewGraphemes(plain)
	for graphemes.Next() {
		cluster := graphemes.Str()
		color := fallbackForeground
		if len(colors) > 0 {
			color = colors[(cell+phase)%len(colors)]
		}
		foreground := theme.ContrastCorrectedForeground(preferredForeground, color, fallbackForeground)
		bold := cell < len(mask) && mask[cell]
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

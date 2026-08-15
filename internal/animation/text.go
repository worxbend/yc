package animation

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/worxbend/yc/internal/theme"
)

// TextEffect selects a chrome label animation. These apply to chrome only -
// chat message text uses Sequence, which keeps chat readable and scroll-stable.
type TextEffect string

const (
	TextEffectNone TextEffect = "none"
	// TextEffectTypewriter reveals clusters left to right behind a cursor.
	TextEffectTypewriter TextEffect = "typewriter"
	// TextEffectGradientWave sweeps a color ramp across the label.
	TextEffectGradientWave TextEffect = "gradient-wave"
	// TextEffectShimmer moves a bright band across a muted base.
	TextEffectShimmer TextEffect = "shimmer"
	// TextEffectBounce moves a short trail back and forth.
	TextEffectBounce TextEffect = "bounce"
)

// DefaultCursor is the typewriter cursor glyph. It must be a single-cell glyph
// so the cursor can stand in for an unrevealed cluster without changing width.
const DefaultCursor = "▌"

const (
	// shimmerBand is how many columns either side of the highlight head
	// still carry some of the accent.
	shimmerBand = 3
	// shimmerRest is how far past the label's end the head keeps traveling,
	// which reads as a pause between passes rather than a strobe.
	shimmerRest = 6
	// bounceTrail is how many fading ghosts follow the bouncing label.
	bounceTrail = 3
)

// TextCell is one styled output cell.
//
// Effects return cells rather than escape sequences, which is what keeps this
// package free of terminal I/O and lets internal/app paint them over an
// explicit background.
type TextCell struct {
	Text       string
	Foreground string
	Bold       bool
}

// TextConfig configures one label effect.
type TextConfig struct {
	Effect TextEffect
	Mode   Mode
	Base   string
	Accent string
	Trail  string
	Cursor string
	Step   time.Duration
	// Width is the label's reserved width. Every effect must preserve it on
	// every frame - a typewriter pads unrevealed clusters with spaces of the
	// right width - so an animated label can never reflow the pane around it.
	Width  int
	Offset int
	Bold   bool
}

// FrameElapsed converts a frame timestamp into the elapsed value effects take.
// A zero time yields the static first frame, which is what makes View a pure
// function of already-ticked state.
func FrameElapsed(now time.Time) time.Duration {
	if now.IsZero() {
		return 0
	}
	return time.Duration(now.UnixNano())
}

// TextFrame renders text under cfg at elapsed, as styled cells.
//
// Negative elapsed is a not-yet-started typewriter, which renders as blank
// cells of the right width; the continuous effects treat it as zero.
func TextFrame(text string, cfg TextConfig, elapsed time.Duration) []TextCell {
	cfg = cfg.withTextDefaults()
	units := textClusters(text)
	if len(units) == 0 {
		return nil
	}
	if cfg.Mode == ModeOff || cfg.Step <= 0 {
		return mergeTextCells(staticTextCells(units, cfg))
	}

	switch cfg.Effect {
	case TextEffectTypewriter:
		return mergeTextCells(typewriterCells(units, cfg, elapsed))
	case TextEffectGradientWave:
		return mergeTextCells(gradientWaveCells(units, cfg, elapsed))
	case TextEffectShimmer:
		return mergeTextCells(shimmerCells(units, cfg, elapsed))
	case TextEffectBounce:
		return mergeTextCells(bounceCells(units, cfg, elapsed))
	default:
		return mergeTextCells(staticTextCells(units, cfg))
	}
}

// TextDone reports whether a typewriter has finished. It is meaningless for the
// continuous effects, which are periodic and never finish.
func TextDone(text string, cfg TextConfig, elapsed time.Duration) bool {
	cfg = cfg.withTextDefaults()
	if cfg.Mode == ModeOff || cfg.Step <= 0 || cfg.Effect == TextEffectNone {
		return true
	}
	if cfg.Effect != TextEffectTypewriter {
		return false
	}
	total := len(textClusters(text))
	return revealedClusters(total, cfg, elapsed) >= total
}

// TextPlain returns the unstyled text of a cell run.
func TextPlain(cells []TextCell) string {
	var builder strings.Builder
	for _, cell := range cells {
		builder.WriteString(cell.Text)
	}
	return builder.String()
}

// TextWidth returns the display width of a cell run.
func TextWidth(cells []TextCell) int {
	return ansi.StringWidth(TextPlain(cells))
}

// textCluster is one grapheme cluster and the number of terminal columns it
// occupies. Effects work in columns, never bytes, so a flag or a ZWJ sequence
// keeps its shape across every frame.
type textCluster struct {
	text  string
	width int
}

func textClusters(text string) []textCluster {
	if text == "" {
		return nil
	}
	units := make([]textCluster, 0, len(text))
	graphemes := uniseg.NewGraphemes(text)
	for graphemes.Next() {
		cluster := graphemes.Str()
		units = append(units, textCluster{text: cluster, width: ansi.StringWidth(cluster)})
	}
	return units
}

func clustersWidth(units []textCluster) int {
	width := 0
	for _, unit := range units {
		width += unit.width
	}
	return width
}

func staticTextCells(units []textCluster, cfg TextConfig) []TextCell {
	cells := make([]TextCell, 0, len(units))
	for _, unit := range units {
		cells = append(cells, TextCell{Text: unit.text, Foreground: cfg.Base, Bold: cfg.Bold})
	}
	return cells
}

func revealedClusters(total int, cfg TextConfig, elapsed time.Duration) int {
	if elapsed <= 0 || cfg.Step <= 0 {
		return 0
	}
	return min(int(elapsed/cfg.Step), total)
}

// typewriterCells reveals the label from the left and pads the rest with
// blanks. Padding rather than truncating is what keeps a centered tagline from
// sliding sideways as it types.
func typewriterCells(units []textCluster, cfg TextConfig, elapsed time.Duration) []TextCell {
	revealed := revealedClusters(len(units), cfg, elapsed)
	cursor := elapsed >= 0 && revealed < len(units) && cursorVisible(cfg, elapsed)
	cells := make([]TextCell, 0, len(units))
	for index, unit := range units {
		switch {
		case index < revealed:
			cells = append(cells, TextCell{Text: unit.text, Foreground: cfg.Base, Bold: cfg.Bold})
		case index == revealed && cursor:
			cells = append(cells, cursorCell(unit.width, cfg))
		default:
			cells = append(cells, TextCell{Text: strings.Repeat(" ", unit.width), Foreground: cfg.Base})
		}
	}
	return cells
}

// cursorVisible blinks the cursor on a multiple of the reveal step so the blink
// rate scales with typing speed instead of needing its own timer.
func cursorVisible(cfg TextConfig, elapsed time.Duration) bool {
	return (elapsed/(cfg.Step*4))%2 == 0
}

func cursorCell(width int, cfg TextConfig) TextCell {
	text := cfg.Cursor
	if pad := width - ansi.StringWidth(cfg.Cursor); pad > 0 {
		text += strings.Repeat(" ", pad)
	}
	return TextCell{Text: text, Foreground: cfg.Accent, Bold: true}
}

func gradientWaveCells(units []textCluster, cfg TextConfig, elapsed time.Duration) []TextCell {
	colors := theme.SeamlessGradient(cfg.Base, cfg.Accent, clustersWidth(units))
	if len(colors) == 0 {
		return staticTextCells(units, cfg)
	}
	phase := phaseColumns(elapsed, cfg)
	cells := make([]TextCell, 0, len(units))
	column := 0
	for _, unit := range units {
		cells = append(cells, TextCell{
			Text:       unit.text,
			Foreground: colors[wrapIndex(column+phase, len(colors))],
			Bold:       cfg.Bold,
		})
		column += unit.width
	}
	return cells
}

// shimmerCells sweeps a highlight head across the label and keeps traveling
// past the end for shimmerRest columns.
func shimmerCells(units []textCluster, cfg TextConfig, elapsed time.Duration) []TextCell {
	width := clustersWidth(units)
	head := wrapIndex(phaseColumns(elapsed, cfg), width+shimmerRest)
	cells := make([]TextCell, 0, len(units))
	column := 0
	for _, unit := range units {
		distance := column - head
		if distance < 0 {
			distance = -distance
		}
		cell := TextCell{Text: unit.text, Foreground: cfg.Base, Bold: cfg.Bold}
		if distance <= shimmerBand {
			intensity := 1 - float64(distance)/float64(shimmerBand+1)
			cell.Foreground = theme.Mix(cfg.Base, cfg.Accent, intensity)
			cell.Bold = cfg.Bold || distance == 0
		}
		cells = append(cells, cell)
		column += unit.width
	}
	return cells
}

// bounceCells draws the label at its current position on the track, preceded by
// progressively fainter ghosts of the positions it just left. Ghosts are drawn
// before the label so the label always wins any overlap.
func bounceCells(units []textCluster, cfg TextConfig, elapsed time.Duration) []TextCell {
	width := clustersWidth(units)
	track := max(cfg.Width, width)
	travel := track - width
	if travel <= 0 {
		return staticTextCells(units, cfg)
	}

	position, direction := pingPong(phaseColumns(elapsed, cfg), travel)
	row := newColumnTrack(track, cfg.Base)
	for ghost := bounceTrail; ghost >= 1; ghost-- {
		at := position - direction*ghost
		if at < 0 || at > travel {
			continue
		}
		intensity := float64(bounceTrail-ghost+1) / float64(bounceTrail+1)
		row.draw(at, units, theme.Mix(cfg.Trail, cfg.Base, intensity), false)
	}
	row.draw(position, units, cfg.Base, cfg.Bold)
	return row.cells()
}

// pingPong maps a monotonic step count onto a position in [0,travel] that
// reverses at each end, plus the direction it is currently moving.
func pingPong(steps, travel int) (int, int) {
	if travel <= 0 {
		return 0, 1
	}
	position := wrapIndex(steps, travel*2)
	if position <= travel {
		return position, 1
	}
	return travel*2 - position, -1
}

// columnTrack is a fixed-width row of terminal cells. Wide clusters occupy
// their first column and mark the rest as continuations, so overlapping draws
// can never widen or narrow the rendered track.
type columnTrack struct {
	columns []TextCell
	filled  []bool
	base    string
}

func newColumnTrack(width int, base string) *columnTrack {
	track := &columnTrack{
		columns: make([]TextCell, width),
		filled:  make([]bool, width),
		base:    base,
	}
	for i := range track.columns {
		track.columns[i] = TextCell{Text: " ", Foreground: base}
	}
	return track
}

func (t *columnTrack) draw(column int, units []textCluster, foreground string, bold bool) {
	for _, unit := range units {
		if column >= len(t.columns) {
			return
		}
		if column >= 0 {
			t.clearContinuation(column)
			t.columns[column] = TextCell{Text: unit.text, Foreground: foreground, Bold: bold}
			t.filled[column] = false
			for offset := 1; offset < unit.width && column+offset < len(t.columns); offset++ {
				t.clearContinuation(column + offset)
				t.columns[column+offset] = TextCell{}
				t.filled[column+offset] = true
			}
		}
		column += unit.width
	}
}

// clearContinuation replaces the wide cluster that owns column with blanks so a
// partially overwritten cluster cannot leave a dangling extra cell.
func (t *columnTrack) clearContinuation(column int) {
	if !t.filled[column] {
		return
	}
	for owner := column - 1; owner >= 0; owner-- {
		if t.filled[owner] {
			continue
		}
		width := ansi.StringWidth(t.columns[owner].Text)
		for offset := 0; offset < width && owner+offset < len(t.columns); offset++ {
			t.columns[owner+offset] = TextCell{Text: " ", Foreground: t.base}
			t.filled[owner+offset] = false
		}
		return
	}
}

func (t *columnTrack) cells() []TextCell {
	cells := make([]TextCell, 0, len(t.columns))
	for index, cell := range t.columns {
		if t.filled[index] {
			continue
		}
		cells = append(cells, cell)
	}
	return cells
}

func phaseColumns(elapsed time.Duration, cfg TextConfig) int {
	if cfg.Step <= 0 {
		return cfg.Offset
	}
	if elapsed < 0 {
		elapsed = 0
	}
	return int(elapsed/cfg.Step) + cfg.Offset
}

func wrapIndex(value, length int) int {
	if length <= 0 {
		return 0
	}
	value %= length
	if value < 0 {
		value += length
	}
	return value
}

// mergeTextCells joins neighboring cells that share styling so a frame emits
// one escape sequence per visual run instead of one per grapheme cluster.
func mergeTextCells(cells []TextCell) []TextCell {
	merged := make([]TextCell, 0, len(cells))
	for _, cell := range cells {
		if cell.Text == "" {
			continue
		}
		last := len(merged) - 1
		if last >= 0 && merged[last].Foreground == cell.Foreground && merged[last].Bold == cell.Bold {
			merged[last].Text += cell.Text
			continue
		}
		merged = append(merged, cell)
	}
	return merged
}

func (c TextConfig) withTextDefaults() TextConfig {
	if c.Effect == "" {
		c.Effect = TextEffectNone
	}
	switch c.Mode {
	case ModeOff, ModeReduced, ModeFast:
	default:
		c.Mode = ModeFast
	}
	if c.Cursor == "" {
		c.Cursor = DefaultCursor
	}
	if c.Trail == "" {
		c.Trail = c.Base
	}
	if c.Step <= 0 {
		c.Step = defaultTextStep(c.Effect, c.Mode)
	}
	return c
}

// defaultTextStep keeps every effect readable at the shared frame clock's rate.
// Reduced mode roughly halves the motion instead of disabling it, which matches
// how the reveal queue treats the same setting.
func defaultTextStep(effect TextEffect, mode Mode) time.Duration {
	reduced := mode == ModeReduced
	switch effect {
	case TextEffectTypewriter:
		if reduced {
			return 90 * time.Millisecond
		}
		return 45 * time.Millisecond
	case TextEffectGradientWave:
		if reduced {
			return 200 * time.Millisecond
		}
		return 100 * time.Millisecond
	case TextEffectShimmer:
		if reduced {
			return 160 * time.Millisecond
		}
		return 80 * time.Millisecond
	case TextEffectBounce:
		if reduced {
			return 180 * time.Millisecond
		}
		return 90 * time.Millisecond
	default:
		return 0
	}
}

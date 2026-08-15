package animation

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const (
	testBase   = "#8b8b8b"
	testAccent = "#ff2d46"
)

func textConfig(effect TextEffect) TextConfig {
	return TextConfig{
		Effect: effect,
		Mode:   ModeFast,
		Base:   testBase,
		Accent: testAccent,
		Trail:  "#3a3a3a",
	}
}

// Width stability is the property every yc surface depends on: an animated
// label sits inside an already-sized pane, so a frame that grows or shrinks
// would reflow the surface around it.
func TestTextFramePreservesDisplayWidthAcrossFrames(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
	}{
		{name: "ascii", text: "yc // YouTube live chat", width: 23},
		{name: "wide clusters", text: "chat 💬 ready 👀", width: 16},
		{name: "combining marks", text: "éclair", width: 6},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ansi.StringWidth(testCase.text); got != testCase.width {
				t.Fatalf("fixture width = %d, want %d", got, testCase.width)
			}
			for _, effect := range []TextEffect{TextEffectNone, TextEffectTypewriter, TextEffectGradientWave, TextEffectShimmer} {
				for step := -2; step < 40; step++ {
					elapsed := time.Duration(step) * 60 * time.Millisecond
					cells := TextFrame(testCase.text, textConfig(effect), elapsed)
					if got := TextWidth(cells); got != testCase.width {
						t.Fatalf("%s width at %s = %d, want %d (%q)", effect, elapsed, got, testCase.width, TextPlain(cells))
					}
				}
			}
		})
	}
}

func TestTextFrameBouncePreservesTrackWidth(t *testing.T) {
	cfg := textConfig(TextEffectBounce)
	cfg.Width = 16
	for step := range 60 {
		cells := TextFrame("◆", cfg, time.Duration(step)*90*time.Millisecond)
		if got := TextWidth(cells); got != 16 {
			t.Fatalf("bounce width at step %d = %d, want 16 (%q)", step, got, TextPlain(cells))
		}
	}
}

func TestTypewriterRevealsClustersInOrderAndSettles(t *testing.T) {
	cfg := textConfig(TextEffectTypewriter)
	cfg.Step = 50 * time.Millisecond
	const text = "hello 💬"

	blank := TextPlain(TextFrame(text, cfg, -time.Second))
	if strings.TrimSpace(blank) != "" {
		t.Fatalf("frame before the effect starts = %q, want blank", blank)
	}

	revealed := TextPlain(TextFrame(text, cfg, 150*time.Millisecond))
	if !strings.HasPrefix(revealed, "hel") {
		t.Fatalf("frame after 3 steps = %q, want it to start with %q", revealed, "hel")
	}
	if strings.Contains(revealed, "💬") {
		t.Fatalf("frame after 3 steps = %q, want the trailing emoji still hidden", revealed)
	}

	// The emoji is one cluster, so it lands whole rather than as a partial
	// two-column write.
	done := TextFrame(text, cfg, time.Second)
	if got := TextPlain(done); got != text {
		t.Fatalf("settled frame = %q, want %q", got, text)
	}
	if !TextDone(text, cfg, time.Second) {
		t.Fatal("TextDone() = false for a fully revealed typewriter, want true")
	}
	if TextDone(text, cfg, 150*time.Millisecond) {
		t.Fatal("TextDone() = true mid-reveal, want false")
	}
}

func TestTypewriterCursorBlinksAtTheRevealHead(t *testing.T) {
	cfg := textConfig(TextEffectTypewriter)
	cfg.Step = 50 * time.Millisecond
	cfg.Cursor = DefaultCursor

	var withCursor, withoutCursor bool
	for step := 1; step < 12; step++ {
		frame := TextPlain(TextFrame("hello world", cfg, time.Duration(step)*cfg.Step))
		if strings.Contains(frame, DefaultCursor) {
			withCursor = true
			continue
		}
		withoutCursor = true
	}
	if !withCursor || !withoutCursor {
		t.Fatalf("cursor blink: visible=%t hidden=%t, want both across the reveal", withCursor, withoutCursor)
	}
	if strings.Contains(TextPlain(TextFrame("hello world", cfg, time.Minute)), DefaultCursor) {
		t.Fatal("settled typewriter still shows a cursor, want it dropped")
	}
}

func TestGradientWaveRotatesColorsWithoutChangingText(t *testing.T) {
	cfg := textConfig(TextEffectGradientWave)
	cfg.Step = 100 * time.Millisecond
	const text = "No live chats open."

	first := TextFrame(text, cfg, 0)
	later := TextFrame(text, cfg, 400*time.Millisecond)
	if TextPlain(first) != text || TextPlain(later) != text {
		t.Fatalf("gradient wave altered the text: %q / %q", TextPlain(first), TextPlain(later))
	}
	if sameColors(first, later) {
		t.Fatal("gradient wave colors are identical four steps apart, want the wave to move")
	}
	if len(first) < 2 {
		t.Fatalf("gradient wave produced %d styled runs, want the label split across the gradient", len(first))
	}
}

func TestShimmerSweepsAHighlightAndRests(t *testing.T) {
	cfg := textConfig(TextEffectShimmer)
	cfg.Step = 80 * time.Millisecond
	const text = "expressive chat"

	highlighted := 0
	resting := 0
	for step := 0; step < ansi.StringWidth(text)+shimmerRest; step++ {
		cells := TextFrame(text, cfg, time.Duration(step)*cfg.Step)
		if TextPlain(cells) != text {
			t.Fatalf("shimmer altered the text at step %d: %q", step, TextPlain(cells))
		}
		if hasColorOtherThan(cells, cfg.Base) {
			highlighted++
			continue
		}
		resting++
	}
	if highlighted == 0 {
		t.Fatal("shimmer never highlighted anything across a full sweep")
	}
	if resting == 0 {
		t.Fatal("shimmer never rested across a full period, want a pause between passes")
	}
}

func TestBounceTravelsBothWaysAndLeavesATrail(t *testing.T) {
	cfg := textConfig(TextEffectBounce)
	cfg.Step = 90 * time.Millisecond
	cfg.Width = 12

	positions := make([]int, 0, 24)
	trailed := false
	for step := range 24 {
		cells := TextFrame("◆", cfg, time.Duration(step)*cfg.Step)
		plain := TextPlain(cells)
		glyphAt := strings.Index(plain, "◆")
		if glyphAt < 0 {
			t.Fatalf("step %d dropped the glyph entirely: %q", step, plain)
		}
		positions = append(positions, ansi.StringWidth(plain[:glyphAt]))
		if strings.Count(plain, "◆") > 1 {
			trailed = true
		}
	}
	if !trailed {
		t.Fatal("bounce never rendered a trail, want fading ghosts behind the marker")
	}

	forward, backward := false, false
	for i := 1; i < len(positions); i++ {
		switch {
		case positions[i] > positions[i-1]:
			forward = true
		case positions[i] < positions[i-1]:
			backward = true
		}
	}
	if !forward || !backward {
		t.Fatalf("bounce direction: forward=%t backward=%t, want it to reverse at both ends", forward, backward)
	}
	for _, position := range positions {
		if position < 0 || position > cfg.Width-1 {
			t.Fatalf("bounce position %d escaped the track width %d", position, cfg.Width)
		}
	}
}

// A stationary track is the narrow-terminal case: there is no room to move, so
// the label must still render rather than disappear.
func TestBounceWithoutRoomRendersStatically(t *testing.T) {
	cfg := textConfig(TextEffectBounce)
	cfg.Width = 1
	first := TextPlain(TextFrame("◆", cfg, 0))
	later := TextPlain(TextFrame("◆", cfg, time.Second))
	if first != "◆" || later != "◆" {
		t.Fatalf("stationary bounce = %q / %q, want %q", first, later, "◆")
	}
}

func TestModeOffRendersTheStaticFrameForEveryEffect(t *testing.T) {
	const text = "yc // YouTube live chat"
	for _, effect := range []TextEffect{TextEffectTypewriter, TextEffectGradientWave, TextEffectShimmer, TextEffectBounce} {
		cfg := textConfig(effect)
		cfg.Mode = ModeOff
		cfg.Width = 40

		first := TextFrame(text, cfg, 0)
		later := TextFrame(text, cfg, 3*time.Second)
		if TextPlain(first) != text {
			t.Fatalf("%s with animation off = %q, want the plain label", effect, TextPlain(first))
		}
		if !sameColors(first, later) || TextPlain(first) != TextPlain(later) {
			t.Fatalf("%s with animation off changed between frames", effect)
		}
		if !TextDone(text, cfg, 0) {
			t.Fatalf("TextDone() = false for %s with animation off, want true", effect)
		}
	}
}

func TestReducedModeSlowsEveryEffectDown(t *testing.T) {
	for _, effect := range []TextEffect{TextEffectTypewriter, TextEffectGradientWave, TextEffectShimmer, TextEffectBounce} {
		fast := defaultTextStep(effect, ModeFast)
		reduced := defaultTextStep(effect, ModeReduced)
		if reduced <= fast {
			t.Fatalf("%s reduced step = %s, want slower than the fast step %s", effect, reduced, fast)
		}
	}
}

func TestTextFrameMergesNeighboringRunsThatShareStyling(t *testing.T) {
	cfg := textConfig(TextEffectTypewriter)
	cfg.Step = 50 * time.Millisecond
	// Four revealed clusters in one color, a cursor, then the blank tail:
	// three runs, not eleven.
	cells := TextFrame("hello world", cfg, 4*cfg.Step)
	if len(cells) > 3 {
		t.Fatalf("typewriter emitted %d runs, want them merged by style: %+v", len(cells), cells)
	}
}

func TestTextFrameEmptyAndZeroClock(t *testing.T) {
	if cells := TextFrame("", textConfig(TextEffectShimmer), time.Second); cells != nil {
		t.Fatalf("TextFrame(\"\") = %+v, want nil", cells)
	}
	if got := FrameElapsed(time.Time{}); got != 0 {
		t.Fatalf("FrameElapsed(zero time) = %s, want 0", got)
	}
	if got := FrameElapsed(time.UnixMilli(1500)); got != 1500*time.Millisecond {
		t.Fatalf("FrameElapsed() = %s, want 1.5s past the epoch", got)
	}
}

func TestTextOffsetStaggersContinuousEffects(t *testing.T) {
	cfg := textConfig(TextEffectGradientWave)
	cfg.Step = 100 * time.Millisecond
	plain := TextFrame("████████", cfg, 0)
	staggered := cfg
	staggered.Offset = 3
	if sameColors(plain, TextFrame("████████", staggered, 0)) {
		t.Fatal("Offset did not shift the gradient phase, want staggered art rows")
	}
}

// An unrecognized animation_mode must degrade to the default rather than
// silently disabling every effect.
func TestUnknownModeFallsBackToFast(t *testing.T) {
	if got := NormalizeMode("turbo"); got != ModeFast {
		t.Fatalf("NormalizeMode(\"turbo\") = %q, want %q", got, ModeFast)
	}
	cfg := textConfig(TextEffectTypewriter)
	cfg.Mode = Mode("turbo")
	if TextDone("hello", cfg, 0) {
		t.Fatal("TextDone() = true at the start of a typewriter in an unknown mode, want false")
	}
}

func sameColors(a, b []TextCell) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Foreground != b[i].Foreground || a[i].Bold != b[i].Bold || a[i].Text != b[i].Text {
			return false
		}
	}
	return true
}

func hasColorOtherThan(cells []TextCell, color string) bool {
	for _, cell := range cells {
		if !strings.EqualFold(cell.Foreground, color) {
			return true
		}
	}
	return false
}

package animation

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// DefaultFrameInterval is the shared animation clock's tick rate. ~10fps is
// enough for pulsing status indicators, gradients, the startup splash, and
// typewriter reveals without meaningful CPU overhead.
const DefaultFrameInterval = 100 * time.Millisecond

// ClockOnlyFrameInterval is the tick rate used when animation is off.
//
// Animation being off means "nothing moves", not "no time passes". The frame
// clock is the only clock View is allowed to read, so every elapsed-time
// readout in the app - the on-air timer, roster tenure, relative timestamps -
// derives from its last tick. Stopping the ticker outright in ModeOff freezes
// all of them at zero for the whole session, which is wrong information rather
// than merely still information.
//
// One tick per second is the coarsest rate an mm:ss readout can advance on
// without visibly skipping, and it starts nothing moving: every motion effect
// keys off the mode rather than off the clock, so a ticking clock in ModeOff
// still paints an identical frame every time.
const ClockOnlyFrameInterval = time.Second

// FrameMsg is emitted by the shared animation clock.
//
// Unlike the reveal-queue tick, which only runs while chat rows are being typed
// in, the frame clock ticks continuously so every chrome effect derives its
// phase from one driver. Adding an animated widget means hooking this tick, not
// starting a second ticker.
type FrameMsg struct {
	At time.Time
}

// ScheduleFrame returns a command that emits a FrameMsg after interval.
func ScheduleFrame(interval time.Duration) tea.Cmd {
	if interval <= 0 {
		interval = DefaultFrameInterval
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return FrameMsg{At: t}
	})
}

// Clock supplies the current time to reveal state. It is an interface so tests
// drive animation from a fake clock rather than a wall-clock sleep.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// Mode selects animation intensity. It maps directly onto the animation_mode
// config value.
type Mode string

const (
	// ModeOff disables animation entirely. Effects collapse to their static
	// frame with wording and layout unchanged, and effects that carry no
	// meaning when stationary are dropped rather than frozen.
	ModeOff Mode = "off"
	// ModeReduced halves the rate and reveals in larger steps.
	ModeReduced Mode = "reduced"
	// ModeFast is the default.
	ModeFast Mode = "fast"
)

// NormalizeMode maps a config string onto a known mode, falling back to
// ModeFast so an unrecognized value degrades instead of failing.
func NormalizeMode(value string) Mode {
	switch Mode(value) {
	case ModeOff:
		return ModeOff
	case ModeReduced:
		return ModeReduced
	default:
		return ModeFast
	}
}

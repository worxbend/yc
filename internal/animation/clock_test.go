package animation

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestScheduleFrameEmitsFrameMsg(t *testing.T) {
	cmd := ScheduleFrame(time.Millisecond)
	if cmd == nil {
		t.Fatal("ScheduleFrame returned nil command")
	}
	msg := cmd()
	frame, ok := msg.(FrameMsg)
	if !ok {
		t.Fatalf("ScheduleFrame command produced %T, want FrameMsg", msg)
	}
	if frame.At.IsZero() {
		t.Fatal("FrameMsg.At is zero")
	}
}

func TestScheduleFrameDefaultsNonPositiveInterval(t *testing.T) {
	cmd := ScheduleFrame(0)
	if cmd == nil {
		t.Fatal("ScheduleFrame returned nil command")
	}
	if _, ok := cmd().(FrameMsg); !ok {
		t.Fatal("ScheduleFrame(0) did not produce a FrameMsg")
	}
}

func TestNormalizeModeMapsConfigValues(t *testing.T) {
	for value, want := range map[string]Mode{
		"off":     ModeOff,
		"reduced": ModeReduced,
		"fast":    ModeFast,
		"":        ModeFast,
		"sparkle": ModeFast,
	} {
		if got := NormalizeMode(value); got != want {
			t.Fatalf("NormalizeMode(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestSystemClockAdvances(t *testing.T) {
	var clock Clock = SystemClock{}
	if clock.Now().IsZero() {
		t.Fatal("SystemClock.Now() is the zero time")
	}
}

var _ tea.Cmd = ScheduleFrame(DefaultFrameInterval)

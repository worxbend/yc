package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

func testStatusBarState() statusBarState {
	return statusBarState{
		Palette:       theme.DefaultPalette(),
		AnimationMode: animation.ModeFast,
		Now:           time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC),
		ChatTitle:     "Launch Day Stream",
		Status:        youtube.ConnectionConnected,
		ChatCount:     1,
		Focus:         "chat",
		Layout:        "inline",
		Live:          true,
		LiveSince:     time.Date(2026, 8, 8, 18, 0, 0, 0, time.UTC),
		Viewers:       12345,
		ViewersKnown:  true,
		QuotaKnown:    true,
		CostPerPoll:   5,
		Quota: quota.Snapshot{
			UsedUnits:         3240,
			LimitUnits:        10000,
			RemainingUnits:    6760,
			EffectiveInterval: 5 * time.Second,
			ServerFloor:       5 * time.Second,
			Mode:              quota.ModeLive,
			Estimated:         true,
		},
	}
}

func TestStatusBarIsExactlyOneRowOfTheRequestedWidth(t *testing.T) {
	st := testStatusBarState()
	for _, width := range []int{1, 8, 20, 40, 60, 80, 96, 120, 200} {
		line := renderStatusBar(width, st)
		if strings.Contains(line, "\n") {
			t.Fatalf("width %d produced more than one row", width)
		}
		if got := ansi.StringWidth(ansi.Strip(line)); got != width {
			t.Fatalf("width %d rendered %d cells: %q", width, got, ansi.Strip(line))
		}
	}
}

// The quota meter is the surface a YouTube client must have and a Twitch one
// need not. This pins its degradation ladder, because "how much budget is left"
// has to survive every width a terminal can be.
func TestQuotaMeterDegradationLadder(t *testing.T) {
	st := testStatusBarState()
	tests := []struct {
		width int
		want  []string
		gone  []string
	}{
		{width: 120, want: []string{"⟳ 5.0s", "3,240/10,000", "est", "68% left", "LIVE"}},
		{width: 80, want: []string{"⟳ 5.0s", "68%", "est"}, gone: []string{"3,240/10,000"}},
		{width: 50, want: []string{"⟳ 5.0s", "68%"}, gone: []string{"STRETCHED", "3,240"}},
		{width: 20, want: []string{"68%"}, gone: []string{"⟳"}},
	}
	for _, test := range tests {
		line := ansi.Strip(renderStatusBar(test.width, st))
		for _, want := range test.want {
			if !strings.Contains(line, want) {
				t.Errorf("width %d: missing %q in %q", test.width, want, line)
			}
		}
		for _, gone := range test.gone {
			if strings.Contains(line, gone) {
				t.Errorf("width %d: unexpectedly kept %q in %q", test.width, gone, line)
			}
		}
	}
}

// Google publishes no quota cost for any live-chat method, so a bare number
// would be a claim yc cannot support. Wherever a unit or budget figure is
// shown with room for the marker, the marker is shown too.
func TestQuotaFiguresAreLabelledAsEstimates(t *testing.T) {
	st := testStatusBarState()
	for _, width := range []int{72, 96, 120, 200} {
		line := ansi.Strip(renderStatusBar(width, st))
		if !strings.Contains(line, "est") {
			t.Fatalf("width %d shows quota figures without the estimate marker: %q", width, line)
		}
	}
}

func TestQuotaColorShiftsAsBudgetDepletes(t *testing.T) {
	palette := theme.DefaultPalette()
	tests := []struct {
		name      string
		remaining float64
		want      string
	}{
		{name: "healthy", remaining: 80, want: palette.Success},
		{name: "warning", remaining: 40, want: palette.Warning},
		{name: "critical", remaining: 5, want: palette.Error},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := quotaColor(palette, test.remaining); got != test.want {
				t.Fatalf("quotaColor(%v) = %q, want %q", test.remaining, got, test.want)
			}
		})
	}
}

func TestStatusBarNamesTheStretchedCadence(t *testing.T) {
	st := testStatusBarState()
	st.Quota.Mode = quota.ModeStretched
	st.Quota.EffectiveInterval = 43 * time.Second
	st.Quota.BudgetFloor = 43 * time.Second

	line := ansi.Strip(renderStatusBar(120, st))
	if !strings.Contains(line, "STRETCHED") {
		t.Fatalf("stretched cadence is not announced: %q", line)
	}
	if !strings.Contains(line, "43s") {
		t.Fatalf("effective interval missing: %q", line)
	}
}

// Losing chat silently is the one failure a moderator must not have to
// discover for themselves, so the counter outranks every decoration.
func TestDroppedMessagesAreShownAtEveryWidth(t *testing.T) {
	st := testStatusBarState()
	st.Dropped = 7
	for _, width := range []int{30, 60, 120} {
		if line := ansi.Strip(renderStatusBar(width, st)); !strings.Contains(line, "dropped=7") {
			t.Fatalf("width %d hid the dropped counter: %q", width, line)
		}
	}
}

func TestPendingClearIsAlwaysVisible(t *testing.T) {
	st := testStatusBarState()
	st.PendingClear = true
	line := ansi.Strip(renderStatusBar(200, st))
	if !strings.Contains(line, "ctrl+L again to confirm") {
		t.Fatalf("armed clear guard is invisible: %q", line)
	}
}

func TestStatusBarWithoutAChatSaysSo(t *testing.T) {
	st := testStatusBarState()
	st.ChatTitle = ""
	if line := ansi.Strip(renderStatusBar(120, st)); !strings.Contains(line, "no chat open") {
		t.Fatalf("empty session is not named: %q", line)
	}
}

func TestStatusBarWithoutQuotaOmitsTheMeter(t *testing.T) {
	st := testStatusBarState()
	st.QuotaKnown = false
	line := ansi.Strip(renderStatusBar(120, st))
	if strings.Contains(line, "est") || strings.Contains(line, "⟳") {
		t.Fatalf("a source with no ledger rendered a meter: %q", line)
	}
}

// With animation off the pulse must settle on the lit phase: a half-faded
// label on a static frame reads as a rendering bug.
func TestPulseIsLitWhenAnimationIsOff(t *testing.T) {
	if !pulseOn(animation.ModeOff, time.Now(), statusPulseInterval) {
		t.Fatal("animation=off produced an unlit pulse")
	}
	if !pulseOn(animation.ModeFast, time.Time{}, statusPulseInterval) {
		t.Fatal("the zero frame time produced an unlit pulse")
	}
}

func TestFormatUnitsGroupsThousands(t *testing.T) {
	tests := map[int]string{0: "0", 42: "42", 999: "999", 1000: "1,000", 10000: "10,000", 1234567: "1,234,567", -1500: "-1,500"}
	for value, want := range tests {
		if got := formatUnits(value); got != want {
			t.Errorf("formatUnits(%d) = %q, want %q", value, got, want)
		}
	}
}

func TestFormatPollInterval(t *testing.T) {
	tests := map[time.Duration]string{
		0:                       "--",
		1500 * time.Millisecond: "1.5s",
		5 * time.Second:         "5.0s",
		43 * time.Second:        "43s",
		150 * time.Second:       "2m30s",
	}
	for value, want := range tests {
		if got := formatPollInterval(value); got != want {
			t.Errorf("formatPollInterval(%v) = %q, want %q", value, got, want)
		}
	}
}

package youtube

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/quota"
)

// pacificTime builds an instant in the quota reset timezone. The poll budget is
// measured against the next Pacific midnight, so a test that anchors "now" in
// any other zone is testing a different horizon than the code uses.
func pacificTime(t *testing.T, year int, month time.Month, day, hour, minute int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(quota.ResetLocation)
	if err != nil {
		t.Fatalf("load %s: %v", quota.ResetLocation, err)
	}
	return time.Date(year, month, day, hour, minute, 0, 0, loc)
}

// fixedClock returns a clock the ledger can be driven by without a wall clock.
func fixedClock(at *time.Time) func() time.Time {
	return func() time.Time { return *at }
}

// The cost model is not an accounting exercise. It is the mechanism that
// decides whether a viewer's chat survives the stream: at an estimated 5 units
// per call against a 10,000-unit day, polling at YouTube's own ~5s cadence
// exhausts the whole allowance in under three hours, and lasting a full day
// needs roughly a 43-second interval. These cover the two functions that turn
// the ledger into that cadence, and the reserve that keeps a stream owner able
// to moderate after their read budget is gone.

// newPacingPoller builds a poller that never dispatches. Only the pacing
// arithmetic is under test, so the handler is a failure.
func newPacingPoller(t *testing.T, cfg PollerConfig) *Poller {
	t.Helper()
	if cfg.Client == nil {
		client, _ := newTestClient(t, oauthCredentials(), func(http.ResponseWriter, *http.Request) {
			t.Error("the pacing arithmetic dispatched a request")
		})
		cfg.Client = client
	}
	if cfg.Target.Raw == "" && !cfg.Target.Resolved() {
		cfg.Target = ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"}
	}
	if cfg.Sleep == nil {
		cfg.Sleep = func(ctx context.Context, _ time.Duration) { <-ctx.Done() }
	}
	poller, err := NewPoller(cfg)
	if err != nil {
		t.Fatalf("NewPoller error = %v", err)
	}
	return poller
}

// TestTheBudgetFloorIsTheDaysArithmetic walks the cadence the remaining units
// imply, including every reason there is no constraint at all.
func TestTheBudgetFloorIsTheDaysArithmetic(t *testing.T) {
	now := pacificTime(t, 2026, time.August, 8, 0, 0)
	reset := quota.ResetAt(now)

	tests := []struct {
		name     string
		cfg      PollerConfig
		snapshot quota.Snapshot
		lastCost int
		want     time.Duration
		because  string
	}{
		{
			name:     "a full day at the estimated list cost",
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 10000, ResetAt: reset},
			lastCost: 5,
			want:     43200 * time.Millisecond,
			because:  "10,000 units at 5 per poll is 2000 polls across 24 hours",
		},
		{
			name:     "half the day spent",
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 5000, ResetAt: reset},
			lastCost: 5,
			want:     86400 * time.Millisecond,
			because:  "half the units over the same horizon is twice the interval",
		},
		{
			name: "the user asked to follow the server's cadence",
			cfg:  PollerConfig{FollowServerCadence: true},
			// Opting out is opting out: the budget floor disappears entirely
			// rather than being merely relaxed, because the whole point is
			// that the server's pollingIntervalMillis is now the only floor.
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 10, ResetAt: reset},
			lastCost: 5,
			want:     0,
			because:  "FollowServerCadence removes the budget constraint",
		},
		{
			name:     "no ledger is wired",
			snapshot: quota.Snapshot{RemainingUnits: 10000, ResetAt: reset},
			lastCost: 5,
			want:     0,
			because:  "there is no budget to protect without a limit",
		},
		{
			name:     "a session horizon shorter than the day",
			cfg:      PollerConfig{SessionHorizon: 2 * time.Hour},
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 10000, ResetAt: reset},
			lastCost: 5,
			want:     3600 * time.Millisecond,
			because:  "a two-hour stream may spend the whole day's budget in two hours",
		},
		{
			name:     "a session horizon longer than the day is ignored",
			cfg:      PollerConfig{SessionHorizon: 72 * time.Hour},
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 10000, ResetAt: reset},
			lastCost: 5,
			want:     43200 * time.Millisecond,
			because:  "the allowance resets at midnight whatever the user planned",
		},
		{
			name:     "nothing left to spend",
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 2, ResetAt: reset},
			lastCost: 5,
			want:     24 * time.Hour,
			because:  "fewer units than one poll costs makes the floor the whole horizon",
		},
		{
			name:     "an unknown last cost falls back to the table",
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 10000, ResetAt: reset},
			lastCost: 0,
			want:     43200 * time.Millisecond,
			because:  "the first poll of a session has no measured cost yet",
		},
		{
			name:     "an absent ResetAt is derived rather than treated as zero",
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 10000},
			lastCost: 5,
			want:     43200 * time.Millisecond,
			because:  "a snapshot with no reset must not collapse the horizon to nothing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := test.cfg
			// The injected clock, not the wall clock. This is the bug the
			// budget comment records: time.Until here silently zeroed the
			// floor whenever the clock was anchored away from the present,
			// which is every deterministic test of the pacing model.
			cfg.Now = func() time.Time { return now }
			poller := newPacingPoller(t, cfg)

			if got := poller.budget(test.snapshot, test.lastCost); got != test.want {
				t.Fatalf("budget = %v, want %v because %s", got, test.want, test.because)
			}
		})
	}
}

// TestTheBudgetFloorUsesTheInjectedClock is the regression on its own, because
// it is the failure that looks like a passing test: a wall-clock horizon
// against an anchored snapshot is negative, quota.BudgetFloor returns 0 for a
// non-positive horizon, and every pacing assertion above would quietly become
// "no constraint".
func TestTheBudgetFloorUsesTheInjectedClock(t *testing.T) {
	// A clock anchored two years in the past. Against the wall clock the
	// horizon is enormously negative.
	anchored := pacificTime(t, 2024, time.January, 15, 6, 0)
	poller := newPacingPoller(t, PollerConfig{Now: func() time.Time { return anchored }})

	snapshot := quota.Snapshot{LimitUnits: 10000, RemainingUnits: 10000, ResetAt: quota.ResetAt(anchored)}
	got := poller.budget(snapshot, 5)
	if got <= 0 {
		t.Fatalf("budget = %v with an anchored clock, want the same floor a live clock would give", got)
	}
	if want := 18 * time.Hour / 2000; got != want {
		t.Fatalf("budget = %v, want %v (the 18 hours left in that Pacific day over 2000 polls)", got, want)
	}
}

// The reserve exists so that running out of read budget does not also take away
// the ability to send or moderate - which is the half of the client a stream
// owner cannot do without, and the half that costs 50 units a call.
func TestTheReserveProtectsTheAbilityToModerate(t *testing.T) {
	reset := pacificTime(t, 2026, time.August, 9, 0, 0)

	tests := []struct {
		name     string
		reserve  int
		snapshot quota.Snapshot
		wantStop bool
		wantSaid string
	}{
		{
			name:     "plenty left",
			reserve:  10,
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 5000, ResetAt: reset},
			wantStop: false,
		},
		{
			name:     "one unit above the reserve",
			reserve:  10,
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 1001, ResetAt: reset},
			wantStop: false,
		},
		{
			name:     "exactly at the reserve",
			reserve:  10,
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 1000, ResetAt: reset},
			wantStop: true,
			wantSaid: "sends still available",
		},
		{
			name:     "nothing left at all",
			reserve:  10,
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 0, ResetAt: reset},
			wantStop: true,
			wantSaid: "exhausted",
		},
		{
			name:     "the reserve is switched off",
			reserve:  0,
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 1, ResetAt: reset},
			wantStop: false,
		},
		{
			name:     "an exhausted budget stops even with the reserve switched off",
			reserve:  0,
			snapshot: quota.Snapshot{LimitUnits: 10000, RemainingUnits: 0, ResetAt: reset},
			wantStop: true,
			wantSaid: "exhausted",
		},
		{
			name:     "no ledger, no reserve to protect",
			reserve:  10,
			snapshot: quota.Snapshot{RemainingUnits: 0, ResetAt: reset},
			wantStop: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			poller := newPacingPoller(t, PollerConfig{ReservePercent: test.reserve})
			detail, stop := poller.reserveTripped(test.snapshot)
			if stop != test.wantStop {
				t.Fatalf("reserveTripped = %v (%q), want %v", stop, detail, test.wantStop)
			}
			if !stop {
				if detail != "" {
					t.Fatalf("detail = %q, want none when polling continues", detail)
				}
				return
			}
			if !strings.Contains(detail, test.wantSaid) {
				t.Fatalf("detail = %q, want it to mention %q", detail, test.wantSaid)
			}
			// Every quota figure yc shows is an estimate, and the user must
			// always have a way to override a pause yc chose for them.
			if !strings.Contains(detail, "(est.)") {
				t.Fatalf("detail = %q, want the estimate marker", detail)
			}
			if !strings.Contains(detail, "ctrl+r") {
				t.Fatalf("detail = %q, want the override named", detail)
			}
		})
	}
}

// The reserve percentage is clamped at construction, so a config typo cannot
// pause polling permanently or disable the protection by going negative.
func TestTheReservePercentIsClamped(t *testing.T) {
	for _, tc := range []struct {
		configured int
		want       int
	}{
		{-10, 0}, {0, 0}, {10, 10}, {100, 100}, {1000, 100},
	} {
		poller := newPacingPoller(t, PollerConfig{ReservePercent: tc.configured})
		if got := poller.cfg.ReservePercent; got != tc.want {
			t.Errorf("ReservePercent %d was stored as %d, want %d", tc.configured, got, tc.want)
		}
	}
}

// chatEnded reads the normalized room events rather than the raw response, so a
// chatEndedEvent item and a 403 liveChatEnded end the session the same way.
func TestChatEndedReadsTheNormalizedRoomEvent(t *testing.T) {
	poller := newPacingPoller(t, PollerConfig{})

	if ended, detail := poller.chatEnded(ListResult{}); ended || detail != "" {
		t.Fatalf("an empty result reported the chat ended (%q)", detail)
	}
	if ended, _ := poller.chatEnded(ListResult{NormalizeResult: NormalizeResult{RoomEvents: []RoomEvent{{Type: RoomSponsorOnlyStarted}}}}); ended {
		t.Fatal("an unrelated room event ended the chat")
	}

	ended, detail := poller.chatEnded(ListResult{NormalizeResult: NormalizeResult{RoomEvents: []RoomEvent{{Type: RoomChatEnded, Detail: "the host ended the stream"}}}})
	if !ended {
		t.Fatal("a chatEnded room event did not end the chat")
	}
	if detail != "the host ended the stream" {
		t.Fatalf("detail = %q, want the event's own reason", detail)
	}

	// A tombstone with no reason still has to say something: an empty status
	// line reads as a bug rather than as a broadcast that finished.
	ended, detail = poller.chatEnded(ListResult{NormalizeResult: NormalizeResult{RoomEvents: []RoomEvent{{Type: RoomChatEnded, Detail: "   "}}}})
	if !ended || strings.TrimSpace(detail) == "" {
		t.Fatalf("a reasonless chatEnded event produced %v/%q, want a default reason", ended, detail)
	}
}

// The default Sleep is what the poll loop uses in production, and its only job
// is to be interruptible: a user who quits must not wait out a 43-second
// budget-paced interval before the process exits.
func TestTheDefaultSleepIsInterruptible(t *testing.T) {
	// A non-positive duration returns at once rather than arming a timer.
	started := time.Now()
	sleepContext(context.Background(), 0)
	sleepContext(context.Background(), -time.Second)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("a non-positive sleep took %v", elapsed)
	}

	// A canceled context cuts a long sleep short.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	started = time.Now()
	sleepContext(ctx, time.Hour)
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("a canceled sleep took %v, want it to return promptly", elapsed)
	}

	// And it does actually wait when nothing interrupts it.
	started = time.Now()
	sleepContext(context.Background(), 25*time.Millisecond)
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("a 25ms sleep returned after %v; the poll loop would spin", elapsed)
	}
}

// climb advances the backoff multiplier without letting the resulting interval
// exceed its cap. The cap is measured against the interval actually in force,
// not the configured minimum: using the minimum let a 5s server cadence stretch
// a 60s cap to 300s and stall the chat for five minutes.
func TestTheBackoffLadderIsCappedAgainstTheInterval(t *testing.T) {
	base := 5 * time.Second
	ceiling := 60 * time.Second

	backoff := backoffFloor
	for i := 0; i < 20; i++ {
		backoff = climb(backoff, base, ceiling)
		if interval := time.Duration(float64(base) * backoff); interval > ceiling {
			t.Fatalf("after %d climbs the interval is %v, past the %v cap", i+1, interval, ceiling)
		}
		if backoff < backoffFloor {
			t.Fatalf("backoff fell to %v, below the floor %v", backoff, backoffFloor)
		}
	}

	// A zero base cannot produce an infinite multiplier.
	if got := climb(backoffFloor, 0, ceiling); got <= 0 {
		t.Fatalf("climb with no base = %v, want a usable multiplier", got)
	}
}

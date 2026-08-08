package youtube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The daily allowance resets at midnight in America/Los_Angeles, and that is
// the only clock that matters: a meter that rolls over at the wrong hour either
// hands the user a budget they do not have, or refuses to poll a chat they have
// every right to poll. Twice a year that hour moves, and the two failure modes
// there are not symmetric - "adding 24h to midnight" lands on 23:00 or 01:00
// across a transition, so a session running through the boundary either resets
// an hour early and overspends, or an hour late and stalls the chat.

// pacificTime builds an instant in the reset timezone.
func pacificTime(t *testing.T, year int, month time.Month, day, hour, minute int) time.Time {
	t.Helper()
	return time.Date(year, month, day, hour, minute, 0, 0, pacificLocation())
}

// TestResetAtCrossesTheDaylightSavingBoundaryByCalendarDay pins the arithmetic
// that the ResetAt comment exists to defend.
//
// A 23-hour day and a 25-hour day are both correct answers here. What would not
// be correct is 24 hours in either case: on the spring-forward day that lands
// an hour into the next day and resets the meter early, and on the fall-back
// day it lands an hour short and reports a reset that has not happened.
func TestResetAtCrossesTheDaylightSavingBoundaryByCalendarDay(t *testing.T) {
	loc := pacificLocation()
	tests := []struct {
		name string
		now  time.Time
		want time.Duration
	}{
		{
			// 2026-03-08 is the US spring-forward day: 02:00 PST becomes
			// 03:00 PDT, so the day is 23 hours long.
			name: "midnight on the spring-forward day",
			now:  pacificTime(t, 2026, time.March, 8, 0, 0),
			want: 23 * time.Hour,
		},
		{
			name: "the hour before the spring-forward transition",
			now:  pacificTime(t, 2026, time.March, 8, 1, 0),
			want: 22 * time.Hour,
		},
		{
			// 2026-11-01 is the US fall-back day: 02:00 PDT becomes 01:00
			// PST, so the day is 25 hours long.
			name: "midnight on the fall-back day",
			now:  pacificTime(t, 2026, time.November, 1, 0, 0),
			want: 25 * time.Hour,
		},
		{
			name: "an ordinary day is exactly 24 hours",
			now:  pacificTime(t, 2026, time.August, 8, 0, 0),
			want: 24 * time.Hour,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reset := ResetAt(test.now)
			if got := reset.Sub(test.now); got != test.want {
				t.Fatalf("ResetAt(%s) is %v away, want %v", test.now, got, test.want)
			}
			local := reset.In(loc)
			if local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 || local.Nanosecond() != 0 {
				t.Fatalf("reset = %s, want exactly midnight Pacific", local)
			}
			if !reset.After(test.now) {
				t.Fatalf("reset %s is not after %s; the meter would report a reset that has passed", reset, test.now)
			}
		})
	}
}

// The reset is always the *next* midnight, including when the clock reads
// midnight exactly. Returning "now" would make the projected exhaustion zero
// and the budget floor infinite for one instant a day.
func TestResetAtIsAlwaysInTheFuture(t *testing.T) {
	for _, now := range []time.Time{
		pacificTime(t, 2026, time.August, 8, 0, 0),
		pacificTime(t, 2026, time.August, 8, 23, 59),
		pacificTime(t, 2026, time.March, 8, 0, 0),
		pacificTime(t, 2026, time.November, 1, 0, 0),
	} {
		if reset := ResetAt(now); !reset.After(now) {
			t.Errorf("ResetAt(%s) = %s, want a strictly later instant", now, reset)
		}
	}
	// The answer must not depend on the caller's timezone, only on the
	// instant. yc runs wherever the user is.
	instant := pacificTime(t, 2026, time.August, 8, 15, 0)
	for _, zone := range []string{"UTC", "Asia/Tokyo", "America/New_York"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			continue
		}
		if got, want := ResetAt(instant.In(loc)), ResetAt(instant); !got.Equal(want) {
			t.Errorf("ResetAt from %s = %s, want %s; the reset is an instant, not a local hour", zone, got, want)
		}
	}
}

// PacificDay is the persistence key, so its boundary is where a restart either
// finds today's spend or silently starts the day over.
func TestPacificDayFlipsExactlyAtMidnight(t *testing.T) {
	midnight := pacificTime(t, 2026, time.August, 9, 0, 0)
	if got := PacificDay(midnight); got != "2026-08-09" {
		t.Fatalf("PacificDay(midnight) = %q, want 2026-08-09", got)
	}
	if got := PacificDay(midnight.Add(-time.Nanosecond)); got != "2026-08-08" {
		t.Fatalf("PacificDay one nanosecond before midnight = %q, want 2026-08-08", got)
	}

	// The ambiguous hour on the fall-back day occurs twice, at two different
	// instants an hour apart. Both are the same Pacific calendar day, so the
	// key must not move - a session running through it would otherwise
	// abandon the tally it is still adding to.
	ambiguous := pacificTime(t, 2026, time.November, 1, 1, 30)
	for _, offset := range []time.Duration{0, time.Hour} {
		if got := PacificDay(ambiguous.Add(offset)); got != "2026-11-01" {
			t.Fatalf("PacificDay in the repeated hour (+%v) = %q, want 2026-11-01", offset, got)
		}
	}
}

// TestLedgerDoesNotRollOverAcrossADaylightSavingTransition is the whole point
// of keying the ledger by calendar day rather than by elapsed hours.
//
// The spring-forward transition moves the wall clock two hours forward in one
// step. A meter that decided "a new day started" from a time difference would
// zero itself in the middle of the afternoon and let the user spend the day's
// allowance twice.
func TestLedgerDoesNotRollOverAcrossADaylightSavingTransition(t *testing.T) {
	tests := []struct {
		name   string
		before time.Time
		after  time.Time
	}{
		{
			name:   "spring forward",
			before: pacificTime(t, 2026, time.March, 8, 1, 59),
			after:  pacificTime(t, 2026, time.March, 8, 3, 1),
		},
		{
			name:   "fall back, the repeated hour",
			before: pacificTime(t, 2026, time.November, 1, 1, 30),
			after:  pacificTime(t, 2026, time.November, 1, 1, 30).Add(time.Hour),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := test.before
			ledger := NewQuotaLedger(LedgerConfig{Now: fixedClock(&now)})
			ledger.Charge(EndpointMessagesList)
			if used := ledger.Snapshot().UsedUnits; used != 5 {
				t.Fatalf("UsedUnits = %d before the transition, want 5", used)
			}

			now = test.after
			if used := ledger.Snapshot().UsedUnits; used != 5 {
				t.Fatalf("UsedUnits = %d after the transition, want the same day's tally to survive", used)
			}
			ledger.Charge(EndpointMessagesList)
			if used := ledger.Snapshot().UsedUnits; used != 10 {
				t.Fatalf("UsedUnits = %d, want the tally to keep accumulating across the transition", used)
			}

			// And the next calendar day still resets, transition or not.
			now = test.after.AddDate(0, 0, 1)
			if used := ledger.Snapshot().UsedUnits; used != 0 {
				t.Fatalf("UsedUnits = %d on the next Pacific day, want a fresh allowance", used)
			}
		})
	}
}

// The rollover has to happen on Charge too, not only on Snapshot. A poll loop
// that runs across midnight without anything reading the snapshot would
// otherwise keep charging yesterday's key, and the day's real spend would be
// scattered across two records.
func TestChargeRollsTheDayOverByItself(t *testing.T) {
	root := t.TempDir()
	now := pacificTime(t, 2026, time.August, 8, 23, 59)
	store := NewFileLedgerStore(root)
	ledger := NewQuotaLedger(LedgerConfig{
		Now:         fixedClock(&now),
		Store:       store,
		Fingerprint: "aaaaaaaaaaaaaaaa",
	})
	ledger.Charge(EndpointMessagesList)

	now = now.Add(2 * time.Minute)
	ledger.Charge(EndpointMessagesInsert)

	if used := ledger.Snapshot().UsedUnits; used != 50 {
		t.Fatalf("UsedUnits = %d, want only the new day's insert", used)
	}
	yesterday, err := store.LoadLedger("aaaaaaaaaaaaaaaa", "2026-08-08")
	if err != nil {
		t.Fatalf("LoadLedger error = %v", err)
	}
	if yesterday[EndpointMessagesList] != 5 {
		t.Fatalf("yesterday's record = %v, want the list call recorded under its own day", yesterday)
	}
	today, err := store.LoadLedger("aaaaaaaaaaaaaaaa", "2026-08-09")
	if err != nil {
		t.Fatalf("LoadLedger error = %v", err)
	}
	if today[EndpointMessagesInsert] != 50 {
		t.Fatalf("today's record = %v, want the insert recorded under the new day", today)
	}
	if _, leaked := today[EndpointMessagesList]; leaked {
		t.Fatalf("today's record = %v, want no trace of yesterday's spend", today)
	}
}

// The two buckets deplete independently. search.list moved into its own
// 100-calls-per-day Search Queries bucket on 2026-06-01, so folding it into the
// main pool would overstate the spend that actually stops chat from working -
// and understating the search bucket would let yc walk into a 403 with the main
// pool nearly untouched.
func TestTheTwoQuotaBucketsNeverBleedIntoEachOther(t *testing.T) {
	now := pacificTime(t, 2026, time.August, 8, 12, 0)
	root := t.TempDir()
	newLedger := func() *QuotaLedger {
		return NewQuotaLedger(LedgerConfig{
			Now:         fixedClock(&now),
			Store:       NewFileLedgerStore(root),
			Fingerprint: "aaaaaaaaaaaaaaaa",
		})
	}

	ledger := newLedger()
	for i := 0; i < 7; i++ {
		ledger.Charge(EndpointSearchList)
	}
	ledger.Charge(EndpointMessagesList)

	snapshot := ledger.Snapshot()
	if snapshot.SearchUsed != 7 {
		t.Fatalf("SearchUsed = %d, want 7", snapshot.SearchUsed)
	}
	if snapshot.UsedUnits != 5 {
		t.Fatalf("UsedUnits = %d, want only the list call; search spends its own bucket", snapshot.UsedUnits)
	}
	if snapshot.SearchLimit != DefaultSearchCalls {
		t.Fatalf("SearchLimit = %d, want %d", snapshot.SearchLimit, DefaultSearchCalls)
	}

	// Both counters survive a restart, and the reserved persistence key is
	// never mistaken for an endpoint.
	restarted := newLedger()
	restored := restarted.Snapshot()
	if restored.SearchUsed != 7 || restored.UsedUnits != 5 {
		t.Fatalf("after a restart SearchUsed/UsedUnits = %d/%d, want 7/5", restored.SearchUsed, restored.UsedUnits)
	}
	for endpoint := range restored.ByEndpoint {
		if strings.HasPrefix(endpoint, "__") {
			t.Fatalf("the reserved key %q reached the per-endpoint tally", endpoint)
		}
	}
	for _, endpoint := range SortedEndpoints(restored.ByEndpoint) {
		if strings.HasPrefix(endpoint, "__") {
			t.Fatalf("the reserved key %q reached `yc quota` output", endpoint)
		}
	}
}

// An overspent day reports zero remaining rather than a negative number. The
// figure is rendered into a status bar and fed to the budget floor, and a
// negative remaining would produce a negative poll interval.
func TestRemainingUnitsNeverGoesNegative(t *testing.T) {
	now := pacificTime(t, 2026, time.August, 8, 12, 0)
	ledger := NewQuotaLedger(LedgerConfig{DailyUnits: 10, Now: fixedClock(&now)})
	for i := 0; i < 5; i++ {
		ledger.Charge(EndpointMessagesInsert) // 50 units each, against a 10-unit day
	}

	snapshot := ledger.Snapshot()
	if snapshot.RemainingUnits != 0 {
		t.Fatalf("RemainingUnits = %d, want it clamped to 0", snapshot.RemainingUnits)
	}
	if snapshot.UsedUnits != 250 {
		t.Fatalf("UsedUnits = %d, want the real spend reported even past the limit", snapshot.UsedUnits)
	}
	if got := snapshot.RemainingPercent(); got != 0 {
		t.Fatalf("RemainingPercent = %v, want 0", got)
	}
	if _, ok := snapshot.ProjectedExhaustion(5); ok {
		t.Fatal("an exhausted budget must not report a meaningful projection")
	}
	if floor := BudgetFloor(snapshot.RemainingUnits, 5, time.Until(snapshot.ResetAt)); floor < 0 {
		t.Fatalf("BudgetFloor = %v, want a non-negative interval", floor)
	}
}

// Every figure the ledger reports is an estimate, and the label has to be a
// property of the data rather than a string the view remembers to add.
func TestEverySnapshotIsLabelledAnEstimate(t *testing.T) {
	now := pacificTime(t, 2026, time.August, 8, 12, 0)
	ledger := NewQuotaLedger(LedgerConfig{Now: fixedClock(&now)})
	if !ledger.Snapshot().Estimated {
		t.Fatal("a fresh snapshot is not labelled an estimate")
	}
	ledger.Charge(EndpointMessagesList)
	if !ledger.Snapshot().Estimated {
		t.Fatal("a charged snapshot is not labelled an estimate")
	}
	var nilLedger *QuotaLedger
	if !nilLedger.Snapshot().Estimated {
		t.Fatal("the nil-ledger snapshot is not labelled an estimate")
	}
	if got := nilLedger.Cost(EndpointMessagesList); got != 5 {
		t.Fatalf("nil ledger Cost = %d, want the built-in table's figure", got)
	}
	if got := nilLedger.Charge(EndpointMessagesList); got != 0 {
		t.Fatalf("nil ledger Charge = %d, want 0", got)
	}
	if got := nilLedger.Remaining(); got != 0 {
		t.Fatalf("nil ledger Remaining = %d, want 0", got)
	}
}

// A cost override of zero or a negative number is a config mistake, not an
// instruction that a call is free. Charging nothing would make the meter read
// as if the poll loop were not running.
func TestNoEndpointCanEverBeChargedNothing(t *testing.T) {
	table := CostTable{
		EndpointMessagesList:   0,
		EndpointMessagesInsert: -50,
	}
	for _, endpoint := range []string{EndpointMessagesList, EndpointMessagesInsert, "some.methodNobodyHasHeardOf"} {
		if got := table.Cost(endpoint); got < 1 {
			t.Errorf("Cost(%q) = %d, want at least 1; a dispatched call is never free", endpoint, got)
		}
	}

	now := pacificTime(t, 2026, time.August, 8, 12, 0)
	ledger := NewQuotaLedger(LedgerConfig{Costs: table, Now: fixedClock(&now)})
	if got := ledger.Charge(EndpointMessagesList); got != 1 {
		t.Fatalf("Charge with a zero override = %d, want the 1-unit floor", got)
	}
}

// --- ledger record retention -----------------------------------------------

// Prune must stop when its context does. The sweep runs with its own deadline
// so a hung filesystem cannot keep a goroutine alive for the length of a
// stream; that deadline only means anything if the loop honours it.
func TestPruneStopsWhenItsContextExpires(t *testing.T) {
	root := t.TempDir()
	for day := 1; day <= 40; day++ {
		writeLedgerRecord(t, root, "aaaaaaaaaaaaaaaa", pacificDayOffset(-day-LedgerRetentionDays))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewFileLedgerStore(root).Prune(ctx, LedgerRetentionDays)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prune error = %v, want context.Canceled", err)
	}

	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatalf("read ledger directory: %v", readErr)
	}
	if len(entries) != 40 {
		t.Fatalf("%d records remain, want the sweep to have stopped before deleting any", len(entries))
	}
}

// A retention window of zero or less keeps only today, which is all the meter
// itself ever reads.
func TestPruneWithNoWindowKeepsOnlyToday(t *testing.T) {
	for _, keepDays := range []int{0, -1, -7} {
		t.Run("keepDays="+strconv.Itoa(keepDays), func(t *testing.T) {
			root := t.TempDir()
			today := writeLedgerRecord(t, root, "aaaaaaaaaaaaaaaa", pacificDayOffset(0))
			yesterday := writeLedgerRecord(t, root, "aaaaaaaaaaaaaaaa", pacificDayOffset(-1))

			if err := NewFileLedgerStore(root).Prune(context.Background(), keepDays); err != nil {
				t.Fatalf("Prune error = %v", err)
			}
			if _, err := os.Stat(today); err != nil {
				t.Errorf("today's record was swept: %v", err)
			}
			if _, err := os.Stat(yesterday); err == nil {
				t.Error("yesterday's record survived a zero-day window")
			}
		})
	}
}

// The ledger directory is inside the cache directory yc shares with everything
// else it caches. A sweep that deleted by exclusion rather than by pattern
// would be one bug away from removing something that matters, so anything that
// is not a dated .json record is left strictly alone - including directories,
// and including a name that merely ends in .json.
func TestPruneTouchesNothingItDoesNotRecognize(t *testing.T) {
	root := t.TempDir()
	stale := writeLedgerRecord(t, root, "aaaaaaaaaaaaaaaa", "2019-01-01")

	// Files whose names are close enough to a record to be tempting, and one
	// directory - the sweep skips directories, so a nested store from a
	// future layout is not silently emptied.
	survivors := []string{
		"notes.txt",
		"config.json",
		"2019-01-01",              // dated, but not a .json record
		"aaaa-2019-1-1.json",      // not the zero-padded shape
		"aaaa-not-a-date.json",    // .json, no date at all
		"aaaa-2019-01-01.json.gz", // a record name with another suffix
	}
	for _, name := range survivors {
		if err := os.WriteFile(filepath.Join(root, name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	nested := filepath.Join(root, "aaaa-2019-01-01.json.d")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}

	if err := NewFileLedgerStore(root).Prune(context.Background(), LedgerRetentionDays); err != nil {
		t.Fatalf("Prune error = %v", err)
	}

	if _, err := os.Stat(stale); err == nil {
		t.Error("the one real stale record survived; the sweep is not doing its job")
	}
	for _, name := range append(survivors, filepath.Base(nested)) {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("Prune deleted %s, which is not a ledger record: %v", name, err)
		}
	}
}

// A store with no root is a machine with no usable cache directory. It is a
// supported configuration - the meter simply stops persisting - and the sweep
// must be a no-op rather than an error the caller has to think about.
func TestPruneWithNoRootIsANoOp(t *testing.T) {
	for _, root := range []string{"", "   "} {
		if err := NewFileLedgerStore(root).Prune(context.Background(), LedgerRetentionDays); err != nil {
			t.Errorf("Prune with root %q = %v, want nil", root, err)
		}
	}
}

// A root that is a file rather than a directory is reported, not ignored: it
// means something else has taken the path yc writes its meter to, and the
// caller logs it. What it must not do is panic or delete anything.
func TestPruneOnAFileRootReportsTheProblem(t *testing.T) {
	root := filepath.Join(t.TempDir(), "quota")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write file at the ledger root: %v", err)
	}
	if err := NewFileLedgerStore(root).Prune(context.Background(), LedgerRetentionDays); err == nil {
		t.Fatal("Prune on a file root reported success")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("Prune removed the file at its root: %v", err)
	}
}

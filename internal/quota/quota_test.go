package quota

import (
	"path/filepath"
	"testing"
	"time"
)

// fixedClock returns a clock the ledger can be driven by without a wall clock.
func fixedClock(at *time.Time) func() time.Time {
	return func() time.Time { return *at }
}

func TestChargeTalliesUnitsPerEndpoint(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	ledger := NewLedger(Config{Now: fixedClock(&now)})

	ledger.Charge(EndpointMessagesList)
	ledger.Charge(EndpointMessagesList)
	ledger.Charge(EndpointVideosList)

	snapshot := ledger.Snapshot()
	if snapshot.UsedUnits != 11 {
		t.Fatalf("UsedUnits = %d, want 11 (5+5+1)", snapshot.UsedUnits)
	}
	if snapshot.RemainingUnits != DefaultDailyUnits-11 {
		t.Fatalf("RemainingUnits = %d, want %d", snapshot.RemainingUnits, DefaultDailyUnits-11)
	}
	if snapshot.ByEndpoint[EndpointMessagesList] != 10 {
		t.Fatalf("list tally = %d, want 10", snapshot.ByEndpoint[EndpointMessagesList])
	}
	if !snapshot.Estimated {
		t.Fatal("Estimated is false; every quota figure yc reports is an estimate")
	}
}

func TestSearchSpendsItsOwnBucketAndNotTheMainPool(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	ledger := NewLedger(Config{Now: fixedClock(&now)})

	ledger.Charge(EndpointSearchList)
	ledger.Charge(EndpointSearchList)

	snapshot := ledger.Snapshot()
	// The 2026-06-01 granular quota change moved search.list into its own
	// 100-calls-per-day Search Queries bucket, at 1 unit per call:
	// https://developers.google.com/youtube/v3/docs/search/list
	// Charging it against the chat budget - as the pre-2026 100-unit model
	// would - tells the user they have less room for chat than they do.
	if snapshot.UsedUnits != 0 {
		t.Fatalf("UsedUnits = %d, want 0; search must not spend the main pool", snapshot.UsedUnits)
	}
	if snapshot.SearchUsed != 2 {
		t.Fatalf("SearchUsed = %d, want 2", snapshot.SearchUsed)
	}
	if snapshot.SearchLimit != DefaultSearchCalls {
		t.Fatalf("SearchLimit = %d, want %d", snapshot.SearchLimit, DefaultSearchCalls)
	}
}

// TestPublishedCostsMatchGooglesTable pins the endpoints Google actually
// documents, so a future edit cannot quietly drift them back to folklore.
// Figures from https://developers.google.com/youtube/v3/determine_quota_cost
// (search.list from its own reference page, which states the bucket).
func TestPublishedCostsMatchGooglesTable(t *testing.T) {
	costs := DefaultCostTable()
	for _, tc := range []struct {
		endpoint string
		want     int
		why      string
	}{
		{EndpointVideosList, 1, "videos.list is a published 1-unit read"},
		{EndpointChannelsList, 1, "channels.list is a published 1-unit read"},
		{EndpointSubscriptions, 1, "subscriptions.list is a published 1-unit read"},
		{EndpointCategoriesList, 1, "videoCategories.list is a published 1-unit read"},
		{EndpointVideosUpdate, 50, "videos.update is a published 50-unit write"},
		{EndpointSearchList, 1, "search.list is 1 unit in the Search Queries bucket, not 100 from the main pool"},
	} {
		if got := costs.Cost(tc.endpoint); got != tc.want {
			t.Errorf("Cost(%s) = %d, want %d: %s", tc.endpoint, got, tc.want, tc.why)
		}
	}
}

// TestLiveChatCostsFollowThePublishedReadWriteRule guards the estimates Google
// does not publish. It cannot prove them right - only that they still follow
// the documented "reads are cheap, writes cost 50" shape rather than having
// drifted into an arbitrary number.
func TestLiveChatCostsFollowThePublishedReadWriteRule(t *testing.T) {
	costs := DefaultCostTable()
	for _, endpoint := range []string{
		EndpointMessagesInsert, EndpointMessagesDelete,
		EndpointBansInsert, EndpointBansDelete,
	} {
		if got := costs.Cost(endpoint); got != 50 {
			t.Errorf("Cost(%s) = %d, want 50 (published write-operation cost)", endpoint, got)
		}
	}
	// The read is the outlier: 5 rather than 1, community-observed, and the
	// number the entire budget arithmetic rests on.
	if got := costs.Cost(EndpointMessagesList); got != 5 {
		t.Errorf("Cost(%s) = %d, want the estimated 5", EndpointMessagesList, got)
	}
}

func TestConfiguredCostsOverrideTheEstimates(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	costs := DefaultCostTable()
	costs[EndpointMessagesList] = 3
	ledger := NewLedger(Config{Now: fixedClock(&now), Costs: costs})

	if charged := ledger.Charge(EndpointMessagesList); charged != 3 {
		t.Fatalf("charged = %d, want the configured 3", charged)
	}
	if cost := ledger.Cost(EndpointMessagesList); cost != 3 {
		t.Fatalf("Cost = %d, want 3", cost)
	}
}

func TestUnknownEndpointStillMovesTheLedger(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	ledger := NewLedger(Config{Now: fixedClock(&now)})

	// A method with no published cost still costs something, so an unmetered
	// call must never read as free.
	if charged := ledger.Charge("some.futureMethod"); charged != 1 {
		t.Fatalf("charged = %d, want 1 for an unknown method", charged)
	}
}

func TestLedgerRollsOverAtPacificMidnight(t *testing.T) {
	// 06:59 UTC on 2026-08-09 is 23:59 Pacific on the 8th; one minute later
	// is a new Pacific day and a fresh allowance.
	now := time.Date(2026, 8, 9, 6, 59, 0, 0, time.UTC)
	ledger := NewLedger(Config{Now: fixedClock(&now)})
	ledger.Charge(EndpointMessagesList)

	if used := ledger.Snapshot().UsedUnits; used != 5 {
		t.Fatalf("UsedUnits before rollover = %d, want 5", used)
	}

	now = now.Add(2 * time.Minute)
	if used := ledger.Snapshot().UsedUnits; used != 0 {
		t.Fatalf("UsedUnits after Pacific midnight = %d, want 0", used)
	}
}

func TestResetAtIsDaylightSavingCorrect(t *testing.T) {
	loc := pacificLocation()
	for _, tc := range []struct {
		name string
		now  time.Time
	}{
		// The 2026 US transitions: forward on 8 March, back on 1 November.
		{"before spring forward", time.Date(2026, 3, 7, 12, 0, 0, 0, loc)},
		{"before fall back", time.Date(2026, 10, 31, 12, 0, 0, 0, loc)},
		{"ordinary summer day", time.Date(2026, 8, 8, 12, 0, 0, 0, loc)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset := ResetAt(tc.now)
			local := reset.In(loc)
			if local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 {
				t.Fatalf("reset = %s, want midnight Pacific", local)
			}
			if got, want := local.Day(), tc.now.AddDate(0, 0, 1).Day(); got != want {
				t.Fatalf("reset day = %d, want %d", got, want)
			}
			if !reset.After(tc.now) {
				t.Fatalf("reset %s is not after %s", reset, tc.now)
			}
		})
	}
}

func TestPacificDayIsTheStorageKey(t *testing.T) {
	// 07:30 UTC is 00:30 Pacific, so the day key must already have advanced.
	if got := PacificDay(time.Date(2026, 8, 9, 7, 30, 0, 0, time.UTC)); got != "2026-08-09" {
		t.Fatalf("PacificDay = %q, want 2026-08-09", got)
	}
	if got := PacificDay(time.Date(2026, 8, 9, 6, 30, 0, 0, time.UTC)); got != "2026-08-08" {
		t.Fatalf("PacificDay = %q, want 2026-08-08", got)
	}
}

func TestBudgetFloorStretchesTheDayOut(t *testing.T) {
	// The arithmetic that defines this client: 10,000 units at an estimated
	// 5 per poll is 2000 polls, which over 24 hours is one poll every 43.2s.
	floor := BudgetFloor(10000, 5, 24*time.Hour)
	if want := 43200 * time.Millisecond; floor != want {
		t.Fatalf("BudgetFloor = %v, want %v", floor, want)
	}
}

func TestBudgetFloorReportsNoConstraintWhenThereIsNone(t *testing.T) {
	if got := BudgetFloor(10000, 5, 0); got != 0 {
		t.Fatalf("BudgetFloor with no horizon = %v, want 0", got)
	}
	if got := BudgetFloor(10000, 0, time.Hour); got != 0 {
		t.Fatalf("BudgetFloor with no cost = %v, want 0", got)
	}
}

func TestBudgetFloorWithNothingLeftClaimsTheWholeHorizon(t *testing.T) {
	// Fewer units left than one poll costs: any poll at all overspends, so
	// the honest floor is the whole remaining horizon.
	if got, want := BudgetFloor(2, 5, 2*time.Hour), 2*time.Hour; got != want {
		t.Fatalf("BudgetFloor = %v, want %v", got, want)
	}
}

func TestLedgerSurvivesARestart(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	fingerprint := CredentialFingerprint("client-id", "UC-account")
	newLedger := func() *Ledger {
		return NewLedger(Config{
			Now:         fixedClock(&now),
			Store:       NewFileStore(filepath.Join(root, "quota")),
			Fingerprint: fingerprint,
		})
	}

	first := newLedger()
	first.Charge(EndpointMessagesList)
	first.Charge(EndpointSearchList)

	// A restart that zeroes the meter hands the user a false sense of budget,
	// which they discover by having chat stop mid-stream.
	second := newLedger()
	snapshot := second.Snapshot()
	if snapshot.UsedUnits != 5 {
		t.Fatalf("UsedUnits after restart = %d, want 5", snapshot.UsedUnits)
	}
	if snapshot.SearchUsed != 1 {
		t.Fatalf("SearchUsed after restart = %d, want 1", snapshot.SearchUsed)
	}
	if _, ok := snapshot.ByEndpoint[ledgerSearchCallsKey]; ok {
		t.Fatal("the reserved search-call key leaked into the endpoint tally")
	}
}

func TestLedgerDoesNotShareAMeterBetweenAccounts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "quota")
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	store := NewFileStore(root)

	mine := NewLedger(Config{Now: fixedClock(&now), Store: store, Fingerprint: CredentialFingerprint("client", "UC-me")})
	mine.Charge(EndpointMessagesList)

	theirs := NewLedger(Config{Now: fixedClock(&now), Store: store, Fingerprint: CredentialFingerprint("client", "UC-them")})
	if used := theirs.Snapshot().UsedUnits; used != 0 {
		t.Fatalf("second account inherited %d units from the first", used)
	}
}

func TestLedgerDoesNotInheritYesterdaysSpend(t *testing.T) {
	root := filepath.Join(t.TempDir(), "quota")
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	store := NewFileStore(root)

	yesterday := NewLedger(Config{Now: fixedClock(&now), Store: store, Fingerprint: "abcdef0123456789"})
	yesterday.Charge(EndpointMessagesInsert)

	tomorrow := now.AddDate(0, 0, 1)
	today := NewLedger(Config{Now: fixedClock(&tomorrow), Store: store, Fingerprint: "abcdef0123456789"})
	if used := today.Snapshot().UsedUnits; used != 0 {
		t.Fatalf("UsedUnits = %d, want 0; the allowance is daily", used)
	}
}

func TestCredentialFingerprintIsAHashAndNotTheInput(t *testing.T) {
	const clientID = "1234567890-abcdefg.apps.googleusercontent.com"
	fingerprint := CredentialFingerprint(clientID, "UC-account")

	// The fingerprint becomes a filename, and a filename is not a place a
	// credential-adjacent identifier may appear verbatim.
	if fingerprint == clientID || fingerprint == "UC-account" {
		t.Fatal("fingerprint echoes its input")
	}
	if !ledgerFingerprintPattern.MatchString(fingerprint) {
		t.Fatalf("fingerprint %q is not the expected hex shape", fingerprint)
	}
	if again := CredentialFingerprint(clientID, "UC-account"); again != fingerprint {
		t.Fatal("fingerprint is not stable across calls")
	}
	if other := CredentialFingerprint(clientID, "UC-other"); other == fingerprint {
		t.Fatal("two accounts share a fingerprint")
	}
	if got := CredentialFingerprint("", ""); got != anonymousFingerprint {
		t.Fatalf("empty fingerprint = %q, want %q", got, anonymousFingerprint)
	}
}

func TestLedgerStoreRefusesAnUnsafeKey(t *testing.T) {
	store := NewFileStore(t.TempDir())
	if err := store.SaveLedger("../../etc", "2026-08-08", map[string]int{}); err == nil {
		t.Fatal("SaveLedger accepted a traversal-shaped fingerprint")
	}
	if err := store.SaveLedger("abcdef0123456789", "../../etc/passwd", map[string]int{}); err == nil {
		t.Fatal("SaveLedger accepted a traversal-shaped day")
	}
}

func TestLedgerStorePrunesOldRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "quota")
	store := NewFileStore(root)
	const fingerprint = "abcdef0123456789"

	old := time.Now().AddDate(0, 0, -30).In(pacificLocation()).Format("2006-01-02")
	today := PacificDay(time.Now())
	if err := store.SaveLedger(fingerprint, old, map[string]int{EndpointMessagesList: 5}); err != nil {
		t.Fatalf("SaveLedger(old) error = %v", err)
	}
	if err := store.SaveLedger(fingerprint, today, map[string]int{EndpointMessagesList: 5}); err != nil {
		t.Fatalf("SaveLedger(today) error = %v", err)
	}

	if err := store.Prune(t.Context(), 7); err != nil {
		t.Fatalf("Prune error = %v", err)
	}
	if tally, _ := store.LoadLedger(fingerprint, old); len(tally) != 0 {
		t.Fatalf("old record survived pruning: %v", tally)
	}
	if tally, _ := store.LoadLedger(fingerprint, today); tally[EndpointMessagesList] != 5 {
		t.Fatal("today's record was pruned")
	}
}

func TestMissingLedgerRecordIsAnEmptyTallyAndNotAnError(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "quota"))
	tally, err := store.LoadLedger("abcdef0123456789", "2026-08-08")
	if err != nil {
		t.Fatalf("LoadLedger error = %v, want nil for a missing record", err)
	}
	if len(tally) != 0 {
		t.Fatalf("tally = %v, want empty", tally)
	}
}

func TestProjectedExhaustionAnswersTheOnlyQuestionThatMatters(t *testing.T) {
	snapshot := Snapshot{RemainingUnits: 6000, EffectiveInterval: 5 * time.Second}
	remaining, ok := snapshot.ProjectedExhaustion(5)
	if !ok {
		t.Fatal("ProjectedExhaustion reported no projection")
	}
	// 1200 polls at five seconds each: the budget does not survive two hours.
	if want := 100 * time.Minute; remaining != want {
		t.Fatalf("ProjectedExhaustion = %v, want %v", remaining, want)
	}
}

package quota

import (
	"sort"
	"sync"
	"time"

	_ "time/tzdata" // embed the tz database so America/Los_Angeles always resolves
)

// Endpoint names the API methods yc charges to the quota ledger. They are
// strings rather than an enum so a ledger persisted by an older build still
// reads back, and so `yc quota` can print an unknown key it finds on disk.
const (
	EndpointMessagesList   = "liveChatMessages.list"
	EndpointMessagesInsert = "liveChatMessages.insert"
	EndpointMessagesDelete = "liveChatMessages.delete"
	EndpointBansInsert     = "liveChatBans.insert"
	EndpointBansDelete     = "liveChatBans.delete"
	EndpointVideosList     = "videos.list"
	EndpointChannelsList   = "channels.list"
	EndpointSubscriptions  = "subscriptions.list"
	EndpointSearchList     = "search.list"
	EndpointCategoriesList = "videoCategories.list"
	EndpointVideosUpdate   = "videos.update"
)

// ResetLocation is the timezone the daily allowance resets in.
const ResetLocation = "America/Los_Angeles"

// Fallback offset for the daily reset when the tz database cannot be loaded at
// all. It is standard Pacific time, so a build without tzdata drifts by an hour
// through daylight saving rather than by a whole day.
const pacificFallbackOffsetSeconds = -8 * 60 * 60

// DefaultDailyUnits and DefaultSearchCalls are the published defaults for a
// new Cloud project. Google states them together:
//
//	"Projects that enable the YouTube Data API have a default quota allocation
//	 of 100 search.list calls, 100 videos.insert calls, and 10,000 units per day
//	 combined for all other endpoints."
//
// https://developers.google.com/youtube/v3/getting-started#quota
const (
	DefaultDailyUnits  = 10000
	DefaultSearchCalls = 100
)

// ledgerSearchCallsKey is the reserved persistence key for the search.list call
// count. It is stripped out of every snapshot, so it can never be mistaken for
// an endpoint in `yc quota` output.
const ledgerSearchCallsKey = "__search_calls"

// CostTable is the per-method unit cost table.
//
// Costs split into two classes, and the distinction is why this table is data
// rather than constants:
//
// Published. Every Data API method yc calls outside live chat has an official
// figure, and each one below matches it:
// https://developers.google.com/youtube/v3/determine_quota_cost
//
// Unpublished. Google documents no quota cost for any live chat method. The
// cost table lists none of liveChat, liveBroadcasts, liveStreams, or
// superChatEvents, and the individual reference pages for
// liveChatMessages.list, liveChatMessages.insert, and liveChatBans.insert carry
// no "Quota impact" line at all, despite the cost page's prose asserting that
// Live Streaming API methods incur the same costs as other methods. The live
// chat values here are community-observed. They are consistent with the
// published rule of thumb - "a read operation that retrieves a list of
// resources usually costs 1 unit [...] a write operation that creates, updates,
// or deletes a resource usually costs 50 units"
// (https://developers.google.com/youtube/v3/getting-started#quota) - but a
// consistent guess is still a guess, so they are config-overridable and are
// labeled as estimates everywhere they surface.
type CostTable map[string]int

// DefaultCostTable returns the built-in costs. Everything except the five
// liveChat entries is an officially published figure.
func DefaultCostTable() CostTable {
	return CostTable{
		EndpointMessagesList:   5,
		EndpointMessagesInsert: 50,
		EndpointMessagesDelete: 50,
		EndpointBansInsert:     50,
		EndpointBansDelete:     50,
		EndpointVideosList:     1,
		EndpointChannelsList:   1,
		EndpointSubscriptions:  1,
		EndpointCategoriesList: 1,
		EndpointVideosUpdate:   50,
		// search.list is not 100 units. The 2026-06-01 granular quota change
		// moved it into its own bucket, and its reference page now reads:
		// "Quota impact: 100 calls per day. A call to this method has a quota
		// cost of 1 unit in the Search Queries quota bucket."
		// https://developers.google.com/youtube/v3/docs/search/list
		// The 100-units-from-the-main-pool figure most third-party references
		// still quote describes the pre-2026 model. Charge() keeps this out of
		// the main pool accordingly. It is still scarce, so it stays opt-in.
		EndpointSearchList: 1,
	}
}

// IsPublishedCost reports whether Google publishes an official unit cost for an
// endpoint.
//
// It exists so every surface that prints a cost can say which class it is in.
// Calling the whole table "estimated" was the earlier shorthand and it was
// false: it invited a user to distrust six figures that are documented, and it
// hid the fact that the one number the entire poll budget rests on -
// liveChatMessages.list - is the guess.
func IsPublishedCost(endpoint string) bool {
	switch endpoint {
	case EndpointVideosList, EndpointChannelsList, EndpointSubscriptions,
		EndpointCategoriesList, EndpointVideosUpdate, EndpointSearchList:
		return true
	default:
		// Every liveChat method, and anything yc has not classified yet. An
		// unknown endpoint is reported as unpublished because that is the
		// claim that cannot be wrong.
		return false
	}
}

// Cost returns the estimated unit cost of a method, defaulting to 1 for an
// unknown method so an unmetered call still moves the ledger.
func (t CostTable) Cost(endpoint string) int {
	if cost, ok := t[endpoint]; ok && cost > 0 {
		return cost
	}
	return 1
}

// Config configures a Ledger.
type Config struct {
	DailyUnits  int
	SearchCalls int
	Costs       CostTable
	// Now is injectable so the Pacific-midnight rollover and the DST
	// boundary can be tested without a wall clock.
	Now func() time.Time
	// Store persists the ledger across restarts. Nil keeps it in memory,
	// which means a restart hands the user a false sense of budget - so the
	// live path always supplies one.
	Store Store
	// Fingerprint keys the persisted ledger to a credential, so two accounts
	// on one machine do not share a meter.
	Fingerprint string
}

// Store persists the estimated ledger through internal/storage, keyed by
// credential fingerprint and Pacific date.
type Store interface {
	LoadLedger(fingerprint string, day string) (map[string]int, error)
	SaveLedger(fingerprint string, day string, byEndpoint map[string]int) error
}

// Ledger accumulates estimated unit spend for the current Pacific day.
//
// Every dispatched request is charged, including failures, because Google
// charges at least one unit for an invalid request. It is safe for concurrent
// use by the poll loop and the send path.
type Ledger struct {
	cfg Config

	mu          sync.Mutex
	day         string
	byEndpoint  map[string]int
	used        int
	searchCalls int
}

// NewLedger returns a ledger with defaults applied and any persisted spend
// for today already loaded.
func NewLedger(cfg Config) *Ledger {
	if cfg.DailyUnits <= 0 {
		cfg.DailyUnits = DefaultDailyUnits
	}
	if cfg.SearchCalls <= 0 {
		cfg.SearchCalls = DefaultSearchCalls
	}
	if len(cfg.Costs) == 0 {
		cfg.Costs = DefaultCostTable()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	ledger := &Ledger{cfg: cfg, byEndpoint: map[string]int{}}
	ledger.rollover(PacificDay(cfg.Now()))
	return ledger
}

// Cost returns the estimated unit cost of an endpoint under this ledger's
// table, without charging it.
func (l *Ledger) Cost(endpoint string) int {
	if l == nil {
		return DefaultCostTable().Cost(endpoint)
	}
	return l.cfg.Costs.Cost(endpoint)
}

// Charge records one dispatched call against endpoint and returns the units
// charged.
func (l *Ledger) Charge(endpoint string) int {
	if l == nil {
		return 0
	}
	cost := l.cfg.Costs.Cost(endpoint)

	l.mu.Lock()
	if day := PacificDay(l.cfg.Now()); day != l.day {
		l.rolloverLocked(day)
	}
	l.byEndpoint[endpoint] += cost
	// search.list is metered separately: since the 2026-06-01 granular change
	// it spends one call from its own 100-per-day Search Queries bucket rather
	// than units from the main pool, so folding it into the main total would
	// overstate the spend that actually matters. The two buckets deplete
	// independently - search can 403 with the main pool nearly untouched.
	if endpoint == EndpointSearchList {
		l.searchCalls++
	} else {
		l.used += cost
	}
	snapshot := copyTally(l.byEndpoint)
	snapshot[ledgerSearchCallsKey] = l.searchCalls
	day := l.day
	l.mu.Unlock()

	l.persist(day, snapshot)
	return cost
}

// Snapshot returns the current ledger state. Cadence fields are filled in by
// the poller, which owns the floors.
func (l *Ledger) Snapshot() Snapshot {
	if l == nil {
		return Snapshot{Estimated: true}
	}
	now := l.cfg.Now()

	l.mu.Lock()
	if day := PacificDay(now); day != l.day {
		l.rolloverLocked(day)
	}
	snapshot := Snapshot{
		UsedUnits:      l.used,
		LimitUnits:     l.cfg.DailyUnits,
		RemainingUnits: l.cfg.DailyUnits - l.used,
		SearchUsed:     l.searchCalls,
		SearchLimit:    l.cfg.SearchCalls,
		ByEndpoint:     copyTally(l.byEndpoint),
		ResetAt:        ResetAt(now),
		Estimated:      true,
		At:             now,
	}
	l.mu.Unlock()

	if snapshot.RemainingUnits < 0 {
		snapshot.RemainingUnits = 0
	}
	return snapshot
}

// Remaining returns the estimated units left in the main bucket.
func (l *Ledger) Remaining() int {
	return l.Snapshot().RemainingUnits
}

// rollover replaces the tally with whatever was persisted for day.
func (l *Ledger) rollover(day string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rolloverLocked(day)
}

// rolloverLocked resets the tally to day's persisted spend. Yesterday's spend
// is deliberately not carried over: the allowance is daily, and a stale total
// would understate today's budget as badly as a zeroed one overstates it.
func (l *Ledger) rolloverLocked(day string) {
	l.day = day
	l.byEndpoint = map[string]int{}
	l.used = 0
	l.searchCalls = 0

	if l.cfg.Store == nil {
		return
	}
	stored, err := l.cfg.Store.LoadLedger(l.cfg.Fingerprint, day)
	if err != nil {
		// A ledger that cannot be read is a degraded meter, not a broken
		// startup: the session still counts its own spend.
		return
	}
	for endpoint, units := range stored {
		if units <= 0 {
			continue
		}
		if endpoint == ledgerSearchCallsKey {
			l.searchCalls = units
			continue
		}
		l.byEndpoint[endpoint] = units
		if endpoint != EndpointSearchList {
			l.used += units
		}
	}
}

// persist writes the tally through the store, best effort.
func (l *Ledger) persist(day string, tally map[string]int) {
	if l.cfg.Store == nil {
		return
	}
	_ = l.cfg.Store.SaveLedger(l.cfg.Fingerprint, day, tally)
}

// copyTally returns an independent copy so a snapshot cannot be mutated by a
// later charge.
func copyTally(tally map[string]int) map[string]int {
	out := make(map[string]int, len(tally))
	for key, value := range tally {
		out[key] = value
	}
	return out
}

// SortedEndpoints returns the endpoints in a tally in a stable order, for
// output that must not reshuffle between runs.
func SortedEndpoints(tally map[string]int) []string {
	endpoints := make([]string, 0, len(tally))
	for endpoint := range tally {
		if endpoint == ledgerSearchCallsKey {
			continue
		}
		endpoints = append(endpoints, endpoint)
	}
	sort.Strings(endpoints)
	return endpoints
}

// pacificLocation returns the reset timezone, falling back to a fixed offset.
//
// The tz database is embedded, so the fallback should be unreachable; it exists
// because getting this wrong silently moves the reset boundary, and a ledger
// that resets at the wrong hour is worse than no ledger at all.
func pacificLocation() *time.Location {
	if loc, err := time.LoadLocation(ResetLocation); err == nil && loc != nil {
		return loc
	}
	return time.FixedZone("PST", pacificFallbackOffsetSeconds)
}

// ResetAt returns the next midnight in America/Los_Angeles.
//
// It is DST-correct through the embedded tz database; if the location cannot be
// loaded at all it falls back to a fixed -08:00 offset and the caller warns,
// rather than silently drifting by an hour twice a year.
func ResetAt(now time.Time) time.Time {
	loc := pacificLocation()
	local := now.In(loc)
	// Adding 24h to midnight would land on 23:00 or 01:00 across a DST
	// boundary. Constructing the next calendar day's midnight directly lets
	// the location resolve the offset itself.
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc)
}

// PacificDay returns the YYYY-MM-DD key the ledger is persisted under.
func PacificDay(now time.Time) string {
	return now.In(pacificLocation()).Format("2006-01-02")
}

// BudgetFloor returns the minimum poll interval that makes remaining units last
// until horizon.
//
// This is the mechanism that lets yc survive a stream. At an estimated 5 units
// per call against a 10,000-unit day, polling at YouTube's own ~5s cadence
// exhausts the entire allowance in under three hours, while lasting a full day
// needs roughly a 43-second interval. Returning zero means "no budget
// constraint" - the caller then uses the server floor alone.
func BudgetFloor(remainingUnits, costPerPoll int, until time.Duration) time.Duration {
	if until <= 0 || costPerPoll <= 0 {
		return 0
	}
	polls := remainingUnits / costPerPoll
	if polls < 1 {
		// Nothing left to spend: the floor becomes the whole horizon, which
		// is the honest answer - any poll at all overspends.
		polls = 1
	}
	return until / time.Duration(polls)
}

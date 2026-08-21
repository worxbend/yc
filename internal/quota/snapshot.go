package quota

import "time"

// Mode describes why the poll loop is running at its current cadence.
type Mode string

const (
	// ModeIdle means no chat has polled yet today, so no cadence is in force.
	//
	// It is the empty string, which makes it Mode's zero value: Ledger.Snapshot
	// leaves the field unset and the poller fills it in once it is running.
	// "Idle" is a real state users see - `yc quota` prints it and the Quota tab
	// describes it - so it is named here rather than left as the thing every
	// switch reaches in its default arm.
	ModeIdle Mode = ""
	// ModeLive means the poll cadence is the server-advised floor.
	ModeLive Mode = "live"
	// ModeStretched means the budget floor exceeded the server floor,
	// so yc is polling slower than YouTube allows in order to survive the
	// quota day. The status bar says so explicitly.
	ModeStretched Mode = "stretched"
	// ModeBackoff means an error ladder is in effect.
	ModeBackoff Mode = "backoff"
	// ModePaused means polling stopped: quota exhausted or the reserve
	// threshold tripped.
	ModePaused Mode = "paused"
)

// Snapshot is an immutable view of the estimated quota ledger plus the
// cadence it currently implies.
//
// Estimated is not decoration. Google publishes no quota costs for any live
// chat method - the documented cost table contains zero liveChat rows - so
// every unit figure yc shows is a community-observed estimate from a
// config-overridable table. Every UI surface that shows a number from this
// snapshot must also show that it is an estimate.
type Snapshot struct {
	UsedUnits      int
	LimitUnits     int
	RemainingUnits int

	// SearchUsed and SearchLimit track search.list separately: since the
	// 2026-06-01 granular quota migration it costs 1 unit from its own
	// 100-calls-per-day bucket rather than 100 units from the main pool.
	SearchUsed  int
	SearchLimit int

	// ByEndpoint is the per-method tally shown by `yc quota` and the quota
	// tab. Nil is a valid empty tally.
	ByEndpoint map[string]int

	// ResetAt is the next midnight in America/Los_Angeles.
	ResetAt time.Time

	// EffectiveInterval is the cadence actually in use after the server
	// floor, budget floor, config clamps, backoff, and jitter.
	EffectiveInterval time.Duration
	// ServerFloor is pollingIntervalMillis. It is an absolute floor: yc must
	// never poll faster, under any circumstance.
	ServerFloor time.Duration
	// BudgetFloor is the cadence needed to reach ResetAt on the remaining
	// units.
	BudgetFloor time.Duration

	Mode Mode
	// Estimated is always true today. It exists so the label is a property
	// of the data rather than a hard-coded string in the view.
	Estimated bool
	At        time.Time
}

// RemainingPercent returns remaining units as a percentage of the daily limit,
// or 0 when no limit is known.
func (q Snapshot) RemainingPercent() float64 {
	if q.LimitUnits <= 0 {
		return 0
	}
	return float64(q.RemainingUnits) / float64(q.LimitUnits) * 100
}

// ProjectedExhaustion returns how long the remaining units last at the current
// effective cadence, and whether that projection is meaningful. It is the
// number that tells a viewer whether they will make it to the end of the
// stream.
func (q Snapshot) ProjectedExhaustion(costPerPoll int) (time.Duration, bool) {
	if costPerPoll <= 0 || q.EffectiveInterval <= 0 || q.RemainingUnits <= 0 {
		return 0, false
	}
	polls := q.RemainingUnits / costPerPoll
	if polls <= 0 {
		return 0, true
	}
	return time.Duration(polls) * q.EffectiveInterval, true
}

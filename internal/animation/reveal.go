package animation

import (
	"time"

	"github.com/rivo/uniseg"
	"github.com/worxbend/yc/internal/render"
)

// DefaultMaxQueued bounds how many messages may be mid-reveal at once. Chat is
// unbounded upstream, so an unbounded queue turns a busy moment into a backlog
// the viewer watches minutes after it happened.
const DefaultMaxQueued = 32

const (
	defaultFastInterval        = 20 * time.Millisecond
	defaultReducedInterval     = 80 * time.Millisecond
	defaultFastUnitsPerTick    = 1
	defaultReducedUnitsPerTick = 4
)

// Config tunes reveal pacing.
type Config struct {
	Mode                Mode
	MaxQueued           int
	FastInterval        time.Duration
	ReducedInterval     time.Duration
	FastUnitsPerTick    int
	ReducedUnitsPerTick int
}

// DefaultConfig returns the built-in reveal pacing.
func DefaultConfig() Config {
	return Config{
		Mode:                ModeFast,
		MaxQueued:           DefaultMaxQueued,
		FastInterval:        defaultFastInterval,
		ReducedInterval:     defaultReducedInterval,
		FastUnitsPerTick:    defaultFastUnitsPerTick,
		ReducedUnitsPerTick: defaultReducedUnitsPerTick,
	}
}

// RevealUnit is one atom of a typed-in message.
//
// Row and Fragment index back into the rows the unit was derived from rather
// than carrying a copy of the fragment, so a reveal frame always inherits the
// current styling of its source fragment.
type RevealUnit struct {
	Row      int
	Fragment int
	Text     string
}

// Units splits rendered rows into reveal atoms.
//
// Plain text splits into grapheme clusters, never bytes or runes, so emoji and
// combining marks stay intact. Semantic tokens - avatars, timestamps, badges,
// usernames, mentions, replies, notices, deleted markers, emoji and shortcode
// chips - stay atomic, along with anything carrying a background or a bold,
// italic, or strikethrough style, because revealing them a cell at a time would
// change their reserved width mid-animation.
func Units(rows []render.Row) []RevealUnit {
	units := make([]RevealUnit, 0)
	for rowIndex, row := range rows {
		for fragmentIndex, fragment := range row.Fragments {
			units = append(units, fragmentUnits(rowIndex, fragmentIndex, fragment)...)
		}
	}
	return units
}

// Sequence is one message's in-progress reveal.
type Sequence struct {
	rows         []render.Row
	units        []RevealUnit
	visibleUnits int
	lastAdvance  time.Time
	interval     time.Duration
	unitsPerTick int
}

// NewSequence starts a reveal of rows. In ModeOff it starts complete.
func NewSequence(rows []render.Row, cfg Config, now time.Time) Sequence {
	cfg = cfg.withDefaults()
	sequence := Sequence{
		rows:         cloneRows(rows),
		units:        Units(rows),
		lastAdvance:  now,
		interval:     cfg.interval(),
		unitsPerTick: cfg.unitsPerTick(),
	}
	if cfg.Mode == ModeOff || len(sequence.units) == 0 {
		sequence.visibleUnits = len(sequence.units)
	}
	return sequence
}

// Advance reveals any units due at now and reports whether anything changed.
func (s *Sequence) Advance(now time.Time) bool {
	if s.Done() {
		return false
	}
	// A degenerate pacing config would otherwise leave the message stuck
	// half-typed forever; finishing it is the only safe reading.
	if s.interval <= 0 || s.unitsPerTick <= 0 {
		s.Complete()
		return true
	}
	if now.Before(s.lastAdvance.Add(s.interval)) {
		return false
	}

	// Advance by whole intervals so a late frame catches up exactly rather
	// than resetting the phase and stretching the reveal.
	ticks := int(now.Sub(s.lastAdvance) / s.interval)
	s.lastAdvance = s.lastAdvance.Add(time.Duration(ticks) * s.interval)
	s.visibleUnits += ticks * s.unitsPerTick
	if s.visibleUnits > len(s.units) {
		s.visibleUnits = len(s.units)
	}
	return true
}

// Complete reveals everything immediately.
func (s *Sequence) Complete() {
	s.visibleUnits = len(s.units)
}

// Done reports whether every unit is visible.
func (s Sequence) Done() bool {
	return s.visibleUnits >= len(s.units)
}

// Frame returns the partially revealed rows, preserving each fragment's style
// and merging adjacent same-style fragments so a frame emits one escape per run.
func (s Sequence) Frame() []render.Row {
	if len(s.rows) == 0 {
		return nil
	}
	if s.Done() {
		return cloneRows(s.rows)
	}

	rows := make([]render.Row, len(s.rows))
	for i := 0; i < s.visibleUnits; i++ {
		unit := s.units[i]
		fragment, ok := s.fragmentFor(unit)
		if !ok {
			continue
		}
		appendFragment(&rows[unit.Row], fragment)
	}
	return rows
}

// fragmentFor rebuilds the styled fragment a unit stands for.
func (s Sequence) fragmentFor(unit RevealUnit) (render.Fragment, bool) {
	if unit.Row < 0 || unit.Row >= len(s.rows) {
		return render.Fragment{}, false
	}
	fragments := s.rows[unit.Row].Fragments
	if unit.Fragment < 0 || unit.Fragment >= len(fragments) {
		return render.Fragment{}, false
	}
	fragment := fragments[unit.Fragment]
	fragment.Text = unit.Text
	return fragment, true
}

// CompletionReason explains why a reveal finished.
type CompletionReason string

const (
	// CompletionFinished means the reveal played out.
	CompletionFinished CompletionReason = "finished"
	// CompletionOverflow means the queue was full and the oldest reveal was
	// completed early to make room. The caller renders it statically rather
	// than dropping the message.
	CompletionOverflow CompletionReason = "overflow"
)

// CompletedReveal reports one finished reveal back to the caller.
type CompletedReveal struct {
	ID     string
	Rows   []render.Row
	Reason CompletionReason
}

// EnqueueResult reports what Enqueue did.
type EnqueueResult struct {
	// Immediate is set when the reveal completed on the spot, which is what
	// ModeOff does without consuming queue capacity.
	Immediate bool
	Completed []CompletedReveal
	QueueSize int
}

// AdvanceResult reports what one tick did.
type AdvanceResult struct {
	Changed   bool
	Completed []CompletedReveal
	QueueSize int
}

// queuedReveal pairs a caller-supplied ID with its reveal state.
type queuedReveal struct {
	id       string
	sequence Sequence
}

// Queue is a bounded set of concurrent reveals.
type Queue struct {
	cfg       Config
	clock     Clock
	items     []queuedReveal
	overflows int
}

// NewQueue returns a queue with defaults applied. A nil clock reads the system
// clock; tests pass a fake one instead of sleeping.
func NewQueue(cfg Config, clock Clock) *Queue {
	cfg = cfg.withDefaults()
	if clock == nil {
		clock = SystemClock{}
	}
	return &Queue{cfg: cfg, clock: clock}
}

// Enqueue starts revealing rows under id. On overflow it completes the oldest
// reveal and hands it back rather than dropping a message.
func (q *Queue) Enqueue(id string, rows []render.Row) EnqueueResult {
	now := q.clock.Now()
	sequence := NewSequence(rows, q.cfg, now)
	if sequence.Done() {
		// ModeOff and empty rows never occupy capacity: nothing is going
		// to be animated, so nothing needs a slot.
		return EnqueueResult{
			Immediate: true,
			Completed: []CompletedReveal{completedReveal(id, &sequence, CompletionFinished)},
			QueueSize: len(q.items),
		}
	}

	result := EnqueueResult{}
	for len(q.items) >= q.cfg.MaxQueued {
		oldest := q.items[0]
		q.items = q.items[1:]
		result.Completed = append(result.Completed, completedReveal(oldest.id, &oldest.sequence, CompletionOverflow))
		q.overflows++
	}
	q.items = append(q.items, queuedReveal{id: id, sequence: sequence})
	result.QueueSize = len(q.items)
	return result
}

// Advance ticks every active reveal.
func (q *Queue) Advance() AdvanceResult {
	now := q.clock.Now()
	result := AdvanceResult{}
	active := q.items[:0]
	for i := range q.items {
		item := q.items[i]
		if item.sequence.Advance(now) {
			result.Changed = true
		}
		if item.sequence.Done() {
			result.Completed = append(result.Completed, completedReveal(item.id, &item.sequence, CompletionFinished))
			continue
		}
		active = append(active, item)
	}
	q.items = active
	result.QueueSize = len(q.items)
	return result
}

// Len returns the number of active reveals.
func (q *Queue) Len() int {
	return len(q.items)
}

// OverflowCount returns how many reveals have been completed early.
func (q *Queue) OverflowCount() int {
	return q.overflows
}

// Frames returns the current partial rows for every active reveal.
func (q *Queue) Frames() map[string][]render.Row {
	frames := make(map[string][]render.Row, len(q.items))
	for _, item := range q.items {
		frames[item.id] = item.sequence.Frame()
	}
	return frames
}

// ReplaceRows swaps in re-rendered rows while preserving reveal progress, so a
// terminal resize does not restart or jump an in-flight animation.
func (q *Queue) ReplaceRows(id string, rows []render.Row) bool {
	for i := range q.items {
		if q.items[i].id != id {
			continue
		}
		current := q.items[i].sequence
		next := NewSequence(rows, q.cfg, current.lastAdvance)
		next.visibleUnits = current.visibleUnits
		if next.visibleUnits > len(next.units) {
			next.visibleUnits = len(next.units)
		}
		q.items[i].sequence = next
		return true
	}
	return false
}

func completedReveal(id string, sequence *Sequence, reason CompletionReason) CompletedReveal {
	sequence.Complete()
	return CompletedReveal{ID: id, Rows: sequence.Frame(), Reason: reason}
}

func fragmentUnits(row, fragment int, source render.Fragment) []RevealUnit {
	if source.Text == "" {
		return nil
	}
	if isAtomic(source) {
		return []RevealUnit{{Row: row, Fragment: fragment, Text: source.Text}}
	}

	graphemes := uniseg.NewGraphemes(source.Text)
	units := make([]RevealUnit, 0)
	for graphemes.Next() {
		units = append(units, RevealUnit{Row: row, Fragment: fragment, Text: graphemes.Str()})
	}
	return units
}

// isAtomic reports whether a fragment must appear all at once. Semantic tokens
// and anything with a reserved width or a decorated style would otherwise
// change the row's shape partway through the reveal.
func isAtomic(fragment render.Fragment) bool {
	switch fragment.Kind {
	case render.FragmentAvatar,
		render.FragmentTimestamp,
		render.FragmentBadge,
		render.FragmentUsername,
		render.FragmentMention,
		render.FragmentReply,
		render.FragmentNotice,
		render.FragmentDeleted,
		render.FragmentEmojiFallback,
		render.FragmentShortcode,
		render.FragmentAmount,
		render.FragmentMembership:
		return true
	}
	return fragment.WidthCells > 0 ||
		fragment.Style.Background != "" ||
		fragment.Style.Bold ||
		fragment.Style.Italic ||
		fragment.Style.Strikethrough
}

func appendFragment(row *render.Row, fragment render.Fragment) {
	if fragment.Text == "" {
		return
	}
	last := len(row.Fragments) - 1
	if last >= 0 && mergeable(row.Fragments[last], fragment) {
		row.Fragments[last].Text += fragment.Text
		return
	}
	row.Fragments = append(row.Fragments, fragment)
}

// mergeable reports whether two neighboring fragments can be emitted as one
// styled run. Fixed-width fragments never merge: two chips side by side reserve
// two chip widths, and concatenating them would reserve one.
func mergeable(a, b render.Fragment) bool {
	if a.WidthCells > 0 || b.WidthCells > 0 {
		return false
	}
	return a.Kind == b.Kind && a.Style == b.Style
}

func cloneRows(rows []render.Row) []render.Row {
	if len(rows) == 0 {
		return nil
	}
	out := make([]render.Row, len(rows))
	for i, row := range rows {
		out[i].Fragments = cloneFragments(row.Fragments)
	}
	return out
}

func cloneFragments(fragments []render.Fragment) []render.Fragment {
	if len(fragments) == 0 {
		return nil
	}
	out := make([]render.Fragment, len(fragments))
	copy(out, fragments)
	return out
}

// withDefaults fills unset or nonsensical pacing values. An unrecognized mode
// degrades to the default rather than disabling animation silently.
func (c Config) withDefaults() Config {
	defaults := DefaultConfig()
	switch c.Mode {
	case ModeOff, ModeReduced, ModeFast:
	default:
		c.Mode = defaults.Mode
	}
	if c.MaxQueued <= 0 {
		c.MaxQueued = defaults.MaxQueued
	}
	if c.FastInterval <= 0 {
		c.FastInterval = defaults.FastInterval
	}
	if c.ReducedInterval <= 0 {
		c.ReducedInterval = defaults.ReducedInterval
	}
	if c.FastUnitsPerTick <= 0 {
		c.FastUnitsPerTick = defaults.FastUnitsPerTick
	}
	if c.ReducedUnitsPerTick <= 0 {
		c.ReducedUnitsPerTick = defaults.ReducedUnitsPerTick
	}
	return c
}

func (c Config) interval() time.Duration {
	switch c.Mode {
	case ModeReduced:
		return c.ReducedInterval
	case ModeOff:
		return 0
	default:
		return c.FastInterval
	}
}

func (c Config) unitsPerTick() int {
	switch c.Mode {
	case ModeReduced:
		return c.ReducedUnitsPerTick
	case ModeOff:
		return 1
	default:
		return c.FastUnitsPerTick
	}
}

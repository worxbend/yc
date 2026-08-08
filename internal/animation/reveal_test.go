package animation

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/worxbend/yc/internal/render"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.now = c.now.Add(d)
}

func TestUnitsRevealGraphemesWithoutInvalidUTF8(t *testing.T) {
	now := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.FastInterval = time.Millisecond
	row := render.Row{Fragments: []render.Fragment{{
		Kind: render.FragmentText,
		Text: "éa😀",
	}}}
	sequence := NewSequence([]render.Row{row}, cfg, now)

	wantFrames := [][]string{
		{""},
		{"é"},
		{"éa"},
		{"éa😀"},
	}
	if got := plainFrame(sequence.Frame()); !reflect.DeepEqual(got, wantFrames[0]) {
		t.Fatalf("initial frame = %#v, want %#v", got, wantFrames[0])
	}
	for i := 1; i < len(wantFrames); i++ {
		if !sequence.Advance(now.Add(time.Duration(i) * time.Millisecond)) {
			t.Fatalf("advance %d did not change frame", i)
		}
		got := plainFrame(sequence.Frame())
		if !reflect.DeepEqual(got, wantFrames[i]) {
			t.Fatalf("frame %d = %#v, want %#v", i, got, wantFrames[i])
		}
		for _, line := range got {
			if !utf8.ValidString(line) {
				t.Fatalf("frame %d contains invalid UTF-8: %q", i, line)
			}
		}
	}
}

func TestSemanticAndStyledFragmentsRevealAsCompleteUnits(t *testing.T) {
	now := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.FastInterval = time.Millisecond
	rows := []render.Row{{Fragments: []render.Fragment{
		{Kind: render.FragmentMention, Text: "@viewer"},
		{Kind: render.FragmentText, Text: " "},
		{Kind: render.FragmentShortcode, Text: ":_hype:", WidthCells: 6},
		{Kind: render.FragmentEmojiFallback, Text: "👨‍👩‍👧‍👦", WidthCells: 2},
		{Kind: render.FragmentText, Text: "MEMBER", Style: render.FragmentStyle{Bold: true}},
	}}}
	sequence := NewSequence(rows, cfg, now)

	wantFrames := [][]string{
		{"@viewer"},
		{"@viewer "},
		{"@viewer :_hype:"},
		{"@viewer :_hype:👨‍👩‍👧‍👦"},
		{"@viewer :_hype:👨‍👩‍👧‍👦MEMBER"},
	}
	for i, want := range wantFrames {
		if !sequence.Advance(now.Add(time.Duration(i+1) * time.Millisecond)) {
			t.Fatalf("advance %d did not change frame", i+1)
		}
		if got := plainFrame(sequence.Frame()); !reflect.DeepEqual(got, want) {
			t.Fatalf("frame %d = %#v, want %#v", i+1, got, want)
		}
	}
}

func TestForegroundOnlyTextPreservesStyleWhileRevealingGraphemes(t *testing.T) {
	rows := []render.Row{{Fragments: []render.Fragment{{
		Kind:  render.FragmentText,
		Text:  "ab",
		Style: render.FragmentStyle{Foreground: "#ffffff"},
	}}}}
	units := Units(rows)

	if len(units) != 2 {
		t.Fatalf("unit count = %d, want 2 grapheme units", len(units))
	}
	for i, want := range []string{"a", "b"} {
		if units[i].Text != want {
			t.Fatalf("unit %d text = %q, want %q", i, units[i].Text, want)
		}
		if units[i].Row != 0 || units[i].Fragment != 0 {
			t.Fatalf("unit %d indexes = row %d fragment %d, want 0/0", i, units[i].Row, units[i].Fragment)
		}
	}

	// A partial frame must still carry the source fragment's styling: the
	// reveal animates how much is visible, never how it is colored.
	sequence := NewSequence(rows, Config{Mode: ModeFast, FastInterval: time.Millisecond}, time.Time{})
	sequence.Advance(time.Time{}.Add(time.Millisecond))
	frame := sequence.Frame()
	if len(frame) != 1 || len(frame[0].Fragments) != 1 {
		t.Fatalf("partial frame = %#v, want one fragment", frame)
	}
	if got, want := frame[0].Fragments[0].Style.Foreground, "#ffffff"; got != want {
		t.Fatalf("partial fragment foreground = %q, want %q", got, want)
	}
}

func TestFixedWidthFallbackFramesDoNotCoalesce(t *testing.T) {
	now := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.FastInterval = time.Millisecond
	rows := []render.Row{{Fragments: []render.Fragment{
		{Kind: render.FragmentEmojiFallback, Text: "😀", WidthCells: 2},
		{Kind: render.FragmentEmojiFallback, Text: "😀", WidthCells: 2},
	}}}
	sequence := NewSequence(rows, cfg, now)

	sequence.Advance(now.Add(time.Millisecond))
	if got, want := plainFrame(sequence.Frame()), []string{"😀"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first fixed-width frame = %#v, want %#v", got, want)
	}
	sequence.Advance(now.Add(2 * time.Millisecond))
	frame := sequence.Frame()
	if got, want := plainFrame(frame), []string{"😀😀"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second fixed-width frame = %#v, want %#v", got, want)
	}
	if got, want := len(frame[0].Fragments), 2; got != want {
		t.Fatalf("frame fragment count = %d, want %d: %#v", got, want, frame[0].Fragments)
	}
}

func TestModesProduceDeterministicFrames(t *testing.T) {
	now := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	rows := []render.Row{{Fragments: []render.Fragment{{
		Kind: render.FragmentText,
		Text: "abcd",
	}}}}
	cfg := DefaultConfig()
	cfg.FastInterval = time.Millisecond
	cfg.ReducedInterval = 2 * time.Millisecond
	cfg.ReducedUnitsPerTick = 3

	off := NewSequence(rows, Config{Mode: ModeOff}, now)
	if !off.Done() {
		t.Fatal("off mode should complete immediately")
	}
	if got, want := plainFrame(off.Frame()), []string{"abcd"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("off frame = %#v, want %#v", got, want)
	}

	fast := NewSequence(rows, cfg, now)
	fast.Advance(now.Add(time.Millisecond))
	if got, want := plainFrame(fast.Frame()), []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fast frame 1 = %#v, want %#v", got, want)
	}
	fast.Advance(now.Add(2 * time.Millisecond))
	if got, want := plainFrame(fast.Frame()), []string{"ab"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fast frame 2 = %#v, want %#v", got, want)
	}

	cfg.Mode = ModeReduced
	reduced := NewSequence(rows, cfg, now)
	if changed := reduced.Advance(now.Add(time.Millisecond)); changed {
		t.Fatal("reduced mode advanced before its interval")
	}
	reduced.Advance(now.Add(2 * time.Millisecond))
	if got, want := plainFrame(reduced.Frame()), []string{"abc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reduced frame = %#v, want %#v", got, want)
	}
}

// The priming page of a live chat arrives as one burst, so off-mode reveals
// must not consume a queue slot: they are already complete when they arrive.
func TestQueueCompletesOffModeRevealsWithoutConsumingCapacity(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)}
	queue := NewQueue(Config{Mode: ModeOff}, clock)

	result := queue.Enqueue("historical", textRows("welcome to the stream"))
	if !result.Immediate {
		t.Fatalf("off-mode enqueue = %#v, want Immediate", result)
	}
	if len(result.Completed) != 1 || result.Completed[0].Reason != CompletionFinished {
		t.Fatalf("off-mode completed = %#v, want one finished reveal", result.Completed)
	}
	if got, want := plainFrame(result.Completed[0].Rows), []string{"welcome to the stream"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("off-mode rows = %#v, want %#v", got, want)
	}
	if queue.Len() != 0 {
		t.Fatalf("queue len = %d, want 0", queue.Len())
	}
}

func TestQueueCompletesOldestRevealOnOverflow(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)}
	cfg := DefaultConfig()
	cfg.MaxQueued = 2
	cfg.FastInterval = time.Second
	queue := NewQueue(cfg, clock)

	if result := queue.Enqueue("one", textRows("one")); result.Immediate || result.QueueSize != 1 {
		t.Fatalf("enqueue one = %#v", result)
	}
	if result := queue.Enqueue("two", textRows("two")); result.Immediate || result.QueueSize != 2 {
		t.Fatalf("enqueue two = %#v", result)
	}
	result := queue.Enqueue("three", textRows("three"))
	if result.QueueSize != 2 {
		t.Fatalf("enqueue three = %#v, want queue size 2", result)
	}
	if queue.Len() != 2 {
		t.Fatalf("queue len = %d, want 2", queue.Len())
	}
	if queue.OverflowCount() != 1 {
		t.Fatalf("overflow count = %d, want 1", queue.OverflowCount())
	}
	if len(result.Completed) != 1 {
		t.Fatalf("overflowed reveals = %d, want 1", len(result.Completed))
	}
	overflowed := result.Completed[0]
	if overflowed.ID != "one" || overflowed.Reason != CompletionOverflow {
		t.Fatalf("overflow = %#v, want oldest reveal completed by overflow", overflowed)
	}
	if got, want := plainFrame(overflowed.Rows), []string{"one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overflow frame = %#v, want %#v", got, want)
	}
	if _, ok := queue.Frames()["one"]; ok {
		t.Fatal("overflowed reveal should not remain queued")
	}
}

func TestQueueBurstOverflowIsDeterministicAndBounded(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)}
	cfg := DefaultConfig()
	cfg.MaxQueued = 3
	cfg.FastInterval = time.Hour
	queue := NewQueue(cfg, clock)

	overflowed := make([]string, 0)
	for i := range 10 {
		id := fmt.Sprintf("message-%02d", i)
		result := queue.Enqueue(id, textRows(id))
		if result.Immediate {
			t.Fatalf("enqueue %s completed immediately: %#v", id, result)
		}
		if result.QueueSize > cfg.MaxQueued {
			t.Fatalf("enqueue %s queue size = %d, want <= %d", id, result.QueueSize, cfg.MaxQueued)
		}
		for _, reveal := range result.Completed {
			overflowed = append(overflowed, reveal.ID)
			if reveal.Reason != CompletionOverflow {
				t.Fatalf("overflow reason for %s = %q, want %q", reveal.ID, reveal.Reason, CompletionOverflow)
			}
			if got, want := plainFrame(reveal.Rows), []string{reveal.ID}; !reflect.DeepEqual(got, want) {
				t.Fatalf("overflow rows for %s = %#v, want %#v", reveal.ID, got, want)
			}
		}
	}

	wantOverflowed := []string{
		"message-00",
		"message-01",
		"message-02",
		"message-03",
		"message-04",
		"message-05",
		"message-06",
	}
	if !reflect.DeepEqual(overflowed, wantOverflowed) {
		t.Fatalf("overflowed IDs = %#v, want %#v", overflowed, wantOverflowed)
	}
	if got, want := queue.Len(), cfg.MaxQueued; got != want {
		t.Fatalf("queue len = %d, want %d", got, want)
	}
	if got, want := queue.OverflowCount(), len(wantOverflowed); got != want {
		t.Fatalf("overflow count = %d, want %d", got, want)
	}
	frames := queue.Frames()
	for _, id := range []string{"message-07", "message-08", "message-09"} {
		if _, ok := frames[id]; !ok {
			t.Fatalf("active frames missing %s; got %#v", id, frames)
		}
	}
}

func TestQueueUsesFakeClockForCompletion(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)}
	cfg := DefaultConfig()
	cfg.FastInterval = time.Millisecond
	queue := NewQueue(cfg, clock)
	queue.Enqueue("message", textRows("ab"))

	if result := queue.Advance(); result.Changed {
		t.Fatalf("advance without time changed frame: %#v", result)
	}
	clock.Add(time.Millisecond)
	if result := queue.Advance(); !result.Changed || len(result.Completed) != 0 {
		t.Fatalf("first advance = %#v, want changed incomplete", result)
	}
	clock.Add(time.Millisecond)
	result := queue.Advance()
	if !result.Changed || len(result.Completed) != 1 || result.Completed[0].Reason != CompletionFinished {
		t.Fatalf("second advance = %#v, want completed reveal", result)
	}
	if got, want := plainFrame(result.Completed[0].Rows), []string{"ab"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("completed frame = %#v, want %#v", got, want)
	}
}

// A terminal resize re-renders the same message at a new width. Progress has to
// survive that, or every visible reveal restarts when the user drags a pane.
func TestReplaceRowsPreservesRevealProgress(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)}
	cfg := DefaultConfig()
	cfg.FastInterval = time.Millisecond
	queue := NewQueue(cfg, clock)
	queue.Enqueue("message", textRows("abcdef"))

	clock.Add(3 * time.Millisecond)
	queue.Advance()
	if got, want := plainFrame(queue.Frames()["message"]), []string{"abc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("frame before replace = %#v, want %#v", got, want)
	}

	if !queue.ReplaceRows("message", textRows("abcdefghi")) {
		t.Fatal("ReplaceRows() = false for an active reveal, want true")
	}
	if got, want := plainFrame(queue.Frames()["message"]), []string{"abc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("frame after replace = %#v, want %#v", got, want)
	}
	if queue.ReplaceRows("missing", textRows("x")) {
		t.Fatal("ReplaceRows() = true for an unknown ID, want false")
	}
}

// A pacing config with no interval must not strand a message half-typed.
func TestSequenceWithDegenerateIntervalCompletesOnAdvance(t *testing.T) {
	rows := textRows("abc")
	sequence := Sequence{rows: cloneRows(rows), units: Units(rows)}
	if sequence.Done() {
		t.Fatal("sequence started done, want pending units")
	}
	if !sequence.Advance(time.Now()) {
		t.Fatal("Advance() = false for a zero-interval sequence, want it to complete")
	}
	if !sequence.Done() {
		t.Fatal("zero-interval sequence is still pending after Advance()")
	}
}

func textRows(text string) []render.Row {
	return []render.Row{{Fragments: []render.Fragment{{
		Kind: render.FragmentText,
		Text: text,
	}}}}
}

// plainFrame concatenates fragment text directly rather than going through
// render.Row.Plain, so reveal behavior is asserted independently of how the
// render package chooses to format a row.
func plainFrame(rows []render.Row) []string {
	plain := make([]string, 0, len(rows))
	for _, row := range rows {
		var builder strings.Builder
		for _, fragment := range row.Fragments {
			builder.WriteString(fragment.Text)
		}
		plain = append(plain, builder.String())
	}
	return plain
}

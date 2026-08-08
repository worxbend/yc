package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The poller is the app's transport, so the optional capabilities the shell
// asserts on it - quota, cadence, drop count - have to be reported, not just
// present. A capability that compiles but always answers zero is a status bar
// that lies.
func TestPollerReportsCadenceAndQuotaAsEstimates(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{MinInterval: time.Second}, func(call int, w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"items":[%s],"pollingIntervalMillis":4000,"nextPageToken":"page-%d"}`,
			textItem(fmt.Sprintf("m-%d", call), "hello"), call)
	})
	awaitMessages(t, harness.poller, 1)

	// The server floor has to reach the snapshot: it is the number the user
	// checks when the cadence looks slower than they expected.
	deadline := time.After(3 * time.Second)
	for {
		snapshot := harness.poller.Quota()
		if snapshot.ServerFloor == 4*time.Second {
			if !snapshot.Estimated {
				t.Error("the snapshot is not marked as an estimate; Google publishes no live-chat costs")
			}
			if snapshot.At.IsZero() {
				t.Error("the snapshot carries no timestamp")
			}
			if snapshot.EffectiveInterval < snapshot.ServerFloor {
				t.Errorf("effective interval %v is below the server floor %v; the floor is absolute",
					snapshot.EffectiveInterval, snapshot.ServerFloor)
			}
			if snapshot.Mode == "" {
				t.Error("the snapshot reports no cadence mode")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the server floor never reached the snapshot: %+v", snapshot)
		case <-time.After(5 * time.Millisecond):
		}
	}

	if got := harness.poller.PollInterval(); got <= 0 {
		t.Errorf("PollInterval = %v, want the effective cadence", got)
	}
	if got := harness.poller.DroppedMessages(); got != 0 {
		t.Errorf("dropped = %d before any consumer fell behind", got)
	}
}

// Blocking the emitter would stall the goroutine that owns the poll schedule
// and cost the whole session, so the poller drops instead - and the loss has to
// stay visible, because the status bar is the only place a moderator can learn
// that chat is quietly losing messages.
func TestPollerCountsDroppedMessagesWhenNobodyReads(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{MinInterval: time.Millisecond}, func(call int, w http.ResponseWriter, r *http.Request) {
		items := make([]string, 0, 64)
		for i := range 64 {
			items = append(items, textItem(fmt.Sprintf("m-%d-%d", call, i), "flood"))
		}
		fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"page-%d"}`, strings.Join(items, ","), call)
	})

	// Deliberately never drain poller.Messages().
	deadline := time.After(5 * time.Second)
	for harness.poller.DroppedMessages() == 0 {
		select {
		case <-deadline:
			t.Fatal("a flood into an undrained stream reported no drops")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Sending is declined locally rather than by learning from a 429: a rate limit
// yc could have predicted costs 50 units to discover.
func TestPollerSendDeclinesLocallyAndFillsInTheChatID(t *testing.T) {
	var seenBody string
	harness := newPollHarness(t, PollerConfig{MinInterval: time.Hour}, func(call int, w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			seenBody = string(body)
			fmt.Fprint(w, `{"id":"sent-1","snippet":{"liveChatId":"chat-1","publishedAt":"2026-08-08T20:00:00Z"}}`)
			return
		}
		fmt.Fprint(w, `{"items":[]}`)
	})

	// A request with no chat ID inherits the poller's own, so the composer does
	// not have to know which chat it is attached to.
	result, err := harness.poller.Send(context.Background(), SendRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.RateLimited {
		t.Fatalf("the first send was rate limited: %+v", result)
	}
	if !strings.Contains(seenBody, `"liveChatId":"chat-1"`) {
		t.Errorf("the send body did not inherit the poller's chat ID: %s", seenBody)
	}

	// Exhaust the local burst; the limiter must decline without dispatching.
	declined := false
	for i := 0; i < 50; i++ {
		result, err := harness.poller.Send(context.Background(), SendRequest{LiveChatID: "chat-1", Text: "spam"})
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		if result.RateLimited {
			declined = true
			if result.RetryAfter <= 0 {
				t.Errorf("a declined send reported no retry delay: %+v", result)
			}
			if !strings.Contains(result.Detail, "retry in") {
				t.Errorf("detail = %q, want it to say when to retry", result.Detail)
			}
			break
		}
	}
	if !declined {
		t.Error("the local limiter never declined; every send reached the API")
	}
}

// Reconnect resumes from the retained page token rather than re-priming: a
// re-prime re-delivers up to 2000 rows and spends a poll to do it.
func TestPollerReconnectResumesFromTheRetainedPageToken(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{MinInterval: time.Hour}, func(call int, w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"page-token-%d"}`,
			textItem(fmt.Sprintf("m-%d", call), "hello"), call)
	})
	awaitMessages(t, harness.poller, 1)

	if err := harness.poller.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	awaitMessages(t, harness.poller, 1)

	queries := harness.recordedQueries()
	if len(queries) < 2 {
		t.Fatalf("only %d requests dispatched, want the reconnect to have polled", len(queries))
	}
	// The request after the reconnect must carry a page token; a bare request
	// is a re-prime.
	last := queries[len(queries)-1]
	if got := last.Get("pageToken"); got == "" {
		t.Errorf("the reconnect polled with no page token: %v", last)
	}
}

func TestPollerReconnectAfterCloseIsRefused(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{MinInterval: time.Hour}, func(call int, w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[]}`)
	})
	if err := harness.poller.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := harness.poller.Reconnect(context.Background()); !errors.Is(err, ErrPollerClosed) {
		t.Fatalf("Reconnect after Close = %v, want %v", err, ErrPollerClosed)
	}
	// Close must be safe to call more than once.
	if err := harness.poller.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestPollerReconnectHonorsACancelledContext(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{MinInterval: time.Hour}, func(call int, w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[]}`)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := harness.poller.Reconnect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconnect with a cancelled context = %v, want %v", err, context.Canceled)
	}
}

// Moderation is not folded into the message stream: a deletion is an
// instruction to remove text that is already on screen, so re-rendering it as a
// chat row puts the removed words back in front of the viewer.
func TestPollerEmitsModerationAndPollsOnTheirOwnStreams(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{MinInterval: time.Millisecond}, func(call int, w http.ResponseWriter, r *http.Request) {
		if call > 1 {
			fmt.Fprint(w, `{"items":[]}`)
			return
		}
		fmt.Fprint(w, `{"items":[
			{"id":"d-1","snippet":{"type":"messageDeletedEvent","liveChatId":"chat-1","publishedAt":"2026-08-08T20:00:00Z","messageDeletedDetails":{"deletedMessageId":"gone-1"}}},
			{"id":"b-1","snippet":{"type":"userBannedEvent","liveChatId":"chat-1","publishedAt":"2026-08-08T20:00:01Z","userBannedDetails":{"banType":"temporary","banDurationSeconds":"300","bannedUserDetails":{"channelId":"UC-banned","displayName":"Spammer"}}}}
		],"activePollItem":{"id":"poll-1","snippet":{"type":"pollEvent","liveChatId":"chat-1","pollDetails":{"metadata":{"questionText":"which map next?","options":[{"optionText":"dust","tally":"3"},{"optionText":"nuke","tally":"5"}]}}}}}`)
	})

	deletion := awaitModeration(t, harness.poller)
	if deletion.TargetMessageID != "gone-1" {
		t.Errorf("deletion = %+v, want the removed message ID", deletion)
	}

	ban := awaitModeration(t, harness.poller)
	if ban.TargetChannelID != "UC-banned" {
		t.Errorf("ban = %+v, want the banned channel", ban)
	}
	if ban.Duration != 5*time.Minute {
		t.Errorf("ban duration = %v, want 5m from the string-encoded seconds", ban.Duration)
	}

	select {
	case poll, ok := <-harness.poller.Polls():
		if !ok {
			t.Fatal("the poll stream closed")
		}
		if poll.Question != "which map next?" || len(poll.Options) != 2 {
			t.Errorf("poll = %+v, want the activePollItem decoded", poll)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the activePollItem never reached the poll stream")
	}
}

// awaitModeration reads one moderation event or fails.
func awaitModeration(t *testing.T, poller *Poller) ModerationEvent {
	t.Helper()
	select {
	case event, ok := <-poller.Moderations():
		if !ok {
			t.Fatal("the moderation stream closed")
		}
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a moderation event")
		return ModerationEvent{}
	}
}

// The endpoint breakdown is what makes an unexpected spend attributable, so it
// is sorted and complete rather than map-ordered.
func TestSortedEndpointsIsDeterministic(t *testing.T) {
	ledger := NewQuotaLedger(LedgerConfig{DailyUnits: 10000})
	for _, endpoint := range []string{
		EndpointVideosList, EndpointMessagesList, EndpointBansInsert, EndpointChannelsList,
	} {
		ledger.Charge(endpoint)
	}

	first := SortedEndpoints(ledger.Snapshot().ByEndpoint)
	if len(first) != 4 {
		t.Fatalf("endpoints = %v, want all four charged endpoints", first)
	}
	for i := 1; i < len(first); i++ {
		if first[i-1] > first[i] {
			t.Errorf("endpoints are not sorted: %v", first)
			break
		}
	}
	// Map iteration order is randomized per range, so repeating must agree.
	for i := 0; i < 20; i++ {
		again := SortedEndpoints(ledger.Snapshot().ByEndpoint)
		if strings.Join(again, ",") != strings.Join(first, ",") {
			t.Fatalf("SortedEndpoints varied between calls: %v then %v", first, again)
		}
	}
	if got := SortedEndpoints(nil); len(got) != 0 {
		t.Errorf("SortedEndpoints(nil) = %v, want empty", got)
	}
}

// The percentage is what the status bar meter draws, so an absent limit must
// not produce a NaN or a division by zero.
func TestRemainingPercentIsSafeAtEveryBoundary(t *testing.T) {
	cases := []struct {
		name     string
		snapshot QuotaSnapshot
		want     float64
	}{
		{"full", QuotaSnapshot{LimitUnits: 10000, RemainingUnits: 10000}, 100},
		{"half", QuotaSnapshot{LimitUnits: 10000, RemainingUnits: 5000}, 50},
		{"empty", QuotaSnapshot{LimitUnits: 10000, RemainingUnits: 0}, 0},
		{"no limit configured", QuotaSnapshot{}, 0},
		{"negative limit is not a limit", QuotaSnapshot{LimitUnits: -1, RemainingUnits: 10}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.snapshot.RemainingPercent(); got != tc.want {
				t.Errorf("RemainingPercent() = %v, want %v", got, tc.want)
			}
		})
	}
}

// RemainingPercent does not clamp, so the guarantee that the meter never draws
// a nonsensical percentage rests entirely on the ledger clamping RemainingUnits
// at the source. That is the invariant worth pinning: overspending is normal -
// the ledger charges failures too, and a 50-unit send can cross the line.
func TestOverspentLedgerStillReportsAPercentageInRange(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	ledger := NewQuotaLedger(LedgerConfig{DailyUnits: 100, Now: fixedClock(&now)})

	// Spend well past the allowance, as a long session with failures does.
	for i := 0; i < 40; i++ {
		ledger.Charge(EndpointMessagesInsert)
	}

	snapshot := ledger.Snapshot()
	if snapshot.UsedUnits <= snapshot.LimitUnits {
		t.Fatalf("used %d of %d; the test did not actually overspend", snapshot.UsedUnits, snapshot.LimitUnits)
	}
	if snapshot.RemainingUnits < 0 {
		t.Errorf("remaining = %d; the ledger must clamp at zero", snapshot.RemainingUnits)
	}
	if got := snapshot.RemainingPercent(); got < 0 || got > 100 {
		t.Errorf("RemainingPercent() = %v after overspending, outside 0-100", got)
	}
}

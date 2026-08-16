package youtube

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/quota"
)

// pollHarness drives a poller against an in-process server with no wall-clock
// sleeps: every scheduled wait is recorded and returned immediately.
type pollHarness struct {
	poller *Poller
	cancel context.CancelFunc

	mu     sync.Mutex
	sleeps []time.Duration
	querys []url.Values
}

// newPollHarness starts a poller whose responses are supplied per request.
func newPollHarness(t testing.TB, cfg PollerConfig, respond func(call int, w http.ResponseWriter, r *http.Request)) *pollHarness {
	t.Helper()

	harness := &pollHarness{}
	var calls atomic.Int64
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		call := int(calls.Add(1))
		harness.mu.Lock()
		harness.querys = append(harness.querys, r.URL.Query())
		harness.mu.Unlock()
		respond(call, w, r)
	})

	cfg.Client = client
	if cfg.Target.Raw == "" && cfg.Target.LiveChatID == "" {
		cfg.Target = ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"}
	}
	cfg.Sleep = func(ctx context.Context, d time.Duration) {
		harness.mu.Lock()
		harness.sleeps = append(harness.sleeps, d)
		harness.mu.Unlock()
		// Yield rather than wait: the schedule is asserted from the recorded
		// durations, so a test never has to spend the interval it verifies.
		select {
		case <-ctx.Done():
		default:
		}
	}

	poller, err := NewPoller(cfg)
	if err != nil {
		t.Fatalf("NewPoller error = %v", err)
	}
	harness.poller = poller

	ctx, cancel := context.WithCancel(context.Background())
	harness.cancel = cancel
	t.Cleanup(func() {
		cancel()
		_ = poller.Close()
	})
	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	return harness
}

// recordedSleeps returns the scheduled intervals so far.
func (h *pollHarness) recordedSleeps() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Duration(nil), h.sleeps...)
}

// recordedQueries returns the query values of every dispatched request.
func (h *pollHarness) recordedQueries() []url.Values {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]url.Values(nil), h.querys...)
}

// awaitMessages reads count messages or fails.
func awaitMessages(t *testing.T, poller *Poller, count int) []Message {
	t.Helper()
	messages := make([]Message, 0, count)
	deadline := time.After(3 * time.Second)
	for len(messages) < count {
		select {
		case message, ok := <-poller.Messages():
			if !ok {
				t.Fatalf("message stream closed after %d of %d messages", len(messages), count)
			}
			messages = append(messages, message)
		case <-deadline:
			t.Fatalf("timed out after %d of %d messages", len(messages), count)
		}
	}
	return messages
}

// awaitState reads connection states until one matches or the deadline passes.
func awaitState(t *testing.T, poller *Poller, want ConnectionStatus) ConnectionState {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case state, ok := <-poller.ConnectionStates():
			if !ok {
				t.Fatalf("connection stream closed before %q", want)
			}
			if state.Status == want {
				return state
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

// textItem renders one wire chat item.
func textItem(id, text string) string {
	return fmt.Sprintf(`{"id":%q,"snippet":{"type":"textMessageEvent","liveChatId":"chat-1","publishedAt":"2026-08-08T20:00:00Z","displayMessage":%q,"textMessageDetails":{"messageText":%q}},"authorDetails":{"channelId":"UC-someone","displayName":"someone"}}`,
		id, text, text)
}

func TestPrimingPageIsMarkedHistorical(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{}, func(call int, w http.ResponseWriter, r *http.Request) {
		if call == 1 {
			fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"page-2","pollingIntervalMillis":5000}`, textItem("m-1", "backlog"))
			return
		}
		fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"page-3","pollingIntervalMillis":5000}`, textItem("m-2", "live"))
	})

	messages := awaitMessages(t, harness.poller, 2)
	// The priming page is up to two thousand rows of history. Animating or
	// notifying them would take minutes and tell the viewer nothing.
	if !messages[0].Historical {
		t.Fatal("priming page message is not marked Historical")
	}
	if messages[1].Historical {
		t.Fatal("post-priming message is marked Historical")
	}

	queries := harness.recordedQueries()
	if queries[0].Has("pageToken") {
		t.Fatal("the priming call carried a page token")
	}
	if got := queries[1].Get("pageToken"); got != "page-2" {
		t.Fatalf("second call pageToken = %q, want page-2", got)
	}
}

func TestEmptyNextPageTokenKeepsTheLastGoodToken(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{}, func(call int, w http.ResponseWriter, r *http.Request) {
		switch call {
		case 1:
			fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"page-2"}`, textItem("m-1", "one"))
		case 2:
			// No nextPageToken at all.
			fmt.Fprintf(w, `{"items":[%s]}`, textItem("m-2", "two"))
		default:
			fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"page-2"}`, textItem("m-3", "three"))
		}
	})

	awaitMessages(t, harness.poller, 3)
	queries := harness.recordedQueries()
	if len(queries) < 3 {
		t.Fatalf("only %d calls dispatched", len(queries))
	}
	// Falling back to a token-less request re-delivers the entire backlog and
	// charges for it, so an absent token must never clear the retained one.
	if got := queries[2].Get("pageToken"); got != "page-2" {
		t.Fatalf("third call pageToken = %q, want the retained page-2", got)
	}
}

func TestDuplicateMessagesAreDeliveredOnce(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{}, func(call int, w http.ResponseWriter, r *http.Request) {
		// Every response re-delivers m-1, which is what a retained token and
		// a retry both do in practice.
		fmt.Fprintf(w, `{"items":[%s,%s],"nextPageToken":"page-%d"}`,
			textItem("m-1", "duplicated"), textItem(fmt.Sprintf("m-new-%d", call), "fresh"), call)
	})

	messages := awaitMessages(t, harness.poller, 3)
	seen := 0
	for _, message := range messages {
		if message.ID == "m-1" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("m-1 delivered %d times, want 1", seen)
	}
}

func TestServerFloorIsNeverUndercut(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{MinInterval: 100 * time.Millisecond}, func(call int, w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"page-%d","pollingIntervalMillis":7000}`,
			textItem(fmt.Sprintf("m-%d", call), "x"), call)
	})

	awaitMessages(t, harness.poller, 3)
	for _, sleep := range harness.recordedSleeps() {
		// pollingIntervalMillis is an absolute floor. yc must never poll
		// faster than YouTube advises, under any circumstance.
		if sleep < 7*time.Second {
			t.Fatalf("scheduled %v, which undercuts the 7s server floor", sleep)
		}
	}
}

func TestChatEndedStopsPollingAndKeepsTheStreamsOpen(t *testing.T) {
	var calls atomic.Int64
	harness := newPollHarness(t, PollerConfig{}, func(call int, w http.ResponseWriter, r *http.Request) {
		calls.Store(int64(call))
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":403,"message":"chat is over","errors":[{"reason":"liveChatEnded"}],"status":"PERMISSION_DENIED"}}`)
	})

	state := awaitState(t, harness.poller, ConnectionClosed)
	if !strings.Contains(strings.ToLower(state.Detail), "ended") {
		t.Fatalf("detail = %q, want it to name the ended chat", state.Detail)
	}

	// A chat that ended must not be retried: every attempt is charged, and
	// the answer will not change.
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d calls dispatched after the chat ended, want 1", got)
	}
	if got := harness.poller.State(); got != PollerEnded {
		t.Fatalf("State = %q, want %q", got, PollerEnded)
	}

	// The streams stay open so history stays on screen and the adapter above
	// does not read a closed channel as "retry this chat".
	select {
	case _, ok := <-harness.poller.Messages():
		if !ok {
			t.Fatal("message stream closed on a chat that merely ended")
		}
	default:
	}
}

func TestQuotaExhaustionPausesRatherThanRetrying(t *testing.T) {
	var calls atomic.Int64
	harness := newPollHarness(t, PollerConfig{}, func(call int, w http.ResponseWriter, r *http.Request) {
		calls.Store(int64(call))
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":403,"message":"no quota left","errors":[{"reason":"quotaExceeded"}]}}`)
	})

	state := awaitState(t, harness.poller, ConnectionPaused)
	if !strings.Contains(state.Detail, "paused") {
		t.Fatalf("detail = %q, want it to say polling paused", state.Detail)
	}
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d calls dispatched after quota exhaustion, want 1", got)
	}
	if got := harness.poller.State(); got != PollerQuotaPaused {
		t.Fatalf("State = %q, want %q", got, PollerQuotaPaused)
	}
}

func TestRateLimitBacksOffInsteadOfStopping(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{MinInterval: time.Second}, func(call int, w http.ResponseWriter, r *http.Request) {
		if call <= 2 {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":403,"errors":[{"reason":"rateLimitExceeded"}]}}`)
			return
		}
		fmt.Fprintf(w, `{"items":[%s]}`, textItem("m-1", "recovered"))
	})

	awaitState(t, harness.poller, ConnectionReconnecting)
	awaitMessages(t, harness.poller, 1)

	sleeps := harness.recordedSleeps()
	if len(sleeps) < 2 {
		t.Fatalf("only %d intervals scheduled, want at least 2", len(sleeps))
	}
	// Backing off uses full jitter, so consecutive samples are not ordered:
	// what the ladder widens is the window, not the draw. Asserting
	// sleeps[1] >= sleeps[0] would be asserting a coin flip. The invariants
	// that actually hold on every sample are the floor and the cap, and the
	// widening itself is proved deterministically by
	// TestBackoffLadderWidensTheWindowItDrawsFrom below.
	for i, sleep := range sleeps {
		if sleep < time.Second {
			t.Errorf("sleep %d = %v, below the configured floor of 1s", i, sleep)
		}
		if sleep > backoffRateLimitCap {
			t.Errorf("sleep %d = %v, above the rate-limit cap of %v", i, sleep, backoffRateLimitCap)
		}
	}
}

// The ladder is what stops a rate limit from becoming a quota exhaustion:
// retrying at the same cadence spends units to be told the same thing again.
// climb is pure, so the widening is asserted exactly rather than sampled.
func TestBackoffLadderWidensTheWindowItDrawsFrom(t *testing.T) {
	base := time.Second
	backoff := backoffFloor
	previous := backoff
	for step := 0; step < 12; step++ {
		backoff = climb(backoff, base, backoffRateLimitCap)
		if backoff < previous {
			t.Fatalf("step %d: backoff shrank from %v to %v", step, previous, backoff)
		}
		if ceiling := time.Duration(float64(base) * backoff); ceiling > backoffRateLimitCap {
			t.Fatalf("step %d: ceiling %v exceeds the cap %v", step, ceiling, backoffRateLimitCap)
		}
		previous = backoff
	}
	if backoff <= backoffFloor {
		t.Fatalf("backoff never left the floor: %v", backoff)
	}

	// A transient 5xx is capped lower than a rate limit: the API asking yc to
	// slow down is a statement about the next few minutes, a 5xx usually is not.
	transient := backoffFloor
	for step := 0; step < 12; step++ {
		transient = climb(transient, base, backoffTransientCap)
	}
	if time.Duration(float64(base)*transient) > backoffTransientCap {
		t.Fatalf("transient ladder %v exceeds its cap %v", transient, backoffTransientCap)
	}
	if transient >= backoff {
		t.Fatalf("transient ladder %v reached the rate-limit ladder %v; the caps must differ", transient, backoff)
	}
}

// Full jitter must draw from [floor, base*backoff] and must actually use the
// widened part of that window. Sampling is safe here because the failure
// probability is (1/3)^samples, not because the assertion is loose.
func TestBackoffJitterStaysInsideTheWidenedWindow(t *testing.T) {
	const (
		base    = 2 * time.Second
		backoff = 4.0
		samples = 4000
	)
	ceiling := time.Duration(float64(base) * backoff)

	widest := time.Duration(0)
	for i := 0; i < samples; i++ {
		got := NextInterval(0, 0, base, 0, backoff)
		if got < base {
			t.Fatalf("sample %d = %v, below the floor %v", i, got, base)
		}
		if got > ceiling {
			t.Fatalf("sample %d = %v, above the backed-off ceiling %v", i, got, ceiling)
		}
		if got > widest {
			widest = got
		}
	}
	// Without this the whole window could collapse onto the floor and every
	// assertion above would still pass.
	if widest <= base {
		t.Fatalf("widest of %d samples was %v; backoff never drew above the floor %v", samples, widest, base)
	}
}

func TestReserveThresholdPausesPollingButNotSending(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	ledger := quota.NewLedger(quota.Config{DailyUnits: 100, Now: fixedClock(&now)})
	// Spend down to the reserve before the first poll.
	for ledger.Remaining() > 10 {
		ledger.Charge(quota.EndpointMessagesList)
	}

	server := newLedgerPoller(t, ledger)
	state := awaitState(t, server.poller, ConnectionPaused)
	if !strings.Contains(state.Detail, "sends still available") {
		t.Fatalf("detail = %q, want it to say sends still work", state.Detail)
	}
}

// newLedgerPoller starts a poller sharing a pre-spent ledger.
func newLedgerPoller(t *testing.T, ledger *quota.Ledger) *pollHarness {
	t.Helper()
	harness := &pollHarness{}
	client, _ := newTestClientWithLedger(t, ledger, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[],"nextPageToken":"page-2"}`)
	})

	poller, err := NewPoller(PollerConfig{
		Client:         client,
		Target:         ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"},
		ReservePercent: 10,
		Sleep:          func(_ context.Context, _ time.Duration) {},
	})
	if err != nil {
		t.Fatalf("NewPoller error = %v", err)
	}
	harness.poller = poller

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = poller.Close()
	})
	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	return harness
}

func TestResolutionRunsBeforePollingAndItsFailureParks(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{
		Target: ChatTarget{Raw: "dQw4w9WgXcQ", Kind: TargetVideoID, VideoID: "dQw4w9WgXcQ"},
	}, func(call int, w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "videos") {
			// A video with no active chat.
			fmt.Fprint(w, `{"items":[{"id":"dQw4w9WgXcQ","snippet":{"title":"Not live"},"liveStreamingDetails":{}}]}`)
			return
		}
		t.Errorf("unexpected call to %s after resolution failed", r.URL.Path)
	})

	state := awaitState(t, harness.poller, ConnectionClosed)
	if strings.TrimSpace(state.Detail) == "" {
		t.Fatal("closed state carries no explanation")
	}
}

func TestCloseIsIdempotentAndClosesEveryStream(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{}, func(call int, w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[],"nextPageToken":"page-2"}`)
	})

	if err := harness.poller.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if err := harness.poller.Close(); err != nil {
		t.Fatalf("second Close error = %v", err)
	}
	for name, drained := range map[string]bool{
		"messages":    drainedMessages(harness.poller.Messages()),
		"states":      drainedStates(harness.poller.ConnectionStates()),
		"moderations": drainedModerations(harness.poller.Moderations()),
		"rooms":       drainedRooms(harness.poller.RoomEvents()),
		"polls":       drainedPolls(harness.poller.Polls()),
	} {
		if !drained {
			t.Fatalf("%s stream is still open after Close", name)
		}
	}
}

func drainedMessages(ch <-chan Message) bool {
	for range ch {
	}
	return true
}
func drainedStates(ch <-chan ConnectionState) bool {
	for range ch {
	}
	return true
}
func drainedModerations(ch <-chan ModerationEvent) bool {
	for range ch {
	}
	return true
}
func drainedRooms(ch <-chan RoomEvent) bool {
	for range ch {
	}
	return true
}
func drainedPolls(ch <-chan PollState) bool {
	for range ch {
	}
	return true
}

func TestStartTwiceIsRefused(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{}, func(call int, w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[]}`)
	})
	// Two loops on one chat would double the spend and interleave tokens.
	if err := harness.poller.Start(context.Background()); err != ErrPollerStarted {
		t.Fatalf("second Start error = %v, want ErrPollerStarted", err)
	}
}

func TestNextIntervalRespectsEveryFloor(t *testing.T) {
	for name, tc := range map[string]struct {
		server, budget, min, max time.Duration
		backoff                  float64
		atLeast, atMost          time.Duration
	}{
		"server floor wins over config minimum": {
			server: 5 * time.Second, min: time.Second,
			atLeast: 5 * time.Second, atMost: 6 * time.Second,
		},
		"budget floor stretches past the server floor": {
			server: 5 * time.Second, budget: 43 * time.Second, min: time.Second,
			atLeast: 38 * time.Second, atMost: 48 * time.Second,
		},
		"ceiling caps the stretch": {
			server: time.Second, budget: 10 * time.Minute, min: time.Second, max: 30 * time.Second,
			atLeast: 27 * time.Second, atMost: 33 * time.Second,
		},
		"backoff never dips below the server floor": {
			server: 5 * time.Second, min: time.Second, backoff: 8,
			atLeast: 5 * time.Second, atMost: 40 * time.Second,
		},
	} {
		t.Run(name, func(t *testing.T) {
			backoff := tc.backoff
			if backoff == 0 {
				backoff = 1
			}
			// Jitter is random, so the property is asserted over many draws
			// rather than on one.
			for range 500 {
				got := NextInterval(tc.server, tc.budget, tc.min, tc.max, backoff)
				if got < tc.atLeast || got > tc.atMost {
					t.Fatalf("NextInterval = %v, want within [%v, %v]", got, tc.atLeast, tc.atMost)
				}
				if got < tc.server {
					t.Fatalf("NextInterval = %v undercuts the server floor %v", got, tc.server)
				}
			}
		})
	}
}

func TestNextIntervalJitters(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 200 {
		seen[NextInterval(5*time.Second, 0, time.Second, 0, 1)] = true
	}
	// Without jitter every restart and every instance polls on the same
	// second, which is how a fleet turns one outage into a thundering herd.
	if len(seen) < 5 {
		t.Fatalf("only %d distinct intervals across 200 draws; jitter is not applied", len(seen))
	}
}

func TestDedupeRingForgetsTheOldestFirst(t *testing.T) {
	ring := newDedupeRing(3)
	for _, id := range []string{"a", "b", "c"} {
		if !ring.add(id) {
			t.Fatalf("%q reported as already seen", id)
		}
	}
	if ring.add("a") {
		t.Fatal("a reported as new while still in the ring")
	}
	ring.add("d") // evicts a
	if !ring.add("a") {
		t.Fatal("a was not evicted after the ring wrapped")
	}
	if ring.has("nothing") {
		t.Fatal("has reported an ID that was never added")
	}
}

func TestTransientResolutionFailureRetriesRatherThanStranding(t *testing.T) {
	var calls atomic.Int64
	harness := newPollHarness(t, PollerConfig{
		Target: ChatTarget{Raw: "dQw4w9WgXcQ", Kind: TargetVideoID, VideoID: "dQw4w9WgXcQ"},
	}, func(call int, w http.ResponseWriter, r *http.Request) {
		calls.Store(int64(call))
		if strings.Contains(r.URL.Path, "videos") {
			if call == 1 {
				// A blip while resolving must not leave the chat in a state
				// it can never leave: the whole session would be lost to one
				// unlucky moment.
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"code":500,"errors":[{"reason":"backendError"}]}}`)
				return
			}
			fmt.Fprint(w, `{"items":[{"id":"dQw4w9WgXcQ","snippet":{"title":"Launch Day"},"liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`)
			return
		}
		fmt.Fprintf(w, `{"items":[%s]}`, textItem("m-1", "resolved after a retry"))
	})

	messages := awaitMessages(t, harness.poller, 1)
	if messages[0].Text != "resolved after a retry" {
		t.Fatalf("message = %q", messages[0].Text)
	}
	if calls.Load() < 3 {
		t.Fatalf("only %d calls; the resolve was not retried", calls.Load())
	}
}

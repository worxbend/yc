package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// poll_reconnect_test.go covers what a reconnect preserves. These cover what it
// must not leave behind.
//
// Every Reconnect cancels a goroutine that owns a poll schedule and starts
// another. yc runs one poller per open chat tab and a reconnect is bound to
// ctrl+r, so a restart that strands the old session does not merely leak a
// goroutine - it leaves a second loop polling the same chat, charging the same
// estimated 5 units per call against the same daily allowance, invisible in the
// quota status bar because the ledger cannot tell the two loops apart. A viewer
// who hits ctrl+r twice while the network is flapping would burn the day's
// budget at double the rate the status bar's arithmetic promises.

// busyPollerStep is the interval a stepped session polls at. It is short so a
// session nobody canceled is loud rather than dormant: a stranded loop shows up
// as requests arriving after the poller was closed, which is unambiguous, rather
// than as a goroutine parked somewhere that only a stack dump would find.
const busyPollerStep = 20 * time.Millisecond

// busyPollerSettle is how long a stop is given to finish before a "nothing is
// still polling" assertion starts counting. It only has to cover one in-flight
// request to an in-process server, and it is several busyPollerStep intervals
// so a loop that really is stranded still announces itself in the window that
// follows.
const busyPollerSettle = 100 * time.Millisecond

// busyPoller is a poller driven against an in-process API at a fixed fast
// cadence, with every dispatched list request counted.
type busyPoller struct {
	t      *testing.T
	poller *Poller
	ctx    context.Context

	requests chan struct{}
	dispatch atomic.Int64
	resolves atomic.Int64
}

func newBusyPoller(t *testing.T, target ChatTarget) *busyPoller {
	t.Helper()

	b := &busyPoller{requests: make(chan struct{}, 4096)}

	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "videos") {
			b.resolves.Add(1)
			fmt.Fprint(w, `{"items":[{"id":"dQw4w9WgXcQ","snippet":{"title":"Launch Day"},"liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`)
			return
		}
		b.dispatch.Add(1)
		select {
		case b.requests <- struct{}{}:
		default:
		}
		fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"page-1","pollingIntervalMillis":1000}`,
			textItem("m-1", "hello"))
	})

	poller, err := NewPoller(PollerConfig{
		Client: client,
		Target: target,
		Sleep: func(ctx context.Context, _ time.Duration) {
			timer := time.NewTimer(busyPollerStep)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
			}
		},
	})
	if err != nil {
		t.Fatalf("NewPoller error = %v", err)
	}
	b.t = t
	b.poller = poller

	ctx, cancel := context.WithCancel(context.Background())
	b.ctx = ctx
	t.Cleanup(func() {
		cancel()
		_ = poller.Close()
	})

	// The streams are drained for the whole test. Nothing here asserts on
	// their contents; the point is that a full buffer must never be what
	// stops a session, or "no requests arrived" would stop meaning "no
	// session is running".
	go drainEverything(poller)

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	return b
}

// drainEverything reads all five streams until they close.
func drainEverything(poller *Poller) {
	for {
		select {
		case _, ok := <-poller.Messages():
			if !ok {
				return
			}
		case <-poller.ConnectionStates():
		case <-poller.Moderations():
		case <-poller.RoomEvents():
		case <-poller.Polls():
		}
	}
}

// awaitRequest waits for one freshly dispatched list request.
func (b *busyPoller) awaitRequest(why string) {
	b.t.Helper()
	select {
	case <-b.requests:
	case <-time.After(5 * time.Second):
		b.t.Fatalf("no list request was dispatched %s", why)
	}
}

// awaitPageToken waits until the retained continuation token is want.
//
// A request arriving at the handler is not the same event as its response being
// processed, and the token is only retained once the page has been decoded.
// Asserting on the token straight after awaitRequest would be asserting on a
// race the test itself created.
func (b *busyPoller) awaitPageToken(want string) {
	b.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b.poller.pageToken() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	b.t.Fatalf("pageToken = %q, want %q", b.poller.pageToken(), want)
}

// drainRequests discards whatever is already queued, so the next awaitRequest
// is about a request dispatched after this point.
func (b *busyPoller) drainRequests() {
	for {
		select {
		case <-b.requests:
		default:
			return
		}
	}
}

// assertNoFurtherPolls fails if the request counter moves at all during window.
// A session that nothing holds a cancel for polls every busyPollerStep, so a
// single stranded loop shows up here as dozens of calls.
func (b *busyPoller) assertNoFurtherPolls(window time.Duration, why string) {
	b.t.Helper()
	// The counter is incremented by the test server's handler, not by the poll
	// loop, so a request that was already on the wire when the loop was
	// canceled still lands - once - after the cancel is complete. Waiting for
	// that straggler before taking the baseline keeps the assertion about what
	// it is meant to catch: a loop nobody canceled, which at busyPollerStep
	// keeps dispatching for as long as anyone watches.
	time.Sleep(busyPollerSettle)
	before := b.dispatch.Load()
	time.Sleep(window)
	if after := b.dispatch.Load(); after != before {
		b.t.Fatalf("%d list requests were dispatched in the %v after %s; a session is still polling and still charging quota",
			after-before, window, why)
	}
}

// TestOverlappingReconnectsLeaveExactlyOneSession is the leak restartMu exists
// to prevent.
//
// Two restarts that interleave each cancel one session and launch another, and
// the second overwrites p.cancel - so the first replacement is never canceled
// by anything and keeps polling for the rest of the process's life. Closing the
// poller does not stop it either, because Close can only cancel the session it
// knows about. That is what makes "no requests after Close" the right assertion:
// it is exactly the observable the bug produces.
func TestOverlappingReconnectsLeaveExactlyOneSession(t *testing.T) {
	b := newBusyPoller(t, ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"})
	b.awaitRequest("to open the chat")

	const restarts = 16
	var wg sync.WaitGroup
	errs := make(chan error, restarts)
	for i := 0; i < restarts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.poller.Reconnect(b.ctx); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Reconnect error = %v", err)
	}

	// The survivor is still a working session.
	b.drainRequests()
	b.awaitRequest("after sixteen overlapping restarts")

	if err := b.poller.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	b.assertNoFurtherPolls(500*time.Millisecond, "Close")
}

// A restart must not re-resolve. The resolve is a videos.list call, and paying
// one per ctrl+r is precisely the cost Reconnect exists to avoid - along with
// re-priming, which reprints a window of chat the viewer has already read.
func TestRepeatedReconnectsNeverReResolveTheTarget(t *testing.T) {
	b := newBusyPoller(t, ChatTarget{Raw: "dQw4w9WgXcQ", Kind: TargetVideoID, VideoID: "dQw4w9WgXcQ"})
	b.awaitRequest("to open the chat")
	if got := b.resolves.Load(); got != 1 {
		t.Fatalf("resolves = %d, want the one that opened the chat", got)
	}

	for i := 0; i < 10; i++ {
		if err := b.poller.Reconnect(b.ctx); err != nil {
			t.Fatalf("Reconnect %d error = %v", i, err)
		}
		b.drainRequests()
		b.awaitRequest(fmt.Sprintf("after reconnect %d", i))
	}

	if got := b.resolves.Load(); got != 1 {
		t.Fatalf("resolves = %d after ten reconnects, want 1", got)
	}
	b.awaitPageToken("page-1")
	if got := b.poller.Target().LiveChatID; got != "chat-1" {
		t.Fatalf("Target().LiveChatID = %q, want the resolved chat to survive a restart", got)
	}
}

// Reconnect must not leak a goroutine per restart. yc is a long-lived process
// and a viewer hits ctrl+r whenever the network hiccups.
func TestRepeatedReconnectsLeaveNoGoroutinesBehind(t *testing.T) {
	b := newBusyPoller(t, ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"})
	b.awaitRequest("to open the chat")

	reconnect := func(times int) {
		t.Helper()
		for i := 0; i < times; i++ {
			if err := b.poller.Reconnect(b.ctx); err != nil {
				t.Fatalf("Reconnect error = %v", err)
			}
			b.drainRequests()
			b.awaitRequest("after a restart")
		}
	}

	// Warm up first, so the HTTP client's idle connections and the server's
	// accept goroutines already exist when the baseline is taken.
	reconnect(3)
	baseline := settleGoroutines()

	reconnect(25)

	if after := settleGoroutines(); after > baseline+4 {
		t.Fatalf("goroutines = %d after 25 reconnects, baseline %d", after, baseline)
	}
}

// settleGoroutines waits for the goroutine count to stop moving and returns it.
func settleGoroutines() int {
	last := -1
	for i := 0; i < 60; i++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
		now := runtime.NumGoroutine()
		if now == last {
			return now
		}
		last = now
	}
	return last
}

// A Reconnect whose context is already canceled reports the cancellation and
// starts nothing. The previous session is still stopped: the caller asked for a
// restart, and leaving the old loop running under a context the caller has
// abandoned would keep spending quota on a chat nobody is watching.
func TestReconnectWithACancelledContextStartsNothing(t *testing.T) {
	b := newBusyPoller(t, ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"})
	b.awaitRequest("to open the chat")

	b.awaitPageToken("page-1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.poller.Reconnect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconnect with a canceled context = %v, want context.Canceled", err)
	}
	b.assertNoFurtherPolls(300*time.Millisecond, "a Reconnect whose context was already canceled")

	// And the poller is not wedged: a later restart on a live context works,
	// resuming from the cursor the refused attempt never touched.
	if err := b.poller.Reconnect(b.ctx); err != nil {
		t.Fatalf("Reconnect after a canceled one = %v", err)
	}
	b.drainRequests()
	b.awaitRequest("after a live restart following a refused one")
	b.awaitPageToken("page-1")
}

// Close is the last word whichever order the two calls arrive in. Reconnect
// releases the poller lock to cancel and drain the previous session, and a
// Close landing in that window closes the five streams; a session launched
// afterwards would emit onto a closed channel and take the process with it.
func TestCloseRacingReconnectNeverEmitsOntoAClosedStream(t *testing.T) {
	for round := 0; round < 20; round++ {
		b := newBusyPoller(t, ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"})
		b.awaitRequest("to open the chat")

		start := make(chan struct{})
		var wg sync.WaitGroup
		var reconnectErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			reconnectErr = b.poller.Reconnect(b.ctx)
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = b.poller.Close()
		}()
		close(start)
		wg.Wait()

		// Either order is legitimate; only a panic, a lingering session, or
		// a Close that stopped being idempotent is not.
		if reconnectErr != nil && !errors.Is(reconnectErr, ErrPollerClosed) && !errors.Is(reconnectErr, context.Canceled) {
			t.Fatalf("round %d: Reconnect racing Close = %v, want nil or ErrPollerClosed", round, reconnectErr)
		}
		if err := b.poller.Close(); err != nil {
			t.Fatalf("round %d: second Close error = %v", round, err)
		}
		if err := b.poller.Reconnect(b.ctx); !errors.Is(err, ErrPollerClosed) {
			t.Fatalf("round %d: Reconnect after Close = %v, want ErrPollerClosed", round, err)
		}
		b.assertNoFurtherPolls(100*time.Millisecond, "a Close that raced a Reconnect")
	}
}

// Close drains every stream exactly once, so a consumer ranging over Messages
// sees a clean end rather than blocking forever, and a second Close does not
// panic on a channel that is already closed.
func TestCloseClosesEveryStreamOnce(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"page-1","pollingIntervalMillis":1000}`,
			textItem("m-1", "hello"))
	})
	poller, err := NewPoller(PollerConfig{
		Client: client,
		Target: ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"},
		Sleep:  func(ctx context.Context, _ time.Duration) { <-ctx.Done() },
	})
	if err != nil {
		t.Fatalf("NewPoller error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	if err := poller.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := poller.Close(); err != nil {
			t.Fatalf("Close %d error = %v", i, err)
		}
	}

	drain := func(name string, recv func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for recv() {
			if time.Now().After(deadline) {
				t.Fatalf("the %s stream was never closed", name)
			}
		}
	}
	drain("message", func() bool { _, ok := <-poller.Messages(); return ok })
	drain("connection state", func() bool { _, ok := <-poller.ConnectionStates(); return ok })
	drain("moderation", func() bool { _, ok := <-poller.Moderations(); return ok })
	drain("room", func() bool { _, ok := <-poller.RoomEvents(); return ok })
	drain("poll", func() bool { _, ok := <-poller.Polls(); return ok })

	if state := poller.State(); state != PollerClosed {
		t.Fatalf("State = %v after Close, want PollerClosed", state)
	}
	// A poller that has been closed cannot be restarted into life.
	if err := poller.Start(context.Background()); !errors.Is(err, ErrPollerClosed) && !errors.Is(err, ErrPollerStarted) {
		t.Fatalf("Start after Close = %v, want a refusal", err)
	}
}

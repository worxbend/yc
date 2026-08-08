package youtube

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// reconnectHarness drives a poller one poll at a time.
//
// The shared poll harness yields instead of sleeping, which is right for
// asserting a schedule but useless here: a reconnect has to be observed between
// two specific requests, and a loop that spins as fast as the loopback can
// answer gives no such window. This one blocks the loop in Sleep until the test
// releases it, so every request in the assertions is one the test asked for.
type reconnectHarness struct {
	t      *testing.T
	poller *Poller
	ctx    context.Context

	// sleeping receives once the poll loop has finished a cycle and parked.
	// Waiting on it is how a test knows the response has been processed and
	// its continuation token retained, rather than merely dispatched.
	sleeping chan struct{}
	// tick releases the poll loop from its next Sleep.
	tick chan struct{}
	// requests carries the query of every dispatched request, in order.
	requests chan url.Values

	mu       sync.Mutex
	resolves int
}

// newReconnectHarness starts a poller against a stepped fake API. respond sees
// the 1-based list-call number.
func newReconnectHarness(t *testing.T, target ChatTarget, respond func(listCall int, w http.ResponseWriter, r *http.Request)) *reconnectHarness {
	t.Helper()

	h := &reconnectHarness{
		t:        t,
		sleeping: make(chan struct{}),
		tick:     make(chan struct{}),
		requests: make(chan url.Values, 64),
	}

	listCalls := 0
	var callMu sync.Mutex
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "videos") {
			h.mu.Lock()
			h.resolves++
			h.mu.Unlock()
			fmt.Fprint(w, `{"items":[{"id":"dQw4w9WgXcQ","snippet":{"title":"Launch Day"},"liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`)
			return
		}
		callMu.Lock()
		listCalls++
		call := listCalls
		callMu.Unlock()
		h.requests <- r.URL.Query()
		respond(call, w, r)
	})

	poller, err := NewPoller(PollerConfig{
		Client: client,
		Target: target,
		Sleep: func(ctx context.Context, _ time.Duration) {
			select {
			case h.sleeping <- struct{}{}:
			case <-ctx.Done():
				return
			}
			select {
			case <-h.tick:
			case <-ctx.Done():
			}
		},
	})
	if err != nil {
		t.Fatalf("NewPoller error = %v", err)
	}
	h.poller = poller

	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	t.Cleanup(func() {
		cancel()
		_ = poller.Close()
	})
	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	return h
}

// nextRequest returns the query of the next dispatched list request.
func (h *reconnectHarness) nextRequest() url.Values {
	h.t.Helper()
	select {
	case query := <-h.requests:
		return query
	case <-time.After(3 * time.Second):
		h.t.Fatal("timed out waiting for the next list request")
		return nil
	}
}

// awaitSleep blocks until the poll loop has finished its current cycle, which
// is the only moment at which the retained token is guaranteed to be up to date.
func (h *reconnectHarness) awaitSleep() {
	h.t.Helper()
	select {
	case <-h.sleeping:
	case <-time.After(3 * time.Second):
		h.t.Fatal("the poll loop never reached its next sleep")
	}
}

// release lets the poll loop out of the Sleep it is parked in.
func (h *reconnectHarness) release() {
	h.t.Helper()
	select {
	case h.tick <- struct{}{}:
	case <-time.After(3 * time.Second):
		h.t.Fatal("the poll loop never took its next tick")
	}
}

// resolveCount reports how many videos.list calls the session has spent.
func (h *reconnectHarness) resolveCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.resolves
}

// drainMessages reads every message currently queued, without waiting for more.
func drainMessages(poller *Poller) []Message {
	var out []Message
	for {
		select {
		case message, ok := <-poller.Messages():
			if !ok {
				return out
			}
			out = append(out, message)
		case <-time.After(200 * time.Millisecond):
			return out
		}
	}
}

// TestReconnectResumesFromTheRetainedTokenWithoutReprinting is the whole point
// of driving a restart through Reconnect rather than rebuilding the poller.
//
// A replacement poller starts with no continuation token and no memory of what
// it has shown, so it re-resolves the chat, primes from the head, and reprints
// whatever window the API hands back - which for a viewer is the last few
// minutes of chat arriving a second time, and for the quota meter is a resolve
// unit plus a full page nobody needed.
func TestReconnectResumesFromTheRetainedTokenWithoutReprinting(t *testing.T) {
	harness := newReconnectHarness(t, ChatTarget{Raw: "dQw4w9WgXcQ", Kind: TargetVideoID, VideoID: "dQw4w9WgXcQ"},
		func(call int, w http.ResponseWriter, r *http.Request) {
			// Every page re-delivers m-overlap, which is exactly what a
			// re-primed page does in practice.
			fmt.Fprintf(w, `{"items":[%s,%s],"nextPageToken":"page-%d","pollingIntervalMillis":1000}`,
				textItem("m-overlap", "already on screen"),
				textItem(fmt.Sprintf("m-%d", call), "fresh"),
				call+1)
		})

	harness.awaitSleep()
	if got := harness.nextRequest().Get("pageToken"); got != "" {
		t.Fatalf("the priming call carried pageToken %q, want none", got)
	}
	if got := harness.resolveCount(); got != 1 {
		t.Fatalf("resolve calls = %d, want 1", got)
	}

	if err := harness.poller.Reconnect(harness.ctx); err != nil {
		t.Fatalf("Reconnect error = %v", err)
	}

	// The resumed session must present the token the previous one earned.
	harness.awaitSleep()
	if got := harness.nextRequest().Get("pageToken"); got != "page-2" {
		t.Fatalf("the call after a reconnect carried pageToken %q, want page-2", got)
	}
	// ...and must not pay to resolve a chat it already resolved.
	if got := harness.resolveCount(); got != 1 {
		t.Fatalf("resolve calls = %d after a reconnect, want the resolved target to survive", got)
	}
	if got := harness.poller.Target().LiveChatID; got != "chat-1" {
		t.Fatalf("target LiveChatID = %q after a reconnect, want chat-1", got)
	}

	messages := drainMessages(harness.poller)
	overlaps := 0
	for _, message := range messages {
		if message.ID == "m-overlap" {
			overlaps++
		}
	}
	if len(messages) < 3 {
		t.Fatalf("delivered %d messages across the reconnect, want both pages", len(messages))
	}
	if overlaps != 1 {
		t.Fatalf("m-overlap was delivered %d times across a reconnect, want exactly once", overlaps)
	}
}

// TestReconnectRePrimesWhenTheRetainedTokenIsRejected pins the fallback. Only a
// token the API has actually refused is worth abandoning; anything less and the
// resume above would be pointless.
func TestReconnectRePrimesWhenTheRetainedTokenIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "400 on the token",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":400,"message":"invalid page token","errors":[{"reason":"invalidPageToken"}]}}`,
		},
		{
			name:   "404 on the token",
			status: http.StatusNotFound,
			body:   `{"error":{"code":404,"message":"not found","errors":[{"reason":"liveChatNotFound"}]}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness := newReconnectHarness(t, ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"},
				func(call int, w http.ResponseWriter, r *http.Request) {
					switch call {
					case 1:
						fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"stale-token","pollingIntervalMillis":1000}`,
							textItem("m-overlap", "already on screen"))
					case 2:
						w.WriteHeader(tc.status)
						fmt.Fprint(w, tc.body)
					default:
						fmt.Fprintf(w, `{"items":[%s,%s],"nextPageToken":"fresh-token","pollingIntervalMillis":1000}`,
							textItem("m-overlap", "already on screen"),
							textItem("m-after", "recovered"))
					}
				})

			harness.awaitSleep()
			harness.nextRequest()
			if err := harness.poller.Reconnect(harness.ctx); err != nil {
				t.Fatalf("Reconnect error = %v", err)
			}

			// The resumed session presents the retained token and is refused.
			harness.awaitSleep()
			if got := harness.nextRequest().Get("pageToken"); got != "stale-token" {
				t.Fatalf("the resumed call carried pageToken %q, want stale-token", got)
			}
			// The rejection is a reason to re-prime, not to end the chat.
			harness.release()
			harness.awaitSleep()
			if got := harness.nextRequest().Get("pageToken"); got != "" {
				t.Fatalf("the retry after a rejected token carried pageToken %q, want none", got)
			}
			if state := harness.poller.State(); state == PollerEnded {
				t.Fatal("a rejected continuation token ended the session")
			}

			// The re-primed page still must not reprint what is on screen.
			messages := drainMessages(harness.poller)
			overlaps, recovered := 0, 0
			for _, message := range messages {
				switch message.ID {
				case "m-overlap":
					overlaps++
				case "m-after":
					recovered++
				}
			}
			if overlaps != 1 {
				t.Fatalf("m-overlap was delivered %d times across a re-prime, want exactly once", overlaps)
			}
			if recovered != 1 {
				t.Fatalf("the recovered message arrived %d times, want once", recovered)
			}

			// A rejected token must not be handed to the next reconnect: it
			// would spend a unit to be refused all over again.
			if got := harness.poller.pageToken(); got == "stale-token" {
				t.Fatal("the poller retained a continuation token the API had already rejected")
			}
		})
	}
}

// TestNotFoundWithNoTokenStillEndsTheChat is the other half of the 404
// recovery: without a continuation token in hand there is nothing to blame but
// the chat, so the session ends rather than retrying a chat that is gone.
func TestNotFoundWithNoTokenStillEndsTheChat(t *testing.T) {
	harness := newReconnectHarness(t, ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"},
		func(_ int, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"not found","errors":[{"reason":"liveChatNotFound"}]}}`)
		})

	state := awaitState(t, harness.poller, ConnectionClosed)
	if state.Detail == "" {
		t.Fatal("the closed state carried no reason")
	}
}

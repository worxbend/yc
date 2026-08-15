package youtube

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestStalePageTokenRePrimesInsteadOfEndingTheChat pins the recovery for a
// continuation token YouTube no longer accepts.
//
// A 400 on the list path classifies as ErrMessageRejected, which is terminal
// for insert and ban - the caller really did send something invalid - but on
// the list path it is almost always a stale cursor. yc stretches its cadence to
// tens of seconds to survive the daily quota, which is exactly the regime that
// makes a token go stale, and ending the session over one would leave ctrl+r as
// the only way back.
func TestStalePageTokenRePrimesInsteadOfEndingTheChat(t *testing.T) {
	var calls atomic.Int64
	harness := newPollHarness(t, PollerConfig{}, func(call int, w http.ResponseWriter, r *http.Request) {
		calls.Store(int64(call))
		switch call {
		case 1:
			// Priming succeeds and hands back a continuation token.
			w.Write([]byte(`{"items":[],"nextPageToken":"stale-token","pollingIntervalMillis":1000}`))
		case 2:
			// The continuation token is rejected.
			if got := r.URL.Query().Get("pageToken"); got != "stale-token" {
				t.Errorf("second call pageToken = %q, want the retained token", got)
			}
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"code":400,"message":"invalid page token","errors":[{"reason":"invalidPageToken"}]}}`))
		default:
			w.Write([]byte(`{"items":[],"nextPageToken":"fresh-token","pollingIntervalMillis":1000}`))
		}
	})

	// The session must survive: it re-primes rather than ending.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() < 4 {
		t.Fatalf("poller stopped after %d calls; a rejected page token ended the session", calls.Load())
	}

	queries := harness.recordedQueries()
	if len(queries) < 3 {
		t.Fatalf("recorded %d queries, want at least 3", len(queries))
	}
	// The retry after the rejection must go out with no page token at all.
	if got := queries[2].Get("pageToken"); got != "" {
		t.Fatalf("retry after a rejected token carried pageToken %q, want none", got)
	}
	if state := harness.poller.State(); state == PollerEnded {
		t.Fatalf("poller state = %v, want a live session", state)
	}
}

// TestASecondRejectionEndsTheSession pins the other half: the token-drop
// recovery is available once. A 400 that survives a token-less call is a real
// rejection, and retrying it forever would spend quota to be told the same
// thing.
func TestASecondRejectionEndsTheSession(t *testing.T) {
	harness := newPollHarness(t, PollerConfig{}, func(call int, w http.ResponseWriter, r *http.Request) {
		if call == 1 {
			w.Write([]byte(`{"items":[],"nextPageToken":"stale-token","pollingIntervalMillis":1000}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":400,"message":"bad request","errors":[{"reason":"invalidValue"}]}}`))
	})

	state := awaitState(t, harness.poller, ConnectionFailed)
	t.Logf("failed as expected: %s", state.Detail)
}

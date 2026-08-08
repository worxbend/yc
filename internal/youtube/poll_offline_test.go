package youtube

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestOfflineGraceWindowClosesTheSession pins that a broadcast that went
// offline eventually closes the chat rather than polling forever.
func TestOfflineGraceWindowClosesTheSession(t *testing.T) {
	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	var ticks atomic.Int64
	now := func() time.Time {
		return base.Add(time.Duration(ticks.Add(1)) * 30 * time.Second)
	}
	offlineAt := base.Add(-time.Minute).Format(time.RFC3339)

	harness := newPollHarness(t, PollerConfig{Now: now}, func(call int, w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"items":[],"offlineAt":%q,"nextPageToken":"p%d","pollingIntervalMillis":5000}`, offlineAt, call)
	})

	state := awaitState(t, harness.poller, ConnectionClosed)
	t.Logf("closed: %s", state.Detail)
}

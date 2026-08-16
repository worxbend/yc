package youtube

import (
	"sync/atomic"
	"testing"
	"time"
)

// The settle window added to assertNoFurtherPolls exists to absorb one request
// that was already on the wire when a session was canceled. This checks it did
// not also absorb the thing the assertion is for: a loop nobody canceled, which
// keeps dispatching at busyPollerStep for as long as anyone watches.
func TestSettleWindowStillLeavesAStrandedLoopVisible(t *testing.T) {
	var counter atomic.Int64
	stop := make(chan struct{})
	defer close(stop)

	go func() {
		ticker := time.NewTicker(busyPollerStep)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				counter.Add(1)
			}
		}
	}()

	// The same sequence assertNoFurtherPolls performs.
	time.Sleep(busyPollerSettle)
	before := counter.Load()
	time.Sleep(500 * time.Millisecond)
	after := counter.Load()

	if after == before {
		t.Fatal("a loop still polling produced no requests after the settle window; the assertion would no longer catch a stranded session")
	}
	if got := after - before; got < 5 {
		t.Fatalf("only %d requests observed in the window; a stranded loop should be loud, not marginal", got)
	}
}

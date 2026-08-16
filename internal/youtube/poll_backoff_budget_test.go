package youtube

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// stubPollerClient is a PollerClient whose list calls always fail. It exists to
// drive the poll loop's error path without a transport, which is what having a
// narrow PollerClient interface rather than a concrete *Client buys.
type stubPollerClient struct {
	snapshot QuotaSnapshot
	listCost int

	mu    sync.Mutex
	calls int
}

func (s *stubPollerClient) ListMessages(context.Context, ListRequest) (ListResult, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	// Rate limiting is a retryable condition, so the loop backs off and
	// retries rather than parking - which is the path under test.
	return ListResult{}, fmt.Errorf("the API asked yc to slow down: %w", ErrRateLimited)
}

func (s *stubPollerClient) ResolveTarget(_ context.Context, target ChatTarget) (ChatTarget, error) {
	return target, nil
}

func (s *stubPollerClient) SendMessage(context.Context, SendRequest) (SendResult, error) {
	return SendResult{}, errors.New("not used by this test")
}

func (s *stubPollerClient) Quota() QuotaSnapshot { return s.snapshot }

func (s *stubPollerClient) CostOf(string) int { return s.listCost }

// An error storm must still respect the day's budget.
//
// Google charges a dispatched request whatever comes back, so a failing call
// costs the same units as a successful one. The backoff used to compute its
// delay with a hard-coded zero budget, so a session that had stretched to a
// minute-scale cadence to make its quota last would drop back to retrying
// every few seconds the moment the API started failing - spending the rest of
// the day's allowance proving the API was down.
func TestBackoffKeepsTheBudgetFloor(t *testing.T) {
	now := pacificTime(t, 2026, time.August, 8, 0, 0)

	// A nearly exhausted day: a handful of units left against a full reset
	// horizon, which implies a cadence far slower than any backoff of the
	// one-second server floor would produce.
	client := &stubPollerClient{
		listCost: 5,
		snapshot: QuotaSnapshot{
			UsedUnits:      9900,
			LimitUnits:     10000,
			RemainingUnits: 100,
			ResetAt:        ResetAt(now),
		},
	}

	const samples = 6
	delays := make(chan time.Duration, samples)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poller, err := NewPoller(PollerConfig{
		Client: client,
		Target: ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"},
		Now:    func() time.Time { return now },
		// Returning immediately lets the loop produce several backoff delays
		// quickly; the durations are inspected, never waited out.
		Sleep: func(_ context.Context, d time.Duration) {
			select {
			case delays <- d:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("NewPoller error = %v", err)
	}
	t.Cleanup(func() { _ = poller.Close() })

	if err := poller.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	// The budget floor the remaining units imply, far above any cadence the
	// server floor alone would produce.
	wantFloor := BudgetFloor(client.snapshot.RemainingUnits, client.listCost, ResetAt(now).Sub(now))
	if wantFloor <= backoffRateLimitCap {
		t.Fatalf("test setup is not budget-constrained: floor = %v", wantFloor)
	}

	// Backing off uses full jitter, so any single delay may land anywhere
	// between the server floor and the ceiling. What the budget changes is the
	// ceiling: with the old hard-coded zero budget the base was the 1s server
	// floor and the multiplier was capped at backoffRateLimitCap/1s, so no
	// delay could ever exceed backoffRateLimitCap. Seeing one that does is
	// only possible if the budget reached the calculation. Across six samples
	// drawn from a window of hours, every one landing under that cap is far
	// less likely than any other cause of a failing test.
	var longest time.Duration
	deadline := time.After(5 * time.Second)
	for range samples {
		select {
		case d := <-delays:
			if d > longest {
				longest = d
			}
		case <-deadline:
			t.Fatalf("the poll loop produced only %v of %d backoff delays", longest, samples)
		}
	}

	if longest <= backoffRateLimitCap {
		t.Fatalf("longest backoff delay = %v, want one above %v: the budget floor never reached the backoff, so an error storm outspends the day",
			longest, backoffRateLimitCap)
	}
}

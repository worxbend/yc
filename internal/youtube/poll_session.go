package youtube

import (
	"context"
	"log/slog"
	"time"

	"github.com/worxbend/yc/internal/quota"
)

// pollSession is one pass of the poll loop, from resolving a target to the
// moment the session context is canceled or the chat parks.
//
// The loop carries state between ticks - which cursor to send, how far the
// backoff ladder has climbed, whether the broadcast has gone offline and when
// - and that state only makes sense inside one session: a reconnect builds a
// fresh one. Holding it on a value of its own rather than as locals of a
// single long function is what lets each phase of a tick be a named method
// small enough to read on its own.
type pollSession struct {
	p      *Poller
	target ChatTarget

	// pageToken is the continuation cursor for the next request, and priming
	// marks a request that asks for recent history instead of only what has
	// arrived since. A session with no token starts by priming.
	pageToken string
	priming   bool

	// backoff is the position on the retry ladder. It climbs on failure and
	// decays one step per success rather than clearing, so a flapping
	// connection does not slam straight back to full cadence.
	backoff float64

	// serverInterval is the cadence YouTube last asked for. It is the floor
	// every computed interval is measured against.
	serverInterval time.Duration

	// offlineNotedAt is when this session first saw offlineAt, not the
	// instant the API reports. The grace window has to be measured from the
	// observation: offlineAt is a past timestamp, so measuring from it - and
	// comparing it against a lastItemAt seeded with the session start - made
	// the window's condition unsatisfiable and left the poller charging quota
	// against a finished broadcast forever.
	offlineNotedAt time.Time
	offlineNoted   bool

	// lastItemAt is when this session last delivered a message. It restarts
	// the offline grace window, so a room still talking after the broadcast
	// ended is not cut off mid-conversation.
	lastItemAt time.Time

	// connected records that the "connected" state has been announced, so a
	// reconnect within one session does not announce it twice.
	connected bool

	// tokenRetried records that this session has already answered a rejected
	// continuation token by dropping it and re-priming. One such recovery is
	// a stale token; a second is a real rejection, and retrying it forever
	// would spend quota to be told the same thing.
	tokenRetried bool
}

// run is the whole state machine. It returns only when the session context is
// canceled: a terminal condition parks instead of returning, because the
// adapter above treats a closed stream as "retry this chat", and retrying a
// chat that has ended spends quota to be told so again.
func (p *Poller) run(ctx context.Context, pageToken string) {
	s := &pollSession{
		p:         p,
		pageToken: pageToken,
		priming:   pageToken == "",
		backoff:   backoffFloor,
	}
	if !s.resolveTarget(ctx) {
		return
	}
	s.announcePriming(ctx)
	s.pump(ctx)
}

// resolveTarget retries resolution until it succeeds, and reports whether the
// session may proceed.
func (s *pollSession) resolveTarget(ctx context.Context) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		resolved, err := s.p.resolve(ctx)
		if err == nil {
			s.target = resolved
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		// A resolution failure is classified exactly like a failed poll, so
		// "this channel is not live" reads as a closed chat rather than as a
		// fault, and a network blip while resolving retries on the same
		// ladder instead of stranding the chat in a state it cannot leave.
		// Resolution holds no continuation token, so the token-drop recovery
		// is not available here.
		stop, next, _ := s.p.handleListError(ctx, resolved, err, s.backoff, 0, false)
		if stop {
			s.p.parkQuietly(ctx)
			return false
		}
		s.backoff = next
		s.p.cfg.Sleep(ctx, NextInterval(0, 0, s.p.cfg.MinInterval, s.p.cfg.MaxInterval, s.backoff))
	}
}

// announcePriming reports that the session is loading the recent backlog.
func (s *pollSession) announcePriming(ctx context.Context) {
	s.p.setState(PollerPriming)
	s.p.emitState(ctx, ConnectionState{
		Status: ConnectionConnecting,
		ChatID: s.target.LiveChatID,
		Detail: "loading recent messages",
		At:     s.p.cfg.Now(),
	})
}

// pump is the tick loop: request a page, absorb it, then decide whether the
// session ends here or how long to wait before the next one.
func (s *pollSession) pump(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		result, err := s.list(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !s.recoverFromListError(ctx, err) {
				return
			}
			continue
		}

		s.decayBackoff()
		s.announceConnected(ctx)
		delivered := s.absorb(ctx, result)

		if s.chatClosed(ctx, result) {
			return
		}
		if s.offlineGraceExpired(ctx, result) {
			return
		}

		snapshot := s.p.cfg.Client.Quota()
		if s.quotaPaused(ctx, snapshot) {
			return
		}
		s.pace(ctx, snapshot, result.QuotaUnits, delivered)
	}
}

// list requests the next page of the chat.
func (s *pollSession) list(ctx context.Context) (ListResult, error) {
	return s.p.cfg.Client.ListMessages(ctx, ListRequest{
		LiveChatID:     s.target.LiveChatID,
		PageToken:      s.pageToken,
		Historical:     s.priming,
		SeenMessageIDs: s.p.seen.has,
	})
}

// recoverFromListError classifies a failed page request, waits out the backoff
// it earned, and reports whether the session continues.
func (s *pollSession) recoverFromListError(ctx context.Context, err error) bool {
	p := s.p
	stop, nextBackoff, dropToken := p.handleListError(
		ctx, s.target, err, s.backoff, s.serverInterval, s.pageToken != "" && !s.tokenRetried,
	)
	if stop {
		p.park(ctx, err)
		return false
	}
	if dropToken {
		// The continuation token was rejected, not the request. Drop it and
		// re-prime from the head of the chat rather than ending a live
		// session over a stale cursor - the dedupe ring keeps the re-primed
		// page from reprinting.
		//
		// The retained copy is cleared too: a reconnect resumes from
		// p.token, and resuming from a cursor the API has already refused
		// would spend a unit to be refused again.
		s.tokenRetried = true
		s.pageToken = ""
		s.priming = true
		p.resetPageToken()
	}
	s.backoff = nextBackoff
	p.setState(PollerBackoff)
	p.setMode(quota.ModeBackoff)
	// The budget floor applies to failures too: Google charges a dispatched
	// request whatever comes back, so an error storm that retried on the
	// server cadence alone would burn the day's units proving the API is
	// down. Hard-coding a zero budget here is exactly the bug this
	// recomputation removes.
	budget := p.budget(p.cfg.Client.Quota(), 0)
	delay := NextInterval(s.serverInterval, budget, p.cfg.MinInterval, p.cfg.MaxInterval, s.backoff)
	p.setEffective(delay)
	p.cfg.Sleep(ctx, delay)
	return true
}

// decayBackoff steps the ladder back down after a success.
//
// A success decays the ladder one step rather than clearing it, so a flapping
// connection does not slam straight back to full cadence.
func (s *pollSession) decayBackoff() {
	if s.backoff <= backoffFloor {
		return
	}
	s.backoff /= backoffStep
	if s.backoff < backoffFloor {
		s.backoff = backoffFloor
	}
}

// announceConnected reports the connection as live, once per session.
func (s *pollSession) announceConnected(ctx context.Context) {
	if s.connected {
		return
	}
	s.connected = true
	s.p.emitState(ctx, ConnectionState{
		Status: ConnectionConnected,
		ChatID: s.target.LiveChatID,
		Detail: connectedDetail(s.target),
		At:     s.p.cfg.Now(),
	})
}

// absorb publishes a page and folds it into the session, returning how many
// messages were delivered.
func (s *pollSession) absorb(ctx context.Context, result ListResult) int {
	delivered := s.p.emitResult(ctx, result)
	if delivered > 0 {
		s.lastItemAt = s.p.cfg.Now()
	}
	if result.PollingInterval > 0 {
		s.serverInterval = result.PollingInterval
	}

	// Retain the last good token. Falling back to a token-less request
	// re-delivers the entire backlog and charges for the privilege.
	if result.NextPageToken != "" {
		s.pageToken = result.NextPageToken
		s.p.setPageToken(s.pageToken)
	}
	s.priming = false
	return delivered
}

// chatClosed reports whether the page says the chat itself has ended, parking
// the session if so.
func (s *pollSession) chatClosed(ctx context.Context, result ListResult) bool {
	ended, reason := s.p.chatEnded(result)
	if !ended {
		return false
	}
	s.p.emitRoom(ctx, RoomEvent{
		Type:       RoomChatEnded,
		LiveChatID: s.target.LiveChatID,
		Detail:     reason,
		At:         s.p.cfg.Now(),
	})
	s.p.setState(PollerEnded)
	s.p.emitState(ctx, ConnectionState{
		Status: ConnectionClosed,
		ChatID: s.target.LiveChatID,
		Detail: reason,
		At:     s.p.cfg.Now(),
	})
	s.p.parkQuietly(ctx)
	return true
}

// offlineGraceExpired tracks the broadcast going offline and reports whether
// the chat has now been quiet long enough to close, parking it if so.
//
// Chat outlives the stream, so going offline is a warning and not a stop. The
// session ends only once the grace window has passed with nothing new
// arriving: a room still talking after the broadcast ended restarts the window
// rather than being cut off mid-conversation.
func (s *pollSession) offlineGraceExpired(ctx context.Context, result ListResult) bool {
	if result.OfflineAt.IsZero() {
		s.offlineNoted = false
		return false
	}
	if !s.offlineNoted {
		s.offlineNoted = true
		s.offlineNotedAt = s.p.cfg.Now()
		s.p.setState(PollerOffline)
		s.p.emitRoom(ctx, RoomEvent{
			Type:       RoomStreamOffline,
			LiveChatID: s.target.LiveChatID,
			Detail:     "the broadcast has ended; chat stays open a little longer",
			At:         s.p.cfg.Now(),
		})
	}

	quietSince := s.offlineNotedAt
	if s.lastItemAt.After(quietSince) {
		quietSince = s.lastItemAt
	}
	if !s.p.cfg.Now().After(quietSince.Add(OfflineGracePeriod)) {
		return false
	}
	s.p.setState(PollerEnded)
	s.p.emitState(ctx, ConnectionState{
		Status: ConnectionClosed,
		ChatID: s.target.LiveChatID,
		Detail: "live chat closed after the broadcast ended",
		At:     s.p.cfg.Now(),
	})
	s.p.parkQuietly(ctx)
	return true
}

// quotaPaused reports whether the day's reserve has been tripped, parking the
// session if so.
func (s *pollSession) quotaPaused(ctx context.Context, snapshot quota.Snapshot) bool {
	detail, paused := s.p.reserveTripped(snapshot)
	if !paused {
		return false
	}
	s.p.setState(PollerQuotaPaused)
	s.p.setMode(quota.ModePaused)
	s.p.emitState(ctx, ConnectionState{
		Status: ConnectionPaused,
		ChatID: s.target.LiveChatID,
		Detail: detail,
		At:     s.p.cfg.Now(),
	})
	s.p.parkQuietly(ctx)
	return true
}

// pace computes the interval until the next request, publishes the cadence the
// meter reports, and sleeps it out.
func (s *pollSession) pace(ctx context.Context, snapshot quota.Snapshot, lastCost, delivered int) {
	p := s.p
	budget := p.budget(snapshot, lastCost)
	interval := NextInterval(s.serverInterval, budget, p.cfg.MinInterval, p.cfg.MaxInterval, backoffFloor)

	p.mu.Lock()
	p.serverFloor = s.serverInterval
	p.budgetFloor = budget
	p.effective = interval
	if budget > s.serverInterval && budget > p.cfg.MinInterval {
		p.state = PollerStretched
		p.mode = quota.ModeStretched
	} else {
		p.state = PollerStreaming
		p.mode = quota.ModeLive
	}
	p.mu.Unlock()

	p.log(ctx, "youtube.poll.tick",
		slog.Int("messages", delivered),
		slog.String("interval", interval.String()),
		slog.String("server_floor", s.serverInterval.String()),
		slog.String("budget_floor", budget.String()),
		slog.Int("remaining_units_est", snapshot.RemainingUnits),
	)

	p.cfg.Sleep(ctx, interval)
}

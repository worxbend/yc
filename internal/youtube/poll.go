package youtube

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/worxbend/yc/internal/debuglog"
)

// DedupeRingSize is how many recently seen message IDs the poller remembers.
// It must comfortably exceed MaxResultsPerPoll, because retries, reconnects,
// and a retained page token all re-deliver rows.
const DedupeRingSize = 8000

// OfflineGracePeriod is how long the poller keeps polling after the list
// response reports offlineAt. Chat outlives the broadcast by a short window, so
// going offline is not itself a reason to stop.
const OfflineGracePeriod = 2 * time.Minute

// Backoff ladder bounds. Rate limiting is capped higher than a transient
// failure because the API asking yc to slow down is a statement about the next
// few minutes, while a 5xx is usually over in seconds.
const (
	backoffRateLimitCap = 120 * time.Second
	backoffTransientCap = 60 * time.Second
	backoffStep         = 2.0
	backoffFloor        = 1.0
)

// defaultMinInterval is the local poll floor when none is configured. The
// server floor still overrides it upward.
const defaultMinInterval = time.Second

// pollJitterFraction is the +/- spread applied to a normal cadence so that
// several yc instances, or one restarted in a loop, do not synchronize onto
// the same second and turn a poll into a thundering herd.
const pollJitterFraction = 0.10

// PollerClient is the slice of the REST client the poller actually uses.
//
// It exists so the poll loop depends on five calls rather than on the whole
// *Client, which keeps the state machine testable against a stub and keeps
// streamList substitutable behind the same boundary if it ever ships. *Client
// satisfies it.
type PollerClient interface {
	// ListMessages fetches one page of the chat.
	ListMessages(ctx context.Context, request ListRequest) (ListResult, error)
	// ResolveTarget turns the user's chat reference into a pollable target.
	ResolveTarget(ctx context.Context, target ChatTarget) (ChatTarget, error)
	// SendMessage posts a chat message.
	SendMessage(ctx context.Context, request SendRequest) (SendResult, error)
	// Quota returns the current estimated ledger snapshot.
	Quota() QuotaSnapshot
	// CostOf quotes the estimated unit cost of an endpoint without charging
	// it; the budget arithmetic needs the price of the next poll.
	CostOf(endpoint string) int
}

// PollerConfig configures one live chat poll session.
type PollerConfig struct {
	Client PollerClient
	Target ChatTarget

	// MinInterval is the absolute local floor. The server floor
	// (pollingIntervalMillis) is still honored above it: yc must never poll
	// faster than YouTube advises, under any circumstance.
	MinInterval time.Duration
	// MaxInterval caps the stretched cadence. Zero means no cap.
	MaxInterval time.Duration
	// FollowServerCadence disables the budget floor entirely, for a project
	// with a raised quota.
	FollowServerCadence bool
	// SessionHorizon narrows the budget horizon from "until the Pacific
	// reset" to the next N hours, trading tomorrow's budget for real-time
	// cadence tonight. Zero budgets to the reset.
	SessionHorizon time.Duration
	// ReservePercent stops polling at this remaining share so sends and
	// moderation still work after reads are cut off.
	ReservePercent int

	// Buffer sizes for the outbound channels. The poller drops rather than
	// blocks when a consumer falls behind, and counts what it dropped.
	MessageBuffer int

	Logger debuglog.Logger
	// Now and Sleep are injectable so the whole state machine can be driven
	// by a fake clock with no wall-clock sleeps in tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration)
}

// PollerState is the poll loop's lifecycle.
type PollerState string

// The poll loop states, roughly in the order a healthy session moves
// through them.
const (
	// PollerIdle is the state before Start and after Close.
	PollerIdle PollerState = "idle"
	// PollerResolving covers target resolution: turning the user's chat
	// reference into a live chat ID.
	PollerResolving PollerState = "resolving"
	// PollerPriming is the first list call, sent without a page token. It
	// returns recent history at once, which is emitted with Historical set.
	PollerPriming   PollerState = "priming"
	PollerStreaming PollerState = "streaming"
	PollerBackoff   PollerState = "backoff"
	// PollerStretched means the budget floor exceeded the server floor, so
	// yc is deliberately polling slower than allowed to survive the day.
	PollerStretched   PollerState = "stretched"
	PollerEnded       PollerState = "ended"
	PollerOffline     PollerState = "offline"
	PollerQuotaPaused PollerState = "quota_paused"
	PollerClosed      PollerState = "closed"
)

// The REST client must keep satisfying the poller's view of it.
var _ PollerClient = (*Client)(nil)

// Errors the poller returns for misuse rather than for an API condition.
var (
	// ErrPollerStarted means Start was called twice. Two loops on one chat
	// would double the quota spend and interleave page tokens.
	ErrPollerStarted = errors.New("poller: already started")
	// ErrPollerClosed means the poller was reused after Close.
	ErrPollerClosed = errors.New("poller: closed")
	// ErrPollerNoClient means the poller was built without a transport.
	ErrPollerNoClient = errors.New("poller: no client configured")
)

// Poller drives one live chat through liveChatMessages.list and satisfies the
// app-facing chat client boundary.
//
// It polls rather than streaming because liveChatMessages.streamList, though
// documented as a low-latency server-streaming alternative, is absent from the
// REST discovery document and is unreachable from a plain REST client today.
// The transport is factored so streamList can be substituted behind this same
// type if it ever ships.
type Poller struct {
	cfg PollerConfig

	messages    chan Message
	states      chan ConnectionState
	moderations chan ModerationEvent
	rooms       chan RoomEvent
	polls       chan PollState

	limiter *SendLimiter
	seen    *dedupeRing

	dropped atomic.Uint64

	// restartMu serializes Reconnect. Two overlapping restarts would each
	// cancel one session and launch another, and the second would overwrite
	// p.cancel - stranding a live goroutine that nothing can stop and that
	// keeps charging quota for a chat the user thinks has one poller.
	restartMu sync.Mutex

	mu      sync.Mutex
	state   PollerState
	target  ChatTarget
	token   string
	started bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}

	// Cadence state, owned by the poll loop and read by Quota.
	serverFloor time.Duration
	budgetFloor time.Duration
	effective   time.Duration
	mode        QuotaMode

	closeStreams sync.Once
}

// NewPoller returns a poller with defaults applied. It performs no I/O; call
// Start to begin.
func NewPoller(cfg PollerConfig) (*Poller, error) {
	if cfg.Client == nil {
		return nil, ErrPollerNoClient
	}
	if strings.TrimSpace(cfg.Target.Raw) == "" && !cfg.Target.Resolved() {
		return nil, newSafeError("poller: no chat target", ErrChatNotFound)
	}
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = defaultMinInterval
	}
	if cfg.MaxInterval > 0 && cfg.MaxInterval < cfg.MinInterval {
		cfg.MaxInterval = cfg.MinInterval
	}
	if cfg.MessageBuffer <= 0 {
		// One priming page can carry MaxResultsPerPoll rows, so a buffer
		// smaller than that would report drops for a backlog the UI was
		// always going to read.
		cfg.MessageBuffer = MaxResultsPerPoll + 256
	}
	if cfg.ReservePercent < 0 {
		cfg.ReservePercent = 0
	}
	if cfg.ReservePercent > 100 {
		cfg.ReservePercent = 100
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepContext
	}

	return &Poller{
		cfg:         cfg,
		messages:    make(chan Message, cfg.MessageBuffer),
		states:      make(chan ConnectionState, 64),
		moderations: make(chan ModerationEvent, 256),
		rooms:       make(chan RoomEvent, 64),
		polls:       make(chan PollState, 64),
		limiter:     NewSendLimiter(SendLimiterConfig{Now: cfg.Now}),
		seen:        newDedupeRing(DedupeRingSize),
		state:       PollerIdle,
		target:      cfg.Target,
		mode:        QuotaModeLive,
	}, nil
}

// Start begins the poll loop against a session context derived from ctx.
// Calling Start twice is an error; use Reconnect to restart.
func (p *Poller) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPollerClosed
	}
	if p.started {
		p.mu.Unlock()
		return ErrPollerStarted
	}
	p.started = true
	p.mu.Unlock()

	p.launch(ctx, "")
	return nil
}

// launch starts one session goroutine resuming from pageToken.
//
// The closed check is repeated here under the lock, not just in the caller.
// Reconnect releases the lock to cancel and drain the previous session, and a
// Close arriving in that window closes the five streams; a session started
// afterwards would emit onto a closed channel and panic the process. Rechecking
// makes Close the last word whichever order the two calls arrive in.
func (p *Poller) launch(ctx context.Context, pageToken string) {
	sessionCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		cancel()
		close(done)
		return
	}
	p.cancel = cancel
	p.done = done
	p.mu.Unlock()

	go func() {
		defer close(done)
		p.run(sessionCtx, pageToken)
	}()
}

// Messages returns the normalized message stream. It is closed by Close.
func (p *Poller) Messages() <-chan Message { return p.messages }

// ConnectionStates returns the session lifecycle stream.
func (p *Poller) ConnectionStates() <-chan ConnectionState { return p.states }

// Moderations returns deletions, tombstones, bans, and timeouts. These are kept
// off the message stream so a removal never reprints the removed text.
func (p *Poller) Moderations() <-chan ModerationEvent { return p.moderations }

// RoomEvents returns chat-wide state changes.
func (p *Poller) RoomEvents() <-chan RoomEvent { return p.rooms }

// Polls returns creator poll state, fed by both pollEvent items and the list
// response's activePollItem.
func (p *Poller) Polls() <-chan PollState { return p.polls }

// Send posts a chat message through liveChatMessages.insert.
//
// The local limiter declines before dispatch rather than after a 429, because
// an insert costs an estimated 50 units whether the API accepts it or not.
func (p *Poller) Send(ctx context.Context, request SendRequest) (SendResult, error) {
	if strings.TrimSpace(request.LiveChatID) == "" {
		request.LiveChatID = p.liveChatID()
	}
	if allowed, retryAfter := p.limiter.Allow(); !allowed {
		return SendResult{
			RateLimited: true,
			RetryAfter:  retryAfter,
			Detail:      "sending too fast; retry in " + retryAfter.Round(100*time.Millisecond).String(),
		}, nil
	}
	return p.cfg.Client.SendMessage(ctx, request)
}

// Reconnect cancels the current session and starts a new one, resuming from the
// retained page token rather than re-priming. The old context is canceled and
// the old streams drained before the replacement exists.
//
// This is the restart path, not rebuilding the poller, because everything worth
// keeping lives on this value: the continuation token, the resolved target, and
// the dedupe ring. A replacement poller has none of them, so it pays a resolve
// unit, primes from the head of the chat, and reprints a window of messages the
// viewer already read. Reconnect keeps all three, so the only visible effect of
// ctrl+r is a reconnecting state and the next page.
//
// The retained token is dropped only when the API has actually rejected it; see
// handleListError's token-drop recovery.
func (p *Poller) Reconnect(ctx context.Context) error {
	// Serialized so two restarts cannot interleave into two live sessions.
	p.restartMu.Lock()
	defer p.restartMu.Unlock()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPollerClosed
	}
	cancel, done := p.cancel, p.done
	p.started = true
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.launch(ctx, p.pageToken())
	return nil
}

// Quota returns the current estimated ledger snapshot with the cadence fields
// filled in from the live floors.
func (p *Poller) Quota() QuotaSnapshot {
	snapshot := p.cfg.Client.Quota()

	p.mu.Lock()
	snapshot.ServerFloor = p.serverFloor
	snapshot.BudgetFloor = p.budgetFloor
	snapshot.EffectiveInterval = p.effective
	snapshot.Mode = p.mode
	p.mu.Unlock()

	snapshot.Estimated = true
	if snapshot.At.IsZero() {
		snapshot.At = p.cfg.Now()
	}
	return snapshot
}

// PollInterval returns the current effective interval.
func (p *Poller) PollInterval() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effective
}

// DroppedMessages returns how many messages were discarded because a consumer
// could not keep up. Blocking the emitter would stall the goroutine that owns
// the poll schedule, so the poller drops - and that loss must stay visible.
func (p *Poller) DroppedMessages() uint64 { return p.dropped.Load() }

// State returns the current lifecycle state.
func (p *Poller) State() PollerState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Target returns the target as far as it has been resolved.
func (p *Poller) Target() ChatTarget {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.target
}

// Close cancels the session and closes every stream. It is safe to call more
// than once.
func (p *Poller) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	cancel, done := p.cancel, p.done
	p.state = PollerClosed
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		// The loop only ever exits on cancellation, and every emit selects
		// on the session context, so this cannot deadlock behind a full
		// channel that nobody is draining.
		<-done
	}
	p.closeAllStreams()
	return nil
}

// closeAllStreams closes the five outbound channels exactly once.
func (p *Poller) closeAllStreams() {
	p.closeStreams.Do(func() {
		close(p.messages)
		close(p.states)
		close(p.moderations)
		close(p.rooms)
		close(p.polls)
	})
}

// --- the poll loop ---------------------------------------------------------

// run is the whole state machine. It returns only when the session context is
// canceled: a terminal condition parks instead of returning, because the
// adapter above treats a closed stream as "retry this chat", and retrying a
// chat that has ended spends quota to be told so again.
func (p *Poller) run(ctx context.Context, pageToken string) {
	backoff := backoffFloor

	var target ChatTarget
	for {
		if ctx.Err() != nil {
			return
		}
		resolved, err := p.resolve(ctx)
		if err == nil {
			target = resolved
			break
		}
		if ctx.Err() != nil {
			return
		}
		// A resolution failure is classified exactly like a failed poll, so
		// "this channel is not live" reads as a closed chat rather than as a
		// fault, and a network blip while resolving retries on the same
		// ladder instead of stranding the chat in a state it cannot leave.
		// Resolution holds no continuation token, so the token-drop recovery
		// is not available here.
		stop, next, _ := p.handleListError(ctx, resolved, err, backoff, 0, false)
		if stop {
			p.parkQuietly(ctx)
			return
		}
		backoff = next
		p.cfg.Sleep(ctx, NextInterval(0, 0, p.cfg.MinInterval, p.cfg.MaxInterval, backoff))
	}

	p.setState(PollerPriming)
	p.emitState(ctx, ConnectionState{
		Status: ConnectionConnecting,
		ChatID: target.LiveChatID,
		Detail: "loading recent messages",
		At:     p.cfg.Now(),
	})

	var (
		// offlineNotedAt is when this session first saw offlineAt, not the
		// instant the API reports. The grace window has to be measured from
		// the observation: offlineAt is a past timestamp, so measuring from it
		// - and comparing it against a lastItemAt seeded with the session
		// start - made the window's condition unsatisfiable and left the
		// poller charging quota against a finished broadcast forever.
		offlineNotedAt time.Time
		offlineNoted   bool
		lastItemAt     time.Time
		connected      bool
		priming        = pageToken == ""
		serverInterval time.Duration
		// tokenRetried records that this session has already answered a
		// rejected continuation token by dropping it and re-priming. One
		// such recovery is a stale token; a second is a real rejection, and
		// retrying it forever would spend quota to be told the same thing.
		tokenRetried bool
	)

	for {
		if ctx.Err() != nil {
			return
		}

		result, err := p.cfg.Client.ListMessages(ctx, ListRequest{
			LiveChatID:     target.LiveChatID,
			PageToken:      pageToken,
			Historical:     priming,
			SeenMessageIDs: p.seen.has,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			stop, nextBackoff, dropToken := p.handleListError(ctx, target, err, backoff, serverInterval, pageToken != "" && !tokenRetried)
			if stop {
				p.park(ctx, err)
				return
			}
			if dropToken {
				// The continuation token was rejected, not the request.
				// Drop it and re-prime from the head of the chat rather
				// than ending a live session over a stale cursor - the
				// dedupe ring keeps the re-primed page from reprinting.
				//
				// The retained copy is cleared too: a reconnect resumes
				// from p.token, and resuming from a cursor the API has
				// already refused would spend a unit to be refused again.
				tokenRetried = true
				pageToken = ""
				priming = true
				p.resetPageToken()
			}
			backoff = nextBackoff
			p.setState(PollerBackoff)
			p.setMode(QuotaModeBackoff)
			// The budget floor applies to failures too: Google charges a
			// dispatched request whatever comes back, so an error storm that
			// retried on the server cadence alone would burn the day's units
			// proving the API is down. Hard-coding a zero budget here is
			// exactly the bug this recomputation removes.
			budget := p.budget(p.cfg.Client.Quota(), 0)
			delay := NextInterval(serverInterval, budget, p.cfg.MinInterval, p.cfg.MaxInterval, backoff)
			p.setEffective(delay)
			p.cfg.Sleep(ctx, delay)
			continue
		}

		// A success decays the ladder one step rather than clearing it, so a
		// flapping connection does not slam straight back to full cadence.
		if backoff > backoffFloor {
			backoff /= backoffStep
			if backoff < backoffFloor {
				backoff = backoffFloor
			}
		}

		if !connected {
			connected = true
			p.emitState(ctx, ConnectionState{
				Status: ConnectionConnected,
				ChatID: target.LiveChatID,
				Detail: connectedDetail(target),
				At:     p.cfg.Now(),
			})
		}

		delivered := p.emitResult(ctx, result)
		if delivered > 0 {
			lastItemAt = p.cfg.Now()
		}
		if result.PollingInterval > 0 {
			serverInterval = result.PollingInterval
		}

		// Retain the last good token. Falling back to a token-less request
		// re-delivers the entire backlog and charges for the privilege.
		if result.NextPageToken != "" {
			pageToken = result.NextPageToken
			p.setPageToken(pageToken)
		}
		priming = false

		if ended, reason := p.chatEnded(result); ended {
			p.emitRoom(ctx, RoomEvent{
				Type:       RoomChatEnded,
				LiveChatID: target.LiveChatID,
				Detail:     reason,
				At:         p.cfg.Now(),
			})
			p.setState(PollerEnded)
			p.emitState(ctx, ConnectionState{
				Status: ConnectionClosed,
				ChatID: target.LiveChatID,
				Detail: reason,
				At:     p.cfg.Now(),
			})
			p.parkQuietly(ctx)
			return
		}

		if !result.OfflineAt.IsZero() {
			if !offlineNoted {
				offlineNoted = true
				offlineNotedAt = p.cfg.Now()
				p.setState(PollerOffline)
				p.emitRoom(ctx, RoomEvent{
					Type:       RoomStreamOffline,
					LiveChatID: target.LiveChatID,
					Detail:     "the broadcast has ended; chat stays open a little longer",
					At:         p.cfg.Now(),
				})
			}
			// Chat outlives the stream, so going offline is a warning and
			// not a stop. The session ends only once the grace window has
			// passed with nothing new arriving: a room still talking after
			// the broadcast ended restarts the window rather than being cut
			// off mid-conversation.
			quietSince := offlineNotedAt
			if lastItemAt.After(quietSince) {
				quietSince = lastItemAt
			}
			if p.cfg.Now().After(quietSince.Add(OfflineGracePeriod)) {
				p.setState(PollerEnded)
				p.emitState(ctx, ConnectionState{
					Status: ConnectionClosed,
					ChatID: target.LiveChatID,
					Detail: "live chat closed after the broadcast ended",
					At:     p.cfg.Now(),
				})
				p.parkQuietly(ctx)
				return
			}
		} else if offlineNoted {
			offlineNoted = false
		}

		snapshot := p.cfg.Client.Quota()
		if detail, paused := p.reserveTripped(snapshot); paused {
			p.setState(PollerQuotaPaused)
			p.setMode(QuotaModePaused)
			p.emitState(ctx, ConnectionState{
				Status: ConnectionPaused,
				ChatID: target.LiveChatID,
				Detail: detail,
				At:     p.cfg.Now(),
			})
			p.parkQuietly(ctx)
			return
		}

		budget := p.budget(snapshot, result.QuotaUnits)
		interval := NextInterval(serverInterval, budget, p.cfg.MinInterval, p.cfg.MaxInterval, backoffFloor)

		p.mu.Lock()
		p.serverFloor = serverInterval
		p.budgetFloor = budget
		p.effective = interval
		if budget > serverInterval && budget > p.cfg.MinInterval {
			p.state = PollerStretched
			p.mode = QuotaModeStretched
		} else {
			p.state = PollerStreaming
			p.mode = QuotaModeLive
		}
		p.mu.Unlock()

		p.log(ctx, "youtube.poll.tick",
			slog.Int("messages", delivered),
			slog.String("interval", interval.String()),
			slog.String("server_floor", serverInterval.String()),
			slog.String("budget_floor", budget.String()),
			slog.Int("remaining_units_est", snapshot.RemainingUnits),
		)

		p.cfg.Sleep(ctx, interval)
	}
}

// resolve turns the configured target into one that can be polled.
func (p *Poller) resolve(ctx context.Context) (ChatTarget, error) {
	target := p.Target()
	if target.Resolved() {
		return target, nil
	}

	p.setState(PollerResolving)
	p.emitState(ctx, ConnectionState{
		Status: ConnectionConnecting,
		ChatID: target.LiveChatID,
		Detail: "resolving live chat",
		At:     p.cfg.Now(),
	})

	resolved, err := p.cfg.Client.ResolveTarget(ctx, target)
	if err != nil {
		return target, err
	}

	p.mu.Lock()
	p.target = resolved
	p.mu.Unlock()
	return resolved, nil
}

// handleListError classifies a failed list call, reporting whether the session
// should stop, what the next backoff multiplier is, and whether the caller
// should drop its continuation token and re-prime.
//
// serverInterval is the cadence YouTube last advised; it is the base the
// backoff ceiling has to be computed against, because NextInterval multiplies
// the effective base rather than the configured minimum.
//
// canDropToken tells the classifier that a token-rejection recovery is still
// available, so a stale cursor is recoverable exactly once per session. It is
// what separates "the chat is gone" from "this cursor is": the same 400 or 404
// is a re-prime with a token in hand and a terminal state without one.
func (p *Poller) handleListError(
	ctx context.Context,
	target ChatTarget,
	err error,
	backoff float64,
	serverInterval time.Duration,
	canDropToken bool,
) (stop bool, next float64, dropToken bool) {
	now := p.cfg.Now()
	// The ceiling has to be measured against the interval actually in force.
	// Using MinInterval here let a 5s server cadence stretch a 60s cap to
	// 300s, silently stalling the chat for five minutes.
	base := p.cfg.MinInterval
	if serverInterval > base {
		base = serverInterval
	}
	switch {
	case errors.Is(err, ErrQuotaExceeded):
		// Never retry an exhausted quota: every attempt is charged, and the
		// allowance does not come back until the Pacific reset.
		p.setState(PollerQuotaPaused)
		p.setMode(QuotaModePaused)
		p.emitState(ctx, ConnectionState{
			Status: ConnectionPaused,
			ChatID: target.LiveChatID,
			Detail: "daily quota exhausted; polling paused until " + ResetAt(now).Local().Format("15:04 MST") + " (est.)",
			Err:    err,
			At:     now,
		})
		return true, backoff, false

	case canDropToken && (errors.Is(err, ErrMessageRejected) || errors.Is(err, ErrChatNotFound)):
		// A 400 or a 404 on the list path, with a continuation token in
		// hand, is far more likely to be about the token than about the
		// chat: yc stretches its cadence to tens of seconds to survive the
		// daily quota, which is exactly the regime that lets a cursor go
		// stale. Ending a live chat over one would leave ctrl+r as the only
		// recovery, so drop the cursor and re-prime instead - the dedupe
		// ring keeps the re-primed page from reprinting what is on screen.
		//
		// The recovery is available once per session (canDropToken), so a
		// chat that really has ended still ends: the token-less retry hits
		// the terminal cases below and stops there, one call later.
		next = climb(backoff, base, backoffTransientCap)
		p.emitState(ctx, ConnectionState{
			Status: ConnectionReconnecting,
			ChatID: target.LiveChatID,
			Detail: "chat cursor expired; reloading recent messages",
			Err:    err,
			At:     now,
		})
		return false, next, true

	case errors.Is(err, ErrChatEnded), errors.Is(err, ErrChatDisabled), errors.Is(err, ErrChatNotFound):
		p.setState(PollerEnded)
		p.emitRoom(ctx, RoomEvent{
			Type:       RoomChatEnded,
			LiveChatID: target.LiveChatID,
			Detail:     safeDetail(err),
			At:         now,
		})
		p.emitState(ctx, ConnectionState{
			Status: ConnectionClosed,
			ChatID: target.LiveChatID,
			Detail: safeDetail(err),
			Err:    err,
			At:     now,
		})
		return true, backoff, false

	case errors.Is(err, ErrAuthFailed), errors.Is(err, ErrNotPermitted), errors.Is(err, ErrNoCredentials):
		// One refresh is the credential holder's job, above this layer. A
		// poller that retried here would spend quota proving the same
		// credential is still wrong.
		p.setState(PollerEnded)
		p.emitState(ctx, ConnectionState{
			Status: ConnectionFailed,
			ChatID: target.LiveChatID,
			Detail: safeDetail(err),
			Err:    err,
			At:     now,
		})
		return true, backoff, false

	case errors.Is(err, ErrRateLimited):
		next = climb(backoff, base, backoffRateLimitCap)
		p.emitState(ctx, ConnectionState{
			Status: ConnectionReconnecting,
			ChatID: target.LiveChatID,
			Detail: "the API asked yc to slow down; backing off",
			Err:    err,
			At:     now,
		})
		return false, next, false

	case Retryable(err):
		next = climb(backoff, base, backoffTransientCap)
		p.emitState(ctx, ConnectionState{
			Status: ConnectionReconnecting,
			ChatID: target.LiveChatID,
			Detail: safeDetail(err),
			Err:    err,
			At:     now,
		})
		return false, next, false

	default:
		p.setState(PollerEnded)
		p.emitState(ctx, ConnectionState{
			Status: ConnectionFailed,
			ChatID: target.LiveChatID,
			Detail: safeDetail(err),
			Err:    err,
			At:     now,
		})
		return true, backoff, false
	}
}

// climb advances the backoff multiplier, capped so the resulting interval
// never exceeds ceiling.
func climb(backoff float64, base, ceiling time.Duration) float64 {
	if base <= 0 {
		base = defaultMinInterval
	}
	next := backoff * backoffStep
	if limit := float64(ceiling) / float64(base); next > limit {
		next = limit
	}
	if next < backoffFloor {
		next = backoffFloor
	}
	return next
}

// budget computes the cadence the remaining estimated units imply.
func (p *Poller) budget(snapshot QuotaSnapshot, lastCost int) time.Duration {
	if p.cfg.FollowServerCadence {
		return 0
	}
	if snapshot.LimitUnits <= 0 {
		// No ledger is wired, so there is no budget to protect.
		return 0
	}
	cost := lastCost
	if cost <= 0 {
		cost = p.cfg.Client.CostOf(EndpointMessagesList)
	}

	// The injected clock, not the wall clock: PollerConfig promises the whole
	// state machine can be driven by a fake Now, and time.Until here silently
	// zeroed the budget floor whenever a test anchored its clock away from
	// the present.
	horizon := snapshot.ResetAt.Sub(p.cfg.Now())
	if snapshot.ResetAt.IsZero() {
		horizon = ResetAt(p.cfg.Now()).Sub(p.cfg.Now())
	}
	if p.cfg.SessionHorizon > 0 && p.cfg.SessionHorizon < horizon {
		horizon = p.cfg.SessionHorizon
	}
	return BudgetFloor(snapshot.RemainingUnits, cost, horizon)
}

// reserveTripped reports whether polling must stop to protect the reserve.
//
// The reserve exists so that running out of read budget does not also take away
// the ability to send or moderate, which is the half of the client a stream
// owner cannot do without.
func (p *Poller) reserveTripped(snapshot QuotaSnapshot) (string, bool) {
	if snapshot.LimitUnits <= 0 {
		return "", false
	}
	if snapshot.RemainingUnits <= 0 {
		return "daily quota exhausted (est.); polling paused until " +
			snapshot.ResetAt.Local().Format("15:04 MST") + ". Press ctrl+r to override", true
	}
	if p.cfg.ReservePercent <= 0 {
		return "", false
	}
	reserve := snapshot.LimitUnits * p.cfg.ReservePercent / 100
	if snapshot.RemainingUnits > reserve {
		return "", false
	}
	return "quota reserve reached (est.); sends still available, polling paused until " +
		snapshot.ResetAt.Local().Format("15:04 MST") + ". Press ctrl+r to override", true
}

// chatEnded reports whether this response says the chat is over.
func (p *Poller) chatEnded(result ListResult) (bool, string) {
	for _, event := range result.RoomEvents {
		if event.Type == RoomChatEnded {
			detail := strings.TrimSpace(event.Detail)
			if detail == "" {
				detail = "live chat ended"
			}
			return true, detail
		}
	}
	return false, ""
}

// emitResult publishes one normalized response and reports how many chat rows
// were delivered.
func (p *Poller) emitResult(ctx context.Context, result ListResult) int {
	delivered := 0
	for _, message := range result.Messages {
		if message.ID != "" && !p.seen.add(message.ID) {
			continue
		}
		if p.emitMessage(ctx, message) {
			delivered++
		}
	}
	for _, event := range result.Moderations {
		p.emitModeration(ctx, event)
	}
	for _, event := range result.RoomEvents {
		if event.Type == RoomChatEnded {
			// Emitted by the caller alongside the closed connection state,
			// so the two cannot disagree about why the chat stopped.
			continue
		}
		p.emitRoom(ctx, event)
	}
	for _, poll := range result.Polls {
		p.emitPoll(ctx, poll)
	}
	return delivered
}

// emitMessage publishes a chat row, dropping rather than blocking.
func (p *Poller) emitMessage(ctx context.Context, message Message) bool {
	select {
	case p.messages <- message:
		return true
	case <-ctx.Done():
		return false
	default:
		// Blocking here would stall the goroutine that owns the poll
		// schedule and cost the whole session, so the row is dropped and the
		// loss is counted for the status bar to show.
		p.dropped.Add(1)
		return false
	}
}

func (p *Poller) emitState(ctx context.Context, state ConnectionState) {
	select {
	case p.states <- state:
	case <-ctx.Done():
	}
}

func (p *Poller) emitModeration(ctx context.Context, event ModerationEvent) {
	select {
	case p.moderations <- event:
	case <-ctx.Done():
	}
}

func (p *Poller) emitRoom(ctx context.Context, event RoomEvent) {
	select {
	case p.rooms <- event:
	case <-ctx.Done():
	}
}

func (p *Poller) emitPoll(ctx context.Context, poll PollState) {
	select {
	case p.polls <- poll:
	case <-ctx.Done():
	}
}

// park reports a terminal failure and then waits for cancellation.
func (p *Poller) park(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	if state := p.State(); state != PollerEnded && state != PollerQuotaPaused {
		p.setState(PollerEnded)
		p.emitState(ctx, ConnectionState{
			Status: ConnectionFailed,
			ChatID: p.liveChatID(),
			Detail: safeDetail(err),
			Err:    err,
			At:     p.cfg.Now(),
		})
	}
	p.parkQuietly(ctx)
}

// parkQuietly holds the session open without polling.
//
// The streams stay open on purpose. The adapter above restarts a transport
// whose streams close, and restarting a chat that ended, or a quota that is
// exhausted, spends units to be told the same thing again. Closing is the
// user's decision, taken with ctrl+r.
func (p *Poller) parkQuietly(ctx context.Context) {
	p.setEffective(0)
	<-ctx.Done()
}

// --- small helpers ---------------------------------------------------------

func (p *Poller) setState(state PollerState) {
	p.mu.Lock()
	p.state = state
	p.mu.Unlock()
}

func (p *Poller) setMode(mode QuotaMode) {
	p.mu.Lock()
	p.mode = mode
	p.mu.Unlock()
}

func (p *Poller) setEffective(d time.Duration) {
	p.mu.Lock()
	p.effective = d
	p.mu.Unlock()
}

func (p *Poller) liveChatID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.target.LiveChatID
}

// pageToken returns the last good continuation token, which is what a
// reconnect resumes from rather than re-priming the whole backlog.
func (p *Poller) pageToken() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.token
}

// setPageToken retains a continuation token.
//
// An empty nextPageToken never clears the retained one: falling back to a
// token-less request re-delivers the entire backlog and charges for it.
func (p *Poller) setPageToken(token string) {
	if token == "" {
		return
	}
	p.mu.Lock()
	p.token = token
	p.mu.Unlock()
}

// resetPageToken forgets the retained continuation token.
//
// It is called only when the API has rejected that token, which is the one
// circumstance in which re-priming is cheaper than resuming: every later
// reconnect would otherwise present the same refused cursor.
func (p *Poller) resetPageToken() {
	p.mu.Lock()
	p.token = ""
	p.mu.Unlock()
}

func (p *Poller) log(ctx context.Context, event string, attrs ...slog.Attr) {
	if !p.cfg.Logger.Enabled() {
		return
	}
	p.cfg.Logger.Log(ctx, event, attrs...)
}

// connectedDetail names what the poller attached to, without an ID: a live chat
// ID is not a secret, but it is noise in a status line that has a title.
func connectedDetail(target ChatTarget) string {
	if label := strings.TrimSpace(target.Label()); label != "" {
		return "connected to " + label
	}
	return "connected"
}

// safeDetail renders an error for the status line. Every error reaching here
// came from the transport, which redacts before wrapping, so this only bounds
// the length.
func safeDetail(err error) string {
	if err == nil {
		return ""
	}
	return truncateRunes(strings.Join(strings.Fields(err.Error()), " "), 160)
}

// sleepContext waits for d or for cancellation, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// NextInterval computes the cadence for the next poll:
//
//	clamp(max(serverFloor, budgetFloor, configMin), configMin, configMax)
//	  * backoffMultiplier, plus jitter
//
// Jitter is +/-10% normally and full jitter while backing off, so restarts and
// multiple yc instances do not synchronize onto the same second. serverFloor is
// an absolute floor beneath everything else.
func NextInterval(serverFloor, budgetFloor, configMin, configMax time.Duration, backoff float64) time.Duration {
	if configMin <= 0 {
		configMin = defaultMinInterval
	}
	base := configMin
	if serverFloor > base {
		base = serverFloor
	}
	if budgetFloor > base {
		base = budgetFloor
	}
	if base < configMin {
		base = configMin
	}
	if configMax > 0 && base > configMax {
		base = configMax
	}

	// The absolute floor is whichever of the server's advice and the local
	// minimum is larger. Nothing below it is ever returned, jitter included.
	floor := configMin
	if serverFloor > floor {
		floor = serverFloor
	}

	if backoff > backoffFloor {
		ceiling := time.Duration(float64(base) * backoff)
		if ceiling < floor {
			ceiling = floor
		}
		// Full jitter while backing off: picking uniformly across the whole
		// window is what stops a fleet of clients retrying in lockstep after
		// a shared outage.
		span := ceiling - floor
		if span <= 0 {
			return floor
		}
		return floor + time.Duration(rand.Int64N(int64(span)+1)) //nolint:gosec // jitter needs speed, not unpredictability
	}

	spread := time.Duration(float64(base) * pollJitterFraction)
	interval := base
	if spread > 0 {
		interval = base - spread + time.Duration(rand.Int64N(int64(2*spread)+1)) //nolint:gosec // jitter needs speed, not unpredictability
	}
	if interval < floor {
		interval = floor
	}
	return interval
}

// --- dedupe ----------------------------------------------------------------

// dedupeRing remembers recently seen message IDs in bounded space.
//
// Deduplication is not optional: a retained page token, a retry, and a
// reconnect all re-deliver rows, and a chat client that prints the same
// Super Chat twice is one the viewer stops trusting.
type dedupeRing struct {
	mu    sync.Mutex
	size  int
	seen  map[string]struct{}
	order []string
	next  int
}

func newDedupeRing(size int) *dedupeRing {
	if size <= 0 {
		size = DedupeRingSize
	}
	return &dedupeRing{
		size:  size,
		seen:  make(map[string]struct{}, size),
		order: make([]string, size),
	}
}

// add records an ID and reports whether it is new.
func (r *dedupeRing) add(id string) bool {
	if r == nil || id == "" {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.seen[id]; ok {
		return false
	}
	if evicted := r.order[r.next]; evicted != "" {
		delete(r.seen, evicted)
	}
	r.order[r.next] = id
	r.next = (r.next + 1) % r.size
	r.seen[id] = struct{}{}
	return true
}

// has reports whether an ID was seen recently, without recording it. It backs
// tombstone correlation: a tombstone for a message yc never showed has nothing
// to redact.
func (r *dedupeRing) has(id string) bool {
	if r == nil || id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.seen[id]
	return ok
}

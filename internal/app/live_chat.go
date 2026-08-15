package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/youtube"
)

// Reconnect ladder bounds. The ladder exists because an HTTP poll failure is
// usually transient, and because the alternative - a frozen UI with no
// explanation - is worse than a visible reconnecting state.
const (
	reconnectInitialDelay = 2 * time.Second
	reconnectMaxDelay     = 60 * time.Second
	reconnectAttemptLimit = 10
	// reconnectResetAfter is how long a session must survive before the
	// ladder is considered healed. Without it a transport that fails a
	// minute into every attempt would never leave the top of the ladder.
	reconnectResetAfter = 30 * time.Second
	// liveChatBuffer sizes the merged streams. Dropping is preferable to
	// blocking the emitter: blocking stalls the goroutine that owns the poll
	// schedule and costs the whole session, and the loss is made visible by
	// DroppedMessages.
	liveChatBuffer = 1024
)

// LiveChatTransportFactory builds a poller for one chat. It is injectable so
// the app-level reconnect ladder can be tested without a network.
type LiveChatTransportFactory func(target youtube.ChatTarget) (LiveChatTransport, error)

// LiveChatTransport is the transport surface the live adapter drives. The
// concrete implementation is a youtube.Poller; the interface exists so the
// adapter's reconnect and fan-in logic is testable against a fake.
type LiveChatTransport interface {
	Start(ctx context.Context) error
	// Target reports the transport's view of the chat, including anything it
	// resolved for itself. The session adopts it so a later reconnect does
	// not pay to resolve the same chat again.
	Target() youtube.ChatTarget
	Messages() <-chan youtube.Message
	ConnectionStates() <-chan youtube.ConnectionState
	Moderations() <-chan youtube.ModerationEvent
	RoomEvents() <-chan youtube.RoomEvent
	Polls() <-chan youtube.PollState
	Send(ctx context.Context, request youtube.SendRequest) (youtube.SendResult, error)
	Quota() youtube.QuotaSnapshot
	Close() error
}

// LiveChatReconnector is the optional capability a transport advertises when it
// can restart itself in place.
//
// Rebuilding a transport is the expensive way to reconnect: the replacement
// starts with no continuation token, so it re-primes from the head of the chat
// and re-delivers a window of messages the viewer has already read, and it has
// no memory of which IDs those were. A transport that can restart itself keeps
// its cursor, its resolved target, and its dedupe memory, so ctrl+r costs one
// list call and shows nothing twice. youtube.Poller implements it; the
// interface is declared here rather than imported so internal/app keeps its
// distance from the concrete API types.
type LiveChatReconnector interface {
	Reconnect(ctx context.Context) error
}

// LiveChatConfig configures the live adapter.
type LiveChatConfig struct {
	Factory LiveChatTransportFactory
	Targets []youtube.ChatTarget
	Logger  debuglog.Logger
	// Moderator performs outbound moderation. It is nil-able: a key-only read
	// session has nothing that can moderate, and the shell must still run.
	//
	// It is supplied here rather than reached through a transport because
	// moderation is not per-chat - liveChatMessages.delete and
	// liveChatBans.insert are calls on the credential, not on a poll loop -
	// and because a chat whose poller is mid-reconnect must still be
	// moderatable. A transport that implements Moderator itself is used as a
	// fallback when this is nil.
	Moderator Moderator
}

// LiveChatClient fans several per-chat pollers into the single set of streams
// the shell consumes, and owns the reconnect ladder.
//
// The transport is built through a factory rather than injected directly, so a
// restart replaces the transport without the shell losing any per-chat UI
// state: the model keys history, drafts, and scroll position by chat, and the
// adapter swaps only what is underneath.
//
// It satisfies ChatClient plus the optional ModerationSource, RoomEventSource,
// PollSource, ChatJoiner, QuotaReporter, MessageDropCounter, Moderator, and
// ModerationCapability capabilities.
type LiveChatClient struct {
	cfg liveChatDeps

	messages    chan youtube.Message
	states      chan youtube.ConnectionState
	moderations chan youtube.ModerationEvent
	rooms       chan youtube.RoomEvent
	polls       chan youtube.PollState

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*liveChatSession
	closed   bool
	// lastQuota is the most recent snapshot any live transport reported. It
	// outlives the sessions so closing the last chat does not blank the
	// quota meter.
	lastQuota youtube.QuotaSnapshot

	wg      sync.WaitGroup
	dropped atomic.Uint64
	// closeOnce guarantees the merged channels are closed exactly once, no
	// matter how many sessions end or how often Close is called.
	closeOnce sync.Once
}

// liveChatDeps is the stored configuration. It is a distinct type only so the
// factory and logger travel together into every session.
type liveChatDeps struct {
	factory   LiveChatTransportFactory
	logger    debuglog.Logger
	moderator Moderator
}

var (
	_ ChatClient           = (*LiveChatClient)(nil)
	_ ModerationSource     = (*LiveChatClient)(nil)
	_ RoomEventSource      = (*LiveChatClient)(nil)
	_ PollSource           = (*LiveChatClient)(nil)
	_ ChatJoiner           = (*LiveChatClient)(nil)
	_ QuotaReporter        = (*LiveChatClient)(nil)
	_ MessageDropCounter   = (*LiveChatClient)(nil)
	_ Moderator            = (*LiveChatClient)(nil)
	_ ModerationCapability = (*LiveChatClient)(nil)
)

// NewLiveChatClient returns an adapter for the configured targets. Sessions
// start immediately; an empty target list is a supported state, so the adapter
// simply waits for OpenChat.
func NewLiveChatClient(cfg LiveChatConfig) (*LiveChatClient, error) {
	if cfg.Factory == nil {
		return nil, errors.New("live chat client: missing transport factory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := &LiveChatClient{
		cfg:         liveChatDeps{factory: cfg.Factory, logger: cfg.Logger, moderator: cfg.Moderator},
		messages:    make(chan youtube.Message, liveChatBuffer),
		states:      make(chan youtube.ConnectionState, liveChatBuffer),
		moderations: make(chan youtube.ModerationEvent, liveChatBuffer),
		rooms:       make(chan youtube.RoomEvent, liveChatBuffer),
		polls:       make(chan youtube.PollState, liveChatBuffer),
		ctx:         ctx,
		cancel:      cancel,
		sessions:    make(map[string]*liveChatSession),
	}
	for _, target := range cfg.Targets {
		client.startSession(target)
	}
	return client, nil
}

// Messages returns the merged chat message stream.
func (c *LiveChatClient) Messages() <-chan youtube.Message { return c.messages }

// ConnectionStates returns the merged connection lifecycle stream.
func (c *LiveChatClient) ConnectionStates() <-chan youtube.ConnectionState { return c.states }

// Moderations returns the merged moderation event stream.
func (c *LiveChatClient) Moderations() <-chan youtube.ModerationEvent { return c.moderations }

// RoomEvents returns the merged room-wide event stream.
func (c *LiveChatClient) RoomEvents() <-chan youtube.RoomEvent { return c.rooms }

// Polls returns the merged creator poll stream.
func (c *LiveChatClient) Polls() <-chan youtube.PollState { return c.polls }

// Send dispatches to the session that owns the request's chat. A request with
// no chat ID goes to the only open session, which is what a single-chat run
// means by it.
func (c *LiveChatClient) Send(ctx context.Context, request youtube.SendRequest) (youtube.SendResult, error) {
	session := c.sessionForChatID(request.LiveChatID)
	if session == nil {
		return youtube.SendResult{}, errors.New("send: no open chat for this request")
	}
	transport := session.currentTransport()
	if transport == nil {
		return youtube.SendResult{}, errors.New("send: chat is not connected")
	}
	return transport.Send(ctx, request)
}

// --- moderation ------------------------------------------------------------

// ModerationAvailable reports whether this adapter has anything that can
// moderate, and why not when it does not.
//
// The shell asks before it arms a destructive confirmation. Asserting Moderator
// alone would always succeed - the methods exist on the type whether or not a
// credential was wired - and the user would only find out after confirming a
// ban that yc never had a way to issue one.
func (c *LiveChatClient) ModerationAvailable() (bool, string) {
	if c.moderator() == nil {
		return false, "this session has no credential that can moderate; run `yc login` and grant youtube.force-ssl"
	}
	return true, ""
}

// moderator returns whatever can perform outbound moderation, preferring the
// configured credential and falling back to a transport that can moderate for
// its own chat.
func (c *LiveChatClient) moderator() Moderator {
	if c.cfg.moderator != nil {
		return c.cfg.moderator
	}
	c.mu.Lock()
	sessions := make([]*liveChatSession, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	c.mu.Unlock()
	for _, session := range sessions {
		transport := session.currentTransport()
		if transport == nil {
			continue
		}
		if moderator, ok := transport.(Moderator); ok {
			return moderator
		}
	}
	return nil
}

// DeleteMessage removes one chat message.
//
// A successful delete is echoed locally by the shell rather than waited for on
// the message stream: the API does not reliably report a deletion back, so a
// caller that waited would watch the row stay on screen indefinitely.
func (c *LiveChatClient) DeleteMessage(ctx context.Context, messageID string) error {
	moderator := c.moderator()
	if moderator == nil {
		return ErrModerationUnavailable
	}
	return moderator.DeleteMessage(ctx, messageID)
}

// Ban times out or permanently bans a chatter. A zero Duration is permanent.
//
// The request's chat identifier is translated to the resolved liveChatId first.
// The shell addresses chats by the routing key it opened them under, which for
// a chat opened by video ID or handle is not a liveChatId at all, and the bans
// endpoint accepts nothing else.
func (c *LiveChatClient) Ban(ctx context.Context, request youtube.BanRequest) (youtube.BanResult, error) {
	moderator := c.moderator()
	if moderator == nil {
		return youtube.BanResult{}, ErrModerationUnavailable
	}
	request.LiveChatID = c.resolveLiveChatID(request.LiveChatID)
	return moderator.Ban(ctx, request)
}

// Unban lifts a ban.
func (c *LiveChatClient) Unban(ctx context.Context, banID string) error {
	moderator := c.moderator()
	if moderator == nil {
		return ErrModerationUnavailable
	}
	return moderator.Unban(ctx, banID)
}

// resolveLiveChatID maps whatever identifier the shell holds onto the liveChatId
// the session resolved, leaving it untouched when no session claims it.
func (c *LiveChatClient) resolveLiveChatID(id string) string {
	session := c.sessionForChatID(id)
	if session == nil {
		return id
	}
	if resolved := strings.TrimSpace(session.currentTarget().LiveChatID); resolved != "" {
		return resolved
	}
	if transport := session.currentTransport(); transport != nil {
		if resolved := strings.TrimSpace(transport.Target().LiveChatID); resolved != "" {
			return resolved
		}
	}
	return id
}

// OpenChat starts polling an additional chat without disturbing the existing
// sessions.
func (c *LiveChatClient) OpenChat(ctx context.Context, target youtube.ChatTarget) (youtube.ChatTarget, error) {
	if err := ctx.Err(); err != nil {
		return target, err
	}
	key := target.Key()
	if key == "" {
		return target, errors.New("open chat: empty target")
	}
	if !c.startSession(target) {
		// Either the chat is already open, which is a no-op, or the client
		// closed. Both leave the caller's target untouched.
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return target, errors.New("open chat: client closed")
		}
	}
	return target, nil
}

// CloseChat stops polling one chat. The session's context is canceled and its
// transport closed before the entry is dropped, so nothing keeps spending quota
// on a chat the user closed.
func (c *LiveChatClient) CloseChat(chatKey string) error {
	key := strings.ToLower(strings.TrimSpace(chatKey))
	c.mu.Lock()
	session, ok := c.sessions[key]
	if ok {
		delete(c.sessions, key)
	}
	c.mu.Unlock()
	if !ok {
		return nil
	}
	session.stop()
	return nil
}

// Reconnect restarts every session in place.
//
// Each session's current transport is closed, which ends its fan-in and drops
// the session back onto the ladder with a fresh attempt count. The old context
// is canceled and the old transport closed before a replacement exists, so two
// pollers can never charge quota for the same chat at once.
func (c *LiveChatClient) Reconnect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrReconnectUnavailable
	}
	sessions := make([]*liveChatSession, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	c.mu.Unlock()
	if len(sessions) == 0 {
		return ErrReconnectUnavailable
	}
	for _, session := range sessions {
		session.restart()
	}
	return nil
}

// Quota returns the merged estimated ledger snapshot.
//
// Sessions normally share one ledger, so the snapshots are the same object seen
// from several places; the largest spend wins rather than being summed, because
// summing a shared ledger would multiply the estimate by the number of open
// chats and tell the user they have far less budget than they do.
func (c *LiveChatClient) Quota() youtube.QuotaSnapshot {
	c.mu.Lock()
	sessions := make([]*liveChatSession, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	c.mu.Unlock()

	var merged youtube.QuotaSnapshot
	answered := false
	for _, session := range sessions {
		transport := session.currentTransport()
		if transport == nil {
			continue
		}
		snapshot := transport.Quota()
		if !answered || snapshot.UsedUnits >= merged.UsedUnits {
			merged = snapshot
			answered = true
		}
	}
	if !answered {
		// No session can answer - every chat is closed, or none was ever
		// opened. Returning the zero value would tell the status bar the
		// daily limit is zero units, which on a client whose headline
		// constraint is quota is worse than saying nothing. The last
		// snapshot a live transport gave us is still the truth about what
		// today has spent.
		c.mu.Lock()
		merged = c.lastQuota
		c.mu.Unlock()
		return merged
	}
	c.mu.Lock()
	c.lastQuota = merged
	c.mu.Unlock()
	return merged
}

// PollInterval reports the shortest effective cadence across open chats, which
// is the one the status bar should show: it is the rate at which the user can
// expect the next message.
func (c *LiveChatClient) PollInterval() time.Duration {
	c.mu.Lock()
	sessions := make([]*liveChatSession, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	c.mu.Unlock()

	var shortest time.Duration
	for _, session := range sessions {
		transport := session.currentTransport()
		if transport == nil {
			continue
		}
		interval := transport.Quota().EffectiveInterval
		if interval <= 0 {
			continue
		}
		if shortest == 0 || interval < shortest {
			shortest = interval
		}
	}
	return shortest
}

// DroppedMessages returns how many messages were discarded because the UI could
// not keep up.
func (c *LiveChatClient) DroppedMessages() uint64 {
	return c.dropped.Load()
}

// Close cancels every session and closes the merged streams. The old context is
// canceled and the old channels closed before any replacement exists, and the
// call is safe to repeat.
func (c *LiveChatClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	sessions := make([]*liveChatSession, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	c.sessions = make(map[string]*liveChatSession)
	c.mu.Unlock()

	c.cancel()
	for _, session := range sessions {
		session.stop()
	}
	c.wg.Wait()
	c.closeOnce.Do(func() {
		close(c.messages)
		close(c.states)
		close(c.moderations)
		close(c.rooms)
		close(c.polls)
	})
	return nil
}

// sessionForChatID finds the session a request belongs to. The shell addresses
// a chat by whichever identifier it holds - the routing key it opened the chat
// under, or the liveChatId once resolution supplies one - so both are matched,
// including the transport's own in-flight resolution.
func (c *LiveChatClient) sessionForChatID(liveChatID string) *liveChatSession {
	c.mu.Lock()
	sessions := make([]*liveChatSession, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	direct, ok := c.sessions[strings.ToLower(strings.TrimSpace(liveChatID))]
	c.mu.Unlock()

	id := strings.TrimSpace(liveChatID)
	if id != "" {
		if ok {
			return direct
		}
		for _, session := range sessions {
			if session.matchesChatID(id) {
				return session
			}
		}
	}
	if len(sessions) == 1 {
		return sessions[0]
	}
	return nil
}

// startSession registers and launches one chat's session, reporting whether it
// actually started.
//
// The closed check, the map insert, and the wg.Add all happen under one hold of
// the mutex. Splitting them let a Close interleave: it would drain the sessions
// map, wait on a WaitGroup with nothing outstanding, and close the five merged
// channels - and then this call would launch a goroutine that emits onto a
// closed channel and panics the process.
func (c *LiveChatClient) startSession(target youtube.ChatTarget) bool {
	key := target.Key()
	if key == "" {
		return false
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	if _, exists := c.sessions[key]; exists {
		c.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(c.ctx)
	session := &liveChatSession{
		client:     c,
		routingKey: key,
		target:     target,
		ctx:        ctx,
		cancel:     cancel,
	}
	c.sessions[key] = session
	c.wg.Add(1)
	c.mu.Unlock()

	go func() {
		defer c.wg.Done()
		session.run()
	}()
	return true
}

// emitMessage forwards a chat message, dropping rather than blocking. See
// liveChatBuffer for why that trade is the right one.
func (c *LiveChatClient) emitMessage(message youtube.Message) {
	select {
	case c.messages <- message:
	default:
		c.dropped.Add(1)
	}
}

// emitState forwards a lifecycle transition. Connection states are rare and
// load-bearing - a dropped "failed" leaves the UI claiming to be connected - so
// this one waits rather than dropping, bounded by the session context.
func (c *LiveChatClient) emitState(ctx context.Context, state youtube.ConnectionState) {
	select {
	case c.states <- state:
	case <-ctx.Done():
	}
}

func (c *LiveChatClient) emitModeration(ctx context.Context, event youtube.ModerationEvent) {
	select {
	case c.moderations <- event:
	case <-ctx.Done():
	}
}

func (c *LiveChatClient) emitRoomEvent(ctx context.Context, event youtube.RoomEvent) {
	select {
	case c.rooms <- event:
	case <-ctx.Done():
	}
}

func (c *LiveChatClient) emitPoll(ctx context.Context, poll youtube.PollState) {
	select {
	case c.polls <- poll:
	case <-ctx.Done():
	}
}

// liveChatSession owns one chat's transport and its place on the reconnect
// ladder.
type liveChatSession struct {
	client *LiveChatClient
	// routingKey is the identity the shell files this chat's state under. It
	// is fixed at construction and never changes, which is the whole point:
	// the transport resolves a liveChatId of its own and stamps it on the
	// events it emits, and the shell - which opened the chat by video ID and
	// may not have resolved it yet - cannot match that ID to a chat. Every
	// event leaves this session wearing the routing key instead, so a message
	// can never be filed under whichever chat happens to be on screen.
	routingKey string

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	target youtube.ChatTarget

	transport LiveChatTransport
	// stopped records that stop() has already run. Canceling the context is
	// not enough on its own: stop() closes whichever transport is published at
	// the moment it runs, so a transport published afterwards would never be
	// closed by anyone. See publishTransport.
	stopped bool
	// pendingRestart records a restart that arrived before there was a
	// transport to close. Like stop(), restart() acts on whatever is published
	// when it runs, so a restart during the initial connect would otherwise be
	// dropped and leave the session fanning in a transport nobody asked it to
	// keep. See publishTransport.
	pendingRestart bool
	// explicitRestart marks a cycle the user asked for, so a manual
	// reconnect starts at the bottom of the ladder rather than inheriting
	// the backoff of whatever failed before it.
	explicitRestart bool
}

func (s *liveChatSession) currentTransport() LiveChatTransport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transport
}

// currentTarget returns the session's target, which grows as the transport
// resolves it.
func (s *liveChatSession) currentTarget() youtube.ChatTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target
}

// matchesChatID reports whether this session owns the given identifier, which
// may be the routing key, the adopted liveChatId, or a liveChatId the running
// transport resolved but the session has not adopted yet.
func (s *liveChatSession) matchesChatID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if strings.EqualFold(s.routingKey, id) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(s.currentTarget().LiveChatID), id) {
		return true
	}
	if transport := s.currentTransport(); transport != nil {
		if strings.EqualFold(strings.TrimSpace(transport.Target().LiveChatID), id) {
			return true
		}
	}
	return false
}

// adoptTarget folds whatever the transport resolved back into the session, so
// the next trip through the reconnect ladder starts from a resolved target
// instead of paying another videos.list unit and re-priming from scratch.
func (s *liveChatSession) adoptTarget(resolved youtube.ChatTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := strings.TrimSpace(resolved.LiveChatID); id != "" {
		s.target.LiveChatID = id
	}
	if id := strings.TrimSpace(resolved.VideoID); id != "" {
		s.target.VideoID = id
	}
	if id := strings.TrimSpace(resolved.ChannelID); id != "" {
		s.target.ChannelID = id
	}
	if title := strings.TrimSpace(resolved.Title); title != "" {
		s.target.Title = title
	}
	if resolved.Kind != "" && resolved.Kind != youtube.TargetUnknown {
		s.target.Kind = resolved.Kind
	}
}

func (s *liveChatSession) setTransport(transport LiveChatTransport) {
	s.mu.Lock()
	s.transport = transport
	s.mu.Unlock()
}

// publishOutcome tells the run loop what to do with a transport it just built.
type publishOutcome int

const (
	// publishAccepted means the transport is live and may be started.
	publishAccepted publishOutcome = iota
	// publishStopped means the session was stopped while the transport was
	// being built; the caller closes it and returns.
	publishStopped
	// publishSuperseded means a restart arrived while the transport was being
	// built; the caller closes it and builds a replacement.
	publishSuperseded
)

// publishTransport stores a freshly built transport and reports whether the
// caller may keep it.
//
// stop() and restart() both act on whatever transport is published at the
// instant they run. The run loop checks for cancellation before it builds a
// transport, so either call can land in the window between that check and the
// publish, find nil, and do nothing — leaving the run loop to fan in streams
// that nobody will ever close. forward() then blocks forever: Close() hangs on
// its WaitGroup, and a reconnect silently wedges the chat instead of rebuilding
// it. Publishing and the flag checks share one lock hold so the window cannot
// reopen between them.
func (s *liveChatSession) publishTransport(transport LiveChatTransport) publishOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return publishStopped
	}
	if s.pendingRestart {
		s.pendingRestart = false
		return publishSuperseded
	}
	s.transport = transport
	return publishAccepted
}

// stop cancels the session and closes its transport. Cancellation comes first
// so the transport's own context-aware work stops before it is torn down.
func (s *liveChatSession) stop() {
	s.cancel()
	s.mu.Lock()
	s.stopped = true
	transport := s.transport
	s.mu.Unlock()
	if transport != nil {
		_ = transport.Close()
	}
}

// restart puts the session back on a live connection.
//
// A transport that can restart itself is asked to, and nothing else happens:
// its streams stay open, so the fan-in never stops, and it resumes from its own
// continuation token instead of re-priming and reprinting the last few minutes
// of chat. Only a transport that cannot - or one whose restart failed - is
// closed, which ends the fan-in and drops the run loop back onto the ladder to
// build a replacement.
func (s *liveChatSession) restart() {
	transport := s.currentTransport()
	if reconnector, ok := transport.(LiveChatReconnector); ok {
		err := reconnector.Reconnect(s.ctx)
		if err == nil {
			return
		}
		s.client.cfg.logger.Log(s.ctx, "app.live_chat.restart_in_place_failed",
			debuglog.Err("error", err),
		)
	}

	// explicitRestart is set only on this path: it tells the run loop that
	// the cycle it is about to make was asked for rather than suffered, and
	// an in-place restart never reaches the run loop at all.
	s.mu.Lock()
	s.explicitRestart = true
	// A restart with nothing published yet is recorded rather than dropped:
	// the run loop is mid-build and will honor it as soon as it publishes.
	s.pendingRestart = transport == nil && !s.stopped
	s.mu.Unlock()
	if transport != nil {
		_ = transport.Close()
	}
}

func (s *liveChatSession) run() {
	delay := reconnectInitialDelay
	attempts := 0

	for {
		if s.ctx.Err() != nil {
			return
		}
		// The target is re-read every attempt rather than captured once: a
		// previous attempt may have resolved it, and handing the factory the
		// resolved target is what keeps a reconnect from re-spending a
		// resolve unit and re-pulling the whole backlog.
		transport, err := s.client.cfg.factory(s.currentTarget())
		if err != nil {
			s.client.emitState(s.ctx, youtube.ConnectionState{
				Status: youtube.ConnectionFailed,
				ChatID: s.routingKey,
				Detail: credentialSafeDetail(err),
				Err:    err,
				At:     time.Now(),
			})
			if !s.wait(&attempts, &delay) {
				return
			}
			continue
		}

		switch s.publishTransport(transport) {
		case publishStopped:
			_ = transport.Close()
			return
		case publishSuperseded:
			_ = transport.Close()
			continue
		}
		if err := transport.Start(s.ctx); err != nil {
			s.setTransport(nil)
			_ = transport.Close()
			s.client.emitState(s.ctx, youtube.ConnectionState{
				Status: youtube.ConnectionFailed,
				ChatID: s.routingKey,
				Detail: credentialSafeDetail(err),
				Err:    err,
				At:     time.Now(),
			})
			if !s.wait(&attempts, &delay) {
				return
			}
			continue
		}

		startedAt := time.Now()
		s.forward(transport)
		s.adoptTarget(transport.Target())
		s.setTransport(nil)
		_ = transport.Close()

		if s.ctx.Err() != nil {
			return
		}
		// A session that survived long enough counts as healthy, so a later
		// blip starts at the bottom of the ladder instead of inheriting an
		// hour-old backoff.
		s.mu.Lock()
		explicit := s.explicitRestart
		s.explicitRestart = false
		s.mu.Unlock()
		if explicit || time.Since(startedAt) >= reconnectResetAfter {
			attempts = 0
			delay = reconnectInitialDelay
		}
		if explicit {
			// A reconnect the user asked for happens now. Making them wait
			// out a backoff they did not cause is the opposite of what the
			// key is for.
			continue
		}
		if !s.wait(&attempts, &delay) {
			return
		}
	}
}

// wait advances the ladder, reporting whether another attempt should be made.
func (s *liveChatSession) wait(attempts *int, delay *time.Duration) bool {
	*attempts++
	if *attempts >= reconnectAttemptLimit {
		s.client.emitState(s.ctx, youtube.ConnectionState{
			Status: youtube.ConnectionFailed,
			ChatID: s.routingKey,
			Detail: "reconnect attempts exhausted; press ctrl+r to retry",
			At:     time.Now(),
		})
		s.client.cfg.logger.Log(s.ctx, "app.live_chat.reconnect_exhausted",
			slog.Int("attempts", *attempts),
		)
		return false
	}
	s.client.emitState(s.ctx, youtube.ConnectionState{
		Status: youtube.ConnectionReconnecting,
		ChatID: s.routingKey,
		Detail: "reconnecting in " + delay.Round(time.Second).String(),
		At:     time.Now(),
	})

	timer := time.NewTimer(*delay)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return false
	case <-timer.C:
	}
	if next := *delay * 2; next < reconnectMaxDelay {
		*delay = next
	} else {
		*delay = reconnectMaxDelay
	}
	return true
}

// forward fans one transport's five streams into the merged streams and returns
// once every one of them has closed, which is how a transport reports that it
// is finished.
func (s *liveChatSession) forward(transport LiveChatTransport) {
	var wg sync.WaitGroup

	if messages := transport.Messages(); messages != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for message := range messages {
				message.LiveChatID = s.routingKey
				s.client.emitMessage(message)
			}
		}()
	}
	if states := transport.ConnectionStates(); states != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for state := range states {
				state.ChatID = s.routingKey
				s.client.emitState(s.ctx, state)
			}
		}()
	}
	if moderations := transport.Moderations(); moderations != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for event := range moderations {
				event.LiveChatID = s.routingKey
				s.client.emitModeration(s.ctx, event)
			}
		}()
	}
	if rooms := transport.RoomEvents(); rooms != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for event := range rooms {
				event.LiveChatID = s.routingKey
				s.client.emitRoomEvent(s.ctx, event)
			}
		}()
	}
	if polls := transport.Polls(); polls != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for poll := range polls {
				poll.LiveChatID = s.routingKey
				s.client.emitPoll(s.ctx, poll)
			}
		}()
	}
	wg.Wait()
}

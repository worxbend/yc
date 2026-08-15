package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/youtube"
)

// ErrReconnectUnavailable is returned when the chat source cannot be restarted
// in place. The key stays bound and reports why rather than doing nothing.
var ErrReconnectUnavailable = errors.New("reconnect unavailable for this chat source")

// ErrReconnectInProgress is returned when a reconnect is already running, so a
// second ctrl+r cannot stack transports on top of each other.
var ErrReconnectInProgress = errors.New("reconnect already in progress")

// reconnectingChatClient is the optional capability behind ctrl+r.
type reconnectingChatClient interface {
	Reconnect(ctx context.Context) error
}

// --- transport messages ----------------------------------------------------
//
// Every inbound stream message carries ok, so a closed channel is a first-class
// state the update loop handles rather than a panic on a nil receive.

type chatClientMessageMsg struct {
	message youtube.Message
	ok      bool
}

type chatClientConnectionStateMsg struct {
	state youtube.ConnectionState
	ok    bool
}

type chatClientModerationMsg struct {
	event youtube.ModerationEvent
	ok    bool
}

type chatClientRoomEventMsg struct {
	event youtube.RoomEvent
	ok    bool
}

type chatClientPollMsg struct {
	poll youtube.PollState
	ok   bool
}

type composerSendCompletedMsg struct {
	id     int
	result youtube.SendResult
	err    error
}

type reconnectCompletedMsg struct {
	chatKey string
	err     error
}

type identityResolvedMsg struct {
	identity youtube.Identity
	err      error
}

type targetResolvedMsg struct {
	chatKey string
	target  youtube.ChatTarget
	err     error
}

type chatMetricsMsg struct {
	chatKey   string
	broadcast youtube.BroadcastInfo
	err       error
}

type chatMetricsTickMsg struct{}

type quotaTickMsg struct{}

type revealTickMsg struct{}

// --- stream commands -------------------------------------------------------
//
// One blocking receive per command, re-armed on every delivery. This is the
// only shape that keeps Update free of goroutines it has to reason about: the
// runtime owns the receive, and the model only ever sees a typed message.

func (m shellModel) nextClientMessageCommand() tea.Cmd {
	if m.client == nil {
		return nil
	}
	messages := m.client.Messages()
	if messages == nil {
		return nil
	}
	return func() tea.Msg {
		message, ok := <-messages
		return chatClientMessageMsg{message: message, ok: ok}
	}
}

func (m shellModel) nextConnectionStateCommand() tea.Cmd {
	if m.client == nil {
		return nil
	}
	states := m.client.ConnectionStates()
	if states == nil {
		return nil
	}
	return func() tea.Msg {
		state, ok := <-states
		return chatClientConnectionStateMsg{state: state, ok: ok}
	}
}

func (m shellModel) nextClientModerationCommand() tea.Cmd {
	source, ok := m.client.(ModerationSource)
	if !ok {
		return nil
	}
	events := source.Moderations()
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-events
		return chatClientModerationMsg{event: event, ok: ok}
	}
}

func (m shellModel) nextClientRoomEventCommand() tea.Cmd {
	source, ok := m.client.(RoomEventSource)
	if !ok {
		return nil
	}
	events := source.RoomEvents()
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-events
		return chatClientRoomEventMsg{event: event, ok: ok}
	}
}

func (m shellModel) nextClientPollCommand() tea.Cmd {
	source, ok := m.client.(PollSource)
	if !ok {
		return nil
	}
	polls := source.Polls()
	if polls == nil {
		return nil
	}
	return func() tea.Msg {
		poll, ok := <-polls
		return chatClientPollMsg{poll: poll, ok: ok}
	}
}

// --- async collaborator commands -------------------------------------------

// resolveIdentityCommand asks who the authenticated user is. The local echo
// has no other source for the sender's display name and badges, and the mention
// filter has none for the handle to match.
func (m shellModel) resolveIdentityCommand() tea.Cmd {
	if m.identityLookup == nil || m.identityKnown {
		return nil
	}
	lookup := m.identityLookup
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		identity, err := lookup.Identity(ctx)
		return identityResolvedMsg{identity: identity, err: err}
	}
}

// resolveTargetsCommand resolves every open chat that still lacks a
// liveChatId. Resolution is quota-charged, so it runs once per target rather
// than on a timer.
func (m shellModel) resolveTargetsCommand() tea.Cmd {
	if m.broadcastResolver == nil || m.chats == nil {
		return nil
	}
	resolver := m.broadcastResolver
	cmds := make([]tea.Cmd, 0, len(m.chats.order))
	for _, key := range m.chats.chatKeys() {
		state := m.chats.ensureKey(key)
		if state == nil || state.target.Resolved() {
			continue
		}
		chatKey := key
		target := state.target
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resolved, err := resolver.ResolveTarget(ctx, target)
			return targetResolvedMsg{chatKey: chatKey, target: resolved, err: err}
		})
	}
	return batchNonNil(cmds...)
}

// refreshChatMetricsCommand refreshes viewer counts and live state for every
// resolved chat. It is deliberately infrequent: each refresh is a charged call.
func (m shellModel) refreshChatMetricsCommand() tea.Cmd {
	if m.broadcastResolver == nil || m.chats == nil {
		return nil
	}
	if m.effectiveConfig.Features.StreamStatusMode == "off" {
		return nil
	}
	resolver := m.broadcastResolver
	cmds := make([]tea.Cmd, 0, len(m.chats.order))
	for _, key := range m.chats.chatKeys() {
		state := m.chats.ensureKey(key)
		if state == nil || strings.TrimSpace(state.target.VideoID) == "" || state.ended {
			continue
		}
		chatKey := key
		videoID := state.target.VideoID
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			info, err := resolver.Broadcast(ctx, videoID)
			return chatMetricsMsg{chatKey: chatKey, broadcast: info, err: err}
		})
	}
	return batchNonNil(cmds...)
}

func (m *shellModel) scheduleChatMetricsTick() tea.Cmd {
	if m.metricsTickScheduled || m.broadcastResolver == nil {
		return nil
	}
	m.metricsTickScheduled = true
	return tea.Tick(chatMetricsInterval, func(time.Time) tea.Msg { return chatMetricsTickMsg{} })
}

// scheduleQuotaTick mirrors the transport's estimated ledger onto the model.
// Reading the ledger is a local accessor and costs nothing; it is done from a
// command rather than from View because View must stay pure.
func (m *shellModel) scheduleQuotaTick() tea.Cmd {
	if m.quotaTickScheduled {
		return nil
	}
	if _, ok := m.client.(QuotaReporter); !ok {
		if _, ok := m.client.(PollIntervalSource); !ok {
			return nil
		}
	}
	m.quotaTickScheduled = true
	return tea.Tick(quotaPollInterval, func(time.Time) tea.Msg { return quotaTickMsg{} })
}

// refreshQuota reads the optional quota and cadence capabilities. Both are
// estimates by construction and are labeled as such wherever they are drawn.
func (m *shellModel) refreshQuota() {
	if reporter, ok := m.client.(QuotaReporter); ok {
		m.quota = reporter.Quota()
		m.quotaKnown = true
	}
	if source, ok := m.client.(PollIntervalSource); ok {
		m.pollInterval = source.PollInterval()
	}
}

// --- sending ---------------------------------------------------------------

// startNextComposerSend dispatches the head of the queue when nothing is in
// flight. Sends are serialized per chat so the queued order is the order
// YouTube sees, which is what a moderator issuing a sequence expects.
func (m *shellModel) startNextComposerSend(state *chatState) tea.Cmd {
	if state == nil || state.activeSend != nil || len(state.sendQueue) == 0 {
		return nil
	}
	if m.client == nil {
		state.sendState = composerSendFailed
		state.sendFeedback = "send unavailable for this chat source"
		state.sendQueue = nil
		return nil
	}
	next := state.sendQueue[0]
	state.sendQueue = state.sendQueue[1:]
	state.activeSend = &next
	state.sendState = composerSendSending
	state.sendFeedback = "sending"

	client := m.client
	request := youtube.SendRequest{
		LiveChatID: state.target.LiveChatID,
		Text:       next.Text,
	}
	if next.Reply != nil {
		request.ReplyToDisplayName = next.Reply.Author
	}
	id := next.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := client.Send(ctx, request)
		return composerSendCompletedMsg{id: id, result: result, err: err}
	}
}

// chatStateForActiveSend finds the chat whose in-flight send has this ID, so a
// completion that lands after the user switched chats still updates the right
// composer.
func (m shellModel) chatStateForActiveSend(id int) *chatState {
	if m.chats == nil {
		return nil
	}
	for _, key := range m.chats.order {
		state := m.chats.states[key]
		if state != nil && state.activeSend != nil && state.activeSend.ID == id {
			return state
		}
	}
	return nil
}

// appendLocalEcho prints the message yc just sent without waiting for it to
// come back around the poll loop, which can take the whole poll interval.
//
// The echo is marked so the authoritative copy replaces it rather than
// duplicating it, and it carries the resolved identity's own badges because
// YouTube never echoes a sender's own state.
func (m *shellModel) appendLocalEcho(sent queuedComposerSend, result youtube.SendResult, now time.Time) {
	state := m.chats.ensureKey(sent.ChatKey)
	if state == nil {
		return
	}
	id := strings.TrimSpace(result.MessageID)
	if id == "" {
		id = fmt.Sprintf("local-echo-%d", sent.ID)
	}
	author := youtube.Author{
		ChannelID:   m.identity.ChannelID,
		DisplayName: m.identity.DisplayName,
	}
	if strings.TrimSpace(author.DisplayName) == "" {
		author.DisplayName = "you"
	}
	timestamp := result.AcceptedAt
	if timestamp.IsZero() {
		timestamp = now
	}
	message := youtube.Message{
		ID:         id,
		LiveChatID: state.target.LiveChatID,
		Timestamp:  timestamp,
		Author:     author,
		Badges:     youtube.BadgesForAuthor(author),
		Text:       sent.Text,
		Kind:       youtube.EventKindText,
		Type:       youtube.MessageTypeChat,
		LocalEcho:  true,
	}
	state.localEchoes[id] = struct{}{}
	state.appendMessage(message)
	state.trimScrollback(m.chats.scrollbackLimit)
	state.observeAuthor(message)
}

// --- reconnect -------------------------------------------------------------

// requestReconnect restarts the transport for the active chat. It reports into
// the connection state rather than failing silently, because a key that appears
// to do nothing is worse than one that explains why it cannot.
func (m *shellModel) requestReconnect(now time.Time) tea.Cmd {
	state := m.activeChatState()
	chatKey := m.activeChatKey()
	chatID := state.target.LiveChatID

	if m.reconnectInFlight {
		m.applyConnectionState(youtube.ConnectionState{
			Status: youtube.ConnectionReconnecting,
			ChatID: chatID,
			Detail: "manual reconnect already in progress",
			At:     now,
		})
		return nil
	}
	client, ok := m.client.(reconnectingChatClient)
	if !ok {
		next := state.status
		next.ChatID = chatID
		if next.Status == "" {
			next.Status = youtube.ConnectionDisconnected
		}
		next.Detail = ErrReconnectUnavailable.Error()
		next.At = now
		m.applyConnectionState(next)
		return nil
	}

	m.reconnectInFlight = true
	m.applyConnectionState(youtube.ConnectionState{
		Status: youtube.ConnectionReconnecting,
		ChatID: chatID,
		Detail: "manual reconnect requested",
		At:     now,
	})
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return reconnectCompletedMsg{chatKey: chatKey, err: client.Reconnect(ctx)}
	}
}

func (m *shellModel) completeReconnect(msg reconnectCompletedMsg, now time.Time) {
	m.reconnectInFlight = false
	state := m.chats.ensureKey(msg.chatKey)
	if state == nil {
		state = m.activeChatState()
	}
	next := youtube.ConnectionState{ChatID: state.target.LiveChatID, At: now}
	switch {
	case msg.err == nil:
		next.Status = youtube.ConnectionConnecting
		next.Detail = "reconnecting"
	case errors.Is(msg.err, ErrReconnectUnavailable):
		next.Status = youtube.ConnectionDisconnected
		next.Detail = ErrReconnectUnavailable.Error()
		next.Err = msg.err
	default:
		next.Status = youtube.ConnectionFailed
		next.Detail = credentialSafeDetail(msg.err)
		next.Err = msg.err
	}
	m.applyConnectionState(next)
}

// applyConnectionState files a lifecycle transition and keeps the debug log in
// step, without ever recording the error's raw text.
func (m *shellModel) applyConnectionState(state youtube.ConnectionState) {
	if m.chats == nil {
		return
	}
	m.chats.applyConnectionState(state)
	m.debugLogger.Log(context.Background(), "app.connection_state",
		slog.String("status", string(state.Status)),
		slog.String("detail", m.debugLogger.Redact(summarizeDetail(state.Detail))),
	)
}

// credentialSafeDetail renders an error for the status line.
//
// Transport errors are already redacted at their own boundary; this is the
// second line of defense, so a message that somehow carries a token-shaped
// substring cannot reach a terminal that is often on stream.
func credentialSafeDetail(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *youtube.APIError
	if errors.As(err, &apiErr) {
		return summarizeDetail(apiErr.Error())
	}
	return summarizeDetail(debuglog.Logger{}.Redact(err.Error()))
}

// sendResultDetail is the composer's one-line report of a completed send.
func sendResultDetail(result youtube.SendResult) string {
	if detail := summarizeDetail(result.Detail); detail != "" {
		return detail
	}
	if result.RateLimited {
		if result.RetryAfter > 0 {
			return fmt.Sprintf("rate limited, retry in %s", result.RetryAfter.Round(time.Second))
		}
		return "rate limited"
	}
	return "sent"
}

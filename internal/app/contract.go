package app

import (
	"context"
	"time"

	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/youtube"
)

// MessageStream is the inbound half of the chat boundary.
//
// Both channels are owned by the client and closed by Close. Consumers must
// treat a closed channel as a first-class state rather than a panic: the shell
// re-arms one blocking receive per delivery, so a closed channel arrives as an
// ordinary message with ok=false.
type MessageStream interface {
	Messages() <-chan youtube.Message
	ConnectionStates() <-chan youtube.ConnectionState
}

// Sender is the outbound half of the chat boundary.
//
// Send must not block the caller for longer than ctx allows, and must return a
// redacted error: nothing derived from a token, an API key, or a bearer header
// may reach a SendResult.Detail or an error string.
type Sender interface {
	Send(ctx context.Context, request youtube.SendRequest) (youtube.SendResult, error)
}

// ChatClient is the app-facing boundary for chat input, connection state, and
// outbound sends. Implementations must emit normalized youtube models, never
// YouTube Data API JSON shapes.
//
// Everything a specific transport can additionally do is expressed as an
// optional interface asserted on the client rather than as a method here, so
// the mock source, the deterministic fake, and the live poller all satisfy the
// same required surface.
type ChatClient interface {
	MessageStream
	Sender
	// Close cancels the session context, stops polling, and closes the
	// streams. It must be safe to call more than once.
	Close() error
}

// ModerationSource is an optional ChatClient capability exposing moderation
// actions: deletions, tombstones, bans, and timeouts.
//
// It is deliberately not folded into the message stream. A moderation action is
// an instruction to remove text that is already on screen, so rendering it as
// another chat message puts the removed text back in front of the viewer - on a
// terminal that is frequently on stream. Consumers apply these to messages they
// already hold instead.
type ModerationSource interface {
	Moderations() <-chan youtube.ModerationEvent
}

// RoomEventSource is an optional ChatClient capability exposing chat-wide state
// changes: members-only mode, chat ended, and the broadcast going offline.
type RoomEventSource interface {
	RoomEvents() <-chan youtube.RoomEvent
}

// PollSource is an optional ChatClient capability exposing creator polls, fed
// by both pollEvent items and the list response's activePollItem.
type PollSource interface {
	Polls() <-chan youtube.PollState
}

// ChatJoiner is an optional ChatClient capability: a transport that can start
// and stop polling an additional live chat after connecting exposes it here, so
// a chat opened from the picker starts receiving messages without tearing down
// the existing sessions. Transports without it still work - the model simply
// tracks the chat locally.
type ChatJoiner interface {
	OpenChat(ctx context.Context, target youtube.ChatTarget) (youtube.ChatTarget, error)
	CloseChat(chatKey string) error
}

// Moderator is an optional ChatClient capability for outbound moderation. It
// requires the youtube.force-ssl scope and the authorized user being the owner
// or a moderator of the target chat; without either the moderation keys stay
// visible but inert, with an explicit reason, rather than disappearing.
//
// Every method must return a redacted error for the same reason Sender does:
// a refusal is rendered into a status bar that is frequently on stream.
type Moderator interface {
	DeleteMessage(ctx context.Context, messageID string) error
	Ban(ctx context.Context, request youtube.BanRequest) (youtube.BanResult, error)
	Unban(ctx context.Context, banID string) error
}

// ModerationCapability is an optional companion to Moderator, reporting whether
// a client that implements the interface can currently act on it.
//
// The two are separate because Go interfaces are satisfied by a type, not by an
// instance: a live adapter built without a moderating credential still has the
// methods, so asserting Moderator alone would report the capability as present
// and only discover otherwise after a destructive keystroke had been confirmed.
// The reason string is user-facing and carries no credential material.
type ModerationCapability interface {
	ModerationAvailable() (available bool, reason string)
}

// MessageDropCounter is an optional ChatClient capability reporting how many
// chat messages were discarded because the UI could not keep up.
//
// Transports drop rather than block, because blocking the emitter stalls the
// goroutine that owns the poll schedule and costs the whole session. That trade
// is only acceptable if the loss is visible, which is what this interface is
// for: the status bar shows "dropped=N" at every terminal width.
type MessageDropCounter interface {
	DroppedMessages() uint64
}

// QuotaReporter is an optional ChatClient capability exposing the estimated
// quota ledger and the cadence it implies.
//
// The snapshot is read from Update, never from View, and mirrored onto the
// model. Everything it reports is an estimate: Google publishes no quota costs
// for live chat methods, so every rendered figure carries an explicit marker.
type QuotaReporter interface {
	Quota() youtube.QuotaSnapshot
}

// PollIntervalSource is an optional ChatClient capability exposing the current
// effective poll interval when a transport tracks cadence without owning a full
// quota ledger.
type PollIntervalSource interface {
	PollInterval() time.Duration
}

// IdentityLookup resolves the authenticated user's own channel. yc renders its
// own sent messages from a local echo, and this is the only place that echo can
// learn the sender's display name and badges from.
//
// Implementations must not perform network work from View; the shell schedules
// the lookup once at startup through a command.
type IdentityLookup interface {
	Identity(ctx context.Context) (youtube.Identity, error)
}

// BroadcastResolver is the app-facing boundary for broadcast metadata: the
// chat title, the LIVE indicator, and the concurrent viewer count.
type BroadcastResolver interface {
	ResolveTarget(ctx context.Context, target youtube.ChatTarget) (youtube.ChatTarget, error)
	Broadcast(ctx context.Context, videoID string) (youtube.BroadcastInfo, error)
}

// SubscriptionLookup feeds the chat picker's autocomplete from the user's own
// subscriptions. Failure is inline text in the picker, not an error that stops
// the UI.
type SubscriptionLookup interface {
	Subscriptions(ctx context.Context) ([]youtube.Subscription, error)
}

// StreamInfoManager backs the Stream Info tab. It is nil-able: without the
// required scope the tab renders read-only with an explicit reason.
type StreamInfoManager interface {
	StreamInfo(ctx context.Context, videoID string) (youtube.StreamInfo, error)
	UpdateStreamInfo(ctx context.Context, info youtube.StreamInfo) (youtube.StreamInfo, error)
}

// CategoryLookup feeds the Stream Info tab's category picker.
type CategoryLookup interface {
	Categories(ctx context.Context) ([]youtube.Category, error)
}

// SystemNotifier delivers focus-aware notifications for non-chat events such as
// Super Chats, new members, and a chat ending.
type SystemNotifier interface {
	Notify(ctx context.Context, notification SystemNotification) error
}

// SystemNotification is the app-level notification payload for a normalized
// YouTube event. It carries only display-safe text.
type SystemNotification struct {
	Title   string
	Body    string
	ChatKey string
	EventID string
}

// ChatLogger persists normalized chat events to the user's opt-in chat log.
//
// It is declared here rather than importing internal/chatlog because the app
// only needs "something that appends a message": the concrete writer, its
// rotation policy, and its file format all stay behind this boundary, and
// tests drive the shell with an in-memory fake.
//
// Append errors must be safe to render: the shell reports the first failure in
// the status line and disables logging for the rest of the session, so an
// error string reaches a terminal that is frequently on stream.
type ChatLogger interface {
	Append(message youtube.Message) error
}

// ClientOptions carries the optional collaborators the shell can run without.
//
// Every field is nil-able on purpose: `yc chat --mock` and credential-free runs
// must drive the full UI with simulated or absent data rather than failing, so
// each consumer degrades to a stable fallback when its collaborator is nil.
type ClientOptions struct {
	SystemNotifier     SystemNotifier
	DebugLogger        debuglog.Logger
	IdentityLookup     IdentityLookup
	BroadcastResolver  BroadcastResolver
	SubscriptionLookup SubscriptionLookup
	StreamInfoManager  StreamInfoManager
	CategoryLookup     CategoryLookup
	ChatLogger         ChatLogger
}

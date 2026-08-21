package youtube

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// MaxChatMessageRunes is the YouTube live chat message length cap. The
// composer enforces it locally so an over-long draft is refused before it
// costs quota on a request the API will reject.
const MaxChatMessageRunes = 200

// EventKind is the normalized form of a YouTube liveChatMessage
// snippet.type. Every documented value has an entry, including the ones the
// API no longer delivers, so an unexpected payload is classified rather than
// dropped.
//
// EventKindUnknown is not an error path: an unrecognized snippet.type is
// carried through with Message.RawType intact and rendered from
// snippet.displayMessage.
type EventKind string

const (
	// EventKindUnknown marks a raw type yc does not recognize; the raw
	// type string is preserved on the message.
	EventKindUnknown EventKind = "unknown"
	// EventKindText is an ordinary chat message (textMessageEvent).
	EventKindText EventKind = "text"
	// EventKindSuperChat is a paid highlighted message (superChatEvent).
	EventKindSuperChat EventKind = "super_chat"
	// EventKindSuperSticker is a paid sticker (superStickerEvent).
	EventKindSuperSticker EventKind = "super_sticker"
	// EventKindNewSponsor is a new or upgraded membership (newSponsorEvent).
	EventKindNewSponsor EventKind = "new_sponsor"
	// EventKindMemberMilestone is a membership anniversary message
	// (memberMilestoneChatEvent).
	EventKindMemberMilestone EventKind = "member_milestone"
	// EventKindMembershipGifting is a gifted-membership purchase
	// (membershipGiftingEvent).
	EventKindMembershipGifting EventKind = "membership_gifting"
	// EventKindGiftMembershipReceived is one recipient of a gifting burst
	// (giftMembershipReceivedEvent). These arrive in floods and must be
	// coalesced by the activity column.
	EventKindGiftMembershipReceived EventKind = "gift_membership_received"
	// EventKindGift is a paid virtual gift (giftEvent, added 2026-03-26).
	EventKindGift EventKind = "gift"
	// EventKindPoll carries poll metadata (pollEvent). The live poll also
	// arrives out of band as the list response's activePollItem.
	EventKindPoll EventKind = "poll"
	// EventKindFanFunding is the deprecated pre-Super-Chat tip event
	// (fanFundingEvent, deprecated 2017). Decoded defensively only.
	EventKindFanFunding EventKind = "fan_funding"
	// EventKindMessageDeleted is messageDeletedEvent. YouTube removed this
	// from the reference on 2026-06-23 as "not being returned by the API";
	// it is decoded opportunistically but deletion UX must key off
	// EventKindTombstone plus the local echo of a successful delete call.
	EventKindMessageDeleted EventKind = "message_deleted"
	// EventKindMessageRetracted is messageRetractedEvent. Same non-delivery
	// caveat as EventKindMessageDeleted.
	EventKindMessageRetracted EventKind = "message_retracted"
	// EventKindUserBanned is a ban or timeout (userBannedEvent).
	EventKindUserBanned EventKind = "user_banned"
	// EventKindSponsorOnlyModeStarted is sponsorOnlyModeStartedEvent.
	EventKindSponsorOnlyModeStarted EventKind = "sponsor_only_mode_started"
	// EventKindSponsorOnlyModeEnded is sponsorOnlyModeEndedEvent.
	EventKindSponsorOnlyModeEnded EventKind = "sponsor_only_mode_ended"
	// EventKindChatEnded is chatEndedEvent. It carries no displayMessage and
	// is a terminal condition, not an error.
	EventKindChatEnded EventKind = "chat_ended"
	// EventKindTombstone is a previously delivered message reappearing with
	// hasDisplayContent false. It is how deletions actually surface.
	EventKindTombstone EventKind = "tombstone"
	// EventKindInvalidType is the API's own invalidType enum member.
	EventKindInvalidType EventKind = "invalid_type"
)

// snippetTypes maps every documented snippet.type onto its normalized kind.
var snippetTypes = map[string]EventKind{
	"textMessageEvent":            EventKindText,
	"superChatEvent":              EventKindSuperChat,
	"superStickerEvent":           EventKindSuperSticker,
	"newSponsorEvent":             EventKindNewSponsor,
	"memberMilestoneChatEvent":    EventKindMemberMilestone,
	"membershipGiftingEvent":      EventKindMembershipGifting,
	"giftMembershipReceivedEvent": EventKindGiftMembershipReceived,
	"giftEvent":                   EventKindGift,
	"pollEvent":                   EventKindPoll,
	"fanFundingEvent":             EventKindFanFunding,
	"messageDeletedEvent":         EventKindMessageDeleted,
	"messageRetractedEvent":       EventKindMessageRetracted,
	"userBannedEvent":             EventKindUserBanned,
	"sponsorOnlyModeStartedEvent": EventKindSponsorOnlyModeStarted,
	"sponsorOnlyModeEndedEvent":   EventKindSponsorOnlyModeEnded,
	"chatEndedEvent":              EventKindChatEnded,
	"tombstone":                   EventKindTombstone,
	"invalidType":                 EventKindInvalidType,
}

// EventKindFromSnippetType normalizes a raw snippet.type. Unknown values map
// to EventKindUnknown so a new YouTube event type degrades into a readable row
// instead of a crash or a silent drop.
func EventKindFromSnippetType(snippetType string) EventKind {
	if kind, ok := snippetTypes[strings.TrimSpace(snippetType)]; ok {
		return kind
	}
	return EventKindUnknown
}

// MessageType is the coarse render class for a message. EventKind carries the
// precise event; MessageType is what the renderer and the local filters switch
// on so a newly added EventKind does not require touching every layout.
type MessageType string

const (
	// MessageTypeChat is an ordinary chatter message.
	MessageTypeChat MessageType = "chat"
	// MessageTypePaid is a Super Chat, Super Sticker, gift, or fan funding.
	MessageTypePaid MessageType = "paid"
	// MessageTypeMembership is any membership join, upgrade, milestone,
	// gifting, or gift-received event.
	MessageTypeMembership MessageType = "membership"
	// MessageTypeNotice is a room-state announcement rendered with a
	// "[notice]" prefix, such as members-only mode changing.
	MessageTypeNotice MessageType = "notice"
	// MessageTypeSystem is a locally generated informational row.
	MessageTypeSystem MessageType = "system"
	// MessageTypeUnknown is an unrecognized snippet.type carried through
	// with its displayMessage and RawType.
	MessageTypeUnknown MessageType = "unknown"
)

// MessageTypeForKind maps a normalized event kind onto its render class.
func MessageTypeForKind(kind EventKind) MessageType {
	switch kind {
	case EventKindText:
		return MessageTypeChat
	case EventKindSuperChat, EventKindSuperSticker, EventKindGift, EventKindFanFunding:
		return MessageTypePaid
	case EventKindNewSponsor, EventKindMemberMilestone, EventKindMembershipGifting, EventKindGiftMembershipReceived:
		return MessageTypeMembership
	case EventKindSponsorOnlyModeStarted, EventKindSponsorOnlyModeEnded, EventKindChatEnded, EventKindPoll:
		return MessageTypeNotice
	default:
		return MessageTypeUnknown
	}
}

// Author is the normalized authorDetails block.
//
// YouTube supplies no author color and no badge imagery for live chat, so
// there are deliberately no color or asset-URL fields here: internal/render
// derives a stable per-author color by hashing ChannelID. AvatarURL is
// retained for redacted diagnostics only and is never fetched.
type Author struct {
	ChannelID   string
	DisplayName string
	ChannelURL  string
	AvatarURL   string

	IsOwner     bool
	IsModerator bool
	IsMember    bool
	IsVerified  bool

	// MemberLevelName and MemberMonths are best-effort tenure learned from
	// membership events, not from authorDetails. Zero means unknown, never
	// "not a member".
	MemberLevelName string
	MemberMonths    int
}

// Identity returns the stable key used for grouping, roster entries, and
// deterministic color selection. ChannelID is preferred because display names
// are user-controlled and not unique.
func (a Author) Identity() string {
	if id := strings.TrimSpace(a.ChannelID); id != "" {
		return id
	}
	return strings.TrimSpace(a.DisplayName)
}

// BadgeKind enumerates the four badges YouTube live chat actually exposes.
// Every Twitch-shaped badge set (vip, founder, staff, turbo, bits, ...) is
// deliberately absent because this API has no equivalent.
type BadgeKind string

// The four author roles the API can attest.
const (
	BadgeOwner     BadgeKind = "owner"
	BadgeModerator BadgeKind = "moderator"
	BadgeMember    BadgeKind = "member"
	BadgeVerified  BadgeKind = "verified"
)

// Badge is a rendered author role marker. It carries no image reference
// because the API supplies none; internal/render maps Kind onto a width-1
// Unicode glyph or a compact text label.
type Badge struct {
	Kind BadgeKind
	// Label is the text-mode label ("mod", "member", ...).
	Label string
	// Info is optional supplementary detail such as a membership level name.
	Info string
}

// BadgesForAuthor derives the badge set from authorDetails booleans in a
// stable order: owner, moderator, member, verified.
func BadgesForAuthor(author Author) []Badge {
	var badges []Badge
	if author.IsOwner {
		badges = append(badges, Badge{Kind: BadgeOwner, Label: "owner"})
	}
	if author.IsModerator {
		badges = append(badges, Badge{Kind: BadgeModerator, Label: "mod"})
	}
	if author.IsMember {
		badges = append(badges, Badge{Kind: BadgeMember, Label: "member", Info: author.MemberLevelName})
	}
	if author.IsVerified {
		badges = append(badges, Badge{Kind: BadgeVerified, Label: "verified"})
	}
	return badges
}

// FragmentType is the semantic role of a span of message text.
//
// YouTube returns plain text with no emote ranges, so every fragment here is
// produced by yc's own scan of displayMessage rather than by transport
// metadata.
type FragmentType string

const (
	// FragmentText is a plain text run with no special role.
	FragmentText FragmentType = "text"
	// FragmentMention is an "@name" token that starts a word.
	FragmentMention FragmentType = "mention"
	// FragmentEmoji is a Unicode emoji grapheme cluster.
	FragmentEmoji FragmentType = "emoji"
	// FragmentShortcode is a ":name:" or ":_name:" channel-emoji literal.
	// The API never resolves these to an image, so they render as a
	// fixed-width token chip.
	FragmentShortcode FragmentType = "shortcode"
	// FragmentURL is a detected link.
	FragmentURL FragmentType = "url"
)

// MessageFragment is one normalized span of a message body.
type MessageFragment struct {
	Type FragmentType
	Text string
}

// Money is a currency amount. It is parsed from the API's uint64-as-string
// amountMicros and is never held as a float, so no rounding can occur between
// the wire and the screen.
type Money struct {
	// Micros is the amount in millionths of the currency unit.
	Micros int64
	// Currency is the ISO 4217 code.
	Currency string
	// Display is YouTube's own pre-localized amountDisplayString. Prefer it
	// for rendering; the numeric fields exist for sorting and thresholds.
	Display string
}

// Zero reports whether no amount is present.
func (m Money) Zero() bool {
	return m.Micros == 0 && strings.TrimSpace(m.Display) == ""
}

// SuperChatDetails is a paid highlighted message (superChatEvent).
type SuperChatDetails struct {
	Amount Money
	// Tier is YouTube's 1-11 purchase tier, or 0 when absent. It drives the
	// amount-chip color, mapped onto palette roles rather than hex.
	Tier int
	// Comment is the buyer's optional message (userComment).
	Comment string
}

// SuperStickerDetails is a paid sticker (superStickerEvent). Unlike a Super
// Chat there is no user comment field.
type SuperStickerDetails struct {
	Amount    Money
	Tier      int
	StickerID string
	// AltText is the sticker's accessible description; it is the only
	// renderable representation, since yc never fetches images.
	AltText string
	// Language is the alt-text language. The discovery document calls this
	// altTextLanguage and the HTML reference calls it language; the
	// normalizer accepts either.
	Language string
}

// GiftDetails is a paid virtual gift (giftEvent).
//
// The two official sources disagree on the wire shape - discovery documents a
// flat snippet.giftDetails while the HTML reference documents
// snippet.giftEventDetails.giftMetadata - so the normalizer decodes both into
// this one struct.
type GiftDetails struct {
	Name            string
	URL             string
	Duration        time.Duration
	AltText         string
	Language        string
	Jewels          int
	ComboCount      int
	HasVisualEffect bool
}

// MembershipKind distinguishes the five membership events.
type MembershipKind string

// The five membership event shapes the API reports.
const (
	MembershipNew          MembershipKind = "new"
	MembershipUpgrade      MembershipKind = "upgrade"
	MembershipMilestone    MembershipKind = "milestone"
	MembershipGifting      MembershipKind = "gifting"
	MembershipGiftReceived MembershipKind = "gift_received"
	MembershipKindUnknown  MembershipKind = "unknown"
)

// MembershipDetails carries the union of the membership event payloads.
// Unset fields mean "not supplied by this event", never a zero fact.
type MembershipDetails struct {
	Kind      MembershipKind
	LevelName string
	// Months is memberMonth from a milestone event.
	Months int
	// GiftCount is giftMembershipsCount from a gifting event.
	GiftCount int
	// Comment is the member's optional message on a milestone event.
	Comment string
	// GifterChannelID and AssociatedGiftMessageID correlate a received gift
	// back to the gifting burst that produced it.
	GifterChannelID         string
	AssociatedGiftMessageID string
}

// Message is a normalized live chat row.
//
// Every consumer outside internal/youtube sees this type, never YouTube JSON.
// The optional detail pointers are nil unless the event carried that payload,
// so a nil pointer means "this event has no such data" rather than an empty
// value that could be mistaken for a real zero.
type Message struct {
	ID         string
	LiveChatID string
	Timestamp  time.Time
	Author     Author
	Badges     []Badge

	// Text is the plain fallback body (snippet.displayMessage, or the
	// type-specific text yc composes for non-chat events).
	Text string
	// Fragments is Text split into semantic spans by internal/youtube, since
	// the API supplies no fragment metadata.
	Fragments []MessageFragment

	Kind EventKind
	Type MessageType

	SuperChat    *SuperChatDetails
	SuperSticker *SuperStickerDetails
	Gift         *GiftDetails
	Membership   *MembershipDetails

	// Historical marks a row from the priming page (the first list call, sent
	// without a pageToken). The app appends these statically so a 2000-row
	// backlog is neither animated nor notified.
	Historical bool
	// Deleted marks a message the viewer already saw that later came back as
	// a tombstone, or that yc itself successfully deleted. The renderer
	// replaces the body wholesale; the original text is never reprinted.
	Deleted bool
	// LocalEcho marks a row yc rendered from its own successful send rather
	// than from a poll response. YouTube does echo sent messages back, so an
	// echo must be reconciled by ID rather than duplicated.
	LocalEcho bool
	// RawType is the original snippet.type, retained for redacted
	// diagnostics only. Rendering must switch on Kind or Type instead.
	RawType string
}

// Amount returns the monetary value attached to a message, if any, together
// with whether one was present. It folds the two currency-bearing shapes -
// Super Chats (which also cover the deprecated fanFundingEvent) and Super
// Stickers - so callers do not have to branch on which pointer is set. Gifts
// are deliberately absent: they carry Jewels, a platform currency with no
// monetary amount, so there is no Money to return for them.
func (m Message) Amount() (Money, bool) {
	switch {
	case m.SuperChat != nil && !m.SuperChat.Amount.Zero():
		return m.SuperChat.Amount, true
	case m.SuperSticker != nil && !m.SuperSticker.Amount.Zero():
		return m.SuperSticker.Amount, true
	default:
		return Money{}, false
	}
}

// Tier returns the paid purchase tier, or 0 when the message is not a tiered
// purchase.
func (m Message) Tier() int {
	switch {
	case m.SuperChat != nil:
		return m.SuperChat.Tier
	case m.SuperSticker != nil:
		return m.SuperSticker.Tier
	default:
		return 0
	}
}

// ModerationType enumerates the actions that remove content already on screen.
type ModerationType string

const (
	// ModerationMessageDeleted is a single message removal, learned from a
	// tombstone or from yc's own successful delete call.
	ModerationMessageDeleted ModerationType = "message_deleted"
	// ModerationUserBanned is a permanent ban.
	ModerationUserBanned ModerationType = "user_banned"
	// ModerationUserTimedOut is a temporary ban with a duration.
	ModerationUserTimedOut ModerationType = "user_timed_out"
	// ModerationTombstone is a raw tombstone whose original message yc never
	// saw, so there is nothing on screen to redact.
	ModerationTombstone ModerationType = "tombstone"
)

// ModerationEvent instructs consumers to remove content they already hold.
//
// It is deliberately not part of the message stream: a moderation action means
// the words should leave the screen, and re-emitting them as a chat row would
// put them back in front of a viewer on a terminal that is often on stream.
type ModerationEvent struct {
	Type       ModerationType
	LiveChatID string
	// TargetMessageID is set for deletions and tombstones.
	TargetMessageID string
	// TargetChannelID and TargetDisplayName are set for bans and timeouts.
	TargetChannelID   string
	TargetDisplayName string
	// Duration is the timeout length; zero means permanent.
	Duration time.Duration
	At       time.Time
}

// RoomEventType enumerates chat-wide state changes.
type RoomEventType string

// The chat-wide state changes yc reports as room events.
const (
	// RoomSponsorOnlyStarted means chat became members-only.
	RoomSponsorOnlyStarted RoomEventType = "sponsor_only_started"
	RoomSponsorOnlyEnded   RoomEventType = "sponsor_only_ended"
	RoomChatEnded          RoomEventType = "chat_ended"
	// RoomStreamOffline is derived from the list response's offlineAt.
	// Chat outlives the broadcast by a short window, so this is not itself a
	// reason to stop polling.
	RoomStreamOffline RoomEventType = "stream_offline"
)

// RoomEvent is a chat-wide state change.
type RoomEvent struct {
	Type       RoomEventType
	LiveChatID string
	Detail     string
	At         time.Time
}

// PollStatus is the lifecycle of a creator poll.
type PollStatus string

// The poll lifecycle stages, in order.
const (
	PollStatusUnknown PollStatus = "unknown"
	PollStatusActive  PollStatus = "active"
	PollStatusClosed  PollStatus = "closed"
)

// PollOption is one choice in a creator poll. Tally arrives as a JSON string
// and is decoded into int64 here.
type PollOption struct {
	Text  string
	Tally int64
}

// PollState is a creator poll, fed both by pollEvent items and by the list
// response's activePollItem.
//
// "Poll" here means the YouTube chat poll feature; it is unrelated to yc's HTTP
// poll loop, which lives in poll.go.
type PollState struct {
	MessageID  string
	LiveChatID string
	Question   string
	Options    []PollOption
	Status     PollStatus
	At         time.Time
}

// TotalVotes sums the option tallies.
func (p PollState) TotalVotes() int64 {
	var total int64
	for _, option := range p.Options {
		total += option.Tally
	}
	return total
}

// ConnectionStatus is the poll session lifecycle as the UI sees it.
type ConnectionStatus string

// The connection lifecycle stages the status bar can show.
const (
	ConnectionConnecting   ConnectionStatus = "connecting"
	ConnectionConnected    ConnectionStatus = "connected"
	ConnectionReconnecting ConnectionStatus = "reconnecting"
	ConnectionDisconnected ConnectionStatus = "disconnected"
	// ConnectionClosed is a clean stop, including a chat that ended. It is
	// not an error and the history stays readable.
	ConnectionClosed ConnectionStatus = "closed"
	// ConnectionFailed is a terminal error the user must act on.
	ConnectionFailed ConnectionStatus = "failed"
	// ConnectionPaused means polling stopped to protect quota. Sends still
	// work; ctrl+r is the manual override.
	ConnectionPaused ConnectionStatus = "paused"
)

// ConnectionState is one transition of a chat's poll session.
type ConnectionState struct {
	Status ConnectionStatus
	// ChatID is the liveChatId this state refers to, or empty for a
	// client-wide state.
	ChatID string
	// Detail is a short, already-redacted, user-facing explanation.
	Detail string
	Err    error
	At     time.Time
}

// SendRequest is an outbound chat message.
type SendRequest struct {
	LiveChatID string
	Text       string
	// ReplyToDisplayName makes the send read as a reply. YouTube live chat
	// has no parent-message field, so the transport prepends "@Name " to the
	// text instead of threading. Callers must not present this as a threaded
	// reply.
	ReplyToDisplayName string
}

// SendResult reports the outcome of an accepted send.
type SendResult struct {
	MessageID  string
	AcceptedAt time.Time
	// Detail is a short, already-redacted, user-facing note.
	Detail string
	// RateLimited marks a send declined by the local limiter or by a 429,
	// with RetryAfter set when known.
	RateLimited bool
	RetryAfter  time.Duration
	// QuotaUnits is the estimated cost charged to the ledger for this send.
	QuotaUnits int
}

// TargetKind classifies how a user named a chat.
type TargetKind string

const (
	// TargetUnknown is an input no classifier recognized.
	//
	// It is the empty string so that it is also TargetKind's zero value. When
	// it was "unknown" there were two sentinels for one fact - a ChatTarget
	// built as a struct literal was unclassified but not TargetUnknown - and
	// callers had to test for both. mergeTarget in resolve.go tested only for
	// TargetUnknown, so a zero-valued Kind passed its guard and could
	// overwrite a Kind that had already been resolved.
	TargetUnknown TargetKind = ""
	// TargetVideoID is a raw 11-character video ID or a watch/live/shorts URL.
	TargetVideoID TargetKind = "video_id"
	// TargetLiveChatID is an explicit --live-chat-id, which skips resolution
	// entirely and costs no quota.
	TargetLiveChatID TargetKind = "live_chat_id"
	// TargetChannelID is a UC... channel ID.
	TargetChannelID TargetKind = "channel_id"
	// TargetHandle is an @handle.
	TargetHandle TargetKind = "handle"
)

// ChatTarget is a chat the user asked for, at whatever stage of resolution it
// has reached. The same value is progressively filled in by the resolver, so a
// partially resolved target is a normal state rather than an error.
type ChatTarget struct {
	// Raw is exactly what the user typed, kept for display and diagnostics.
	Raw  string
	Kind TargetKind

	VideoID    string
	ChannelID  string
	Handle     string
	LiveChatID string

	// Title is the broadcast title once known. The UI labels chats by title,
	// never by ID.
	Title string
}

// Resolved reports whether the target can be polled without further lookups.
func (t ChatTarget) Resolved() bool {
	return strings.TrimSpace(t.LiveChatID) != ""
}

// Key is the stable map key for a chat in per-chat UI state. It prefers the
// immutable liveChatId and degrades through video ID, channel ID, handle, and
// finally the raw input so an unresolved target still has a distinct slot.
func (t ChatTarget) Key() string {
	for _, candidate := range []string{t.LiveChatID, t.VideoID, t.ChannelID, t.Handle, t.Raw} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return strings.ToLower(trimmed)
		}
	}
	return ""
}

// Label is the human-facing name for a chat: the broadcast title when known,
// otherwise whatever the user typed.
func (t ChatTarget) Label() string {
	if title := strings.TrimSpace(t.Title); title != "" {
		return title
	}
	return strings.TrimSpace(t.Raw)
}

// ParseChatTarget classifies a user-supplied target without performing any
// network work. It accepts a raw video ID, a watch?v= / youtu.be / /live/ /
// /shorts/ URL, an @handle, or a UC... channel ID.
//
// It never resolves a liveChatId; that requires the quota-charged ladder in
// resolve.go.
func ParseChatTarget(raw string) (ChatTarget, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ChatTarget{}, errors.New("no chat target given")
	}
	target := ChatTarget{Raw: trimmed, Kind: TargetUnknown}

	// An explicit prefix skips every heuristic below, which is what a user
	// reaches for when a title or an ID is ambiguous.
	if rest, ok := cutPrefixFold(trimmed, "livechat:"); ok {
		target.Kind = TargetLiveChatID
		target.LiveChatID = rest
		return target, nil
	}

	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, ".") {
		if parsed, ok := parseTargetURL(trimmed); ok {
			parsed.Raw = trimmed
			return parsed, nil
		}
	}

	switch {
	case strings.HasPrefix(trimmed, "@"):
		target.Kind = TargetHandle
		target.Handle = trimmed
	case channelIDPattern.MatchString(trimmed):
		target.Kind = TargetChannelID
		target.ChannelID = trimmed
	case liveChatIDPattern.MatchString(trimmed):
		// Live chat IDs are long opaque "Cg..." strings. Nothing else a user
		// types is shaped like one, so recognizing it here means
		// --live-chat-id and a pasted ID behave identically.
		target.Kind = TargetLiveChatID
		target.LiveChatID = trimmed
	case videoIDPattern.MatchString(trimmed):
		target.Kind = TargetVideoID
		target.VideoID = trimmed
	default:
		// A bare word is treated as a handle: it is the only form that can
		// still be resolved, and channels.list?forHandle costs 1 unit.
		if handlePattern.MatchString(trimmed) {
			target.Kind = TargetHandle
			target.Handle = "@" + trimmed
			return target, nil
		}
		return ChatTarget{Raw: trimmed}, errors.New("unrecognized chat target; use a video ID, a watch URL, an @handle, or a channel ID")
	}
	return target, nil
}

// Target shapes yc can classify without a network call.
var (
	// videoIDPattern is the 11-character YouTube video ID.
	videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	// channelIDPattern is the 24-character "UC..." channel ID.
	channelIDPattern = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	// liveChatIDPattern is the opaque live chat ID.
	liveChatIDPattern = regexp.MustCompile(`^Cg[A-Za-z0-9_.\-]{24,}$`)
	// handlePattern is a bare handle without its leading "@".
	handlePattern = regexp.MustCompile(`^[A-Za-z0-9_.\-]{3,30}$`)
)

// videoPathPrefixes are the URL path forms that name a video directly.
var videoPathPrefixes = []string{"live/", "shorts/", "embed/", "v/"}

// parseTargetURL classifies a YouTube URL. It reports false for anything that
// is not recognizably a YouTube target, so the caller can fall through to the
// bare-token heuristics rather than accepting a link to somewhere else.
func parseTargetURL(raw string) (ChatTarget, bool) {
	candidate := raw
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil {
		return ChatTarget{}, false
	}

	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	path := strings.Trim(parsed.Path, "/")

	switch host {
	case "youtu.be":
		if videoIDPattern.MatchString(path) {
			return ChatTarget{Kind: TargetVideoID, VideoID: path}, true
		}
		return ChatTarget{}, false
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com":
	default:
		return ChatTarget{}, false
	}

	if id := strings.TrimSpace(parsed.Query().Get("v")); videoIDPattern.MatchString(id) {
		return ChatTarget{Kind: TargetVideoID, VideoID: id}, true
	}
	for _, prefix := range videoPathPrefixes {
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			rest, _, _ = strings.Cut(rest, "/")
			if videoIDPattern.MatchString(rest) {
				return ChatTarget{Kind: TargetVideoID, VideoID: rest}, true
			}
			return ChatTarget{}, false
		}
	}
	if rest, ok := strings.CutPrefix(path, "channel/"); ok {
		rest, _, _ = strings.Cut(rest, "/")
		if channelIDPattern.MatchString(rest) {
			return ChatTarget{Kind: TargetChannelID, ChannelID: rest}, true
		}
		return ChatTarget{}, false
	}
	for _, prefix := range []string{"c/", "user/"} {
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			rest, _, _ = strings.Cut(rest, "/")
			if rest != "" {
				return ChatTarget{Kind: TargetHandle, Handle: "@" + strings.TrimPrefix(rest, "@")}, true
			}
			return ChatTarget{}, false
		}
	}
	if strings.HasPrefix(path, "@") {
		handle, _, _ := strings.Cut(path, "/")
		return ChatTarget{Kind: TargetHandle, Handle: handle}, true
	}
	return ChatTarget{}, false
}

// cutPrefixFold is a case-insensitive strings.CutPrefix for the explicit
// target prefixes.
func cutPrefixFold(value, prefix string) (string, bool) {
	if len(value) < len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(value[len(prefix):]), true
}

// BroadcastInfo is the normalized videos.list view of a broadcast, used for
// the chat title, the LIVE indicator, and the viewer count.
type BroadcastInfo struct {
	VideoID     string
	ChannelID   string
	Title       string
	Description string
	// LiveChatID is liveStreamingDetails.activeLiveChatId. It is absent once
	// the broadcast transitions to complete.
	LiveChatID string
	Live       bool
	// ConcurrentViewers is only meaningful when ViewersKnown is set; the
	// owner can hide it and it disappears after the stream ends.
	ConcurrentViewers int
	ViewersKnown      bool

	ActualStartTime    time.Time
	ActualEndTime      time.Time
	ScheduledStartTime time.Time
	// OfflineAt comes from the live chat list response, not videos.list, and
	// marks the start of the post-broadcast grace window.
	OfflineAt time.Time
}

// Identity is the authenticated user's own channel, resolved once at startup
// through channels.list?mine=true. Sent messages are echoed locally from this,
// since yc cannot learn its own badges any other way before a poll returns.
type Identity struct {
	ChannelID   string
	DisplayName string
	Handle      string
	AvatarURL   string

	SubscriberCount      int
	SubscriberCountKnown bool

	// Scopes are the granted OAuth scope strings. Empty means key-only
	// read mode, where the composer is disabled with an explicit reason.
	Scopes []string
}

// Subscription is one entry from subscriptions.list, used to autocomplete the
// chat picker.
type Subscription struct {
	ChannelID string
	Title     string
	Handle    string
}

// BanRequest is a moderation ban or timeout.
type BanRequest struct {
	LiveChatID string
	ChannelID  string
	// Duration of a timeout; zero means a permanent ban.
	Duration time.Duration
}

// BanResult identifies a created ban so it can be lifted again.
type BanResult struct {
	BanID     string
	Permanent bool
	At        time.Time
}

// StreamInfo is the editable broadcast metadata behind the Stream Info tab.
type StreamInfo struct {
	VideoID       string
	Title         string
	Description   string
	CategoryID    string
	CategoryTitle string
	// Privacy is "public", "unlisted", or "private".
	Privacy string
	Tags    []string
}

// Category is one videoCategories.list entry for the category picker.
type Category struct {
	ID    string
	Title string
}

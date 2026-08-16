package youtube

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// NormalizeOptions tunes normalization for one list response.
type NormalizeOptions struct {
	// Historical marks every produced message as backlog, which the app uses
	// to skip reveal animation and notifications for the priming page.
	Historical bool
	// Now anchors any locally derived timestamp.
	Now time.Time
	// SeenMessageIDs lets tombstone correlation decide whether a removed
	// message is still on screen. A tombstone for an unseen message is a
	// ModerationTombstone with nothing to redact.
	SeenMessageIDs func(id string) bool
}

// NormalizeResult is everything one list response produced. Each slice may be
// empty; none of them may be nil-checked as an error signal.
type NormalizeResult struct {
	Messages    []Message
	Moderations []ModerationEvent
	RoomEvents  []RoomEvent
	Polls       []PollState
}

// append merges another result into this one.
func (r *NormalizeResult) append(other NormalizeResult) {
	r.Messages = append(r.Messages, other.Messages...)
	r.Moderations = append(r.Moderations, other.Moderations...)
	r.RoomEvents = append(r.RoomEvents, other.RoomEvents...)
	r.Polls = append(r.Polls, other.Polls...)
}

// normalizeItem converts one wire item into whichever normalized events it
// produces.
//
// The unknown-type rule is absolute: any snippet.type not recognized -
// including invalidType, fanFundingEvent, and anything YouTube adds next -
// becomes a Message with MessageTypeUnknown, Text taken from
// snippet.displayMessage, and RawType preserved for redacted diagnostics.
// Never a crash, never a dropped row. Note that tombstone and chatEndedEvent
// are documented as carrying no displayMessage.
func normalizeItem(item liveChatMessage, opts NormalizeOptions) NormalizeResult {
	snippet := item.Snippet
	kind := EventKindFromSnippetType(snippet.Type)
	at := parseAPITime(snippet.PublishedAt, opts.Now)

	switch kind {
	case EventKindMessageDeleted, EventKindMessageRetracted:
		return NormalizeResult{Moderations: []ModerationEvent{
			normalizeDeletion(item, kind, at),
		}}

	case EventKindUserBanned:
		return NormalizeResult{Moderations: []ModerationEvent{
			normalizeBan(item, at),
		}}

	case EventKindTombstone:
		return NormalizeResult{Moderations: []ModerationEvent{
			normalizeTombstone(item, at, opts),
		}}

	case EventKindChatEnded:
		// chatEndedEvent carries no displayMessage and is documented as
		// silent. It is a room transition, not a chat row: the poller turns
		// it into ConnectionClosed and the history stays readable.
		return NormalizeResult{RoomEvents: []RoomEvent{{
			Type:       RoomChatEnded,
			LiveChatID: snippet.LiveChatID,
			Detail:     "live chat ended",
			At:         at,
		}}}

	case EventKindSponsorOnlyModeStarted, EventKindSponsorOnlyModeEnded:
		result := NormalizeResult{RoomEvents: []RoomEvent{normalizeSponsorOnly(item, kind, at)}}
		result.Messages = append(result.Messages, buildMessage(item, kind, at, opts, sponsorOnlyText(kind, snippet.DisplayMessage)))
		return result

	case EventKindPoll:
		result := NormalizeResult{}
		if poll, ok := normalizePoll(item, at); ok {
			result.Polls = append(result.Polls, poll)
			result.Messages = append(result.Messages, buildMessage(item, kind, at, opts, pollNoticeText(poll)))
			return result
		}
		result.Messages = append(result.Messages, buildMessage(item, kind, at, opts, snippet.DisplayMessage))
		return result

	default:
		return NormalizeResult{Messages: []Message{normalizeChatMessage(item, kind, at, opts)}}
	}
}

// normalizeChatMessage builds the chat row for every kind that renders as one,
// including the unknown-type fallback.
func normalizeChatMessage(item liveChatMessage, kind EventKind, at time.Time, opts NormalizeOptions) Message {
	snippet := item.Snippet
	msg := buildMessage(item, kind, at, opts, snippet.DisplayMessage)

	switch kind {
	case EventKindText:
		if snippet.TextMessageDetails != nil && strings.TrimSpace(snippet.TextMessageDetails.MessageText) != "" {
			msg.setText(snippet.TextMessageDetails.MessageText)
		}

	case EventKindSuperChat:
		if details := snippet.SuperChatDetails; details != nil {
			msg.SuperChat = &SuperChatDetails{
				Amount:  money(details.AmountMicros, details.Currency, details.AmountDisplayString),
				Tier:    details.Tier,
				Comment: details.UserComment,
			}
			if strings.TrimSpace(details.UserComment) != "" {
				msg.setText(details.UserComment)
			}
		}

	case EventKindSuperSticker:
		if details := snippet.SuperStickerDetails; details != nil {
			language := details.SuperStickerMetadata.AltTextLanguage
			if strings.TrimSpace(language) == "" {
				language = details.SuperStickerMetadata.Language
			}
			msg.SuperSticker = &SuperStickerDetails{
				Amount:    money(details.AmountMicros, details.Currency, details.AmountDisplayString),
				Tier:      details.Tier,
				StickerID: details.SuperStickerMetadata.StickerID,
				AltText:   details.SuperStickerMetadata.AltText,
				Language:  language,
			}
			if strings.TrimSpace(details.SuperStickerMetadata.AltText) != "" {
				// The alt text is the only renderable form of a sticker:
				// yc never fetches images.
				msg.setText(details.SuperStickerMetadata.AltText)
			}
		}

	case EventKindFanFunding:
		// fanFundingEvent predates Super Chat and has been deprecated since
		// 2017. It is decoded into the Super Chat shape rather than given a
		// pointer of its own, because it is the same fact - a tip with a
		// comment - and RawType still records what actually arrived.
		if details := snippet.FanFundingEventDetails; details != nil {
			msg.SuperChat = &SuperChatDetails{
				Amount:  money(details.AmountMicros, details.Currency, details.AmountDisplayString),
				Comment: details.UserComment,
			}
			if strings.TrimSpace(details.UserComment) != "" {
				msg.setText(details.UserComment)
			}
		}

	case EventKindGift:
		if details, ok := giftDetails(snippet); ok {
			msg.Gift = &details
			if strings.TrimSpace(details.AltText) != "" {
				msg.setText(details.AltText)
			} else if strings.TrimSpace(details.Name) != "" {
				msg.setText(details.Name)
			}
		}

	case EventKindNewSponsor:
		if details := snippet.NewSponsorDetails; details != nil {
			membershipKind := MembershipNew
			if details.IsUpgrade {
				membershipKind = MembershipUpgrade
			}
			msg.Membership = &MembershipDetails{Kind: membershipKind, LevelName: details.MemberLevelName}
			msg.Author.MemberLevelName = details.MemberLevelName
			msg.Author.IsMember = true
		}

	case EventKindMemberMilestone:
		if details := snippet.MemberMilestoneChatDetails; details != nil {
			msg.Membership = &MembershipDetails{
				Kind:      MembershipMilestone,
				LevelName: details.MemberLevelName,
				Months:    details.MemberMonth,
				Comment:   details.UserComment,
			}
			msg.Author.MemberLevelName = details.MemberLevelName
			msg.Author.MemberMonths = details.MemberMonth
			msg.Author.IsMember = true
			if strings.TrimSpace(details.UserComment) != "" {
				msg.setText(details.UserComment)
			}
		}

	case EventKindMembershipGifting:
		if details := snippet.MembershipGiftingDetails; details != nil {
			msg.Membership = &MembershipDetails{
				Kind:      MembershipGifting,
				LevelName: details.GiftMembershipsLevelName,
				GiftCount: details.GiftMembershipsCount,
			}
		}

	case EventKindGiftMembershipReceived:
		if details := snippet.GiftMembershipReceivedDetails; details != nil {
			msg.Membership = &MembershipDetails{
				Kind:                    MembershipGiftReceived,
				LevelName:               details.MemberLevelName,
				GifterChannelID:         details.GifterChannelID,
				AssociatedGiftMessageID: details.AssociatedMembershipGiftingMessageID,
			}
			msg.Author.MemberLevelName = details.MemberLevelName
			msg.Author.IsMember = true
		}
	}

	// Badges are derived after the membership details have had their say, so
	// a newSponsorEvent shows a member badge on the row that announces it.
	msg.Badges = BadgesForAuthor(msg.Author)
	return msg
}

// buildMessage assembles the fields every normalized row shares.
func buildMessage(item liveChatMessage, kind EventKind, at time.Time, opts NormalizeOptions, text string) Message {
	snippet := item.Snippet
	author := Author{
		ChannelID:   firstNonEmpty(item.AuthorDetails.ChannelID, snippet.AuthorChannelID),
		DisplayName: item.AuthorDetails.DisplayName,
		ChannelURL:  item.AuthorDetails.ChannelURL,
		AvatarURL:   item.AuthorDetails.ProfileImageURL,
		IsOwner:     item.AuthorDetails.IsChatOwner,
		IsModerator: item.AuthorDetails.IsChatModerator,
		IsMember:    item.AuthorDetails.IsChatSponsor,
		IsVerified:  item.AuthorDetails.IsVerified,
	}
	msg := Message{
		ID:         item.ID,
		LiveChatID: snippet.LiveChatID,
		Timestamp:  at,
		Author:     author,
		Badges:     BadgesForAuthor(author),
		Kind:       kind,
		Type:       MessageTypeForKind(kind),
		Historical: opts.Historical,
		RawType:    strings.TrimSpace(snippet.Type),
	}
	msg.setText(text)
	return msg
}

// setText replaces the body and re-splits it into fragments. Fragments are
// produced here rather than by the transport because the API supplies no
// fragment metadata at all.
func (m *Message) setText(text string) {
	m.Text = text
	m.Fragments = SplitFragments(text)
}

// normalizeDeletion handles messageDeletedEvent and messageRetractedEvent.
//
// Neither is delivered in practice - YouTube removed both from the reference on
// 2026-06-23 as "not being returned by the API" - but the enum members survive
// in the discovery document, so an arriving one is handled rather than dropped.
func normalizeDeletion(item liveChatMessage, kind EventKind, at time.Time) ModerationEvent {
	target := ""
	switch {
	case kind == EventKindMessageDeleted && item.Snippet.MessageDeletedDetails != nil:
		target = item.Snippet.MessageDeletedDetails.DeletedMessageID
	case kind == EventKindMessageRetracted && item.Snippet.MessageRetractedDetails != nil:
		target = item.Snippet.MessageRetractedDetails.RetractedMessageID
	}
	return ModerationEvent{
		Type:            ModerationMessageDeleted,
		LiveChatID:      item.Snippet.LiveChatID,
		TargetMessageID: target,
		At:              at,
	}
}

// normalizeBan converts userBannedEvent into a ban or a timeout.
func normalizeBan(item liveChatMessage, at time.Time) ModerationEvent {
	event := ModerationEvent{
		Type:       ModerationUserBanned,
		LiveChatID: item.Snippet.LiveChatID,
		At:         at,
	}
	details := item.Snippet.UserBannedDetails
	if details == nil {
		return event
	}
	event.TargetChannelID = details.BannedUserDetails.ChannelID
	event.TargetDisplayName = details.BannedUserDetails.DisplayName
	// banDurationSeconds is a uint64 sent as a JSON string.
	if seconds, ok := parseMicros(details.BanDurationSeconds); ok && seconds > 0 {
		// parseMicros clamps at MaxInt64, but that is a count of seconds:
		// multiplying it into nanoseconds would overflow and wrap a huge ban
		// into a negative Duration. Clamp to the largest representable
		// duration instead - "effectively forever" is the honest reading.
		if seconds > math.MaxInt64/int64(time.Second) {
			seconds = math.MaxInt64 / int64(time.Second)
		}
		event.Duration = time.Duration(seconds) * time.Second
	}
	if strings.EqualFold(strings.TrimSpace(details.BanType), "temporary") || event.Duration > 0 {
		event.Type = ModerationUserTimedOut
	}
	return event
}

// normalizeTombstone is how deletion actually reaches yc.
//
// A message the viewer already saw comes back with hasDisplayContent false; a
// tombstone for a message yc never held has nothing on screen to redact and is
// reported as such rather than as a deletion the UI cannot act on.
func normalizeTombstone(item liveChatMessage, at time.Time, opts NormalizeOptions) ModerationEvent {
	event := ModerationEvent{
		Type:            ModerationTombstone,
		LiveChatID:      item.Snippet.LiveChatID,
		TargetMessageID: item.ID,
		At:              at,
	}
	if opts.SeenMessageIDs != nil && opts.SeenMessageIDs(item.ID) {
		event.Type = ModerationMessageDeleted
	}
	return event
}

// normalizeSponsorOnly converts a members-only mode change into a room event.
func normalizeSponsorOnly(item liveChatMessage, kind EventKind, at time.Time) RoomEvent {
	roomType := RoomSponsorOnlyStarted
	detail := "members-only mode started"
	if kind == EventKindSponsorOnlyModeEnded {
		roomType = RoomSponsorOnlyEnded
		detail = "members-only mode ended"
	}
	return RoomEvent{
		Type:       roomType,
		LiveChatID: item.Snippet.LiveChatID,
		Detail:     detail,
		At:         at,
	}
}

// sponsorOnlyText prefers YouTube's own wording and falls back to yc's when the
// API sends none.
func sponsorOnlyText(kind EventKind, displayMessage string) string {
	if trimmed := strings.TrimSpace(displayMessage); trimmed != "" {
		return trimmed
	}
	if kind == EventKindSponsorOnlyModeEnded {
		return "members-only mode ended"
	}
	return "members-only mode started"
}

// normalizePoll converts a pollEvent item or the response's activePollItem into
// poll state. Tallies arrive as JSON strings.
func normalizePoll(item liveChatMessage, at time.Time) (PollState, bool) {
	details := item.Snippet.PollDetails
	if details == nil {
		return PollState{}, false
	}
	poll := PollState{
		MessageID:  item.ID,
		LiveChatID: item.Snippet.LiveChatID,
		Question:   details.Metadata.QuestionText,
		Status:     pollStatus(details.Status),
		At:         at,
	}
	for _, option := range details.Metadata.Options {
		tally, _ := parseMicros(option.Tally)
		poll.Options = append(poll.Options, PollOption{Text: option.OptionText, Tally: tally})
	}
	return poll, true
}

// pollStatus normalizes the poll lifecycle enum.
func pollStatus(value string) PollStatus {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active":
		return PollStatusActive
	case "closed":
		return PollStatusClosed
	default:
		return PollStatusUnknown
	}
}

// pollNoticeText is the chat row that accompanies a poll, so a viewer scrolling
// history sees that a poll happened even after it closed.
func pollNoticeText(poll PollState) string {
	question := strings.TrimSpace(poll.Question)
	if question == "" {
		return "poll"
	}
	return "poll: " + question
}

// giftDetails decodes whichever of the two documented gift shapes arrived.
//
// The discovery document places a flat object at snippet.giftDetails while the
// HTML reference nests the same fields under snippet.giftEventDetails.
// giftMetadata. Both are official and they disagree, so both are accepted.
func giftDetails(snippet liveChatSnippet) (GiftDetails, bool) {
	var wire wireGiftDetails
	switch {
	case snippet.GiftDetails != nil:
		wire = *snippet.GiftDetails
	case snippet.GiftEventDetails != nil:
		wire = snippet.GiftEventDetails.GiftMetadata
	default:
		return GiftDetails{}, false
	}
	return GiftDetails{
		Name:            wire.GiftName,
		URL:             wire.GiftURL,
		Duration:        parseGoogleDuration(wire.GiftDuration),
		AltText:         wire.AltText,
		Language:        wire.Language,
		Jewels:          wire.JewelsAmount,
		ComboCount:      wire.ComboCount,
		HasVisualEffect: wire.HasVisualEffect,
	}, true
}

// money builds a Money from the wire's string-encoded micros. No float is
// involved at any point between the wire and the screen.
func money(micros, currency, display string) Money {
	amount := Money{Currency: strings.TrimSpace(currency), Display: strings.TrimSpace(display)}
	if value, ok := parseMicros(micros); ok {
		amount.Micros = value
	}
	return amount
}

// parseMicros decodes an amountMicros-style field. The API sends uint64 and
// int64 values as JSON strings - amountMicros, banDurationSeconds,
// concurrentViewers, and poll tallies all do - so decoding them straight into a
// Go int64 fails. Money never passes through a float.
func parseMicros(value string) (int64, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		// Some fields are documented uint64; a value above MaxInt64 is not
		// a real amount, but it must not wrap into a negative one either.
		if unsigned, uerr := strconv.ParseUint(trimmed, 10, 64); uerr == nil {
			if unsigned > math.MaxInt64 {
				return math.MaxInt64, true
			}
			return int64(unsigned), true
		}
		return 0, false
	}
	return parsed, true
}

// parseGoogleDuration decodes a protobuf duration string such as "3.5s". An
// unparseable value yields zero rather than an error: a gift with an unreadable
// animation length is still a gift.
func parseGoogleDuration(value string) time.Duration {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	if parsed, err := time.ParseDuration(trimmed); err == nil {
		return parsed
	}
	seconds, err := strconv.ParseFloat(strings.TrimSuffix(trimmed, "s"), 64)
	if err != nil {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// parseAPITime decodes an RFC3339 timestamp, falling back to the supplied clock
// so a row with an unreadable publishedAt still sorts sensibly instead of
// jumping to the epoch.
func parseAPITime(value string, fallback time.Time) time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed
	}
	return fallback
}

// firstNonEmpty returns the first value with content after trimming.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

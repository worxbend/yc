package app

import (
	"strings"
	"unicode"

	"github.com/worxbend/yc/internal/youtube"
)

// messageFilter is one local view predicate. Filters are a view concern only:
// they decide what is drawn, never what is retained, so toggling one back off
// restores the full history without a refetch that would cost quota.
type messageFilter uint8

const (
	messageFilterMentions messageFilter = 1 << iota
	messageFilterRoles
	messageFilterEvents
	messageFilterNotices
)

// messageFilterSet is the enabled set for one chat. The zero value shows
// everything, which is why "no filter" needs no separate state.
type messageFilterSet uint8

type messageFilterDefinition struct {
	filter   messageFilter
	label    string
	shortcut string
	// keywords let the command palette find a filter by intent rather than
	// by its exact label.
	keywords []string
}

// messageFilterDefinitions is the whole filter vocabulary, in shortcut order.
// The role filter tests owner/moderator/member because those are the only
// roles the YouTube live chat API reports; there is no VIP, founder, or staff
// equivalent to carry over from a Twitch client.
var messageFilterDefinitions = []messageFilterDefinition{
	{
		filter:   messageFilterMentions,
		label:    "mentions",
		shortcut: "1",
		keywords: []string{"mention", "mentions", "at", "me"},
	},
	{
		filter:   messageFilterRoles,
		label:    "roles",
		shortcut: "2",
		keywords: []string{"owner", "moderator", "mod", "member", "role", "roles"},
	},
	{
		filter:   messageFilterEvents,
		label:    "events",
		shortcut: "3",
		keywords: []string{"superchat", "super", "sticker", "member", "gift", "paid", "event"},
	},
	{
		filter:   messageFilterNotices,
		label:    "notices",
		shortcut: "4",
		keywords: []string{"notice", "notices", "system", "moderation", "error"},
	},
}

func (s messageFilterSet) active() bool {
	return s != 0
}

func (s messageFilterSet) enabled(filter messageFilter) bool {
	return s&messageFilterSet(filter) != 0
}

func (s *messageFilterSet) toggle(filter messageFilter) {
	if s == nil {
		return
	}
	if s.enabled(filter) {
		*s &^= messageFilterSet(filter)
		return
	}
	*s |= messageFilterSet(filter)
}

func (s *messageFilterSet) reset() {
	if s != nil {
		*s = 0
	}
}

// summary is the status-bar rendering of the enabled set, in declaration order
// so the label never reorders itself as filters are toggled.
func (s messageFilterSet) summary() string {
	if !s.active() {
		return ""
	}
	labels := make([]string, 0, len(messageFilterDefinitions))
	for _, def := range messageFilterDefinitions {
		if s.enabled(def.filter) {
			labels = append(labels, def.label)
		}
	}
	return strings.Join(labels, ",")
}

// matches reports whether a message survives the set. Enabled filters are
// unioned rather than intersected: "mentions" plus "events" means "show me
// both", which is what a viewer means by turning two of them on.
func (s messageFilterSet) matches(message youtube.Message, mentionHandle string) bool {
	if !s.active() {
		return true
	}
	for _, def := range messageFilterDefinitions {
		if s.enabled(def.filter) && messageMatchesFilter(message, def.filter, mentionHandle) {
			return true
		}
	}
	return false
}

func messageMatchesFilter(message youtube.Message, filter messageFilter, mentionHandle string) bool {
	switch filter {
	case messageFilterMentions:
		return messageMentionsHandle(message, mentionHandle)
	case messageFilterRoles:
		return messageHasRoleBadge(message)
	case messageFilterEvents:
		return messageIsHighSignalEvent(message)
	case messageFilterNotices:
		return messageIsSystemNotice(message)
	default:
		return false
	}
}

// messageFilterForShortcutRune maps a bare digit to the filter it toggles.
func messageFilterForShortcutRune(r rune) (messageFilter, bool) {
	for _, def := range messageFilterDefinitions {
		if def.shortcut == string(r) {
			return def.filter, true
		}
	}
	return 0, false
}

func messageFilterLabel(filter messageFilter) string {
	for _, def := range messageFilterDefinitions {
		if def.filter == filter {
			return def.label
		}
	}
	return ""
}

// messageMentionsHandle reports whether a message names the user. YouTube
// supplies no reply metadata and no mention markup, so a mention is whatever
// the normalizer tagged as FragmentMention plus a plain-text scan of the
// remaining fragments - a reply here is literally an "@Name " prefix.
//
// An empty handle matches any mention, which is what the filter should do
// before identity resolution has returned.
func messageMentionsHandle(message youtube.Message, mentionHandle string) bool {
	target := normalizeMentionHandle(mentionHandle)
	for _, fragment := range message.Fragments {
		if fragment.Type == youtube.FragmentMention && mentionTextMatches(fragment.Text, target) {
			return true
		}
		if textHasMention(fragment.Text, target) {
			return true
		}
	}
	return textHasMention(message.Text, target)
}

func mentionTextMatches(text, target string) bool {
	mention := normalizeMentionHandle(text)
	if mention == "" {
		return false
	}
	return target == "" || mention == target
}

// textHasMention scans for "@word" runs. It walks runes rather than using a
// regexp because it runs once per fragment per arriving message.
func textHasMention(text, target string) bool {
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '@' || i+1 >= len(runes) || !isMentionRune(runes[i+1]) {
			continue
		}
		// The @ has to start a word. "mailto:me@example" is an address, not
		// a mention, and highlighting it would make every pasted email look
		// like someone was being called out.
		if i > 0 && isMentionRune(runes[i-1]) {
			continue
		}
		start := i + 1
		end := start + 1
		for end < len(runes) && isMentionRune(runes[end]) {
			end++
		}
		mention := strings.ToLower(string(runes[start:end]))
		if target == "" || mention == target {
			return true
		}
		i = end
	}
	return false
}

// normalizeMentionHandle reduces a display name, @handle, or channel title to
// the comparable token an "@..." mention would carry.
func normalizeMentionHandle(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "@")
	if value == "" {
		return ""
	}
	runes := []rune(value)
	end := 0
	for end < len(runes) && isMentionRune(runes[end]) {
		end++
	}
	if end == 0 {
		return ""
	}
	return strings.ToLower(string(runes[:end]))
}

func isMentionRune(r rune) bool {
	return r == '_' || r == '-' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// messageHasRoleBadge tests the three roles YouTube reports for a chat author.
// Verified is deliberately excluded: it says something about the channel, not
// about its standing in this chat.
func messageHasRoleBadge(message youtube.Message) bool {
	if message.Author.IsOwner || message.Author.IsModerator || message.Author.IsMember {
		return true
	}
	for _, badge := range message.Badges {
		switch badge.Kind {
		case youtube.BadgeOwner, youtube.BadgeModerator, youtube.BadgeMember:
			return true
		}
	}
	return false
}

// messageIsHighSignalEvent tests the paid and membership events a creator
// actually wants to pick out of a fast chat.
func messageIsHighSignalEvent(message youtube.Message) bool {
	switch message.Type {
	case youtube.MessageTypePaid, youtube.MessageTypeMembership:
		return true
	}
	return message.SuperChat != nil || message.SuperSticker != nil ||
		message.Gift != nil || message.Membership != nil
}

func messageIsSystemNotice(message youtube.Message) bool {
	switch message.Type {
	case youtube.MessageTypeNotice, youtube.MessageTypeSystem, youtube.MessageTypeUnknown:
		return true
	}
	return message.Deleted
}

// visibleMessages returns the messages a chat would draw under its filters.
// It never mutates the stored history: the filter is a predicate over the
// retained buffer, so turning it off restores everything without a refetch.
func (s *chatState) visibleMessages(mentionHandle string) []youtube.Message {
	if s == nil {
		return nil
	}
	if !s.filters.active() {
		return s.messages
	}
	out := make([]youtube.Message, 0, len(s.messages))
	for _, message := range s.messages {
		if s.filters.matches(message, mentionHandle) {
			out = append(out, message)
		}
	}
	return out
}

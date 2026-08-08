package app

import (
	"testing"

	"github.com/worxbend/yc/internal/youtube"
)

func TestMessageFilterPredicates(t *testing.T) {
	mention := youtube.Message{
		Text: "nice one @creator",
		Fragments: []youtube.MessageFragment{
			{Type: youtube.FragmentText, Text: "nice one "},
			{Type: youtube.FragmentMention, Text: "@creator"},
		},
		Type: youtube.MessageTypeChat,
	}
	moderator := youtube.Message{
		Author: youtube.Author{DisplayName: "mod", IsModerator: true},
		Type:   youtube.MessageTypeChat,
	}
	superChat := youtube.Message{
		Type:      youtube.MessageTypePaid,
		Kind:      youtube.EventKindSuperChat,
		SuperChat: &youtube.SuperChatDetails{Tier: 5},
	}
	notice := youtube.Message{Type: youtube.MessageTypeNotice, Text: "members-only mode on"}
	plain := youtube.Message{Type: youtube.MessageTypeChat, Text: "just chatting"}

	tests := []struct {
		name    string
		filter  messageFilter
		message youtube.Message
		want    bool
	}{
		{"mention matches", messageFilterMentions, mention, true},
		{"mention rejects plain", messageFilterMentions, plain, false},
		{"role matches moderator", messageFilterRoles, moderator, true},
		{"role rejects plain", messageFilterRoles, plain, false},
		{"events match super chat", messageFilterEvents, superChat, true},
		{"events reject plain", messageFilterEvents, plain, false},
		{"notices match", messageFilterNotices, notice, true},
		{"notices reject plain", messageFilterNotices, plain, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var set messageFilterSet
			set.toggle(tc.filter)
			if got := set.matches(tc.message, "creator"); got != tc.want {
				t.Fatalf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInactiveFilterSetMatchesEverything(t *testing.T) {
	var set messageFilterSet
	if !set.matches(youtube.Message{}, "creator") {
		t.Fatal("the zero filter set must show everything")
	}
	if set.summary() != "" {
		t.Fatalf("summary = %q, want empty", set.summary())
	}
}

func TestEnabledFiltersAreUnionedNotIntersected(t *testing.T) {
	var set messageFilterSet
	set.toggle(messageFilterMentions)
	set.toggle(messageFilterEvents)

	superChat := youtube.Message{Type: youtube.MessageTypePaid, Kind: youtube.EventKindSuperChat}
	if !set.matches(superChat, "creator") {
		t.Fatal("a message matching one enabled filter must survive the set")
	}
	if got := set.summary(); got != "mentions,events" {
		t.Fatalf("summary = %q, want declaration order", got)
	}
}

func TestMentionMatchingIsCaseInsensitiveAndWordBounded(t *testing.T) {
	message := youtube.Message{Text: "hey @Creator and @creatorfan"}
	if !messageMentionsHandle(message, "creator") {
		t.Fatal("a differently cased mention should match")
	}
	if messageMentionsHandle(youtube.Message{Text: "mailto:me@creator"}, "creator") {
		t.Fatal("an @ inside a word is not a mention")
	}
	if !messageMentionsHandle(message, "@Creator") {
		t.Fatal("a handle written with its sigil should still match")
	}
}

func TestFilterShortcutsCoverEveryDefinition(t *testing.T) {
	for _, def := range messageFilterDefinitions {
		filter, ok := messageFilterForShortcutRune(rune(def.shortcut[0]))
		if !ok || filter != def.filter {
			t.Fatalf("shortcut %q does not reach filter %q", def.shortcut, def.label)
		}
		if messageFilterLabel(def.filter) != def.label {
			t.Fatalf("label lookup failed for %q", def.label)
		}
	}
}

func TestVisibleMessagesLeavesHistoryIntact(t *testing.T) {
	state := &chatState{}
	state.messages = []youtube.Message{
		{ID: "a", Text: "plain", Type: youtube.MessageTypeChat},
		{ID: "b", Text: "@creator hi", Type: youtube.MessageTypeChat},
	}
	state.filters.toggle(messageFilterMentions)

	visible := state.visibleMessages("creator")
	if len(visible) != 1 || visible[0].ID != "b" {
		t.Fatalf("visible = %+v", visible)
	}
	if len(state.messages) != 2 {
		t.Fatal("filtering mutated the retained history")
	}
}

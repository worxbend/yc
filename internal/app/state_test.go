package app

import (
	"testing"
	"time"

	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/youtube"
)

// newModelForTest builds a deterministic model: animation off so no reveal
// queue and no renderer are involved, and a fixed size so scroll arithmetic is
// stable.
func newModelForTest(t *testing.T, chats ...string) shellModel {
	t.Helper()
	cfg := config.Default()
	cfg.Features.AnimationMode = "off"
	cfg.Features.ScrollbackLimit = 100
	cfg.DefaultChats = chats
	model := newShellModel(cfg, nil)
	model.width = 100
	model.height = 30
	return model
}

func testMessage(t *testing.T, id, chatID, author, text string) youtube.Message {
	t.Helper()
	return youtube.Message{
		ID:         id,
		LiveChatID: chatID,
		Timestamp:  time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		Author: youtube.Author{
			ChannelID:   "UC-" + author,
			DisplayName: author,
		},
		Text: text,
		Kind: youtube.EventKindText,
		Type: youtube.MessageTypeChat,
	}
}

func TestChatStateSetKeepsPerChatStateAcrossSwitches(t *testing.T) {
	model := newModelForTest(t, "first", "second")
	if got := model.chatCount(); got != 2 {
		t.Fatalf("chatCount = %d, want 2", got)
	}

	first := model.activeChatState()
	first.composerText = "draft one"
	first.filters.toggle(messageFilterMentions)
	first.scrollOffset = 3
	first.messages = append(first.messages, testMessage(t, "m1", "", "alice", "hello"))

	if !model.chats.switchBy(1) {
		t.Fatal("switchBy(1) did not move to the second chat")
	}
	second := model.activeChatState()
	if second == first {
		t.Fatal("switching chats returned the same state")
	}
	if second.composerText != "" || second.filters.active() || len(second.messages) != 0 {
		t.Fatalf("second chat inherited state: %+v", second)
	}

	second.composerText = "draft two"
	model.chats.switchBy(-1)
	back := model.activeChatState()
	if back.composerText != "draft one" {
		t.Fatalf("draft = %q, want %q", back.composerText, "draft one")
	}
	if !back.filters.enabled(messageFilterMentions) {
		t.Fatal("filters were lost across the switch")
	}
	if back.scrollOffset != 3 {
		t.Fatalf("scrollOffset = %d, want 3", back.scrollOffset)
	}
	if len(back.messages) != 1 {
		t.Fatalf("history = %d messages, want 1", len(back.messages))
	}
}

func TestEmptyChatSetIsANormalState(t *testing.T) {
	model := newModelForTest(t)
	if !model.noChatsOpen() {
		t.Fatal("a model with no configured chats should report an empty set")
	}
	state := model.activeChatState()
	if state == nil {
		t.Fatal("the empty state must still supply a chat state")
	}
	// Every key handler runs against the placeholder without panicking.
	state.composerText = "typed with nothing open"
	model.clampScroll()
	if model.chatCount() != 0 {
		t.Fatalf("chatCount = %d, want 0", model.chatCount())
	}
}

func TestCloseChatMovesActiveToNeighbor(t *testing.T) {
	model := newModelForTest(t, "one", "two", "three")
	keys := model.chats.chatKeys()
	model.chats.setActive(keys[1])

	if !model.chats.close(keys[1]) {
		t.Fatal("close reported no change")
	}
	if got := model.chats.activeKey(); got != keys[2] {
		t.Fatalf("active = %q, want the following chat %q", got, keys[2])
	}
	model.chats.close(keys[0])
	model.chats.close(keys[2])
	if !model.noChatsOpen() {
		t.Fatal("closing every chat should leave an empty, usable set")
	}
}

func TestApplyMessageCountsUnreadForBackgroundChats(t *testing.T) {
	model := newModelForTest(t, "one", "two")
	keys := model.chats.chatKeys()
	background := model.chats.ensureKey(keys[1])
	background.target.LiveChatID = "chat-two"

	state, isActive := model.chats.applyMessage(testMessage(t, "m1", "chat-two", "alice", "hi"))
	if isActive {
		t.Fatal("a background chat must not be reported as active")
	}
	if state.unread != 1 {
		t.Fatalf("unread = %d, want 1", state.unread)
	}
	if len(state.messages) != 1 {
		t.Fatalf("background message was not retained: %d", len(state.messages))
	}
	if model.chats.totalUnread() != 1 {
		t.Fatalf("totalUnread = %d, want 1", model.chats.totalUnread())
	}

	model.chats.setActive(keys[1])
	if state.unread != 0 {
		t.Fatalf("switching to a chat must clear its unread count, got %d", state.unread)
	}
}

func TestTrimScrollbackKeepsTheNewestMessages(t *testing.T) {
	state := &chatState{}
	for i := 0; i < 10; i++ {
		state.messages = append(state.messages, testMessage(t, string(rune('a'+i)), "", "alice", "x"))
	}
	state.trimScrollback(4)
	if len(state.messages) != 4 {
		t.Fatalf("retained %d messages, want 4", len(state.messages))
	}
	if state.messages[0].ID != "g" {
		t.Fatalf("oldest retained = %q, want %q", state.messages[0].ID, "g")
	}
	state.trimScrollback(0)
	if len(state.messages) != 4 {
		t.Fatal("a zero limit must disable trimming rather than clear the buffer")
	}
}

func TestLocalEchoIsReplacedRatherThanDuplicated(t *testing.T) {
	state := &chatState{localEchoes: map[string]struct{}{}}
	echo := testMessage(t, "local-echo-1", "", "you", "hello there")
	echo.LocalEcho = true
	state.localEchoes[echo.ID] = struct{}{}
	state.messages = append(state.messages, echo)

	authoritative := testMessage(t, "real-1", "", "you", "hello there")
	state.appendMessage(authoritative)

	if len(state.messages) != 1 {
		t.Fatalf("echo was duplicated: %d rows", len(state.messages))
	}
	if state.messages[0].ID != "real-1" || state.messages[0].LocalEcho {
		t.Fatalf("echo was not replaced by the authoritative message: %+v", state.messages[0])
	}
	if len(state.localEchoes) != 0 {
		t.Fatal("the echo marker should be cleared once it is confirmed")
	}
}

func TestMarkMessagesDeletedRemovesTheText(t *testing.T) {
	state := &chatState{}
	state.messages = append(state.messages,
		testMessage(t, "m1", "", "alice", "something removable"),
		testMessage(t, "m2", "", "bob", "kept"),
	)
	changed := state.markMessagesDeleted(func(message youtube.Message) bool {
		return message.ID == "m1"
	})
	if changed != 1 {
		t.Fatalf("markMessagesDeleted reported %d changes, want 1", changed)
	}
	if !state.messages[0].Deleted {
		t.Fatal("the matched message was not marked deleted")
	}
	if state.messages[0].Text != "" {
		t.Fatalf("deleted text survived as %q; a deletion must take the words off screen", state.messages[0].Text)
	}
	if state.messages[1].Text != "kept" {
		t.Fatal("an unmatched message was modified")
	}
}

func TestMergeTargetUpgradesAChatInPlace(t *testing.T) {
	model := newModelForTest(t, "dQw4w9WgXcQ")
	key := model.chats.chatKeys()[0]
	state := model.chats.ensureKey(key)
	state.messages = append(state.messages, testMessage(t, "m1", "", "alice", "hi"))

	state.mergeTarget(youtube.ChatTarget{
		VideoID:    "dQw4w9WgXcQ",
		LiveChatID: "live-chat-id",
		Title:      "Tonight's stream",
	})
	if got := model.chats.ensureKey(key); got == nil || len(got.messages) != 1 {
		t.Fatal("resolving a target must not move the chat's history to a new key")
	}
	if state.target.Label() != "Tonight's stream" {
		t.Fatalf("label = %q, want the resolved title", state.target.Label())
	}
	if !state.target.Resolved() {
		t.Fatal("the target should report as resolved once it has a liveChatId")
	}
}

func TestDrainUnsentComposerSendsRestoresDrafts(t *testing.T) {
	state := &chatState{}
	failed := queuedComposerSend{ID: 1, Draft: "first"}
	state.sendQueue = []queuedComposerSend{{ID: 2, Draft: "second"}}

	drafts, reply := state.drainUnsentComposerSends(failed)
	if len(drafts) != 2 || drafts[0] != "first" || drafts[1] != "second" {
		t.Fatalf("drafts = %v, want both unsent drafts in order", drafts)
	}
	if reply != nil {
		t.Fatal("a send with no reply context should restore none")
	}
	if len(state.sendQueue) != 0 {
		t.Fatal("the queue should be drained")
	}

	state.restoreComposerText(drafts...)
	if state.composerText != "first second" {
		t.Fatalf("composer = %q, want the drafts restored", state.composerText)
	}
}

func TestConfiguredTargetsDeduplicatesAndPreservesOrder(t *testing.T) {
	targets := configuredTargets([]string{" one ", "two", "one", "", "three"})
	if len(targets) != 3 {
		t.Fatalf("targets = %d, want 3", len(targets))
	}
	want := []string{"one", "two", "three"}
	for i, target := range targets {
		if target.Raw != want[i] {
			t.Fatalf("target[%d] = %q, want %q", i, target.Raw, want[i])
		}
	}
}

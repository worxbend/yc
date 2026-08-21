package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/youtube"
)

func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func altRuneKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
}

// press feeds one key and returns the updated model, so a test reads as a
// sequence of keystrokes rather than as Update plumbing.
func press(t *testing.T, model shellModel, msg tea.KeyMsg) shellModel {
	t.Helper()
	next, _ := model.Update(msg)
	updated, ok := next.(shellModel)
	if !ok {
		t.Fatalf("Update returned %T, want shellModel", next)
	}
	return updated
}

func TestInsertKeysFocusTheComposerAndEscReturns(t *testing.T) {
	for _, r := range []rune{'i', 'o', 'a'} {
		model := newModelForTest(t, "demo")
		model = press(t, model, runeKey(r))
		if model.focus != focusComposer {
			t.Fatalf("%q did not focus the composer", r)
		}
		model.activeChatState().composerText = "draft"
		model = press(t, model, key(tea.KeyEsc))
		if model.focus != focusChat {
			t.Fatalf("esc did not return to chat after %q", r)
		}
		if model.activeChatState().composerText != "draft" {
			t.Fatal("esc discarded the draft; it must keep it")
		}
	}
}

func TestTabCyclesFocus(t *testing.T) {
	model := newModelForTest(t, "demo")
	model = press(t, model, key(tea.KeyTab))
	if model.focus != focusComposer {
		t.Fatalf("focus = %v, want composer", model.focus)
	}
	model = press(t, model, key(tea.KeyTab))
	if model.focus != focusSidebar {
		t.Fatalf("focus = %v, want sidebar", model.focus)
	}
	model = press(t, model, key(tea.KeyTab))
	if model.focus != focusChat {
		t.Fatalf("focus = %v, want chat", model.focus)
	}
}

func TestTabSkipsTheSidebarWhenItIsHidden(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.sidebarVisibility = paneVisibilityHidden
	model = press(t, model, key(tea.KeyTab))
	model = press(t, model, key(tea.KeyTab))
	if model.focus != focusChat {
		t.Fatalf("focus = %v; tab must never stop on a hidden pane", model.focus)
	}
}

func TestSpaceLeaderChordIsExactlyTwoKeystrokes(t *testing.T) {
	model := newModelForTest(t, "demo")
	model = press(t, model, key(tea.KeySpace))
	if !model.leaderPending {
		t.Fatal("space outside the composer must arm the leader")
	}
	model = press(t, model, runeKey('e'))
	if model.leaderPending {
		t.Fatal("the leader must clear after the second keystroke")
	}
	if model.sidebarVisibility != paneVisibilityShown {
		t.Fatalf("space e did not show the sidebar: %v", model.sidebarVisibility)
	}

	// An unbound second key cancels rather than trapping the user in a mode.
	model = press(t, model, key(tea.KeySpace))
	model = press(t, model, runeKey('z'))
	if model.leaderPending {
		t.Fatal("an unbound chord key must still clear the leader")
	}
}

func TestSpaceIsLiteralInsideTheComposer(t *testing.T) {
	model := newModelForTest(t, "demo")
	model = press(t, model, runeKey('i'))
	model = press(t, model, runeKey('h'))
	model = press(t, model, key(tea.KeySpace))
	model = press(t, model, runeKey('i'))
	if model.leaderPending {
		t.Fatal("space in the composer must not arm the leader")
	}
	if got := model.activeChatState().composerText; got != "h i" {
		t.Fatalf("composer = %q, want %q", got, "h i")
	}
}

func TestBracketKeysSwitchChats(t *testing.T) {
	model := newModelForTest(t, "one", "two")
	first := model.activeChatKey()
	model = press(t, model, runeKey(']'))
	if model.activeChatKey() == first {
		t.Fatal("] did not switch chats")
	}
	model = press(t, model, runeKey('['))
	if model.activeChatKey() != first {
		t.Fatal("[ did not switch back")
	}
}

func TestFilterKeysToggleAndReset(t *testing.T) {
	model := newModelForTest(t, "demo")
	model = press(t, model, runeKey('1'))
	if !model.activeChatState().filters.enabled(messageFilterMentions) {
		t.Fatal("1 did not enable the mentions filter")
	}
	model = press(t, model, runeKey('3'))
	if !model.activeChatState().filters.enabled(messageFilterEvents) {
		t.Fatal("3 did not enable the events filter")
	}
	model = press(t, model, runeKey('1'))
	if model.activeChatState().filters.enabled(messageFilterMentions) {
		t.Fatal("1 did not toggle the mentions filter back off")
	}
	model = press(t, model, runeKey('0'))
	if model.activeChatState().filters.active() {
		t.Fatal("0 did not reset every filter")
	}
}

func TestFiltersNeverMutateStoredHistory(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.messages = append(state.messages,
		testMessage(t, "m1", "", "alice", "ordinary"),
		testMessage(t, "m2", "", "bob", "hey @you"),
	)
	model.mentionHandle = "you"

	model = press(t, model, runeKey('1'))
	if got := len(model.visibleMessages()); got != 1 {
		t.Fatalf("visible = %d, want only the mention", got)
	}
	if got := len(model.activeChatState().messages); got != 2 {
		t.Fatalf("stored history = %d, want 2; a filter is a view predicate", got)
	}
	model = press(t, model, runeKey('0'))
	if got := len(model.visibleMessages()); got != 2 {
		t.Fatalf("visible after reset = %d, want 2", got)
	}
}

func TestClearChatAsksOnceAndAnyOtherKeyCancels(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.activeChatState().messages = append(model.activeChatState().messages,
		testMessage(t, "m1", "", "alice", "hi"))

	model = press(t, model, key(tea.KeyCtrlL))
	if !model.pendingClearChat {
		t.Fatal("the first ctrl+l must arm the confirmation, not clear")
	}
	if len(model.activeChatState().messages) != 1 {
		t.Fatal("the first ctrl+l cleared history without asking")
	}
	model = press(t, model, runeKey('j'))
	if model.pendingClearChat {
		t.Fatal("an unrelated key must cancel the pending clear")
	}

	model = press(t, model, key(tea.KeyCtrlL))
	model = press(t, model, key(tea.KeyCtrlL))
	if model.pendingClearChat {
		t.Fatal("the confirmation should be consumed")
	}
	if len(model.activeChatState().messages) != 0 {
		t.Fatal("the confirmed clear did not empty the chat")
	}
}

func TestSelectionAndReplyMode(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.messages = append(state.messages,
		testMessage(t, "m1", "", "alice", "first"),
		testMessage(t, "m2", "", "bob", "second"),
	)

	model = press(t, model, runeKey('k'))
	if got := replyMessageID(model.activeChatState().selected); got != "m2" {
		t.Fatalf("selection = %q, want the newest message", got)
	}
	model = press(t, model, runeKey('k'))
	if got := replyMessageID(model.activeChatState().selected); got != "m1" {
		t.Fatalf("selection = %q, want the older message", got)
	}
	model = press(t, model, runeKey('j'))
	if got := replyMessageID(model.activeChatState().selected); got != "m2" {
		t.Fatalf("selection = %q, want to move back down", got)
	}
	// Browsing must not arm a reply. It used to, which meant a j on the way
	// past someone silently prefixed "@them" to the next line typed for the
	// room and sent it to a public chat.
	if model.activeChatState().replyTo != nil {
		t.Fatal("moving the selection must not arm a reply")
	}

	model = press(t, model, runeKey('r'))
	if model.focus != focusComposer {
		t.Fatal("r must move focus to the composer")
	}
	if got := replyMessageID(model.activeChatState().replyTo); got != "m2" {
		t.Fatalf("armed reply = %q, want r to arm the selected message", got)
	}
	if _, ok := model.selectedMessage(); !ok {
		t.Fatal("the selected message should be resolvable for the inspect panel")
	}

	// esc unwinds one step: the reply is canceled, the cursor is kept.
	model = press(t, model, key(tea.KeyEsc))
	model = press(t, model, key(tea.KeyEsc))
	if model.activeChatState().replyTo != nil {
		t.Fatal("esc did not cancel the armed reply")
	}
	if got := replyMessageID(model.activeChatState().selected); got != "m2" {
		t.Fatalf("selection = %q, want esc to keep the browsing cursor", got)
	}
}

func TestInspectTogglesAndEscCloses(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.activeChatState().messages = append(model.activeChatState().messages,
		testMessage(t, "m1", "", "alice", "hi"))

	model = press(t, model, runeKey('K'))
	if !model.activeChatState().inspectOpen {
		t.Fatal("K did not open inspect")
	}
	model = press(t, model, key(tea.KeyEsc))
	if model.activeChatState().inspectOpen {
		t.Fatal("esc did not close inspect")
	}
}

func TestOverlaysAreMutuallyExclusive(t *testing.T) {
	model := newModelForTest(t, "demo")
	model = press(t, model, key(tea.KeyCtrlP))
	if model.overlay.kind != overlayPalette {
		t.Fatal("ctrl+p did not open the palette")
	}
	model = press(t, model, key(tea.KeyCtrlT))
	if model.overlay.kind != overlayThemePicker {
		t.Fatal("ctrl+t did not replace the palette with the theme picker")
	}
	model = press(t, model, key(tea.KeyEsc))
	if model.overlay.open() {
		t.Fatal("esc did not close the overlay")
	}
}

func TestOverlayQueryEditingIsGraphemeAware(t *testing.T) {
	model := newModelForTest(t, "demo")
	model = press(t, model, key(tea.KeyCtrlP))
	model.overlay.query = "the👩‍👩‍👧‍👦"
	model = press(t, model, key(tea.KeyBackspace))
	if model.overlay.query != "the" {
		t.Fatalf("query = %q; backspace must remove one grapheme cluster", model.overlay.query)
	}
}

func TestThemePickerPreviewsAndCancelRestores(t *testing.T) {
	model := newModelForTest(t, "demo")
	original := model.theme
	model = press(t, model, key(tea.KeyCtrlT))
	model = press(t, model, key(tea.KeyDown))
	if model.theme == original {
		t.Skip("theme presets are not distinct enough to observe a preview")
	}
	model = press(t, model, key(tea.KeyEsc))
	if model.theme != original {
		t.Fatal("canceling the theme picker must restore the palette it opened with")
	}
}

func TestDisplayTogglesCycle(t *testing.T) {
	model := newModelForTest(t, "demo")
	layout := model.messageLayout
	model = press(t, model, key(tea.KeyCtrlG))
	if model.messageLayout == layout {
		t.Fatal("ctrl+g did not cycle the message layout")
	}
	badges := model.badgeMode
	model = press(t, model, key(tea.KeyCtrlB))
	if model.badgeMode == badges {
		t.Fatal("ctrl+b did not cycle the badge mode")
	}
	highlight := model.highlightEmoji
	model = press(t, model, key(tea.KeyCtrlY))
	if model.highlightEmoji == highlight {
		t.Fatal("ctrl+y did not toggle the emoji highlight")
	}
	full := model.fullUsername
	model = press(t, model, key(tea.KeyCtrlN))
	if model.fullUsername == full {
		t.Fatal("ctrl+n did not toggle full usernames")
	}
}

func TestAltDigitsSwitchTabs(t *testing.T) {
	model := newModelForTest(t, "demo")
	model = press(t, model, altRuneKey('2'))
	if model.activeTab != tabStreamInfo {
		t.Fatalf("activeTab = %v, want Stream Info", model.activeTab)
	}
	model = press(t, model, altRuneKey('1'))
	if model.activeTab != tabChat {
		t.Fatalf("activeTab = %v, want Chat", model.activeTab)
	}
}

func TestPageKeysScrollAndClamp(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	for i := 0; i < 40; i++ {
		state.messages = append(state.messages, testMessage(t, string(rune('a'+i)), "", "alice", "x"))
	}
	model = press(t, model, key(tea.KeyPgUp))
	if model.activeChatState().scrollOffset <= 0 {
		t.Fatal("page up did not scroll back into history")
	}
	for i := 0; i < 50; i++ {
		model = press(t, model, key(tea.KeyPgDown))
	}
	if got := model.activeChatState().scrollOffset; got != 0 {
		t.Fatalf("scrollOffset = %d, want it clamped to the bottom", got)
	}
}

func TestComposerRespectsTheMessageLimit(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.focus = focusComposer
	model.insertComposerText(strings.Repeat("x", youtube.MaxChatMessageRunes+50))
	if got := len([]rune(model.activeChatState().composerText)); got != youtube.MaxChatMessageRunes {
		t.Fatalf("composer length = %d, want %d", got, youtube.MaxChatMessageRunes)
	}
	if !strings.Contains(model.activeChatState().sendFeedback, "capped") {
		t.Fatal("the user must be told the message was capped")
	}
}

func TestComposerDropsControlCharacters(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.insertComposerText("a\x07b\nc")
	if got := model.activeChatState().composerText; got != "ab c" {
		t.Fatalf("composer = %q, want control characters dropped and newlines folded", got)
	}
}

func TestSendQueuesThroughTheClientAndEchoesLocally(t *testing.T) {
	model := newModelForTest(t, "demo")
	client := NewFakeChatClient()
	defer client.Close()
	model.client = client
	model.identity = youtube.Identity{ChannelID: "UC-me", DisplayName: "Me"}
	model.activeChatState().target.LiveChatID = "live-chat"

	model.focus = focusComposer
	model.insertComposerText("hello chat")
	next, cmd := model.Update(key(tea.KeyEnter))
	model = next.(shellModel)
	if cmd == nil {
		t.Fatal("enter did not dispatch the send")
	}
	msg := cmd()
	completed, ok := msg.(composerSendCompletedMsg)
	if !ok {
		t.Fatalf("send command produced %T, want composerSendCompletedMsg", msg)
	}
	if requests := client.SentRequests(); len(requests) != 1 || requests[0].Text != "hello chat" {
		t.Fatalf("client received %+v", requests)
	}

	next, _ = model.Update(completed)
	model = next.(shellModel)
	state := model.activeChatState()
	if state.sendState != composerSendSucceeded {
		t.Fatalf("sendState = %q, want %q", state.sendState, composerSendSucceeded)
	}
	if len(state.messages) != 1 || !state.messages[0].LocalEcho {
		t.Fatalf("the send was not echoed locally: %+v", state.messages)
	}
	if state.messages[0].Author.DisplayName != "Me" {
		t.Fatalf("echo author = %q, want the resolved identity", state.messages[0].Author.DisplayName)
	}
}

func TestReplyPrefixesTheSendWithAMention(t *testing.T) {
	model := newModelForTest(t, "demo")
	client := NewFakeChatClient()
	defer client.Close()
	model.client = client
	state := model.activeChatState()
	state.target.LiveChatID = "live-chat"
	state.messages = append(state.messages, testMessage(t, "m1", "", "Alice", "first"))

	model = press(t, model, runeKey('r'))
	model.insertComposerText("agreed")
	next, cmd := model.Update(key(tea.KeyEnter))
	model = next.(shellModel)
	if cmd != nil {
		cmd()
	}
	requests := client.SentRequests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if !strings.HasPrefix(requests[0].Text, "@Alice ") {
		t.Fatalf("text = %q; a reply is an @mention prefix, YouTube has no parent field", requests[0].Text)
	}
	if requests[0].ReplyToDisplayName != "Alice" {
		t.Fatalf("ReplyToDisplayName = %q", requests[0].ReplyToDisplayName)
	}
}

func TestFailedSendRestoresTheDraft(t *testing.T) {
	model := newModelForTest(t, "demo")
	client := NewFakeChatClient()
	defer client.Close()
	client.QueueSendResult(youtube.SendResult{}, ErrFakeChatClientClosed)
	model.client = client
	model.activeChatState().target.LiveChatID = "live-chat"
	model.focus = focusComposer

	model.insertComposerText("please keep this")
	next, cmd := model.Update(key(tea.KeyEnter))
	model = next.(shellModel)
	next, _ = model.Update(cmd())
	model = next.(shellModel)

	state := model.activeChatState()
	if state.sendState != composerSendFailed {
		t.Fatalf("sendState = %q, want failed", state.sendState)
	}
	if state.composerText != "please keep this" {
		t.Fatalf("composer = %q; a failed send must not lose the user's text", state.composerText)
	}
}

func TestChatCommandOpensThePickerAndAChat(t *testing.T) {
	model := newModelForTest(t)
	model.focus = focusComposer
	model.insertComposerText("/chats")
	next, _ := model.Update(key(tea.KeyEnter))
	model = next.(shellModel)
	if model.overlay.kind != overlayTargetPicker {
		t.Fatal("a bare /chats must open the picker")
	}
	model.overlay = overlayState{}

	model.insertComposerText("/chats dQw4w9WgXcQ")
	next, _ = model.Update(key(tea.KeyEnter))
	model = next.(shellModel)
	if model.chatCount() != 1 {
		t.Fatalf("chatCount = %d, want the named chat opened", model.chatCount())
	}
	if model.activeChatLabel() != "dQw4w9WgXcQ" {
		t.Fatalf("label = %q", model.activeChatLabel())
	}
}

func TestClosedMessageStreamBecomesADisconnectNotAPanic(t *testing.T) {
	model := newModelForTest(t, "demo")
	next, cmd := model.Update(chatClientMessageMsg{ok: false})
	model = next.(shellModel)
	if cmd != nil {
		t.Fatal("a closed stream must not re-arm the receive")
	}
	if got := model.activeChatState().status.Status; got != youtube.ConnectionDisconnected {
		t.Fatalf("status = %q, want disconnected", got)
	}
}

func TestChatEndedClosesTheChatButKeepsHistory(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.target.LiveChatID = "live-chat"
	state.messages = append(state.messages, testMessage(t, "m1", "live-chat", "alice", "bye"))

	ended := youtube.Message{
		ID:         "end",
		LiveChatID: "live-chat",
		Kind:       youtube.EventKindChatEnded,
		Type:       youtube.MessageTypeSystem,
	}
	next, _ := model.Update(chatClientMessageMsg{message: ended, ok: true})
	model = next.(shellModel)

	if got := model.activeChatState().status.Status; got != youtube.ConnectionClosed {
		t.Fatalf("status = %q, want closed", got)
	}
	if !model.activeChatState().ended {
		t.Fatal("the chat should be marked ended")
	}
	if len(model.activeChatState().messages) == 0 {
		t.Fatal("history must stay readable after a chat ends")
	}
}

func TestModerationRedactsRatherThanReprinting(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.target.LiveChatID = "live-chat"
	state.messages = append(state.messages,
		testMessage(t, "m1", "live-chat", "troll", "something removed"),
		testMessage(t, "m2", "live-chat", "alice", "kept"),
	)
	before := len(state.messages)

	next, _ := model.Update(chatClientModerationMsg{
		event: youtube.ModerationEvent{
			Type:            youtube.ModerationTombstone,
			LiveChatID:      "live-chat",
			TargetMessageID: "m1",
			At:              time.Now(),
		},
		ok: true,
	})
	model = next.(shellModel)

	messages := model.activeChatState().messages
	if len(messages) != before {
		t.Fatalf("moderation added or removed rows: %d, want %d", len(messages), before)
	}
	if !messages[0].Deleted || messages[0].Text != "" {
		t.Fatalf("the deleted message still carries text: %+v", messages[0])
	}
}

func TestBanRedactsEveryMessageFromTheChannel(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.target.LiveChatID = "live-chat"
	state.messages = append(state.messages,
		testMessage(t, "m1", "live-chat", "troll", "one"),
		testMessage(t, "m2", "live-chat", "troll", "two"),
		testMessage(t, "m3", "live-chat", "alice", "kept"),
	)

	model.applyModeration(youtube.ModerationEvent{
		Type:            youtube.ModerationUserBanned,
		LiveChatID:      "live-chat",
		TargetChannelID: "UC-troll",
	})
	messages := model.activeChatState().messages
	if !messages[0].Deleted || !messages[1].Deleted {
		t.Fatal("a ban must retroactively redact the banned channel's history")
	}
	if messages[2].Deleted {
		t.Fatal("a ban redacted an unrelated author")
	}
}

func TestMessagesArriveWithoutAnimationWhenAnimationIsOff(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.activeChatState().target.LiveChatID = "live-chat"
	next, _ := model.Update(chatClientMessageMsg{
		message: testMessage(t, "m1", "live-chat", "alice", "hi"),
		ok:      true,
	})
	model = next.(shellModel)
	state := model.activeChatState()
	if len(state.messages) != 1 {
		t.Fatalf("messages = %d, want the message appended statically", len(state.messages))
	}
	if state.active.len() != 0 {
		t.Fatal("no reveal should be queued while animation is off")
	}
}

func TestHistoricalMessagesAreNeverAnimated(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.animationMode = "fast"
	model.activeChatState().target.LiveChatID = "live-chat"

	message := testMessage(t, "m1", "live-chat", "alice", "backlog")
	message.Historical = true
	cmd := model.enqueueMessage(message)
	if cmd != nil {
		t.Fatal("the priming page must not schedule a reveal tick")
	}
	if model.activeChatState().active.len() != 0 {
		t.Fatal("a historical message was queued for reveal")
	}
}

func TestScrolledAwayMessagesAppendStatically(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.animationMode = "fast"
	state := model.activeChatState()
	state.target.LiveChatID = "live-chat"
	state.scrollOffset = 5

	if cmd := model.enqueueMessage(testMessage(t, "m1", "live-chat", "alice", "hi")); cmd != nil {
		t.Fatal("a message arriving while scrolled away must not animate")
	}
	if len(model.activeChatState().messages) != 1 {
		t.Fatal("the message was not appended")
	}
}

func TestReconnectReportsWhenUnavailable(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.client = nil
	cmd := model.requestReconnect(time.Now())
	if cmd != nil {
		t.Fatal("an unavailable reconnect must not schedule work")
	}
	if !strings.Contains(model.activeChatState().status.Detail, "reconnect unavailable") {
		t.Fatalf("detail = %q; the key must explain itself rather than do nothing",
			model.activeChatState().status.Detail)
	}
}

func TestReconnectRoundTrip(t *testing.T) {
	model := newModelForTest(t, "demo")
	client := NewFakeChatClient()
	defer client.Close()
	model.client = client

	next, cmd := model.Update(key(tea.KeyCtrlR))
	model = next.(shellModel)
	if cmd == nil {
		t.Fatal("ctrl+r did not schedule a reconnect")
	}
	if !model.reconnectInFlight {
		t.Fatal("the model should record the reconnect as in flight")
	}
	next, _ = model.Update(cmd())
	model = next.(shellModel)
	if model.reconnectInFlight {
		t.Fatal("the reconnect should no longer be in flight")
	}
	if client.ReconnectCount() != 1 {
		t.Fatalf("reconnects = %d, want 1", client.ReconnectCount())
	}
}

func TestQuotaSnapshotIsMirroredFromTheTransport(t *testing.T) {
	model := newModelForTest(t, "demo")
	client := NewFakeChatClient()
	defer client.Close()
	client.SetQuota(quota.Snapshot{
		UsedUnits:         3240,
		LimitUnits:        10000,
		RemainingUnits:    6760,
		EffectiveInterval: 43 * time.Second,
		Mode:              quota.ModeStretched,
		Estimated:         true,
	})
	model.client = client

	next, _ := model.Update(quotaTickMsg{})
	model = next.(shellModel)
	snapshot, known := model.quotaSnapshot()
	if !known {
		t.Fatal("the quota snapshot was not mirrored")
	}
	if snapshot.UsedUnits != 3240 || !snapshot.Estimated {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if model.effectivePollInterval() != 43*time.Second {
		t.Fatalf("poll interval = %s", model.effectivePollInterval())
	}
}

func TestWindowResizeClampsScroll(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.activeChatState().scrollOffset = 500
	next, _ := model.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	model = next.(shellModel)
	if model.width != 60 || model.height != 12 {
		t.Fatalf("size = %dx%d", model.width, model.height)
	}
	if model.activeChatState().scrollOffset != 0 {
		t.Fatalf("scrollOffset = %d, want it clamped to an empty buffer",
			model.activeChatState().scrollOffset)
	}
}

func TestSplashSwallowsTheFirstKey(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.splashUntil = time.Now().Add(time.Minute)
	model = press(t, model, runeKey('i'))
	if model.focus == focusComposer {
		t.Fatal("a key that skips the splash must not also act")
	}
	if !model.splashSkipped {
		t.Fatal("the splash was not skipped")
	}
	model = press(t, model, runeKey('i'))
	if model.focus != focusComposer {
		t.Fatal("the key after the splash should act normally")
	}
}

func TestCtrlCQuitsFromEverywhere(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.splashUntil = time.Now().Add(time.Minute)
	model.focus = focusComposer
	model.overlay = overlayState{kind: overlayPalette}
	_, cmd := model.Update(key(tea.KeyCtrlC))
	if cmd == nil {
		t.Fatal("ctrl+c must quit unconditionally")
	}
}

// TestPaletteDisplayTogglesMatchTheirKeys pins that the command palette and the
// key bindings run the same code for the four display cycles. The palette used
// to carry its own copies which skipped refreshActiveRevealRows, so cycling the
// layout from the palette while a message was still animating left that reveal
// drawn at the layout it started in.
func TestPaletteDisplayTogglesMatchTheirKeys(t *testing.T) {
	cases := []struct {
		title string
		key   tea.KeyType
	}{
		{title: "Cycle message layout", key: tea.KeyCtrlG},
		{title: "Cycle badge mode", key: tea.KeyCtrlB},
		{title: "Toggle emoji highlight", key: tea.KeyCtrlY},
		{title: "Toggle full usernames", key: tea.KeyCtrlN},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			inFlight := func(t *testing.T) shellModel {
				t.Helper()
				model := newAnimatedModelForTest(t, "first")
				next, _ := model.Update(chatClientMessageMsg{
					message: testMessage(t, "m1", "", "alice",
						"a fairly long message that will not reveal instantly"),
					ok: true,
				})
				model = next.(shellModel)
				if state := model.activeChatState(); state.active.len() == 0 {
					t.Skip("message revealed immediately; nothing to keep in sync")
				}
				return model
			}

			byKey := inFlight(t)
			if !byKey.handleDisplayToggleKey(tea.KeyMsg{Type: tc.key}) {
				t.Fatalf("key %v was not handled as a display toggle", tc.key)
			}

			byPalette := inFlight(t)
			updated, _ := byPalette.runPaletteCommand(tc.title)
			byPalette = updated.(shellModel)

			wantFrames := fmt.Sprintf("%v", byKey.activeChatState().revealQueue.Frames())
			gotFrames := fmt.Sprintf("%v", byPalette.activeChatState().revealQueue.Frames())
			if gotFrames != wantFrames {
				t.Errorf("palette %q left the in-flight reveal rendered differently from its key:\n got %s\nwant %s",
					tc.title, gotFrames, wantFrames)
			}
			if fmt.Sprintf("%v", byPalette.effectiveConfig.Features) != fmt.Sprintf("%v", byKey.effectiveConfig.Features) {
				t.Errorf("palette %q recorded different feature config from its key", tc.title)
			}

		})
	}
}

// TestRevealAndViewAgreeOnAvatarMode pins that the reveal animation and the
// view normalize avatarMode the same way. They used to each decide for
// themselves - the reveal lane compared the raw string to "off" while the view
// trimmed and case-folded it - so an avatarMode of "Off " animated a message
// with avatars and then settled it without them.
func TestRevealAndViewAgreeOnAvatarMode(t *testing.T) {
	for _, mode := range []string{"off", "Off", " off ", "OFF", "on", "auto", ""} {
		model := newModelForTest(t, "demo")
		model.avatarMode = mode
		reveal := model.revealRenderOptions(testMessage(t, "m1", "", "alice", "hello"))
		view := model.renderOptions(model.revealRowWidth())
		if reveal.Assets.ShowAvatars != view.Assets.ShowAvatars {
			t.Errorf("avatarMode %q: reveal shows avatars=%v, view shows avatars=%v",
				mode, reveal.Assets.ShowAvatars, view.Assets.ShowAvatars)
		}
		if reveal.Width != view.Width {
			t.Errorf("avatarMode %q: reveal width %d, view width %d", mode, reveal.Width, view.Width)
		}
	}
}

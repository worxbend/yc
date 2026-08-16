package app

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/youtube"
)

// Per-target isolation. Every chat yc has open is a separate world: its own
// history, scroll position, filters, draft, reply target, unread count, roster,
// dedupe ring, connection status, and moderation log. The shell is one model
// holding all of them, which is exactly the shape that leaks state between
// them, and the leaks are quiet - a draft that reappears in the wrong chat, a
// ban that blanks an innocent channel, an unread badge that never clears.
//
// These tests assert the separation directly rather than through the frame, so
// a failure names the field that bled.

// deliverMessage feeds one transport message through Update the way the runtime
// does, so routing, unread accounting, and dedupe all run.
func deliverMessage(t *testing.T, model shellModel, message youtube.Message) shellModel {
	t.Helper()
	next, _ := model.Update(chatClientMessageMsg{message: message, ok: true})
	updated, ok := next.(shellModel)
	if !ok {
		t.Fatalf("Update returned %T, want shellModel", next)
	}
	return updated
}

// isolationModel opens three chats, each resolved to its own liveChatId so
// stamped messages route by ID rather than falling back to the active chat.
func isolationModel(t *testing.T) (shellModel, []string) {
	t.Helper()
	model := newModelForTest(t, "alpha", "beta", "gamma")
	keys := model.chats.chatKeys()
	if len(keys) != 3 {
		t.Fatalf("chatKeys = %v, want three chats", keys)
	}
	for _, key := range keys {
		state := model.chats.stateForKey(key)
		state.target.LiveChatID = key
	}
	model.chats.setActive(keys[0])
	return model, keys
}

// chatFingerprint is everything about one chat that must not move when another
// chat is touched.
type chatFingerprint struct {
	messages     []youtube.Message
	scrollOffset int
	filters      messageFilterSet
	composerText string
	selected     string
	replyTo      string
	unread       int
	moderations  int
	status       youtube.ConnectionStatus
	rosterSize   int
	seenSize     int
}

func fingerprint(state *chatState) chatFingerprint {
	return chatFingerprint{
		messages:     append([]youtube.Message(nil), state.messages...),
		scrollOffset: state.scrollOffset,
		filters:      state.filters,
		composerText: state.composerText,
		selected:     replyMessageID(state.selected),
		replyTo:      replyMessageID(state.replyTo),
		unread:       state.unread,
		moderations:  len(state.moderations),
		status:       state.status.Status,
		rosterSize:   len(state.roster),
		seenSize:     len(state.seenIDs),
	}
}

func assertUnchanged(t *testing.T, what string, before, after chatFingerprint) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%s changed:\n before %+v\n after  %+v", what, before, after)
	}
}

// Everything a chat remembers has to survive a round trip through the other
// two, and none of it may be visible from them.
func TestEveryPerChatFieldSurvivesASwitchAndIsInvisibleFromElsewhere(t *testing.T) {
	model, keys := isolationModel(t)
	model.mentionHandle = "you"

	// Give each chat a distinct value in every per-chat field.
	for index, key := range keys {
		model.chats.setActive(key)
		state := model.activeChatState()
		state.composerText = "draft for " + key
		// Unread is deliberately zero here: visiting a chat is supposed to
		// clear it, so it is the one field a switch may legitimately move.
		// Its isolation is asserted separately below.
		state.status = youtube.ConnectionState{Status: youtube.ConnectionConnected, Detail: key}
		// Deep enough history that a scroll offset is a position the viewport
		// can actually hold; a shorter backlog is clamped straight back to
		// zero on the next frame and the assertion would prove nothing. Every
		// line is from a moderator and mentions the user, so the distinct
		// filter sets below still leave a full viewport to scroll through -
		// otherwise the clamp, not a leak, would be what moved the offset.
		state.messages = nil
		for i := 0; i < 60; i++ {
			message := testMessage(t, key+"-"+strconv.Itoa(i), key, "alice",
				"@you line "+strconv.Itoa(i)+" in "+key)
			message.Author.IsModerator = true
			state.messages = append(state.messages, message)
		}
		state.scrollOffset = index + 1
		state.selected = replyContextFromMessage(state.messages[index%2])
		state.replyTo = replyContextFromMessage(state.messages[0])
		state.recordModeration(youtube.ModerationEvent{
			LiveChatID:        key,
			Type:              youtube.ModerationUserBanned,
			TargetDisplayName: "spammer-" + key,
		})
		for _, message := range state.messages {
			state.observeAuthor(message)
			state.markSeen(message.ID)
		}
	}
	// Distinct filter sets, including the empty one, so "filters bled" is not
	// hidden by every chat happening to agree.
	model.chats.stateForKey(keys[0]).filters.toggle(messageFilterMentions)
	model.chats.stateForKey(keys[1]).filters.toggle(messageFilterRoles)

	before := make(map[string]chatFingerprint, len(keys))
	for _, key := range keys {
		before[key] = fingerprint(model.chats.stateForKey(key))
	}

	// Walk the whole ring twice with the real key bindings.
	for i := 0; i < 2*len(keys); i++ {
		model = press(t, model, runeKey(']'))
	}
	for i := 0; i < len(keys); i++ {
		model = press(t, model, runeKey('['))
	}

	for _, key := range keys {
		assertUnchanged(t, "chat "+key+" after switching", before[key], fingerprint(model.chats.stateForKey(key)))
	}

	// And no two chats agree on anything they were given distinct values for.
	first := fingerprint(model.chats.stateForKey(keys[0]))
	second := fingerprint(model.chats.stateForKey(keys[1]))
	if first.composerText == second.composerText {
		t.Fatalf("both chats hold the draft %q", first.composerText)
	}
	if first.filters == second.filters {
		t.Fatalf("both chats hold the filter set %v", first.filters)
	}
	if first.scrollOffset == second.scrollOffset {
		t.Fatalf("both chats hold scrollOffset %d", first.scrollOffset)
	}
}

// Unread is the one per-chat counter a switch is meant to move, and it must
// move for exactly the chat that was visited. Clearing the whole set would make
// the sidebar badges useless the first time a user tabbed through their chats.
func TestVisitingAChatClearsOnlyItsOwnUnread(t *testing.T) {
	model, keys := isolationModel(t)
	for index, key := range keys {
		state := model.chats.stateForKey(key)
		state.unread = index + 5
	}
	// Start somewhere that is not the chat under test.
	model.chats.setActive(keys[0])

	model = press(t, model, runeKey(']'))
	if got := model.activeChatKey(); got != keys[1] {
		t.Fatalf("] landed on %q, want %q", got, keys[1])
	}
	if got := model.chats.stateForKey(keys[1]).unread; got != 0 {
		t.Fatalf("the visited chat still shows %d unread", got)
	}
	if got := model.chats.stateForKey(keys[2]).unread; got != 7 {
		t.Fatalf("an unvisited chat's unread became %d, want 7", got)
	}
	if got := model.chats.totalUnread(); got != 7 {
		t.Fatalf("totalUnread = %d, want only the unvisited chat's 7", got)
	}
}

// A message stamped for one chat must land there and nowhere else - not in the
// active chat's history, not in its unread count, and not moving its cursor.
func TestAStampedMessageTouchesExactlyOneChat(t *testing.T) {
	model, keys := isolationModel(t)
	active, background := keys[0], keys[2]

	for _, key := range keys {
		state := model.chats.stateForKey(key)
		state.messages = []youtube.Message{testMessage(t, key+"-seed", key, "alice", "seed")}
	}
	before := make(map[string]chatFingerprint, len(keys))
	for _, key := range keys {
		before[key] = fingerprint(model.chats.stateForKey(key))
	}

	model = deliverMessage(t, model, testMessage(t, "new-1", background, "carol", "for the background chat"))

	backgroundState := model.chats.stateForKey(background)
	if len(backgroundState.messages) != 2 {
		t.Fatalf("background chat holds %d messages, want the delivery", len(backgroundState.messages))
	}
	if backgroundState.unread != before[background].unread+1 {
		t.Fatalf("background unread = %d, want %d", backgroundState.unread, before[background].unread+1)
	}
	if got := model.activeChatKey(); got != active {
		t.Fatalf("a background delivery moved the active chat to %q", got)
	}
	assertUnchanged(t, "the active chat", before[active], fingerprint(model.chats.stateForKey(active)))
	assertUnchanged(t, "the untouched chat", before[keys[1]], fingerprint(model.chats.stateForKey(keys[1])))
}

// A moderation event names the chat it belongs to. Applying it to the active
// chat instead would blank an innocent channel's backlog in a chat nobody
// moderated - and the words are gone for good, since redaction is destructive.
func TestAModerationEventRedactsOnlyItsOwnChat(t *testing.T) {
	model, keys := isolationModel(t)
	target, bystander := keys[1], keys[0]

	for _, key := range keys {
		state := model.chats.stateForKey(key)
		state.messages = []youtube.Message{
			testMessage(t, key+"-1", key, "spammer", "the same words in every chat"),
			testMessage(t, key+"-2", key, "alice", "an innocent line"),
		}
	}

	model.applyModeration(youtube.ModerationEvent{
		LiveChatID:        target,
		Type:              youtube.ModerationUserBanned,
		TargetChannelID:   "UC-spammer",
		TargetDisplayName: "spammer",
	})

	targetState := model.chats.stateForKey(target)
	if !targetState.messages[0].Deleted || targetState.messages[0].Text != "" {
		t.Fatalf("the ban did not redact its own chat: %+v", targetState.messages[0])
	}
	if len(targetState.moderations) != 1 {
		t.Fatalf("target chat recorded %d moderations, want 1", len(targetState.moderations))
	}

	for _, key := range keys {
		if key == target {
			continue
		}
		state := model.chats.stateForKey(key)
		if state.messages[0].Deleted || state.messages[0].Text == "" {
			t.Fatalf("chat %q was redacted by a ban in %q: %+v", key, target, state.messages[0])
		}
		if len(state.moderations) != 0 {
			t.Fatalf("chat %q recorded a moderation that belonged to %q", key, target)
		}
	}
	_ = bystander
}

// Closing a chat must take exactly that chat's state with it and leave the rest
// untouched, and reopening the same key must start clean rather than inheriting
// the closed chat's draft, history, or unread count.
func TestClosingAChatNeitherDisturbsTheOthersNorLeavesARemnant(t *testing.T) {
	model, keys := isolationModel(t)
	doomed := keys[1]

	for index, key := range keys {
		state := model.chats.stateForKey(key)
		state.composerText = "draft " + key
		state.unread = index + 1
		state.messages = []youtube.Message{testMessage(t, key+"-1", key, "alice", "history of "+key)}
		state.filters.toggle(messageFilterRoles)
	}
	before := map[string]chatFingerprint{
		keys[0]: fingerprint(model.chats.stateForKey(keys[0])),
		keys[2]: fingerprint(model.chats.stateForKey(keys[2])),
	}

	if !model.chats.close(doomed) {
		t.Fatal("close reported no change")
	}
	if model.chats.stateForKey(doomed) != nil {
		t.Fatal("the closed chat is still reachable by key")
	}
	assertUnchanged(t, "the chat before the closed one", before[keys[0]], fingerprint(model.chats.stateForKey(keys[0])))
	assertUnchanged(t, "the chat after the closed one", before[keys[2]], fingerprint(model.chats.stateForKey(keys[2])))

	// Reopening the same key is a new chat, not a resumed one.
	if !model.chats.open(youtube.ChatTarget{LiveChatID: doomed, Title: "reopened"}) {
		t.Fatal("reopening the closed key reported no change")
	}
	reopened := model.chats.stateForChatID(doomed)
	if reopened == nil {
		t.Fatal("the reopened chat is not reachable")
	}
	if reopened.composerText != "" {
		t.Fatalf("the reopened chat inherited the draft %q", reopened.composerText)
	}
	if len(reopened.messages) != 0 {
		t.Fatalf("the reopened chat inherited %d messages", len(reopened.messages))
	}
	if reopened.unread != 0 || reopened.filters.active() {
		t.Fatalf("the reopened chat inherited unread=%d filters=%v", reopened.unread, reopened.filters)
	}
}

// ctrl+L clears one chat. It is a destructive, confirmed key, and clearing the
// wrong chat - or every chat - is unrecoverable without spending quota to
// refetch, which for an ended stream is not possible at all.
func TestClearingOneChatLeavesTheOthersWhole(t *testing.T) {
	model, keys := isolationModel(t)
	for _, key := range keys {
		state := model.chats.stateForKey(key)
		state.messages = []youtube.Message{
			testMessage(t, key+"-1", key, "alice", "history of "+key),
			testMessage(t, key+"-2", key, "bob", "more history of "+key),
		}
		state.recordModeration(youtube.ModerationEvent{LiveChatID: key, Type: youtube.ModerationUserBanned})
	}
	before := map[string]chatFingerprint{
		keys[1]: fingerprint(model.chats.stateForKey(keys[1])),
		keys[2]: fingerprint(model.chats.stateForKey(keys[2])),
	}

	model = press(t, model, key(tea.KeyCtrlL))
	if !model.pendingClearChat {
		t.Fatal("ctrl+L did not arm the clear confirmation")
	}
	model = press(t, model, key(tea.KeyCtrlL))

	cleared := model.chats.stateForKey(keys[0])
	if len(cleared.messages) != 0 || len(cleared.moderations) != 0 {
		t.Fatalf("the active chat was not cleared: %d messages, %d moderations",
			len(cleared.messages), len(cleared.moderations))
	}
	assertUnchanged(t, "the second chat", before[keys[1]], fingerprint(model.chats.stateForKey(keys[1])))
	assertUnchanged(t, "the third chat", before[keys[2]], fingerprint(model.chats.stateForKey(keys[2])))
}

// A connection state names its chat. One chat reconnecting must not make the
// others look disconnected, which is the difference between "one stream ended"
// and "yc lost the network".
func TestAConnectionStateChangesOnlyItsOwnChat(t *testing.T) {
	model, keys := isolationModel(t)
	for _, key := range keys {
		model.chats.stateForKey(key).status = youtube.ConnectionState{Status: youtube.ConnectionConnected}
	}

	model.applyConnectionState(youtube.ConnectionState{
		Status: youtube.ConnectionReconnecting,
		ChatID: keys[2],
		Detail: "backing off",
	})

	if got := model.chats.stateForKey(keys[2]).status.Status; got != youtube.ConnectionReconnecting {
		t.Fatalf("the named chat is %v, want reconnecting", got)
	}
	for _, other := range keys[:2] {
		if got := model.chats.stateForKey(other).status.Status; got != youtube.ConnectionConnected {
			t.Fatalf("chat %q became %v because another chat reconnected", other, got)
		}
	}
}

// --- filters are view predicates -------------------------------------------

// deepHistorySnapshot captures history in a form where any mutation shows up,
// including inside a message's fragments.
func deepHistorySnapshot(messages []youtube.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(message.ID)
		builder.WriteString("\x00")
		builder.WriteString(message.Text)
		builder.WriteString("\x00")
		builder.WriteString(message.Author.ChannelID)
		builder.WriteString("\x00")
		builder.WriteString(string(message.Kind))
		builder.WriteString("\x00")
		if message.Deleted {
			builder.WriteString("deleted")
		}
		builder.WriteString("\x00")
		for _, fragment := range message.Fragments {
			builder.WriteString(string(fragment.Type))
			builder.WriteString(":")
			builder.WriteString(fragment.Text)
			builder.WriteString(",")
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

// filterCorpus is one message of every shape the four filters discriminate on,
// so no subset of them is a no-op.
func filterCorpus(t *testing.T) []youtube.Message {
	t.Helper()
	mention := testMessage(t, "f-mention", "chat", "alice", "hey @you look at this")
	mention.Fragments = []youtube.MessageFragment{{Type: youtube.FragmentText, Text: "hey @you look at this"}}

	moderator := testMessage(t, "f-mod", "chat", "carol", "settle down")
	moderator.Author.IsModerator = true

	owner := testMessage(t, "f-owner", "chat", "studio", "thanks for watching")
	owner.Author.IsOwner = true

	member := testMessage(t, "f-member", "chat", "bob", "member here")
	member.Author.IsMember = true

	superChat := testMessage(t, "f-super", "chat", "dave", "take my money")
	superChat.Kind = youtube.EventKindSuperChat
	superChat.Type = youtube.MessageTypeForKind(youtube.EventKindSuperChat)
	superChat.SuperChat = &youtube.SuperChatDetails{Amount: youtube.Money{Micros: 5_000_000, Currency: "USD"}}

	sponsor := testMessage(t, "f-sponsor", "chat", "erin", "")
	sponsor.Kind = youtube.EventKindNewSponsor
	sponsor.Type = youtube.MessageTypeForKind(youtube.EventKindNewSponsor)

	notice := testMessage(t, "f-notice", "chat", "", "chat is now members only")
	notice.Kind = youtube.EventKindSponsorOnlyModeStarted
	notice.Type = youtube.MessageTypeNotice

	system := testMessage(t, "f-system", "chat", "", "reconnected")
	system.Type = youtube.MessageTypeSystem

	plain := testMessage(t, "f-plain", "chat", "frank", "just an ordinary line")

	return []youtube.Message{mention, moderator, owner, member, superChat, sponsor, notice, system, plain}
}

// A filter decides what is drawn and never what is retained. Every subset of
// the four filters is exercised, because the union logic means a leak can hide
// behind a filter that happens to admit everything.
func TestEveryFilterCombinationIsAPurePredicate(t *testing.T) {
	filters := []messageFilter{
		messageFilterMentions,
		messageFilterRoles,
		messageFilterEvents,
		messageFilterNotices,
	}

	for mask := 0; mask < 1<<len(filters); mask++ {
		state := &chatState{messages: filterCorpus(t)}
		want := deepHistorySnapshot(state.messages)
		wantLen := len(state.messages)

		for index, filter := range filters {
			if mask&(1<<index) != 0 {
				state.filters.toggle(filter)
			}
		}

		visible := state.visibleMessages("you")

		if got := deepHistorySnapshot(state.messages); got != want {
			t.Fatalf("mask %04b mutated retained history:\n got %q\nwant %q", mask, got, want)
		}
		if len(state.messages) != wantLen {
			t.Fatalf("mask %04b changed history length to %d", mask, len(state.messages))
		}

		// The visible set is an order-preserving subsequence of history: a
		// filter may hide a row but must never reorder or invent one.
		cursor := 0
		for _, message := range visible {
			for cursor < len(state.messages) && state.messages[cursor].ID != message.ID {
				cursor++
			}
			if cursor >= len(state.messages) {
				t.Fatalf("mask %04b produced %q, which is not in history in order", mask, message.ID)
			}
			cursor++
		}
		if mask == 0 && len(visible) != wantLen {
			t.Fatalf("the empty filter set hid %d rows", wantLen-len(visible))
		}
		if mask != 0 && len(visible) == 0 {
			t.Fatalf("mask %04b hid every message; the corpus no longer discriminates", mask)
		}

		// Turning every filter back off restores exactly the original view.
		state.filters.reset()
		if got := deepHistorySnapshot(state.visibleMessages("you")); got != want {
			t.Fatalf("mask %04b did not restore on reset:\n got %q\nwant %q", mask, got, want)
		}
	}
}

// Rendering a filtered frame must not mutate history either. View() is pure by
// house rule, and the filter path is the one place where the view walks the
// retained slice with a predicate in hand.
func TestRenderingAFilteredFrameLeavesHistoryIntact(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.mentionHandle = "you"
	state := model.activeChatState()
	state.messages = filterCorpus(t)
	want := deepHistorySnapshot(state.messages)

	for _, shortcut := range []rune{'1', '2', '3', '4'} {
		model = press(t, model, runeKey(shortcut))
		for _, width := range []int{62, 100, 130} {
			model.width, model.height = width, 30
			_ = model.View()
		}
		if got := deepHistorySnapshot(model.activeChatState().messages); got != want {
			t.Fatalf("filter %q plus a render mutated history:\n got %q\nwant %q", shortcut, got, want)
		}
	}

	model = press(t, model, runeKey('0'))
	if got := len(model.visibleMessages()); got != len(state.messages) {
		t.Fatalf("resetting filters showed %d of %d messages", got, len(state.messages))
	}
}

// A filter set belongs to one chat. Filtering the chat you are reading must not
// hide anything in the one you are not, because the unread badge and the
// activity column are both computed from the other chat's full history.
func TestFiltersDoNotCrossChats(t *testing.T) {
	model, keys := isolationModel(t)
	model.mentionHandle = "you"
	for _, key := range keys {
		state := model.chats.stateForKey(key)
		state.messages = filterCorpus(t)
	}

	model = press(t, model, runeKey('1'))
	activeVisible := len(model.visibleMessages())
	if activeVisible == len(filterCorpus(t)) {
		t.Fatal("the mentions filter hid nothing; the corpus no longer discriminates")
	}

	for _, other := range keys[1:] {
		state := model.chats.stateForKey(other)
		if state.filters.active() {
			t.Fatalf("chat %q inherited the active chat's filters", other)
		}
		if got := len(state.visibleMessages(model.mentionHandle)); got != len(state.messages) {
			t.Fatalf("chat %q shows %d of %d messages while another chat is filtered",
				other, got, len(state.messages))
		}
	}
}

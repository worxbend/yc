package app

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/render"
	"github.com/worxbend/yc/internal/youtube"
)

// A high-throughput flood arriving in four chats at once, while the user is
// doing everything the shell lets them do.
//
// The single-chat harness in stress_test.go pins the properties that break
// under load in one chat. This pins the ones that break under load in several:
// a message filed against the wrong chat, a draft that follows the user from
// one chat to the next, a reveal queue that keeps animating a chat nobody is
// looking at, an unread count that never settles, and a moderation action that
// blanks rows in a chat it was not armed in.
//
// Like its sibling it runs on arithmetic alone - a fake clock, no goroutines,
// no wall-clock sleeps - so a failure is a regression rather than a busy CI box.

// multiChatCount is small enough that every chat gets a meaningful share of the
// burst and large enough that "the active one" is a minority of the set.
const multiChatCount = 4

// multiChatKeys are the liveChatIds the burst is stamped with.
func multiChatKeys() []string {
	keys := make([]string, 0, multiChatCount)
	for i := 0; i < multiChatCount; i++ {
		keys = append(keys, "chat-"+strconv.Itoa(i))
	}
	return keys
}

// newMultiChatStressModel builds a live shell with multiChatCount chats open,
// each resolved to its own liveChatId so every delivery routes by ID rather
// than through the single-chat fallback.
func newMultiChatStressModel(t *testing.T, clock *stressClock, width, height int) (shellModel, *FakeChatClient) {
	t.Helper()
	cfg := config.Default()
	cfg.Features.AnimationMode = "fast"
	cfg.Features.ScrollbackLimit = stressScrollback
	cfg.Features.HighlightEmoji = true
	cfg.DefaultChats = multiChatKeys()

	client := NewFakeChatClient()
	t.Cleanup(func() { _ = client.Close() })

	model := newLiveModel(cfg, client, clock, ClientOptions{})
	model.width, model.height = width, height
	model.lastFrameAt = clock.now
	model.mentionHandle = "you"

	keys := model.chats.chatKeys()
	if len(keys) != multiChatCount {
		t.Fatalf("opened %d chats, want %d", len(keys), multiChatCount)
	}
	for index, key := range keys {
		state := model.chats.stateForKey(key)
		state.target.LiveChatID = multiChatKeys()[index]
		state.target.Title = "Stream " + strconv.Itoa(index)
		state.status = youtube.ConnectionState{Status: youtube.ConnectionConnected, Detail: "polling"}
	}
	model.chats.setActive(keys[0])
	return model, client
}

// TestMultiChatHighThroughputKeepsEveryTargetSeparate floods four chats at once
// while switching between them, cycling layouts, resizing, filtering, typing,
// and moderating - and asserts after every single delivery that nothing crossed
// a chat boundary.
func TestMultiChatHighThroughputKeepsEveryTargetSeparate(t *testing.T) {
	clock := &stressClock{now: stressStart}
	model, _ := newMultiChatStressModel(t, clock, 120, 34)

	chatIDs := multiChatKeys()
	keys := model.chats.chatKeys()
	keyForChatID := make(map[string]string, len(chatIDs))
	for index, id := range chatIDs {
		keyForChatID[id] = keys[index]
	}

	// Each chat gets its own draft up front. Nothing in the flood may move it.
	drafts := make(map[string]string, len(keys))
	for index, key := range keys {
		draft := "draft-" + strconv.Itoa(index)
		model.chats.stateForKey(key).composerText = draft
		drafts[key] = draft
	}

	widths := []int{120, 62, render.MinimumRenderWidth + 2, 88, 160}
	delivered := make(map[string]int, len(chatIDs))
	// newest records the last ID fed to each chat. Whatever the scrollback
	// limit trims, the newest message must always still be reachable: losing
	// the front of the chat is the one loss a reader cannot notice.
	newest := make(map[string]string, len(chatIDs))
	switches := 0

	burst := stressBurst(600)
	for i, message := range burst {
		chatID := chatIDs[i%len(chatIDs)]
		message.LiveChatID = chatID
		message.ID = fmt.Sprintf("multi-%04d", i)
		targetKey := keyForChatID[chatID]

		// Snapshot every chat the delivery must not touch.
		before := make(map[string]chatFingerprint, len(keys))
		for _, key := range keys {
			if key == targetKey {
				continue
			}
			before[key] = fingerprint(model.chats.stateForKey(key))
		}

		activeBefore := model.activeChatKey()
		targetLenBefore := len(model.chats.stateForKey(targetKey).messages)

		model = feedStress(t, model, message)
		delivered[chatID]++
		newest[chatID] = message.ID

		// --- the delivery landed in exactly one chat ------------------------
		target := model.chats.stateForKey(targetKey)
		grew := len(target.messages) - targetLenBefore
		if grew <= 0 && target.revealQueue.Len() == 0 && len(target.messages) < stressScrollback {
			t.Fatalf("message %d for %q landed nowhere: history %d, reveals 0",
				i, chatID, len(target.messages))
		}
		for key, snapshot := range before {
			assertUnchanged(t, fmt.Sprintf("chat %q while %q received message %d", key, chatID, i),
				snapshot, fingerprint(model.chats.stateForKey(key)))
		}

		// --- only the chat on screen animates -------------------------------
		//
		// A background chat that queued reveals would keep re-rendering rows
		// nobody can see and would still be draining them minutes after the
		// user switched away.
		for _, key := range keys {
			state := model.chats.stateForKey(key)
			if got := state.revealQueue.Len(); got > animation.DefaultMaxQueued {
				t.Fatalf("message %d: chat %q holds %d reveals, above the bound of %d",
					i, key, got, animation.DefaultMaxQueued)
			}
			if key == model.activeChatKey() {
				continue
			}
			if got := state.revealQueue.Len(); got != 0 {
				t.Fatalf("message %d: background chat %q holds %d reveals", i, key, got)
			}
			if len(state.activeMessages) != 0 || len(state.activeOrder) != 0 {
				t.Fatalf("message %d: background chat %q tracks %d mid-reveal messages",
					i, key, len(state.activeMessages))
			}
		}

		// --- unread belongs to the chats nobody is reading -------------------
		if got := model.chats.stateForKey(model.activeChatKey()).unread; got != 0 {
			t.Fatalf("message %d: the chat on screen shows %d unread", i, got)
		}

		// --- drafts never follow the user -----------------------------------
		for key, draft := range drafts {
			if got := model.chats.stateForKey(key).composerText; got != draft {
				t.Fatalf("message %d: chat %q holds the draft %q, want %q", i, key, got, draft)
			}
		}
		if activeBefore != model.activeChatKey() {
			t.Fatalf("message %d: a delivery moved the active chat from %q to %q",
				i, activeBefore, model.activeChatKey())
		}

		// --- interaction ------------------------------------------------------
		switch {
		case i%29 == 0:
			model = press(t, model, runeKey(']'))
			switches++
		case i%23 == 0:
			model = press(t, model, key(tea.KeyCtrlG))
			if got := render.NormalizeLayoutMode(string(model.messageLayout)); got != model.messageLayout {
				t.Fatalf("message %d: ctrl+g produced the unknown layout %q", i, model.messageLayout)
			}
		case i%19 == 0:
			size := tea.WindowSizeMsg{Width: widths[(i/19)%len(widths)], Height: 20 + (i/19)%16}
			next, _ := model.Update(size)
			model = next.(shellModel)
		case i%17 == 0:
			clock.advance(2 * animation.DefaultFrameInterval)
			next, _ := model.Update(revealTickMsg{})
			model = next.(shellModel)
		case i%13 == 0:
			// Filter the chat on screen. It is a view predicate, so it must
			// not change what any chat retains - including this one.
			model = press(t, model, runeKey('1'))
		case i%11 == 0:
			model = press(t, model, runeKey('0'))
		case i%7 == 0:
			model = press(t, model, key(tea.KeyPgUp))
		case i%5 == 0:
			model = press(t, model, key(tea.KeyEnd))
		}
		// Drafts are re-checked after interaction too: switching chats with a
		// draft in the composer is exactly how a draft leaks.
		for key, draft := range drafts {
			if got := model.chats.stateForKey(key).composerText; got != draft {
				t.Fatalf("message %d: interaction moved chat %q's draft to %q, want %q",
					i, key, got, draft)
			}
		}

		if i%31 == 0 {
			assertRectangularFrame(t, model, fmt.Sprintf("multi-chat flood at message %d", i))
		}
	}

	if switches == 0 {
		t.Fatal("the harness never switched chats; the switch path went unasserted")
	}

	// --- accounting ---------------------------------------------------------
	//
	// Every message is in exactly one chat's history or mid-reveal, or was
	// deliberately trimmed by the scrollback limit. Anything else is a silent
	// loss, and a moderator cannot detect that for themselves.
	totalDelivered := 0
	for _, count := range delivered {
		totalDelivered += count
	}
	if totalDelivered != len(burst) {
		t.Fatalf("delivered %d of %d messages", totalDelivered, len(burst))
	}
	for index, key := range keys {
		state := model.chats.stateForKey(key)
		chatID := chatIDs[index]
		held := len(state.messages) + len(state.activeMessages)
		if held > stressScrollback+animation.DefaultMaxQueued {
			t.Errorf("chat %q holds %d messages, above the scrollback limit of %d",
				key, held, stressScrollback)
		}
		if held > delivered[chatID] {
			t.Errorf("chat %q holds %d messages but only %d were delivered to it",
				key, held, delivered[chatID])
		}
		if held == 0 {
			t.Errorf("chat %q holds nothing after %d deliveries", key, delivered[chatID])
		}
		var newestHeld bool
		for _, message := range state.messages {
			if message.ID == newest[chatID] {
				newestHeld = true
			}
		}
		// Mid-reveal rows are filed under a synthesized reveal key rather
		// than the message ID, so this matches on the message itself.
		for _, message := range state.activeMessages {
			if message.ID == newest[chatID] {
				newestHeld = true
			}
		}
		if !newestHeld {
			t.Errorf("chat %q lost its newest message %q", key, newest[chatID])
		}
		// Every retained row wears the routing key it was filed under, so a
		// misfiled message shows up here rather than as a mystery in the
		// activity column.
		for _, message := range state.messages {
			if message.LiveChatID != "" && message.LiveChatID != chatID {
				t.Fatalf("chat %q retains a message stamped for %q", key, message.LiveChatID)
			}
		}
	}

	// --- the frame is still exact at every size -----------------------------
	for _, width := range append(widths, 8, 200) {
		for _, height := range []int{3, 20, 34, 60} {
			resized, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
			assertRectangularFrame(t, resized.(shellModel),
				fmt.Sprintf("post multi-chat flood at %dx%d", width, height))
		}
	}
}

// A moderation action taken in the middle of a four-chat flood must land in the
// chat it was armed in and nowhere else, and the flood must keep arriving in
// every other chat while the request is on the wire.
func TestModerationDuringAMultiChatFloodStaysInItsOwnChat(t *testing.T) {
	clock := &stressClock{now: stressStart}
	model, _ := newMultiChatStressModel(t, clock, 120, 34)

	chatIDs := multiChatKeys()
	keys := model.chats.chatKeys()

	moderator := newFakeModeratingClient()
	model.client = moderator
	model.identity = youtube.Identity{
		ChannelID:   "UC-owner",
		DisplayName: "you",
		Scopes:      []string{"https://www.googleapis.com/auth/youtube.force-ssl"},
	}
	model.identityKnown = true
	for _, key := range keys {
		model.chats.stateForKey(key).target.ChannelID = "UC-owner"
	}

	// A single channel says the same thing in every chat, which is the shape
	// that catches a ban applied by author instead of by chat.
	const spam = "buy followers at example dot com"
	for index, chatID := range chatIDs {
		message := testMessage(t, "spam-"+strconv.Itoa(index), chatID, "spammer", spam)
		model = feedStress(t, model, message)
	}

	// Switching away and back settles anything still mid-reveal, because the
	// chat being left stops animating the instant it stops being active. The
	// moderation cursor only reaches settled history, so this is the state a
	// user pressing b would actually be in.
	armedKey := keys[0]
	model.chats.setActive(keys[1])
	model.chats.setActive(armedKey)
	armedState := model.chats.stateForKey(armedKey)
	if len(armedState.messages) == 0 {
		t.Fatal("the armed chat has no settled message to moderate")
	}
	target := armedState.messages[len(armedState.messages)-1]
	armedState.selected = replyContextFromMessage(target)

	model = armModeration(t, model, moderationBanRune)
	model, cmd := commitArmed(t, model, moderationBanRune)

	// The flood keeps arriving in the other chats while the ban is in flight.
	for i, message := range stressBurst(120) {
		message.LiveChatID = chatIDs[1+i%(len(chatIDs)-1)]
		message.ID = fmt.Sprintf("inflight-%04d", i)
		model = feedStress(t, model, message)
	}
	if model.moderation.stage != moderationStageInFlight {
		t.Fatalf("the flood disturbed the in-flight ban: stage %v", model.moderation.stage)
	}

	model = runModerationCmd(t, model, cmd)

	for index, key := range keys {
		state := model.chats.stateForKey(key)
		var spamVisible bool
		for _, message := range state.messages {
			if strings.Contains(message.Text, "buy followers") {
				spamVisible = true
			}
		}
		for _, message := range state.activeMessages {
			if strings.Contains(message.Text, "buy followers") {
				spamVisible = true
			}
		}
		if key == armedKey {
			if spamVisible {
				t.Fatalf("the ban left the spam on screen in the chat it was armed in")
			}
			if len(state.moderations) != 1 {
				t.Fatalf("the armed chat recorded %d moderations, want 1", len(state.moderations))
			}
			continue
		}
		if !spamVisible {
			t.Fatalf("a ban in %q also blanked chat %q (index %d)", armedKey, key, index)
		}
		if len(state.moderations) != 0 {
			t.Fatalf("chat %q recorded a moderation that belonged to %q", key, armedKey)
		}
	}

	assertRectangularFrame(t, model, "after moderating during a multi-chat flood")
}

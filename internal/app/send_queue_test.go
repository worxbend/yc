package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/youtube"
)

// newSendModel returns a model wired to a fake client and ready to compose.
func newSendModel(t *testing.T) (shellModel, *FakeChatClient) {
	t.Helper()
	model := newModelForTest(t, "demo")
	client := NewFakeChatClient()
	t.Cleanup(func() { _ = client.Close() })
	model.client = client
	model.identity = youtube.Identity{ChannelID: "UC-me", DisplayName: "Me"}
	model.activeChatState().target.LiveChatID = "live-chat"
	model.focus = focusComposer
	return model, client
}

// submit types a draft and presses enter, returning the model and the command
// the send produced.
func submit(t *testing.T, model shellModel, text string) (shellModel, tea.Cmd) {
	t.Helper()
	model.insertComposerText(text)
	next, cmd := model.Update(key(tea.KeyEnter))
	return next.(shellModel), cmd
}

// Sends are serialized per chat so the queued order is the order YouTube sees,
// which is what a moderator issuing a sequence expects. Dispatching them
// concurrently would let the second land first.
func TestSendQueueSerializesAndPreservesOrder(t *testing.T) {
	model, client := newSendModel(t)

	// Three sends with nothing completing in between: only the first may be
	// in flight, the rest queue behind it.
	var first tea.Cmd
	for i, text := range []string{"one", "two", "three"} {
		next, cmd := submit(t, model, text)
		model = next
		if i == 0 {
			if cmd == nil {
				t.Fatal("the first send produced no command")
			}
			first = cmd
			continue
		}
		if cmd != nil {
			t.Fatalf("send %d dispatched while another was in flight", i)
		}
	}

	state := model.activeChatState()
	if len(state.sendQueue) != 2 {
		t.Fatalf("queue holds %d, want the two sends waiting behind the first", len(state.sendQueue))
	}
	if state.activeSend == nil || state.activeSend.Text != "one" {
		t.Fatalf("active send = %+v, want the first submission", state.activeSend)
	}
	if state.sendQueue[0].Text != "two" || state.sendQueue[1].Text != "three" {
		t.Fatalf("queue order = %q/%q, want two then three", state.sendQueue[0].Text, state.sendQueue[1].Text)
	}

	// Drain: each completion releases exactly the next one, in order.
	completion := first()
	for range 3 {
		next, cmd := model.Update(completion)
		model = next.(shellModel)
		if cmd == nil {
			break
		}
		completion = cmd()
	}

	sent := client.SentRequests()
	if len(sent) != 3 {
		t.Fatalf("dispatched %d sends, want 3", len(sent))
	}
	for i, want := range []string{"one", "two", "three"} {
		if sent[i].Text != want {
			t.Errorf("send %d = %q, want %q; the queue reordered", i, sent[i].Text, want)
		}
	}
	if got := model.activeChatState().sendQueue; len(got) != 0 {
		t.Errorf("queue still holds %d after draining", len(got))
	}
}

// A rate limit is feedback, not a failure: the draft is gone (it was accepted
// into the queue) but the user has to be told when to retry.
func TestRateLimitedSendReportsWhenToRetry(t *testing.T) {
	model, client := newSendModel(t)
	client.QueueSendResult(youtube.SendResult{
		RateLimited: true,
		RetryAfter:  12 * time.Second,
	}, nil)

	model, cmd := submit(t, model, "too fast")
	if cmd == nil {
		t.Fatal("the send produced no command")
	}
	next, _ := model.Update(cmd())
	model = next.(shellModel)

	state := model.activeChatState()
	if state.sendState != composerSendRateLimited {
		t.Fatalf("sendState = %q, want %q", state.sendState, composerSendRateLimited)
	}
	if !strings.Contains(state.sendFeedback, "rate limited") {
		t.Errorf("feedback = %q, want it to name the rate limit", state.sendFeedback)
	}
	if !strings.Contains(state.sendFeedback, "12s") {
		t.Errorf("feedback = %q, want it to say when to retry", state.sendFeedback)
	}
	// A rate-limited send never reached YouTube, so it must not be echoed as
	// though it had.
	for _, message := range state.messages {
		if message.LocalEcho {
			t.Error("a rate-limited send was echoed locally")
		}
	}
}

// sendResultDetail is what the status line reads, so every result shape has to
// produce a sentence rather than an empty cell.
func TestSendResultDetailAlwaysSaysSomething(t *testing.T) {
	cases := []struct {
		name   string
		result youtube.SendResult
		want   string
	}{
		{"accepted", youtube.SendResult{MessageID: "m1"}, "sent"},
		{"explicit detail wins", youtube.SendResult{Detail: "queued behind a slow mode"}, "queued behind a slow mode"},
		{"rate limited with a delay", youtube.SendResult{RateLimited: true, RetryAfter: 90 * time.Second}, "retry in 1m30s"},
		{"rate limited without one", youtube.SendResult{RateLimited: true}, "rate limited"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sendResultDetail(tc.result)
			if !strings.Contains(got, tc.want) {
				t.Errorf("sendResultDetail = %q, want it to contain %q", got, tc.want)
			}
			if strings.TrimSpace(got) == "" {
				t.Error("the status line would show an empty send result")
			}
		})
	}
}

// A failed send returns the text so the user can retry without retyping.
//
// The status line is a second line of defense, not the first: transport errors
// are redacted at their own boundary, and this layer can only catch what a
// credential looks like. So the fixture is credential-shaped - the forms the
// pattern redactor exists to catch - rather than an opaque string no layer here
// could recognize.
func TestFailedSendRestoresTheDraftAndRedactsCredentialShapedText(t *testing.T) {
	model, client := newSendModel(t)
	leaky := "insert failed: Authorization: Bearer ya29.a0LEAKED " +
		"access_token=ya29.anotherLEAK key=AIzaSyLEAK00000000000000000000000000000"
	client.QueueSendResult(youtube.SendResult{}, errors.New(leaky))

	model, cmd := submit(t, model, "a message worth keeping")
	if cmd == nil {
		t.Fatal("the send produced no command")
	}
	next, _ := model.Update(cmd())
	model = next.(shellModel)

	state := model.activeChatState()
	if state.sendState != composerSendFailed {
		t.Fatalf("sendState = %q, want %q", state.sendState, composerSendFailed)
	}
	if state.composerText != "a message worth keeping" {
		t.Errorf("composer = %q; a failed send must give the text back", state.composerText)
	}
	for _, secret := range []string{"ya29.a0LEAKED", "ya29.anotherLEAK", "AIzaSyLEAK00000000000000000000000000000"} {
		if strings.Contains(state.sendFeedback, secret) {
			t.Fatalf("the send failure leaked %q into the status line: %q", secret, state.sendFeedback)
		}
	}
	if strings.TrimSpace(state.sendFeedback) == "" {
		t.Error("a failed send gave no reason")
	}
}

// A completion that lands after the user switched chats must update the chat it
// belonged to, not whichever one happens to be in front.
func TestSendCompletionFindsItsOwnChatAfterASwitch(t *testing.T) {
	model := newModelForTest(t, "one", "two")
	client := NewFakeChatClient()
	t.Cleanup(func() { _ = client.Close() })
	model.client = client
	model.identity = youtube.Identity{ChannelID: "UC-me", DisplayName: "Me"}

	keys := model.chats.chatKeys()
	model.chats.stateForKey(keys[0]).target.LiveChatID = "chat-one"
	model.chats.stateForKey(keys[1]).target.LiveChatID = "chat-two"

	model.focus = focusComposer
	model, cmd := submit(t, model, "sent from the first chat")
	if cmd == nil {
		t.Fatal("the send produced no command")
	}

	// Switch away before the completion arrives. Leaving the composer first is
	// required: "]" inside it is literal text, not a chat switch.
	model = press(t, model, key(tea.KeyEsc))
	model = press(t, model, runeKey(']'))
	if model.activeChatKey() == keys[0] {
		t.Fatal("the test did not actually switch chats")
	}

	next, _ := model.Update(cmd())
	model = next.(shellModel)

	origin := model.chats.stateForKey(keys[0])
	if origin.sendState != composerSendSucceeded {
		t.Errorf("the originating chat's send state = %q, want %q", origin.sendState, composerSendSucceeded)
	}
	if len(origin.messages) != 1 || !origin.messages[0].LocalEcho {
		t.Errorf("the echo did not land in the originating chat: %+v", origin.messages)
	}
	if other := model.chats.stateForKey(keys[1]); len(other.messages) != 0 {
		t.Errorf("the echo landed in the wrong chat: %+v", other.messages)
	}
}

// A source that cannot send must say so rather than queueing forever.
func TestSendWithoutAClientReportsRatherThanQueueing(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.client = nil
	model.focus = focusComposer

	model, cmd := submit(t, model, "into the void")
	if cmd != nil {
		t.Error("a model with no client produced a send command")
	}
	state := model.activeChatState()
	if state.sendState != composerSendFailed {
		t.Errorf("sendState = %q, want %q", state.sendState, composerSendFailed)
	}
	if !strings.Contains(state.sendFeedback, "unavailable") {
		t.Errorf("feedback = %q, want it to say sending is unavailable", state.sendFeedback)
	}
	if len(state.sendQueue) != 0 {
		t.Errorf("queue holds %d sends that can never be dispatched", len(state.sendQueue))
	}
}

// The local echo is replaced by the authoritative copy rather than duplicated:
// YouTube delivers yc's own message back around the poll loop.
func TestLocalEchoIsReplacedByTheRealMessage(t *testing.T) {
	model, client := newSendModel(t)
	client.QueueSendResult(youtube.SendResult{MessageID: "server-id-1"}, nil)

	model, cmd := submit(t, model, "hello")
	next, _ := model.Update(cmd())
	model = next.(shellModel)

	state := model.activeChatState()
	if len(state.messages) != 1 {
		t.Fatalf("history holds %d messages after the echo", len(state.messages))
	}

	// The same ID arriving from the poller must replace, not duplicate.
	authoritative := testMessage(t, "server-id-1", "live-chat", "Me", "hello")
	next, _ = model.Update(chatClientMessageMsg{message: authoritative, ok: true})
	model = next.(shellModel)

	state = model.activeChatState()
	if len(state.messages) != 1 {
		t.Fatalf("history holds %d messages; the echo was duplicated:\n%+v", len(state.messages), state.messages)
	}
	if state.messages[0].LocalEcho {
		t.Error("the authoritative copy did not replace the echo")
	}
}

// A send that outlives its context must not leave the composer stuck in
// "sending" with no way back.
func TestSendCancellationIsReportedNotSwallowed(t *testing.T) {
	model, client := newSendModel(t)
	client.QueueSendResult(youtube.SendResult{}, context.Canceled)

	model, cmd := submit(t, model, "interrupted")
	next, _ := model.Update(cmd())
	model = next.(shellModel)

	state := model.activeChatState()
	if state.sendState == composerSendSending {
		t.Error("a canceled send left the composer stuck in the sending state")
	}
	if state.activeSend != nil {
		t.Error("a canceled send is still marked in flight")
	}
	if strings.TrimSpace(state.sendFeedback) == "" {
		t.Error("a canceled send gave no feedback")
	}
}

// YouTube caps a chat message at 200 characters, and the composer enforces it
// locally so the 50-unit insert is never spent learning that.
func TestComposerEnforcesTheMessageLimitByGraphemes(t *testing.T) {
	model, _ := newSendModel(t)

	// The cap counts runes, matching youtube.MaxChatMessageRunes and the
	// transport's own capGraphemes, but it may never fall inside a cluster: a
	// family emoji is seven runes and cutting it leaves a dangling joiner in
	// the draft, which is then exactly what gets dispatched.
	for range 250 {
		model.insertComposerText("👨‍👩‍👧‍👦")
	}
	limited := model.activeChatState().composerText
	if limited == "" {
		t.Fatal("the composer rejected everything")
	}
	assertComposerWithinCap(t, limited)

	// Adding more must not push it past the cap either.
	model.insertComposerText("more")
	assertComposerWithinCap(t, model.activeChatState().composerText)
}

// assertComposerWithinCap checks the two properties the composer cap owes the
// send path: no more runes than YouTube accepts, and no half a cluster.
func assertComposerWithinCap(t *testing.T, text string) {
	t.Helper()
	if got := len([]rune(text)); got > youtube.MaxChatMessageRunes {
		t.Errorf("composer holds %d runes, past the %d cap", got, youtube.MaxChatMessageRunes)
	}
	if strings.Contains(text, "�") {
		t.Error("the composer limit split a grapheme cluster")
	}
	// A trailing zero-width joiner is the signature of a cluster cut in half.
	if strings.HasSuffix(text, "\u200d") {
		t.Error("the composer limit left a dangling zero-width joiner")
	}
}

// Every send request the model builds must name the chat it belongs to, or the
// message lands in whichever chat the transport defaults to.
func TestEverySendRequestNamesItsChat(t *testing.T) {
	model, client := newSendModel(t)
	for i := range 3 {
		next, cmd := submit(t, model, fmt.Sprintf("message %d", i))
		model = next
		if cmd != nil {
			next, _ := model.Update(cmd())
			model = next.(shellModel)
		}
	}
	for i, request := range client.SentRequests() {
		if request.LiveChatID != "live-chat" {
			t.Errorf("request %d has LiveChatID %q, want the active chat's", i, request.LiveChatID)
		}
	}
}

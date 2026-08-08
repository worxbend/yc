package app

import (
	"context"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/youtube"
)

// TestUnknownChatIDDoesNotLandInTheActiveChat pins the routing rule for an
// event whose identifier matches no open chat.
//
// Two chats opened by video ID both start with an empty LiveChatID. The
// transport resolves a real liveChatId internally and stamps it on the messages
// it emits, well before the shell's own videos.list round trip answers. Falling
// back to the active chat in that window filed one broadcast's messages into
// the other's history, unread count, and mention roster - and which chat won
// was a coin flip on every startup with more than one --chat.
func TestUnknownChatIDDoesNotLandInTheActiveChat(t *testing.T) {
	set := newChatStateSet([]youtube.ChatTarget{
		{Raw: "vidA", Kind: youtube.TargetVideoID, VideoID: "vidA"},
		{Raw: "vidB", Kind: youtube.TargetVideoID, VideoID: "vidB"},
	}, animation.Config{}, animation.SystemClock{}, 100)

	stray := testMessage(t, "m1", "CgAyBcResolvedByTheTransport", "alice", "hello")
	if state := set.stateForChatID(stray.LiveChatID); state != nil {
		t.Fatalf("an unroutable chat ID resolved to chat %q; it must be refused, "+
			"not filed under whichever chat is on screen", state.key)
	}
	if _, ok := set.applyMessage(stray); ok {
		t.Fatal("applyMessage accepted an unroutable message")
	}
	for _, key := range set.order {
		if got := len(set.states[key].messages); got != 0 {
			t.Fatalf("chat %q received %d messages from an unroutable event", key, got)
		}
	}
}

// TestSingleChatStillAcceptsAnUnknownChatID pins the other side: with one chat
// open there is no ambiguity, so a transport that stamps an ID the shell has
// not learned yet must still be delivered rather than dropped.
func TestSingleChatStillAcceptsAnUnknownChatID(t *testing.T) {
	set := newChatStateSet([]youtube.ChatTarget{
		{Raw: "vidA", Kind: youtube.TargetVideoID, VideoID: "vidA"},
	}, animation.Config{}, animation.SystemClock{}, 100)

	message := testMessage(t, "m1", "CgAyBcResolvedByTheTransport", "alice", "hello")
	state, active := set.applyMessage(message)
	if state == nil || !active {
		t.Fatalf("applyMessage(state=%v, active=%v), want the only chat", state, active)
	}
}

// TestForwardedEventsWearTheSessionRoutingKey pins that the live adapter stamps
// its own stable key on every event, which is what makes the shell's lookup
// exact regardless of what the transport resolved.
func TestForwardedEventsWearTheSessionRoutingKey(t *testing.T) {
	transport := newFakeTransport()
	// The transport resolved a liveChatId the shell has never seen.
	transport.target = youtube.ChatTarget{LiveChatID: "CgAyBcResolvedByTheTransport"}

	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) { return transport, nil },
		Targets: []youtube.ChatTarget{{Raw: "vidA", Kind: youtube.TargetVideoID, VideoID: "vidA"}},
	})
	if err != nil {
		t.Fatalf("NewLiveChatClient error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	transport.messages <- youtube.Message{ID: "m1", LiveChatID: "CgAyBcResolvedByTheTransport"}

	select {
	case got := <-client.Messages():
		if got.LiveChatID != "vida" {
			t.Fatalf("forwarded LiveChatID = %q, want the session routing key %q",
				got.LiveChatID, "vida")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the forwarded message")
	}
}

// TestQuotaSurvivesTheLastChatClosing pins that the quota meter keeps reporting
// the real daily limit after every chat is closed. Reporting the zero value
// told the status bar the limit was zero units, on a client whose headline
// constraint is quota.
func TestQuotaSurvivesTheLastChatClosing(t *testing.T) {
	transport := newFakeTransport()
	transport.quota = youtube.QuotaSnapshot{LimitUnits: 10000, UsedUnits: 250, RemainingUnits: 9750}

	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) { return transport, nil },
		Targets: []youtube.ChatTarget{{Raw: "vidA", Kind: youtube.TargetVideoID, VideoID: "vidA"}},
	})
	if err != nil {
		t.Fatalf("NewLiveChatClient error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// Let the session bind its transport.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && client.Quota().LimitUnits == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := client.Quota().LimitUnits; got != 10000 {
		t.Fatalf("LimitUnits while connected = %d, want 10000", got)
	}

	if err := client.CloseChat("vida"); err != nil {
		t.Fatalf("CloseChat error = %v", err)
	}
	snapshot := client.Quota()
	if snapshot.LimitUnits != 10000 || snapshot.UsedUnits != 250 {
		t.Fatalf("Quota after closing the last chat = %+v, want the last live snapshot", snapshot)
	}
}

// TestOpenChatAfterCloseDoesNotStartASession pins that Close is the last word.
// Splitting the closed check from the map insert let a session launch after the
// merged channels were closed, and its first emit panicked the process.
func TestOpenChatAfterCloseDoesNotStartASession(t *testing.T) {
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) { return newFakeTransport(), nil },
	})
	if err != nil {
		t.Fatalf("NewLiveChatClient error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if _, err := client.OpenChat(context.Background(), youtube.ChatTarget{Raw: "vidA", VideoID: "vidA"}); err == nil {
		t.Fatal("OpenChat after Close returned no error")
	}
}

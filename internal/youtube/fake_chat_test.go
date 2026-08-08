package youtube

import (
	"context"
	"testing"
	"time"
)

var fakeStart = time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)

func TestSampleScriptCoversEveryRenderableEventKind(t *testing.T) {
	script := SampleScript("live-chat-1", fakeStart)
	if len(script) == 0 {
		t.Fatal("SampleScript returned nothing")
	}

	seen := map[EventKind]bool{}
	for _, msg := range script {
		seen[msg.Kind] = true
		if msg.LiveChatID != "live-chat-1" {
			t.Fatalf("message %q has LiveChatID %q", msg.ID, msg.LiveChatID)
		}
	}

	// Only the kinds that render as a chat row belong here. Bans,
	// tombstones, and chat-ended reach the moderation and room streams
	// instead, which is where a removal belongs.
	want := []EventKind{
		EventKindText,
		EventKindSuperChat,
		EventKindSuperSticker,
		EventKindNewSponsor,
		EventKindMemberMilestone,
		EventKindMembershipGifting,
		EventKindGiftMembershipReceived,
		EventKindGift,
		EventKindFanFunding,
		EventKindPoll,
		EventKindSponsorOnlyModeStarted,
		EventKindSponsorOnlyModeEnded,
		EventKindUnknown,
	}
	for _, kind := range want {
		if !seen[kind] {
			t.Fatalf("SampleScript is missing %q; the mock run is the CI smoke path and must exercise every kind", kind)
		}
	}
	for _, kind := range []EventKind{EventKindUserBanned, EventKindTombstone, EventKindChatEnded} {
		if seen[kind] {
			t.Fatalf("SampleScript contains %q as a chat row; removals must not be reprinted", kind)
		}
	}
}

func TestSampleScriptIsDeterministic(t *testing.T) {
	first := SampleScript("live-chat-1", fakeStart)
	second := SampleScript("live-chat-1", fakeStart)
	if len(first) != len(second) {
		t.Fatalf("lengths differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Text != second[i].Text || !first[i].Timestamp.Equal(second[i].Timestamp) {
			t.Fatalf("message %d differs between runs", i)
		}
	}
}

func TestFakeChatClientEmitsEveryStreamWithoutNetworkOrCredentials(t *testing.T) {
	fake := NewFakeChatClient(FakeChatConfig{LiveChatID: "live-chat-1", Now: func() time.Time { return fakeStart }})
	t.Cleanup(func() { _ = fake.Close() })

	if err := fake.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	// Emission is synchronous when Interval is zero, but Start hands off to
	// a goroutine; drain by waiting for the buffered channel to fill rather
	// than by sleeping.
	messages := drain(t, fake.Messages(), 1)
	if len(messages) == 0 {
		t.Fatal("no messages emitted")
	}
	if len(drain(t, fake.Moderations(), 1)) == 0 {
		t.Fatal("no moderation events emitted; the mock must exercise the deletion path")
	}
	if len(drain(t, fake.RoomEvents(), 1)) == 0 {
		t.Fatal("no room events emitted")
	}
	if len(drain(t, fake.Polls(), 1)) == 0 {
		t.Fatal("no poll state emitted")
	}
	if len(drain(t, fake.ConnectionStates(), 2)) < 2 {
		t.Fatal("want at least connecting and connected states")
	}
}

func TestFakeChatClientSendEchoesLocally(t *testing.T) {
	fake := NewFakeChatClient(FakeChatConfig{Now: func() time.Time { return fakeStart }})
	t.Cleanup(func() { _ = fake.Close() })

	result, err := fake.Send(context.Background(), SendRequest{Text: "hello from the mock"})
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if result.MessageID == "" {
		t.Fatal("MessageID is empty")
	}

	var echo Message
	for _, msg := range drain(t, fake.Messages(), 1) {
		if msg.ID == result.MessageID {
			echo = msg
		}
	}
	if echo.ID == "" {
		t.Fatal("the sent message was not echoed onto the message stream")
	}
	if !echo.LocalEcho {
		t.Fatal("LocalEcho = false; YouTube echoes sent messages back, so the app must be able to reconcile by ID")
	}
}

func TestFakeChatClientQuotaIsLabelledAnEstimate(t *testing.T) {
	fake := NewFakeChatClient(FakeChatConfig{Now: func() time.Time { return fakeStart }})
	t.Cleanup(func() { _ = fake.Close() })

	snapshot := fake.Quota()
	if !snapshot.Estimated {
		t.Fatal("Estimated = false; Google publishes no live chat quota costs, and the mock must not imply otherwise")
	}
	if snapshot.LimitUnits <= 0 || snapshot.RemainingUnits <= 0 {
		t.Fatalf("snapshot = %#v, want a plausible ledger", snapshot)
	}
}

func TestFakeChatClientCloseIsIdempotentAndClosesEveryStream(t *testing.T) {
	fake := NewFakeChatClient(FakeChatConfig{Now: func() time.Time { return fakeStart }})
	if err := fake.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if err := fake.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if err := fake.Close(); err != nil {
		t.Fatalf("second Close error = %v", err)
	}

	for range fake.Messages() {
	}
	for range fake.ConnectionStates() {
	}
	for range fake.Moderations() {
	}
	for range fake.RoomEvents() {
	}
	for range fake.Polls() {
	}
}

// drain collects everything currently buffered on a channel, waiting only for
// the first want items so the test is deterministic without a wall-clock sleep.
func drain[T any](t *testing.T, ch <-chan T, want int) []T {
	t.Helper()
	var collected []T
	deadline := time.After(2 * time.Second)
	for len(collected) < want {
		select {
		case value, ok := <-ch:
			if !ok {
				return collected
			}
			collected = append(collected, value)
		case <-deadline:
			return collected
		}
	}
	for {
		select {
		case value, ok := <-ch:
			if !ok {
				return collected
			}
			collected = append(collected, value)
		default:
			return collected
		}
	}
}

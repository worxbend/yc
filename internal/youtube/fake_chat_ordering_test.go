package youtube

import (
	"context"
	"testing"
	"time"
)

// A tombstone must never be delivered before the message it deletes.
//
// The mock used to flush the whole moderation stream before pacing out any
// messages, so the deletion for the sample script's first message arrived
// while the consumer had no row to redact. A consumer that applies deletions
// by ID then had nothing to match, and the "removed" text was rendered anyway
// - the one outcome the moderation design says must never happen.
func TestFakeChatEmitsAMessageBeforeItsModeration(t *testing.T) {
	fake := NewFakeChatClient(FakeChatConfig{LiveChatID: "fake-chat"})

	// Item index at which each message ID first reaches the message stream.
	firstSeenAt := make(map[string]int)
	for index, result := range fake.script {
		for _, message := range result.Messages {
			if _, ok := firstSeenAt[message.ID]; !ok {
				firstSeenAt[message.ID] = index
			}
		}
	}

	targeted := 0
	for index, result := range fake.script {
		for _, event := range result.Moderations {
			if event.TargetMessageID == "" {
				continue
			}
			targeted++
			messageIndex, ok := firstSeenAt[event.TargetMessageID]
			if !ok {
				t.Fatalf("moderation for %q targets a message the script never emits", event.TargetMessageID)
			}
			if messageIndex > index {
				t.Fatalf("moderation for %q is emitted at item %d, before the message at item %d",
					event.TargetMessageID, index, messageIndex)
			}
		}
	}
	if targeted == 0 {
		t.Fatal("the sample script no longer covers a message-targeting moderation event")
	}
}

// The same ordering must hold in what actually reaches the streams, not only
// in the script the emitter walks.
func TestFakeChatDeliversTheDeletedMessageFirst(t *testing.T) {
	fake := NewFakeChatClient(FakeChatConfig{LiveChatID: "fake-chat"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fake.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = fake.Close() })

	// How much the script will emit, so the reads below can block for exactly
	// that much instead of racing the emitter goroutine with a non-blocking
	// poll that would pass vacuously.
	var wantMessages, wantModerations int
	for _, result := range fake.script {
		wantMessages += len(result.Messages)
		wantModerations += len(result.Moderations)
	}

	deadline := time.After(5 * time.Second)
	delivered := make(map[string]bool, wantMessages)
	for range wantMessages {
		select {
		case message := <-fake.Messages():
			delivered[message.ID] = true
		case <-deadline:
			t.Fatal("timed out draining the message stream")
		}
	}

	checked := 0
	for range wantModerations {
		select {
		case event := <-fake.Moderations():
			if event.TargetMessageID == "" {
				continue
			}
			checked++
			if !delivered[event.TargetMessageID] {
				t.Fatalf("moderation for %q was delivered before the message itself", event.TargetMessageID)
			}
		case <-deadline:
			t.Fatal("timed out draining the moderation stream")
		}
	}
	if checked == 0 {
		t.Fatal("no message-targeting moderation event reached the stream")
	}
}

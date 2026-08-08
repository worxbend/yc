package app

import (
	"context"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/youtube"
)

// TestMockChatClientNeedsNoCredentials is the guard on the primary demo path:
// `yc chat --mock` must drive the full UI with no account and no network.
func TestMockChatClientCoversEveryHighSignalEvent(t *testing.T) {
	client := newMockChatClient("demo", time.Millisecond)
	defer client.Close()

	seen := map[youtube.EventKind]bool{}
	deadline := time.After(10 * time.Second)
	want := []youtube.EventKind{
		youtube.EventKindText,
		youtube.EventKindSuperChat,
		youtube.EventKindSuperSticker,
		youtube.EventKindNewSponsor,
		youtube.EventKindMemberMilestone,
		youtube.EventKindMembershipGifting,
		youtube.EventKindGiftMembershipReceived,
		youtube.EventKindGift,
		youtube.EventKindUnknown,
		youtube.EventKindChatEnded,
	}

	for {
		complete := true
		for _, kind := range want {
			if !seen[kind] {
				complete = false
				break
			}
		}
		if complete {
			return
		}
		select {
		case message := <-client.Messages():
			if message.LiveChatID != mockLiveChatID {
				t.Fatalf("LiveChatID = %q, want the mock chat", message.LiveChatID)
			}
			seen[message.Kind] = true
		case <-client.Moderations():
		case <-client.RoomEvents():
		case <-client.ConnectionStates():
		case <-deadline:
			t.Fatalf("the mock script did not cover every event kind; saw %v", seen)
		}
	}
}

func TestMockChatClientSendEchoesAndQuotaIsLabelledAnEstimate(t *testing.T) {
	client := NewMockChatClient("demo")
	defer client.Close()

	result, err := client.Send(context.Background(), youtube.SendRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.MessageID == "" {
		t.Fatal("a mock send must still return an ID for the local echo")
	}
	snapshot := client.Quota()
	if !snapshot.Estimated {
		t.Fatal("every quota figure yc shows is an estimate and must say so")
	}
	if snapshot.LimitUnits == 0 {
		t.Fatal("the simulated ledger should populate the status bar")
	}
}

func TestMockChatClientCloseIsIdempotent(t *testing.T) {
	client := NewMockChatClient("demo")
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

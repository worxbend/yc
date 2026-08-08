package youtube

import (
	"context"
	"testing"
	"time"
)

// Close must be the last word regardless of the order it races Reconnect in.
// Reconnect releases the poller lock to drain the previous session, and a
// session started after Close has closed the five streams would emit onto a
// closed channel and take the process down with it.
func TestReconnectAfterCloseDoesNotStartASession(t *testing.T) {
	client, err := NewClient(ClientConfig{
		Credentials: StaticCredentials{Key: "test-not-a-real-key"},
		Endpoint:    "https://127.0.0.1:1/youtube/v3",
		Timeout:     time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	poller, err := NewPoller(PollerConfig{
		Client: client,
		Target: ChatTarget{Kind: TargetLiveChatID, LiveChatID: "test-chat", Raw: "test-chat"},
		Sleep:  func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	if err := poller.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// launch is what a lost race would reach; calling it directly is the only
	// deterministic way to pin the guard.
	poller.launch(context.Background(), "")

	select {
	case _, open := <-poller.Messages():
		if open {
			t.Fatal("a session started after Close emitted a message")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("the message stream should already be closed")
	}
	if state := poller.State(); state != PollerClosed {
		t.Errorf("State() = %s, want %s", state, PollerClosed)
	}
}

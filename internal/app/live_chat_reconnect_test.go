package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/youtube"
)

// reconnectableTransport is a fake transport that can restart itself, which is
// what a real youtube.Poller does.
type reconnectableTransport struct {
	*fakeTransport

	mu         sync.Mutex
	reconnects int
	err        error
}

func newReconnectableTransport() *reconnectableTransport {
	return &reconnectableTransport{fakeTransport: newFakeTransport()}
}

// Reconnect records the attempt and returns the configured outcome. A real
// in-place restart leaves the streams open, and so does this one.
func (r *reconnectableTransport) Reconnect(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reconnects++
	return r.err
}

func (r *reconnectableTransport) reconnectCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reconnects
}

func (r *reconnectableTransport) isClosed() bool {
	r.fakeTransport.mu.Lock()
	defer r.fakeTransport.mu.Unlock()
	return r.closed
}

// TestReconnectRestartsTheTransportInPlaceWhenItCan pins the cheap path.
//
// Rebuilding the transport throws away the continuation token, the resolved
// target, and the dedupe memory, so ctrl+r would spend a resolve unit, prime
// from the head of the chat, and reprint the last few minutes for the viewer. A
// transport that can restart itself is asked to instead, and is left open.
func TestReconnectRestartsTheTransportInPlaceWhenItCan(t *testing.T) {
	var mu sync.Mutex
	built := 0
	transport := newReconnectableTransport()

	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) {
			mu.Lock()
			built++
			mu.Unlock()
			return transport, nil
		},
		Targets: []youtube.ChatTarget{{Raw: "demo", LiveChatID: "live-chat"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return built == 1
	})

	if err := client.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	waitFor(t, func() bool { return transport.reconnectCount() == 1 })

	if transport.isClosed() {
		t.Fatal("the transport was closed even though it restarted itself")
	}
	mu.Lock()
	rebuilds := built
	mu.Unlock()
	if rebuilds != 1 {
		t.Fatalf("the factory ran %d times; an in-place restart must not rebuild the transport", rebuilds)
	}

	// The fan-in must have survived the restart, or the chat is silent until
	// something else tears the session down.
	transport.messages <- youtube.Message{ID: "after-reconnect"}
	select {
	case message := <-client.Messages():
		if message.ID != "after-reconnect" {
			t.Fatalf("message = %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the fan-in did not survive an in-place restart")
	}
}

// TestReconnectRebuildsWhenTheTransportCannotRestartItself pins the fallback,
// so a transport whose restart fails still gets the user back onto a live chat.
func TestReconnectRebuildsWhenTheTransportCannotRestartItself(t *testing.T) {
	var mu sync.Mutex
	built := 0
	first := newReconnectableTransport()
	first.err = errors.New("restart refused")

	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) {
			mu.Lock()
			built++
			count := built
			mu.Unlock()
			if count == 1 {
				return first, nil
			}
			return newFakeTransport(), nil
		},
		Targets: []youtube.ChatTarget{{Raw: "demo", LiveChatID: "live-chat"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return built == 1
	})
	if err := client.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return built >= 2
	})
	if !first.isClosed() {
		t.Fatal("the transport that refused to restart was left open")
	}
	if got := first.reconnectCount(); got != 1 {
		t.Fatalf("in-place restarts attempted = %d, want 1 before the rebuild", got)
	}
}

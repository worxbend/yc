package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/youtube"
)

// fakeTransport is a controllable LiveChatTransport, so the reconnect ladder
// and the fan-in are testable without a network or a wall-clock sleep.
type fakeTransport struct {
	mu       sync.Mutex
	messages chan youtube.Message
	states   chan youtube.ConnectionState
	mods     chan youtube.ModerationEvent
	rooms    chan youtube.RoomEvent
	polls    chan youtube.PollState

	startErr error
	// target is what the transport reports it resolved, which the session
	// adopts so a reconnect does not re-resolve the same chat.
	target  youtube.ChatTarget
	sends   []youtube.SendRequest
	quota   quota.Snapshot
	closed  bool
	started bool
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		messages: make(chan youtube.Message, 16),
		states:   make(chan youtube.ConnectionState, 16),
		mods:     make(chan youtube.ModerationEvent, 16),
		rooms:    make(chan youtube.RoomEvent, 16),
		polls:    make(chan youtube.PollState, 16),
	}
}

func (f *fakeTransport) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	return f.startErr
}

func (f *fakeTransport) Target() youtube.ChatTarget {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.target
}

func (f *fakeTransport) Messages() <-chan youtube.Message                 { return f.messages }
func (f *fakeTransport) ConnectionStates() <-chan youtube.ConnectionState { return f.states }
func (f *fakeTransport) Moderations() <-chan youtube.ModerationEvent      { return f.mods }
func (f *fakeTransport) RoomEvents() <-chan youtube.RoomEvent             { return f.rooms }
func (f *fakeTransport) Polls() <-chan youtube.PollState                  { return f.polls }
func (f *fakeTransport) Quota() quota.Snapshot                            { return f.quota }

func (f *fakeTransport) Send(_ context.Context, request youtube.SendRequest) (youtube.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, request)
	return youtube.SendResult{MessageID: "sent-1"}, nil
}

func (f *fakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	close(f.messages)
	close(f.states)
	close(f.mods)
	close(f.rooms)
	close(f.polls)
	return nil
}

func (f *fakeTransport) sentRequests() []youtube.SendRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]youtube.SendRequest, len(f.sends))
	copy(out, f.sends)
	return out
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before the deadline")
}

func TestLiveChatClientRequiresAFactory(t *testing.T) {
	if _, err := NewLiveChatClient(LiveChatConfig{}); err == nil {
		t.Fatal("a client with no transport factory must not be constructed")
	}
}

func TestLiveChatClientFansStreamsIn(t *testing.T) {
	transport := newFakeTransport()
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) { return transport, nil },
		Targets: []youtube.ChatTarget{{Raw: "demo", LiveChatID: "live-chat"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	transport.messages <- youtube.Message{ID: "m1", Text: "hello"}
	transport.mods <- youtube.ModerationEvent{Type: youtube.ModerationTombstone, TargetMessageID: "m1"}
	transport.rooms <- youtube.RoomEvent{Type: youtube.RoomChatEnded}
	transport.polls <- youtube.PollState{MessageID: "p1", Question: "which game?"}

	select {
	case message := <-client.Messages():
		if message.ID != "m1" {
			t.Fatalf("message = %+v", message)
		}
		if message.LiveChatID != "live-chat" {
			t.Fatalf("LiveChatID = %q; the adapter must stamp the chat it came from", message.LiveChatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message arrived")
	}
	select {
	case event := <-client.Moderations():
		if event.TargetMessageID != "m1" {
			t.Fatalf("moderation = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no moderation event arrived")
	}
	select {
	case event := <-client.RoomEvents():
		if event.Type != youtube.RoomChatEnded {
			t.Fatalf("room event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no room event arrived")
	}
	select {
	case poll := <-client.Polls():
		if poll.MessageID != "p1" {
			t.Fatalf("poll = %+v", poll)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no poll arrived")
	}
}

func TestLiveChatClientRoutesSendsToTheOwningChat(t *testing.T) {
	first := newFakeTransport()
	second := newFakeTransport()
	transports := map[string]*fakeTransport{"one": first, "two": second}

	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(target youtube.ChatTarget) (LiveChatTransport, error) {
			return transports[target.Raw], nil
		},
		Targets: []youtube.ChatTarget{
			{Raw: "one", LiveChatID: "chat-one"},
			{Raw: "two", LiveChatID: "chat-two"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	waitFor(t, func() bool {
		second.mu.Lock()
		defer second.mu.Unlock()
		return second.started
	})

	if _, err := client.Send(context.Background(), youtube.SendRequest{LiveChatID: "chat-two", Text: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(second.sentRequests()) != 1 {
		t.Fatal("the send did not reach the chat it named")
	}
	if len(first.sentRequests()) != 0 {
		t.Fatal("the send leaked into another chat")
	}
}

func TestLiveChatClientRebuildsTheTransportOnReconnect(t *testing.T) {
	var mu sync.Mutex
	built := 0
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) {
			mu.Lock()
			built++
			mu.Unlock()
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
}

func TestLiveChatClientOpenAndCloseChat(t *testing.T) {
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) { return newFakeTransport(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	target := youtube.ChatTarget{Raw: "demo", LiveChatID: "live-chat"}
	if _, err := client.OpenChat(context.Background(), target); err != nil {
		t.Fatalf("OpenChat: %v", err)
	}
	if _, err := client.OpenChat(context.Background(), target); err != nil {
		t.Fatalf("reopening an open chat must be a no-op, got %v", err)
	}
	if err := client.CloseChat(target.Key()); err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	if err := client.CloseChat("not-open"); err != nil {
		t.Fatalf("closing an unknown chat must be a no-op, got %v", err)
	}
}

func TestLiveChatClientCloseIsIdempotent(t *testing.T) {
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) { return newFakeTransport(), nil },
		Targets: []youtube.ChatTarget{{Raw: "demo", LiveChatID: "live-chat"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, ok := <-client.Messages(); ok {
		t.Fatal("the merged message stream should be closed")
	}
}

func TestLiveChatClientReconnectWithNoSessionsIsUnavailable(t *testing.T) {
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) { return newFakeTransport(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Reconnect(context.Background()); !errors.Is(err, ErrReconnectUnavailable) {
		t.Fatalf("err = %v, want ErrReconnectUnavailable", err)
	}
}

func TestFakeChatClientCountsDroppedMessages(t *testing.T) {
	client := NewFakeChatClient()
	defer client.Close()
	for i := 0; i < fakeChatBuffer+5; i++ {
		client.FeedMessage(youtube.Message{ID: "m"})
	}
	if got := client.DroppedMessages(); got != 5 {
		t.Fatalf("dropped = %d, want 5", got)
	}
}

func TestFakeChatClientCloseIsIdempotent(t *testing.T) {
	client := NewFakeChatClient()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Send(context.Background(), youtube.SendRequest{}); !errors.Is(err, ErrFakeChatClientClosed) {
		t.Fatalf("err = %v, want ErrFakeChatClientClosed", err)
	}
}

// parkedFactory blocks the run loop inside the transport factory, which is the
// one moment when a session has been started but has published nothing for
// stop() or restart() to act on. Both regression tests below drive that window
// deliberately; before the publish handshake existed, landing in it left the
// session fanning in a transport nobody would ever close.
func parkedFactory() (factory func(youtube.ChatTarget) (LiveChatTransport, error), entered <-chan struct{}, release func()) {
	in := make(chan struct{}, 1)
	gate := make(chan struct{})
	var once sync.Once
	return func(youtube.ChatTarget) (LiveChatTransport, error) {
		select {
		case in <- struct{}{}:
		default:
		}
		<-gate
		return newFakeTransport(), nil
	}, in, func() { once.Do(func() { close(gate) }) }
}

func TestLiveChatClientCloseDuringInitialConnectDoesNotHang(t *testing.T) {
	factory, entered, release := parkedFactory()
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: factory,
		Targets: []youtube.ChatTarget{{Raw: "demo", LiveChatID: "live-chat"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	<-entered // the run loop is inside the factory; no transport is published

	closed := make(chan error, 1)
	go func() { closed <- client.Close() }()
	release() // the transport is published while Close is already tearing down

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Close hung: a transport published after stop() was never closed")
	}
}

func TestLiveChatClientReconnectDuringInitialConnectStillRebuilds(t *testing.T) {
	var mu sync.Mutex
	built := 0
	inner, entered, release := parkedFactory()
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(target youtube.ChatTarget) (LiveChatTransport, error) {
			mu.Lock()
			built++
			mu.Unlock()
			return inner(target)
		},
		Targets: []youtube.ChatTarget{{Raw: "demo", LiveChatID: "live-chat"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	<-entered
	if err := client.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	release()

	// A reconnect that arrives before the first transport is published must
	// still produce a replacement rather than being silently dropped.
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return built >= 2
	})
}

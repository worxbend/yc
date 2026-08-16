package app

import (
	"context"
	"errors"
	"sync"

	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/youtube"
)

// Errors a caller can assert on when driving the fake.
var (
	ErrFakeChatClientClosed     = errors.New("fake chat client closed")
	ErrFakeChatClientBufferFull = errors.New("fake chat client buffer full")
)

// fakeChatBuffer is deep enough for a scripted burst and shallow enough that a
// test which forgets to drain fails fast instead of hanging.
const fakeChatBuffer = 256

// FakeChatClient is a fully controllable ChatClient for shell tests: the caller
// feeds messages, connection states, moderation, room events, and polls, and
// queues send and reconnect outcomes.
//
// Nothing happens on a timer, so every test drives the model deterministically
// and no test needs a wall-clock sleep.
type FakeChatClient struct {
	messages    chan youtube.Message
	states      chan youtube.ConnectionState
	moderations chan youtube.ModerationEvent
	rooms       chan youtube.RoomEvent
	polls       chan youtube.PollState

	mu               sync.Mutex
	closed           bool
	sends            []youtube.SendRequest
	results          []fakeSendResult
	reconnects       int
	reconnectResults []error
	dropped          uint64
	quota            quota.Snapshot
}

type fakeSendResult struct {
	result youtube.SendResult
	err    error
}

var (
	_ ChatClient         = (*FakeChatClient)(nil)
	_ ModerationSource   = (*FakeChatClient)(nil)
	_ RoomEventSource    = (*FakeChatClient)(nil)
	_ PollSource         = (*FakeChatClient)(nil)
	_ QuotaReporter      = (*FakeChatClient)(nil)
	_ MessageDropCounter = (*FakeChatClient)(nil)
)

// NewFakeChatClient returns an empty fake client.
func NewFakeChatClient() *FakeChatClient {
	return &FakeChatClient{
		messages:    make(chan youtube.Message, fakeChatBuffer),
		states:      make(chan youtube.ConnectionState, fakeChatBuffer),
		moderations: make(chan youtube.ModerationEvent, fakeChatBuffer),
		rooms:       make(chan youtube.RoomEvent, fakeChatBuffer),
		polls:       make(chan youtube.PollState, fakeChatBuffer),
	}
}

// FeedMessage delivers a message to the model.
func (c *FakeChatClient) FeedMessage(msg youtube.Message) {
	c.feed(func() bool {
		select {
		case c.messages <- msg:
			return true
		default:
			return false
		}
	})
}

// FeedConnectionState delivers a lifecycle transition to the model.
func (c *FakeChatClient) FeedConnectionState(state youtube.ConnectionState) {
	c.feed(func() bool {
		select {
		case c.states <- state:
			return true
		default:
			return false
		}
	})
}

// FeedModeration delivers a moderation event to the model.
func (c *FakeChatClient) FeedModeration(event youtube.ModerationEvent) {
	c.feed(func() bool {
		select {
		case c.moderations <- event:
			return true
		default:
			return false
		}
	})
}

// FeedRoomEvent delivers a chat-wide state change to the model.
func (c *FakeChatClient) FeedRoomEvent(event youtube.RoomEvent) {
	c.feed(func() bool {
		select {
		case c.rooms <- event:
			return true
		default:
			return false
		}
	})
}

// FeedPoll delivers a creator poll to the model.
func (c *FakeChatClient) FeedPoll(poll youtube.PollState) {
	c.feed(func() bool {
		select {
		case c.polls <- poll:
			return true
		default:
			return false
		}
	})
}

// feed applies one non-blocking send, counting a full buffer as a drop so the
// fake exercises the same "dropped=N" path the live transport does.
func (c *FakeChatClient) feed(send func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if !send() {
		c.dropped++
	}
}

// QueueSendResult queues the outcome of the next Send.
func (c *FakeChatClient) QueueSendResult(result youtube.SendResult, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results = append(c.results, fakeSendResult{result: result, err: err})
}

// QueueReconnectError queues the outcome of the next Reconnect.
func (c *FakeChatClient) QueueReconnectError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconnectResults = append(c.reconnectResults, err)
}

// SetQuota sets the snapshot Quota reports.
func (c *FakeChatClient) SetQuota(snapshot quota.Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.quota = snapshot
}

// SentRequests returns every request passed to Send.
func (c *FakeChatClient) SentRequests() []youtube.SendRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	sends := make([]youtube.SendRequest, len(c.sends))
	copy(sends, c.sends)
	return sends
}

// ReconnectCount reports how many times Reconnect was called.
func (c *FakeChatClient) ReconnectCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reconnects
}

// Messages returns the scripted chat message stream.
func (c *FakeChatClient) Messages() <-chan youtube.Message { return c.messages }

// ConnectionStates returns the scripted connection lifecycle stream.
func (c *FakeChatClient) ConnectionStates() <-chan youtube.ConnectionState { return c.states }

// Moderations returns the scripted moderation event stream.
func (c *FakeChatClient) Moderations() <-chan youtube.ModerationEvent { return c.moderations }

// RoomEvents returns the scripted room-wide event stream.
func (c *FakeChatClient) RoomEvents() <-chan youtube.RoomEvent { return c.rooms }

// Polls returns the scripted creator poll stream.
func (c *FakeChatClient) Polls() <-chan youtube.PollState { return c.polls }

// Send records the request and returns the next queued result.
func (c *FakeChatClient) Send(ctx context.Context, request youtube.SendRequest) (youtube.SendResult, error) {
	if err := ctx.Err(); err != nil {
		return youtube.SendResult{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return youtube.SendResult{}, ErrFakeChatClientClosed
	}
	c.sends = append(c.sends, request)
	if len(c.results) == 0 {
		return youtube.SendResult{}, nil
	}
	next := c.results[0]
	c.results = c.results[1:]
	return next.result, next.err
}

// Reconnect records the attempt and returns the next queued outcome.
func (c *FakeChatClient) Reconnect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrFakeChatClientClosed
	}
	c.reconnects++
	if len(c.reconnectResults) == 0 {
		return nil
	}
	err := c.reconnectResults[0]
	c.reconnectResults = c.reconnectResults[1:]
	return err
}

// Quota returns the snapshot set by SetQuota. Like every quota figure in yc it
// is an estimate; here it is simply a fixture.
func (c *FakeChatClient) Quota() quota.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.quota
}

// DroppedMessages reports how many feeds were discarded because the buffer was
// full.
func (c *FakeChatClient) DroppedMessages() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// Closed reports whether Close has been called, so a test can assert that a
// run which owns a client actually released it.
func (c *FakeChatClient) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Close closes every stream. It is safe to call more than once.
func (c *FakeChatClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	close(c.messages)
	close(c.states)
	close(c.moderations)
	close(c.rooms)
	close(c.polls)
	return nil
}

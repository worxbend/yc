package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/worxbend/yc/internal/youtube"
)

// mockChatInterval paces the scripted stream at something a person can read.
const mockChatInterval = 650 * time.Millisecond

// mockLiveChatID is the fixed chat identity the script speaks for. It is a
// visibly fake value so a screenshot of a mock run can never be mistaken for a
// real broadcast.
const mockLiveChatID = "mock-live-chat"

// MockChatClient is the credential-free scripted chat source behind
// `yc chat --mock`. It performs no network and no filesystem work, and it
// exercises every normalized event kind so the full UI can be driven, demoed,
// and smoke-tested without an account.
//
// The script is fixed rather than random: a bug report that says "the third
// Super Chat wraps wrong" has to mean the same thing on someone else's machine.
type MockChatClient struct {
	chatTitle string
	interval  time.Duration

	messages    chan youtube.Message
	states      chan youtube.ConnectionState
	moderations chan youtube.ModerationEvent
	rooms       chan youtube.RoomEvent

	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	closed bool
	sent   int
}

var (
	_ ChatClient       = (*MockChatClient)(nil)
	_ ModerationSource = (*MockChatClient)(nil)
	_ RoomEventSource  = (*MockChatClient)(nil)
	_ QuotaReporter    = (*MockChatClient)(nil)
)

// NewMockChatClient returns a scripted client for the named chat.
func NewMockChatClient(chatName string) *MockChatClient {
	return newMockChatClient(chatName, mockChatInterval)
}

// newMockChatClient is NewMockChatClient with an injectable pace, so a test can
// walk the whole script without eighteen seconds of wall clock.
func newMockChatClient(chatName string, interval time.Duration) *MockChatClient {
	title := strings.TrimSpace(chatName)
	if title == "" {
		title = "demo"
	}
	ctx, cancel := context.WithCancel(context.Background())
	if interval <= 0 {
		interval = mockChatInterval
	}
	client := &MockChatClient{
		chatTitle:   title,
		interval:    interval,
		messages:    make(chan youtube.Message, 64),
		states:      make(chan youtube.ConnectionState, 8),
		moderations: make(chan youtube.ModerationEvent, 8),
		rooms:       make(chan youtube.RoomEvent, 8),
		cancel:      cancel,
		done:        make(chan struct{}),
	}
	go client.play(ctx)
	return client
}

// Messages returns the merged chat message stream.
func (c *MockChatClient) Messages() <-chan youtube.Message { return c.messages }

// ConnectionStates returns the merged connection lifecycle stream.
func (c *MockChatClient) ConnectionStates() <-chan youtube.ConnectionState { return c.states }

// Moderations returns the merged moderation event stream.
func (c *MockChatClient) Moderations() <-chan youtube.ModerationEvent { return c.moderations }

// RoomEvents returns the merged room-wide event stream.
func (c *MockChatClient) RoomEvents() <-chan youtube.RoomEvent { return c.rooms }

// Send echoes the message back on the message stream so the composer round trip
// is exercised without credentials.
func (c *MockChatClient) Send(ctx context.Context, _ youtube.SendRequest) (youtube.SendResult, error) {
	if err := ctx.Err(); err != nil {
		return youtube.SendResult{}, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return youtube.SendResult{}, ErrFakeChatClientClosed
	}
	c.sent++
	id := fmt.Sprintf("mock-sent-%d", c.sent)
	c.mu.Unlock()

	return youtube.SendResult{
		MessageID:  id,
		Detail:     "sent (mock)",
		AcceptedAt: time.Now(),
		QuotaUnits: 50,
	}, nil
}

// Quota returns a plausible simulated ledger so the status bar and the quota tab
// are exercised without credentials. Like every quota figure in yc it is an
// estimate, and in this mode it is not even a measurement.
func (c *MockChatClient) Quota() youtube.QuotaSnapshot {
	now := time.Now()
	return youtube.QuotaSnapshot{
		UsedUnits:         3240,
		LimitUnits:        10000,
		RemainingUnits:    6760,
		SearchUsed:        1,
		SearchLimit:       100,
		ByEndpoint:        map[string]int{"liveChatMessages.list": 3200, "videos.list": 40},
		ResetAt:           now.Add(4*time.Hour + 12*time.Minute),
		EffectiveInterval: 5 * time.Second,
		ServerFloor:       5 * time.Second,
		BudgetFloor:       43 * time.Second,
		Mode:              youtube.QuotaModeLive,
		Estimated:         true,
		At:                now,
	}
}

// PollInterval reports the cadence the simulated transport claims.
func (c *MockChatClient) PollInterval() time.Duration { return 5 * time.Second }

// Close stops the script. It is safe to call more than once.
func (c *MockChatClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	c.cancel()
	<-c.done
	close(c.messages)
	close(c.states)
	close(c.moderations)
	close(c.rooms)
	return nil
}

// play walks the script once and then loops the ordinary chat portion, so a
// demo left running keeps moving without repeating the whole event tour.
func (c *MockChatClient) play(ctx context.Context) {
	defer close(c.done)

	c.emitState(ctx, youtube.ConnectionState{
		Status: youtube.ConnectionConnecting,
		ChatID: mockLiveChatID,
		Detail: "mock source",
		At:     time.Now(),
	})
	c.emitState(ctx, youtube.ConnectionState{
		Status: youtube.ConnectionConnected,
		ChatID: mockLiveChatID,
		Detail: "mock source: no credentials, no network",
		At:     time.Now(),
	})

	script := mockScript(c.chatTitle)
	for cursor := 0; ; cursor++ {
		if cursor >= len(script) {
			// After the full tour, replay only the conversational part so
			// the demo does not keep ending the chat every minute.
			script = mockLoopScript()
			cursor = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.interval):
		}
		script[cursor](c, ctx)
	}
}

// mockLoopScript is what the demo repeats after the tour.
//
// It reopens the connection first because the tour ends by closing the chat,
// and it does so as its own scripted beat rather than immediately after the
// closing message. The close is derived from a message on the message stream
// while the reopen travels on the connection stream, and the shell consumes
// those independently - so emitting both in the same instant is a race the
// close can win, leaving the demo delivering messages into a chat it says has
// ended.
func mockLoopScript() []mockStep {
	reopen := func(c *MockChatClient, ctx context.Context) {
		c.emitState(ctx, youtube.ConnectionState{
			Status: youtube.ConnectionConnected,
			ChatID: mockLiveChatID,
			Detail: "mock source: replaying the conversation",
			At:     time.Now(),
		})
	}
	return append([]mockStep{reopen}, mockChatterScript()...)
}

func (c *MockChatClient) emit(ctx context.Context, message youtube.Message) {
	message.LiveChatID = mockLiveChatID
	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now()
	}
	if len(message.Badges) == 0 {
		message.Badges = youtube.BadgesForAuthor(message.Author)
	}
	select {
	case c.messages <- message:
	case <-ctx.Done():
	}
}

func (c *MockChatClient) emitState(ctx context.Context, state youtube.ConnectionState) {
	select {
	case c.states <- state:
	case <-ctx.Done():
	}
}

func (c *MockChatClient) emitModeration(ctx context.Context, event youtube.ModerationEvent) {
	event.LiveChatID = mockLiveChatID
	if event.At.IsZero() {
		event.At = time.Now()
	}
	select {
	case c.moderations <- event:
	case <-ctx.Done():
	}
}

func (c *MockChatClient) emitRoom(ctx context.Context, event youtube.RoomEvent) {
	event.LiveChatID = mockLiveChatID
	if event.At.IsZero() {
		event.At = time.Now()
	}
	select {
	case c.rooms <- event:
	case <-ctx.Done():
	}
}

// mockStep is one scripted beat.
type mockStep func(c *MockChatClient, ctx context.Context)

func mockAuthor(name, id string) youtube.Author {
	return youtube.Author{ChannelID: "UC" + id, DisplayName: name, ChannelURL: "https://www.youtube.com/channel/UC" + id}
}

func mockChat(author youtube.Author, id, text string) youtube.Message {
	return youtube.Message{
		ID:      id,
		Author:  author,
		Text:    text,
		Kind:    youtube.EventKindText,
		Type:    youtube.MessageTypeChat,
		RawType: "textMessageEvent",
	}
}

// mockChatterScript is the conversational loop: ordinary lines, a mention, a
// moderator, and the owner, which is what most of a real chat looks like.
func mockChatterScript() []mockStep {
	nova := mockAuthor("nova_dev", "-nova")
	pixel := mockAuthor("pixelwitch", "-pixel")
	quinn := mockAuthor("quinn", "-quinn")
	mod := mockAuthor("streammod", "-mod")
	mod.IsModerator = true
	mod.IsMember = true
	mod.MemberLevelName = "Regulars"
	mod.MemberMonths = 18
	owner := mockAuthor("The Channel", "-owner")
	owner.IsOwner = true
	owner.IsVerified = true

	return []mockStep{
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, mockChat(nova, "mock-chat-1", "first time catching this live 👋"))
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, mockChat(pixel, "mock-chat-2", "the terminal setup is unreal"))
		},
		func(c *MockChatClient, ctx context.Context) {
			message := mockChat(quinn, "mock-chat-3", "@you does this work over ssh?")
			message.Fragments = []youtube.MessageFragment{
				{Type: youtube.FragmentMention, Text: "@you"},
				{Type: youtube.FragmentText, Text: " does this work over ssh?"},
			}
			c.emit(ctx, message)
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, mockChat(mod, "mock-chat-4", "keep it friendly in here please"))
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, mockChat(owner, "mock-chat-5", "thanks for hanging out, everyone"))
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, mockChat(pixel, "mock-chat-6", "it even does :_channelemoji: fallbacks"))
		},
	}
}

// mockScript is the full tour: conversation plus one of every high-signal
// event, a deletion, a ban, and finally the chat ending.
//
// It deliberately covers the paid tiers rather than one representative amount,
// because tier color and amount-chip width are exactly the things that break
// quietly and are only visible side by side.
func mockScript(chatTitle string) []mockStep {
	steps := append([]mockStep{}, mockChatterScript()...)

	tipper := mockAuthor("bigfan", "-bigfan")
	sticker := mockAuthor("stickerfan", "-sticker")
	newMember := mockAuthor("freshmember", "-fresh")
	milestone := mockAuthor("longtimer", "-long")
	milestone.IsMember = true
	milestone.MemberLevelName = "Regulars"
	milestone.MemberMonths = 24
	gifter := mockAuthor("generous", "-gift")
	troll := mockAuthor("spambot", "-spam")

	amounts := []struct {
		display string
		micros  int64
		tier    int
		comment string
	}{
		{"$2.00", 2_000_000, 1, "small but sincere"},
		{"$10.00", 10_000_000, 4, "loving the quota meter"},
		{"$50.00", 50_000_000, 7, "ship it"},
		{"$500.00", 500_000_000, 10, "for the terminal renaissance"},
	}
	for i, amount := range amounts {
		amount := amount
		index := i
		steps = append(steps, func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, youtube.Message{
				ID:     fmt.Sprintf("mock-superchat-%d", index),
				Author: tipper,
				Text:   amount.comment,
				Kind:   youtube.EventKindSuperChat,
				Type:   youtube.MessageTypePaid,
				SuperChat: &youtube.SuperChatDetails{
					Amount:  youtube.Money{Micros: amount.micros, Currency: "USD", Display: amount.display},
					Tier:    amount.tier,
					Comment: amount.comment,
				},
				RawType: "superChatEvent",
			})
		})
	}

	steps = append(steps,
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, youtube.Message{
				ID:     "mock-supersticker-1",
				Author: sticker,
				Kind:   youtube.EventKindSuperSticker,
				Type:   youtube.MessageTypePaid,
				SuperSticker: &youtube.SuperStickerDetails{
					Amount:    youtube.Money{Micros: 5_000_000, Currency: "EUR", Display: "€5.00"},
					Tier:      3,
					StickerID: "mock-sticker",
					AltText:   "a cat typing furiously",
					Language:  "en",
				},
				RawType: "superStickerEvent",
			})
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, youtube.Message{
				ID:     "mock-newsponsor-1",
				Author: newMember,
				Kind:   youtube.EventKindNewSponsor,
				Type:   youtube.MessageTypeMembership,
				Membership: &youtube.MembershipDetails{
					Kind:      youtube.MembershipNew,
					LevelName: "Regulars",
				},
				RawType: "newSponsorEvent",
			})
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, youtube.Message{
				ID:     "mock-milestone-1",
				Author: milestone,
				Text:   "two years of this nonsense",
				Kind:   youtube.EventKindMemberMilestone,
				Type:   youtube.MessageTypeMembership,
				Membership: &youtube.MembershipDetails{
					Kind:      youtube.MembershipMilestone,
					LevelName: "Regulars",
					Months:    24,
					Comment:   "two years of this nonsense",
				},
				RawType: "memberMilestoneChatEvent",
			})
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, youtube.Message{
				ID:     "mock-gifting-1",
				Author: gifter,
				Kind:   youtube.EventKindMembershipGifting,
				Type:   youtube.MessageTypeMembership,
				Membership: &youtube.MembershipDetails{
					Kind:      youtube.MembershipGifting,
					LevelName: "Regulars",
					GiftCount: 5,
				},
				RawType: "membershipGiftingEvent",
			})
		},
	)

	// A gift drop arrives one receipt per recipient, which is the burst the
	// notifier and the activity column both have to collapse.
	for i, name := range []string{"lurker_one", "lurker_two", "lurker_three", "lurker_four", "lurker_five"} {
		recipient := mockAuthor(name, "-"+name)
		index := i
		steps = append(steps, func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, youtube.Message{
				ID:     fmt.Sprintf("mock-giftreceived-%d", index),
				Author: recipient,
				Kind:   youtube.EventKindGiftMembershipReceived,
				Type:   youtube.MessageTypeMembership,
				Membership: &youtube.MembershipDetails{
					Kind:            youtube.MembershipGiftReceived,
					LevelName:       "Regulars",
					GifterChannelID: gifter.ChannelID,
				},
				RawType: "giftMembershipReceivedEvent",
			})
		})
	}

	steps = append(steps,
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, youtube.Message{
				ID:     "mock-gift-1",
				Author: gifter,
				Kind:   youtube.EventKindGift,
				Type:   youtube.MessageTypePaid,
				Gift: &youtube.GiftDetails{
					Name:            "Confetti",
					Duration:        5 * time.Second,
					AltText:         "a burst of confetti",
					Jewels:          250,
					ComboCount:      3,
					HasVisualEffect: true,
				},
				RawType: "giftEvent",
			})
		},
		// A message the moderator is about to remove, so the deletion has
		// something on screen to act on.
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, mockChat(troll, "mock-removable-1", "buy followers at definitely-not-a-scam dot example"))
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emitModeration(ctx, youtube.ModerationEvent{
				Type:            youtube.ModerationTombstone,
				TargetMessageID: "mock-removable-1",
			})
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, mockChat(troll, "mock-removable-2", "same scam, second attempt"))
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emitModeration(ctx, youtube.ModerationEvent{
				Type:              youtube.ModerationUserTimedOut,
				TargetChannelID:   troll.ChannelID,
				TargetDisplayName: troll.DisplayName,
				Duration:          5 * time.Minute,
			})
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emitRoom(ctx, youtube.RoomEvent{Type: youtube.RoomSponsorOnlyStarted, Detail: "members-only mode on"})
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emitRoom(ctx, youtube.RoomEvent{Type: youtube.RoomSponsorOnlyEnded, Detail: "members-only mode off"})
		},
		// An unrecognized snippet type must render as a row, not a crash.
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, youtube.Message{
				ID:      "mock-unknown-1",
				Author:  mockAuthor("futurefeature", "-future"),
				Text:    "an event kind yc has never seen",
				Kind:    youtube.EventKindUnknown,
				Type:    youtube.MessageTypeUnknown,
				RawType: "someFutureEvent",
			})
		},
		func(c *MockChatClient, ctx context.Context) {
			c.emit(ctx, youtube.Message{
				ID:      "mock-chatended-1",
				Text:    chatTitle + " has ended",
				Kind:    youtube.EventKindChatEnded,
				Type:    youtube.MessageTypeSystem,
				RawType: "chatEndedEvent",
			})
		},
	)
	return steps
}

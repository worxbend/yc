package youtube

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FakeChatConfig configures the deterministic fake chat source.
type FakeChatConfig struct {
	LiveChatID string
	ChatTitle  string
	// Script is the message sequence to emit. Empty uses SampleScript.
	Script []Message
	// Interval paces emission. Zero emits everything immediately, which is
	// what tests want; the mock UI run uses a human-readable pace.
	Interval time.Duration
	// Now is injectable so a fake run produces byte-identical output.
	Now func() time.Time
}

// FakeChatClient is a deterministic chat source with no network and no
// credentials. It backs `yc chat --mock`, which is the primary demo path, the
// bug-report path, and the CI smoke path, and so must exercise every event kind.
type FakeChatClient struct {
	cfg FakeChatConfig

	start sync.Once
	stop  sync.Once
	done  chan struct{}

	messages    chan Message
	states      chan ConnectionState
	moderations chan ModerationEvent
	rooms       chan RoomEvent
	polls       chan PollState

	// closeMu excludes emission while the streams are closed.
	closeMu sync.RWMutex
	closed  bool

	mu       sync.Mutex
	sent     int
	quotaHit int
}

// NewFakeChatClient returns a fake client with defaults applied.
func NewFakeChatClient(cfg FakeChatConfig) *FakeChatClient {
	if cfg.LiveChatID == "" {
		cfg.LiveChatID = "fake-live-chat"
	}
	if cfg.ChatTitle == "" {
		cfg.ChatTitle = "Mock live stream"
	}
	if cfg.Now == nil {
		start := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
		cfg.Now = func() time.Time { return start }
	}

	fake := &FakeChatClient{cfg: cfg, done: make(chan struct{})}
	normalized := fake.normalizedScript()

	// The buffers are sized to hold the whole script so a zero Interval can
	// deliver everything without a reader, which is what the golden tests
	// and the non-interactive smoke run rely on.
	fake.messages = make(chan Message, len(normalized.Messages)+len(cfg.Script)+8)
	fake.states = make(chan ConnectionState, 8)
	fake.moderations = make(chan ModerationEvent, len(normalized.Moderations)+4)
	fake.rooms = make(chan RoomEvent, len(normalized.RoomEvents)+4)
	fake.polls = make(chan PollState, len(normalized.Polls)+4)
	return fake
}

// Start begins emission. It is idempotent, and every stream accessor starts it
// too, so a consumer that only reads Messages still sees a live chat.
func (f *FakeChatClient) Start(ctx context.Context) error {
	f.start.Do(func() { go f.run(ctx) })
	return nil
}

// run emits the script. It is the only writer to the streams, so closing them
// once from Close is safe.
func (f *FakeChatClient) run(ctx context.Context) {
	now := f.cfg.Now()
	f.emitState(ConnectionState{Status: ConnectionConnecting, ChatID: f.cfg.LiveChatID, At: now})
	f.emitState(ConnectionState{Status: ConnectionConnected, ChatID: f.cfg.LiveChatID, Detail: f.cfg.ChatTitle, At: now})

	normalized := f.normalizedScript()
	for _, event := range normalized.RoomEvents {
		f.emitRoom(event)
	}
	for _, event := range normalized.Polls {
		f.emitPoll(event)
	}
	for _, event := range normalized.Moderations {
		f.emitModeration(event)
	}

	for _, message := range normalized.Messages {
		if !f.wait(ctx) {
			return
		}
		f.emitMessage(message)
	}
}

// wait paces emission and reports whether the client is still running.
func (f *FakeChatClient) wait(ctx context.Context) bool {
	if f.cfg.Interval <= 0 {
		select {
		case <-ctx.Done():
			return false
		case <-f.done:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(f.cfg.Interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-f.done:
		return false
	case <-timer.C:
		return true
	}
}

// normalizedScript returns the script as the four normalized streams. An
// explicit Script is used verbatim; otherwise the sample wire items are run
// through the real normalizer, so the mock exercises the same code path a live
// chat does rather than a hand-written parallel one.
func (f *FakeChatClient) normalizedScript() NormalizeResult {
	if len(f.cfg.Script) > 0 {
		return NormalizeResult{Messages: f.cfg.Script}
	}
	var result NormalizeResult
	opts := NormalizeOptions{Now: f.cfg.Now(), SeenMessageIDs: func(string) bool { return true }}
	for _, item := range sampleItems(f.cfg.LiveChatID, f.cfg.Now()) {
		result.append(normalizeItem(item, opts))
	}
	return result
}

// SampleScript returns one representative message for every EventKind,
// including a deliberately unknown snippet type, so the mock run and the golden
// tests both cover the full normalization surface.
//
// Kinds that carry no chat row - bans, tombstones, chat-ended - are absent by
// design: they reach the moderation and room streams instead, which is exactly
// where a deletion belongs. Removed words must not come back as a chat row.
func SampleScript(liveChatID string, start time.Time) []Message {
	var result NormalizeResult
	opts := NormalizeOptions{Now: start, SeenMessageIDs: func(string) bool { return true }}
	for _, item := range sampleItems(liveChatID, start) {
		result.append(normalizeItem(item, opts))
	}
	return result.Messages
}

// sampleAuthors are the fake chatters. They differ in role so the badge column,
// the identity colors, and the role filter all have something to show.
var sampleAuthors = []liveChatAuthorDetails{
	{ChannelID: "UCowner00000000000000000", DisplayName: "Streamer", IsChatOwner: true, IsVerified: true},
	{ChannelID: "UCmod0000000000000000000", DisplayName: "Mod Squad", IsChatModerator: true},
	{ChannelID: "UCmember00000000000000000", DisplayName: "Loyal Member", IsChatSponsor: true},
	{ChannelID: "UCviewer00000000000000000", DisplayName: "Regular Viewer"},
}

// sampleItems returns one wire item per documented snippet.type, plus one type
// yc has deliberately never heard of. Building the mock from wire items rather
// than from normalized values means the fake cannot drift away from the
// normalizer it is supposed to demonstrate.
func sampleItems(liveChatID string, start time.Time) []liveChatMessage {
	author := func(index int) liveChatAuthorDetails {
		picked := sampleAuthors[index%len(sampleAuthors)]
		picked.ChannelURL = "https://www.youtube.com/channel/" + picked.ChannelID
		return picked
	}
	at := func(offset int) string {
		return start.Add(time.Duration(offset) * time.Second).UTC().Format(time.RFC3339)
	}

	items := make([]liveChatMessage, 0, 18)
	add := func(snippetType string, mutate func(*liveChatMessage)) {
		index := len(items)
		item := liveChatMessage{
			ID:            fmt.Sprintf("fake-%02d", index),
			AuthorDetails: author(index),
			Snippet: liveChatSnippet{
				Type:              snippetType,
				LiveChatID:        liveChatID,
				PublishedAt:       at(index),
				HasDisplayContent: true,
			},
		}
		item.Snippet.AuthorChannelID = item.AuthorDetails.ChannelID
		if mutate != nil {
			mutate(&item)
		}
		items = append(items, item)
	}

	add("textMessageEvent", func(item *liveChatMessage) {
		item.Snippet.DisplayMessage = "hey @Streamer this terminal chat is unreal :_wave: 🎉"
		item.Snippet.TextMessageDetails = &struct {
			MessageText string `json:"messageText"`
		}{MessageText: "hey @Streamer this terminal chat is unreal :_wave: 🎉"}
	})
	add("textMessageEvent", func(item *liveChatMessage) {
		item.Snippet.DisplayMessage = "docs are at https://developers.google.com/youtube/v3"
		item.Snippet.TextMessageDetails = &struct {
			MessageText string `json:"messageText"`
		}{MessageText: "docs are at https://developers.google.com/youtube/v3"}
	})
	add("superChatEvent", func(item *liveChatMessage) {
		item.Snippet.DisplayMessage = "keep it up!"
		item.Snippet.SuperChatDetails = &wireSuperChatDetails{
			wireAmountDetails: wireAmountDetails{
				AmountMicros:        "5000000",
				Currency:            "USD",
				AmountDisplayString: "$5.00",
				UserComment:         "keep it up!",
			},
			Tier: 3,
		}
	})
	add("superStickerEvent", func(item *liveChatMessage) {
		item.Snippet.SuperStickerDetails = &wireSuperStickerDetails{
			AmountMicros:        "2000000",
			Currency:            "EUR",
			AmountDisplayString: "€2.00",
			Tier:                2,
		}
		item.Snippet.SuperStickerDetails.SuperStickerMetadata.StickerID = "sticker-01"
		item.Snippet.SuperStickerDetails.SuperStickerMetadata.AltText = "a cat waving a tiny flag"
		item.Snippet.SuperStickerDetails.SuperStickerMetadata.AltTextLanguage = "en"
	})
	add("newSponsorEvent", func(item *liveChatMessage) {
		item.Snippet.DisplayMessage = "welcome to the channel!"
		item.Snippet.NewSponsorDetails = &struct {
			MemberLevelName string `json:"memberLevelName"`
			IsUpgrade       bool   `json:"isUpgrade"`
		}{MemberLevelName: "Supporter"}
	})
	add("memberMilestoneChatEvent", func(item *liveChatMessage) {
		item.Snippet.MemberMilestoneChatDetails = &struct {
			MemberLevelName string `json:"memberLevelName"`
			MemberMonth     int    `json:"memberMonth"`
			UserComment     string `json:"userComment"`
		}{MemberLevelName: "Supporter", MemberMonth: 14, UserComment: "fourteen months!"}
	})
	add("membershipGiftingEvent", func(item *liveChatMessage) {
		item.Snippet.MembershipGiftingDetails = &struct {
			GiftMembershipsCount     int    `json:"giftMembershipsCount"`
			GiftMembershipsLevelName string `json:"giftMembershipsLevelName"`
		}{GiftMembershipsCount: 5, GiftMembershipsLevelName: "Supporter"}
	})
	add("giftMembershipReceivedEvent", func(item *liveChatMessage) {
		item.Snippet.GiftMembershipReceivedDetails = &struct {
			MemberLevelName                      string `json:"memberLevelName"`
			GifterChannelID                      string `json:"gifterChannelId"`
			AssociatedMembershipGiftingMessageID string `json:"associatedMembershipGiftingMessageId"`
		}{MemberLevelName: "Supporter", GifterChannelID: "UCmember00000000000000000", AssociatedMembershipGiftingMessageID: "fake-06"}
	})
	add("giftEvent", func(item *liveChatMessage) {
		item.Snippet.GiftDetails = &wireGiftDetails{
			GiftName:        "Confetti",
			GiftDuration:    "3.5s",
			AltText:         "a burst of confetti",
			Language:        "en",
			JewelsAmount:    120,
			ComboCount:      2,
			HasVisualEffect: true,
		}
	})
	add("fanFundingEvent", func(item *liveChatMessage) {
		item.Snippet.FanFundingEventDetails = &wireAmountDetails{
			AmountMicros:        "1500000",
			Currency:            "GBP",
			AmountDisplayString: "£1.50",
			UserComment:         "a tip from the old days",
		}
	})
	add("pollEvent", func(item *liveChatMessage) {
		item.Snippet.PollDetails = &struct {
			Status   string `json:"status"`
			Metadata struct {
				QuestionText string `json:"questionText"`
				Options      []struct {
					OptionText string `json:"optionText"`
					Tally      string `json:"tally"`
				} `json:"options"`
			} `json:"metadata"`
		}{Status: "active"}
		item.Snippet.PollDetails.Metadata.QuestionText = "which theme next?"
		item.Snippet.PollDetails.Metadata.Options = []struct {
			OptionText string `json:"optionText"`
			Tally      string `json:"tally"`
		}{
			{OptionText: "kanagawa", Tally: "128"},
			{OptionText: "rose-pine", Tally: "97"},
		}
	})
	add("sponsorOnlyModeStartedEvent", nil)
	add("sponsorOnlyModeEndedEvent", nil)
	add("userBannedEvent", func(item *liveChatMessage) {
		item.Snippet.UserBannedDetails = &struct {
			BannedUserDetails  wireChannelProfile `json:"bannedUserDetails"`
			BanType            string             `json:"banType"`
			BanDurationSeconds string             `json:"banDurationSeconds"`
		}{
			BannedUserDetails:  wireChannelProfile{ChannelID: "UCspam0000000000000000000", DisplayName: "Spam Bot"},
			BanType:            "temporary",
			BanDurationSeconds: "300",
		}
	})
	add("tombstone", func(item *liveChatMessage) {
		item.ID = "fake-00"
		item.Snippet.HasDisplayContent = false
	})
	add("thisEventTypeDoesNotExistYet", func(item *liveChatMessage) {
		item.Snippet.DisplayMessage = "something new happened in chat"
	})
	return items
}

// Messages returns the scripted message stream.
func (f *FakeChatClient) Messages() <-chan Message {
	_ = f.Start(context.Background())
	return f.messages
}

// ConnectionStates returns the scripted lifecycle stream.
func (f *FakeChatClient) ConnectionStates() <-chan ConnectionState {
	_ = f.Start(context.Background())
	return f.states
}

// Moderations returns the scripted moderation stream.
func (f *FakeChatClient) Moderations() <-chan ModerationEvent {
	_ = f.Start(context.Background())
	return f.moderations
}

// RoomEvents returns the scripted room-event stream.
func (f *FakeChatClient) RoomEvents() <-chan RoomEvent {
	_ = f.Start(context.Background())
	return f.rooms
}

// Polls returns the scripted creator-poll stream.
func (f *FakeChatClient) Polls() <-chan PollState {
	_ = f.Start(context.Background())
	return f.polls
}

// Send accepts a message and echoes it back on the message stream.
//
// The echo is marked LocalEcho so the app can reconcile it by ID when the real
// row arrives, which is the same reconciliation the live path needs: YouTube
// echoes a sent message back through the poll.
func (f *FakeChatClient) Send(ctx context.Context, request SendRequest) (SendResult, error) {
	if err := ctx.Err(); err != nil {
		return SendResult{}, err
	}
	text := composeSendText(request)
	if text == "" {
		return SendResult{}, newSafeError("nothing to send", ErrMessageRejected)
	}

	f.mu.Lock()
	f.sent++
	id := fmt.Sprintf("fake-echo-%02d", f.sent)
	f.quotaHit += DefaultCostTable().Cost(EndpointMessagesInsert)
	f.mu.Unlock()

	now := f.cfg.Now()
	echo := Message{
		ID:         id,
		LiveChatID: f.cfg.LiveChatID,
		Timestamp:  now,
		Author: Author{
			ChannelID:   "UCyou0000000000000000000",
			DisplayName: "You",
		},
		Text:      text,
		Fragments: SplitFragments(text),
		Kind:      EventKindText,
		Type:      MessageTypeChat,
		LocalEcho: true,
		RawType:   "textMessageEvent",
	}
	echo.Badges = BadgesForAuthor(echo.Author)
	f.emitMessage(echo)

	return SendResult{
		MessageID:  id,
		AcceptedAt: now,
		QuotaUnits: DefaultCostTable().Cost(EndpointMessagesInsert),
	}, nil
}

// Quota returns a plausible simulated ledger so the status bar and the quota tab
// are exercised without credentials.
//
// Estimated is true here for the same reason it is true on the live path:
// Google publishes no quota cost for any live chat method, and a mock that
// implied otherwise would teach the wrong thing.
func (f *FakeChatClient) Quota() QuotaSnapshot {
	f.mu.Lock()
	spent := f.quotaHit
	f.mu.Unlock()

	costs := DefaultCostTable()
	used := 240 + spent
	now := f.cfg.Now()
	return QuotaSnapshot{
		UsedUnits:      used,
		LimitUnits:     10000,
		RemainingUnits: 10000 - used,
		SearchUsed:     1,
		SearchLimit:    100,
		ByEndpoint: map[string]int{
			EndpointMessagesList: 240,
			EndpointVideosList:   costs.Cost(EndpointVideosList),
		},
		ResetAt:           now.Add(6 * time.Hour),
		EffectiveInterval: 5 * time.Second,
		ServerFloor:       5 * time.Second,
		BudgetFloor:       4 * time.Second,
		Mode:              QuotaModeLive,
		Estimated:         true,
		At:                now,
	}
}

// Close stops emission and closes every stream. It is safe to call more than
// once.
//
// The close lock is what makes that safe while the emitter goroutine is still
// running: a send on a closed channel panics no matter how the send is
// guarded, so closing has to exclude emission rather than race it.
func (f *FakeChatClient) Close() error {
	f.stop.Do(func() {
		close(f.done)

		f.closeMu.Lock()
		defer f.closeMu.Unlock()
		select {
		case f.states <- ConnectionState{Status: ConnectionClosed, ChatID: f.cfg.LiveChatID, At: f.cfg.Now()}:
		default:
		}
		f.closed = true
		close(f.messages)
		close(f.states)
		close(f.moderations)
		close(f.rooms)
		close(f.polls)
	})
	return nil
}

// The emitters drop rather than block. A fake that wedges on a slow consumer
// would be a worse test double than one that loses a row, and the live poller
// makes the same choice for the same reason.
func (f *FakeChatClient) emitMessage(message Message) {
	f.closeMu.RLock()
	defer f.closeMu.RUnlock()
	if f.closed {
		return
	}
	select {
	case f.messages <- message:
	default:
	}
}

func (f *FakeChatClient) emitState(state ConnectionState) {
	f.closeMu.RLock()
	defer f.closeMu.RUnlock()
	if f.closed {
		return
	}
	select {
	case f.states <- state:
	default:
	}
}

func (f *FakeChatClient) emitModeration(event ModerationEvent) {
	f.closeMu.RLock()
	defer f.closeMu.RUnlock()
	if f.closed {
		return
	}
	select {
	case f.moderations <- event:
	default:
	}
}

func (f *FakeChatClient) emitRoom(event RoomEvent) {
	f.closeMu.RLock()
	defer f.closeMu.RUnlock()
	if f.closed {
		return
	}
	select {
	case f.rooms <- event:
	default:
	}
}

func (f *FakeChatClient) emitPoll(state PollState) {
	f.closeMu.RLock()
	defer f.closeMu.RUnlock()
	if f.closed {
		return
	}
	select {
	case f.polls <- state:
	default:
	}
}

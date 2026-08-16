package youtube

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readFixtureBytes returns one golden wire item verbatim, for tests that need
// to build a list envelope around it.
func readFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "events", name+".json"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

// loadEventFixture decodes one golden wire item. Fixtures are real API shapes
// rather than hand-built structs so a field rename in the reference shows up
// here rather than in production.
func loadEventFixture(t *testing.T, name string) liveChatMessage {
	t.Helper()
	raw := readFixtureBytes(t, name)
	var item liveChatMessage
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return item
}

var fixtureNow = time.Date(2026, 8, 8, 21, 0, 0, 0, time.UTC)

func TestNormalizeItemClassifiesEveryFixture(t *testing.T) {
	tests := []struct {
		fixture     string
		wantKind    EventKind
		wantType    MessageType
		wantMessage bool
		wantModType ModerationType
		wantRoom    RoomEventType
		wantPoll    bool
	}{
		{fixture: "textMessageEvent", wantKind: EventKindText, wantType: MessageTypeChat, wantMessage: true},
		{fixture: "superChatEvent", wantKind: EventKindSuperChat, wantType: MessageTypePaid, wantMessage: true},
		{fixture: "superStickerEvent", wantKind: EventKindSuperSticker, wantType: MessageTypePaid, wantMessage: true},
		{fixture: "superStickerEventHTMLLanguage", wantKind: EventKindSuperSticker, wantType: MessageTypePaid, wantMessage: true},
		{fixture: "newSponsorEvent", wantKind: EventKindNewSponsor, wantType: MessageTypeMembership, wantMessage: true},
		{fixture: "newSponsorEventUpgrade", wantKind: EventKindNewSponsor, wantType: MessageTypeMembership, wantMessage: true},
		{fixture: "memberMilestoneChatEvent", wantKind: EventKindMemberMilestone, wantType: MessageTypeMembership, wantMessage: true},
		{fixture: "membershipGiftingEvent", wantKind: EventKindMembershipGifting, wantType: MessageTypeMembership, wantMessage: true},
		{fixture: "giftMembershipReceivedEvent", wantKind: EventKindGiftMembershipReceived, wantType: MessageTypeMembership, wantMessage: true},
		{fixture: "giftEventFlat", wantKind: EventKindGift, wantType: MessageTypePaid, wantMessage: true},
		{fixture: "giftEventNested", wantKind: EventKindGift, wantType: MessageTypePaid, wantMessage: true},
		{fixture: "fanFundingEvent", wantKind: EventKindFanFunding, wantType: MessageTypePaid, wantMessage: true},
		{fixture: "pollEvent", wantKind: EventKindPoll, wantType: MessageTypeNotice, wantMessage: true, wantPoll: true},
		{fixture: "messageDeletedEvent", wantModType: ModerationMessageDeleted},
		{fixture: "messageRetractedEvent", wantModType: ModerationMessageDeleted},
		{fixture: "userBannedEventTemporary", wantModType: ModerationUserTimedOut},
		{fixture: "userBannedEventPermanent", wantModType: ModerationUserBanned},
		{fixture: "sponsorOnlyModeStartedEvent", wantKind: EventKindSponsorOnlyModeStarted, wantType: MessageTypeNotice, wantMessage: true, wantRoom: RoomSponsorOnlyStarted},
		{fixture: "sponsorOnlyModeEndedEvent", wantKind: EventKindSponsorOnlyModeEnded, wantType: MessageTypeNotice, wantMessage: true, wantRoom: RoomSponsorOnlyEnded},
		{fixture: "chatEndedEvent", wantRoom: RoomChatEnded},
		{fixture: "tombstone", wantModType: ModerationTombstone},
		{fixture: "invalidType", wantKind: EventKindInvalidType, wantType: MessageTypeUnknown, wantMessage: true},
		{fixture: "unknownFutureEvent", wantKind: EventKindUnknown, wantType: MessageTypeUnknown, wantMessage: true},
	}

	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			item := loadEventFixture(t, test.fixture)
			result := normalizeItem(item, NormalizeOptions{Now: fixtureNow})

			if test.wantMessage {
				if len(result.Messages) != 1 {
					t.Fatalf("messages = %d, want 1", len(result.Messages))
				}
				msg := result.Messages[0]
				if msg.Kind != test.wantKind {
					t.Fatalf("kind = %q, want %q", msg.Kind, test.wantKind)
				}
				if msg.Type != test.wantType {
					t.Fatalf("type = %q, want %q", msg.Type, test.wantType)
				}
				if msg.RawType != item.Snippet.Type {
					t.Fatalf("RawType = %q, want the original snippet.type %q", msg.RawType, item.Snippet.Type)
				}
				if msg.LiveChatID != "live-chat-1" {
					t.Fatalf("LiveChatID = %q, want live-chat-1", msg.LiveChatID)
				}
			} else if len(result.Messages) != 0 {
				t.Fatalf("messages = %#v, want none: a removal must not reprint the removed row", result.Messages)
			}

			if test.wantModType != "" {
				if len(result.Moderations) != 1 {
					t.Fatalf("moderations = %d, want 1", len(result.Moderations))
				}
				if got := result.Moderations[0].Type; got != test.wantModType {
					t.Fatalf("moderation type = %q, want %q", got, test.wantModType)
				}
			}

			if test.wantRoom != "" {
				if len(result.RoomEvents) != 1 {
					t.Fatalf("room events = %d, want 1", len(result.RoomEvents))
				}
				if got := result.RoomEvents[0].Type; got != test.wantRoom {
					t.Fatalf("room event type = %q, want %q", got, test.wantRoom)
				}
			}

			if test.wantPoll && len(result.Polls) != 1 {
				t.Fatalf("polls = %d, want 1", len(result.Polls))
			}
		})
	}
}

func TestNormalizeSuperChatKeepsMoneyOutOfFloats(t *testing.T) {
	item := loadEventFixture(t, "superChatEvent")
	msg := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Messages[0]

	if msg.SuperChat == nil {
		t.Fatal("SuperChat = nil, want details")
	}
	amount, ok := msg.Amount()
	if !ok {
		t.Fatal("Amount() reported no amount")
	}
	if amount.Micros != 5000000 {
		t.Fatalf("Micros = %d, want 5000000 decoded from the JSON string", amount.Micros)
	}
	if amount.Currency != "USD" || amount.Display != "$5.00" {
		t.Fatalf("amount = %#v, want USD/$5.00", amount)
	}
	if msg.Tier() != 3 {
		t.Fatalf("Tier() = %d, want 3", msg.Tier())
	}
	if msg.Text != "keep going!" {
		t.Fatalf("Text = %q, want the buyer comment", msg.Text)
	}
}

func TestNormalizeSuperStickerAcceptsBothLanguageFieldNames(t *testing.T) {
	tests := []struct {
		fixture string
		want    string
	}{
		{fixture: "superStickerEvent", want: "en"},
		{fixture: "superStickerEventHTMLLanguage", want: "de"},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			item := loadEventFixture(t, test.fixture)
			msg := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Messages[0]
			if msg.SuperSticker == nil {
				t.Fatal("SuperSticker = nil, want details")
			}
			if msg.SuperSticker.Language != test.want {
				t.Fatalf("Language = %q, want %q", msg.SuperSticker.Language, test.want)
			}
			if msg.Text != msg.SuperSticker.AltText {
				t.Fatalf("Text = %q, want the alt text: it is the only renderable form", msg.Text)
			}
		})
	}
}

func TestNormalizeGiftAcceptsBothWireShapes(t *testing.T) {
	tests := []struct {
		fixture      string
		wantName     string
		wantDuration time.Duration
	}{
		{fixture: "giftEventFlat", wantName: "Confetti", wantDuration: 3500 * time.Millisecond},
		{fixture: "giftEventNested", wantName: "Fireworks", wantDuration: 10 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			item := loadEventFixture(t, test.fixture)
			msg := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Messages[0]
			if msg.Gift == nil {
				t.Fatalf("Gift = nil, want the %s shape decoded", test.fixture)
			}
			if msg.Gift.Name != test.wantName {
				t.Fatalf("Name = %q, want %q", msg.Gift.Name, test.wantName)
			}
			if msg.Gift.Duration != test.wantDuration {
				t.Fatalf("Duration = %v, want %v", msg.Gift.Duration, test.wantDuration)
			}
		})
	}
}

func TestNormalizeMembershipDetails(t *testing.T) {
	tests := []struct {
		fixture   string
		wantKind  MembershipKind
		wantLevel string
		wantCount int
		wantMonth int
	}{
		{fixture: "newSponsorEvent", wantKind: MembershipNew, wantLevel: "Supporter"},
		{fixture: "newSponsorEventUpgrade", wantKind: MembershipUpgrade, wantLevel: "Super Supporter"},
		{fixture: "memberMilestoneChatEvent", wantKind: MembershipMilestone, wantLevel: "Supporter", wantMonth: 14},
		{fixture: "membershipGiftingEvent", wantKind: MembershipGifting, wantLevel: "Supporter", wantCount: 5},
		{fixture: "giftMembershipReceivedEvent", wantKind: MembershipGiftReceived, wantLevel: "Supporter"},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			item := loadEventFixture(t, test.fixture)
			msg := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Messages[0]
			if msg.Membership == nil {
				t.Fatal("Membership = nil, want details")
			}
			if msg.Membership.Kind != test.wantKind {
				t.Fatalf("Kind = %q, want %q", msg.Membership.Kind, test.wantKind)
			}
			if msg.Membership.LevelName != test.wantLevel {
				t.Fatalf("LevelName = %q, want %q", msg.Membership.LevelName, test.wantLevel)
			}
			if msg.Membership.GiftCount != test.wantCount {
				t.Fatalf("GiftCount = %d, want %d", msg.Membership.GiftCount, test.wantCount)
			}
			if msg.Membership.Months != test.wantMonth {
				t.Fatalf("Months = %d, want %d", msg.Membership.Months, test.wantMonth)
			}
		})
	}
}

func TestNormalizeBanDurationDecodesStringSeconds(t *testing.T) {
	item := loadEventFixture(t, "userBannedEventTemporary")
	event := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Moderations[0]

	if event.Duration != 5*time.Minute {
		t.Fatalf("Duration = %v, want 5m decoded from the string banDurationSeconds", event.Duration)
	}
	if event.TargetChannelID != "UCspam0000000000000000001" || event.TargetDisplayName != "Spam Bot" {
		t.Fatalf("target = %q/%q, want the banned user", event.TargetChannelID, event.TargetDisplayName)
	}

	permanent := normalizeItem(loadEventFixture(t, "userBannedEventPermanent"), NormalizeOptions{Now: fixtureNow}).Moderations[0]
	if permanent.Duration != 0 {
		t.Fatalf("permanent ban Duration = %v, want 0", permanent.Duration)
	}
}

// banDurationSeconds counts seconds, but Duration counts nanoseconds. A value
// near the top of the range that parseMicros allows would overflow when
// multiplied into nanoseconds and wrap negative, turning "banned for longer
// than the universe has existed" into a timeout that has already elapsed.
func TestNormalizeBanDurationClampsInsteadOfOverflowing(t *testing.T) {
	item := loadEventFixture(t, "userBannedEventTemporary")
	item.Snippet.UserBannedDetails.BanDurationSeconds = fmt.Sprint(uint64(math.MaxUint64))

	event := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Moderations[0]

	if event.Duration <= 0 {
		t.Fatalf("Duration = %v, want a positive clamped value rather than an overflowed one", event.Duration)
	}
	// The clamp is applied in seconds before the multiply, so the result is
	// the largest whole number of seconds a Duration can hold.
	wantSeconds := math.MaxInt64 / int64(time.Second)
	if event.Duration != time.Duration(wantSeconds)*time.Second {
		t.Fatalf("Duration = %v, want %v", event.Duration, time.Duration(wantSeconds)*time.Second)
	}
	if event.Type != ModerationUserTimedOut {
		t.Fatalf("Type = %v, want the event to stay a timeout", event.Type)
	}
}

func TestNormalizeTombstoneCorrelatesWithSeenMessages(t *testing.T) {
	item := loadEventFixture(t, "tombstone")

	unseen := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Moderations[0]
	if unseen.Type != ModerationTombstone {
		t.Fatalf("type = %q, want ModerationTombstone when the message was never on screen", unseen.Type)
	}

	seen := normalizeItem(item, NormalizeOptions{
		Now:            fixtureNow,
		SeenMessageIDs: func(id string) bool { return id == "msg-text-1" },
	}).Moderations[0]
	if seen.Type != ModerationMessageDeleted {
		t.Fatalf("type = %q, want ModerationMessageDeleted for a message the viewer already saw", seen.Type)
	}
	if seen.TargetMessageID != "msg-text-1" {
		t.Fatalf("TargetMessageID = %q, want msg-text-1", seen.TargetMessageID)
	}
}

func TestNormalizeUnknownTypeKeepsDisplayMessageAndRawType(t *testing.T) {
	item := loadEventFixture(t, "unknownFutureEvent")
	msg := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Messages[0]

	if msg.Kind != EventKindUnknown || msg.Type != MessageTypeUnknown {
		t.Fatalf("kind/type = %q/%q, want unknown/unknown", msg.Kind, msg.Type)
	}
	if msg.Text != "something new happened in chat" {
		t.Fatalf("Text = %q, want snippet.displayMessage", msg.Text)
	}
	if msg.RawType != "someEventYouTubeAddedTomorrow" {
		t.Fatalf("RawType = %q, want the original snippet.type", msg.RawType)
	}
	if len(msg.Fragments) == 0 {
		t.Fatal("Fragments is empty; an unknown event still renders as a row")
	}
}

func TestNormalizeBadgesFollowAuthorAndMembershipDetails(t *testing.T) {
	item := loadEventFixture(t, "newSponsorEvent")
	msg := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Messages[0]

	// The author block on a newSponsorEvent does not yet say isChatSponsor,
	// but the event itself is the membership. The badge has to follow the
	// event, or the row that announces a membership shows no member badge.
	var found bool
	for _, badge := range msg.Badges {
		if badge.Kind == BadgeMember {
			found = true
			if badge.Info != "Supporter" {
				t.Fatalf("member badge Info = %q, want the level name", badge.Info)
			}
		}
	}
	if !found {
		t.Fatalf("badges = %#v, want a member badge", msg.Badges)
	}
}

func TestNormalizeHistoricalMarksThePrimingPage(t *testing.T) {
	item := loadEventFixture(t, "textMessageEvent")

	live := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Messages[0]
	if live.Historical {
		t.Fatal("Historical = true for a live row")
	}
	priming := normalizeItem(item, NormalizeOptions{Now: fixtureNow, Historical: true}).Messages[0]
	if !priming.Historical {
		t.Fatal("Historical = false for the priming page; a 2000-row backlog must not animate")
	}
}

func TestNormalizeTimestampFallsBackToTheClock(t *testing.T) {
	item := loadEventFixture(t, "textMessageEvent")
	item.Snippet.PublishedAt = "not-a-timestamp"

	msg := normalizeItem(item, NormalizeOptions{Now: fixtureNow}).Messages[0]
	if !msg.Timestamp.Equal(fixtureNow) {
		t.Fatalf("Timestamp = %v, want the injected clock rather than the epoch", msg.Timestamp)
	}
}

func TestParseMicros(t *testing.T) {
	tests := []struct {
		value string
		want  int64
		ok    bool
	}{
		{value: "5000000", want: 5000000, ok: true},
		{value: " 300 ", want: 300, ok: true},
		{value: "0", want: 0, ok: true},
		{value: "", ok: false},
		{value: "nonsense", ok: false},
		{value: "18446744073709551615", want: 9223372036854775807, ok: true},
	}
	for _, test := range tests {
		got, ok := parseMicros(test.value)
		if ok != test.ok || got != test.want {
			t.Fatalf("parseMicros(%q) = (%d, %t), want (%d, %t)", test.value, got, ok, test.want, test.ok)
		}
	}
}

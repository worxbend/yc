package render

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/youtube"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata")

const goldenPath = "testdata/event_rows.golden"

// eventFixture is one normalized message plus the name it appears under in the
// golden file.
type eventFixture struct {
	name string
	msg  youtube.Message
}

func viewerAuthor() youtube.Author {
	return youtube.Author{
		ChannelID:   "UC_bob_00000000000000002",
		DisplayName: "Bob",
		ChannelURL:  "https://www.youtube.com/@bobstreams",
	}
}

func memberAuthor() youtube.Author {
	author := viewerAuthor()
	author.DisplayName = "Carol Danvers"
	author.ChannelID = "UC_carol_000000000000003"
	author.IsMember = true
	author.MemberLevelName = "Comet Crew"
	return author
}

func withAuthor(msg youtube.Message, author youtube.Author) youtube.Message {
	msg.Author = author
	msg.Badges = youtube.BadgesForAuthor(author)
	return msg
}

func baseMessage(kind youtube.EventKind, text string) youtube.Message {
	return youtube.Message{
		ID:         "msg-" + string(kind),
		LiveChatID: "chat-1",
		Timestamp:  testTimestamp,
		Text:       text,
		Kind:       kind,
		Type:       youtube.MessageTypeForKind(kind),
	}
}

// eventFixtures covers every event kind the renderer can be handed, including
// the ones YouTube no longer delivers and one type invented after this build,
// because "never crash, never drop a row" is only true if it is tested.
func eventFixtures() []eventFixture {
	owner := viewerAuthor()
	owner.DisplayName = "Studio Nine"
	owner.ChannelID = "UC_studio_00000000000004"
	owner.IsOwner = true
	owner.IsVerified = true

	moderator := viewerAuthor()
	moderator.DisplayName = "mod_dana"
	moderator.ChannelID = "UC_dana_0000000000000005"
	moderator.IsModerator = true

	superChat := withAuthor(baseMessage(youtube.EventKindSuperChat, "thanks for the stream"), viewerAuthor())
	superChat.SuperChat = &youtube.SuperChatDetails{
		Amount:  youtube.Money{Micros: 5_000_000, Currency: "USD", Display: "$5.00"},
		Tier:    3,
		Comment: "thanks for the stream",
	}

	bigSuperChat := withAuthor(baseMessage(youtube.EventKindSuperChat, ""), memberAuthor())
	bigSuperChat.SuperChat = &youtube.SuperChatDetails{
		Amount: youtube.Money{Micros: 500_000_000, Currency: "USD"},
		Tier:   11,
	}

	sticker := withAuthor(baseMessage(youtube.EventKindSuperSticker, "sent a Super Sticker"), viewerAuthor())
	sticker.SuperSticker = &youtube.SuperStickerDetails{
		Amount:    youtube.Money{Micros: 2_000_000, Currency: "EUR", Display: "€2.00"},
		Tier:      2,
		StickerID: "sticker-42",
		AltText:   "a cat waving a tiny flag",
		Language:  "en",
	}

	gift := withAuthor(baseMessage(youtube.EventKindGift, "sent a gift"), viewerAuthor())
	gift.Gift = &youtube.GiftDetails{
		Name:       "Star Shower",
		Jewels:     250,
		ComboCount: 3,
	}

	newMember := withAuthor(baseMessage(youtube.EventKindNewSponsor, "became a member"), memberAuthor())
	newMember.Membership = &youtube.MembershipDetails{Kind: youtube.MembershipNew, LevelName: "Comet Crew"}

	upgrade := withAuthor(baseMessage(youtube.EventKindNewSponsor, "upgraded their membership"), memberAuthor())
	upgrade.Membership = &youtube.MembershipDetails{Kind: youtube.MembershipUpgrade, LevelName: "Comet Captain"}

	milestone := withAuthor(baseMessage(youtube.EventKindMemberMilestone, "member for 14 months"), memberAuthor())
	milestone.Membership = &youtube.MembershipDetails{
		Kind:      youtube.MembershipMilestone,
		LevelName: "Comet Crew",
		Months:    14,
		Comment:   "still the best chat on the platform",
	}

	gifting := withAuthor(baseMessage(youtube.EventKindMembershipGifting, "gifted 5 memberships"), memberAuthor())
	gifting.Membership = &youtube.MembershipDetails{
		Kind:      youtube.MembershipGifting,
		LevelName: "Comet Crew",
		GiftCount: 5,
	}

	giftReceived := withAuthor(baseMessage(youtube.EventKindGiftMembershipReceived, "received a gift membership"), viewerAuthor())
	giftReceived.Membership = &youtube.MembershipDetails{
		Kind:                    youtube.MembershipGiftReceived,
		LevelName:               "Comet Crew",
		GifterChannelID:         "UC_carol_000000000000003",
		AssociatedGiftMessageID: "msg-gifting",
	}

	banned := withAuthor(baseMessage(youtube.EventKindUserBanned, "Bob was timed out for 5m"), moderator)

	// A chat message a moderator removed: the row survives, the words do not.
	deleted := withAuthor(baseMessage(youtube.EventKindText, "this will not be printed"), viewerAuthor())
	deleted.Deleted = true

	// The messageDeletedEvent item itself, which is a different thing from the
	// row above: YouTube delivers it as its own item naming another message.
	deletedEvent := withAuthor(baseMessage(youtube.EventKindMessageDeleted, "a message was deleted"), moderator)
	deletedEvent.Deleted = true

	tombstone := baseMessage(youtube.EventKindTombstone, "")

	unknown := withAuthor(baseMessage(youtube.EventKindUnknown, "a brand new event nobody has shipped support for"), viewerAuthor())
	unknown.RawType = "hologramEvent"

	// fanFundingEvent predates Super Chat and YouTube no longer delivers it,
	// but a replayed or archived chat still can, and it carries an amount.
	fanFunding := withAuthor(baseMessage(youtube.EventKindFanFunding, "an old-style tip"), viewerAuthor())
	fanFunding.SuperChat = &youtube.SuperChatDetails{
		Amount:  youtube.Money{Micros: 3_000_000, Currency: "GBP", Display: "£3.00"},
		Comment: "an old-style tip",
	}

	// messageRetractedEvent is the author withdrawing their own message. Like a
	// deletion, the words have to leave the screen.
	retracted := withAuthor(baseMessage(youtube.EventKindMessageRetracted, "something the author took back"), viewerAuthor())
	retracted.Deleted = true

	// invalidType is the API's own enum member, not a yc fallback: YouTube
	// itself can label an item this way and it must still render as a row.
	invalidType := withAuthor(baseMessage(youtube.EventKindInvalidType, ""), viewerAuthor())
	invalidType.RawType = "invalidType"

	return []eventFixture{
		{"text", withAuthor(baseMessage(youtube.EventKindText, "hello chat, how is everyone doing tonight"), viewerAuthor())},
		{"text_owner_verified", withAuthor(baseMessage(youtube.EventKindText, "starting in five"), owner)},
		{"super_chat", superChat},
		{"super_chat_top_tier", bigSuperChat},
		{"super_sticker", sticker},
		{"gift", gift},
		{"new_sponsor", newMember},
		{"member_upgrade", upgrade},
		{"member_milestone", milestone},
		{"membership_gifting", gifting},
		{"gift_membership_received", giftReceived},
		{"user_banned", banned},
		{"message_deleted", deleted},
		{"message_deleted_event", deletedEvent},
		{"tombstone", tombstone},
		{"sponsor_only_started", baseMessage(youtube.EventKindSponsorOnlyModeStarted, "")},
		{"sponsor_only_ended", baseMessage(youtube.EventKindSponsorOnlyModeEnded, "")},
		{"chat_ended", baseMessage(youtube.EventKindChatEnded, "")},
		{"poll", baseMessage(youtube.EventKindPoll, "which map next?")},
		{"fan_funding", fanFunding},
		{"message_retracted", retracted},
		{"invalid_type", invalidType},
		{"unknown", unknown},
	}
}

// The golden is only a regression net for "every event kind" if it actually
// covers every event kind. A kind added to internal/youtube without a fixture
// here would go unrendered until someone hit it on a live stream.
func TestEveryEventKindHasAGoldenFixture(t *testing.T) {
	covered := make(map[youtube.EventKind]bool, len(eventFixtures()))
	for _, fixture := range eventFixtures() {
		covered[fixture.msg.Kind] = true
	}

	// Every kind reachable from a documented snippet.type, plus the unknown
	// fallback that a future YouTube event degrades into.
	//
	// The list comes from internal/youtube rather than being written out here.
	// A hand-copied list looks like an exhaustiveness guard and is not one: it
	// passes when it is stale, which is exactly when the guard is needed. Now
	// adding an event kind in model.go fails this test until a fixture exists.
	for _, kind := range youtube.AllEventKinds() {
		if !covered[kind] {
			t.Errorf("event kind %q has no golden fixture", kind)
		}
	}

	// And every documented snippet.type must normalize into one of them, so a
	// new wire type cannot slip past both this list and the golden. The
	// undocumented spelling is appended to prove the unknown fallback is
	// itself covered.
	for _, snippetType := range append(youtube.DocumentedSnippetTypes(), "hologramEventFromTheFuture") {
		if kind := youtube.EventKindFromSnippetType(snippetType); !covered[kind] {
			t.Errorf("snippet.type %q normalizes to %q, which has no golden fixture", snippetType, kind)
		}
	}
}

// TestEventRowsGolden renders every event kind in every layout at a narrow and
// a wide terminal. It is the regression net for the whole package: a change to
// wrapping, chips, badges, or budgeting shows up here as a diff rather than as
// a bug report about one event type nobody rendered locally.
func TestEventRowsGolden(t *testing.T) {
	var builder strings.Builder
	for _, fixture := range eventFixtures() {
		for _, layout := range []LayoutMode{LayoutInline, LayoutGrouped, LayoutCompact} {
			for _, width := range []int{32, 72} {
				opts := testOptions(width)
				opts.Layout = layout
				opts.HighlightEmoji = true

				builder.WriteString("=== " + fixture.name + " / " + string(layout) + " / w=" + itoa(width) + "\n")
				for _, row := range Rows(fixture.msg, opts) {
					builder.WriteString("|" + row.Plain() + "|\n")
				}
			}
		}
	}
	got := builder.String()

	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run `go test ./internal/render -update` to create it): %v", err)
	}
	if got != string(want) {
		t.Fatalf("event rows differ from %s; rerun with -update to inspect the diff\n--- got ---\n%s", goldenPath, got)
	}
}

// No row may exceed the width it was rendered at, in any layout, for any event.
// A single over-wide row wraps in the terminal itself and desynchronizes the
// app's scroll arithmetic from what is on screen.
func TestEventRowsNeverExceedWidth(t *testing.T) {
	for _, fixture := range eventFixtures() {
		for _, layout := range []LayoutMode{LayoutInline, LayoutGrouped, LayoutCompact} {
			for _, badges := range []BadgeMode{BadgeModeGlyph, BadgeModeText, BadgeModeOff} {
				for width := MinimumRenderWidth; width <= 96; width++ {
					opts := testOptions(width)
					opts.Layout = layout
					opts.Badges = badges
					opts.FullUsername = true
					opts.Meta = AuthorMeta{Role: "member", MemberMonths: 14, Now: testTimestamp, FirstSeen: testTimestamp}

					for index, row := range Rows(fixture.msg, opts) {
						if got := row.Width(); got > width {
							t.Fatalf("%s / %s / %s / w=%d row %d width = %d: %q",
								fixture.name, layout, badges, width, index, got, row.Plain())
						}
						if got := textWidth(row.Plain()); got > width {
							t.Fatalf("%s / %s / %s / w=%d row %d plain width = %d: %q",
								fixture.name, layout, badges, width, index, got, row.Plain())
						}
					}
				}
			}
		}
	}
}

// A deleted message must never reprint its text. yc runs on terminals that are
// often on stream, so the whole point of a removal is that the words leave.
func TestDeletedMessageNeverReprintsItsText(t *testing.T) {
	msg := chatMessage("something a moderator removed")
	msg.Deleted = true
	for _, layout := range []LayoutMode{LayoutInline, LayoutGrouped, LayoutCompact} {
		opts := testOptions(72)
		opts.Layout = layout
		joined := strings.Join(rowsToPlain(Rows(msg, opts)), "\n")
		if strings.Contains(joined, "removed") && !strings.Contains(joined, "[message deleted]") {
			t.Fatalf("%s: deleted body leaked: %q", layout, joined)
		}
		if strings.Contains(joined, "something a moderator") {
			t.Fatalf("%s: deleted body reprinted: %q", layout, joined)
		}
	}
}

// A paid event whose amount never arrived must degrade to its text rather than
// render an empty block of tier color.
func TestPaidEventWithoutAmountFallsBackToText(t *testing.T) {
	msg := withAuthor(baseMessage(youtube.EventKindSuperChat, "a paid message with no amount"), viewerAuthor())
	joined := strings.Join(rowsToPlain(Rows(msg, testOptions(72))), " ")
	if !strings.Contains(joined, "a paid message with no amount") {
		t.Fatalf("unpriced Super Chat lost its text: %q", joined)
	}
}

// Structured details beat Message.Text: internal/youtube composes prose for
// these events too, and rendering both states the same fact twice.
func TestMembershipEventPrefersStructuredDetails(t *testing.T) {
	msg := withAuthor(baseMessage(youtube.EventKindMembershipGifting, "gifted 5 memberships"), memberAuthor())
	msg.Membership = &youtube.MembershipDetails{Kind: youtube.MembershipGifting, LevelName: "Comet Crew", GiftCount: 5}

	joined := strings.Join(rowsToPlain(Rows(msg, testOptions(72))), " ")
	if !strings.Contains(joined, "5 gift memberships") {
		t.Fatalf("gifting chip missing: %q", joined)
	}
	if strings.Contains(joined, "gifted 5 memberships") {
		t.Fatalf("composed fallback text rendered alongside the chip: %q", joined)
	}
}

func TestTierColorLadderIsMonotonicAndThemed(t *testing.T) {
	palette := testPalette()
	seen := make([]string, 0, 12)
	for tier := 0; tier <= 12; tier++ {
		color := TierColor(tier, palette)
		if color == "" {
			t.Fatalf("TierColor(%d) returned no color", tier)
		}
		seen = append(seen, color)
	}
	if TierColor(0, palette) != palette.Accent {
		t.Errorf("TierColor(0) = %q, want the accent role", TierColor(0, palette))
	}
	if TierColor(12, palette) != palette.Accent {
		t.Errorf("out-of-range TierColor(12) = %q, want the accent role", TierColor(12, palette))
	}
	if TierColor(11, palette) != palette.Error {
		t.Errorf("TierColor(11) = %q, want the error role", TierColor(11, palette))
	}
	distinct := map[string]struct{}{}
	for _, color := range seen[1:12] {
		distinct[color] = struct{}{}
	}
	if len(distinct) < 5 {
		t.Errorf("tier ladder collapsed to %d colors, want at least 5 distinguishable steps", len(distinct))
	}
}

// Money is decoded from a uint64-as-string and must never round-trip a float:
// a misprinted amount is the row a viewer screenshots.
func TestAmountChipFormatsMicrosWithoutFloats(t *testing.T) {
	msg := baseMessage(youtube.EventKindSuperChat, "")
	msg.SuperChat = &youtube.SuperChatDetails{
		Amount: youtube.Money{Micros: 1_234_567_890, Currency: "JPY"},
		Tier:   9,
	}
	chip, ok := AmountChip(msg, testPalette())
	if !ok {
		t.Fatal("AmountChip returned nothing for a priced Super Chat")
	}
	if want := " 1234.56 JPY "; chip.Text != want {
		t.Fatalf("chip text = %q, want %q", chip.Text, want)
	}
}

func TestAmountChipPrefersTheLocalizedDisplayString(t *testing.T) {
	msg := baseMessage(youtube.EventKindSuperChat, "")
	msg.SuperChat = &youtube.SuperChatDetails{
		Amount: youtube.Money{Micros: 5_000_000, Currency: "USD", Display: "US$5.00"},
	}
	chip, _ := AmountChip(msg, testPalette())
	if !strings.Contains(chip.Text, "US$5.00") {
		t.Fatalf("chip text = %q, want YouTube's own localized amount", chip.Text)
	}
}

func TestAmountChipDescribesGiftsInJewels(t *testing.T) {
	msg := baseMessage(youtube.EventKindGift, "")
	msg.Gift = &youtube.GiftDetails{Name: "Star Shower", Jewels: 250}
	chip, ok := AmountChip(msg, testPalette())
	if !ok {
		t.Fatal("AmountChip returned nothing for a gift")
	}
	if !strings.Contains(chip.Text, "250 jewels") {
		t.Fatalf("chip text = %q, want a jewel count", chip.Text)
	}
}

func TestMembershipChipRequiresDetails(t *testing.T) {
	msg := baseMessage(youtube.EventKindNewSponsor, "became a member")
	if _, ok := MembershipChip(msg, testPalette()); ok {
		t.Fatal("MembershipChip invented a chip for an event with no membership details")
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

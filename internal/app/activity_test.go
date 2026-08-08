package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

func paidMessage(at time.Time, name, display string) youtube.Message {
	return youtube.Message{
		ID:        "sc-" + display,
		Timestamp: at,
		Author:    youtube.Author{ChannelID: "UC-" + name, DisplayName: name},
		Kind:      youtube.EventKindSuperChat,
		Type:      youtube.MessageTypePaid,
		SuperChat: &youtube.SuperChatDetails{
			// Money is an integer micro-amount and a pre-localized display
			// string: no float ever touches a currency value.
			Amount: youtube.Money{Micros: 5_000_000, Currency: "USD", Display: display},
			Tier:   3,
		},
	}
}

func giftReceipt(at time.Time, name string) youtube.Message {
	return youtube.Message{
		ID:         "gift-" + name + at.String(),
		Timestamp:  at,
		Author:     youtube.Author{ChannelID: "UC-" + name, DisplayName: name},
		Kind:       youtube.EventKindGiftMembershipReceived,
		Type:       youtube.MessageTypeMembership,
		Membership: &youtube.MembershipDetails{Kind: youtube.MembershipGiftReceived},
	}
}

func TestActivityLogHasExactDimensions(t *testing.T) {
	at := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	st := activityLogState{
		Palette: theme.DefaultPalette(),
		Entries: []activityEntry{
			{Kind: activityPaid, Text: "alice sent $5.00", At: at},
			{Kind: activityMembership, Text: "bob became a member", At: at},
		},
	}
	for _, width := range []int{16, 20, 28, 34, 60} {
		lines := plainLines(renderActivityLog(width, 5, st))
		if len(lines) != 7 {
			t.Fatalf("width %d rendered %d rows", width, len(lines))
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got != width {
				t.Fatalf("width %d row %d is %d cells (%q)", width, i, got, line)
			}
		}
	}
}

// The log is bottom-anchored like chat, so a nearly empty column pads above
// the entries rather than below them.
func TestActivityLogIsBottomAnchored(t *testing.T) {
	at := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	st := activityLogState{
		Palette: theme.DefaultPalette(),
		Entries: []activityEntry{{Kind: activityPaid, Text: "alice sent $5.00", At: at}},
	}
	lines := plainLines(renderActivityLog(30, 4, st))
	// Rows 1..4 are content; the entry must be on the last of them.
	if !strings.Contains(lines[4], "alice sent") {
		t.Fatalf("the newest entry is not at the bottom:\n%s", strings.Join(lines, "\n"))
	}
}

// Every glyph must be one cell, or the text column beside them stops aligning.
func TestActivityGlyphsAreOneCell(t *testing.T) {
	palette := theme.DefaultPalette()
	kinds := []activityKind{
		activityPaid, activityMembership, activityGift, activityModeration,
		activityRoom, activityPoll, activityQuota, activityChat, activityKind("unknown"),
	}
	for _, kind := range kinds {
		glyph, color := activityKindGlyph(kind, palette)
		if got := ansi.StringWidth(glyph); got != 1 {
			t.Errorf("kind %q glyph %q is %d cells", kind, glyph, got)
		}
		if color == "" {
			t.Errorf("kind %q has no color", kind)
		}
	}
}

// The narrow column drops the timestamp before the glyph and the glyph before
// the text: the text is the part that always survives.
func TestActivityRowDegradesTextLast(t *testing.T) {
	at := time.Date(2026, 8, 8, 20, 30, 0, 0, time.UTC)
	st := activityLogState{Palette: theme.DefaultPalette()}
	entry := activityEntry{Kind: activityPaid, Text: "alice sent $5.00", At: at}

	wide := plainLines(activityLogLine(40, entry, st))[0]
	if !strings.Contains(wide, "◈") || !strings.Contains(wide, "alice") {
		t.Fatalf("wide row is missing parts: %q", wide)
	}
	narrow := plainLines(activityLogLine(11, entry, st))[0]
	if strings.Contains(narrow, "◈") {
		t.Fatalf("narrow row kept the glyph: %q", narrow)
	}
	if !strings.Contains(narrow, "alice") {
		t.Fatalf("narrow row lost the text: %q", narrow)
	}
}

// Paid rows prefer YouTube's own pre-localized amount string. yc never does its
// own currency formatting.
func TestPaidEntryUsesTheAPIsDisplayString(t *testing.T) {
	at := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	entry, ok := activityEntryForMessage(paidMessage(at, "alice", "¥1,000"), "chat", "Launch Day")
	if !ok {
		t.Fatal("a Super Chat produced no activity entry")
	}
	if !strings.Contains(entry.Text, "¥1,000") {
		t.Fatalf("entry text = %q, want the API's display string", entry.Text)
	}
}

// Ordinary chat produces no entry: the activity column exists to surface what
// is not ordinary chat.
func TestOrdinaryChatProducesNoActivityEntry(t *testing.T) {
	message := youtube.Message{Kind: youtube.EventKindText, Type: youtube.MessageTypeChat}
	if _, ok := activityEntryForMessage(message, "chat", "Launch Day"); ok {
		t.Fatal("a plain chat message produced an activity entry")
	}
}

// A deletion exists to take words off a screen that is frequently on stream, so
// the activity row must not reprint them.
func TestModerationEntryDoesNotReprintRemovedText(t *testing.T) {
	event := youtube.ModerationEvent{
		Type:              youtube.ModerationMessageDeleted,
		TargetMessageID:   "m1",
		TargetDisplayName: "carol",
		At:                time.Now(),
	}
	entry, ok := activityEntryForModeration(event, "Launch Day")
	if !ok {
		t.Fatal("a deletion produced no activity entry")
	}
	if entry.Kind != activityModeration {
		t.Fatalf("kind = %q", entry.Kind)
	}
	if !strings.Contains(entry.Text, "deleted") {
		t.Fatalf("entry text = %q", entry.Text)
	}
}

func TestTimeoutEntryNamesTheDuration(t *testing.T) {
	event := youtube.ModerationEvent{
		Type:              youtube.ModerationUserTimedOut,
		TargetDisplayName: "carol",
		Duration:          5 * time.Minute,
		At:                time.Now(),
	}
	entry, _ := activityEntryForModeration(event, "Launch Day")
	if !strings.Contains(entry.Text, "carol") || !strings.Contains(entry.Text, "5m") {
		t.Fatalf("timeout entry = %q", entry.Text)
	}
}

// A fifty-membership gift arrives as fifty events. Logging each one would bury
// every other kind of activity, so the tail collapses into a count.
func TestGiftBurstsCollapse(t *testing.T) {
	at := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	entries := make([]activityEntry, 0, 20)
	for i := range 20 {
		entry, _ := activityEntryForMessage(giftReceipt(at.Add(time.Duration(i)*100*time.Millisecond), "viewer"), "chat", "Launch Day")
		entries = append(entries, entry)
	}
	collapsed := collapseGiftBursts(entries)
	if len(collapsed) != giftBurstLimit+1 {
		t.Fatalf("collapsed to %d rows, want %d", len(collapsed), giftBurstLimit+1)
	}
	summary := collapsed[len(collapsed)-1]
	if !strings.Contains(summary.Text, "+15 more") {
		t.Fatalf("summary row = %q", summary.Text)
	}
}

// A gap longer than the burst window starts a new run rather than folding two
// unrelated gifts together.
func TestGiftBurstWindowResets(t *testing.T) {
	at := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	entries := []activityEntry{
		{Kind: activityGift, Text: "one", At: at},
		{Kind: activityGift, Text: "two", At: at.Add(giftBurstWindow * 2)},
	}
	if got := len(collapseGiftBursts(entries)); got != 2 {
		t.Fatalf("collapsed %d rows across a gap, want 2", got)
	}
}

func TestActivityEntriesAreDerivedFromChatHistory(t *testing.T) {
	model := newModelForTest(t, "launch-day")
	at := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	state := model.activeChatState()
	state.messages = append(state.messages,
		testMessage(t, "m1", "", "alice", "just chatting"),
		paidMessage(at, "bob", "$5.00"),
	)

	entries := activityEntriesForChats(model.chats, maxActivityEntries)
	if len(entries) != 1 {
		t.Fatalf("derived %d entries, want 1", len(entries))
	}
	if entries[0].Kind != activityPaid {
		t.Fatalf("derived entry kind = %q", entries[0].Kind)
	}
}

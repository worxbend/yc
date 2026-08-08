package render

import (
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/youtube"
)

func TestNormalizeLayoutModeFallsBackToDefault(t *testing.T) {
	for _, test := range []struct {
		in   string
		want LayoutMode
	}{
		{"inline", LayoutInline},
		{"GROUPED", LayoutGrouped},
		{" compact ", LayoutCompact},
		{"", DefaultLayoutMode},
		{"nonsense", DefaultLayoutMode},
	} {
		if got := NormalizeLayoutMode(test.in); got != test.want {
			t.Errorf("NormalizeLayoutMode(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestGroupedLayoutPutsAuthorOnItsOwnRow(t *testing.T) {
	opts := testOptions(72)
	opts.Layout = LayoutGrouped

	rows := rowsToPlain(Rows(chatMessage("hello chat"), opts))
	if len(rows) != 2 {
		t.Fatalf("grouped rows = %d (%#v), want a header row plus a body row", len(rows), rows)
	}
	if !strings.Contains(rows[0], "Alice Lovelace") {
		t.Fatalf("header row = %q, want the author name", rows[0])
	}
	if strings.Contains(rows[0], "hello chat") {
		t.Fatalf("header row = %q, want the message text on its own row", rows[0])
	}
	if !strings.Contains(rows[1], "hello chat") {
		t.Fatalf("body row = %q, want the message text", rows[1])
	}
	// The body is indented under the header so a run of messages reads as one
	// block rather than as a flat list.
	if !strings.HasPrefix(rows[1], "   ") {
		t.Fatalf("body row = %q, want it indented under the author header", rows[1])
	}
}

func TestGroupedLayoutSuppressesRepeatedAuthorHeader(t *testing.T) {
	opts := testOptions(72)
	opts.Layout = LayoutGrouped
	opts.ContinuesGroup = true

	rows := rowsToPlain(Rows(chatMessage("hello chat"), opts))
	if len(rows) != 1 {
		t.Fatalf("continued-group rows = %d (%#v), want just the body row", len(rows), rows)
	}
	if strings.Contains(rows[0], "Alice Lovelace") {
		t.Fatalf("continued group repeated the author header: %q", rows[0])
	}
	if !strings.Contains(rows[0], "hello chat") {
		t.Fatalf("continued-group row = %q, want the message text", rows[0])
	}
}

func TestCompactLayoutDropsDecorations(t *testing.T) {
	opts := testOptions(72)
	opts.Layout = LayoutCompact

	rows := rowsToPlain(Rows(chatMessage("hello chat"), opts))
	if len(rows) != 1 {
		t.Fatalf("compact rows = %d (%#v), want one", len(rows), rows)
	}
	if got, want := rows[0], "Alice Lovelace: hello chat"; got != want {
		t.Fatalf("compact row = %q, want %q", got, want)
	}
}

// Compact drops decoration, not meaning: a Super Chat is still a Super Chat in
// a 40-column side pane, and hiding the amount would misreport the message.
func TestCompactLayoutKeepsTheAmountChip(t *testing.T) {
	msg := chatMessage("")
	msg.Kind = youtube.EventKindSuperChat
	msg.Type = youtube.MessageTypePaid
	msg.SuperChat = &youtube.SuperChatDetails{
		Amount:  youtube.Money{Micros: 10_000_000, Currency: "USD", Display: "$10.00"},
		Tier:    4,
		Comment: "good luck",
	}

	opts := testOptions(60)
	opts.Layout = LayoutCompact
	joined := strings.Join(rowsToPlain(Rows(msg, opts)), " ")
	if !strings.Contains(joined, "$10.00") {
		t.Fatalf("compact Super Chat lost its amount: %q", joined)
	}
	if !strings.Contains(joined, "good luck") {
		t.Fatalf("compact Super Chat lost its comment: %q", joined)
	}
}

// The prefix budget drops decorations in a fixed order - badges, then avatar,
// then timestamp - because the author's name is the one part of a row that
// cannot be sacrificed.
func TestNarrowInlinePrefixDropsDecorationsBeforeTheName(t *testing.T) {
	msg := chatMessage("hi")
	msg.Author.IsOwner = true
	msg.Author.IsModerator = true
	msg.Badges = youtube.BadgesForAuthor(msg.Author)

	previousKinds := -1
	for width := 72; width >= MinimumRenderWidth; width-- {
		opts := testOptions(width)
		prefix := messagePrefix(msg, opts)

		kinds := map[FragmentKind]bool{}
		for _, fragment := range prefix {
			kinds[fragment.Kind] = true
		}
		if !kinds[FragmentUsername] {
			t.Fatalf("width %d dropped the username from the prefix", width)
		}
		if kinds[FragmentBadge] && !kinds[FragmentAvatar] && width >= 24 {
			t.Fatalf("width %d kept badges after dropping the avatar", width)
		}
		count := len(kinds)
		if previousKinds >= 0 && count > previousKinds {
			t.Fatalf("width %d gained decorations while narrowing (%d -> %d)", width, previousKinds, count)
		}
		previousKinds = count
	}
}

// Every layout must survive the narrowest width the package accepts. A panic or
// an empty render here is a crash in a resized terminal.
func TestEveryLayoutRendersAtTheMinimumWidth(t *testing.T) {
	for _, layout := range []LayoutMode{LayoutInline, LayoutGrouped, LayoutCompact} {
		opts := testOptions(MinimumRenderWidth)
		opts.Layout = layout
		rows := Rows(chatMessage("hello"), opts)
		if len(rows) == 0 {
			t.Fatalf("%s produced no rows at the minimum width", layout)
		}
		for _, row := range rows {
			if row.Width() > MinimumRenderWidth {
				t.Fatalf("%s row %q is %d cells wide at width %d", layout, row.Plain(), row.Width(), MinimumRenderWidth)
			}
		}
	}
}

func TestGroupedIndentScalesWithWidth(t *testing.T) {
	for _, test := range []struct {
		width int
		want  int
	}{
		{80, 3},
		{40, 3},
		{39, 2},
		{20, 2},
		{19, 0},
	} {
		if got := groupedIndentWidth(test.width); got != test.want {
			t.Errorf("groupedIndentWidth(%d) = %d, want %d", test.width, got, test.want)
		}
	}
}

// A room-state event names nobody, so it must not be given a placeholder
// author, and grouping must not spend a header row on a bare clock.
func TestAuthorlessNoticeSkipsTheAuthorColumn(t *testing.T) {
	msg := youtube.Message{
		Timestamp: testTimestamp,
		Kind:      youtube.EventKindSponsorOnlyModeStarted,
		Type:      youtube.MessageTypeNotice,
	}
	for _, layout := range []LayoutMode{LayoutInline, LayoutGrouped, LayoutCompact} {
		opts := testOptions(72)
		opts.Layout = layout
		rows := rowsToPlain(Rows(msg, opts))
		if len(rows) != 1 {
			t.Fatalf("%s: rows = %#v, want a single row", layout, rows)
		}
		if strings.Contains(rows[0], "notice:") {
			t.Fatalf("%s: invented an author for a room event: %q", layout, rows[0])
		}
		if !strings.Contains(rows[0], "members-only mode on") {
			t.Fatalf("%s: room event lost its description: %q", layout, rows[0])
		}
	}
}

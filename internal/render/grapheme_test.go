package render

import (
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/youtube"
)

// Grapheme and width safety for attacker-supplied identity and body text.
//
// Display names and message bodies are the two strings in yc that a stranger
// on the internet chooses and yc prints to a terminal that is frequently on
// stream. Every one of them is measured, truncated, padded, and wrapped, and
// each of those four operations has a naive implementation that is wrong for
// CJK, for a ZWJ sequence, and for a combining mark. The failure mode is not
// cosmetic: a row that measures 40 and prints 41 wraps in the terminal itself,
// which desynchronizes the app's scroll arithmetic from what is on screen, and
// a cluster cut in half prints as a replacement glyph or - worse - as a
// dangling ZWJ that swallows whatever character follows it.
//
// These tests assert the invariants rather than the output, so they survive a
// palette change or a layout tweak and still fail if the arithmetic breaks.

// pathologicalNames are display names chosen so that every width primitive in
// the package is wrong about at least one of them under a naive implementation.
var pathologicalNames = []struct {
	name  string
	value string
}{
	{"ascii", "Alice Lovelace"},
	{"cjk_wide", "山田太郎のチャンネル"},
	{"cjk_mixed", "Bob 中文 Name"},
	{"zwj_family", "\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466"},
	{"zwj_prefixed", "\U0001F3F3\ufe0f\u200d\U0001F308 Rainbow"},
	{"skin_tone", "\U0001F44B\U0001F3FD Waver"},
	{"regional_flag", "\U0001F1EF\U0001F1F5 Japan Fan"},
	{"keycap", "1\ufe0f⃣ First"},
	{"combining_nfd", "éléonore"},
	{"combining_stack", "ź̧̖́ stacked"},
	{"variation_selector", "☺\ufe0f Smiler"},
	{"rtl", "مرحبا بالعالم"},
	{"hangul_jamo", "각 Korean"},
	{"single_wide", "中"},
	{"all_zero_width", "\u200b\u200b\u200b"},
}

// pathologicalBodies are message bodies with the same properties as the names.
var pathologicalBodies = []string{
	"plain ascii body",
	"\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466 the whole family said hello",
	strings.Repeat("日本語", 24),
	strings.Repeat("é", 60),
	"mixed 中文 and \U0001F44B\U0001F3FD and é in one line",
	strings.Repeat("\U0001F1EF\U0001F1F5", 30),
	"",
}

// graphemeNameMessage is a chat message wearing one of the pathological names.
func graphemeNameMessage(name, text string) youtube.Message {
	author := youtube.Author{
		ChannelID:   "UC_grapheme_00000000001",
		DisplayName: name,
		ChannelURL:  "https://www.youtube.com/@graphemetest",
		IsMember:    true,
		IsModerator: true,
	}
	return youtube.Message{
		ID:         "grapheme-1",
		LiveChatID: "chat-1",
		Timestamp:  testTimestamp,
		Author:     author,
		Badges:     youtube.BadgesForAuthor(author),
		Text:       text,
		Kind:       youtube.EventKindText,
		Type:       youtube.MessageTypeChat,
		RawType:    "textMessageEvent",
	}
}

// assertNoSplitCluster fails when a rendered string carries evidence that a
// grapheme cluster was cut in half.
//
// Three signatures, each of which is impossible in correctly sliced text and
// each of which is exactly what a byte- or rune-based slice produces:
//
//   - a cluster whose first rune is a non-spacing mark, meaning its base
//     character was left behind on the other side of the cut;
//   - a cluster that ends in a zero-width joiner, meaning the sequence it was
//     joining was truncated and the ZWJ will now bind to whatever is printed
//     next;
//   - a cluster whose first rune is a variation selector or a combining
//     enclosing keycap, which are likewise orphaned suffixes.
func assertNoSplitCluster(t *testing.T, context, value string) {
	t.Helper()
	for _, cluster := range graphemeStrings(value) {
		runes := []rune(cluster)
		if len(runes) == 0 {
			continue
		}
		first := runes[0]
		switch {
		case unicode.Is(unicode.Mn, first):
			t.Fatalf("%s: cluster %q starts with an orphaned combining mark U+%04X in %q",
				context, cluster, first, value)
		case first == '\ufe0f' || first == '\ufe0e' || first == '⃣':
			t.Fatalf("%s: cluster %q starts with an orphaned selector U+%04X in %q",
				context, cluster, first, value)
		}
		if runes[len(runes)-1] == '\u200d' {
			t.Fatalf("%s: cluster %q ends in a dangling zero-width joiner in %q",
				context, cluster, value)
		}
	}
}

// splitClusterEvidence is assertNoSplitCluster's predicate, factored out so the
// detector itself can be tested. A test whose net has no teeth passes for the
// wrong reason forever.
func splitClusterEvidence(value string) bool {
	for _, cluster := range graphemeStrings(value) {
		runes := []rune(cluster)
		if len(runes) == 0 {
			continue
		}
		first := runes[0]
		if unicode.Is(unicode.Mn, first) || first == '\ufe0f' || first == '\ufe0e' || first == '⃣' {
			return true
		}
		if runes[len(runes)-1] == '\u200d' {
			return true
		}
	}
	return false
}

// The detector must actually fire on the slices a naive implementation
// produces, and must stay quiet on correctly cut text.
func TestSplitClusterDetectorHasTeeth(t *testing.T) {
	family := "\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466"
	for _, bad := range []string{
		string([]rune(family)[:2]),     // cut after a joiner, leaving it dangling
		string([]rune(family)[1:]),     // cut before a ZWJ, orphaning the joiner
		"́",                            // NFD "é" with the base removed
		string([]rune("1\ufe0f⃣")[1:]), // keycap suffix without its digit
		string([]rune("☺\ufe0f")[1:]),  // orphaned variation selector
	} {
		if !splitClusterEvidence(bad) {
			t.Fatalf("detector missed a split cluster in %q (%U)", bad, []rune(bad))
		}
	}
	for _, good := range []string{family, "é", "1\ufe0f⃣", "☺\ufe0f", "中文", "", "ascii"} {
		if splitClusterEvidence(good) {
			t.Fatalf("detector fired on intact text %q", good)
		}
	}
}

// A row must print exactly what it measured, and never more than the width it
// was rendered at, for every pathological name and body, in every layout and
// badge mode, at every width from the supported minimum upward.
//
// Measured-versus-printed is asserted separately from the budget because they
// fail for different reasons: the first is a bug in Fragment.Width, the second
// is a bug in the wrapper's accounting.
func TestPathologicalIdentityAndBodyNeverOverflowARow(t *testing.T) {
	for _, subject := range pathologicalNames {
		for _, body := range pathologicalBodies {
			msg := graphemeNameMessage(subject.value, body)
			for _, layout := range []LayoutMode{LayoutInline, LayoutGrouped, LayoutCompact} {
				for _, badges := range []BadgeMode{BadgeModeGlyph, BadgeModeText, BadgeModeOff} {
					for width := MinimumRenderWidth; width <= 60; width++ {
						opts := testOptions(width)
						opts.Layout = layout
						opts.Badges = badges
						opts.FullUsername = true
						opts.HighlightEmoji = true

						for index, row := range Rows(msg, opts) {
							plain := row.Plain()
							if got := ansi.StringWidth(plain); got != row.Width() {
								t.Fatalf("%s/%s/%s/w=%d row %d: measured %d cells but prints %d: %q",
									subject.name, layout, badges, width, index, row.Width(), got, plain)
							}
							if row.Width() > width {
								t.Fatalf("%s/%s/%s/w=%d row %d overflowed to %d cells: %q",
									subject.name, layout, badges, width, index, row.Width(), plain)
							}
						}
					}
				}
			}
		}
	}
}

// Truncating a name or a body to fit the terminal must break between clusters.
//
// The widths swept here are the ones where the author column is squeezed hard
// enough that truncation actually runs; at a comfortable width nothing is cut
// and the test would pass without asserting anything.
func TestTruncationNeverSplitsAGraphemeCluster(t *testing.T) {
	for _, subject := range pathologicalNames {
		for _, body := range pathologicalBodies {
			msg := graphemeNameMessage(subject.value, body)
			for _, layout := range []LayoutMode{LayoutInline, LayoutGrouped, LayoutCompact} {
				for width := MinimumRenderWidth; width <= 48; width++ {
					opts := testOptions(width)
					opts.Layout = layout
					opts.FullUsername = true
					opts.HighlightEmoji = true
					for index, row := range Rows(msg, opts) {
						assertNoSplitCluster(t,
							subject.name+"/"+string(layout)+"/w="+itoa(width)+" row "+itoa(index),
							row.Plain())
					}
				}
			}
		}
	}
}

// The avatar chip is a fixed column. Whatever the name turns out to be - two
// wide CJK glyphs, a four-codepoint family emoji, or nothing printable at all -
// it must occupy exactly the cells it reserved, because every fragment to its
// right is positioned by that reservation.
func TestAvatarChipHoldsItsColumnForEveryName(t *testing.T) {
	for _, subject := range pathologicalNames {
		msg := graphemeNameMessage(subject.value, "body")
		opts := testOptions(72)
		fragment := avatarFragment(msg, opts)
		if got := fragment.Width(); got != opts.Assets.AvatarWidthCells {
			t.Fatalf("%s: avatar chip is %d cells, want the reserved %d (%q)",
				subject.name, got, opts.Assets.AvatarWidthCells, fragment.Text)
		}
		if got := ansi.StringWidth(fitCells(fragment.Text, fragment.WidthCells)); got != fragment.WidthCells {
			t.Fatalf("%s: avatar chip prints %d cells in a %d-cell column: %q",
				subject.name, got, fragment.WidthCells, fragment.Text)
		}
		assertNoSplitCluster(t, subject.name+" avatar", fragment.Text)
	}
}

// Initials are two cells of somebody's name, which makes them the smallest and
// therefore the most dangerous slice in the package.
func TestInitialsAreClusterSafeAndBounded(t *testing.T) {
	for _, subject := range pathologicalNames {
		got := initials(subject.value)
		if width := ansi.StringWidth(got); width > 2 {
			t.Fatalf("%s: initials %q are %d cells, want at most 2", subject.name, got, width)
		}
		assertNoSplitCluster(t, subject.name+" initials", got)
		// Every cluster kept must be a cluster that was actually in the name,
		// upper-cased. A cluster that appears in the initials but not in the
		// source is a slice that landed inside a sequence.
		source := make(map[string]bool)
		for _, cluster := range graphemeStrings(subject.value) {
			source[strings.ToUpper(cluster)] = true
		}
		for _, cluster := range graphemeStrings(got) {
			if !source[cluster] {
				t.Fatalf("%s: initials %q contain %q, which is not a cluster of the name",
					subject.name, got, cluster)
			}
		}
	}
}

// The three cell primitives are the foundation every other width decision sits
// on, so their contracts are pinned directly rather than only through rendered
// rows: takeCells never exceeds, truncateCells never exceeds, and fitCells is
// exactly the width asked for - including when the very first cluster is two
// cells wide and a one-cell budget therefore fits nothing at all.
func TestCellPrimitivesRespectWideClusters(t *testing.T) {
	values := []string{
		"中文测试",
		"\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466x",
		"ééé",
		"\U0001F1EF\U0001F1F5\U0001F1EF\U0001F1F5",
		"1\ufe0f⃣ 2\ufe0f⃣",
		"ascii",
	}
	for _, value := range values {
		for limit := 0; limit <= 12; limit++ {
			taken := takeCells(value, limit)
			if got := ansi.StringWidth(taken); got > limit {
				t.Fatalf("takeCells(%q, %d) = %q at %d cells", value, limit, taken, got)
			}
			assertNoSplitCluster(t, "takeCells", taken)
			if !strings.HasPrefix(value, taken) {
				t.Fatalf("takeCells(%q, %d) = %q, which is not a prefix", value, limit, taken)
			}

			truncated := truncateCells(value, limit)
			if got := ansi.StringWidth(truncated); got > limit {
				t.Fatalf("truncateCells(%q, %d) = %q at %d cells", value, limit, truncated, got)
			}
			assertNoSplitCluster(t, "truncateCells", truncated)

			fitted := fitCells(value, limit)
			if got := ansi.StringWidth(fitted); got != limit {
				t.Fatalf("fitCells(%q, %d) = %q at %d cells, want exactly %d",
					value, limit, fitted, got, limit)
			}
			assertNoSplitCluster(t, "fitCells", fitted)
		}
	}
}

// A wide first cluster and a one-cell budget is the case that tempts an
// implementation into returning half a character. It must return nothing, and
// the caller's padding must still fill the column.
func TestOneCellBudgetRefusesAWideCluster(t *testing.T) {
	if got := takeCells("中", 1); got != "" {
		t.Fatalf("takeCells(%q, 1) = %q, want the wide cluster refused entirely", "中", got)
	}
	if got := fitCells("中", 1); got != " " {
		t.Fatalf("fitCells(%q, 1) = %q, want a single pad cell", "中", got)
	}
	if got := truncateCells("中文", 3); ansi.StringWidth(got) > 3 {
		t.Fatalf("truncateCells(%q, 3) = %q, which is wider than 3", "中文", got)
	}
}

// A display name made only of zero-width characters measures nothing, which
// would leave the author column blank and the message unattributable. The
// avatar falls back rather than printing an empty chip.
func TestZeroWidthNameStillProducesAnAvatar(t *testing.T) {
	msg := graphemeNameMessage("\u200b\u200b", "body")
	if got := avatarText(msg); ansi.StringWidth(got) == 0 {
		t.Fatalf("a zero-width display name produced a zero-width avatar %q", got)
	}
}

// Sanitization runs before measurement, so a name carrying an escape sequence
// or a bidi override must be measured at its post-strip width. Measuring first
// and stripping afterward is the classic ordering bug, and it shows up as rows
// that are narrower than their budget - never wider - which is why this asserts
// the exact width rather than only the ceiling.
func TestControlSequencesInANameAreStrippedBeforeMeasuring(t *testing.T) {
	hostile := "\x1b[31mRed\x1b[0m\u202eName\u202c"
	msg := graphemeNameMessage(hostile, "body")
	name := displayAuthor(msg)
	if strings.Contains(name, "\x1b") {
		t.Fatalf("display name kept an escape sequence: %q", name)
	}
	for _, r := range name {
		if isBidiControl(r) {
			t.Fatalf("display name kept a bidi override U+%04X: %q", r, name)
		}
	}
	if got, want := ansi.StringWidth(name), len("RedName"); got != want {
		t.Fatalf("sanitized name measures %d cells, want %d: %q", got, want, name)
	}
}

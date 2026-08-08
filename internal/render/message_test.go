package render

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

func TestFragmentWidthPrefersTheReservedCells(t *testing.T) {
	fixed := Fragment{Kind: FragmentAvatar, Text: "[AL]", WidthCells: 5}
	if got := fixed.Width(); got != 5 {
		t.Fatalf("fixed fragment width = %d, want the 5 reserved cells", got)
	}
	measured := Fragment{Kind: FragmentText, Text: "hello"}
	if got := measured.Width(); got != 5 {
		t.Fatalf("measured fragment width = %d, want 5", got)
	}
	// A fragment narrower than its reservation is padded, and a wider one is
	// clipped, so the column can never move.
	if got := fragmentFallbackText(Fragment{Text: "ab", WidthCells: 5}); got != "ab   " {
		t.Fatalf("padded fallback = %q, want %q", got, "ab   ")
	}
	if got := textWidth(fragmentFallbackText(Fragment{Text: "abcdefghij", WidthCells: 5})); got != 5 {
		t.Fatalf("clipped fallback width = %d, want 5", got)
	}
}

func TestFragmentWithDefaultBackgroundFillsOnlyWhenUnset(t *testing.T) {
	withoutBackground := Fragment{Kind: FragmentText, Text: "hi", Style: FragmentStyle{Foreground: "#ffffff"}}
	filled := fragmentWithDefaultBackground(withoutBackground, "#111018")
	if filled.Style.Background != "#111018" {
		t.Fatalf("Background = %q, want #111018", filled.Style.Background)
	}
	if withoutBackground.Style.Background != "" {
		t.Fatal("fragmentWithDefaultBackground mutated the original fragment")
	}

	withBackground := Fragment{Kind: FragmentText, Text: "hi", Style: FragmentStyle{Background: "#9146ff"}}
	unchanged := fragmentWithDefaultBackground(withBackground, "#111018")
	if unchanged.Style.Background != "#9146ff" {
		t.Fatalf("Background = %q, want the explicit #9146ff preserved", unchanged.Style.Background)
	}
}

// backgroundOnlySGRCode renders a background-only fragment to learn which SGR
// code this environment's detected color profile produces, so the assertions
// below do not hardcode one profile's downsampled color.
func backgroundOnlySGRCode(t *testing.T, hex string) string {
	t.Helper()
	ref := Row{Fragments: []Fragment{{Kind: FragmentText, Text: "x", Style: FragmentStyle{Background: hex}}}}
	out := ref.TerminalString()
	start := strings.Index(out, "\x1b[")
	end := strings.Index(out, "m")
	if start < 0 || end < 0 || end <= start+2 {
		t.Fatalf("could not parse an SGR sequence out of %q", out)
	}
	return out[start+2 : end]
}

func TestTerminalStringWithBackgroundAppliesPastEmbeddedResets(t *testing.T) {
	forceColorProfile(t)
	background := "#111018"
	backgroundCode := backgroundOnlySGRCode(t, background)

	row := Row{Fragments: []Fragment{
		{Kind: FragmentText, Text: "red", Style: FragmentStyle{Foreground: "#ff0000"}},
		{Kind: FragmentText, Text: " plain"},
		{Kind: FragmentUsername, Text: "green", Style: FragmentStyle{Foreground: "#00ff00"}},
	}}

	// Plain TerminalString leaves every background empty, so each fragment's
	// own reset ends any coloring - which is the exact bug: an outer
	// Background() applied to the assembled row would only reach the first
	// reset.
	if plain := row.TerminalString(); strings.Contains(plain, backgroundCode+"m") {
		t.Fatalf("TerminalString() unexpectedly carries the background SGR code %q: %q", backgroundCode, plain)
	}

	withBg := row.TerminalStringWithBackground(background)
	if count := strings.Count(withBg, backgroundCode+"m"); count < 3 {
		t.Fatalf("TerminalStringWithBackground applied the background %d times, want once per fragment:\n%q", count, withBg)
	}
}

func TestTerminalStringWithBackgroundPreservesExplicitFragmentBackground(t *testing.T) {
	forceColorProfile(t)
	defaultCode := backgroundOnlySGRCode(t, "#111018")
	explicitCode := backgroundOnlySGRCode(t, "#9146ff")

	row := Row{Fragments: []Fragment{{Kind: FragmentAvatar, Text: "AB", Style: FragmentStyle{Background: "#9146ff"}}}}
	out := row.TerminalStringWithBackground("#111018")
	if strings.Contains(out, defaultCode+"m") {
		t.Fatalf("TerminalStringWithBackground overrode an explicit fragment background: %q", out)
	}
	if !strings.Contains(out, explicitCode+"m") {
		t.Fatalf("TerminalStringWithBackground dropped the explicit fragment background: %q", out)
	}
}

func TestRowsClampsWidthAndFillsDefaults(t *testing.T) {
	rows := Rows(chatMessage("hello"), Options{Width: 1})
	if len(rows) == 0 {
		t.Fatal("Rows returned nothing for a sub-minimum width")
	}
	for _, row := range rows {
		if row.Width() > MinimumRenderWidth {
			t.Fatalf("row %q is %d cells wide, want at most %d", row.Plain(), row.Width(), MinimumRenderWidth)
		}
	}

	// A zero palette must be filled rather than rendering colorless
	// fragments that the app would then paint inconsistently.
	filled := Rows(chatMessage("hello"), Options{Width: 60})
	var colored bool
	for _, row := range filled {
		for _, fragment := range row.Fragments {
			if fragment.Style.Foreground != "" {
				colored = true
			}
		}
	}
	if !colored {
		t.Fatal("Rows with a zero palette produced no colored fragments")
	}
}

func TestDefaultWidthAppliesWhenUnset(t *testing.T) {
	rows := Rows(chatMessage(strings.Repeat("word ", 40)), Options{Palette: testPalette()})
	for _, row := range rows {
		if row.Width() > DefaultWidth {
			t.Fatalf("row %q is %d cells wide, want at most DefaultWidth (%d)", row.Plain(), row.Width(), DefaultWidth)
		}
	}
}

func TestTextRowJoinsRowsWithNewlines(t *testing.T) {
	msg := chatMessage("a message long enough to need wrapping at a narrow width")
	joined := TextRow(msg, 32)
	if !strings.Contains(joined, "\n") {
		t.Fatalf("TextRow did not join wrapped rows: %q", joined)
	}
	if got, want := len(strings.Split(joined, "\n")), len(PlainRows(msg, 32)); got != want {
		t.Fatalf("TextRow produced %d lines, want %d", got, want)
	}
}

func TestMentionsAreAtomicAndAccented(t *testing.T) {
	opts := testOptions(72)
	fragments := splitTextFragments("hey @alice_99 look", opts)

	var mention *Fragment
	for i := range fragments {
		if fragments[i].Kind == FragmentMention {
			mention = &fragments[i]
		}
	}
	if mention == nil {
		t.Fatalf("no mention fragment in %#v", fragments)
	}
	if mention.Text != "@alice_99" {
		t.Fatalf("mention text = %q, want %q", mention.Text, "@alice_99")
	}
	if mention.Style.Foreground != opts.Palette.Accent || !mention.Style.Bold {
		t.Fatalf("mention style = %#v, want bold accent", mention.Style)
	}
	if !isAtomicFragment(*mention) {
		t.Fatal("mentions must never be split across a wrap")
	}
}

// A mid-word "@" is part of an address, not a mention of the domain.
func TestMentionRequiresAWordBoundary(t *testing.T) {
	for _, fragment := range splitTextFragments("mail me at alice@example.com", testOptions(72)) {
		if fragment.Kind == FragmentMention {
			t.Fatalf("email address produced a mention: %q", fragment.Text)
		}
	}
}

func TestShortcodesRenderAsFixedWidthChips(t *testing.T) {
	opts := testOptions(72)
	opts.HighlightEmoji = true
	fragments := splitTextFragments("nice :_heart: and :thumbsup:", opts)

	count := 0
	for _, fragment := range fragments {
		if fragment.Kind != FragmentShortcode {
			continue
		}
		count++
		if fragment.WidthCells != opts.Assets.ShortcodeCells {
			t.Fatalf("shortcode %q reserved %d cells, want %d", fragment.Text, fragment.WidthCells, opts.Assets.ShortcodeCells)
		}
		if fragment.Style.Background == "" {
			t.Fatalf("shortcode %q lost its highlight chip", fragment.Text)
		}
	}
	if count != 2 {
		t.Fatalf("found %d shortcodes in %#v, want 2", count, fragments)
	}
}

// The API never resolves shortcodes, so the scanner is all yc has; it must not
// swallow ordinary punctuation on the way.
func TestShortcodeScannerIgnoresOrdinaryColons(t *testing.T) {
	for _, text := range []string{"starts at 12:30 sharp", "note: hello", "ratio 3:2", "a::b"} {
		for _, fragment := range splitTextFragments(text, testOptions(72)) {
			if fragment.Kind == FragmentShortcode {
				t.Fatalf("%q produced a shortcode chip: %q", text, fragment.Text)
			}
		}
	}
}

func TestUnicodeEmojiBecomeFixedWidthFragments(t *testing.T) {
	opts := testOptions(72)
	opts.HighlightEmoji = true
	fragments := splitTextFragments("great stream 🎉", opts)

	found := false
	for _, fragment := range fragments {
		if fragment.Kind != FragmentEmojiFallback {
			continue
		}
		found = true
		if fragment.WidthCells != opts.Assets.EmojiWidthCells {
			t.Fatalf("emoji reserved %d cells, want %d", fragment.WidthCells, opts.Assets.EmojiWidthCells)
		}
		if fragment.Style.Background == "" {
			t.Fatal("emoji lost its highlight chip")
		}
	}
	if !found {
		t.Fatalf("no emoji fragment in %#v", fragments)
	}
}

// A ZWJ sequence is one cluster of several runes. Splitting it produces
// mojibake, so the scanner must treat it as a unit.
func TestZeroWidthJoinerEmojiStaysOneFragment(t *testing.T) {
	fragments := splitTextFragments("family 👨‍👩‍👧", testOptions(72))
	emojiCount := 0
	for _, fragment := range fragments {
		if fragment.Kind == FragmentEmojiFallback {
			emojiCount++
		}
	}
	if emojiCount != 1 {
		t.Fatalf("ZWJ sequence produced %d emoji fragments, want 1: %#v", emojiCount, fragments)
	}
}

func TestNormalizedTransportFragmentsAreHonored(t *testing.T) {
	msg := chatMessage("")
	msg.Fragments = []youtube.MessageFragment{
		{Type: youtube.FragmentText, Text: "look "},
		{Type: youtube.FragmentMention, Text: "@bob"},
		{Type: youtube.FragmentText, Text: " "},
		{Type: youtube.FragmentShortcode, Text: ":_wave:"},
		{Type: youtube.FragmentURL, Text: "https://example.test/clip"},
	}

	kinds := map[FragmentKind]int{}
	for _, row := range Rows(msg, testOptions(120)) {
		for _, fragment := range row.Fragments {
			kinds[fragment.Kind]++
		}
	}
	if kinds[FragmentMention] == 0 {
		t.Fatal("transport mention fragment was not rendered as a mention")
	}
	if kinds[FragmentShortcode] == 0 {
		t.Fatal("transport shortcode fragment was not rendered as a chip")
	}
}

func TestIdentityColorIsStableAndCaseInsensitive(t *testing.T) {
	palette := testPalette()
	first := chatMessage("hi")
	second := chatMessage("hi")
	second.Author.DisplayName = "a completely different display name"

	if usernameColor(first, palette) != usernameColor(second, palette) {
		t.Fatal("author color changed when only the display name changed; it must key on the channel ID")
	}

	upper := chatMessage("hi")
	upper.Author.ChannelID = strings.ToUpper(upper.Author.ChannelID)
	if usernameColor(first, palette) != usernameColor(upper, palette) {
		t.Fatal("author color changed with channel-ID capitalization")
	}

	// With no channel ID the display name is the only identity left.
	nameOnly := chatMessage("hi")
	nameOnly.Author.ChannelID = ""
	if usernameColor(nameOnly, palette) == "" {
		t.Fatal("author with no channel ID got no color")
	}
}

func TestUsernameColorIsReadableOnBothMessageSurfaces(t *testing.T) {
	palette := testPalette()
	for _, id := range []string{"UC_a", "UC_b", "UC_c", "UC_d", "UC_e", "UC_f", "UC_g", "UC_h"} {
		msg := chatMessage("hi")
		msg.Author.ChannelID = id
		color := usernameColor(msg, palette)
		if color == "" {
			t.Fatalf("%s got no color", id)
		}
		// theme.IdentityColor either clears the contrast bar against every
		// listed background or hands back the fallback; either way the
		// result must be one of those two outcomes, never an unreadable
		// invention.
		if color != palette.Foreground &&
			theme.ContrastCorrectedForeground(color, palette.Background, palette.Foreground) != color {
			t.Fatalf("%s color %s is unreadable on the canvas background", id, color)
		}
	}
}

func TestAvatarChipUsesInitials(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{"Alice Lovelace", "[AL]"},
		{"alice_lovelace", "[AL]"},
		{"bob", "[B]"},
		{"", "[?]"},
	} {
		msg := chatMessage("hi")
		msg.Author.DisplayName = test.name
		if test.name == "" {
			msg.Author.ChannelID = ""
			msg.Type = youtube.MessageTypeChat
		}
		got := avatarText(msg)
		if test.name == "" {
			// An authorless chat message falls back to "unknown".
			if got != "[U]" {
				t.Fatalf("avatar for an unnamed author = %q, want %q", got, "[U]")
			}
			continue
		}
		if got != test.want {
			t.Fatalf("avatar for %q = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestAvatarChipReservesItsColumn(t *testing.T) {
	opts := testOptions(72)
	opts.Assets = opts.Assets.withFallbackWidths()
	fragment := avatarFragment(chatMessage("hi"), opts)
	if fragment.WidthCells != opts.Assets.AvatarWidthCells {
		t.Fatalf("avatar reserved %d cells, want %d", fragment.WidthCells, opts.Assets.AvatarWidthCells)
	}
	if got := textWidth(fragmentFallbackText(fragment)); got != opts.Assets.AvatarWidthCells {
		t.Fatalf("avatar drew %d cells, want %d", got, opts.Assets.AvatarWidthCells)
	}
	if fragment.Style.Background == "" {
		t.Fatal("avatar chip has no identity-colored ground")
	}
}

func TestAvatarsAreSuppressedWhenDisabled(t *testing.T) {
	opts := testOptions(72)
	opts.Assets.ShowAvatars = false
	for _, row := range Rows(chatMessage("hi"), opts) {
		for _, fragment := range row.Fragments {
			if fragment.Kind == FragmentAvatar {
				t.Fatal("avatar rendered with ShowAvatars disabled")
			}
		}
	}
}

func TestFullUsernameAppendsTheChannelHandle(t *testing.T) {
	opts := testOptions(72)
	opts.FullUsername = true

	msg := chatMessage("hi")
	if got, want := usernameText(msg, opts), "Alice Lovelace (@alicelovelace)"; got != want {
		t.Fatalf("usernameText = %q, want %q", got, want)
	}

	// An ID-form channel URL exposes no handle, and nothing is the honest
	// answer - never a guess derived from the display name.
	msg.Author.ChannelURL = "https://www.youtube.com/channel/UC_alice_0000000000001"
	if got, want := usernameText(msg, opts), "Alice Lovelace"; got != want {
		t.Fatalf("usernameText with an ID-form URL = %q, want %q", got, want)
	}
}

func TestFullUsernameSkipsARedundantHandle(t *testing.T) {
	opts := testOptions(72)
	opts.FullUsername = true

	msg := chatMessage("hi")
	msg.Author.DisplayName = "AliceLovelace"
	if got, want := usernameText(msg, opts), "AliceLovelace"; got != want {
		t.Fatalf("usernameText = %q, want the handle omitted as redundant", got)
	}
}

func TestAuthorMetaOmitsWhatIsUnknown(t *testing.T) {
	opts := testOptions(72)
	if fragments := authorMetaFragments(opts); fragments != nil {
		t.Fatalf("zero AuthorMeta rendered %#v, want nothing", fragments)
	}

	opts.Meta = AuthorMeta{
		Role:            "mod",
		MemberLevelName: "Comet Crew",
		MemberMonths:    14,
		FirstSeen:       testTimestamp.Add(-90 * time.Minute),
		Now:             testTimestamp,
	}
	metaRow := Row{Fragments: authorMetaFragments(opts)}
	got := metaRow.Plain()
	for _, want := range []string{"mod", "Comet Crew", "member 14mo", "seen 1h"} {
		if !strings.Contains(got, want) {
			t.Fatalf("author meta %q missing %q", got, want)
		}
	}

	// Tenure is only knowable from a milestone event, so an unknown month
	// count must render nothing rather than "member 0mo".
	opts.Meta.MemberMonths = 0
	row := Row{Fragments: authorMetaFragments(opts)}
	if got := row.Plain(); strings.Contains(got, "member 0") {
		t.Fatalf("author meta invented a tenure: %q", got)
	}
}

func TestHumanizeDurationUsesOneUnit(t *testing.T) {
	for _, test := range []struct {
		in   time.Duration
		want string
	}{
		{-time.Hour, "0m"},
		{30 * time.Minute, "30m"},
		{5 * time.Hour, "5h"},
		{72 * time.Hour, "3d"},
		{60 * 24 * time.Hour, "2mo"},
		{800 * 24 * time.Hour, "2y"},
	} {
		if got := humanizeDuration(test.in); got != test.want {
			t.Errorf("humanizeDuration(%s) = %q, want %q", test.in, got, test.want)
		}
	}
}

// Message text is attacker-controlled and printed to a terminal that is often
// on stream, so escape sequences, control characters, and bidi overrides must
// not survive into a row.
func TestUserTextIsStrippedOfTerminalControlSequences(t *testing.T) {
	msg := chatMessage("safe \x1b[31mred\x1b[0m \x07bell\x00 \u202etxet")
	msg.Author.DisplayName = "\x1b]8;;https://evil.test\x07Alice\x1b]8;;\x07"

	joined := strings.Join(rowsToPlain(Rows(msg, testOptions(120))), "\n")
	for _, forbidden := range []string{"\x1b", "\x07", "\x00", "\u202e"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered row kept the control sequence %q: %q", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "red") {
		t.Fatalf("sanitizing removed the visible text too: %q", joined)
	}
}

func TestSanitizeUserTextKeepsNewlinesAsRowBreaks(t *testing.T) {
	if got := sanitizeUserText("a\nb"); got != "a\nb" {
		t.Fatalf("sanitizeUserText(%q) = %q, want the newline preserved", "a\nb", got)
	}
	if got := sanitizeUserText("a\tb"); got != "a b" {
		t.Fatalf("sanitizeUserText(%q) = %q, want the tab flattened to a space", "a\tb", got)
	}
}

// The wrapper measures in cells, so a row that measures at the limit must also
// print at the limit, including with wide CJK and emoji clusters.
func TestWrappedRowsMeasureAndPrintTheSameWidth(t *testing.T) {
	texts := []string{
		strings.Repeat("hello ", 20),
		strings.Repeat("こんにちは", 12),
		strings.Repeat("🎉 party ", 12),
		strings.Repeat("supercalifragilistic", 6),
	}
	for _, text := range texts {
		for width := MinimumRenderWidth; width <= 64; width += 7 {
			for _, row := range Rows(chatMessage(text), testOptions(width)) {
				if row.Width() != ansi.StringWidth(row.Plain()) {
					t.Fatalf("width %d: row measured %d but prints %d: %q",
						width, row.Width(), ansi.StringWidth(row.Plain()), row.Plain())
				}
				if row.Width() > width {
					t.Fatalf("width %d: row overflowed to %d cells: %q", width, row.Width(), row.Plain())
				}
			}
		}
	}
}

// An explicit newline in a message body is a row break, not a space.
func TestEmbeddedNewlineBreaksTheRow(t *testing.T) {
	rows := rowsToPlain(Rows(chatMessage("first\nsecond"), testOptions(72)))
	if len(rows) < 2 {
		t.Fatalf("newline did not break the row: %#v", rows)
	}
	if !strings.Contains(rows[0], "first") || !strings.Contains(rows[1], "second") {
		t.Fatalf("newline split incorrectly: %#v", rows)
	}
}

// The prefix must survive a message that opens with an atomic fragment wider
// than the space beside it; dropping it would leave an unattributed line.
func TestPrefixSurvivesALeadingAtomicFragment(t *testing.T) {
	msg := chatMessage("@a_very_long_mention_that_will_not_fit rest of the message")
	for width := 24; width <= 48; width++ {
		rows := rowsToPlain(Rows(msg, testOptions(width)))
		joined := strings.Join(rows, "\n")
		if !strings.Contains(joined, "Alice") {
			t.Fatalf("width %d dropped the author: %#v", width, rows)
		}
	}
}

func TestCoalesceAdjacentKeepsFixedWidthFragmentsSeparate(t *testing.T) {
	in := []Fragment{
		{Kind: FragmentText, Text: "a", Style: FragmentStyle{Foreground: "#fff"}},
		{Kind: FragmentText, Text: "b", Style: FragmentStyle{Foreground: "#fff"}},
		{Kind: FragmentBadge, Text: "◉", WidthCells: 2},
		{Kind: FragmentBadge, Text: "⚔", WidthCells: 2},
	}
	out := coalesceAdjacent(in)
	if len(out) != 3 {
		t.Fatalf("coalesceAdjacent produced %d fragments (%#v), want 3", len(out), out)
	}
	if out[0].Text != "ab" {
		t.Fatalf("adjacent text fragments were not merged: %q", out[0].Text)
	}
	if out[1].Width() != 2 || out[2].Width() != 2 {
		t.Fatal("fixed-width badges were merged and lost their columns")
	}
}

func TestFallbackAssetOptionsFillOnlyMissingWidths(t *testing.T) {
	custom := AssetOptions{AvatarWidthCells: 9, BadgeWidthCells: -3}
	filled := custom.withFallbackWidths()
	if filled.AvatarWidthCells != 9 {
		t.Fatalf("AvatarWidthCells = %d, want the caller's 9 preserved", filled.AvatarWidthCells)
	}
	if filled.BadgeWidthCells != 0 {
		t.Fatalf("BadgeWidthCells = %d, want a negative width clamped to 0", filled.BadgeWidthCells)
	}
	defaults := FallbackAssetOptions()
	if filled.EmojiWidthCells != defaults.EmojiWidthCells || filled.ShortcodeCells != defaults.ShortcodeCells {
		t.Fatalf("unset chip widths were not filled: %#v", filled)
	}
}

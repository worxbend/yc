package render

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/worxbend/yc/internal/emoji"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

const (
	// DefaultWidth is used when Options.Width is unset.
	DefaultWidth = 80
	// MinimumRenderWidth is the narrowest width Rows will render at; smaller
	// requests are clamped up rather than rejected.
	MinimumRenderWidth = 8
)

// FragmentKind identifies the semantic role of a render fragment.
type FragmentKind string

// The semantic roles a rendered fragment can carry.
const (
	FragmentAvatar    FragmentKind = "avatar"
	FragmentTimestamp FragmentKind = "timestamp"
	FragmentBadge     FragmentKind = "badge"
	FragmentUsername  FragmentKind = "username"
	FragmentText      FragmentKind = "text"
	FragmentMention   FragmentKind = "mention"
	// FragmentReply is the muted "reply to <author>:" prefix. YouTube live
	// chat has no parent-message field, so this reflects yc's own composed
	// "@Name " prefix rather than a threaded reply.
	FragmentReply  FragmentKind = "reply"
	FragmentNotice FragmentKind = "notice"
	// FragmentDeleted replaces a removed message body wholesale. The
	// original text is never reprinted.
	FragmentDeleted FragmentKind = "deleted"
	// FragmentEmojiFallback is a Unicode emoji cluster drawn as itself, at a
	// fixed cell width so late data cannot reflow history.
	FragmentEmojiFallback FragmentKind = "emoji_fallback"
	// FragmentShortcode is a ":name:" / ":_name:" channel-emoji literal. The
	// API resolves these to nothing, so they render as a fixed-width token.
	FragmentShortcode FragmentKind = "shortcode"
	// FragmentAmount is the Super Chat / Super Sticker / gift amount chip,
	// colored from the purchase tier via palette roles.
	FragmentAmount FragmentKind = "amount"
	// FragmentMembership is the membership level or milestone chip.
	FragmentMembership FragmentKind = "membership"
)

// FragmentStyle describes terminal styling that can be applied without changing
// a fragment's layout width.
type FragmentStyle struct {
	Foreground    string
	Background    string
	Bold          bool
	Italic        bool
	Strikethrough bool
}

// Fragment is a normalized, styled segment of a rendered chat message.
type Fragment struct {
	Kind  FragmentKind
	Text  string
	Style FragmentStyle
	// WidthCells reserves a fixed terminal width regardless of Text. It is
	// what keeps avatars, badges, emoji, and shortcode chips column-aligned
	// between rows and prevents late-arriving data from reflowing history.
	// Zero means "measure Text".
	WidthCells int
}

// Width returns the terminal cell width reserved by the fragment.
func (f Fragment) Width() int {
	if f.WidthCells > 0 {
		return f.WidthCells
	}
	return textWidth(f.Text)
}

// Row is a width-bounded collection of render fragments.
type Row struct {
	Fragments []Fragment
}

// Plain returns the row fallback text without terminal styling.
func (r Row) Plain() string {
	var builder strings.Builder
	for _, fragment := range r.Fragments {
		builder.WriteString(fragmentFallbackText(fragment))
	}
	return builder.String()
}

// String returns the row with ANSI styling applied, as styled text fallbacks.
// Avatars, badges, and emoji always render as text; there is no image
// rendering path.
func (r Row) String() string {
	var builder strings.Builder
	for _, fragment := range r.Fragments {
		builder.WriteString(renderFragment(fragment))
	}
	return builder.String()
}

// TerminalStringWithBackground behaves like String but fills every
// fragment's unset background with background instead of leaving it empty.
//
// Each fragment renders through its own style and ends in its own ANSI reset,
// so wrapping an assembled row in an outer background only colors up to the
// first embedded reset and terminals then paint their own - often transparent -
// default. Stamping an explicit background on every fragment sidesteps that,
// because explicit SGR backgrounds are opaque regardless of terminal
// transparency. Fragments that already carry a background are left untouched.
func (r Row) TerminalStringWithBackground(background string) string {
	var builder strings.Builder
	for _, fragment := range r.Fragments {
		builder.WriteString(renderFragment(fragmentWithDefaultBackground(fragment, background)))
	}
	return builder.String()
}

// Width returns the terminal cell width reserved by the row.
func (r Row) Width() int {
	return fragmentsWidth(r.Fragments)
}

func fragmentWithDefaultBackground(fragment Fragment, background string) Fragment {
	if fragment.Style.Background == "" {
		fragment.Style.Background = background
	}
	return fragment
}

// LayoutMode selects how a message's author and text are arranged.
type LayoutMode string

const (
	// LayoutInline puts the author and the message on one row. Densest
	// layout; every row is self-describing, which suits fast-moving chat.
	LayoutInline LayoutMode = "inline"
	// LayoutGrouped puts the author on their own header row with the text
	// indented beneath, and omits the header entirely for consecutive
	// messages by the same author (see Options.ContinuesGroup).
	LayoutGrouped LayoutMode = "grouped"
	// LayoutCompact drops avatars, timestamps, and badges, leaving
	// "Name: message". For narrow terminals and side-by-side panes.
	LayoutCompact LayoutMode = "compact"
)

// DefaultLayoutMode is used when a config value is missing or unrecognized.
const DefaultLayoutMode = LayoutInline

// NormalizeLayoutMode maps a config string onto a known layout, falling back to
// DefaultLayoutMode so an unrecognized value degrades instead of failing.
func NormalizeLayoutMode(value string) LayoutMode {
	switch LayoutMode(strings.ToLower(strings.TrimSpace(value))) {
	case LayoutInline:
		return LayoutInline
	case LayoutGrouped:
		return LayoutGrouped
	case LayoutCompact:
		return LayoutCompact
	default:
		return DefaultLayoutMode
	}
}

// AuthorMeta is per-author context the renderer cannot derive from a message on
// its own. The app resolves it from its per-chat roster and hands it in per
// message.
//
// Every field is optional: a zero AuthorMeta renders no metadata rather than
// claiming an author has no roles. An absent value means "no data", never a
// negative fact.
type AuthorMeta struct {
	// Role is the highest-precedence role label ("owner", "mod", "member")
	// or empty.
	Role string
	// MemberMonths is membership tenure, or 0 when unknown. YouTube only
	// reveals it through milestone events, so it is usually unknown.
	MemberMonths int
	// MemberLevelName is the membership tier name, or empty.
	MemberLevelName string
	// FirstSeen is when yc first saw this author in this session. It is not
	// when they arrived in the chat - that is not knowable from this API.
	FirstSeen time.Time
	// Now anchors relative durations. Zero means "use the wall clock", which
	// tests override for determinism.
	Now time.Time
}

func (a AuthorMeta) empty() bool {
	return strings.TrimSpace(a.Role) == "" &&
		a.MemberMonths == 0 &&
		strings.TrimSpace(a.MemberLevelName) == "" &&
		a.FirstSeen.IsZero()
}

// now is the instant relative durations are measured from, truncated to the
// minute.
//
// The truncation is what keeps a repainting chat cheap. Rendered rows are
// cached by their inputs, and a clock that ticks every frame would make every
// row's inputs different every frame, so nothing would ever hit the cache. The
// renderer does this itself rather than asking callers to pass a rounded time,
// because a caller that forgets costs a full re-render and nothing says so.
func (a AuthorMeta) now() time.Time {
	if a.Now.IsZero() {
		return time.Now().Truncate(time.Minute)
	}
	return a.Now.Truncate(time.Minute)
}

// AssetOptions controls the fixed widths of the text chips that stand in for
// imagery yc never fetches.
type AssetOptions struct {
	ShowAvatars      bool
	AvatarWidthCells int
	BadgeWidthCells  int
	EmojiWidthCells  int
	ShortcodeCells   int
}

// FallbackAssetOptions returns the default chip widths.
func FallbackAssetOptions() AssetOptions {
	return AssetOptions{
		ShowAvatars:      true,
		AvatarWidthCells: 5,
		BadgeWidthCells:  BadgeTextWidth,
		EmojiWidthCells:  2,
		ShortcodeCells:   6,
	}
}

// withFallbackWidths fills unset chip widths from the defaults and clamps
// negative ones to zero, so a partially populated AssetOptions - which is what
// a config round trip produces - still reserves sane columns.
func (o AssetOptions) withFallbackWidths() AssetOptions {
	defaults := FallbackAssetOptions()
	if o.AvatarWidthCells <= 0 {
		o.AvatarWidthCells = defaults.AvatarWidthCells
	}
	if o.EmojiWidthCells <= 0 {
		o.EmojiWidthCells = defaults.EmojiWidthCells
	}
	if o.ShortcodeCells <= 0 {
		o.ShortcodeCells = defaults.ShortcodeCells
	}
	if o.BadgeWidthCells < 0 {
		o.BadgeWidthCells = 0
	}
	return o
}

// Options controls one message render.
type Options struct {
	Width   int
	Palette theme.Palette
	Assets  AssetOptions
	Layout  LayoutMode
	Badges  BadgeMode
	// ContinuesGroup marks a message as following another by the same
	// author. LayoutGrouped uses it to suppress the repeated author header.
	ContinuesGroup bool
	// FullUsername renders "DisplayName (handle)" when the two differ by
	// more than capitalization, so a stylized display name stays traceable.
	FullUsername bool
	// HighlightEmoji draws emoji and shortcode chips on a tinted background
	// so they separate visually from surrounding text.
	HighlightEmoji bool
	// Meta is optional author context rendered beside the username.
	Meta AuthorMeta
}

// RenderOptions is an alias for Options. Both names appear in the design specs;
// they are the same type so no consumer can pick the wrong one.
//
//nolint:revive // the alias intentionally repeats the package name; both spellings appear in the design specs and must stay interchangeable
type RenderOptions = Options

// DefaultOptions returns Options for width with the default palette, layout,
// badge mode, and chip widths.
func DefaultOptions(width int) Options {
	return Options{
		Width:   width,
		Palette: theme.DefaultPalette(),
		Assets:  FallbackAssetOptions(),
		Layout:  DefaultLayoutMode,
		Badges:  DefaultBadgeMode,
	}
}

func (o Options) layout() LayoutMode {
	return NormalizeLayoutMode(string(o.Layout))
}

func (o Options) badgeMode() BadgeMode {
	return NormalizeBadgeMode(string(o.Badges))
}

// chipHighlightBlend is how far a highlighted chip background is pulled from
// the message surface toward a role color. It is deliberately small: the chip
// must read as a raised surface behind the token, not as a block of solid color
// that overwhelms the text drawn on it.
const chipHighlightBlend = 0.22

// emojiHighlight returns the tinted background for emoji chips, or "" when
// highlighting is off. Blending from the theme's own surface keeps the chip
// legible in every preset instead of hard-coding a color that only works on
// dark themes.
func (o Options) emojiHighlight() string {
	if !o.HighlightEmoji {
		return ""
	}
	return theme.Mix(o.Palette.Surface, o.Palette.Warning, chipHighlightBlend)
}

// shortcodeHighlight mirrors emojiHighlight for channel-emoji shortcodes,
// tinted toward the accent hue so an unresolvable ":_name:" token stays
// distinguishable from a Unicode emoji that actually drew.
func (o Options) shortcodeHighlight() string {
	if !o.HighlightEmoji {
		return ""
	}
	return theme.Mix(o.Palette.Surface, o.Palette.Accent, chipHighlightBlend)
}

// Rows renders one normalized message into width-bounded rows. It is the single
// entry point into this package: Width is clamped to at least
// MinimumRenderWidth, a zero Palette is filled with the default, and zero chip
// widths are filled from FallbackAssetOptions.
func Rows(msg youtube.Message, opts Options) []Row {
	if opts.Width <= 0 {
		opts.Width = DefaultWidth
	}
	if opts.Width < MinimumRenderWidth {
		opts.Width = MinimumRenderWidth
	}
	if opts.Palette == (theme.Palette{}) {
		opts.Palette = theme.DefaultPalette()
	}
	opts.Assets = opts.Assets.withFallbackWidths()

	// An authorless row has no name to head a group with, so grouping it
	// would spend a whole row on a bare clock.
	if opts.layout() == LayoutGrouped && hasAuthor(msg) {
		return groupedRows(msg, opts)
	}

	prefix := messagePrefix(msg, opts)
	content := messageContent(msg, opts)
	rows := wrap(prefix, content, opts.Width)
	if len(rows) == 0 {
		return []Row{{Fragments: prefix}}
	}
	return rows
}

// groupedRows renders LayoutGrouped: an author header row followed by the
// message text indented beneath it. Consecutive messages from the same author
// (Options.ContinuesGroup) skip the header, so a run of messages reads as one
// block under a single name.
func groupedRows(msg youtube.Message, opts Options) []Row {
	indent := groupedIndentWidth(opts.Width)
	var rows []Row
	if !opts.ContinuesGroup {
		header := newWrapper(opts.Width)
		header.setIndent(indent)
		header.add(groupedHeaderFragments(msg, opts))
		rows = header.finish()
	}

	body := newWrapper(opts.Width)
	body.add([]Fragment{{Kind: FragmentText, Text: strings.Repeat(" ", indent)}})
	body.setIndent(indent)
	body.add(messageContent(msg, opts))
	return append(rows, body.finish()...)
}

// groupedIndentWidth is how far grouped message text is inset under its author
// header. It scales down on narrow terminals so the indent never eats the
// message.
func groupedIndentWidth(width int) int {
	switch {
	case width >= 40:
		return 3
	case width >= 20:
		return 2
	default:
		return 0
	}
}

// groupedHeaderFragments builds the author row for LayoutGrouped: avatar chip,
// username, badges, then muted metadata (timestamp and author context).
//
// The username carries the heading weight here - a terminal cannot vary font
// size, so the visual hierarchy between "who" and "what" comes from putting the
// name on its own bold, colored row above unemphasized body text.
func groupedHeaderFragments(msg youtube.Message, opts Options) []Fragment {
	var fragments []Fragment
	if opts.Assets.ShowAvatars && opts.Width >= minWidthWideChip {
		fragments = append(fragments, avatarFragment(msg, opts))
	}
	fragments = append(fragments, usernameFragment(msg, opts))
	badged := false
	if badges := badgeFragments(msg, opts); len(badges) > 0 && opts.Width >= minWidthNarrowChip {
		fragments = append(fragments, Fragment{Kind: FragmentText, Text: " "})
		fragments = append(fragments, badges...)
		badged = true
	}
	if opts.Width >= minWidthGroupedClock {
		// Badge fragments already reserve a trailing pad cell, so adding
		// another separator here would open a two-cell gap in front of the
		// clock on exactly the rows that carry a badge.
		separator := " "
		if badged {
			separator = ""
		}
		fragments = append(fragments, Fragment{
			Kind:  FragmentTimestamp,
			Text:  separator + timestampText(msg.Timestamp),
			Style: FragmentStyle{Foreground: opts.Palette.Muted},
		})
	}
	if opts.Width >= minWidthAuthorMeta {
		fragments = append(fragments, authorMetaFragments(opts)...)
	}
	return fragments
}

// PlainRows renders msg at width and returns the unstyled fallback text of each
// row. It backs golden tests and non-interactive output.
func PlainRows(msg youtube.Message, width int) []string {
	rows := Rows(msg, DefaultOptions(width))
	plain := make([]string, 0, len(rows))
	for _, row := range rows {
		plain = append(plain, row.Plain())
	}
	return plain
}

// StringRows renders msg at width and returns each row with ANSI styling.
func StringRows(msg youtube.Message, width int) []string {
	rows := Rows(msg, DefaultOptions(width))
	rendered := make([]string, 0, len(rows))
	for _, row := range rows {
		rendered = append(rendered, row.String())
	}
	return rendered
}

// TextRow renders msg at width and joins the rows with newlines.
func TextRow(msg youtube.Message, width int) string {
	return strings.Join(PlainRows(msg, width), "\n")
}

// Width breakpoints for the decorations around a message. A terminal narrower
// than a breakpoint drops that decoration rather than letting it crowd out the
// author's name or the message text.
//
// The two layouts share the tiers but not the order they spend them in: the
// inline prefix buys the avatar at the narrow tier and badges at the wide one,
// while the grouped header does the reverse, because a grouped header already
// gives the name a row of its own and can afford badges sooner.
const (
	minWidthTimestamp    = 16
	minWidthNarrowChip   = 24
	minWidthWideChip     = 28
	minWidthGroupedClock = 30
	minWidthAuthorMeta   = 46
)

// timestampWidth is the column budget messagePrefix reserves for the clock,
// including the space that separates it from what follows. It has to match what
// timestampText produces - TestTimestampWidthMatchesFormat holds the two
// together, so changing the format fails a test instead of silently
// overspending the row.
const timestampWidth = len("15:04") + 1

// messagePrefix builds the inline "[AB] 12:00 ◉⚔ Name: " run.
//
// The decorations are budgeted before anything is built and dropped in order -
// badges, then avatar, then timestamp - until the author's name fits. The name
// is the one part that cannot be sacrificed: a row nobody can attribute is
// worse than a row with no clock on it.
func messagePrefix(msg youtube.Message, opts Options) []Fragment {
	// Room-state events - members-only mode, chat ended, a tombstone yc never
	// saw the original of - belong to nobody. Printing "notice:" in the author
	// column would spend the widest part of the row restating the "[notice]"
	// marker the body already carries.
	if !hasAuthor(msg) {
		if opts.Width < minWidthTimestamp || opts.layout() == LayoutCompact {
			return nil
		}
		return []Fragment{{
			Kind:  FragmentTimestamp,
			Text:  timestampText(msg.Timestamp) + " ",
			Style: FragmentStyle{Foreground: opts.Palette.Muted},
		}}
	}

	author := usernameText(msg, opts)
	includeTimestamp := opts.Width >= minWidthTimestamp
	includeBadges := opts.Width >= minWidthWideChip && len(msg.Badges) > 0 && opts.badgeMode() != BadgeModeOff
	includeAvatar := opts.Assets.ShowAvatars && opts.Width >= minWidthNarrowChip
	// LayoutCompact trades every decoration for message text.
	if opts.layout() == LayoutCompact {
		includeTimestamp, includeBadges, includeAvatar = false, false, false
	}

	for {
		fixedWidth := 2 // the ": " separator
		if includeTimestamp {
			fixedWidth += timestampWidth
		}
		if includeBadges {
			fixedWidth += badgeSetWidth(msg.Badges, opts)
		}
		if includeAvatar {
			fixedWidth += opts.Assets.AvatarWidthCells
		}

		if fixedWidth+textWidth(author) <= opts.Width || (!includeAvatar && !includeBadges && !includeTimestamp) {
			break
		}
		if includeBadges {
			includeBadges = false
			continue
		}
		if includeAvatar {
			includeAvatar = false
			continue
		}
		includeTimestamp = false
	}

	var fragments []Fragment
	if includeAvatar {
		fragments = append(fragments, avatarFragment(msg, opts))
	}
	if includeTimestamp {
		fragments = append(fragments, Fragment{
			Kind:  FragmentTimestamp,
			Text:  timestampText(msg.Timestamp) + " ",
			Style: FragmentStyle{Foreground: opts.Palette.Muted},
		})
	}
	if includeBadges {
		fragments = append(fragments, badgeFragments(msg, opts)...)
	}
	fragments = append(fragments, usernameFragment(msg, opts))
	fragments = append(fragments, Fragment{
		Kind:  FragmentText,
		Text:  ": ",
		Style: FragmentStyle{Foreground: opts.Palette.Foreground},
	})
	return fragments
}

// messageContent builds everything after the author: the deletion placeholder,
// the notice marker, the event chips, and the body text.
//
// This is the one place that coalesces: every list it returns has had
// same-styled neighbors merged. The helpers it calls append freely and leave
// the merging to the end, so a run of text split across two spans still
// arrives as a single styled run.
func messageContent(msg youtube.Message, opts Options) []Fragment {
	// A deletion means the words leave the screen. yc runs on terminals that
	// are frequently on stream, so a removed message is never reprinted -
	// not in a strikethrough, not in an inspect panel. The original text
	// survives only in the debug log.
	if msg.Deleted || msg.Kind == youtube.EventKindTombstone {
		return []Fragment{{
			Kind: FragmentDeleted,
			Text: "[message deleted]",
			Style: FragmentStyle{
				Foreground:    opts.Palette.Muted,
				Italic:        true,
				Strikethrough: true,
			},
		}}
	}

	var fragments []Fragment
	if noticeStyled(msg) {
		fragments = append(fragments, Fragment{
			Kind: FragmentNotice,
			Text: "[notice] ",
			Style: FragmentStyle{
				Foreground: opts.Palette.Warning,
				Bold:       true,
			},
		})
	}

	if decoration := decorateEvent(msg, opts); decoration.Recognized {
		fragments = append(fragments, decoration.Fragments...)
		if body := splitTextFragments(decoration.Body, opts); len(body) > 0 {
			if len(decoration.Fragments) > 0 {
				fragments = append(fragments, spacerFragment(opts))
			}
			fragments = append(fragments, body...)
		}
		return coalesceAdjacent(fragments)
	}

	// Ordinary chat, and any event yc did not recognize. The transport
	// already split the body into semantic spans; the raw-text path exists
	// for locally composed rows and for fixtures that carry text only.
	if len(msg.Fragments) > 0 {
		fragments = append(fragments, normalizedFragments(msg.Fragments, opts)...)
		return coalesceAdjacent(fragments)
	}
	fragments = append(fragments, splitTextFragments(msg.Text, opts)...)
	return coalesceAdjacent(fragments)
}

// normalizedFragments renders the spans internal/youtube produced. Unknown span
// types fall through to a local rescan rather than being dropped, so a fragment
// type added to the transport still renders before this package learns it.
func normalizedFragments(in []youtube.MessageFragment, opts Options) []Fragment {
	var out []Fragment
	for _, fragment := range in {
		text := sanitizeUserText(fragment.Text)
		if text == "" {
			continue
		}
		switch fragment.Type {
		case youtube.FragmentMention:
			out = append(out, mentionFragment(text, opts))
		case youtube.FragmentEmoji:
			out = append(out, emojiFragment(text, opts))
		case youtube.FragmentShortcode:
			out = append(out, shortcodeFragment(text, opts))
		case youtube.FragmentURL:
			out = append(out, urlFragment(text, opts))
		default:
			out = append(out, splitTextFragments(text, opts)...)
		}
	}
	return out
}

// splitTextFragments scans plain text for the spans the API never marks up:
// mentions, Unicode emoji, and ":_shortcode:" channel-emoji literals.
func splitTextFragments(text string, opts Options) []Fragment {
	text = sanitizeUserText(text)
	if text == "" {
		return nil
	}

	var fragments []Fragment
	var textBuffer strings.Builder
	flushText := func() {
		if textBuffer.Len() == 0 {
			return
		}
		fragments = append(fragments, Fragment{
			Kind:  FragmentText,
			Text:  textBuffer.String(),
			Style: FragmentStyle{Foreground: opts.Palette.Foreground},
		})
		textBuffer.Reset()
	}

	graphemes := graphemeStrings(text)
	for i := 0; i < len(graphemes); {
		cluster := graphemes[i]
		if cluster == "@" && startsWord(graphemes, i) && i+1 < len(graphemes) && isMentionPart(graphemes[i+1]) {
			flushText()
			start := i
			i += 2
			for i < len(graphemes) && isMentionPart(graphemes[i]) {
				i++
			}
			fragments = append(fragments, mentionFragment(strings.Join(graphemes[start:i], ""), opts))
			continue
		}
		if end, ok := shortcodeEnd(graphemes, i); ok {
			flushText()
			fragments = append(fragments, shortcodeFragment(strings.Join(graphemes[i:end], ""), opts))
			i = end
			continue
		}
		if isEmojiCluster(cluster) {
			flushText()
			fragments = append(fragments, emojiFragment(cluster, opts))
			i++
			continue
		}
		textBuffer.WriteString(cluster)
		i++
	}
	flushText()
	return fragments
}

func mentionFragment(text string, opts Options) Fragment {
	return Fragment{
		Kind: FragmentMention,
		Text: text,
		Style: FragmentStyle{
			Foreground: opts.Palette.Accent,
			Bold:       true,
		},
	}
}

func emojiFragment(cluster string, opts Options) Fragment {
	return Fragment{
		Kind:       FragmentEmojiFallback,
		Text:       cluster,
		WidthCells: opts.Assets.EmojiWidthCells,
		Style: FragmentStyle{
			Foreground: opts.Palette.Foreground,
			Background: opts.emojiHighlight(),
		},
	}
}

// shortcodeFragment renders a channel-emoji literal as a fixed-width token.
//
// The API returns no emote metadata at all, so ":_heart:" is as far as yc will
// ever resolve it. A fixed cell budget keeps the token from reflowing the row
// and makes a message full of them still scan as text.
func shortcodeFragment(token string, opts Options) Fragment {
	return Fragment{
		Kind:       FragmentShortcode,
		Text:       token,
		WidthCells: opts.Assets.ShortcodeCells,
		Style: FragmentStyle{
			Foreground: opts.Palette.Success,
			Background: opts.shortcodeHighlight(),
			Bold:       true,
		},
	}
}

// urlFragment colors a link without reserving a fixed width: a URL is real text
// a viewer may want to read or copy, so it wraps like prose rather than being
// clipped into a chip.
func urlFragment(text string, opts Options) Fragment {
	return Fragment{
		Kind:  FragmentText,
		Text:  text,
		Style: FragmentStyle{Foreground: opts.Palette.Accent},
	}
}

// usernameFragment renders the author name in their stable identity color.
func usernameFragment(msg youtube.Message, opts Options) Fragment {
	return Fragment{
		Kind: FragmentUsername,
		Text: usernameText(msg, opts),
		Style: FragmentStyle{
			Foreground: usernameColor(msg, opts.Palette),
			Bold:       true,
		},
	}
}

// usernameText is the author label, optionally suffixed with the channel handle.
//
// FullUsername appends the handle when the display name is not simply a
// recapitalization of it: YouTube display names are free-form and duplicable,
// so a stylized or impersonating name is otherwise impossible to tie back to an
// account.
func usernameText(msg youtube.Message, opts Options) string {
	author := displayAuthor(msg)
	if !opts.FullUsername {
		return author
	}
	handle := authorHandle(msg.Author)
	if handle == "" || strings.EqualFold(handle, author) || strings.EqualFold("@"+author, handle) {
		return author
	}
	return author + " (" + handle + ")"
}

// authorHandle recovers the "@name" handle from the author's channel URL, which
// is the only place authorDetails exposes it. An ID-form channel URL yields
// nothing, and nothing is the honest answer.
func authorHandle(author youtube.Author) string {
	url := sanitizeUserText(strings.TrimSpace(author.ChannelURL))
	index := strings.Index(url, "/@")
	if index < 0 {
		return ""
	}
	handle := url[index+1:]
	if cut := strings.IndexAny(handle, "/?#"); cut >= 0 {
		handle = handle[:cut]
	}
	if handle == "@" {
		return ""
	}
	return handle
}

// hasAuthor reports whether a message names a person. Room-state events and
// locally composed system rows do not, and the renderer drops the author column
// for them rather than inventing a placeholder chatter.
func hasAuthor(msg youtube.Message) bool {
	return strings.TrimSpace(msg.Author.DisplayName) != "" ||
		strings.TrimSpace(msg.Author.ChannelID) != ""
}

func displayAuthor(msg youtube.Message) string {
	if name := sanitizeUserText(strings.TrimSpace(msg.Author.DisplayName)); name != "" {
		return name
	}
	if id := strings.TrimSpace(msg.Author.ChannelID); id != "" {
		return id
	}
	switch msg.Kind {
	case youtube.EventKindTombstone, youtube.EventKindMessageDeleted, youtube.EventKindMessageRetracted:
		// A removal yc never saw the original of has no author to name,
		// and inventing "unknown" for it reads as a chatter called that.
		return "removed"
	}
	switch msg.Type {
	case youtube.MessageTypeNotice:
		return "notice"
	case youtube.MessageTypeSystem:
		return "system"
	default:
		return "unknown"
	}
}

// usernameColor is the author's stable identity color.
//
// YouTube supplies no author color, so it is derived from the channel ID - the
// one identifier a chatter cannot change - and falls back to the display name
// only when the ID is missing. Nothing here is stateful: the same author is the
// same color in every session and in every yc instance, with no per-user color
// map to keep in sync.
func usernameColor(msg youtube.Message, palette theme.Palette) string {
	identity := strings.ToLower(msg.Author.Identity())
	return theme.IdentityColor(identity, []string{palette.Background, palette.Surface}, palette.Foreground)
}

// avatarFragment renders the author as an initials chip on their identity
// color. YouTube supplies a profileImageUrl and yc never fetches it: this is a
// terminal client, and a stable two-letter chip identifies a repeat chatter
// faster than a 2x2-cell image ever could.
func avatarFragment(msg youtube.Message, opts Options) Fragment {
	return Fragment{
		Kind:       FragmentAvatar,
		Text:       avatarText(msg),
		WidthCells: opts.Assets.AvatarWidthCells,
		Style: FragmentStyle{
			Foreground: theme.ContrastCorrectedForeground(opts.Palette.Background, usernameColor(msg, opts.Palette), opts.Palette.Foreground),
			Background: usernameColor(msg, opts.Palette),
			Bold:       true,
		},
	}
}

func avatarText(msg youtube.Message) string {
	return "[" + emptyFallback(initials(displayAuthor(msg)), "?") + "]"
}

// initials takes the first grapheme of up to two words, so "Ada Lovelace" and
// "ada_lovelace" both read as "AL". Clusters, not runes: an emoji or a
// combining-mark name must not be cut in half.
func initials(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	words := strings.FieldsFunc(value, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || unicode.IsSpace(r)
	})
	if len(words) == 0 {
		words = []string{value}
	}
	var builder strings.Builder
	for _, word := range words {
		if word == "" {
			continue
		}
		for _, cluster := range graphemeStrings(word) {
			builder.WriteString(strings.ToUpper(cluster))
			break
		}
		if textWidth(builder.String()) >= 2 {
			break
		}
	}
	return takeCells(builder.String(), 2)
}

func timestampText(timestamp time.Time) string {
	if timestamp.IsZero() {
		return "--:--"
	}
	return timestamp.Local().Format("15:04")
}

// authorMetaFragments renders the muted author context: role, membership level,
// tenure, and how long yc has seen this person.
//
// Anything yc does not actually know is omitted rather than guessed. YouTube
// only reveals membership tenure through milestone events, so an absent "6mo"
// means "no data", never "joined this month".
func authorMetaFragments(opts Options) []Fragment {
	meta := opts.Meta
	if meta.empty() {
		return nil
	}
	parts := make([]string, 0, 4)
	if role := strings.TrimSpace(meta.Role); role != "" {
		parts = append(parts, role)
	}
	if level := sanitizeUserText(strings.TrimSpace(meta.MemberLevelName)); level != "" {
		parts = append(parts, level)
	}
	if meta.MemberMonths > 0 {
		parts = append(parts, "member "+strconv.Itoa(meta.MemberMonths)+"mo")
	}
	if !meta.FirstSeen.IsZero() {
		parts = append(parts, "seen "+humanizeDuration(meta.now().Sub(meta.FirstSeen)))
	}
	if len(parts) == 0 {
		return nil
	}
	return []Fragment{{
		Kind: FragmentText,
		Text: " · " + strings.Join(parts, " · "),
		Style: FragmentStyle{
			Foreground: opts.Palette.Muted,
			Italic:     true,
		},
	}}
}

// humanizeDuration renders an age at one significant unit ("3mo", "5d", "2h").
// Chat metadata only needs a sense of scale, and a single short unit keeps the
// header from crowding out the message.
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	case d < 30*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	case d < 365*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/(24*30))) + "mo"
	default:
		return strconv.Itoa(int(d.Hours()/(24*365))) + "y"
	}
}

// minimumWrapColumns is the narrowest usable text column. Below it, wrapping
// stops producing readable lines, so the hanging indent gives way instead.
const minimumWrapColumns = 16

// wrap lays prefix and content out at width, hanging every continuation row
// under the prefix so wrapped text stays visually attached to its author.
func wrap(prefix, content []Fragment, width int) []Row {
	if width <= 0 {
		return nil
	}

	indentWidth := fragmentsWidth(prefix)
	// Cap the hanging indent so a continuation row always has room for whole
	// words. On a narrow terminal the prefix can be most of the row, and
	// hanging text under a 28-cell prefix at width 32 leaves four columns to
	// wrap into, which shreds prose into a vertical ribbon of syllables.
	if indentWidth > 0 && width-indentWidth < minimumWrapColumns {
		indentWidth = width - minimumWrapColumns
	}
	if indentWidth < 0 {
		indentWidth = 0
	}
	if indentWidth >= width {
		indentWidth = width / 2
	}

	w := newWrapper(width)
	w.add(prefix)
	w.setIndent(indentWidth)
	w.add(content)
	return w.finish()
}

// wrapper lays fragments out into rows of a fixed width. It owns the row still
// under construction and the columns that row has used, so a caller can hand it
// several fragment lists in turn without threading that state itself.
//
// indentWidth is the hanging indent continuation rows start at. Callers change
// it between passes: an author prefix wraps from column zero, and the message
// text that follows hangs underneath it.
type wrapper struct {
	rows        []Row
	current     Row
	used        int
	width       int
	indentWidth int
}

func newWrapper(width int) *wrapper {
	return &wrapper{rows: make([]Row, 0, 2), width: width}
}

// setIndent changes the hanging indent used from the next wrap onwards. The row
// being built is left alone - it was laid out under the previous indent.
func (w *wrapper) setIndent(indentWidth int) {
	w.indentWidth = indentWidth
}

// finish emits the row still under construction and returns every row. The
// wrapper is not usable afterwards.
func (w *wrapper) finish() []Row {
	return append(w.rows, w.current)
}

func (w *wrapper) breakRow(indentWidth int) {
	w.rows = append(w.rows, w.current)
	w.current = continuationRow(indentWidth)
	w.used = indentWidth
}

func (w *wrapper) add(fragments []Fragment) {
	width, indentWidth := w.width, w.indentWidth
	for _, fragment := range fragments {
		if fragment.WidthCells > 0 || isAtomicFragment(fragment) {
			fragmentWidth := fragment.Width()
			if fragmentWidth == 0 {
				continue
			}
			if w.used+fragmentWidth > width && w.used > indentWidth {
				w.breakRow(indentWidth)
			}
			if w.used+fragmentWidth > width && w.used == indentWidth && w.used > 0 && fragmentWidth <= width {
				// The fragment cannot fit beside the indent but would fit
				// on a full-width row, so give up the indent.
				//
				// The row being abandoned must be emitted first if it holds
				// anything real. On the content pass indentWidth is exactly
				// the prefix width, so used == indentWidth is also true on
				// the very first row - where the row holds the timestamp,
				// badges, and author name. Discarding it unconditionally
				// would drop the whole prefix whenever a message opened
				// with a long mention or an amount chip, leaving an
				// unattributed line in chat at ordinary terminal widths.
				if rowHasContent(w.current) {
					w.rows = append(w.rows, w.current)
				}
				w.current = Row{}
				w.used = 0
			}
			if w.used+fragmentWidth <= width {
				w.current.Append(fragment)
				w.used += fragmentWidth
				continue
			}
		}

		// Prefer a break between words. Chat is prose, and breaking mid-word
		// makes it materially harder to read at the speed a busy chat moves.
		// A word wider than the line still falls through to
		// cluster-by-cluster wrapping below, so nothing becomes unrenderable.
		for _, chunk := range wrapChunks(fragment.Text) {
			chunkWidth := textWidth(chunk)
			if chunkWidth > 0 && chunkWidth <= width-indentWidth &&
				w.used+chunkWidth > width && w.used > indentWidth &&
				strings.TrimSpace(chunk) != "" {
				w.breakRow(indentWidth)
			}
			w.addClusters(fragment, chunk)
		}
	}
}

// wrapChunks splits text into word-sized pieces, each a run of non-space
// clusters together with the spaces that follow it. Keeping the trailing spaces
// attached means a break taken before a chunk lands between words rather than
// stranding a space at the start of the next row.
func wrapChunks(text string) []string {
	if text == "" {
		return nil
	}
	var chunks []string
	var current strings.Builder
	inTrailingSpace := false
	for _, cluster := range graphemeStrings(text) {
		if cluster == "\n" {
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			chunks = append(chunks, cluster)
			inTrailingSpace = false
			continue
		}
		isSpace := strings.TrimSpace(cluster) == ""
		if !isSpace && inTrailingSpace {
			chunks = append(chunks, current.String())
			current.Reset()
			inTrailingSpace = false
		}
		current.WriteString(cluster)
		if isSpace {
			inTrailingSpace = true
		}
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks
}

// addClusters is the fallback for text that will not fit a row whole: it lays
// the chunk out one grapheme cluster at a time, breaking wherever the row runs
// out of columns.
func (w *wrapper) addClusters(fragment Fragment, text string) {
	width, indentWidth := w.width, w.indentWidth
	for _, cluster := range graphemeStrings(text) {
		if cluster == "\n" {
			w.breakRow(indentWidth)
			continue
		}

		clusterWidth := textWidth(cluster)
		if w.used+clusterWidth > width && w.used > indentWidth {
			w.breakRow(indentWidth)
			if strings.TrimSpace(cluster) == "" {
				continue
			}
		}
		if w.used+clusterWidth > width && w.used == indentWidth && w.used > 0 {
			w.breakRow(0)
		}

		next := fragment
		next.Text = cluster
		next.WidthCells = 0
		w.current.Append(next)
		w.used += clusterWidth
	}
}

// isAtomicFragment reports the fragments that must move to the next row whole.
// A mention, an emoji, or a price chip split across a wrap stops being the
// thing it is; ordinary prose does not.
func isAtomicFragment(fragment Fragment) bool {
	switch fragment.Kind {
	case FragmentMention, FragmentEmojiFallback, FragmentShortcode, FragmentAmount, FragmentMembership:
		return true
	default:
		return false
	}
}

func continuationRow(indentWidth int) Row {
	if indentWidth <= 0 {
		return Row{}
	}
	return Row{Fragments: []Fragment{{
		Kind: FragmentText,
		Text: strings.Repeat(" ", indentWidth),
	}}}
}

// Append adds fragment to the row, merging it into the previous fragment when
// the two carry the same kind and style. Empty text is dropped: a fragment with
// nothing in it would only add a styled run nobody can see.
//
// Every builder of rows goes through here, in this package and in
// internal/animation, so the merge rule stays in one place.
func (r *Row) Append(fragment Fragment) {
	if fragment.Text == "" {
		return
	}
	lastIndex := len(r.Fragments) - 1
	if lastIndex >= 0 && mergeableFragments(r.Fragments[lastIndex], fragment) {
		r.Fragments[lastIndex].Text += fragment.Text
		return
	}
	r.Fragments = append(r.Fragments, fragment)
}

// coalesceAdjacent merges neighboring fragments that share a kind and style.
// Callers building fragment lists by hand use it once, at the end.
func coalesceAdjacent(in []Fragment) []Fragment {
	if len(in) == 0 {
		return nil
	}
	row := Row{Fragments: make([]Fragment, 0, len(in))}
	for _, fragment := range in {
		row.Append(fragment)
	}
	return row.Fragments
}

// mergeableFragments reports whether two neighbors can be merged into one
// styled run. Fixed-width fragments never merge: their reserved columns are the
// point, and concatenating two of them would collapse a column.
func mergeableFragments(a, b Fragment) bool {
	if a.WidthCells > 0 || b.WidthCells > 0 {
		return false
	}
	return a.Kind == b.Kind && a.Style == b.Style
}

func renderFragment(fragment Fragment) string {
	style := lipgloss.NewStyle()
	if fragment.Style.Foreground != "" {
		style = style.Foreground(lipgloss.Color(fragment.Style.Foreground))
	}
	if fragment.Style.Background != "" {
		style = style.Background(lipgloss.Color(fragment.Style.Background))
	}
	if fragment.Style.Bold {
		style = style.Bold(true)
	}
	if fragment.Style.Italic {
		style = style.Italic(true)
	}
	if fragment.Style.Strikethrough {
		style = style.Strikethrough(true)
	}
	return style.Render(fragmentFallbackText(fragment))
}

// fragmentFallbackText is the text actually drawn: a fixed-width fragment is
// clipped and padded to exactly the cells it reserved, so what is measured and
// what is printed can never disagree.
func fragmentFallbackText(fragment Fragment) string {
	if fragment.WidthCells <= 0 {
		return fragment.Text
	}
	return fitCells(fragment.Text, fragment.WidthCells)
}

func fragmentsWidth(fragments []Fragment) int {
	width := 0
	for _, fragment := range fragments {
		width += fragment.Width()
	}
	return width
}

// rowHasContent reports whether a row holds anything other than indent padding,
// so an abandoned continuation row can be discarded while a row carrying real
// fragments is emitted.
func rowHasContent(row Row) bool {
	for _, fragment := range row.Fragments {
		if strings.TrimSpace(fragment.Text) != "" {
			return true
		}
	}
	return false
}

// startsWord reports whether the cluster at index begins a word, so an address
// like "name@example.com" is not mistaken for a mention of "@example".
func startsWord(graphemes []string, index int) bool {
	if index == 0 {
		return true
	}
	return strings.TrimSpace(graphemes[index-1]) == ""
}

func isMentionPart(cluster string) bool {
	for _, r := range cluster {
		return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
	}
	return false
}

// maxShortcodeClusters bounds how far the scanner will look for a closing
// colon. A real channel-emoji shortcode is short; without a bound, a message
// containing two distant colons would swallow everything between them into one
// fixed-width chip.
const maxShortcodeClusters = 40

// shortcodeEnd returns the index just past a ":name:" / ":_name:" token
// starting at index, and whether one is there.
//
// The token must open with a letter or underscore, which is what keeps "12:30"
// and "note: hello" from matching.
func shortcodeEnd(graphemes []string, start int) (int, bool) {
	if graphemes[start] != ":" {
		return 0, false
	}
	i := start + 1
	if i >= len(graphemes) || !isShortcodeStart(graphemes[i]) {
		return 0, false
	}
	for i < len(graphemes) && isShortcodePart(graphemes[i]) {
		if i-start > maxShortcodeClusters {
			return 0, false
		}
		i++
	}
	if i >= len(graphemes) || graphemes[i] != ":" {
		return 0, false
	}
	return i + 1, true
}

func isShortcodeStart(cluster string) bool {
	for _, r := range cluster {
		return r == '_' || (r < unicode.MaxASCII && unicode.IsLetter(r))
	}
	return false
}

func isShortcodePart(cluster string) bool {
	for _, r := range cluster {
		return r == '_' || r == '+' || r == '-' ||
			(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)))
	}
	return false
}

// lowestEmojiCodepoint is U+00A9 COPYRIGHT SIGN, the lowest codepoint with an
// emoji presentation. Nothing below it can be part of an emoji cluster.
const lowestEmojiCodepoint = 0x00A9

// isEmojiCluster reports whether a grapheme cluster is a Unicode emoji.
//
// The codepoint test in front is not a micro-optimization for its own sake:
// splitTextFragments runs per cluster on every message at chat speed, and the
// overwhelming majority of clusters are ASCII, which cannot be emoji under any
// definition.
func isEmojiCluster(cluster string) bool {
	for _, r := range cluster {
		if r >= lowestEmojiCodepoint {
			return emoji.IsCluster(cluster)
		}
	}
	return false
}

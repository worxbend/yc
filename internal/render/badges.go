package render

import (
	"strings"

	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// BadgeMode selects how badges are drawn beside a username.
type BadgeMode string

const (
	// BadgeModeText draws bracketed labels such as "[mod]". Widest, but
	// unambiguous on any terminal and any font.
	BadgeModeText BadgeMode = "text"
	// BadgeModeGlyph draws a single icon per badge, compact enough to sit
	// inline with the username without pushing message text off the row.
	BadgeModeGlyph BadgeMode = "glyph"
	// BadgeModeOff hides badges entirely.
	BadgeModeOff BadgeMode = "off"
)

// DefaultBadgeMode is used when a config value is missing or unrecognized.
const DefaultBadgeMode = BadgeModeGlyph

// BadgeGlyphWidth is the cell width reserved for one glyph badge: the icon plus
// a single separating space.
const BadgeGlyphWidth = 2

// BadgeTextWidth is the cell width reserved for one compact text badge.
const BadgeTextWidth = 6

// NormalizeBadgeMode maps a config string onto a known mode, falling back to
// DefaultBadgeMode so an unrecognized value degrades instead of failing.
func NormalizeBadgeMode(value string) BadgeMode {
	switch BadgeMode(strings.ToLower(strings.TrimSpace(value))) {
	case BadgeModeText:
		return BadgeModeText
	case BadgeModeGlyph:
		return BadgeModeGlyph
	case BadgeModeOff:
		return BadgeModeOff
	default:
		return DefaultBadgeMode
	}
}

// badgeGlyphs maps the four badges YouTube live chat exposes onto single-cell
// icons.
//
// The vocabulary is deliberately tiny because authorDetails carries exactly
// four booleans; there is no vip, founder, staff, or turbo equivalent to model.
// Every glyph is a plain Unicode symbol rather than a Nerd Font private-use
// codepoint, so it renders in an unpatched font, and every one is width 1 under
// uniseg - an emoji-presentation character would measure 2 cells and silently
// break the badge column on every row that carried it.
var badgeGlyphs = map[youtube.BadgeKind]string{
	youtube.BadgeOwner:     "◉", // ◉ the broadcaster
	youtube.BadgeModerator: "⚔", // ⚔ moderation authority
	youtube.BadgeMember:    "★", // ★ a paying channel member
	youtube.BadgeVerified:  "✓", // ✓ a verified channel
}

// badgeFallbackGlyph stands in for a badge kind added to the API after this
// build. It keeps the column occupied so the row below still lines up.
const badgeFallbackGlyph = "•" // •

// badgeTextLabels are the compact labels for BadgeModeText.
//
// Each is bracketed and clipped to three letters so it fits BadgeTextWidth with
// one trailing pad cell, matching the glyph mode's trailing pad. "verified"
// cannot be spelled out in six cells, and a badge column that changes width
// with the badge would undo the alignment text mode exists to provide.
var badgeTextLabels = map[youtube.BadgeKind]string{
	youtube.BadgeOwner:     "[own]",
	youtube.BadgeModerator: "[mod]",
	youtube.BadgeMember:    "[mem]",
	youtube.BadgeVerified:  "[ver]",
}

// BadgeGlyph returns the icon for a badge kind and whether one is defined.
//
// The glyphs must be plain Unicode symbols, not Nerd Font private-use
// codepoints, so they render in an unpatched font, and every one must be width
// 1 under uniseg or BadgeGlyphWidth stops being accurate and badge columns stop
// lining up between rows. An unknown kind falls back to a neutral marker so it
// still occupies its column rather than silently disappearing.
func BadgeGlyph(kind youtube.BadgeKind) (string, bool) {
	glyph, ok := badgeGlyphs[normalizeBadgeKind(kind)]
	if !ok {
		return badgeFallbackGlyph, false
	}
	return glyph, true
}

// BadgeColor returns the palette role color for a badge kind.
//
// The split is by meaning rather than by prettiness: authority (the owner and
// their moderators) reads in the alert roles, paid status in the warning role,
// and platform verification in the accent role, so the same badge keeps the
// same significance under all 57 presets.
func BadgeColor(kind youtube.BadgeKind, palette theme.Palette) string {
	switch normalizeBadgeKind(kind) {
	case youtube.BadgeOwner:
		return palette.Error
	case youtube.BadgeModerator:
		return palette.Success
	case youtube.BadgeMember:
		return palette.Warning
	case youtube.BadgeVerified:
		return palette.Accent
	default:
		return palette.Muted
	}
}

// badgeTextLabel is the compact bracketed label for a badge kind. An unknown
// kind falls back to the badge's own label text, clipped to fit.
func badgeTextLabel(badge youtube.Badge) string {
	if label, ok := badgeTextLabels[normalizeBadgeKind(badge.Kind)]; ok {
		return label
	}
	name := strings.TrimSpace(badge.Label)
	if name == "" {
		name = strings.TrimSpace(string(badge.Kind))
	}
	if name == "" {
		return "[?]"
	}
	return "[" + takeCells(name, BadgeTextWidth-3) + "]"
}

// normalizeBadgeKind lets a caller pass a badge kind with stray case or
// whitespace - config files and fixtures both produce them - without the badge
// silently degrading to the unknown marker.
func normalizeBadgeKind(kind youtube.BadgeKind) youtube.BadgeKind {
	return youtube.BadgeKind(strings.ToLower(strings.TrimSpace(string(kind))))
}

// badgeSetWidth is the total width a message's badges occupy under the active
// badge mode. The prefix budget needs it before any fragment is built.
func badgeSetWidth(badges []youtube.Badge, opts Options) int {
	switch opts.badgeMode() {
	case BadgeModeOff:
		return 0
	case BadgeModeGlyph:
		return len(badges) * BadgeGlyphWidth
	default:
		return len(badges) * badgeTextCells(opts.Assets)
	}
}

func badgeTextCells(assets AssetOptions) int {
	if assets.BadgeWidthCells > 0 {
		return assets.BadgeWidthCells
	}
	return BadgeTextWidth
}

// badgeFragments renders a message's badges according to the active badge mode:
// single-cell glyphs, compact bracketed labels, or nothing.
//
// Each fragment reserves its own trailing pad cell, so callers append badges
// directly without adding a separator and without doubling the gap.
func badgeFragments(msg youtube.Message, opts Options) []Fragment {
	mode := opts.badgeMode()
	if mode == BadgeModeOff || len(msg.Badges) == 0 {
		return nil
	}
	fragments := make([]Fragment, 0, len(msg.Badges))
	for _, badge := range msg.Badges {
		fragments = append(fragments, badgeFragment(badge, opts, mode))
	}
	return fragments
}

func badgeFragment(badge youtube.Badge, opts Options, mode BadgeMode) Fragment {
	text := badgeTextLabel(badge)
	width := badgeTextCells(opts.Assets)
	if mode == BadgeModeGlyph {
		text, _ = BadgeGlyph(badge.Kind)
		width = BadgeGlyphWidth
	}
	return Fragment{
		Kind:       FragmentBadge,
		Text:       text,
		WidthCells: width,
		Style: FragmentStyle{
			Foreground: BadgeColor(badge.Kind, opts.Palette),
			Bold:       true,
		},
	}
}

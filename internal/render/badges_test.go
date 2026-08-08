package render

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/youtube"
)

// Glyph badges reserve BadgeGlyphWidth cells each. A width-2 glyph - any
// emoji-presentation codepoint - would silently push message text out of
// alignment on every row that carried that badge, so the invariant is enforced
// rather than assumed.
func TestBadgeGlyphsAreSingleCell(t *testing.T) {
	for kind, glyph := range badgeGlyphs {
		if got := ansi.StringWidth(glyph); got != 1 {
			t.Errorf("badge %q glyph %q width = %d cells, want 1", kind, glyph, got)
		}
	}
	if ansi.StringWidth(badgeFallbackGlyph) != 1 {
		t.Errorf("unknown-badge fallback %q width = %d cells, want 1",
			badgeFallbackGlyph, ansi.StringWidth(badgeFallbackGlyph))
	}
}

// Every badge YouTube can report must have a glyph. A missing entry would
// render as the neutral marker and quietly lose the difference between a
// moderator and the broadcaster.
func TestBadgeGlyphCoversEveryYouTubeBadge(t *testing.T) {
	author := youtube.Author{IsOwner: true, IsModerator: true, IsMember: true, IsVerified: true}
	badges := youtube.BadgesForAuthor(author)
	if len(badges) != 4 {
		t.Fatalf("BadgesForAuthor returned %d badges, want the four YouTube exposes", len(badges))
	}
	for _, badge := range badges {
		if _, ok := BadgeGlyph(badge.Kind); !ok {
			t.Errorf("BadgeGlyph(%q) has no mapping", badge.Kind)
		}
		if _, ok := badgeTextLabels[badge.Kind]; !ok {
			t.Errorf("badgeTextLabels has no entry for %q", badge.Kind)
		}
	}
}

func TestBadgeGlyphUnknownKindStillOccupiesItsColumn(t *testing.T) {
	glyph, ok := BadgeGlyph("not-a-real-badge")
	if ok {
		t.Fatalf("BadgeGlyph reported an unknown kind as known: %q", glyph)
	}
	if glyph == "" {
		t.Fatal("BadgeGlyph dropped an unknown kind instead of marking its column")
	}
}

func TestBadgeGlyphLookupIsCaseInsensitive(t *testing.T) {
	glyph, ok := BadgeGlyph("  MODERATOR ")
	if !ok {
		t.Fatal("BadgeGlyph did not match a padded, upper-case badge kind")
	}
	if want, _ := BadgeGlyph(youtube.BadgeModerator); glyph != want {
		t.Fatalf("BadgeGlyph case mismatch: got %q, want %q", glyph, want)
	}
}

// Text badges are fixed width so the column does not move between a row with
// "[mod]" and a row with "[verified]".
func TestBadgeTextLabelsFitTheReservedColumn(t *testing.T) {
	for kind, label := range badgeTextLabels {
		if got := ansi.StringWidth(label); got > BadgeTextWidth {
			t.Errorf("badge %q label %q width = %d cells, want at most %d", kind, label, got, BadgeTextWidth)
		}
	}
}

func TestBadgeColorsAreDistinctPerRole(t *testing.T) {
	palette := testPalette()
	seen := map[string]youtube.BadgeKind{}
	for _, kind := range []youtube.BadgeKind{
		youtube.BadgeOwner, youtube.BadgeModerator, youtube.BadgeMember, youtube.BadgeVerified,
	} {
		color := BadgeColor(kind, palette)
		if color == "" {
			t.Fatalf("BadgeColor(%q) returned no color", kind)
		}
		if other, ok := seen[color]; ok {
			t.Errorf("BadgeColor(%q) reuses the color of %q (%s)", kind, other, color)
		}
		seen[color] = kind
	}
}

func TestNormalizeBadgeModeFallsBackToDefault(t *testing.T) {
	for _, test := range []struct {
		in   string
		want BadgeMode
	}{
		{"text", BadgeModeText},
		{"GLYPH", BadgeModeGlyph},
		{" off ", BadgeModeOff},
		{"", DefaultBadgeMode},
		{"nonsense", DefaultBadgeMode},
	} {
		if got := NormalizeBadgeMode(test.in); got != test.want {
			t.Errorf("NormalizeBadgeMode(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestBadgeSetWidthMatchesRenderedWidth(t *testing.T) {
	msg := chatMessage("hi")
	msg.Author.IsOwner = true
	msg.Author.IsModerator = true
	msg.Badges = youtube.BadgesForAuthor(msg.Author)

	for _, mode := range []BadgeMode{BadgeModeGlyph, BadgeModeText, BadgeModeOff} {
		opts := testOptions(80)
		opts.Badges = mode
		opts.Assets = opts.Assets.withFallbackWidths()

		budgeted := badgeSetWidth(msg.Badges, opts)
		rendered := fragmentsWidth(badgeFragments(msg, opts))
		if budgeted != rendered {
			t.Errorf("badge mode %q: budgeted %d cells but rendered %d", mode, budgeted, rendered)
		}
	}
}

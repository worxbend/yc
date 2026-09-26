package emoji

import (
	"fmt"
	"strings"
)

const (
	variationText  = rune(0xFE0E)
	variationImage = rune(0xFE0F)
	modifierMin    = rune(0x1F3FB)
	modifierMax    = rune(0x1F3FF)
	keycap         = rune(0x20E3)
	zeroWidthJoin  = rune(0x200D)
	// Tag-sequence flag components: a black flag, the printable tag range
	// that spells a subdivision code, and the cancel tag that closes it.
	tagFlagBase = rune(0x1F3F4)
	tagSpace    = rune(0xE0020)
	tagTilde    = rune(0xE007E)
	tagCancel   = rune(0xE007F)
)

// IsCluster reports whether a grapheme cluster is a Unicode emoji.
//
// Callers must pass whole grapheme clusters obtained from a segmenter, not
// bytes or runes: a flag, a keycap, a skin-tone modifier, and a ZWJ sequence
// are all single clusters made of several runes, and splitting one produces
// mojibake in the middle of a chat row.
//
// Emoji are yc's main graphical vocabulary, because the YouTube live chat API
// supplies no emote imagery at all.
func IsCluster(cluster string) bool {
	_, ok := AssetID(cluster)
	return ok
}

// AssetID returns a stable identifier for an emoji cluster and whether the
// cluster is one. The identifier is used to key picker entries and diagnostics;
// it is never a URL, because yc downloads nothing.
//
// The form is lowercase hyphen-separated codepoints with presentation selectors
// dropped, so the same emoji typed with and without a variation selector keys
// to one entry.
func AssetID(cluster string) (string, bool) {
	runes := []rune(cluster)
	if !isStandardCluster(runes) {
		return "", false
	}

	parts := make([]string, 0, len(runes))
	for _, r := range runes {
		if isVariationSelector(r) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%x", r))
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "-"), true
}

// isStandardCluster validates the shape of an emoji cluster: a base, optionally
// followed by a skin-tone modifier and variation selectors, optionally joined to
// further bases by ZWJ. Keycaps and regional-indicator flags are their own
// shapes and are checked first.
func isStandardCluster(runes []rune) bool {
	if len(runes) == 0 {
		return false
	}
	if isKeycapCluster(runes) || isRegionalIndicatorFlag(runes) || isTagSequenceFlag(runes) {
		return true
	}
	return isJoinedBaseSequence(runes)
}

func isJoinedBaseSequence(runes []rune) bool {
	seenBase := false
	expectBase := true
	for _, r := range runes {
		switch {
		case isBase(r):
			if !expectBase {
				return false
			}
			seenBase = true
			expectBase = false
		case isModifier(r):
			if !followsBase(seenBase, expectBase) {
				return false
			}
		case isVariationSelector(r):
		case r == zeroWidthJoin:
			if !followsBase(seenBase, expectBase) {
				return false
			}
			expectBase = true
		default:
			return false
		}
	}
	return seenBase && !expectBase
}

// followsBase reports whether a modifier or a ZWJ may legally appear here:
// only directly after a base, never as the first rune or right after a ZWJ.
func followsBase(seenBase, expectBase bool) bool {
	return seenBase && !expectBase
}

func isKeycapCluster(runes []rune) bool {
	if len(runes) != 2 && len(runes) != 3 {
		return false
	}
	if !isKeycapBase(runes[0]) {
		return false
	}
	if len(runes) == 2 {
		return runes[1] == keycap
	}
	return runes[1] == variationImage && runes[2] == keycap
}

func isRegionalIndicatorFlag(runes []rune) bool {
	if len(runes) != 2 {
		return false
	}
	return isRegionalIndicator(runes[0]) && isRegionalIndicator(runes[1])
}

// isTagSequenceFlag covers the subdivision flags - England, Scotland, Wales -
// which are a black flag followed by tag characters spelling the subdivision
// code and closed by a cancel tag.
//
// They are their own shape, like keycaps and regional-indicator flags, and are
// checked alongside them: the generic base/modifier/ZWJ walk rejects tag
// characters outright, so without this the three flags real chat actually
// contains would be the only standard emoji yc does not recognize.
func isTagSequenceFlag(runes []rune) bool {
	if len(runes) < 3 || runes[0] != tagFlagBase {
		return false
	}
	if runes[len(runes)-1] != tagCancel {
		return false
	}
	for _, r := range runes[1 : len(runes)-1] {
		if r < tagSpace || r > tagTilde {
			return false
		}
	}
	return true
}

// isBase covers the emoji blocks by range rather than by a generated table.
// The ranges are deliberately generous: an unrecognized new emoji rendering as
// a chip is a far better failure than one splitting mid-cluster.
func isBase(r rune) bool {
	if isModifier(r) || isRegionalIndicator(r) {
		return false
	}
	if isBaseSymbol(r) {
		return true
	}
	for _, span := range baseSpans {
		if r >= span[0] && r <= span[1] {
			return true
		}
	}
	return false
}

var baseSpans = [][2]rune{
	{0x2194, 0x2199},
	{0x21A9, 0x21AA},
	{0x231A, 0x231B},
	{0x23E9, 0x23F3},
	{0x23F8, 0x23FA},
	{0x25AA, 0x25AB},
	{0x25FB, 0x25FE},
	{0x2600, 0x27BF},
	{0x2934, 0x2935},
	{0x2B05, 0x2B07},
	{0x2B1B, 0x2B1C},
	{0x1F000, 0x1FAFF},
}

func isBaseSymbol(r rune) bool {
	switch r {
	case 0x00A9, 0x00AE, 0x203C, 0x2049,
		0x2122, 0x2139, 0x2328, 0x23CF,
		0x24C2, 0x25B6, 0x25C0, 0x3030,
		0x303D, 0x3297, 0x3299,
		0x2B50, 0x2B55:
		return true
	}
	return false
}

func isKeycapBase(r rune) bool {
	return r == '#' || r == '*' || (r >= '0' && r <= '9')
}

func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

func isModifier(r rune) bool {
	return r >= modifierMin && r <= modifierMax
}

func isVariationSelector(r rune) bool {
	return r == variationText || r == variationImage
}

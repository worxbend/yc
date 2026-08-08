package youtube

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// Anchored patterns for the tokens yc recognizes inside a plain message body.
// Each is matched against the remainder of the string at a grapheme-cluster
// boundary, never against a byte offset chosen arbitrarily.
var (
	// mentionPattern matches an "@name" token. YouTube display names allow
	// letters, digits, underscores, hyphens, and periods.
	mentionPattern = regexp.MustCompile(`^@[\p{L}\p{N}_][\p{L}\p{N}_.\-]*`)
	// shortcodePattern matches a channel-emoji literal. Custom channel emoji
	// arrive as ":_name:" and standard ones as ":name:"; the API never
	// resolves either to an image, so they render as fixed-width chips.
	shortcodePattern = regexp.MustCompile(`^:_?[A-Za-z0-9][A-Za-z0-9_+\-]*:`)
	// urlPattern matches an http(s) link or a bare "www." host.
	urlPattern = regexp.MustCompile(`^(?:https?://|www\.)[^\s<>"']+`)
)

// SplitFragments converts a plain message body into semantic spans.
//
// The YouTube live chat API returns no emote ranges and no emote images:
// snippet.displayMessage is plain text in which channel emoji appear literally
// as ":shortcut:" or ":_shortcut:". So yc produces its own fragments here -
// mentions, URLs, shortcode tokens, and Unicode emoji clusters - rather than
// consuming transport metadata that does not exist.
//
// Splitting is grapheme-cluster based. Never slice the input by byte or rune.
func SplitFragments(text string) []MessageFragment {
	if text == "" {
		return nil
	}

	var fragments []MessageFragment
	var pending strings.Builder

	flush := func() {
		if pending.Len() == 0 {
			return
		}
		fragments = append(fragments, MessageFragment{Type: FragmentText, Text: pending.String()})
		pending.Reset()
	}
	emit := func(kind FragmentType, value string) {
		flush()
		fragments = append(fragments, MessageFragment{Type: kind, Text: value})
	}

	graphemes := uniseg.NewGraphemes(text)
	offset := 0
	atWordStart := true
	for graphemes.Next() {
		start, end := graphemes.Positions()
		if start < offset {
			// A token consumed this cluster already.
			continue
		}
		cluster := text[start:end]

		// Emoji are yc's whole graphical vocabulary here, so they are
		// checked first: a cluster can be several runes and must move as one.
		if isEmojiCluster(cluster) {
			emit(FragmentEmoji, cluster)
			offset = end
			atWordStart = false
			continue
		}

		if token, kind, ok := matchToken(text[start:], cluster, atWordStart); ok {
			emit(kind, token)
			// The clusters the token covered are skipped by the
			// start < offset guard at the top of the loop, so the
			// segmenter still owns every boundary.
			offset = start + len(token)
			atWordStart = false
			continue
		}

		pending.WriteString(cluster)
		offset = end
		atWordStart = isBoundaryCluster(cluster)
	}
	flush()
	return fragments
}

// matchToken tries the anchored token patterns against the remainder of the
// message. Mentions and URLs only start at a word boundary so an address inside
// a word is not mistaken for one; a shortcode may start anywhere, because
// YouTube emits them flush against neighbouring text.
func matchToken(remainder, cluster string, atWordStart bool) (string, FragmentType, bool) {
	switch {
	case atWordStart && cluster == "@":
		if token := mentionPattern.FindString(remainder); token != "" {
			return token, FragmentMention, true
		}
	case cluster == ":":
		if token := shortcodePattern.FindString(remainder); token != "" {
			return token, FragmentShortcode, true
		}
	case atWordStart && (cluster == "h" || cluster == "H" || cluster == "w" || cluster == "W"):
		if token := urlPattern.FindString(remainder); token != "" {
			return token, FragmentURL, true
		}
	}
	return "", FragmentText, false
}

// isBoundaryCluster reports whether a cluster ends a word, so the next cluster
// may begin a mention or a URL. Only the leading rune matters: a cluster whose
// base is a letter is part of a word however many marks follow it.
func isBoundaryCluster(cluster string) bool {
	base, size := utf8.DecodeRuneInString(cluster)
	if size == 0 {
		return false
	}
	return unicode.IsSpace(base) || strings.ContainsRune("([{<\"'`,;", base)
}

// emojiRanges approximates Unicode's Extended_Pictographic property.
//
// internal/emoji owns the authoritative detector for the picker and the
// renderer, but internal/youtube deliberately does not depend on it: the
// package graph keeps the transport free of UI-facing packages, and a chat
// fragment must be classifiable with nothing but the standard library. The
// approximation is generous on purpose - a false positive costs one fragment
// boundary, a false negative would split a ZWJ sequence across a wrap.
var emojiRanges = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x00A9, Hi: 0x00AE, Stride: 0x0005},
		{Lo: 0x203C, Hi: 0x203C, Stride: 1},
		{Lo: 0x2049, Hi: 0x2049, Stride: 1},
		{Lo: 0x2122, Hi: 0x2122, Stride: 1},
		{Lo: 0x2139, Hi: 0x2139, Stride: 1},
		{Lo: 0x2194, Hi: 0x21AA, Stride: 1},
		{Lo: 0x231A, Hi: 0x231B, Stride: 1},
		{Lo: 0x2328, Hi: 0x2328, Stride: 1},
		{Lo: 0x23CF, Hi: 0x23FA, Stride: 1},
		{Lo: 0x24C2, Hi: 0x24C2, Stride: 1},
		{Lo: 0x25AA, Hi: 0x25FE, Stride: 1},
		{Lo: 0x2600, Hi: 0x27BF, Stride: 1},
		{Lo: 0x2934, Hi: 0x2935, Stride: 1},
		{Lo: 0x2B00, Hi: 0x2BFF, Stride: 1},
		{Lo: 0x3030, Hi: 0x3030, Stride: 1},
		{Lo: 0x303D, Hi: 0x303D, Stride: 1},
		{Lo: 0x3297, Hi: 0x3299, Stride: 2},
	},
	R32: []unicode.Range32{
		{Lo: 0x1F000, Hi: 0x1FAFF, Stride: 1},
		{Lo: 0x1FC00, Hi: 0x1FFFD, Stride: 1},
	},
}

// Cluster components that mark an emoji without being pictographic themselves.
const (
	variationSelector16 = '\uFE0F'
	combiningKeycap     = '\u20E3'
	zeroWidthJoiner     = '\u200D'
)

// isEmojiCluster reports whether a grapheme cluster renders as an emoji.
//
// The cluster must come from a segmenter: a flag is two regional indicators, a
// keycap is three runes, and a ZWJ family is seven or more. Testing runes
// individually is how mojibake gets into a chat row.
func isEmojiCluster(cluster string) bool {
	if cluster == "" {
		return false
	}
	for _, r := range cluster {
		switch {
		case r == variationSelector16, r == combiningKeycap, r == zeroWidthJoiner:
			return true
		case unicode.Is(emojiRanges, r):
			return true
		}
	}
	return false
}

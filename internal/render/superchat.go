package render

import (
	"strconv"
	"strings"

	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// giftGlyph marks a virtual gift, whose value is denominated in jewels rather
// than money. Width 1 under uniseg, like every other chip glyph.
const giftGlyph = "◆"

// membershipGlyph marks a membership event and giftMembershipGlyph a gifted
// one, so a gifting burst is distinguishable from a member joining at a glance.
const (
	membershipGlyph     = "★"
	giftMembershipGlyph = "♥"
)

// AmountChip renders the paid-amount fragment for a Super Chat, Super Sticker,
// or gift. It prefers the API's pre-localized display string over any local
// formatting, and returns ok false when the message carries no amount.
//
// The chip is drawn as colored ground with contrast-corrected text rather than
// as colored text: on a busy chat the point of a Super Chat row is that it is
// impossible to scroll past, and a solid block reads at a glance where a tinted
// word does not.
func AmountChip(msg youtube.Message, palette theme.Palette) (Fragment, bool) {
	label, ok := amountLabel(msg)
	if !ok {
		return Fragment{}, false
	}
	background := TierColor(msg.Tier(), palette)
	return Fragment{
		Kind: FragmentAmount,
		Text: " " + label + " ",
		Style: FragmentStyle{
			Foreground: theme.ContrastCorrectedForeground(palette.Background, background, palette.Foreground),
			Background: background,
			Bold:       true,
		},
	}, true
}

// amountLabel is the chip text: YouTube's own localized amount when it sent
// one, a currency amount reconstructed from amountMicros when it did not, and a
// jewel count for gift events, which carry no money at all.
func amountLabel(msg youtube.Message) (string, bool) {
	if amount, ok := msg.Amount(); ok {
		if display := strings.TrimSpace(amount.Display); display != "" {
			return sanitizeUserText(display), true
		}
		if formatted, ok := formatMicros(amount); ok {
			return formatted, true
		}
	}
	if msg.Gift != nil && msg.Gift.Jewels > 0 {
		return giftGlyph + " " + strconv.Itoa(msg.Gift.Jewels) + " jewels", true
	}
	return "", false
}

// formatMicros renders amountMicros without ever converting it to a float.
//
// The value arrives as a uint64-in-a-string and is money; a float64 round trip
// would silently misprint large amounts in low-denomination currencies, which
// is precisely the row a viewer will screenshot.
func formatMicros(amount youtube.Money) (string, bool) {
	if amount.Micros == 0 {
		return "", false
	}
	micros := amount.Micros
	sign := ""
	if micros < 0 {
		sign = "-"
		micros = -micros
	}
	whole := micros / 1_000_000
	hundredths := (micros % 1_000_000) / 10_000
	value := sign + strconv.FormatInt(whole, 10) + "." + twoDigits(hundredths)
	if currency := strings.TrimSpace(amount.Currency); currency != "" {
		return value + " " + sanitizeUserText(currency), true
	}
	return value, true
}

func twoDigits(value int64) string {
	if value < 10 {
		return "0" + strconv.FormatInt(value, 10)
	}
	return strconv.FormatInt(value, 10)
}

// TierColor maps a YouTube purchase tier (1-11) onto a palette role rather than
// a hard-coded hex value, so a paid message stays legible under every theme.
// Tier 0 or an out-of-range tier returns the accent role.
//
// YouTube's own ladder runs blue, cyan, green, yellow, orange, magenta, red
// across eleven tiers. Eleven distinct hues do not exist in a nine-role palette
// and inventing them would break the theme contract, so the ladder collapses
// onto six monotonic steps built from palette roles and blends between them.
// The exact amount is on the chip either way; the color only has to convey
// "more than the last one".
func TierColor(tier int, palette theme.Palette) string {
	switch {
	case tier <= 0 || tier > 11:
		return palette.Accent
	case tier == 1:
		return palette.Accent
	case tier == 2:
		return theme.Mix(palette.Accent, palette.Success, 0.5)
	case tier <= 4:
		return palette.Success
	case tier <= 6:
		return palette.Warning
	case tier <= 8:
		return theme.Mix(palette.Warning, palette.Error, 0.5)
	default:
		return palette.Error
	}
}

// MembershipChip renders the membership level or milestone fragment, returning
// ok false when the message carries no membership details.
func MembershipChip(msg youtube.Message, palette theme.Palette) (Fragment, bool) {
	if msg.Membership == nil {
		return Fragment{}, false
	}
	label, background := membershipChipLabel(*msg.Membership, palette)
	return Fragment{
		Kind: FragmentMembership,
		Text: " " + label + " ",
		Style: FragmentStyle{
			Foreground: theme.ContrastCorrectedForeground(palette.Background, background, palette.Foreground),
			Background: background,
			Bold:       true,
		},
	}, true
}

// membershipChipLabel states what happened, not who it happened to: the author
// column already names the person, and a chip that repeated the name would cost
// the columns the message text needs.
func membershipChipLabel(details youtube.MembershipDetails, palette theme.Palette) (string, string) {
	switch details.Kind {
	case youtube.MembershipUpgrade:
		return membershipGlyph + " member upgrade", palette.Accent
	case youtube.MembershipMilestone:
		if details.Months > 0 {
			return membershipGlyph + " member " + strconv.Itoa(details.Months) + " mo", palette.Accent
		}
		return membershipGlyph + " member milestone", palette.Accent
	case youtube.MembershipGifting:
		return giftMembershipGlyph + " " + giftCountLabel(details.GiftCount), palette.Success
	case youtube.MembershipGiftReceived:
		return giftMembershipGlyph + " gift member", palette.Success
	case youtube.MembershipNew:
		return membershipGlyph + " new member", palette.Accent
	default:
		return membershipGlyph + " member", palette.Accent
	}
}

func giftCountLabel(count int) string {
	if count <= 0 {
		return "gift memberships"
	}
	if count == 1 {
		return "1 gift membership"
	}
	return strconv.Itoa(count) + " gift memberships"
}

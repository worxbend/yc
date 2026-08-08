package render

import (
	"strconv"
	"strings"

	"github.com/worxbend/yc/internal/youtube"
)

// eventDecoration is the rendered form of a non-chat live chat event: the chips
// and labels that carry its meaning, plus the chatter's own words to render
// after them.
type eventDecoration struct {
	// Fragments are the leading chips and labels. They are atomic and are
	// never split across a wrap.
	Fragments []Fragment
	// Body is the author's own text - a Super Chat comment, a milestone
	// comment - and nothing yc composed. It is wrapped like ordinary chat.
	Body string
	// Recognized is false when the event carried no structured payload, in
	// which case the caller falls back to Message.Text and its fragments.
	Recognized bool
}

// decorateEvent builds the decoration for a message's event kind.
//
// Structured details always win over Message.Text. internal/youtube also
// composes a prose fallback into Text for these events ("gifted 5
// memberships"), and rendering both would state the same fact twice on one row;
// Text is therefore the fallback for exactly the case the structured pointer is
// nil, which is what a nil pointer already means in this model.
func decorateEvent(msg youtube.Message, opts Options) eventDecoration {
	switch msg.Kind {
	case youtube.EventKindSuperChat, youtube.EventKindFanFunding:
		return decoratePaid(msg, opts, superChatComment(msg), "")
	case youtube.EventKindSuperSticker:
		return decoratePaid(msg, opts, "", stickerLabel(msg))
	case youtube.EventKindGift:
		return decoratePaid(msg, opts, "", giftLabel(msg))
	case youtube.EventKindNewSponsor,
		youtube.EventKindMemberMilestone,
		youtube.EventKindMembershipGifting,
		youtube.EventKindGiftMembershipReceived:
		return decorateMembership(msg, opts)
	case youtube.EventKindSponsorOnlyModeStarted:
		return decorateNotice(opts, "members-only mode on")
	case youtube.EventKindSponsorOnlyModeEnded:
		return decorateNotice(opts, "members-only mode off")
	case youtube.EventKindChatEnded:
		return decorateNotice(opts, "live chat ended")
	case youtube.EventKindUserBanned:
		return decorateNotice(opts, emptyFallback(sanitizeUserText(msg.Text), "a viewer was removed from chat"))
	case youtube.EventKindMessageDeleted, youtube.EventKindMessageRetracted:
		return decorateNotice(opts, emptyFallback(sanitizeUserText(msg.Text), "a message was removed"))
	case youtube.EventKindPoll:
		return decorateNotice(opts, "poll "+emptyFallback(sanitizeUserText(msg.Text), "updated"))
	default:
		return eventDecoration{}
	}
}

// decoratePaid builds the amount chip plus an optional descriptive label for
// the paid events. A paid event with no amount is not recognized: without the
// money there is nothing a chip could say that the text does not, and the plain
// fallback reads better than an empty block of color.
func decoratePaid(msg youtube.Message, opts Options, body, label string) eventDecoration {
	chip, ok := AmountChip(msg, opts.Palette)
	if !ok {
		return eventDecoration{}
	}
	fragments := []Fragment{chip}
	if label != "" {
		fragments = append(fragments, spacerFragment(opts), detailFragment(label, opts))
	}
	return eventDecoration{Fragments: fragments, Body: body, Recognized: true}
}

// decorateMembership builds the membership chip plus the level name. The level
// name is the one piece of a membership event a viewer cannot infer, so it is
// rendered even when nothing else about the event is known.
func decorateMembership(msg youtube.Message, opts Options) eventDecoration {
	chip, ok := MembershipChip(msg, opts.Palette)
	if !ok {
		return eventDecoration{}
	}
	fragments := []Fragment{chip}
	if level := sanitizeUserText(strings.TrimSpace(msg.Membership.LevelName)); level != "" {
		fragments = append(fragments, spacerFragment(opts), detailFragment(level, opts))
	}
	body := ""
	if msg.Membership.Kind == youtube.MembershipMilestone {
		body = sanitizeUserText(msg.Membership.Comment)
	}
	return eventDecoration{Fragments: fragments, Body: body, Recognized: true}
}

// decorateNotice renders a room-state change as a single muted line. These
// events describe the room rather than a person, so they carry no chip: a
// colored block would give a mode change the same visual weight as a paid
// message.
func decorateNotice(opts Options, text string) eventDecoration {
	if strings.TrimSpace(text) == "" {
		return eventDecoration{}
	}
	return eventDecoration{
		Fragments: []Fragment{{
			Kind: FragmentNotice,
			Text: text,
			Style: FragmentStyle{
				Foreground: opts.Palette.Warning,
			},
		}},
		Recognized: true,
	}
}

func spacerFragment(opts Options) Fragment {
	return Fragment{
		Kind:  FragmentText,
		Text:  " ",
		Style: FragmentStyle{Foreground: opts.Palette.Foreground},
	}
}

// detailFragment renders the muted supporting label beside a chip - a sticker's
// alt text, a gift's name, a membership level. It is deliberately unemphasized:
// the chip already carries the event, and this is the detail a reader drops to
// when they want it.
func detailFragment(text string, opts Options) Fragment {
	return Fragment{
		Kind: FragmentText,
		Text: text,
		Style: FragmentStyle{
			Foreground: opts.Palette.Muted,
			Italic:     true,
		},
	}
}

func superChatComment(msg youtube.Message) string {
	if msg.SuperChat == nil {
		return ""
	}
	return sanitizeUserText(msg.SuperChat.Comment)
}

// stickerLabel describes a Super Sticker by its alt text, which is the only
// renderable form: yc fetches no images, and the sticker art is the whole point
// of the purchase, so dropping the description would leave a bare price.
func stickerLabel(msg youtube.Message) string {
	if msg.SuperSticker == nil {
		return "super sticker"
	}
	alt := compactWhitespace(sanitizeUserText(msg.SuperSticker.AltText))
	if alt == "" {
		return "super sticker"
	}
	return "sticker: " + alt
}

// giftLabel describes a virtual gift by name, falling back to its alt text, and
// notes a combo so a burst of the same gift reads as one escalating event
// rather than as repetition.
func giftLabel(msg youtube.Message) string {
	if msg.Gift == nil {
		return "gift"
	}
	name := compactWhitespace(sanitizeUserText(msg.Gift.Name))
	if name == "" {
		name = compactWhitespace(sanitizeUserText(msg.Gift.AltText))
	}
	if name == "" {
		name = "gift"
	}
	if msg.Gift.ComboCount > 1 {
		return name + " x" + strconv.Itoa(msg.Gift.ComboCount)
	}
	return name
}

// noticeStyled reports whether a message describes the room rather than a
// person, and so earns the "[notice]" prefix.
//
// It tests EventKind as well as MessageType because the moderation kinds
// normalize to MessageTypeUnknown - they reach the renderer only when the app
// chooses to show them as rows, and when it does they are still notices.
func noticeStyled(msg youtube.Message) bool {
	switch msg.Type {
	case youtube.MessageTypeNotice, youtube.MessageTypeSystem:
		return true
	}
	switch msg.Kind {
	case youtube.EventKindUserBanned,
		youtube.EventKindMessageDeleted,
		youtube.EventKindMessageRetracted,
		youtube.EventKindChatEnded,
		youtube.EventKindSponsorOnlyModeStarted,
		youtube.EventKindSponsorOnlyModeEnded:
		return true
	default:
		return false
	}
}

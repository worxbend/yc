package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// The activity column is the running log of everything that is not ordinary
// chat: paid messages, memberships, moderation, and the room's own lifecycle.
// Twitch's raid/cheer/follow vocabulary has no YouTube equivalent, so the
// slots are repointed rather than translated - there is no follower event in
// this API at all, and gift memberships are the only thing that arrives in a
// flood worth collapsing.

const maxActivityEntries = 200

// activityKind categorizes one entry for its glyph and color.
type activityKind string

const (
	// activityPaid is a Super Chat, a Super Sticker, or a gift.
	activityPaid activityKind = "paid"
	// activityMembership is a new member, an upgrade, or a milestone.
	activityMembership activityKind = "membership"
	// activityGift is a gift-membership purchase or receipt. Receipts arrive
	// in bursts, which is what giftBurst collapses.
	activityGift activityKind = "gift"
	// activityModeration is a deletion, a ban, or a timeout.
	activityModeration activityKind = "moderation"
	// activityRoom is a members-only mode change, a chat ending, or the
	// broadcast going offline.
	activityRoom activityKind = "room"
	// activityPoll is a creator poll opening or closing.
	activityPoll activityKind = "poll"
	// activityQuota is a quota state change: stretched cadence, backoff, or
	// a paused poll loop.
	activityQuota activityKind = "quota"
	// activityChat is a chat opening, closing, or changing connection state.
	activityChat activityKind = "chat"
)

// giftBurstLimit is how many gift-membership receipts are logged individually
// before the rest of a burst collapses into one rolling summary row. A single
// 50-membership gift would otherwise bury every other kind of activity.
const giftBurstLimit = 5

// giftBurstWindow is how close together receipts must arrive to count as one
// burst.
const giftBurstWindow = 5 * time.Second

// activityEntry is one row in the activity column.
type activityEntry struct {
	Kind activityKind
	// ChatKey identifies the chat the entry belongs to, so the column can
	// prefix rows once more than one chat is open.
	ChatKey   string
	ChatLabel string
	Text      string
	At        time.Time
}

// activityScanWindow bounds how far back into each chat's history the column
// looks. The log only ever shows a screenful, and scanning a full 2000-message
// scrollback on every repaint would make the activity column the most
// expensive thing on screen.
const activityScanWindow = 400

// activityEntriesForChats derives the column from the chats themselves.
//
// The log is a projection of history rather than a second copy of it, so it
// cannot drift from what chat shows, cannot survive a cleared chat, and needs
// no bookkeeping in the update loop. Entries are collected newest-first per
// chat and then merged on timestamp.
func activityEntriesForChats(set *chatStateSet, limit int) []activityEntry {
	if set == nil || limit <= 0 {
		return nil
	}
	entries := make([]activityEntry, 0, limit)
	for _, key := range set.order {
		state := set.states[key]
		if state == nil {
			continue
		}
		label := state.target.Label()
		messages := state.messages
		if len(messages) > activityScanWindow {
			messages = messages[len(messages)-activityScanWindow:]
		}
		for _, message := range messages {
			if entry, ok := activityEntryForMessage(message, key, label); ok {
				entries = append(entries, entry)
			}
		}
		// Moderation is the one category that cannot be read off the message
		// list: a deletion or a ban never becomes a chat row, by design, so
		// the events themselves are the only record of it.
		for _, event := range state.moderations {
			if entry, ok := activityEntryForModeration(event, label); ok {
				entry.ChatKey = key
				entries = append(entries, entry)
			}
		}
	}
	mergeActivityByTime(entries)
	entries = collapseGiftBursts(entries)
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries
}

// mergeActivityByTime orders entries oldest first. It is an insertion sort
// because the input is already several nearly-sorted runs - one per chat - and
// insertion sort is linear on that shape, which the general-purpose sorts are
// not.
func mergeActivityByTime(entries []activityEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].At.Before(entries[j-1].At); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

// collapseGiftBursts folds a run of gift-membership receipts into one summary
// row.
//
// Gifted memberships arrive as one event per recipient, so a fifty-membership
// drop would otherwise bury every other kind of activity under fifty near
// identical rows. The first few are kept because knowing who received one is
// the point; the rest become a count.
func collapseGiftBursts(entries []activityEntry) []activityEntry {
	out := make([]activityEntry, 0, len(entries))
	burst := 0
	var burstAt time.Time
	summaryIndex := -1

	for _, entry := range entries {
		if entry.Kind != activityGift {
			out = append(out, entry)
			burst, summaryIndex = 0, -1
			continue
		}
		if burst == 0 || entry.At.Sub(burstAt) > giftBurstWindow {
			burst, summaryIndex = 0, -1
		}
		burstAt = entry.At
		burst++
		if burst <= giftBurstLimit {
			out = append(out, entry)
			continue
		}
		summary := activityEntry{
			Kind:      activityGift,
			ChatKey:   entry.ChatKey,
			ChatLabel: entry.ChatLabel,
			Text:      fmt.Sprintf("+%d more gift memberships", burst-giftBurstLimit),
			At:        entry.At,
		}
		if summaryIndex >= 0 {
			out[summaryIndex] = summary
			continue
		}
		out = append(out, summary)
		summaryIndex = len(out) - 1
	}
	return out
}

// activityEntryForMessage classifies a normalized message into an activity row,
// reporting false for ordinary chat, which produces no entry.
//
// Deleted-message text is never carried into an entry: a deletion exists to
// take words off a screen that is frequently on stream, so reprinting them in
// the activity column would defeat the moderation action.
func activityEntryForMessage(msg youtube.Message, chatKey, chatLabel string) (activityEntry, bool) {
	entry := activityEntry{ChatKey: chatKey, ChatLabel: chatLabel, At: msg.Timestamp}
	name := displayNameOr(msg.Author, "someone")

	switch msg.Kind {
	case youtube.EventKindSuperChat, youtube.EventKindSuperSticker, youtube.EventKindFanFunding:
		entry.Kind = activityPaid
		entry.Text = name + " sent " + paidAmountLabel(msg)
	case youtube.EventKindGift:
		entry.Kind = activityPaid
		entry.Text = name + " sent a gift"
		if msg.Gift != nil && strings.TrimSpace(msg.Gift.Name) != "" {
			entry.Text = name + " sent " + msg.Gift.Name
		}
	case youtube.EventKindNewSponsor:
		entry.Kind = activityMembership
		entry.Text = name + " became a member" + membershipLevelSuffix(msg)
	case youtube.EventKindMemberMilestone:
		entry.Kind = activityMembership
		entry.Text = name + " hit " + membershipMonthsLabel(msg)
	case youtube.EventKindMembershipGifting:
		entry.Kind = activityGift
		entry.Text = name + " gifted " + membershipGiftCountLabel(msg)
	case youtube.EventKindGiftMembershipReceived:
		entry.Kind = activityGift
		entry.Text = name + " received a gift membership"
	case youtube.EventKindSponsorOnlyModeStarted:
		entry.Kind = activityRoom
		entry.Text = "members-only mode on"
	case youtube.EventKindSponsorOnlyModeEnded:
		entry.Kind = activityRoom
		entry.Text = "members-only mode off"
	case youtube.EventKindChatEnded:
		// Room-wide changes reach the app as normalized messages as well as
		// on the RoomEvent stream, so they are classified here rather than
		// twice in two shapes that could disagree.
		entry.Kind = activityRoom
		entry.Text = "live chat ended"
	case youtube.EventKindPoll:
		entry.Kind = activityPoll
		entry.Text = "poll updated"
	default:
		return activityEntry{}, false
	}
	return entry, true
}

// activityEntryForModeration renders a moderation action without reproducing
// the removed content.
func activityEntryForModeration(event youtube.ModerationEvent, chatLabel string) (activityEntry, bool) {
	entry := activityEntry{
		Kind:      activityModeration,
		ChatKey:   event.LiveChatID,
		ChatLabel: chatLabel,
		At:        event.At,
	}
	target := strings.TrimSpace(event.TargetDisplayName)
	if target == "" {
		target = "a viewer"
	}
	switch event.Type {
	case youtube.ModerationMessageDeleted, youtube.ModerationTombstone:
		entry.Text = "a message was deleted"
	case youtube.ModerationUserBanned:
		entry.Text = target + " was banned"
	case youtube.ModerationUserTimedOut:
		entry.Text = target + " was timed out for " + formatCompactDuration(event.Duration)
	default:
		return activityEntry{}, false
	}
	return entry, true
}

// activityLogState is everything the column draws.
type activityLogState struct {
	Palette theme.Palette
	Entries []activityEntry
	// ShowChatLabels prefixes each row with its chat, which only
	// disambiguates once more than one chat is open. In a narrow column with
	// a single chat it would just eat width.
	ShowChatLabels bool
	Phase          int
}

// renderActivityLog draws the right-hand column, newest entries at the bottom
// to match chat's own bottom-anchored convention.
func renderActivityLog(width, contentHeight int, st activityLogState) string {
	if width <= 0 {
		return ""
	}
	contentWidth := clampMin(width-2, 1)
	entries := st.Entries
	if len(entries) > contentHeight {
		entries = entries[len(entries)-clampMin(contentHeight, 0):]
	}
	lines := make([]string, 0, clampMin(contentHeight, 0))
	for _, entry := range entries {
		lines = append(lines, activityLogLine(contentWidth, entry, st))
	}
	// Blank rows go above the entries so the log stays bottom-anchored.
	if pad := contentHeight - len(lines); pad > 0 {
		blank := backgroundStyledLine(fitLine("", contentWidth), st.Palette.Surface)
		filled := make([]string, 0, contentHeight)
		for range pad {
			filled = append(filled, blank)
		}
		lines = append(filled, lines...)
	}
	lines = padLines(lines, contentWidth, contentHeight, st.Palette.Surface)

	return renderPane(paneSpec{
		palette:       st.Palette,
		icon:          "⚡",
		title:         fmt.Sprintf("Activity · %02d", len(st.Entries)),
		content:       strings.Join(lines, "\n"),
		width:         width,
		contentHeight: contentHeight,
		accent:        st.Palette.Warning,
		phase:         st.Phase,
	})
}

// activityLogLine renders one row as "HH:MM ◆ text". The pane is narrow, so the
// timestamp is dropped before the glyph and the glyph before the text as width
// shrinks: the text is the part that always survives.
func activityLogLine(width int, entry activityEntry, st activityLogState) string {
	if width <= 0 {
		return ""
	}
	glyph, color := activityKindGlyph(entry.Kind, st.Palette)
	text := entry.Text
	if st.ShowChatLabels && strings.TrimSpace(entry.ChatLabel) != "" {
		text = entry.ChatLabel + " " + text
	}

	writer := newPaneLineWriter(width, st.Palette.Surface)
	if width >= 18 && !entry.At.IsZero() {
		writer.write(" "+entry.At.Local().Format("15:04"), st.Palette.Muted, false)
	}
	if width >= 12 {
		writer.write(" "+glyph, color, true)
	}
	writer.write(" "+text, st.Palette.Foreground, false)
	return writer.String()
}

// activityKindGlyph maps an entry kind onto a width-1 glyph and a palette role.
// Every glyph is plain Unicode of display width one so the text column below
// them stays aligned on terminals without a patched font.
func activityKindGlyph(kind activityKind, palette theme.Palette) (string, string) {
	switch kind {
	case activityPaid:
		return "◈", palette.Warning
	case activityMembership:
		return "★", palette.Accent
	case activityGift:
		return "♥", palette.Success
	case activityModeration:
		return "⊘", palette.Error
	case activityRoom:
		return "●", palette.Success
	case activityPoll:
		return "▤", palette.Accent
	case activityQuota:
		return "⟳", palette.Warning
	case activityChat:
		return "▸", palette.Muted
	default:
		return "·", palette.Muted
	}
}

// paidAmountLabel prefers YouTube's own pre-localized amount string, which is
// already formatted for the viewer's locale and currency. yc never does its own
// currency math: the money value is an integer micro-amount precisely so no
// float arithmetic can round it.
func paidAmountLabel(msg youtube.Message) string {
	amount, ok := msg.Amount()
	if !ok {
		return "a paid message"
	}
	if display := strings.TrimSpace(amount.Display); display != "" {
		return display
	}
	if currency := strings.TrimSpace(amount.Currency); currency != "" {
		return currency
	}
	return "a paid message"
}

func membershipLevelSuffix(msg youtube.Message) string {
	if msg.Membership == nil {
		return ""
	}
	if level := strings.TrimSpace(msg.Membership.LevelName); level != "" {
		return " (" + level + ")"
	}
	return ""
}

func membershipMonthsLabel(msg youtube.Message) string {
	months := 0
	if msg.Membership != nil {
		months = msg.Membership.Months
	}
	if months == 1 {
		return "1 month"
	}
	return fmt.Sprintf("%d months", months)
}

func membershipGiftCountLabel(msg youtube.Message) string {
	count := 0
	if msg.Membership != nil {
		count = msg.Membership.GiftCount
	}
	if count == 1 {
		return "1 membership"
	}
	return fmt.Sprintf("%d memberships", count)
}

// displayNameOr returns an author's visible name, falling back rather than
// rendering an empty run of cells.
func displayNameOr(author youtube.Author, fallback string) string {
	if name := strings.TrimSpace(author.DisplayName); name != "" {
		return name
	}
	return fallback
}

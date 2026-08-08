package app

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/render"
	"github.com/worxbend/yc/internal/youtube"
)

// Frame-level layout coverage for every event kind.
//
// internal/render already pins the rows themselves against a golden file. What
// it cannot see is the shell around them: the gutter, the side panes, the
// scroll window, and the budget arithmetic that decides how many columns the
// message content actually gets. A row that is correct at width 72 can still
// overflow the frame when the app hands it 72 columns and then draws a border
// on either side, and that failure only appears here.
//
// So these assert the properties the app layer owns - exact frame geometry, no
// event kind disappearing, no layout mode losing the author - across every
// event kind, all three layouts, and a narrow and a wide terminal.

// everyEventKind is one message per normalized kind, carrying whatever
// structured detail that kind renders from. It mirrors the render package's
// fixture list; a kind added there without one here would render untested
// inside the shell.
func everyEventKind(t *testing.T, chatID string) []youtube.Message {
	t.Helper()
	kinds := []youtube.EventKind{
		youtube.EventKindText,
		youtube.EventKindSuperChat,
		youtube.EventKindSuperSticker,
		youtube.EventKindNewSponsor,
		youtube.EventKindMemberMilestone,
		youtube.EventKindMembershipGifting,
		youtube.EventKindGiftMembershipReceived,
		youtube.EventKindGift,
		youtube.EventKindPoll,
		youtube.EventKindFanFunding,
		youtube.EventKindMessageDeleted,
		youtube.EventKindMessageRetracted,
		youtube.EventKindUserBanned,
		youtube.EventKindSponsorOnlyModeStarted,
		youtube.EventKindSponsorOnlyModeEnded,
		youtube.EventKindChatEnded,
		youtube.EventKindTombstone,
		youtube.EventKindInvalidType,
		youtube.EventKindUnknown,
	}

	messages := make([]youtube.Message, 0, len(kinds))
	for index, kind := range kinds {
		message := testMessage(t, "kind-"+string(kind), chatID, "Alice Lovelace", "body for "+string(kind))
		message.Kind = kind
		message.Type = youtube.MessageTypeForKind(kind)
		message.RawType = string(kind)
		message.Badges = youtube.BadgesForAuthor(message.Author)
		switch kind {
		case youtube.EventKindSuperChat, youtube.EventKindFanFunding:
			message.SuperChat = &youtube.SuperChatDetails{
				Amount:  youtube.Money{Micros: int64(index+1) * 1_500_000, Currency: "USD"},
				Tier:    index % 12,
				Comment: message.Text,
			}
		case youtube.EventKindSuperSticker:
			message.SuperSticker = &youtube.SuperStickerDetails{
				Amount:    youtube.Money{Micros: 3_000_000, Currency: "EUR"},
				Tier:      3,
				StickerID: "sticker-7",
				AltText:   "a waving cat",
			}
		case youtube.EventKindGift:
			message.Gift = &youtube.GiftDetails{Name: "Star Shower", Jewels: 120, ComboCount: 2}
		case youtube.EventKindNewSponsor:
			message.Membership = &youtube.MembershipDetails{Kind: youtube.MembershipNew, LevelName: "Comet Crew"}
		case youtube.EventKindMemberMilestone:
			message.Membership = &youtube.MembershipDetails{
				Kind: youtube.MembershipMilestone, LevelName: "Comet Crew", Months: 14, Comment: message.Text,
			}
		case youtube.EventKindMembershipGifting:
			message.Membership = &youtube.MembershipDetails{
				Kind: youtube.MembershipGifting, LevelName: "Comet Crew", GiftCount: 20,
			}
		case youtube.EventKindGiftMembershipReceived:
			message.Membership = &youtube.MembershipDetails{
				Kind: youtube.MembershipGiftReceived, LevelName: "Comet Crew", GifterChannelID: "UC-gifter",
			}
		case youtube.EventKindMessageDeleted, youtube.EventKindMessageRetracted, youtube.EventKindTombstone:
			message.Deleted = true
		case youtube.EventKindUnknown:
			message.RawType = "hologramEventFromTheFuture"
		}
		messages = append(messages, message)
	}
	return messages
}

// layoutFrameModel builds a shell holding one message of every event kind, in
// one layout, at one size.
func layoutFrameModel(t *testing.T, layout render.LayoutMode, width, height int) shellModel {
	t.Helper()
	model := newModelForTest(t, "layout-golden")
	model.width, model.height = width, height
	model.messageLayout = layout
	model.effectiveConfig.Features.MessageLayout = string(layout)
	state := model.activeChatState()
	state.target.LiveChatID = "layout-golden"
	state.target.Title = "Launch Day Stream"
	state.status = youtube.ConnectionState{Status: youtube.ConnectionConnected, Detail: "polling"}
	state.messages = everyEventKind(t, "layout-golden")
	return model
}

// layoutGoldenSizes are the two terminals the frame must be correct at: the
// narrowest the app claims to support, and a comfortable wide one.
var layoutGoldenSizes = []struct {
	name          string
	width, height int
}{
	{"narrow", 62, 20},
	{"wide", 130, 36},
}

var allLayouts = []render.LayoutMode{render.LayoutInline, render.LayoutGrouped, render.LayoutCompact}

// Every event kind, in every layout, at a narrow and a wide terminal, must
// produce a frame that is exactly the terminal's size - with the side panes on
// or off, the activity column on or off, and the selection cursor parked on the
// event being drawn.
func TestEveryEventKindKeepsTheFrameRectangularInEveryLayout(t *testing.T) {
	for _, layout := range allLayouts {
		for _, size := range layoutGoldenSizes {
			for _, message := range everyEventKind(t, "layout-golden") {
				model := layoutFrameModel(t, layout, size.width, size.height)
				model.activeChatState().messages = []youtube.Message{message}
				model.activeChatState().selected = replyContextFromMessage(message)

				context := string(layout) + "/" + size.name + "/" + string(message.Kind)
				assertRectangularFrame(t, model, context)

				// The same frame with the inspector open, which is the pane
				// that renders a single event's structured detail and is
				// therefore the one that varies most by kind.
				model.activeChatState().inspectOpen = true
				assertRectangularFrame(t, model, context+"/inspect")
			}
		}
	}
}

// No event kind may render as a blank row. A kind the shell draws as nothing is
// indistinguishable from a message yc dropped, and yc's whole claim is that it
// does not silently lose chat.
func TestNoEventKindRendersAsNothing(t *testing.T) {
	for _, layout := range allLayouts {
		for _, size := range layoutGoldenSizes {
			for _, message := range everyEventKind(t, "layout-golden") {
				model := layoutFrameModel(t, layout, size.width, size.height)
				model.activeChatState().messages = []youtube.Message{message}

				rows := model.visibleChatRows(model.layout())
				var printed int
				for _, row := range rows {
					if strings.TrimSpace(ansi.Strip(row)) != "" {
						printed++
					}
				}
				if printed == 0 {
					t.Fatalf("%s/%s: event kind %q rendered no visible row",
						layout, size.name, message.Kind)
				}
			}
		}
	}
}

// The whole corpus on screen at once, scrolled from the bottom to the top, must
// stay rectangular at every scroll position in every layout. Scroll arithmetic
// is where a multi-row event kind desynchronizes the window from the row count,
// and the symptom is a frame one row too tall on exactly one offset.
func TestScrollingTheWholeEventCorpusStaysRectangular(t *testing.T) {
	for _, layout := range allLayouts {
		for _, size := range layoutGoldenSizes {
			model := layoutFrameModel(t, layout, size.width, size.height)
			total := model.chatRowCount(model.layout())
			if total == 0 {
				t.Fatalf("%s/%s: the corpus produced no rows", layout, size.name)
			}
			for offset := 0; offset <= total+2; offset++ {
				model.activeChatState().scrollOffset = offset
				model.clampScroll()
				assertRectangularFrame(t, model,
					string(layout)+"/"+size.name+" at scroll offset "+strconv.Itoa(offset))
			}
		}
	}
}

// ctrl+g cycles the layout live. Every layout it lands on has to be one the
// renderer knows, and the frame has to stay exact through the whole cycle -
// including the re-measurement of rows that were already on screen.
func TestCyclingLayoutsLiveKeepsEveryFrameExact(t *testing.T) {
	for _, size := range layoutGoldenSizes {
		model := layoutFrameModel(t, render.LayoutInline, size.width, size.height)
		seen := make(map[render.LayoutMode]bool, len(allLayouts))

		for step := 0; step < 2*len(allLayouts)+1; step++ {
			seen[model.messageLayout] = true
			if got := render.NormalizeLayoutMode(string(model.messageLayout)); got != model.messageLayout {
				t.Fatalf("%s: ctrl+g produced the unknown layout %q", size.name, model.messageLayout)
			}
			if got := model.effectiveConfig.Features.MessageLayout; got != string(model.messageLayout) {
				t.Fatalf("%s: config says layout %q while the model is in %q",
					size.name, got, model.messageLayout)
			}
			assertRectangularFrame(t, model, size.name+"/after "+strconv.Itoa(step)+" layout cycles")
			model = press(t, model, key(tea.KeyCtrlG))
		}

		if len(seen) != len(allLayouts) {
			t.Fatalf("%s: ctrl+g reached %d of %d layouts", size.name, len(seen), len(allLayouts))
		}
	}
}

// The layouts have to actually differ. Three identical renderings would pass
// every geometry assertion above while making ctrl+g a key that does nothing,
// so this pins the one distinction each layout exists for.
func TestTheThreeLayoutsProduceThreeDifferentFrames(t *testing.T) {
	for _, size := range layoutGoldenSizes {
		rendered := make(map[string]render.LayoutMode, len(allLayouts))
		for _, layout := range allLayouts {
			model := layoutFrameModel(t, layout, size.width, size.height)
			rows := strings.Join(model.visibleChatRows(model.layout()), "\n")
			plain := ansi.Strip(rows)
			if previous, clash := rendered[plain]; clash {
				t.Fatalf("%s: layouts %q and %q render identically", size.name, previous, layout)
			}
			rendered[plain] = layout
		}
	}
}

// Every layout must keep the author attached to what they said. A row that
// loses the name is a row a moderator cannot act on, and the compact layout -
// which exists precisely to drop decoration - is where that goes wrong.
func TestEveryLayoutKeepsTheAuthorAttachedToTheMessage(t *testing.T) {
	for _, layout := range allLayouts {
		for _, size := range layoutGoldenSizes {
			model := layoutFrameModel(t, layout, size.width, size.height)
			// One authored, ordinary message, so there is nothing else the
			// name could be coming from.
			message := testMessage(t, "authored", "layout-golden", "Alice Lovelace", "an ordinary line")
			model.activeChatState().messages = []youtube.Message{message}

			frame := ansi.Strip(strings.Join(model.visibleChatRows(model.layout()), "\n"))
			if !strings.Contains(frame, "Alice") {
				t.Fatalf("%s/%s dropped the author:\n%s", layout, size.name, frame)
			}
			if !strings.Contains(frame, "ordinary") {
				t.Fatalf("%s/%s dropped the message body:\n%s", layout, size.name, frame)
			}
		}
	}
}

// A deleted or retracted event must never carry the text it removed into any
// layout at any width. This is the one content assertion the frame layer owns
// that the row layer cannot make: the app is what decides which collection a
// redacted message is read from.
func TestRemovedEventsNeverReprintTheirTextInAnyLayout(t *testing.T) {
	const secret = "the words that were removed"
	for _, kind := range []youtube.EventKind{
		youtube.EventKindMessageDeleted,
		youtube.EventKindMessageRetracted,
		youtube.EventKindTombstone,
	} {
		for _, layout := range allLayouts {
			for _, size := range layoutGoldenSizes {
				model := layoutFrameModel(t, layout, size.width, size.height)
				message := testMessage(t, "removed", "layout-golden", "Alice Lovelace", secret)
				message.Kind = kind
				message.Type = youtube.MessageTypeForKind(kind)
				message.Deleted = true
				model.activeChatState().messages = []youtube.Message{message}
				model.activeChatState().selected = replyContextFromMessage(message)
				model.activeChatState().inspectOpen = true

				frame := ansi.Strip(model.View())
				if strings.Contains(frame, secret) {
					t.Fatalf("%s/%s/%s reprinted the removed text:\n%s", kind, layout, size.name, frame)
				}
			}
		}
	}
}

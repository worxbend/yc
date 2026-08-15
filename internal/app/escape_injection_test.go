package app

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/youtube"
)

// Escape-injection probes for the paths that carry attacker-controlled text
// into the frame without passing through render.sanitizeUserText: the activity
// column's moderation lines (TargetDisplayName), displayNameOr fallbacks, the
// status line's notification summary, and the tab bar's identity context.
//
// Each probe pushes a hostile string through the public rendering surface -
// shellModel.View - and asserts the finished frame. The frame legitimately
// contains SGR color sequences of yc's own, so the assertions distinguish
// yc's output from injected control data: after ansi.Strip the visible text
// must hold no control character except the row separator, no bidirectional
// override, and the raw frame must hold none of the sequence introducers that
// could retitle the terminal, plant a hyperlink, or ring the bell.

// escapePayloads is one string per escape family. Every entry carries the
// marker "evil" so a test can prove the value reached the frame rather than
// being filtered out wholesale before rendering.
var escapePayloads = []struct {
	name  string
	value string
}{
	{"csi", "evil\x1b[31;1m\x1b[2Jname"},
	{"osc-title", "evil\x1b]0;owned\x07name"},
	{"osc-hyperlink", "evil\x1b]8;;http://attacker.invalid\x07x\x1b]8;;\x07name"},
	{"c1-csi", "evil\u009b31mname"},
	{"c1-osc", "evil\u009d0;ownedname"},
	{"bidi-override", "evil\u202egnihtemos\u202cname"},
	{"bidi-isolate", "evil\u2066right\u2069name"},
	{"bell-del", "evil\a\x7fname"},
}

// assertNeutralizedFrame fails when a frame still carries anything a hostile
// string could use against the terminal: raw C0 controls beyond the newline
// row separator, raw C1 controls, bidirectional overrides, OSC or DCS or APC
// introducers, or the BEL that terminates an OSC while ringing the terminal.
func assertNeutralizedFrame(t *testing.T, frame, context string) {
	t.Helper()
	for _, introducer := range []string{"\x1b]", "\x1bP", "\x1b_", "\x1b^", "\a"} {
		if strings.Contains(frame, introducer) {
			t.Errorf("%s: frame contains the sequence introducer %q", context, introducer)
		}
	}
	plain := ansi.Strip(frame)
	for _, r := range plain {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) {
			t.Errorf("%s: stripped frame still holds control rune %U", context, r)
		}
		if isBidiOverride(r) {
			t.Errorf("%s: stripped frame still holds bidi control %U", context, r)
		}
	}
}

// hostileFrameModel is a wide shell - wide enough for the activity column and
// the status line's notification segment - holding one chat.
func hostileFrameModel(t *testing.T) shellModel {
	t.Helper()
	model := newModelForTest(t, "hostile-chat")
	model.width, model.height = 130, 36
	state := model.activeChatState()
	state.target.LiveChatID = "hostile-chat"
	state.target.Title = "Stream"
	state.status = youtube.ConnectionState{Status: youtube.ConnectionConnected, Detail: "polling"}
	return model
}

// A ban or timeout renders its target's display name into the activity
// column. The name is attacker-chosen, so every escape family must be
// neutralized by the time the frame is assembled.
func TestModerationTargetNameCannotInjectEscapes(t *testing.T) {
	for _, hostile := range escapePayloads {
		t.Run(hostile.name, func(t *testing.T) {
			for _, modType := range []youtube.ModerationType{
				youtube.ModerationUserBanned,
				youtube.ModerationUserTimedOut,
			} {
				model := hostileFrameModel(t)
				model.activeChatState().moderations = []youtube.ModerationEvent{{
					Type:              modType,
					LiveChatID:        "hostile-chat",
					TargetChannelID:   "UC-target",
					TargetDisplayName: hostile.value,
					Duration:          5 * time.Minute,
					At:                time.Now(),
				}}
				frame := model.View()
				context := hostile.name + "/" + string(modType)
				assertNeutralizedFrame(t, frame, context)
				assertRectangularFrame(t, model, context)
				if !strings.Contains(ansi.Strip(frame), "evil") {
					t.Fatalf("%s: hostile name never reached the frame; the probe proves nothing", context)
				}
			}
		})
	}
}

// Paid events route the author's display name through displayNameOr into the
// activity column, alongside the chat row itself.
func TestActivityAuthorNameCannotInjectEscapes(t *testing.T) {
	for _, hostile := range escapePayloads {
		t.Run(hostile.name, func(t *testing.T) {
			model := hostileFrameModel(t)
			message := testMessage(t, "hostile-super", "hostile-chat", hostile.value, "thanks for the stream")
			message.Kind = youtube.EventKindSuperChat
			message.Type = youtube.MessageTypeForKind(message.Kind)
			message.SuperChat = &youtube.SuperChatDetails{
				Amount: youtube.Money{Micros: 5_000_000, Currency: "USD", Display: "$5.00"},
			}
			model.activeChatState().messages = []youtube.Message{message}
			frame := model.View()
			assertNeutralizedFrame(t, frame, hostile.name)
			assertRectangularFrame(t, model, hostile.name)
			if !strings.Contains(ansi.Strip(frame), "evil") {
				t.Fatalf("%s: hostile author never reached the frame; the probe proves nothing", hostile.name)
			}
		})
	}
}

// The status line summarizes the last notification, whose title and body are
// built from an attacker-chosen author name.
func TestNotificationSummaryCannotInjectEscapes(t *testing.T) {
	for _, hostile := range escapePayloads {
		t.Run(hostile.name, func(t *testing.T) {
			model := hostileFrameModel(t)
			model.lastNotification = &SystemNotification{
				Title:   "Gift from " + hostile.value,
				Body:    hostile.value + ": take this",
				ChatKey: "hostile-chat",
			}
			frame := model.View()
			assertNeutralizedFrame(t, frame, hostile.name)
			assertRectangularFrame(t, model, hostile.name)
			if !strings.Contains(ansi.Strip(frame), "evil") {
				t.Fatalf("%s: hostile summary never reached the frame; the probe proves nothing", hostile.name)
			}
		})
	}
}

// The tab bar renders the signed-in handle and the active chat label through
// sanitizeContextValue. The handle comes back from the API and the label can
// come from a config file, so neither is trusted.
func TestTabBarContextCannotInjectEscapes(t *testing.T) {
	for _, hostile := range escapePayloads {
		t.Run(hostile.name, func(t *testing.T) {
			model := hostileFrameModel(t)
			model.identity = youtube.Identity{
				ChannelID:   "UC-self",
				DisplayName: hostile.value,
				Handle:      hostile.value,
			}
			model.activeChatState().target.Title = hostile.value
			frame := model.View()
			assertNeutralizedFrame(t, frame, hostile.name)
			assertRectangularFrame(t, model, hostile.name)
			if !strings.Contains(ansi.Strip(frame), "evil") {
				t.Fatalf("%s: hostile handle never reached the frame; the probe proves nothing", hostile.name)
			}
		})
	}
}

// sanitizeContextValue must neutralize hostile input on its own, not by
// leaning on the pane writers that happen to run later. C0 and DEL were
// always replaced; a raw C1 introducer and a bidi override used to slip
// through here and only die in fitLine, which made the safety of the tab bar
// depend on an ordering the function's name does not promise.
func TestSanitizeContextValueNeutralizesEveryControlFamily(t *testing.T) {
	for _, hostile := range escapePayloads {
		t.Run(hostile.name, func(t *testing.T) {
			got := sanitizeContextValue(hostile.value)
			for _, r := range got {
				if unicode.IsControl(r) {
					t.Errorf("control rune %U survived: %q", r, got)
				}
				if isBidiOverride(r) {
					t.Errorf("bidi control %U survived: %q", r, got)
				}
			}
			if !strings.Contains(got, "evil") || !strings.Contains(got, "name") {
				t.Errorf("visible text was lost: %q", got)
			}
		})
	}
	if got := sanitizeContextValue("  a\x00b  "); got != "a�b" {
		t.Errorf("got %q, want %q", got, "a�b")
	}
}

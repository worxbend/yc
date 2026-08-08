package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

func testInspectMessage() youtube.Message {
	return youtube.Message{
		ID:         "msg-1",
		LiveChatID: "chat-1",
		Timestamp:  time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC),
		Author: youtube.Author{
			ChannelID:       "UCabcdef",
			DisplayName:     "alice",
			AvatarURL:       "https://yt3.example/photo.jpg",
			IsMember:        true,
			MemberLevelName: "Supporter",
			MemberMonths:    6,
		},
		Badges: []youtube.Badge{{Kind: youtube.BadgeMember, Label: "member", Info: "6 months"}},
		Text:   "hello chat",
		Kind:   youtube.EventKindSuperChat,
		Type:   youtube.MessageTypePaid,
		SuperChat: &youtube.SuperChatDetails{
			Amount: youtube.Money{Micros: 5_000_000, Currency: "USD", Display: "$5.00"},
			Tier:   3,
		},
		// RawType is retained for exactly this surface: when YouTube adds an
		// event kind, the row still renders and this names what it was.
		RawType: "superChatEvent",
	}
}

func testInspectState() inspectState {
	return inspectState{Palette: theme.DefaultPalette(), Message: testInspectMessage(), Selected: true}
}

func TestInspectHasExactDimensions(t *testing.T) {
	st := testInspectState()
	for _, width := range []int{20, 60, 100} {
		lines := plainLines(renderInspect(width, 7, 5, true, st))
		if len(lines) != 7 {
			t.Fatalf("width %d rendered %d rows", width, len(lines))
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got != width {
				t.Fatalf("width %d row %d is %d cells (%q)", width, i, got, line)
			}
		}
	}
}

func TestInspectWithNoSelectionExplainsHowToSelect(t *testing.T) {
	st := testInspectState()
	st.Selected = false
	rendered := strings.Join(plainLines(renderInspect(80, 7, 5, true, st)), "\n")
	if !strings.Contains(rendered, "no selected message") {
		t.Fatalf("empty inspect panel is unexplained:\n%s", rendered)
	}
	if !strings.Contains(rendered, "K") {
		t.Fatalf("empty inspect panel does not name the key:\n%s", rendered)
	}
}

// The inspect panel is the one surface that deliberately shows raw-ish values,
// which makes it the one place a credential-shaped string could reach a
// terminal that is frequently on stream. Redaction happens before fitting, so a
// secret cannot survive as a truncated prefix either.
func TestInspectRedactsCredentialShapedValues(t *testing.T) {
	leaks := []string{
		"access_token=" + auth.FakeTokenMarker,
		"refresh_token=" + auth.FakeTokenMarker,
		"client_secret=" + auth.FakeTokenMarker,
		"Bearer " + auth.FakeTokenMarker,
		"AIzaBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		"code=" + auth.FakeTokenMarker,
		"code_verifier=" + auth.FakeTokenMarker,
	}
	for _, leak := range leaks {
		st := testInspectState()
		st.Message.Text = "look at this " + leak
		st.Message.Author.DisplayName = leak
		st.Message.Fragments = []youtube.MessageFragment{{Type: youtube.FragmentText, Text: leak}}

		rendered := strings.Join(plainLines(renderInspect(200, 12, 10, true, st)), "\n")
		if strings.Contains(rendered, auth.FakeTokenMarker) {
			t.Errorf("inspect leaked %q:\n%s", leak, rendered)
		}
		if strings.Contains(rendered, "AIzaBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB") {
			t.Errorf("inspect leaked an API key:\n%s", rendered)
		}
	}
}

// The original snippet.type is the field that makes an unrecognized event
// diagnosable rather than mysterious.
func TestInspectReportsTheOriginalSnippetType(t *testing.T) {
	st := testInspectState()
	st.Message.Kind = youtube.EventKindUnknown
	st.Message.RawType = "somethingBrandNewEvent"
	rendered := strings.Join(plainLines(renderInspect(200, 12, 10, true, st)), "\n")
	if !strings.Contains(rendered, "somethingBrandNewEvent") {
		t.Fatalf("inspect omits the original snippet type:\n%s", rendered)
	}
}

// Money is reported as an integer micro-amount plus the API's own display
// string. No float ever touches a currency value.
func TestInspectReportsMoneyWithoutFloats(t *testing.T) {
	rendered := strings.Join(plainLines(renderInspect(200, 12, 10, true, testInspectState())), "\n")
	for _, want := range []string{"$5.00", "USD", "micros=5000000", "tier=3"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("inspect omits %q:\n%s", want, rendered)
		}
	}
}

// The avatar URL is recorded but never fetched: yc has no image path at all, so
// reporting "recorded" is honest and printing the URL would be noise.
func TestInspectDoesNotPrintTheAvatarURL(t *testing.T) {
	rendered := strings.Join(plainLines(renderInspect(200, 12, 10, true, testInspectState())), "\n")
	if strings.Contains(rendered, "yt3.example") {
		t.Fatalf("inspect printed an avatar URL:\n%s", rendered)
	}
	if !strings.Contains(rendered, "avatar=recorded") {
		t.Fatalf("inspect does not report the recorded avatar:\n%s", rendered)
	}
}

// The line has to be byte-identical between frames, or the panel flickers.
func TestInspectMessageLineIsStable(t *testing.T) {
	message := testInspectMessage()
	message.Historical, message.Deleted, message.LocalEcho = true, true, true
	first := inspectMessageLine(message)
	for range 20 {
		if got := inspectMessageLine(message); got != first {
			t.Fatalf("inspect message line is not deterministic:\n%q\n%q", first, got)
		}
	}
	for _, want := range []string{"historical=true", "deleted=true", "echo=true"} {
		if !strings.Contains(first, want) {
			t.Errorf("missing %q in %q", want, first)
		}
	}
}

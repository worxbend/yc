package render

import (
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// testTimestamp is built in time.Local so timestampText formats it identically
// wherever the suite runs; a UTC fixture would render a different clock face on
// every developer's machine.
var testTimestamp = time.Date(2026, 7, 1, 20, 0, 0, 0, time.Local)

// testPalette pins every role to a distinct, parseable color so contrast
// correction and identity hashing are exercised without depending on whichever
// preset happens to be the default.
func testPalette() theme.Palette {
	return theme.Palette{
		Background: "#0b0b10",
		Foreground: "#e6e6ef",
		Accent:     "#7aa2f7",
		Muted:      "#6f7285",
		Border:     "#2a2b3a",
		Surface:    "#15161f",
		Warning:    "#e0af68",
		Error:      "#f7768e",
		Success:    "#9ece6a",
	}
}

func testOptions(width int) Options {
	opts := DefaultOptions(width)
	opts.Palette = testPalette()
	return opts
}

// forceColorProfile pins lipgloss's default renderer to TrueColor for the
// duration of the test and restores the previous profile afterward.
//
// Setting env vars alone is not reliable: lipgloss and termenv detect and cache
// the profile once per process, so whichever test first touches rendering can
// otherwise lock in "no color" for every test that follows it in the same
// binary.
func forceColorProfile(t *testing.T) {
	t.Helper()
	original := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(original)
	})
}

func rowsToPlain(rows []Row) []string {
	plain := make([]string, 0, len(rows))
	for _, row := range rows {
		plain = append(plain, row.Plain())
	}
	return plain
}

func chatMessage(text string) youtube.Message {
	author := youtube.Author{
		ChannelID:   "UC_alice_0000000000001",
		DisplayName: "Alice Lovelace",
		ChannelURL:  "https://www.youtube.com/@alicelovelace",
		IsMember:    true,
	}
	return youtube.Message{
		ID:         "msg-1",
		LiveChatID: "chat-1",
		Timestamp:  testTimestamp,
		Author:     author,
		Badges:     youtube.BadgesForAuthor(author),
		Text:       text,
		Kind:       youtube.EventKindText,
		Type:       youtube.MessageTypeChat,
		RawType:    "textMessageEvent",
	}
}

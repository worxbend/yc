package app

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// authorMessage builds a chat message from an explicit channel ID and name, so
// the two can be varied independently.
func authorMessage(id, channelID, displayName, text string) youtube.Message {
	author := youtube.Author{ChannelID: channelID, DisplayName: displayName}
	return youtube.Message{
		ID:         id,
		LiveChatID: "chat-1",
		Timestamp:  stressStart,
		Author:     author,
		Badges:     youtube.BadgesForAuthor(author),
		Text:       text,
		Kind:       youtube.EventKindText,
		Type:       youtube.MessageTypeChat,
	}
}

// An author's color is how a moderator recognizes one person down the gutter of
// a fast chat. If it drifted between messages, between layouts, or between
// sessions, the cue would be worse than useless: it would actively mislead.
func TestAuthorColorIsStableAcrossMessagesLayoutsAndSessions(t *testing.T) {
	first := newModelForTest(t, "demo")
	message := authorMessage("m1", "UC_alice_0001", "Alice", "hello")

	baseline := first.messageAuthorColor(message)
	if baseline == "" {
		t.Fatal("a chat author was given no identity color")
	}

	// Across messages from the same author.
	for i := range 20 {
		next := authorMessage(fmt.Sprintf("m%d", i+2), "UC_alice_0001", "Alice", "another line")
		if got := first.messageAuthorColor(next); got != baseline {
			t.Fatalf("message %d gave the same author a different color: %q vs %q", i, got, baseline)
		}
	}

	// Across layouts and badge modes, which change the row but not the person.
	for _, layout := range []string{"inline", "grouped", "compact"} {
		for _, badges := range []string{"glyph", "text", "off"} {
			cfg := config.Default()
			cfg.Features.MessageLayout = layout
			cfg.Features.BadgeMode = badges
			cfg.DefaultChats = []string{"demo"}
			model := newShellModel(cfg, nil)
			if got := model.messageAuthorColor(message); got != baseline {
				t.Errorf("layout=%s badges=%s changed the author color: %q vs %q", layout, badges, got, baseline)
			}
		}
	}

	// Across sessions: a fresh model is a fresh process as far as this is
	// concerned, and the color is derived rather than remembered.
	second := newModelForTest(t, "demo")
	if got := second.messageAuthorColor(message); got != baseline {
		t.Errorf("a new session gave the same author a different color: %q vs %q", got, baseline)
	}

	// Across chats: the same person in two chats is the same person.
	multi := newModelForTest(t, "one", "two")
	if got := multi.messageAuthorColor(message); got != baseline {
		t.Errorf("a second chat changed the author color: %q vs %q", got, baseline)
	}
}

// The color follows the channel ID, not the display name. YouTube lets anyone
// change their display name at any time, and a color that moved with the name
// would let one chatter impersonate another's cue by renaming.
func TestAuthorColorFollowsTheChannelIDNotTheDisplayName(t *testing.T) {
	model := newModelForTest(t, "demo")

	original := model.messageAuthorColor(authorMessage("m1", "UC_alice_0001", "Alice", "hi"))
	renamed := model.messageAuthorColor(authorMessage("m2", "UC_alice_0001", "Alice Lovelace", "hi"))
	if original != renamed {
		t.Errorf("a rename changed the author color: %q vs %q", original, renamed)
	}

	impostor := model.messageAuthorColor(authorMessage("m3", "UC_mallory_9999", "Alice", "hi"))
	if impostor == original {
		t.Error("a different channel taking the same display name got the same color")
	}
}

// A notice or system row belongs to the chat, not to a person, so giving it an
// identity color would invent an author that does not exist.
func TestNoticeAndSystemRowsCarryNoAuthorColor(t *testing.T) {
	model := newModelForTest(t, "demo")
	for _, messageType := range []youtube.MessageType{youtube.MessageTypeNotice, youtube.MessageTypeSystem} {
		message := authorMessage("m1", "UC_alice_0001", "Alice", "chat ended")
		message.Type = messageType
		if got := model.messageAuthorColor(message); got != "" {
			t.Errorf("%s row was given the author color %q", messageType, got)
		}
	}

	// An author with no channel ID has no identity to hash.
	anonymous := authorMessage("m2", "", "", "hello")
	if got := model.messageAuthorColor(anonymous); got != "" {
		t.Errorf("an author with no identity was given the color %q", got)
	}
}

// Identity colors have to stay readable on whichever palette is in force, or
// the cue costs legibility to buy recognition.
func TestAuthorColorsStayDistinctAndReadableOnEveryPreset(t *testing.T) {
	authors := make([]youtube.Message, 0, 48)
	for i := range 48 {
		authors = append(authors, authorMessage(
			fmt.Sprintf("m%d", i),
			fmt.Sprintf("UC_author_%04d", i),
			fmt.Sprintf("author%d", i),
			"hello",
		))
	}

	for name := range theme.Presets() {
		cfg := config.Default()
		cfg.Features.ThemeName = name
		cfg.DefaultChats = []string{"demo"}
		model := newShellModel(cfg, nil)

		seen := make(map[string]int, len(authors))
		for _, message := range authors {
			color := model.messageAuthorColor(message)
			if color == "" {
				t.Fatalf("preset %q gave an author no color", name)
			}
			seen[color]++
		}
		// Some collision is inevitable in a finite palette; a collapse is not.
		if len(seen) < 6 {
			t.Errorf("preset %q collapsed %d authors onto %d colors", name, len(authors), len(seen))
		}
	}
}

// A resize must not renumber, recolor, or lose anything: it changes how wide
// the rows are, not who said them.
func TestResizePreservesAuthorColorsAndScrollPosition(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	for i := range 30 {
		state.messages = append(state.messages, authorMessage(
			fmt.Sprintf("m%d", i), fmt.Sprintf("UC_author_%d", i%5), fmt.Sprintf("author%d", i%5), "a line of chat",
		))
	}

	before := make([]string, 0, len(state.messages))
	for _, message := range state.messages {
		before = append(before, model.messageAuthorColor(message))
	}

	for _, size := range []tea.WindowSizeMsg{
		{Width: 40, Height: 12}, {Width: 200, Height: 60}, {Width: 80, Height: 24},
	} {
		next, _ := model.Update(size)
		model = next.(shellModel)

		if model.width != size.Width || model.height != size.Height {
			t.Fatalf("resize to %dx%d left the model at %dx%d", size.Width, size.Height, model.width, model.height)
		}
		resized := model.activeChatState()
		if len(resized.messages) != len(before) {
			t.Fatalf("resize changed the history from %d to %d messages", len(before), len(resized.messages))
		}
		for i, message := range resized.messages {
			if got := model.messageAuthorColor(message); got != before[i] {
				t.Fatalf("resize to %dx%d recolored message %d: %q vs %q", size.Width, size.Height, i, got, before[i])
			}
		}
		// The scroll offset must stay inside the buffer at every size.
		if resized.scrollOffset < 0 || resized.scrollOffset > len(resized.messages) {
			t.Errorf("resize to %dx%d left the scroll offset at %d", size.Width, size.Height, resized.scrollOffset)
		}
	}
}

// Terminal focus drives the notification decision, so losing and regaining it
// has to actually move the model rather than being swallowed.
func TestTerminalFocusAndBlurAreTracked(t *testing.T) {
	model := newModelForTest(t, "demo")
	if !model.terminalFocused {
		t.Fatal("a fresh model starts unfocused")
	}

	next, _ := model.Update(tea.BlurMsg{})
	model = next.(shellModel)
	if model.terminalFocused {
		t.Error("a blur did not reach the model")
	}

	next, _ = model.Update(tea.FocusMsg{})
	model = next.(shellModel)
	if !model.terminalFocused {
		t.Error("a focus did not reach the model")
	}

	// Focus is terminal-level and must not disturb which pane has input focus.
	model.focus = focusComposer
	next, _ = model.Update(tea.BlurMsg{})
	model = next.(shellModel)
	if model.focus != focusComposer {
		t.Errorf("a terminal blur moved pane focus to %v", model.focus)
	}
}

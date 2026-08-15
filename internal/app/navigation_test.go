package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestVimScrollKeys(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	for i := 0; i < 40; i++ {
		state.messages = append(state.messages, testMessage(t, string(rune('a'+i)), "", "alice", "x"))
	}

	// g jumps to the oldest retained row, G back to the live bottom.
	model = press(t, model, runeKey('g'))
	if model.activeChatState().scrollOffset <= 0 {
		t.Fatal("g did not scroll back into history")
	}
	model = press(t, model, runeKey('G'))
	if got := model.activeChatState().scrollOffset; got != 0 {
		t.Fatalf("after G, scrollOffset = %d, want 0", got)
	}

	// ctrl+u scrolls half a page into history; ctrl+d comes half a page back.
	model = press(t, model, key(tea.KeyCtrlU))
	half := model.activeChatState().scrollOffset
	if half <= 0 {
		t.Fatal("ctrl+u did not scroll back into history")
	}
	model = press(t, model, key(tea.KeyCtrlD))
	if got := model.activeChatState().scrollOffset; got != 0 {
		t.Fatalf("ctrl+d did not return to the bottom, scrollOffset = %d", got)
	}

	// Inside the composer ctrl+u keeps its "clear the line" meaning.
	model.focus = focusComposer
	model.activeChatState().composerText = "draft"
	model = press(t, model, key(tea.KeyCtrlU))
	if model.activeChatState().composerText != "" {
		t.Fatal("ctrl+u in the composer did not clear the draft")
	}
	if model.activeChatState().scrollOffset != 0 {
		t.Fatal("ctrl+u in the composer scrolled the chat")
	}
}

func TestNewMessagesIndicatorCountsAndClears(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.target.LiveChatID = "live-chat"
	for i := 0; i < 40; i++ {
		state.messages = append(state.messages, testMessage(t, string(rune('a'+i)), "live-chat", "alice", "x"))
	}
	model = press(t, model, key(tea.KeyPgUp))
	if !model.scrolledAway() {
		t.Fatal("page up did not scroll away")
	}

	model.enqueueMessage(testMessage(t, "new-1", "live-chat", "bob", "hi"))
	model.enqueueMessage(testMessage(t, "new-2", "live-chat", "bob", "ho"))
	if got := model.activeChatState().newBelow; got != 2 {
		t.Fatalf("newBelow = %d, want 2", got)
	}

	// The bottom viewport row carries the sticky indicator.
	frame := model.View()
	if !strings.Contains(frame, "2 new") {
		t.Fatal("the frame does not show the new-message indicator while scrolled away")
	}

	// Jumping to the bottom clears it.
	model = press(t, model, runeKey('G'))
	if got := model.activeChatState().newBelow; got != 0 {
		t.Fatalf("after G, newBelow = %d, want 0", got)
	}
	if strings.Contains(model.View(), "new · G to jump") {
		t.Fatal("the indicator survived the jump to the bottom")
	}
}

func TestHistoricalBacklogDoesNotFeedTheIndicator(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.target.LiveChatID = "live-chat"
	state.messages = append(state.messages, testMessage(t, "m1", "live-chat", "alice", "x"))
	state.scrollOffset = 3

	historical := testMessage(t, "old-1", "live-chat", "bob", "backlog")
	historical.Historical = true
	model.enqueueMessage(historical)
	if got := model.activeChatState().newBelow; got != 0 {
		t.Fatalf("a historical backlog row counted as new, newBelow = %d", got)
	}
}

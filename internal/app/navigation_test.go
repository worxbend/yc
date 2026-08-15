package app

import (
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

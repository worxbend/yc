package app

import (
	"strings"
	"testing"
)

func TestCopyKeyStagesAnOSC52PayloadAndRetiresIt(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.messages = append(state.messages, testMessage(t, "m1", "", "alice", "copy \x1b[31mme\x1b[0m\nplease"))

	next, cmd := model.Update(runeKey('y'))
	model = next.(shellModel)
	if cmd == nil {
		t.Fatal("y did not schedule the clipboard flush")
	}
	// Control characters are stripped and newlines flattened, so a hostile
	// message cannot smuggle escape sequences into a paste.
	if got := model.clipboardText; got != "copy [31mme[0m please" {
		t.Fatalf("clipboardText = %q; control characters must be stripped", got)
	}
	if !strings.Contains(model.activeChatState().sendFeedback, "copied") {
		t.Fatal("y gave no feedback")
	}

	// The sequence only ever rides in interactive frames.
	if model.clipboardSequence() != "" {
		t.Fatal("clipboard sequence emitted without an interactive terminal")
	}
	model.terminalOutput = &strings.Builder{}
	if seq := model.clipboardSequence(); !strings.Contains(seq, "]52;c;") {
		t.Fatalf("clipboard sequence = %q, want an OSC 52 write", seq)
	}

	// The flush message retires the payload; a stale one must not.
	model.retireClipboard(clipboardClearMsg{seq: model.clipboardSeq - 1})
	if model.clipboardText == "" {
		t.Fatal("a stale clipboard timer dropped a newer payload")
	}
	model.retireClipboard(clipboardClearMsg{seq: model.clipboardSeq})
	if model.clipboardText != "" {
		t.Fatal("the clipboard payload was not retired")
	}
}

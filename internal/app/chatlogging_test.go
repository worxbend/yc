package app

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/youtube"
)

// recordingChatLogger is an in-memory ChatLogger that can be told to fail.
type recordingChatLogger struct {
	appended []youtube.Message
	err      error
}

func (l *recordingChatLogger) Append(message youtube.Message) error {
	if l.err != nil {
		return l.err
	}
	l.appended = append(l.appended, message)
	return nil
}

// deliver feeds one inbound transport message through Update.
func deliver(t *testing.T, model shellModel, message youtube.Message) shellModel {
	t.Helper()
	next, _ := model.Update(chatClientMessageMsg{message: message, ok: true})
	updated, ok := next.(shellModel)
	if !ok {
		t.Fatalf("Update returned %T, want shellModel", next)
	}
	return updated
}

func TestInboundMessagesReachTheChatLogger(t *testing.T) {
	model := newModelForTest(t, "demo")
	logger := &recordingChatLogger{}
	model.chatLogger = logger

	model = deliver(t, model, testMessage(t, "m1", "demo", "alice", "hello"))
	model = deliver(t, model, testMessage(t, "m2", "demo", "bob", "hi"))

	if len(logger.appended) != 2 {
		t.Fatalf("logged = %d messages, want 2", len(logger.appended))
	}
	if logger.appended[0].Text != "hello" || logger.appended[1].Text != "hi" {
		t.Fatalf("logged texts = %q, %q", logger.appended[0].Text, logger.appended[1].Text)
	}
	if model.chatLogFailed {
		t.Fatal("chatLogFailed latched without a failure")
	}
}

func TestNoChatLoggerMeansNoLoggingAndNoPanic(t *testing.T) {
	model := newModelForTest(t, "demo")
	model = deliver(t, model, testMessage(t, "m1", "demo", "alice", "hello"))
	if model.chatLogFailed {
		t.Fatal("chatLogFailed latched with no logger configured")
	}
}

func TestChatLogFailureDegradesToOneNoticeAndDisablesLogging(t *testing.T) {
	model := newModelForTest(t, "demo")
	logger := &recordingChatLogger{err: errors.New("disk full")}
	model.chatLogger = logger

	model = deliver(t, model, testMessage(t, "m1", "demo", "alice", "hello"))
	if !model.chatLogFailed {
		t.Fatal("first failure did not latch chatLogFailed")
	}
	state := model.activeChatState()
	if state == nil || state.status.Detail != "chat log write failed; logging is off for this session" {
		t.Fatalf("status detail = %q; the failure must surface once in the status line", state.status.Detail)
	}

	// The message itself must still be on screen: logging failure can never
	// cost the session its chat.
	if len(state.messages) != 1 {
		t.Fatalf("messages on screen = %d, want 1", len(state.messages))
	}

	// Later messages skip the dead logger instead of erroring per message.
	logger.err = nil
	model = deliver(t, model, testMessage(t, "m2", "demo", "bob", "hi"))
	if len(logger.appended) != 0 {
		t.Fatal("logging resumed after a failure; it must stay off for the session")
	}
}

var _ tea.Model = shellModel{}

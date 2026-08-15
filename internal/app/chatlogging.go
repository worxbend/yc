package app

import (
	"context"

	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/youtube"
)

// logChatMessage appends one inbound message to the opt-in chat log.
//
// It is failure-tolerant by design: the first write error disables logging for
// the rest of the session and surfaces once, as a status-line detail on the
// message's chat, so a full disk costs the user their log and one notice - it
// can never cost them the chat session, and it can never repeat the error for
// every message that follows.
func (m *shellModel) logChatMessage(message youtube.Message) {
	if m.chatLogger == nil || m.chatLogFailed {
		return
	}
	err := m.chatLogger.Append(message)
	if err == nil {
		return
	}
	m.chatLogFailed = true
	m.debugLogger.Log(context.Background(), "app.chat_log.write_failed",
		debuglog.Err("error", err),
	)
	if state := m.chats.stateForChatID(message.LiveChatID); state != nil {
		state.status.Detail = "chat log write failed; logging is off for this session"
	}
}

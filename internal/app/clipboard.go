package app

import (
	"encoding/base64"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// clipboardFlushDelay is how long the OSC 52 clipboard sequence stays embedded
// in the frame before it is dropped again. One rendered frame is enough for
// the terminal to see the sequence; the delay only has to outlive the
// renderer's frame cadence.
const clipboardFlushDelay = 300 * time.Millisecond

// clipboardClearMsg retires an emitted OSC 52 sequence. It carries the
// sequence number so a stale timer from an earlier copy cannot cancel a newer
// one that is still waiting to render.
type clipboardClearMsg struct{ seq int }

// copySelectedMessage copies the browsing cursor's message text - or the
// newest message when nothing is selected - to the system clipboard over
// OSC 52. The sequence travels inside the rendered frame for the same reason
// the OSC 11 background does: Bubble Tea's renderer owns the output writer,
// and writing around it from a command races the frame it is painting.
func (m shellModel) copySelectedMessage() (tea.Model, tea.Cmd) {
	state := m.activeChatState()
	if state == nil {
		return m, nil
	}
	if state.selected == nil {
		m.selectMessage(-1)
	}
	if state.selected == nil {
		return m, nil
	}
	text := sanitizeClipboardText(state.selected.Text)
	if text == "" {
		state.sendFeedback = "nothing to copy"
		return m, nil
	}
	m.clipboardSeq++
	m.clipboardText = text
	state.sendFeedback = "copied message to clipboard"
	seq := m.clipboardSeq
	return m, tea.Tick(clipboardFlushDelay, func(time.Time) tea.Msg {
		return clipboardClearMsg{seq: seq}
	})
}

// retireClipboard drops an emitted clipboard payload once its sequence has had
// a frame to render. A stale timer from an earlier copy is ignored so it
// cannot cancel a newer payload still waiting to be drawn.
func (m *shellModel) retireClipboard(msg clipboardClearMsg) {
	if msg.seq == m.clipboardSeq {
		m.clipboardText = ""
	}
}

// clipboardSequence is the OSC 52 sequence embedded in the frame, or empty
// when there is nothing pending or the session is not interactive - piped and
// test output must stay free of escape codes.
func (m shellModel) clipboardSequence() string {
	if m.terminalOutput == nil || m.clipboardText == "" {
		return ""
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(m.clipboardText))
	return ansi.SetClipboard('c', encoded)
}

// sanitizeClipboardText strips control characters so a hostile message cannot
// smuggle escape sequences into whatever the user pastes into next. Newlines
// become spaces, matching how the composer treats them.
func sanitizeClipboardText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

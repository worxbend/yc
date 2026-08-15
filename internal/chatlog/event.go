package chatlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/worxbend/yc/internal/youtube"
)

// Event is one logged chat event, flattened for line-oriented consumers.
//
// The JSON field names are the log's file format: changing one is a breaking
// change for anything that reads existing logs, including `yc export
// superchats`. Optional fields carry omitempty so an ordinary text message
// stays a short line.
type Event struct {
	// Timestamp is the event's own time (when the message was published),
	// not when it was written to the log.
	Timestamp time.Time `json:"ts"`
	// ChatID is the identifier the app routes this chat under. Depending on
	// how the chat was opened it may be a liveChatId, a video ID, a channel
	// ID, or an @handle.
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id,omitempty"`
	// Kind is the normalized event kind ("text", "super_chat", ...); Type is
	// the coarse render class ("chat", "paid", "membership", ...). Both come
	// from internal/youtube's normalization.
	Kind string `json:"kind"`
	Type string `json:"type"`

	AuthorChannelID string `json:"author_channel_id,omitempty"`
	Author          string `json:"author,omitempty"`

	Text string `json:"text,omitempty"`

	// AmountMicros is a paid event's value in millionths of the currency
	// unit, exactly as the API supplies it - never a float, so no rounding
	// can occur between the wire and the ledger export.
	AmountMicros  int64  `json:"amount_micros,omitempty"`
	Currency      string `json:"currency,omitempty"`
	AmountDisplay string `json:"amount_display,omitempty"`
	// Tier is YouTube's 1-11 purchase tier, or 0 when absent.
	Tier int `json:"tier,omitempty"`

	// LocalEcho marks a row yc rendered from its own send; Historical marks
	// a row from the priming backlog rather than the live tail.
	LocalEcho  bool `json:"local_echo,omitempty"`
	Historical bool `json:"historical,omitempty"`
}

// FromMessage flattens a normalized message into its logged form.
func FromMessage(message youtube.Message) Event {
	event := Event{
		Timestamp:       message.Timestamp,
		ChatID:          message.LiveChatID,
		MessageID:       message.ID,
		Kind:            string(message.Kind),
		Type:            string(message.Type),
		AuthorChannelID: message.Author.ChannelID,
		Author:          message.Author.DisplayName,
		Text:            message.Text,
		LocalEcho:       message.LocalEcho,
		Historical:      message.Historical,
	}
	if amount, ok := message.Amount(); ok {
		event.AmountMicros = amount.Micros
		event.Currency = amount.Currency
		event.AmountDisplay = amount.Display
	}
	event.Tier = message.Tier()
	return event
}

// Records streams every decodable event from one JSONL log to fn, in file
// order.
//
// A malformed line is skipped rather than failing the whole read: a log cut
// off mid-line by a crash still has every complete record before it, and those
// records are the reason the log exists. An error from fn stops the scan and
// is returned as-is.
func Records(r io.Reader, fn func(Event) error) error {
	decoder := json.NewDecoder(&limitedLineReader{r: r})
	for {
		var event Event
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// The decoder cannot resync after a syntax error, and a line cut
			// off by a crash surfaces as an unexpected EOF. Either way the
			// corrupt tail ends the scan; everything decoded before it stands.
			var syntaxErr *json.SyntaxError
			if errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return fmt.Errorf("read chat log: %w", err)
		}
		if err := fn(event); err != nil {
			return err
		}
	}
}

// limitedLineReader caps how much a single Records call can pull through, so a
// pathologically large file cannot be read without bound by an export.
type limitedLineReader struct {
	r    io.Reader
	read int64
}

// maxLogReadBytes bounds one whole log file read (current rotation budget
// times a generous safety factor).
const maxLogReadBytes = int64(1) << 30

func (l *limitedLineReader) Read(p []byte) (int, error) {
	if l.read >= maxLogReadBytes {
		return 0, io.EOF
	}
	if remaining := maxLogReadBytes - l.read; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := l.r.Read(p)
	l.read += int64(n)
	return n, err
}

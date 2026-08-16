package chatlog

import (
	"bufio"
	"bytes"
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
// order, and returns how many lines it could not decode.
//
// The log is one JSON object per line, so a damaged line damages exactly one
// record: the scan drops it, moves to the next newline, and carries on. That
// resynchronization is the whole point of the returned count. An earlier
// version stopped at the first syntax error, which meant a single torn line -
// the shape a failed or short write leaves behind - silently hid every record
// after it while the export still reported success. Callers are expected to
// surface a non-zero skipped count so a partial export is never mistaken for a
// complete one. An error from fn stops the scan and is returned as-is.
func Records(r io.Reader, fn func(Event) error) (skipped int, err error) {
	scanner := bufio.NewScanner(&limitedLineReader{r: r})
	// A single record is far smaller than this, but a corrupt file can hold
	// a line of any length and the scanner's 64 KiB default would turn that
	// into a whole-file failure.
	scanner.Buffer(make([]byte, 0, 64*1024), maxLogLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event Event
		if decodeErr := json.Unmarshal(line, &event); decodeErr != nil {
			skipped++
			continue
		}
		if err := fn(event); err != nil {
			return skipped, err
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		// A line too long to buffer is corruption rather than a read failure,
		// and the records around it are still worth returning.
		if errors.Is(scanErr, bufio.ErrTooLong) {
			return skipped + 1, nil
		}
		return skipped, fmt.Errorf("read chat log: %w", scanErr)
	}
	return skipped, nil
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

// maxLogLineBytes bounds one line. A chat message plus its metadata is a few
// hundred bytes; this leaves room for an unusually long one without letting a
// corrupt file be buffered without bound.
const maxLogLineBytes = 4 << 20

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

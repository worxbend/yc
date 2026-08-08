package youtube

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rivo/uniseg"
)

// Local send allowance defaults.
//
// YouTube publishes no chat send limit for the API, so these are conservative
// values chosen to keep a human typist unblocked while making an accidental
// loop impossible. They cost a moderator nothing they would notice by hand.
const (
	defaultSendBurst    = 3
	defaultSendInterval = 2 * time.Second
)

// SendLimiterConfig bounds how fast yc will dispatch sends.
//
// The limiter declines locally rather than learning from a 429, because an
// insert costs an estimated 50 units whether it succeeds or not: discovering
// the limit by hitting it is the most expensive possible way to find it.
type SendLimiterConfig struct {
	// Burst is how many sends may go out back to back.
	Burst int
	// Interval is the steady-state spacing between sends.
	Interval time.Duration
	Now      func() time.Time
}

// SendLimiter is a local token-bucket limiter for outbound chat messages.
//
// It is safe for concurrent use: the composer and a command-palette action can
// both reach it.
type SendLimiter struct {
	cfg SendLimiterConfig

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// NewSendLimiter returns a limiter with defaults applied.
func NewSendLimiter(cfg SendLimiterConfig) *SendLimiter {
	if cfg.Burst <= 0 {
		cfg.Burst = defaultSendBurst
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultSendInterval
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &SendLimiter{cfg: cfg, tokens: float64(cfg.Burst), last: cfg.Now()}
}

// Allow reports whether a send may go out now, and when to retry if not.
//
// A rejection names how long to wait rather than just saying no, because the
// composer shows that number and a moderator deciding whether to retype is
// entitled to know.
func (l *SendLimiter) Allow() (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.cfg.Now()
	if l.last.IsZero() {
		l.last = now
		l.tokens = float64(l.cfg.Burst)
	}
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += elapsed.Seconds() / l.cfg.Interval.Seconds()
		if l.tokens > float64(l.cfg.Burst) {
			l.tokens = float64(l.cfg.Burst)
		}
		l.last = now
	}

	if l.tokens >= 1 {
		l.tokens--
		return true, 0
	}
	missing := 1 - l.tokens
	return false, time.Duration(missing * float64(l.cfg.Interval))
}

// sendPath is the liveChatMessages collection.
const sendPath = "liveChat/messages"

// liveChatInsertRequest is the liveChatMessages.insert body.
type liveChatInsertRequest struct {
	Snippet struct {
		LiveChatID         string `json:"liveChatId"`
		Type               string `json:"type"`
		TextMessageDetails struct {
			MessageText string `json:"messageText"`
		} `json:"textMessageDetails"`
	} `json:"snippet"`
}

// SendMessage posts one chat message through liveChatMessages.insert.
//
// A reply is expressed by prepending "@DisplayName " to the text: YouTube live
// chat has no parent-message field, so there is nothing to thread. The text is
// capped at MaxChatMessageRunes before dispatch so an over-long draft never
// costs quota.
func (c *Client) SendMessage(ctx context.Context, request SendRequest) (SendResult, error) {
	result := SendResult{QuotaUnits: c.cost(EndpointMessagesInsert)}

	liveChatID := strings.TrimSpace(request.LiveChatID)
	if liveChatID == "" {
		return result, newSafeError(EndpointMessagesInsert+": no live chat id", ErrMessageRejected)
	}
	if !c.hasToken() {
		// An API key can read a public chat but can never write to one.
		// Saying so costs nothing; finding out from the API costs 50 units.
		return result, newSafeError(
			"sending requires signing in with Google (scope youtube.force-ssl)",
			ErrNotPermitted,
		)
	}

	text := composeSendText(request)
	if text == "" {
		return result, newSafeError("nothing to send", ErrMessageRejected)
	}

	var body liveChatInsertRequest
	body.Snippet.LiveChatID = liveChatID
	body.Snippet.Type = "textMessageEvent"
	body.Snippet.TextMessageDetails.MessageText = text

	var response struct {
		ID      string `json:"id"`
		Snippet struct {
			PublishedAt string `json:"publishedAt"`
		} `json:"snippet"`
	}
	query := map[string]string{"part": "snippet"}
	if err := c.doJSON(ctx, "POST", EndpointMessagesInsert, sendPath, query, body, &response); err != nil {
		if apiErr, ok := asAPIError(err); ok && apiErr.StatusCode == 429 {
			// insert reports rate limiting as 429, unlike list, which uses
			// a 403 with reason rateLimitExceeded.
			result.RateLimited = true
			result.Detail = "YouTube is rate limiting sends; try again shortly"
		}
		return result, err
	}

	result.MessageID = response.ID
	result.AcceptedAt = parseAPITime(response.Snippet.PublishedAt, c.now())
	return result, nil
}

// composeSendText applies the reply convention and the length cap.
//
// The cap is enforced on grapheme-cluster boundaries: truncating a ZWJ emoji or
// a combining mark by rune would put mojibake into someone's public chat.
func composeSendText(request SendRequest) string {
	text := strings.TrimSpace(request.Text)
	if name := strings.TrimSpace(request.ReplyToDisplayName); name != "" {
		text = strings.TrimSpace("@" + name + " " + text)
	}
	return capGraphemes(text, MaxChatMessageRunes)
}

// capGraphemes truncates to at most limit runes without ever splitting a
// grapheme cluster.
func capGraphemes(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	graphemes := uniseg.NewGraphemes(value)
	runes := 0
	end := 0
	for graphemes.Next() {
		cluster := graphemes.Runes()
		if runes+len(cluster) > limit {
			return value[:end]
		}
		runes += len(cluster)
		_, end = graphemes.Positions()
	}
	return value
}

// asAPIError extracts the classified API error from a chain, if there is one.
func asAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

package app

import (
	"strings"

	"github.com/worxbend/yc/internal/youtube"
)

// The target picker is how a live chat gets opened without leaving the
// terminal. Twitch's equivalent lists channel logins; YouTube's unit is a
// broadcast, so the picker accepts everything ParseChatTarget does - a raw
// video ID, a watch/live/shorts URL, an @handle, a channel ID - and offers
// what the session already knows about as suggestions.
//
// It never searches. search.list draws on a separate 100-calls-per-day bucket,
// so a picker that searched as you typed would spend a scarce daily allowance
// on keystrokes. Anything typed can be opened verbatim instead.

// targetPickerKeyHint names the key that opens the picker. It appears in the
// empty state, the help strip, and the picker's own header, so the three cannot
// drift apart.
const targetPickerKeyHint = "space c"

// targetPickerSource classifies where a row came from, which is what its detail
// column names. The distinction matters: switching to an already-open chat is
// free, while resolving a new one costs a quota unit.
type targetPickerSource string

const (
	targetSourceOpen       targetPickerSource = "open"
	targetSourceSubscribed targetPickerSource = "subscribed"
	targetSourceConfigured targetPickerSource = "configured"
	// targetSourceLiteral is the "open exactly what was typed" row, so a
	// broadcast in no list is still one keystroke away.
	targetSourceLiteral targetPickerSource = "open by name"
)

// targetPickerHeader renders the picker's query line.
func targetPickerHeader(query, err string, loading bool) string {
	switch {
	case loading:
		return " Open a live chat: loading subscriptions…"
	case strings.TrimSpace(err) != "":
		// A failed or absent subscriptions lookup is not something the user
		// has to act on: the picker still lists open chats and configured
		// defaults and still accepts any typed target.
		return " Open a live chat: " + redactDiagnosticText(err)
	case strings.TrimSpace(query) != "":
		return " Open a live chat: " + query
	default:
		return " Open a live chat — video ID, URL, or @handle"
	}
}

// targetPickerDetail classifies one candidate row for the detail column.
//
// The classification is derived from the session's own state rather than
// carried alongside the item, because the model's overlay items are plain
// strings; deriving keeps the two from disagreeing about what a row is.
func targetPickerDetail(item string, openLabels, configured []string) string {
	if containsFold(openLabels, item) {
		return string(targetSourceOpen)
	}
	if containsFold(configured, item) {
		return string(targetSourceConfigured)
	}
	if _, err := youtube.ParseChatTarget(item); err == nil {
		return string(targetSourceLiteral)
	}
	return ""
}

// targetPickerSubscriptionLabels renders subscriptions as picker candidates.
// A handle is preferred over a channel ID because it is what a person can read
// back and confirm they picked the right creator.
func targetPickerSubscriptionLabels(subscriptions []youtube.Subscription) []string {
	labels := make([]string, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		if label := firstNonEmpty(subscription.Handle, subscription.Title, subscription.ChannelID); label != "" {
			labels = append(labels, label)
		}
	}
	return labels
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

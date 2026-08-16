package youtube

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/quota"
)

// listPath is the liveChatMessages collection.
//
// yc polls this rather than liveChatMessages.streamList. streamList is
// documented in HTML as a low-latency server-streaming alternative, but it is
// absent from the REST discovery document, which exposes only insert, delete,
// list, and transition - so it is unreachable from a plain REST client today.
// This call is factored so streamList can be substituted behind it if it ever
// ships.
const listPath = "liveChat/messages"

// ListRequest is one liveChatMessages.list call.
type ListRequest struct {
	LiveChatID string
	// PageToken continues from the previous response. Empty means the
	// priming call, which returns recent history at once.
	PageToken string
	// Historical marks every produced message as backlog. The poller sets it
	// for the priming page so a 2000-row history is neither animated nor
	// notified.
	Historical bool
	// SeenMessageIDs lets tombstone correlation tell a deletion the viewer
	// can act on from one with nothing on screen to redact.
	SeenMessageIDs func(id string) bool
	// ProfileImageSize is the documented 16-720 avatar size hint. Zero uses
	// the API default. yc never fetches the image; the value only keeps the
	// recorded URL honest for diagnostics.
	ProfileImageSize int
}

// MaxResultsPerPoll is what yc always requests.
//
// The documented range for liveChatMessages.list is 200-2000 with a default of
// 500, so the commonly cited 200 is the minimum, not the maximum. Quota is
// charged per method call and not per item, so asking for 2000 costs exactly
// the same as asking for 200 and buys an order of magnitude more headroom
// between polls. It is the cheapest robustness win available.
const MaxResultsPerPoll = 2000

// ListResult is one normalized liveChatMessages.list response.
type ListResult struct {
	NormalizeResult

	// NextPageToken continues the stream. The poller retains the last good
	// token when this comes back empty: falling back to a token-less request
	// re-delivers the entire backlog.
	NextPageToken string
	// PollingInterval is pollingIntervalMillis. It is an absolute floor -
	// yc must never poll faster, under any circumstance.
	PollingInterval time.Duration
	// OfflineAt marks the start of the post-broadcast window. Chat outlives
	// the stream by a short while, so this is not itself a reason to stop.
	OfflineAt time.Time
	// TotalResults is pageInfo.totalResults, for diagnostics only.
	TotalResults int
	// QuotaUnits is the estimated cost charged for this call.
	QuotaUnits int
}

// ListMessages performs one liveChatMessages.list call and normalizes it.
//
// maxResults is always MaxResultsPerPoll. Quota is charged per method call and
// not per item, so asking for the documented maximum costs exactly what asking
// for the minimum costs and buys an order of magnitude more headroom between
// polls; it is the cheapest robustness win the API offers.
//
// This is the transport half of the poll loop. The cadence, the backoff ladder,
// the dedupe ring, and the page-token retention live in poll.go, which owns the
// schedule.
func (c *Client) ListMessages(ctx context.Context, request ListRequest) (ListResult, error) {
	liveChatID := strings.TrimSpace(request.LiveChatID)
	if liveChatID == "" {
		return ListResult{}, newSafeError(quota.EndpointMessagesList+": no live chat id", ErrChatNotFound)
	}

	query := map[string]string{
		"liveChatId": liveChatID,
		"part":       "snippet,authorDetails",
		"maxResults": strconv.Itoa(MaxResultsPerPoll),
	}
	if token := strings.TrimSpace(request.PageToken); token != "" {
		query["pageToken"] = token
	}
	if hl := strings.TrimSpace(c.cfg.HL); hl != "" {
		// hl localizes amountDisplayString, which is the string yc renders
		// for every paid event.
		query["hl"] = hl
	}
	if size := request.ProfileImageSize; size >= 16 && size <= 720 {
		query["profileImageSize"] = strconv.Itoa(size)
	}

	var response liveChatMessageListResponse
	err := c.doJSON(ctx, "GET", quota.EndpointMessagesList, listPath, query, nil, &response)
	result := ListResult{QuotaUnits: c.cost(quota.EndpointMessagesList)}
	if err != nil {
		return result, err
	}

	opts := NormalizeOptions{
		Historical:     request.Historical,
		Now:            c.now(),
		SeenMessageIDs: request.SeenMessageIDs,
	}
	for _, item := range response.Items {
		result.append(normalizeItem(item, opts))
	}
	// activePollItem is a full message resource carried out of band, set
	// while a poll is live. It is normalized for its poll state only: it is
	// not a new chat row and must not be appended to history again.
	if response.ActivePollItem != nil {
		if poll, ok := normalizePoll(*response.ActivePollItem, parseAPITime(response.ActivePollItem.Snippet.PublishedAt, opts.Now)); ok {
			result.Polls = append(result.Polls, poll)
		}
	}

	result.NextPageToken = strings.TrimSpace(response.NextPageToken)
	if response.PollingIntervalMs > 0 {
		result.PollingInterval = time.Duration(response.PollingIntervalMs) * time.Millisecond
	}
	result.OfflineAt = parseAPITime(response.OfflineAt, time.Time{})
	result.TotalResults = response.PageInfo.TotalResults
	return result, nil
}

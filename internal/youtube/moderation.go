package youtube

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// bansPath is the liveChatBans collection.
const bansPath = "liveChat/bans"

// liveChatBanRequest is the liveChatBans.insert body. banDurationSeconds is a
// uint64 the API expects as a JSON string, matching how it sends one back.
type liveChatBanRequest struct {
	Snippet struct {
		LiveChatID         string `json:"liveChatId"`
		Type               string `json:"type"`
		BanDurationSeconds string `json:"banDurationSeconds,omitempty"`
		BannedUserDetails  struct {
			ChannelID string `json:"channelId"`
		} `json:"bannedUserDetails"`
	} `json:"snippet"`
}

// DeleteMessage removes one chat message through liveChatMessages.delete.
//
// A successful delete is echoed locally as a ModerationMessageDeleted, because
// the API does not reliably report deletions back: messageDeletedEvent was
// removed from the reference on 2026-06-23 as "not being returned by the API",
// and the only inbound signal is a previously seen message reappearing as a
// tombstone.
func (c *Client) DeleteMessage(ctx context.Context, messageID string) error {
	identifier := strings.TrimSpace(messageID)
	if identifier == "" {
		return newSafeError(EndpointMessagesDelete+": no message id", ErrMessageRejected)
	}
	if !c.hasToken() {
		return newSafeError("deleting messages requires signing in with Google (scope youtube.force-ssl)", ErrNotPermitted)
	}
	query := map[string]string{"id": identifier}
	return c.doJSON(ctx, "DELETE", EndpointMessagesDelete, sendPath, query, nil, nil)
}

// Ban times out or permanently bans a chatter through liveChatBans.insert. A
// zero Duration means a permanent ban.
func (c *Client) Ban(ctx context.Context, request BanRequest) (BanResult, error) {
	liveChatID := strings.TrimSpace(request.LiveChatID)
	channelID := strings.TrimSpace(request.ChannelID)
	switch {
	case liveChatID == "":
		return BanResult{}, newSafeError(EndpointBansInsert+": no live chat id", ErrMessageRejected)
	case channelID == "":
		return BanResult{}, newSafeError(EndpointBansInsert+": no channel id", ErrMessageRejected)
	case !c.hasToken():
		return BanResult{}, newSafeError("banning requires signing in with Google (scope youtube.force-ssl)", ErrNotPermitted)
	}

	var body liveChatBanRequest
	body.Snippet.LiveChatID = liveChatID
	body.Snippet.BannedUserDetails.ChannelID = channelID
	permanent := request.Duration <= 0
	if permanent {
		body.Snippet.Type = "permanent"
	} else {
		body.Snippet.Type = "temporary"
		seconds := int64(request.Duration / time.Second)
		if seconds < 1 {
			// A sub-second timeout is a rounding artifact, not an intent.
			seconds = 1
		}
		body.Snippet.BanDurationSeconds = strconv.FormatInt(seconds, 10)
	}

	var response struct {
		ID string `json:"id"`
	}
	query := map[string]string{"part": "snippet"}
	if err := c.doJSON(ctx, "POST", EndpointBansInsert, bansPath, query, body, &response); err != nil {
		return BanResult{}, err
	}
	return BanResult{BanID: response.ID, Permanent: permanent, At: c.now()}, nil
}

// Unban lifts a ban through liveChatBans.delete.
func (c *Client) Unban(ctx context.Context, banID string) error {
	identifier := strings.TrimSpace(banID)
	if identifier == "" {
		return newSafeError(EndpointBansDelete+": no ban id", ErrMessageRejected)
	}
	if !c.hasToken() {
		return newSafeError("lifting a ban requires signing in with Google (scope youtube.force-ssl)", ErrNotPermitted)
	}
	query := map[string]string{"id": identifier}
	return c.doJSON(ctx, "DELETE", EndpointBansDelete, bansPath, query, nil, nil)
}

package youtube

import (
	"context"
	"fmt"
	"strings"
)

// SearchWarning is the exact wording shown before spending a search.
//
// The old "burns 100 units of chat budget" framing is wrong: since the
// 2026-06-01 granular quota migration, search.list costs 1 unit from its own
// 100-calls-per-day bucket rather than 100 units from the main pool. It is
// still a scarce, separate, non-replenishing daily allowance, so it stays
// opt-in - but the warning must say what is actually being spent.
const SearchWarning = "searching for a live broadcast uses 1 of your 100 daily searches"

// ErrNoActiveBroadcast reports a channel that resolved cleanly but is not live.
// It is a normal answer, not a failure: the user asked about a channel that
// simply is not streaming, and the CLI offers the opt-in search from here.
var ErrNoActiveBroadcast = fmt.Errorf("no active broadcast: %w", ErrChatNotFound)

// ResolveTarget fills in a ChatTarget's liveChatId, video ID, and title,
// spending the cheapest ladder that can answer. An explicit live chat ID
// resolves for free.
//
// The ladder is ordered by cost because quota is the defining constraint:
// an explicit live chat ID costs nothing, videos.list and channels.list cost
// one unit each, and search.list is never reached from here - it is opt-in and
// the caller must ask for it explicitly after showing SearchWarning.
func (c *Client) ResolveTarget(ctx context.Context, target ChatTarget) (ChatTarget, error) {
	if target.Resolved() {
		return target, nil
	}

	switch {
	case strings.TrimSpace(target.VideoID) != "":
		resolved, err := c.ResolveVideo(ctx, target.VideoID)
		if err != nil {
			return target, err
		}
		return mergeTarget(target, resolved), nil

	case strings.TrimSpace(target.Handle) != "" || strings.TrimSpace(target.ChannelID) != "":
		identifier := firstNonEmpty(target.Handle, target.ChannelID)
		resolved, err := c.ResolveHandle(ctx, identifier)
		if err != nil {
			return target, err
		}
		merged := mergeTarget(target, resolved)
		if merged.Resolved() {
			return merged, nil
		}
		// channels.list answered but the channel is not live. Finding the
		// broadcast from here needs search.list, which spends the separate
		// daily search allowance, so it is the caller's decision.
		return merged, newSafeError("no active broadcast for this channel; "+SearchWarning, ErrNoActiveBroadcast)

	case strings.TrimSpace(target.Raw) != "":
		parsed, err := ParseChatTarget(target.Raw)
		if err != nil {
			return target, newSafeError(err.Error(), ErrChatNotFound)
		}
		if parsed.Kind == TargetUnknown {
			return target, newSafeError("unrecognized chat target", ErrChatNotFound)
		}
		return c.ResolveTarget(ctx, mergeTarget(target, parsed))

	default:
		return target, newSafeError("no chat target given", ErrChatNotFound)
	}
}

// ResolveVideo returns the active live chat ID for a video, at the cost of one
// videos.list call.
func (c *Client) ResolveVideo(ctx context.Context, videoID string) (ChatTarget, error) {
	info, err := c.Broadcast(ctx, videoID)
	if err != nil {
		return ChatTarget{Raw: videoID, Kind: TargetVideoID, VideoID: videoID}, err
	}
	target := ChatTarget{
		Raw:        videoID,
		Kind:       TargetVideoID,
		VideoID:    info.VideoID,
		ChannelID:  info.ChannelID,
		LiveChatID: info.LiveChatID,
		Title:      info.Title,
	}
	if !target.Resolved() {
		// A video with no activeLiveChatId is either not a broadcast, or one
		// that has already ended. Both leave the title on screen.
		return target, newSafeError("this video has no active live chat", ErrChatNotFound)
	}
	return target, nil
}

// ResolveHandle returns the channel behind an @handle or a channel ID, at the
// cost of one channels.list call.
//
// It also picks up the channel's current broadcast when channels.list reports
// one, which is why this can return a fully resolved target without ever
// touching the expensive search path.
func (c *Client) ResolveHandle(ctx context.Context, handleOrChannelID string) (ChatTarget, error) {
	identifier := strings.TrimSpace(handleOrChannelID)
	if identifier == "" {
		return ChatTarget{}, newSafeError("no channel given", ErrChatNotFound)
	}

	query := map[string]string{"part": "snippet,contentDetails"}
	kind := TargetHandle
	switch {
	case strings.HasPrefix(identifier, "@"):
		query["forHandle"] = identifier
	case channelIDPattern.MatchString(identifier):
		query["id"] = identifier
		kind = TargetChannelID
	default:
		query["forHandle"] = "@" + identifier
	}

	var response channelListResponse
	if err := c.doJSON(ctx, "GET", EndpointChannelsList, "channels", query, nil, &response); err != nil {
		return ChatTarget{Raw: identifier, Kind: kind}, err
	}
	if len(response.Items) == 0 {
		return ChatTarget{Raw: identifier, Kind: kind}, newSafeError("no such channel", ErrChatNotFound)
	}

	item := response.Items[0]
	target := ChatTarget{
		Raw:       identifier,
		Kind:      kind,
		ChannelID: item.ID,
		Handle:    item.Snippet.CustomURL,
		Title:     item.Snippet.Title,
	}
	if kind == TargetHandle {
		target.Handle = firstNonEmpty(item.Snippet.CustomURL, identifier)
	}
	return target, nil
}

// SearchLiveVideo finds a channel's current live broadcast through search.list.
//
// It is opt-in and must never sit on a hot path: the caller is responsible for
// showing SearchWarning and getting explicit consent first, and the spend is
// metered against the separate search bucket rather than the main ledger.
func (c *Client) SearchLiveVideo(ctx context.Context, channelID string) (ChatTarget, error) {
	identifier := strings.TrimSpace(channelID)
	if identifier == "" {
		return ChatTarget{}, newSafeError("no channel given", ErrChatNotFound)
	}

	query := map[string]string{
		"part":       "snippet",
		"channelId":  identifier,
		"eventType":  "live",
		"type":       "video",
		"maxResults": "1",
	}
	var response searchListResponse
	if err := c.doJSON(ctx, "GET", EndpointSearchList, "search", query, nil, &response); err != nil {
		return ChatTarget{Raw: identifier, Kind: TargetChannelID, ChannelID: identifier}, err
	}
	for _, item := range response.Items {
		if videoID := strings.TrimSpace(item.ID.VideoID); videoID != "" {
			return ChatTarget{
				Raw:       identifier,
				Kind:      TargetVideoID,
				VideoID:   videoID,
				ChannelID: identifier,
				Title:     item.Snippet.Title,
			}, nil
		}
	}
	return ChatTarget{Raw: identifier, Kind: TargetChannelID, ChannelID: identifier},
		newSafeError("no active broadcast for this channel", ErrNoActiveBroadcast)
}

// mergeTarget layers newly resolved fields over what the user originally typed,
// so Raw survives every step and a partially resolved target is never reset.
func mergeTarget(base, resolved ChatTarget) ChatTarget {
	merged := base
	if resolved.Kind != TargetUnknown {
		merged.Kind = resolved.Kind
	}
	merged.VideoID = firstNonEmpty(resolved.VideoID, base.VideoID)
	merged.ChannelID = firstNonEmpty(resolved.ChannelID, base.ChannelID)
	merged.Handle = firstNonEmpty(resolved.Handle, base.Handle)
	merged.LiveChatID = firstNonEmpty(resolved.LiveChatID, base.LiveChatID)
	merged.Title = firstNonEmpty(resolved.Title, base.Title)
	merged.Raw = firstNonEmpty(base.Raw, resolved.Raw)
	return merged
}

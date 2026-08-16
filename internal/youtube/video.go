package youtube

import (
	"context"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/quota"
)

// videoListResponse is the videos.list envelope.
type videoListResponse struct {
	Items []struct {
		ID      string `json:"id"`
		Snippet struct {
			ChannelID            string   `json:"channelId"`
			Title                string   `json:"title"`
			Description          string   `json:"description"`
			CategoryID           string   `json:"categoryId"`
			LiveBroadcastContent string   `json:"liveBroadcastContent"`
			Tags                 []string `json:"tags"`
		} `json:"snippet"`
		Status struct {
			PrivacyStatus string `json:"privacyStatus"`
		} `json:"status"`
		LiveStreamingDetails struct {
			ActualStartTime    string `json:"actualStartTime"`
			ActualEndTime      string `json:"actualEndTime"`
			ScheduledStartTime string `json:"scheduledStartTime"`
			ScheduledEndTime   string `json:"scheduledEndTime"`
			// ConcurrentViewers is a uint64 sent as a JSON string, and is
			// absent both when the owner hides it and after the stream ends.
			ConcurrentViewers string `json:"concurrentViewers"`
			ActiveLiveChatID  string `json:"activeLiveChatId"`
		} `json:"liveStreamingDetails"`
	} `json:"items"`
}

// channelListResponse is the channels.list envelope.
type channelListResponse struct {
	Items []struct {
		ID      string `json:"id"`
		Snippet struct {
			Title      string `json:"title"`
			CustomURL  string `json:"customUrl"`
			Thumbnails struct {
				Default struct {
					URL string `json:"url"`
				} `json:"default"`
			} `json:"thumbnails"`
		} `json:"snippet"`
		Statistics struct {
			SubscriberCount       string `json:"subscriberCount"`
			HiddenSubscriberCount bool   `json:"hiddenSubscriberCount"`
		} `json:"statistics"`
	} `json:"items"`
}

// subscriptionListResponse is the subscriptions.list envelope.
type subscriptionListResponse struct {
	NextPageToken string `json:"nextPageToken"`
	Items         []struct {
		Snippet struct {
			Title      string `json:"title"`
			ResourceID struct {
				ChannelID string `json:"channelId"`
			} `json:"resourceId"`
		} `json:"snippet"`
	} `json:"items"`
}

// searchListResponse is the search.list envelope.
type searchListResponse struct {
	Items []struct {
		ID struct {
			VideoID string `json:"videoId"`
		} `json:"id"`
		Snippet struct {
			Title string `json:"title"`
		} `json:"snippet"`
	} `json:"items"`
}

// videoCategoryListResponse is the videoCategories.list envelope.
type videoCategoryListResponse struct {
	Items []struct {
		ID      string `json:"id"`
		Snippet struct {
			Title string `json:"title"`
		} `json:"snippet"`
	} `json:"items"`
}

// Broadcast returns videos.list metadata for a video: the active live chat ID,
// the title, the live state, and the concurrent viewer count.
//
// It costs one unit, works with an API key alone, and is cheap enough to repeat
// on the metrics tick, which is how the viewer count in the status bar stays
// current without touching the chat budget in any meaningful way.
func (c *Client) Broadcast(ctx context.Context, videoID string) (BroadcastInfo, error) {
	identifier := strings.TrimSpace(videoID)
	if identifier == "" {
		return BroadcastInfo{}, newSafeError(quota.EndpointVideosList+": no video id", ErrChatNotFound)
	}

	query := map[string]string{
		"part": "snippet,liveStreamingDetails,status",
		"id":   identifier,
	}
	var response videoListResponse
	if err := c.doJSON(ctx, "GET", quota.EndpointVideosList, "videos", query, nil, &response); err != nil {
		return BroadcastInfo{VideoID: identifier}, err
	}
	if len(response.Items) == 0 {
		return BroadcastInfo{VideoID: identifier}, newSafeError("no such video", ErrChatNotFound)
	}

	item := response.Items[0]
	info := BroadcastInfo{
		VideoID:            firstNonEmpty(item.ID, identifier),
		ChannelID:          item.Snippet.ChannelID,
		Title:              item.Snippet.Title,
		Description:        item.Snippet.Description,
		LiveChatID:         item.LiveStreamingDetails.ActiveLiveChatID,
		Live:               strings.EqualFold(strings.TrimSpace(item.Snippet.LiveBroadcastContent), "live"),
		ActualStartTime:    parseAPITime(item.LiveStreamingDetails.ActualStartTime, time.Time{}),
		ActualEndTime:      parseAPITime(item.LiveStreamingDetails.ActualEndTime, time.Time{}),
		ScheduledStartTime: parseAPITime(item.LiveStreamingDetails.ScheduledStartTime, time.Time{}),
	}
	if viewers, ok := parseMicros(item.LiveStreamingDetails.ConcurrentViewers); ok {
		info.ConcurrentViewers = int(viewers)
		info.ViewersKnown = true
	}
	return info, nil
}

// Identity resolves the authenticated user's own channel through
// channels.list?mine=true.
//
// yc cannot learn its own badges any other way before a poll returns, so the
// local echo of a sent message is rendered from this.
func (c *Client) Identity(ctx context.Context) (Identity, error) {
	if !c.hasToken() {
		// An API key identifies a project, not a person. Asking anyway would
		// spend a unit to be told so.
		return Identity{}, newSafeError("sign in with Google to resolve your own channel", ErrNotPermitted)
	}

	query := map[string]string{"part": "snippet,statistics", "mine": "true"}
	var response channelListResponse
	if err := c.doJSON(ctx, "GET", quota.EndpointChannelsList, "channels", query, nil, &response); err != nil {
		return Identity{}, err
	}
	if len(response.Items) == 0 {
		return Identity{}, newSafeError("this Google account has no YouTube channel", ErrChatNotFound)
	}

	item := response.Items[0]
	identity := Identity{
		ChannelID:   item.ID,
		DisplayName: item.Snippet.Title,
		Handle:      item.Snippet.CustomURL,
		AvatarURL:   item.Snippet.Thumbnails.Default.URL,
	}
	if !item.Statistics.HiddenSubscriberCount {
		if count, ok := parseMicros(item.Statistics.SubscriberCount); ok {
			identity.SubscriberCount = int(count)
			identity.SubscriberCountKnown = true
		}
	}
	return identity, nil
}

// Subscriptions lists the authenticated user's subscriptions for the chat
// picker's autocomplete.
//
// It is fetched once per session and a failure is inline picker text rather
// than an error: autocomplete is a convenience, and the typed value always
// remains a valid choice.
func (c *Client) Subscriptions(ctx context.Context) ([]Subscription, error) {
	if !c.hasToken() {
		return nil, newSafeError("sign in with Google to list your subscriptions", ErrNotPermitted)
	}

	query := map[string]string{
		"part":       "snippet",
		"mine":       "true",
		"maxResults": "50",
		"order":      "alphabetical",
	}
	var response subscriptionListResponse
	if err := c.doJSON(ctx, "GET", quota.EndpointSubscriptions, "subscriptions", query, nil, &response); err != nil {
		return nil, err
	}

	subscriptions := make([]Subscription, 0, len(response.Items))
	for _, item := range response.Items {
		channelID := strings.TrimSpace(item.Snippet.ResourceID.ChannelID)
		if channelID == "" {
			continue
		}
		subscriptions = append(subscriptions, Subscription{
			ChannelID: channelID,
			Title:     item.Snippet.Title,
		})
	}
	return subscriptions, nil
}

// StreamInfo returns the editable broadcast metadata behind the Stream Info tab.
func (c *Client) StreamInfo(ctx context.Context, videoID string) (StreamInfo, error) {
	identifier := strings.TrimSpace(videoID)
	if identifier == "" {
		return StreamInfo{}, newSafeError(quota.EndpointVideosList+": no video id", ErrChatNotFound)
	}

	query := map[string]string{"part": "snippet,status", "id": identifier}
	var response videoListResponse
	if err := c.doJSON(ctx, "GET", quota.EndpointVideosList, "videos", query, nil, &response); err != nil {
		return StreamInfo{VideoID: identifier}, err
	}
	if len(response.Items) == 0 {
		return StreamInfo{VideoID: identifier}, newSafeError("no such video", ErrChatNotFound)
	}

	item := response.Items[0]
	return StreamInfo{
		VideoID:     firstNonEmpty(item.ID, identifier),
		Title:       item.Snippet.Title,
		Description: item.Snippet.Description,
		CategoryID:  item.Snippet.CategoryID,
		Privacy:     item.Status.PrivacyStatus,
		Tags:        item.Snippet.Tags,
	}, nil
}

// videoUpdateRequest is the videos.update body. snippet.title and
// snippet.categoryId are both required by the API even when only one of them
// changed, so the caller must send back a complete snippet.
type videoUpdateRequest struct {
	ID      string `json:"id"`
	Snippet struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		CategoryID  string   `json:"categoryId"`
		Tags        []string `json:"tags,omitempty"`
	} `json:"snippet"`
	Status *struct {
		PrivacyStatus string `json:"privacyStatus"`
	} `json:"status,omitempty"`
}

// UpdateStreamInfo writes broadcast metadata and returns the stored result.
func (c *Client) UpdateStreamInfo(ctx context.Context, info StreamInfo) (StreamInfo, error) {
	identifier := strings.TrimSpace(info.VideoID)
	if identifier == "" {
		return info, newSafeError(quota.EndpointVideosUpdate+": no video id", ErrMessageRejected)
	}
	if !c.hasToken() {
		return info, newSafeError("editing broadcast details requires signing in with Google", ErrNotPermitted)
	}

	body := videoUpdateRequest{ID: identifier}
	body.Snippet.Title = info.Title
	body.Snippet.Description = info.Description
	body.Snippet.CategoryID = info.CategoryID
	body.Snippet.Tags = info.Tags

	parts := "snippet"
	if privacy := strings.TrimSpace(info.Privacy); privacy != "" {
		body.Status = &struct {
			PrivacyStatus string `json:"privacyStatus"`
		}{PrivacyStatus: privacy}
		parts = "snippet,status"
	}

	var response struct {
		ID      string `json:"id"`
		Snippet struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			CategoryID  string   `json:"categoryId"`
			Tags        []string `json:"tags"`
		} `json:"snippet"`
		Status struct {
			PrivacyStatus string `json:"privacyStatus"`
		} `json:"status"`
	}
	query := map[string]string{"part": parts}
	if err := c.doJSON(ctx, "PUT", quota.EndpointVideosUpdate, "videos", query, body, &response); err != nil {
		return info, err
	}

	return StreamInfo{
		VideoID:       firstNonEmpty(response.ID, identifier),
		Title:         response.Snippet.Title,
		Description:   response.Snippet.Description,
		CategoryID:    response.Snippet.CategoryID,
		CategoryTitle: info.CategoryTitle,
		Privacy:       firstNonEmpty(response.Status.PrivacyStatus, info.Privacy),
		Tags:          response.Snippet.Tags,
	}, nil
}

// Categories lists video categories for the Stream Info category picker.
func (c *Client) Categories(ctx context.Context) ([]Category, error) {
	region := "US"
	query := map[string]string{"part": "snippet", "regionCode": region}
	if hl := strings.TrimSpace(c.cfg.HL); hl != "" {
		query["hl"] = hl
	}

	var response videoCategoryListResponse
	if err := c.doJSON(ctx, "GET", quota.EndpointCategoriesList, "videoCategories", query, nil, &response); err != nil {
		return nil, err
	}

	categories := make([]Category, 0, len(response.Items))
	for _, item := range response.Items {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		categories = append(categories, Category{ID: item.ID, Title: item.Snippet.Title})
	}
	return categories, nil
}

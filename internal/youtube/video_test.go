package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestBroadcastDecodesTheStringViewerCount(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":"dQw4w9WgXcQ","snippet":{"channelId":"UCuAXFkgsw1L7xaCfnd5JJOw","title":"Live coding","description":"desc","liveBroadcastContent":"live"},"liveStreamingDetails":{"activeLiveChatId":"chat-1","concurrentViewers":"1234","actualStartTime":"2026-08-08T18:00:00Z"}}]}`)
	})

	info, err := client.Broadcast(context.Background(), "dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("Broadcast error = %v", err)
	}
	if !info.Live {
		t.Fatal("Live = false, want live")
	}
	if !info.ViewersKnown || info.ConcurrentViewers != 1234 {
		t.Fatalf("viewers = %d/%t, want 1234 decoded from the JSON string", info.ConcurrentViewers, info.ViewersKnown)
	}
	if !info.ActualStartTime.Equal(time.Date(2026, 8, 8, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("ActualStartTime = %v", info.ActualStartTime)
	}
}

func TestBroadcastHiddenViewerCountIsUnknownNotZero(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":"dQw4w9WgXcQ","snippet":{"title":"Live coding","liveBroadcastContent":"live"},"liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`)
	})

	info, err := client.Broadcast(context.Background(), "dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("Broadcast error = %v", err)
	}
	// An owner who hides the count has not told yc there are zero viewers.
	// Rendering "0 viewers" would be a fabrication.
	if info.ViewersKnown {
		t.Fatal("ViewersKnown = true with no concurrentViewers field")
	}
}

func TestBroadcastMissingVideoIsNotFound(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[]}`)
	})
	if _, err := client.Broadcast(context.Background(), "dQw4w9WgXcQ"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("error = %v, want ErrChatNotFound", err)
	}
}

func TestIdentityRequiresOAuthAndDecodesTheSubscriberCount(t *testing.T) {
	keyOnly, _ := newTestClient(t, keyCredentials(), func(http.ResponseWriter, *http.Request) {
		t.Fatal("channels.list?mine dispatched with only an API key; a key identifies a project, not a person")
	})
	if _, err := keyOnly.Identity(context.Background()); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("error = %v, want ErrNotPermitted", err)
	}

	var got url.Values
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		fmt.Fprint(w, `{"items":[{"id":"UCme00000000000000000001","snippet":{"title":"Me","customUrl":"@me","thumbnails":{"default":{"url":"https://yt3.example/me.jpg"}}},"statistics":{"subscriberCount":"4200","hiddenSubscriberCount":false}}]}`)
	})

	identity, err := client.Identity(context.Background())
	if err != nil {
		t.Fatalf("Identity error = %v", err)
	}
	if got.Get("mine") != "true" {
		t.Fatalf("mine = %q, want true", got.Get("mine"))
	}
	if identity.ChannelID != "UCme00000000000000000001" || identity.Handle != "@me" {
		t.Fatalf("identity = %#v", identity)
	}
	if !identity.SubscriberCountKnown || identity.SubscriberCount != 4200 {
		t.Fatalf("subscribers = %d/%t, want 4200", identity.SubscriberCount, identity.SubscriberCountKnown)
	}
}

func TestIdentityHiddenSubscriberCountStaysUnknown(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":"UCme00000000000000000001","snippet":{"title":"Me"},"statistics":{"subscriberCount":"0","hiddenSubscriberCount":true}}]}`)
	})

	identity, err := client.Identity(context.Background())
	if err != nil {
		t.Fatalf("Identity error = %v", err)
	}
	if identity.SubscriberCountKnown {
		t.Fatal("SubscriberCountKnown = true for a hidden count")
	}
}

func TestSubscriptionsFeedTheChatPicker(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"snippet":{"title":"First","resourceId":{"channelId":"UCa00000000000000000001"}}},{"snippet":{"title":"Broken","resourceId":{}}},{"snippet":{"title":"Second","resourceId":{"channelId":"UCb00000000000000000002"}}}]}`)
	})

	subscriptions, err := client.Subscriptions(context.Background())
	if err != nil {
		t.Fatalf("Subscriptions error = %v", err)
	}
	if len(subscriptions) != 2 {
		t.Fatalf("subscriptions = %#v, want the two entries that carry a channel id", subscriptions)
	}
	if subscriptions[0].Title != "First" || subscriptions[1].ChannelID != "UCb00000000000000000002" {
		t.Fatalf("subscriptions = %#v", subscriptions)
	}
}

func TestStreamInfoRoundTrip(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":"dQw4w9WgXcQ","snippet":{"title":"Live coding","description":"desc","categoryId":"20","tags":["go","tui"]},"status":{"privacyStatus":"public"}}]}`)
	})

	info, err := client.StreamInfo(context.Background(), "dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("StreamInfo error = %v", err)
	}
	if info.Title != "Live coding" || info.CategoryID != "20" || info.Privacy != "public" {
		t.Fatalf("info = %#v", info)
	}
	if len(info.Tags) != 2 {
		t.Fatalf("Tags = %#v, want two", info.Tags)
	}
}

func TestUpdateStreamInfoSendsACompleteSnippet(t *testing.T) {
	var body videoUpdateRequest
	var part string
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		part = r.URL.Query().Get("part")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode update body: %v", err)
		}
		fmt.Fprint(w, `{"id":"dQw4w9WgXcQ","snippet":{"title":"New title","description":"d","categoryId":"20","tags":["go"]},"status":{"privacyStatus":"unlisted"}}`)
	})

	got, err := client.UpdateStreamInfo(context.Background(), StreamInfo{
		VideoID:     "dQw4w9WgXcQ",
		Title:       "New title",
		Description: "d",
		CategoryID:  "20",
		Privacy:     "unlisted",
		Tags:        []string{"go"},
	})
	if err != nil {
		t.Fatalf("UpdateStreamInfo error = %v", err)
	}
	if part != "snippet,status" {
		t.Fatalf("part = %q, want snippet,status", part)
	}
	// videos.update requires title and categoryId even when only one field
	// changed, so a partial snippet silently blanks the rest.
	if body.Snippet.Title == "" || body.Snippet.CategoryID == "" {
		t.Fatalf("body = %#v, want a complete snippet", body)
	}
	if got.Title != "New title" || got.Privacy != "unlisted" {
		t.Fatalf("result = %#v", got)
	}
}

func TestUpdateStreamInfoWithoutOAuthIsRefusedLocally(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(http.ResponseWriter, *http.Request) {
		t.Fatal("videos.update dispatched in key-only mode")
	})
	_, err := client.UpdateStreamInfo(context.Background(), StreamInfo{VideoID: "dQw4w9WgXcQ", Title: "x"})
	if !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("error = %v, want ErrNotPermitted", err)
	}
}

func TestCategoriesFeedThePicker(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":"20","snippet":{"title":"Gaming"}},{"id":"","snippet":{"title":"Broken"}},{"id":"28","snippet":{"title":"Science & Technology"}}]}`)
	})

	categories, err := client.Categories(context.Background())
	if err != nil {
		t.Fatalf("Categories error = %v", err)
	}
	if len(categories) != 2 || categories[0].ID != "20" || categories[1].Title != "Science & Technology" {
		t.Fatalf("categories = %#v", categories)
	}
}

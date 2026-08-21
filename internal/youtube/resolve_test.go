package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/worxbend/yc/internal/quota"
)

func TestParseChatTarget(t *testing.T) {
	tests := []struct {
		raw        string
		wantKind   TargetKind
		wantVideo  string
		wantChan   string
		wantHandle string
		wantChat   string
		wantErr    bool
	}{
		{raw: "dQw4w9WgXcQ", wantKind: TargetVideoID, wantVideo: "dQw4w9WgXcQ"},
		{raw: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", wantKind: TargetVideoID, wantVideo: "dQw4w9WgXcQ"},
		{raw: "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s", wantKind: TargetVideoID, wantVideo: "dQw4w9WgXcQ"},
		{raw: "https://youtu.be/dQw4w9WgXcQ", wantKind: TargetVideoID, wantVideo: "dQw4w9WgXcQ"},
		{raw: "youtu.be/dQw4w9WgXcQ", wantKind: TargetVideoID, wantVideo: "dQw4w9WgXcQ"},
		{raw: "https://www.youtube.com/live/dQw4w9WgXcQ", wantKind: TargetVideoID, wantVideo: "dQw4w9WgXcQ"},
		{raw: "https://www.youtube.com/shorts/dQw4w9WgXcQ", wantKind: TargetVideoID, wantVideo: "dQw4w9WgXcQ"},
		{raw: "https://m.youtube.com/watch?v=dQw4w9WgXcQ", wantKind: TargetVideoID, wantVideo: "dQw4w9WgXcQ"},
		{raw: "@handle", wantKind: TargetHandle, wantHandle: "@handle"},
		{raw: "https://www.youtube.com/@handle", wantKind: TargetHandle, wantHandle: "@handle"},
		{raw: "https://www.youtube.com/@handle/live", wantKind: TargetHandle, wantHandle: "@handle"},
		{raw: "https://www.youtube.com/c/LegacyName", wantKind: TargetHandle, wantHandle: "@LegacyName"},
		{raw: "https://www.youtube.com/user/LegacyUser", wantKind: TargetHandle, wantHandle: "@LegacyUser"},
		{raw: "UCuAXFkgsw1L7xaCfnd5JJOw", wantKind: TargetChannelID, wantChan: "UCuAXFkgsw1L7xaCfnd5JJOw"},
		{raw: "https://www.youtube.com/channel/UCuAXFkgsw1L7xaCfnd5JJOw", wantKind: TargetChannelID, wantChan: "UCuAXFkgsw1L7xaCfnd5JJOw"},
		{raw: "livechat:Cg0KC2RRdzR3OVdnWGNRKicKGFVDdUFYRmtnc3cxTDd4YUNmbmQ1SkpPdw", wantKind: TargetLiveChatID, wantChat: "Cg0KC2RRdzR3OVdnWGNRKicKGFVDdUFYRmtnc3cxTDd4YUNmbmQ1SkpPdw"},
		{raw: "Cg0KC2RRdzR3OVdnWGNRKicKGFVDdUFYRmtnc3cxTDd4YUNmbmQ1SkpPdw", wantKind: TargetLiveChatID, wantChat: "Cg0KC2RRdzR3OVdnWGNRKicKGFVDdUFYRmtnc3cxTDd4YUNmbmQ1SkpPdw"},
		// A bare word that cannot be a video ID is treated as a handle,
		// because channels.list?forHandle costs 1 unit and is the only
		// lookup that can still answer. An 11-character token is ambiguous
		// and resolves as a video ID, which is what people actually paste.
		{raw: "somechannelname", wantKind: TargetHandle, wantHandle: "@somechannelname"},
		{raw: "somechannel", wantKind: TargetVideoID, wantVideo: "somechannel"},
		{raw: "", wantErr: true},
		{raw: "https://twitch.tv/someone", wantErr: true},
		{raw: "!!", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			target, err := ParseChatTarget(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseChatTarget(%q) = %#v, want an error", test.raw, target)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseChatTarget(%q) error = %v", test.raw, err)
			}
			if target.Kind != test.wantKind {
				t.Fatalf("Kind = %q, want %q", target.Kind, test.wantKind)
			}
			if target.VideoID != test.wantVideo {
				t.Fatalf("VideoID = %q, want %q", target.VideoID, test.wantVideo)
			}
			if target.ChannelID != test.wantChan {
				t.Fatalf("ChannelID = %q, want %q", target.ChannelID, test.wantChan)
			}
			if target.Handle != test.wantHandle {
				t.Fatalf("Handle = %q, want %q", target.Handle, test.wantHandle)
			}
			if target.LiveChatID != test.wantChat {
				t.Fatalf("LiveChatID = %q, want %q", target.LiveChatID, test.wantChat)
			}
			if target.Raw != test.raw {
				t.Fatalf("Raw = %q, want the input preserved", target.Raw)
			}
		})
	}
}

func TestParseChatTargetIsPureAndNeedsNoNetwork(t *testing.T) {
	// Resolution costs quota; classification must not. This is what lets the
	// CLI validate a target list before spending a unit on any of them.
	target, err := ParseChatTarget("https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("ParseChatTarget error = %v", err)
	}
	if target.Resolved() {
		t.Fatal("Resolved() = true; parsing must never invent a liveChatId")
	}
	if target.Key() != "dqw4w9wgxcq" {
		t.Fatalf("Key() = %q, want a stable lowercased key", target.Key())
	}
	if target.Label() != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatalf("Label() = %q, want the raw input until a title is known", target.Label())
	}
}

func TestResolveVideoReturnsTheActiveLiveChatID(t *testing.T) {
	var got url.Values
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		fmt.Fprint(w, `{"items":[{"id":"dQw4w9WgXcQ","snippet":{"channelId":"UCuAXFkgsw1L7xaCfnd5JJOw","title":"Live coding","liveBroadcastContent":"live"},"liveStreamingDetails":{"activeLiveChatId":"chat-1","concurrentViewers":"1234","actualStartTime":"2026-08-08T18:00:00Z"}}]}`)
	})

	target, err := client.ResolveVideo(context.Background(), "dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("ResolveVideo error = %v", err)
	}
	if !target.Resolved() || target.LiveChatID != "chat-1" {
		t.Fatalf("target = %#v, want a resolved live chat id", target)
	}
	if target.Title != "Live coding" {
		t.Fatalf("Title = %q, want the broadcast title", target.Title)
	}
	if got.Get("id") != "dQw4w9WgXcQ" {
		t.Fatalf("id = %q, want the video id", got.Get("id"))
	}
}

func TestResolveVideoWithoutActiveChatIsNotFound(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":"dQw4w9WgXcQ","snippet":{"title":"An old VOD"}}]}`)
	})

	target, err := client.ResolveVideo(context.Background(), "dQw4w9WgXcQ")
	if !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("error = %v, want ErrChatNotFound", err)
	}
	// The title survives the failure so the UI can name what it could not open.
	if target.Title != "An old VOD" {
		t.Fatalf("Title = %q, want the title retained on failure", target.Title)
	}
}

func TestResolveHandleUsesForHandleAndNeverSearch(t *testing.T) {
	var got url.Values
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/channels" {
			t.Fatalf("path = %q, want /channels: search is opt-in and must never be automatic", r.URL.Path)
		}
		got = r.URL.Query()
		fmt.Fprint(w, `{"items":[{"id":"UCuAXFkgsw1L7xaCfnd5JJOw","snippet":{"title":"A Channel","customUrl":"@achannel"}}]}`)
	})

	target, err := client.ResolveHandle(context.Background(), "@achannel")
	if err != nil {
		t.Fatalf("ResolveHandle error = %v", err)
	}
	if got.Get("forHandle") != "@achannel" {
		t.Fatalf("forHandle = %q, want @achannel", got.Get("forHandle"))
	}
	if target.ChannelID != "UCuAXFkgsw1L7xaCfnd5JJOw" || target.Title != "A Channel" {
		t.Fatalf("target = %#v, want the resolved channel", target)
	}
}

func TestResolveHandleAcceptsAChannelID(t *testing.T) {
	var got url.Values
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		fmt.Fprint(w, `{"items":[{"id":"UCuAXFkgsw1L7xaCfnd5JJOw","snippet":{"title":"A Channel"}}]}`)
	})

	if _, err := client.ResolveHandle(context.Background(), "UCuAXFkgsw1L7xaCfnd5JJOw"); err != nil {
		t.Fatalf("ResolveHandle error = %v", err)
	}
	if got.Get("id") != "UCuAXFkgsw1L7xaCfnd5JJOw" || got.Has("forHandle") {
		t.Fatalf("query = %v, want an id lookup", got)
	}
}

func TestResolveTargetOnAHandleStopsBeforeSpendingASearch(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			t.Fatal("search.list dispatched automatically; it must be opt-in")
		}
		fmt.Fprint(w, `{"items":[{"id":"UCuAXFkgsw1L7xaCfnd5JJOw","snippet":{"title":"A Channel","customUrl":"@achannel"}}]}`)
	})

	target, err := client.ResolveTarget(context.Background(), ChatTarget{Raw: "@achannel", Kind: TargetHandle, Handle: "@achannel"})
	if !errors.Is(err, ErrNoActiveBroadcast) {
		t.Fatalf("error = %v, want ErrNoActiveBroadcast", err)
	}
	if !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("error = %v, want it to also classify as ErrChatNotFound", err)
	}
	if target.ChannelID == "" {
		t.Fatal("ChannelID is empty; the partial resolution must survive so the search opt-in has something to search")
	}
}

func TestResolveTargetPassesAnExplicitLiveChatIDThroughForFree(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(http.ResponseWriter, *http.Request) {
		t.Fatal("request dispatched for an already-resolved target")
	})

	target := ChatTarget{Raw: "chat-1", Kind: TargetLiveChatID, LiveChatID: "chat-1"}
	got, err := client.ResolveTarget(context.Background(), target)
	if err != nil {
		t.Fatalf("ResolveTarget error = %v", err)
	}
	if got != target {
		t.Fatalf("target = %#v, want it unchanged", got)
	}
}

func TestResolveTargetClimbsTheLadderFromRawInput(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/videos" {
			t.Fatalf("path = %q, want /videos: a watch URL needs nothing more expensive", r.URL.Path)
		}
		fmt.Fprint(w, `{"items":[{"id":"dQw4w9WgXcQ","snippet":{"title":"Live coding"},"liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`)
	})

	got, err := client.ResolveTarget(context.Background(), ChatTarget{Raw: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"})
	if err != nil {
		t.Fatalf("ResolveTarget error = %v", err)
	}
	if got.LiveChatID != "chat-1" || got.VideoID != "dQw4w9WgXcQ" {
		t.Fatalf("target = %#v, want the resolved video and chat", got)
	}
	if got.Raw != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatalf("Raw = %q, want the user's input preserved through resolution", got.Raw)
	}
}

func TestSearchLiveVideoIsMeteredAgainstItsOwnBucket(t *testing.T) {
	var got url.Values
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		fmt.Fprint(w, `{"items":[{"id":{"videoId":"dQw4w9WgXcQ"},"snippet":{"title":"Live now"}}]}`)
	})

	target, err := client.SearchLiveVideo(context.Background(), "UCuAXFkgsw1L7xaCfnd5JJOw")
	if err != nil {
		t.Fatalf("SearchLiveVideo error = %v", err)
	}
	if target.VideoID != "dQw4w9WgXcQ" || target.Title != "Live now" {
		t.Fatalf("target = %#v, want the live video", target)
	}
	if got.Get("eventType") != "live" || got.Get("type") != "video" {
		t.Fatalf("query = %v, want a live video search", got)
	}
	if quota.DefaultCostTable().Cost(quota.EndpointSearchList) != 1 {
		t.Fatal("search.list cost is not 1; since the 2026-06-01 granular migration it is 1 unit from a separate 100/day bucket")
	}
}

func TestSearchWarningNamesWhatIsActuallySpent(t *testing.T) {
	// The old "100 units of chat budget" framing is wrong and would push
	// users away from a call that is cheap in units and scarce in calls.
	if SearchWarning != "searching for a live broadcast uses 1 of your 100 daily searches" {
		t.Fatalf("SearchWarning = %q", SearchWarning)
	}
}

// TestUnclassifiedKindDoesNotClobberAResolvedKind pins the reason TargetUnknown
// is the empty string rather than "unknown".
//
// mergeTarget layers a resolved target over what the user typed, and it keeps
// the resolved Kind only when that Kind says something. While TargetUnknown was
// "unknown", TargetKind had two sentinels for "unclassified": the named one and
// the zero value a struct literal produces. mergeTarget tested for the named
// one, so a ChatTarget built without a Kind passed the guard and erased a Kind
// that had already been established.
func TestUnclassifiedKindDoesNotClobberAResolvedKind(t *testing.T) {
	base := ChatTarget{Raw: "dQw4w9WgXcQ", Kind: TargetVideoID, VideoID: "dQw4w9WgXcQ"}
	// A ChatTarget assembled field by field, carrying an ID but no Kind.
	resolved := ChatTarget{LiveChatID: "lc-1"}

	merged := mergeTarget(base, resolved)

	if merged.Kind != TargetVideoID {
		t.Errorf("Kind = %q, want %q: an unclassified target must not erase a resolved kind",
			merged.Kind, TargetVideoID)
	}
	if merged.LiveChatID != "lc-1" {
		t.Errorf("LiveChatID = %q, want %q", merged.LiveChatID, "lc-1")
	}
}

package youtube

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestListMessagesAlwaysRequestsTheDocumentedMaximum(t *testing.T) {
	var got url.Values
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		fmt.Fprint(w, `{"items":[],"pollingIntervalMillis":5000}`)
	})

	if _, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"}); err != nil {
		t.Fatalf("ListMessages error = %v", err)
	}

	// 200 is the documented minimum, not the maximum. Quota is charged per
	// call, so asking for 2000 costs the same and buys ten times the headroom.
	if got.Get("maxResults") != "2000" {
		t.Fatalf("maxResults = %q, want 2000", got.Get("maxResults"))
	}
	if got.Get("part") != "snippet,authorDetails" {
		t.Fatalf("part = %q, want snippet,authorDetails", got.Get("part"))
	}
	if got.Get("liveChatId") != "chat-1" {
		t.Fatalf("liveChatId = %q, want chat-1", got.Get("liveChatId"))
	}
	if got.Has("pageToken") {
		t.Fatal("pageToken present on the priming call; the first page must be token-less")
	}
}

func TestListMessagesCarriesThePageToken(t *testing.T) {
	var got string
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("pageToken")
		fmt.Fprint(w, `{"items":[],"nextPageToken":"page-2"}`)
	})

	result, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1", PageToken: "page-1"})
	if err != nil {
		t.Fatalf("ListMessages error = %v", err)
	}
	if got != "page-1" {
		t.Fatalf("pageToken = %q, want page-1", got)
	}
	if result.NextPageToken != "page-2" {
		t.Fatalf("NextPageToken = %q, want page-2", result.NextPageToken)
	}
}

func TestListMessagesReportsCadenceAndOfflineWindow(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[],"pollingIntervalMillis":2500,"offlineAt":"2026-08-08T21:30:00Z","pageInfo":{"totalResults":7,"resultsPerPage":2000}}`)
	})

	result, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
	if err != nil {
		t.Fatalf("ListMessages error = %v", err)
	}
	if result.PollingInterval != 2500*time.Millisecond {
		t.Fatalf("PollingInterval = %v, want 2.5s", result.PollingInterval)
	}
	want := time.Date(2026, 8, 8, 21, 30, 0, 0, time.UTC)
	if !result.OfflineAt.Equal(want) {
		t.Fatalf("OfflineAt = %v, want %v", result.OfflineAt, want)
	}
	if result.TotalResults != 7 {
		t.Fatalf("TotalResults = %d, want 7", result.TotalResults)
	}
	if result.QuotaUnits <= 0 {
		t.Fatalf("QuotaUnits = %d, want the estimated list cost", result.QuotaUnits)
	}
}

func TestListMessagesAbsentPollingIntervalLeavesNoServerFloor(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[]}`)
	})

	result, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
	if err != nil {
		t.Fatalf("ListMessages error = %v", err)
	}
	// Zero means "the server named no floor", which the poller reads as
	// "use the local floor". Inventing one here would hide the distinction.
	if result.PollingInterval != 0 {
		t.Fatalf("PollingInterval = %v, want 0", result.PollingInterval)
	}
}

func TestListMessagesNormalizesEveryStream(t *testing.T) {
	body := listResponseFromFixtures(t,
		"textMessageEvent",
		"superChatEvent",
		"userBannedEventTemporary",
		"sponsorOnlyModeStartedEvent",
		"pollEvent",
		"chatEndedEvent",
	)
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	})

	result, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1", Historical: true})
	if err != nil {
		t.Fatalf("ListMessages error = %v", err)
	}

	// text + superchat + sponsor-only notice + poll notice
	if len(result.Messages) != 4 {
		t.Fatalf("messages = %d, want 4: %#v", len(result.Messages), result.Messages)
	}
	for _, msg := range result.Messages {
		if !msg.Historical {
			t.Fatalf("message %q is not Historical on the priming page", msg.ID)
		}
	}
	if len(result.Moderations) != 1 {
		t.Fatalf("moderations = %d, want 1", len(result.Moderations))
	}
	if len(result.RoomEvents) != 2 {
		t.Fatalf("room events = %d, want the sponsor-only change and the chat ending", len(result.RoomEvents))
	}
	if len(result.Polls) != 1 {
		t.Fatalf("polls = %d, want 1", len(result.Polls))
	}
}

func TestListMessagesActivePollItemIsNotAChatRow(t *testing.T) {
	poll := readFixtureBytes(t, "pollEvent")
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"items":[],"activePollItem":%s}`, poll)
	})

	result, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
	if err != nil {
		t.Fatalf("ListMessages error = %v", err)
	}
	if len(result.Polls) != 1 {
		t.Fatalf("polls = %d, want the out-of-band active poll", len(result.Polls))
	}
	if got := result.Polls[0]; got.Question != "which theme next?" || got.TotalVotes() != 225 {
		t.Fatalf("poll = %#v, want the question and a 225-vote total decoded from string tallies", got)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("messages = %#v, want none: activePollItem is not new history", result.Messages)
	}
}

func TestListMessagesRejectsAnEmptyChatIDWithoutSpendingQuota(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(http.ResponseWriter, *http.Request) {
		t.Fatal("request dispatched for an empty live chat id")
	})

	if _, err := client.ListMessages(context.Background(), ListRequest{}); err == nil {
		t.Fatal("error = nil, want a refusal before dispatch")
	}
}

func TestListMessagesSendsLocalizationAndProfileImageSize(t *testing.T) {
	var got url.Values
	server := func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		fmt.Fprint(w, `{"items":[]}`)
	}
	client, _ := newTestClient(t, oauthCredentials(), server)
	client.cfg.HL = "de"

	if _, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1", ProfileImageSize: 88}); err != nil {
		t.Fatalf("ListMessages error = %v", err)
	}
	if got.Get("hl") != "de" {
		t.Fatalf("hl = %q, want de: it localizes amountDisplayString", got.Get("hl"))
	}
	if got.Get("profileImageSize") != "88" {
		t.Fatalf("profileImageSize = %q, want 88", got.Get("profileImageSize"))
	}
}

// listResponseFromFixtures assembles a list envelope from golden event files.
func listResponseFromFixtures(t *testing.T, names ...string) string {
	t.Helper()
	items := make([]string, 0, len(names))
	for _, name := range names {
		items = append(items, string(readFixtureBytes(t, name)))
	}
	return `{"items":[` + strings.Join(items, ",") + `],"nextPageToken":"page-2","pollingIntervalMillis":5000}`
}

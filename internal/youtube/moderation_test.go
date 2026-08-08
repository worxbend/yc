package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestDeleteMessageUsesTheDocumentedDeleteCall(t *testing.T) {
	var method, path, id string
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		method, path, id = r.Method, r.URL.Path, r.URL.Query().Get("id")
		w.WriteHeader(http.StatusNoContent)
	})

	if err := client.DeleteMessage(context.Background(), "msg-1"); err != nil {
		t.Fatalf("DeleteMessage error = %v", err)
	}
	if method != http.MethodDelete || path != "/liveChat/messages" || id != "msg-1" {
		t.Fatalf("request = %s %s?id=%s", method, path, id)
	}
}

func TestDeleteMessageWithoutOAuthIsRefusedLocally(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(http.ResponseWriter, *http.Request) {
		t.Fatal("delete dispatched in key-only mode")
	})
	if err := client.DeleteMessage(context.Background(), "msg-1"); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("error = %v, want ErrNotPermitted", err)
	}
}

func TestDeleteMessageMapsModificationNotAllowed(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, googleError(403, "modificationNotAllowed", "PERMISSION_DENIED", "", "not your chat"))
	})
	if err := client.DeleteMessage(context.Background(), "msg-1"); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("error = %v, want ErrNotPermitted", err)
	}
}

func TestBanSendsTemporaryAndPermanentShapes(t *testing.T) {
	tests := []struct {
		name          string
		duration      time.Duration
		wantType      string
		wantSeconds   string
		wantPermanent bool
	}{
		{name: "permanent", duration: 0, wantType: "permanent", wantPermanent: true},
		{name: "timeout", duration: 5 * time.Minute, wantType: "temporary", wantSeconds: "300"},
		// A sub-second duration is a rounding artifact, not an intent to ban
		// for zero seconds - which the API would reject anyway.
		{name: "sub-second timeout", duration: 200 * time.Millisecond, wantType: "temporary", wantSeconds: "1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body liveChatBanRequest
			client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/liveChat/bans" {
					t.Fatalf("path = %q, want /liveChat/bans", r.URL.Path)
				}
				raw, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatalf("decode ban body: %v", err)
				}
				fmt.Fprint(w, `{"id":"ban-1"}`)
			})

			result, err := client.Ban(context.Background(), BanRequest{
				LiveChatID: "chat-1",
				ChannelID:  "UCspam0000000000000000001",
				Duration:   test.duration,
			})
			if err != nil {
				t.Fatalf("Ban error = %v", err)
			}
			if body.Snippet.Type != test.wantType {
				t.Fatalf("type = %q, want %q", body.Snippet.Type, test.wantType)
			}
			if body.Snippet.BanDurationSeconds != test.wantSeconds {
				t.Fatalf("banDurationSeconds = %q, want %q as a JSON string", body.Snippet.BanDurationSeconds, test.wantSeconds)
			}
			if body.Snippet.BannedUserDetails.ChannelID != "UCspam0000000000000000001" {
				t.Fatalf("bannedUserDetails.channelId = %q", body.Snippet.BannedUserDetails.ChannelID)
			}
			if result.BanID != "ban-1" || result.Permanent != test.wantPermanent {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestBanValidatesLocallyBeforeSpendingQuota(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(http.ResponseWriter, *http.Request) {
		t.Fatal("ban dispatched with an incomplete request")
	})

	if _, err := client.Ban(context.Background(), BanRequest{ChannelID: "UCx"}); !errors.Is(err, ErrMessageRejected) {
		t.Fatalf("error = %v, want ErrMessageRejected for a missing chat id", err)
	}
	if _, err := client.Ban(context.Background(), BanRequest{LiveChatID: "chat-1"}); !errors.Is(err, ErrMessageRejected) {
		t.Fatalf("error = %v, want ErrMessageRejected for a missing channel id", err)
	}
}

func TestBanMapsInsufficientPermissions(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, googleError(403, "insufficientPermissions", "PERMISSION_DENIED", "", "not a moderator"))
	})

	_, err := client.Ban(context.Background(), BanRequest{LiveChatID: "chat-1", ChannelID: "UCspam0000000000000000001"})
	if !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("error = %v, want ErrNotPermitted", err)
	}
}

func TestUnbanDeletesTheBan(t *testing.T) {
	var method, path, id string
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		method, path, id = r.Method, r.URL.Path, r.URL.Query().Get("id")
		w.WriteHeader(http.StatusNoContent)
	})

	if err := client.Unban(context.Background(), "ban-1"); err != nil {
		t.Fatalf("Unban error = %v", err)
	}
	if method != http.MethodDelete || path != "/liveChat/bans" || id != "ban-1" {
		t.Fatalf("request = %s %s?id=%s", method, path, id)
	}
}

func TestUnbanMapsBanNotFound(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, googleError(404, "liveChatBanNotFound", "NOT_FOUND", "", "already lifted"))
	})
	if err := client.Unban(context.Background(), "ban-1"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("error = %v, want ErrChatNotFound", err)
	}
}

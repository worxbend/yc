package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rivo/uniseg"
)

func TestSendMessagePostsTheDocumentedInsertBody(t *testing.T) {
	var body liveChatInsertRequest
	var part string
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		part = r.URL.Query().Get("part")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode insert body: %v", err)
		}
		fmt.Fprint(w, `{"id":"sent-1","snippet":{"publishedAt":"2026-08-08T20:20:00Z"}}`)
	})

	result, err := client.SendMessage(context.Background(), SendRequest{LiveChatID: "chat-1", Text: "hello chat"})
	if err != nil {
		t.Fatalf("SendMessage error = %v", err)
	}
	if part != "snippet" {
		t.Fatalf("part = %q, want snippet", part)
	}
	if body.Snippet.LiveChatID != "chat-1" || body.Snippet.Type != "textMessageEvent" {
		t.Fatalf("body = %#v, want a textMessageEvent for chat-1", body)
	}
	if body.Snippet.TextMessageDetails.MessageText != "hello chat" {
		t.Fatalf("messageText = %q", body.Snippet.TextMessageDetails.MessageText)
	}
	if result.MessageID != "sent-1" {
		t.Fatalf("MessageID = %q, want sent-1", result.MessageID)
	}
	if result.QuotaUnits != DefaultCostTable().Cost(EndpointMessagesInsert) {
		t.Fatalf("QuotaUnits = %d, want the estimated insert cost", result.QuotaUnits)
	}
	if !result.AcceptedAt.Equal(time.Date(2026, 8, 8, 20, 20, 0, 0, time.UTC)) {
		t.Fatalf("AcceptedAt = %v", result.AcceptedAt)
	}
}

func TestSendMessageExpressesAReplyAsAMentionPrefix(t *testing.T) {
	var body liveChatInsertRequest
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		fmt.Fprint(w, `{"id":"sent-2"}`)
	})

	// YouTube live chat has no parent-message field, so there is nothing to
	// thread. The convention has to be visible in the text itself.
	if _, err := client.SendMessage(context.Background(), SendRequest{
		LiveChatID:         "chat-1",
		Text:               "good point",
		ReplyToDisplayName: "Streamer",
	}); err != nil {
		t.Fatalf("SendMessage error = %v", err)
	}
	if got := body.Snippet.TextMessageDetails.MessageText; got != "@Streamer good point" {
		t.Fatalf("messageText = %q, want the @-prefixed form", got)
	}
}

func TestSendMessageWithoutOAuthNeverSpendsQuota(t *testing.T) {
	client, _ := newTestClient(t, keyCredentials(), func(http.ResponseWriter, *http.Request) {
		t.Fatal("insert dispatched in key-only mode; an insert costs an estimated 50 units to be told no")
	})

	_, err := client.SendMessage(context.Background(), SendRequest{LiveChatID: "chat-1", Text: "hello"})
	if !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("error = %v, want ErrNotPermitted", err)
	}
	if !strings.Contains(err.Error(), "force-ssl") {
		t.Fatalf("error = %q, want it to name the missing scope", err.Error())
	}
}

func TestSendMessageMapsThe429ThatOnlyInsertUses(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, googleError(429, "rateLimitExceeded", "RESOURCE_EXHAUSTED", "", "slow down"))
	})

	result, err := client.SendMessage(context.Background(), SendRequest{LiveChatID: "chat-1", Text: "hello"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	if !result.RateLimited {
		t.Fatal("RateLimited = false; the composer needs to know why the send failed")
	}
}

func TestSendMessageRejectsAnEmptyDraftBeforeDispatch(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(http.ResponseWriter, *http.Request) {
		t.Fatal("insert dispatched for an empty draft")
	})

	if _, err := client.SendMessage(context.Background(), SendRequest{LiveChatID: "chat-1", Text: "   "}); !errors.Is(err, ErrMessageRejected) {
		t.Fatalf("error = %v, want ErrMessageRejected", err)
	}
}

func TestComposeSendTextCapsWithoutSplittingGraphemeClusters(t *testing.T) {
	// A ZWJ family is one cluster of many runes. Truncating it by rune would
	// put mojibake into someone's public chat.
	family := "\U0001F468\u200d\U0001F469\u200d\U0001F467"
	text := strings.Repeat(family, 80)

	got := composeSendText(SendRequest{Text: text})
	if got == "" {
		t.Fatal("composeSendText returned nothing")
	}
	if len([]rune(got)) > MaxChatMessageRunes {
		t.Fatalf("rune count = %d, want at most %d", len([]rune(got)), MaxChatMessageRunes)
	}
	if !strings.HasSuffix(got, family) {
		t.Fatalf("result ends mid-cluster: %q", got)
	}
	clusters := 0
	graphemes := uniseg.NewGraphemes(got)
	for graphemes.Next() {
		clusters++
	}
	if clusters != len(got)/len(family) {
		t.Fatalf("clusters = %d, want whole families only", clusters)
	}
}

func TestComposeSendTextLeavesShortMessagesAlone(t *testing.T) {
	if got := composeSendText(SendRequest{Text: " hello chat "}); got != "hello chat" {
		t.Fatalf("composeSendText = %q, want the trimmed text", got)
	}
}

func TestSendLimiterDeclinesLocallyRatherThanLearningFromA429(t *testing.T) {
	now := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	limiter := NewSendLimiter(SendLimiterConfig{
		Burst:    2,
		Interval: time.Second,
		Now:      func() time.Time { return now },
	})

	for attempt := 1; attempt <= 2; attempt++ {
		if ok, _ := limiter.Allow(); !ok {
			t.Fatalf("send %d declined inside the burst", attempt)
		}
	}
	ok, retryAfter := limiter.Allow()
	if ok {
		t.Fatal("third send allowed; the burst is 2")
	}
	if retryAfter <= 0 || retryAfter > time.Second {
		t.Fatalf("retryAfter = %v, want a wait inside one interval", retryAfter)
	}

	// A fake clock, never a wall-clock sleep.
	now = now.Add(time.Second)
	if ok, _ := limiter.Allow(); !ok {
		t.Fatal("send declined after a full interval elapsed")
	}
}

func TestSendLimiterRefillIsCappedAtTheBurst(t *testing.T) {
	now := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	limiter := NewSendLimiter(SendLimiterConfig{
		Burst:    2,
		Interval: time.Second,
		Now:      func() time.Time { return now },
	})

	now = now.Add(time.Hour)
	for attempt := 1; attempt <= 2; attempt++ {
		if ok, _ := limiter.Allow(); !ok {
			t.Fatalf("send %d declined after a long idle period", attempt)
		}
	}
	if ok, _ := limiter.Allow(); ok {
		t.Fatal("idling for an hour banked more than the burst")
	}
}

func TestNilSendLimiterAllows(t *testing.T) {
	var limiter *SendLimiter
	if ok, _ := limiter.Allow(); !ok {
		t.Fatal("a nil limiter must not block sends")
	}
}

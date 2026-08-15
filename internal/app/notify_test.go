package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/youtube"
)

func TestNotificationSelectsOnlyHighSignalEvents(t *testing.T) {
	tests := []struct {
		kind youtube.EventKind
		want bool
	}{
		{youtube.EventKindText, false},
		{youtube.EventKindSuperChat, true},
		{youtube.EventKindSuperSticker, true},
		{youtube.EventKindNewSponsor, true},
		{youtube.EventKindMemberMilestone, true},
		{youtube.EventKindMembershipGifting, true},
		{youtube.EventKindGiftMembershipReceived, true},
		{youtube.EventKindGift, true},
		{youtube.EventKindChatEnded, true},
		{youtube.EventKindSponsorOnlyModeStarted, false},
		{youtube.EventKindTombstone, false},
	}
	for _, tc := range tests {
		_, ok := notificationTitleForMessage(youtube.Message{Kind: tc.kind})
		if ok != tc.want {
			t.Errorf("%q notifies = %v, want %v", tc.kind, ok, tc.want)
		}
	}
}

func TestNotificationCarriesTheAmountAndAuthor(t *testing.T) {
	message := youtube.Message{
		Kind:   youtube.EventKindSuperChat,
		Author: youtube.Author{DisplayName: "Alice"},
		Text:   "great stream",
		SuperChat: &youtube.SuperChatDetails{
			Amount: youtube.Money{Micros: 5_000_000, Currency: "USD", Display: "$5.00"},
			Tier:   3,
		},
	}
	notification, ok := notificationFromMessage(message, "Tonight's stream")
	if !ok {
		t.Fatal("a super chat should notify")
	}
	if !strings.Contains(notification.Title, "$5.00") {
		t.Fatalf("title = %q, want the amount", notification.Title)
	}
	if !strings.Contains(notification.Title, "Tonight's stream") {
		t.Fatalf("title = %q, want the chat label", notification.Title)
	}
	if !strings.Contains(notification.Body, "Alice") || !strings.Contains(notification.Body, "great stream") {
		t.Fatalf("body = %q", notification.Body)
	}
}

func TestNotificationIsFocusAware(t *testing.T) {
	superChat := youtube.Message{
		Kind:       youtube.EventKindSuperChat,
		LiveChatID: "live-chat",
	}

	model := newModelForTest(t, "demo")
	model.activeChatState().target.LiveChatID = "live-chat"
	model.terminalFocused = true
	model.focus = focusChat
	if model.shouldNotify(superChat) {
		t.Fatal("an event in the chat the user is watching has already been seen")
	}

	model.terminalFocused = false
	if !model.shouldNotify(superChat) {
		t.Fatal("an unfocused terminal must notify")
	}

	model.terminalFocused = true
	model.focus = focusComposer
	if !model.shouldNotify(superChat) {
		t.Fatal("a focused composer means the user is not reading chat")
	}

	model.focus = focusChat
	model.overlay = overlayState{kind: overlayPalette}
	if !model.shouldNotify(superChat) {
		t.Fatal("an open overlay covers the chat pane")
	}
}

func TestHistoricalEventsNeverNotify(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.terminalFocused = false
	message := youtube.Message{Kind: youtube.EventKindSuperChat, Historical: true}
	if model.shouldNotify(message) {
		t.Fatal("the priming page is a backlog, not news")
	}
}

func TestGiftMembershipBurstIsCoalesced(t *testing.T) {
	model := newModelForTest(t, "demo")
	model.terminalFocused = false
	model.systemNotifier = nil
	now := time.Now()

	gift := youtube.Message{
		Kind:       youtube.EventKindGiftMembershipReceived,
		LiveChatID: "live-chat",
		Author:     youtube.Author{DisplayName: "Recipient"},
	}
	model.maybeNotify(gift, now)
	if model.giftBurstCount != 1 {
		t.Fatalf("giftBurstCount = %d, want 1", model.giftBurstCount)
	}
	first := model.lastNotification

	for i := 0; i < 10; i++ {
		model.maybeNotify(gift, now.Add(time.Duration(i)*time.Second))
	}
	if model.giftBurstCount != 11 {
		t.Fatalf("giftBurstCount = %d, want the burst folded into one", model.giftBurstCount)
	}
	if model.lastNotification == nil || model.lastNotification == first {
		t.Fatal("the burst summary should replace the first notification's body")
	}
	if !strings.Contains(model.lastNotification.Body, "11 gift memberships") {
		t.Fatalf("body = %q, want the running count", model.lastNotification.Body)
	}

	// A gift well after the window starts a new burst.
	model.maybeNotify(gift, now.Add(giftBurstWindow*3))
	if model.giftBurstCount != 1 {
		t.Fatalf("giftBurstCount = %d, want a fresh burst", model.giftBurstCount)
	}
}

func TestNotificationTextIsSanitizedAndRedacted(t *testing.T) {
	value := sanitizeNotificationText("access_token=test-not-a-real-token\x07 and\tspaces", 320)
	if strings.Contains(value, "test-not-a-real-token") {
		t.Fatalf("sanitized text still carries a credential: %q", value)
	}
	if strings.ContainsAny(value, "\x07\t\n") {
		t.Fatalf("control characters survived: %q", value)
	}
	if strings.Contains(value, "  ") {
		t.Fatalf("whitespace was not collapsed: %q", value)
	}
}

func TestNotificationTextIsBounded(t *testing.T) {
	value := sanitizeNotificationText(strings.Repeat("x", 500), 32)
	if len([]rune(value)) != 32 {
		t.Fatalf("length = %d, want 32", len([]rune(value)))
	}
	if !strings.HasSuffix(value, "...") {
		t.Fatalf("a truncated value should say so: %q", value)
	}
}

func TestDesktopNotifierFallsBackToTheBell(t *testing.T) {
	var out bytes.Buffer
	notifier := defaultSystemNotifier{
		desktop: desktopNotifier{goos: "plan9"},
		bell:    terminalBellNotifier{w: &out},
	}
	if err := notifier.Notify(context.Background(), SystemNotification{Title: "Super Chat"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if out.String() != terminalBell {
		t.Fatalf("output = %q, want the terminal bell", out.String())
	}
}

func TestDesktopNotifierUsesThePlatformCommand(t *testing.T) {
	var got []string
	notifier := desktopNotifier{
		goos:     "linux",
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		runCommand: func(_ context.Context, path string, args ...string) error {
			got = append([]string{path}, args...)
			return nil
		},
	}
	if err := notifier.Notify(context.Background(), SystemNotification{Title: "New member", Body: "Alice"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(got) == 0 || !strings.HasSuffix(got[0], "notify-send") {
		t.Fatalf("command = %v, want notify-send", got)
	}
	if !strings.Contains(strings.Join(got, " "), "New member") {
		t.Fatalf("command = %v, want the title", got)
	}
}

// A chatter can be named "--icon=/etc/passwd". notify-send parses options
// anywhere on its command line, so without the "--" terminator the name would
// be consumed as an option instead of displayed as text.
func TestDesktopNotifierTerminatesOptionsBeforeUserText(t *testing.T) {
	name, args, ok := desktopNotificationCommand("linux", "New member", "--icon=/etc/passwd joined")
	if !ok || name != "notify-send" {
		t.Fatalf("command = %q ok=%v, want notify-send", name, ok)
	}
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep == -1 {
		t.Fatalf("args = %v, want a \"--\" terminator before the title", args)
	}
	if want := []string{"--", "New member", "--icon=/etc/passwd joined"}; !slices.Equal(args[sep:], want) {
		t.Fatalf("args tail = %v, want %v", args[sep:], want)
	}
}

func TestDesktopNotifierReportsUnsupportedPlatforms(t *testing.T) {
	notifier := desktopNotifier{goos: "js"}
	err := notifier.Notify(context.Background(), SystemNotification{Title: "x"})
	if !errors.Is(err, ErrDesktopNotificationUnsupported) {
		t.Fatalf("err = %v, want ErrDesktopNotificationUnsupported", err)
	}
}

func TestNotificationSummaryJoinsTitleAndBody(t *testing.T) {
	if got := notificationSummary(SystemNotification{Title: "New member", Body: "Alice"}); got != "New member: Alice" {
		t.Fatalf("summary = %q", got)
	}
	if got := notificationSummary(SystemNotification{Title: "Same", Body: "same"}); got != "Same" {
		t.Fatalf("summary = %q, want no duplication", got)
	}
}

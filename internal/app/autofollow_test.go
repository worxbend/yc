package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/youtube"
)

// stubResolver answers ResolveTarget from a queue of scripted outcomes and is
// safe to call from command goroutines.
type stubResolver struct {
	mu       sync.Mutex
	outcomes []resolveOutcome
	calls    int
}

type resolveOutcome struct {
	target youtube.ChatTarget
	err    error
}

func (r *stubResolver) ResolveTarget(ctx context.Context, target youtube.ChatTarget) (youtube.ChatTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if len(r.outcomes) == 0 {
		return target, errors.New("no scripted outcome")
	}
	outcome := r.outcomes[0]
	r.outcomes = r.outcomes[1:]
	return outcome.target, outcome.err
}

func (r *stubResolver) Broadcast(ctx context.Context, videoID string) (youtube.BroadcastInfo, error) {
	return youtube.BroadcastInfo{}, errors.New("not scripted")
}

// newAutoFollowModel builds a model with auto-follow on, one chat that knows
// its channel, and a scripted resolver.
func newAutoFollowModel(t *testing.T, resolver *stubResolver) shellModel {
	t.Helper()
	model := newModelForTest(t, "oldchatid")
	model.autoFollowEnabled = true
	model.autoFollowInterval = minAutoFollowInterval
	model.autoFollowMaxChecks = 2
	model.broadcastResolver = resolver
	state := model.activeChatState()
	state.mergeTarget(youtube.ChatTarget{
		LiveChatID: "oldchatid",
		ChannelID:  "UC-channel",
		VideoID:    "oldvideo",
	})
	return model
}

// closeChat sends the transport's "this chat is over" state through Update.
func closeChat(t *testing.T, model shellModel) (shellModel, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(chatClientConnectionStateMsg{
		state: youtube.ConnectionState{
			Status: youtube.ConnectionClosed,
			ChatID: "oldchatid",
			Detail: "live chat ended",
			At:     time.Now(),
		},
		ok: true,
	})
	return next.(shellModel), cmd
}

func TestAutoFollowStaysOffByDefault(t *testing.T) {
	model := newModelForTest(t, "oldchatid")
	model.broadcastResolver = &stubResolver{}
	model, _ = closeChat(t, model)
	if state := model.activeChatState(); state.autoFollowActive {
		t.Fatal("auto-follow armed without being enabled")
	}
}

func TestAutoFollowNeedsAChannelToFollow(t *testing.T) {
	resolver := &stubResolver{}
	model := newModelForTest(t, "oldchatid")
	model.autoFollowEnabled = true
	model.broadcastResolver = resolver
	// The chat never learned its channel, so there is nothing to re-resolve.
	model, _ = closeChat(t, model)
	if state := model.activeChatState(); state.autoFollowActive {
		t.Fatal("auto-follow armed with no channel or handle to follow")
	}
}

func TestChatEndedArmsAutoFollowAndSchedulesACheck(t *testing.T) {
	model := newAutoFollowModel(t, &stubResolver{})
	model, cmd := closeChat(t, model)

	state := model.activeChatState()
	if !state.autoFollowActive {
		t.Fatal("auto-follow did not arm on ConnectionClosed")
	}
	if cmd == nil {
		t.Fatal("no tick was scheduled")
	}
	if !strings.Contains(state.status.Detail, "auto-follow") {
		t.Fatalf("status detail = %q; it must say auto-follow is watching", state.status.Detail)
	}
}

func TestAutoFollowStopsAfterTheConfiguredChecks(t *testing.T) {
	resolver := &stubResolver{}
	model := newAutoFollowModel(t, resolver)
	model, _ = closeChat(t, model)

	// Each failed check counts toward the cap of 2.
	for i := 0; i < 2; i++ {
		next, _ := model.Update(autoFollowResolvedMsg{
			chatKey: "oldchatid",
			err:     youtube.ErrNoActiveBroadcast,
		})
		model = next.(shellModel)
	}

	state := model.activeChatState()
	if state.autoFollowActive {
		t.Fatal("auto-follow still active past the check cap")
	}
	if !strings.Contains(state.status.Detail, "stopped after 2 checks") {
		t.Fatalf("status detail = %q; the stop must say why", state.status.Detail)
	}
}

func TestAutoFollowTreatsTheOldChatAsStillOffline(t *testing.T) {
	model := newAutoFollowModel(t, &stubResolver{})
	model, _ = closeChat(t, model)

	// Resolution succeeded but returned the ended broadcast's own chat:
	// chat outlives the stream, so this is "still offline", not a new one.
	next, cmd := model.Update(autoFollowResolvedMsg{
		chatKey: "oldchatid",
		target: youtube.ChatTarget{
			Kind:       youtube.TargetChannelID,
			ChannelID:  "UC-channel",
			LiveChatID: "oldchatid",
		},
	})
	model = next.(shellModel)

	state := model.activeChatState()
	if !state.autoFollowActive {
		t.Fatal("auto-follow gave up on seeing the ended chat's own ID")
	}
	if state.autoFollowChecks != 1 {
		t.Fatalf("checks = %d, want 1", state.autoFollowChecks)
	}
	if cmd == nil {
		t.Fatal("no follow-up tick was scheduled")
	}
}

func TestAutoFollowAdoptsTheNewStreamAndReconnects(t *testing.T) {
	model := newAutoFollowModel(t, &stubResolver{})
	model, _ = closeChat(t, model)

	next, cmd := model.Update(autoFollowResolvedMsg{
		chatKey: "oldchatid",
		target: youtube.ChatTarget{
			Kind:       youtube.TargetChannelID,
			ChannelID:  "UC-channel",
			VideoID:    "newvideo",
			LiveChatID: "newchatid",
			Title:      "round two",
		},
	})
	model = next.(shellModel)
	_ = cmd

	state := model.activeChatState()
	if state.autoFollowActive {
		t.Fatal("auto-follow still active after finding the new stream")
	}
	if state.ended {
		t.Fatal("chat still marked ended after adopting the new stream")
	}
	if state.target.LiveChatID != "newchatid" || state.target.VideoID != "newvideo" {
		t.Fatalf("target = %+v; the new identifiers were not adopted", state.target)
	}
	if state.status.Status != youtube.ConnectionConnecting {
		t.Fatalf("status = %q, want connecting", state.status.Status)
	}
	// The chat keeps its history: auto-follow reconnects the same UI chat
	// rather than opening a second one.
	if got := model.chatCount(); got != 1 {
		t.Fatalf("chatCount = %d, want 1", got)
	}
	// Messages stamped with the new routing key still land in this chat.
	if found := model.chats.stateForChatID("newchatid"); found != state {
		t.Fatal("a message for the new liveChatId would not route to the followed chat")
	}
}

func TestAutoFollowIntervalAndCapClamps(t *testing.T) {
	if got := autoFollowIntervalFor(1); got != minAutoFollowInterval {
		t.Fatalf("interval for 1s = %s, want the %s floor", got, minAutoFollowInterval)
	}
	if got := autoFollowIntervalFor(120); got != 2*time.Minute {
		t.Fatalf("interval for 120s = %s, want 2m", got)
	}
	if got := autoFollowMaxChecksFor(0); got != 30 {
		t.Fatalf("max checks for 0 = %d, want the default 30", got)
	}
	if got := autoFollowMaxChecksFor(5); got != 5 {
		t.Fatalf("max checks for 5 = %d, want 5", got)
	}
}

func TestAutoFollowProbeTargetPrefersTheChannelID(t *testing.T) {
	probe := autoFollowProbeTarget(youtube.ChatTarget{ChannelID: "UC-x", Handle: "@x"})
	if probe.Kind != youtube.TargetChannelID || probe.ChannelID != "UC-x" {
		t.Fatalf("probe = %+v, want channel ID form", probe)
	}
	probe = autoFollowProbeTarget(youtube.ChatTarget{Handle: "@x"})
	if probe.Kind != youtube.TargetHandle || probe.Handle != "@x" {
		t.Fatalf("probe = %+v, want handle form", probe)
	}
	if probe := autoFollowProbeTarget(youtube.ChatTarget{VideoID: "v"}); probe.Key() != "" {
		t.Fatalf("probe = %+v, want empty for a channel-less chat", probe)
	}
}

package app

import (
	"testing"

	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/youtube"
)

// newAnimatedModelForTest builds a model with the reveal animation switched on,
// which is the only configuration in which a message can be mid-reveal when the
// user switches chats.
func newAnimatedModelForTest(t *testing.T, chats ...string) shellModel {
	t.Helper()
	cfg := config.Default()
	cfg.Features.AnimationMode = "full"
	cfg.Features.ScrollbackLimit = 100
	cfg.DefaultChats = chats
	model := newShellModel(cfg, animation.SystemClock{})
	model.width = 100
	model.height = 30
	return model
}

// TestReconnectDoesNotDuplicateHistory pins that the priming page a rebuilt
// transport re-delivers after ctrl+r does not print every message twice.
func TestReconnectDoesNotDuplicateHistory(t *testing.T) {
	model := newModelForTest(t, "demo")
	backlog := []youtube.Message{
		testMessage(t, "m1", "", "alice", "one"),
		testMessage(t, "m2", "", "bob", "two"),
		testMessage(t, "m3", "", "carol", "three"),
	}
	deliver := func(m shellModel) shellModel {
		for _, message := range backlog {
			message.Historical = true
			next, _ := m.Update(chatClientMessageMsg{message: message, ok: true})
			m = next.(shellModel)
		}
		return m
	}
	model = deliver(model)
	// ctrl+r rebuilds the poller, which re-primes with no page token and an
	// empty dedupe ring, so the same backlog arrives again.
	model = deliver(model)

	state := model.activeChatState()
	if got := len(state.messages); got != len(backlog) {
		ids := make([]string, 0, got)
		for _, message := range state.messages {
			ids = append(ids, message.ID)
		}
		t.Fatalf("messages = %d %v, want %d; the re-primed backlog was appended twice",
			got, ids, len(backlog))
	}
}

// TestSwitchingChatsDoesNotStrandAnInFlightReveal pins that a message still
// mid-reveal when the user switches away reaches the retained history.
func TestSwitchingChatsDoesNotStrandAnInFlightReveal(t *testing.T) {
	model := newAnimatedModelForTest(t, "first", "second")

	next, _ := model.Update(chatClientMessageMsg{
		message: testMessage(t, "m1", "", "alice", "a fairly long message that will not reveal instantly"),
		ok:      true,
	})
	model = next.(shellModel)
	first := model.chats.stateForKey("first")
	if first.active.len() == 0 {
		t.Skip("message revealed immediately; nothing to strand")
	}

	if !model.chats.switchBy(1) {
		t.Fatal("switchBy(1) did not move to the second chat")
	}
	// Drive the reveal clock the way the runtime would.
	for i := 0; i < 200; i++ {
		next, _ := model.Update(revealTickMsg{})
		model = next.(shellModel)
	}

	first = model.chats.stateForKey("first")
	if first.active.len() != 0 {
		t.Fatalf("reveal stranded after switching away: still tracking %v", first.active.ids())
	}
	if len(first.messages) != 1 {
		t.Fatalf("messages = %d, want 1; the revealed row never reached history", len(first.messages))
	}
}

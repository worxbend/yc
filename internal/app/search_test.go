package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// typeSearch opens the search input and types query one rune at a time, the
// way a user would.
func typeSearch(t *testing.T, model shellModel, query string) shellModel {
	t.Helper()
	model = press(t, model, runeKey('/'))
	if !model.activeChatState().searchInput {
		t.Fatal("/ did not open the search input")
	}
	for _, r := range query {
		model = press(t, model, runeKey(r))
	}
	return model
}

func TestSearchIsModalAndIncremental(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.messages = append(state.messages,
		testMessage(t, "m1", "", "alice", "hello world"),
		testMessage(t, "m2", "", "bob", "nothing here"),
		testMessage(t, "m3", "", "carol", "hello again"),
	)

	model = typeSearch(t, model, "hello")
	state = model.activeChatState()
	if state.searchQuery != "hello" {
		t.Fatalf("searchQuery = %q, want %q", state.searchQuery, "hello")
	}
	// Incremental: while typing, the cursor already sits on the newest match.
	if got := replyMessageID(state.selected); got != "m3" {
		t.Fatalf("selected = %q, want the newest match m3", got)
	}
	// Modal: "d" is query text, never an armed deletion.
	if model.moderation.stage != moderationStageIdle {
		t.Fatal("typing into the search input armed a moderation action")
	}

	model = press(t, model, key(tea.KeyEnter))
	if model.activeChatState().searchInput {
		t.Fatal("enter did not commit the search")
	}
	if model.activeChatState().searchQuery != "hello" {
		t.Fatal("enter discarded the committed query")
	}
}

func TestSearchNextPrevWalkMatchesWithoutWrapping(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.messages = append(state.messages,
		testMessage(t, "m1", "", "alice", "target one"),
		testMessage(t, "m2", "", "bob", "other"),
		testMessage(t, "m3", "", "carol", "target two"),
	)
	model = typeSearch(t, model, "target")
	model = press(t, model, key(tea.KeyEnter))

	// n walks older, stopping at the oldest match rather than wrapping.
	model = press(t, model, runeKey('n'))
	if got := replyMessageID(model.activeChatState().selected); got != "m1" {
		t.Fatalf("after n, selected = %q, want m1", got)
	}
	model = press(t, model, runeKey('n'))
	if got := replyMessageID(model.activeChatState().selected); got != "m1" {
		t.Fatalf("n wrapped past the oldest match to %q", got)
	}
	// N walks back toward the newest match.
	model = press(t, model, runeKey('N'))
	if got := replyMessageID(model.activeChatState().selected); got != "m3" {
		t.Fatalf("after N, selected = %q, want m3", got)
	}
}

func TestEscClearsTheSearchBeforeTheReply(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.messages = append(state.messages, testMessage(t, "m1", "", "alice", "hello"))

	// esc inside the input abandons query and mode together.
	model = typeSearch(t, model, "hel")
	model = press(t, model, key(tea.KeyEsc))
	state = model.activeChatState()
	if state.searchInput || state.searchQuery != "" {
		t.Fatal("esc inside the input did not clear the search")
	}

	// A committed query is cleared by the next esc from the chat view, before
	// the cursor underneath it is touched.
	model = typeSearch(t, model, "hello")
	model = press(t, model, key(tea.KeyEnter))
	model = press(t, model, key(tea.KeyEsc))
	state = model.activeChatState()
	if state.searchQuery != "" {
		t.Fatal("esc did not clear the committed query")
	}
	if state.selected == nil {
		t.Fatal("clearing the search also dropped the cursor; it must unwind one step at a time")
	}
}

func TestSearchStatusLabelReportsMatches(t *testing.T) {
	model := newModelForTest(t, "demo")
	state := model.activeChatState()
	state.messages = append(state.messages,
		testMessage(t, "m1", "", "alice", "hello"),
		testMessage(t, "m2", "", "bob", "hello"),
	)
	model = typeSearch(t, model, "hello")
	if label := model.searchStatusLabel(); !strings.HasPrefix(label, "/hello") {
		t.Fatalf("input label = %q, want it to echo the query", label)
	}
	model = press(t, model, key(tea.KeyEnter))
	if label := model.searchStatusLabel(); !strings.Contains(label, "2 matches") {
		t.Fatalf("committed label = %q, want the match count", label)
	}
}

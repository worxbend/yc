package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/youtube"
)

// mentionModel returns a composer-focused model whose roster has spoken.
func mentionModel(t *testing.T, draft string) shellModel {
	t.Helper()
	cfg := config.Default()
	cfg.Features.AnimationMode = "off"
	cfg.DefaultChats = []string{"demo"}

	model := newShellModel(cfg, nil)
	model.focus = focusComposer
	state := model.activeChatState()
	for _, name := range []string{"nova_dev", "pixelwitch", "novacaine", "quinn"} {
		state.observeAuthor(youtube.Message{
			ID:     name,
			Author: youtube.Author{ChannelID: "UC-" + name, DisplayName: name},
		})
	}
	state.composerText = draft
	return model
}

func TestMentionPrefixOnlyMatchesAWordStartingAt(t *testing.T) {
	for name, tc := range map[string]struct {
		draft  string
		prefix string
		ok     bool
	}{
		"bare at":               {draft: "@", prefix: "", ok: true},
		"partial handle":        {draft: "hey @nov", prefix: "nov", ok: true},
		"at start of draft":     {draft: "@pix", prefix: "pix", ok: true},
		"finished mention":      {draft: "@nova_dev hello", ok: false},
		"no at at all":          {draft: "just talking", ok: false},
		"email is not a handle": {draft: "mail me at someone@example.com", ok: false},
	} {
		t.Run(name, func(t *testing.T) {
			prefix, ok := mentionPrefix(tc.draft)
			if ok != tc.ok {
				t.Fatalf("ok = %t, want %t", ok, tc.ok)
			}
			if ok && prefix != tc.prefix {
				t.Fatalf("prefix = %q, want %q", prefix, tc.prefix)
			}
		})
	}
}

func TestMentionSuggestionsRankPrefixMatchesFirst(t *testing.T) {
	model := mentionModel(t, "thanks @nova")
	got := model.mentionSuggestions()
	if len(got) < 2 {
		t.Fatalf("suggestions = %v, want at least two", got)
	}
	// A prefix match is what the user meant; a substring match is a guess and
	// must never outrank one.
	for _, name := range got[:2] {
		if !strings.HasPrefix(strings.ToLower(name), "nova") {
			t.Fatalf("suggestions = %v, want the prefix matches first", got)
		}
	}
}

func TestMentionSuggestionsOnlyOfferPeopleWhoHaveSpoken(t *testing.T) {
	model := mentionModel(t, "@")
	for _, name := range model.mentionSuggestions() {
		if name == "someone_who_never_spoke" {
			t.Fatal("suggested a name that is not in the roster")
		}
	}
	// YouTube reports no presence, so the roster is the only membership list
	// there is; suggesting anything else would offer names not in the room.
	if len(model.mentionSuggestions()) != 4 {
		t.Fatalf("suggestions = %v, want the four observed authors", model.mentionSuggestions())
	}
}

func TestTabAcceptsTheSelectedMention(t *testing.T) {
	model := mentionModel(t, "thanks @pix")
	updated, _, handled := model.handleMentionKey(tea.KeyMsg{Type: tea.KeyTab})
	if !handled {
		t.Fatal("tab was not claimed while suggestions were showing")
	}
	got := updated.(shellModel).activeChatState().composerText
	if got != "thanks @pixelwitch " {
		t.Fatalf("draft = %q, want %q", got, "thanks @pixelwitch ")
	}
	// The trailing space closes the strip, so the next keystroke is text.
	if suggestions := updated.(shellModel).mentionSuggestions(); len(suggestions) != 0 {
		t.Fatalf("strip still open after accepting: %v", suggestions)
	}
}

func TestArrowsMoveTheSelectionCyclically(t *testing.T) {
	model := mentionModel(t, "@")
	count := len(model.mentionSuggestions())

	updated, _, _ := model.handleMentionKey(tea.KeyMsg{Type: tea.KeyUp})
	// Holding a direction key must never strand the user at an end.
	if got := updated.(shellModel).mentionSelection(count); got != count-1 {
		t.Fatalf("selection after up = %d, want %d", got, count-1)
	}
	updated, _, _ = updated.(shellModel).handleMentionKey(tea.KeyMsg{Type: tea.KeyDown})
	if got := updated.(shellModel).mentionSelection(count); got != 0 {
		t.Fatalf("selection after wrapping down = %d, want 0", got)
	}
}

func TestEscDismissesOnlyTheCurrentWord(t *testing.T) {
	model := mentionModel(t, "hey @nov")
	updated, _, handled := model.handleMentionKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled {
		t.Fatal("esc was not claimed while suggestions were showing")
	}
	dismissed := updated.(shellModel)
	if got := dismissed.mentionSuggestions(); len(got) != 0 {
		t.Fatalf("suggestions = %v, want none after dismissal", got)
	}

	// Typing on turns it into a different word, which was never dismissed.
	dismissed.activeChatState().composerText = "hey @nova"
	if got := dismissed.mentionSuggestions(); len(got) == 0 {
		t.Fatal("dismissal outlived the word it was aimed at")
	}
}

func TestTheStripDoesNotClaimKeysItHasNoUseFor(t *testing.T) {
	// With nothing to offer, tab must still cycle focus and esc must still
	// leave the composer - four bindings would otherwise break silently.
	model := mentionModel(t, "no mention here")
	for _, key := range []tea.KeyType{tea.KeyTab, tea.KeyUp, tea.KeyDown, tea.KeyEsc} {
		if _, _, handled := model.handleMentionKey(tea.KeyMsg{Type: key}); handled {
			t.Fatalf("key %v was claimed with no suggestions showing", key)
		}
	}
}

func TestTheStripIsSilentOutsideTheComposer(t *testing.T) {
	model := mentionModel(t, "@nov")
	model.focus = focusChat
	if got := model.mentionSuggestions(); len(got) != 0 {
		t.Fatalf("suggestions = %v while the chat had focus", got)
	}
}

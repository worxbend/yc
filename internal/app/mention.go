package app

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// maxMentionSuggestions bounds the completion strip. It is short on purpose:
// the strip sits above the composer and a long list would push the message the
// user is replying to off the screen.
const maxMentionSuggestions = 5

// mentionSuggestions returns the ranked completions for the word being typed.
//
// Candidates come from the roster, which is only the people yc has actually
// seen speak - YouTube reports no presence at all, so there is no member list
// to complete against and pretending otherwise would offer names that are not
// in the room.
func (m shellModel) mentionSuggestions() []string {
	if m.focus != focusComposer || m.anyOverlayOpen() {
		return nil
	}
	state := m.activeChatState()
	if state == nil {
		return nil
	}
	prefix, ok := mentionPrefix(state.composerText)
	if !ok || state.mentionDismissed == dismissalKey(prefix) {
		return nil
	}

	lowered := strings.ToLower(prefix)
	var starts, contains []string
	for _, name := range state.rosterNames() {
		switch candidate := strings.ToLower(name); {
		case lowered == "":
			starts = append(starts, name)
		case strings.HasPrefix(candidate, lowered):
			starts = append(starts, name)
		case strings.Contains(candidate, lowered):
			contains = append(contains, name)
		}
	}

	// A prefix match is what the user meant; a substring match is a guess, so
	// it never outranks one.
	ranked := append(starts, contains...) //nolint:gocritic // starts is dead after this; extending it in place is the intent
	if len(ranked) > maxMentionSuggestions {
		ranked = ranked[:maxMentionSuggestions]
	}
	return ranked
}

// mentionPrefix returns the partial handle at the end of a draft.
//
// Completion only offers itself while the caret is inside an @word: an "@" in
// the middle of a finished sentence is text the user already committed to, and
// popping a list over it would steal the next tab keystroke.
func mentionPrefix(draft string) (string, bool) {
	at := strings.LastIndex(draft, "@")
	if at < 0 {
		return "", false
	}
	// The "@" has to start a word, or it is an email address rather than a
	// mention.
	if at > 0 {
		previous := draft[at-1]
		if previous != ' ' && previous != '\t' {
			return "", false
		}
	}
	prefix := draft[at+1:]
	if strings.ContainsAny(prefix, " \t") {
		return "", false
	}
	return prefix, true
}

// mentionSelection bounds the highlighted suggestion.
func (m shellModel) mentionSelection(count int) int {
	if count <= 0 {
		return 0
	}
	state := m.activeChatState()
	if state == nil {
		return 0
	}
	selected := state.mentionSelected
	if selected < 0 || selected >= count {
		return 0
	}
	return selected
}

// handleMentionKey lets the completion strip claim tab, up, down, and esc, and
// only while it has something to offer.
//
// Claiming them unconditionally would break the four bindings those keys carry
// everywhere else in the composer, so this reports whether it consumed the key
// rather than swallowing it.
//
//nolint:unparam // every modal key handler shares the (model, cmd, consumed) shape, even when this one never issues a command
func (m shellModel) handleMentionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	suggestions := m.mentionSuggestions()
	if len(suggestions) == 0 {
		return m, nil, false
	}
	state := m.activeChatState()
	if state == nil {
		return m, nil, false
	}

	switch msg.Type {
	case tea.KeyTab:
		m.acceptMention(suggestions[m.mentionSelection(len(suggestions))])
		return m, nil, true
	case tea.KeyUp:
		state.mentionSelected = wrapIndex(m.mentionSelection(len(suggestions))-1, len(suggestions))
		return m, nil, true
	case tea.KeyDown:
		state.mentionSelected = wrapIndex(m.mentionSelection(len(suggestions))+1, len(suggestions))
		return m, nil, true
	case tea.KeyEsc:
		// Dismissed for this word only. A blanket dismissal would mean the
		// next mention in the same message silently offers nothing.
		if prefix, ok := mentionPrefix(state.composerText); ok {
			state.mentionDismissed = dismissalKey(prefix)
		}
		state.mentionSelected = 0
		return m, nil, true
	}
	return m, nil, false
}

// acceptMention replaces the partial handle with the chosen name.
func (m *shellModel) acceptMention(name string) {
	state := m.activeChatState()
	if state == nil {
		return
	}
	at := strings.LastIndex(state.composerText, "@")
	if at < 0 {
		return
	}
	state.composerText = state.composerText[:at]
	state.mentionSelected = 0
	state.mentionDismissed = ""
	// The trailing space both finishes the mention and closes the strip, so
	// the next keystroke is ordinary text again.
	m.insertComposerText("@" + name + " ")
}

// dismissalKey is the stored form of a dismissed partial handle.
//
// It carries the "@" so that an empty key - the zero value, meaning nothing was
// dismissed - can never be mistaken for a dismissal of the bare "@" the user
// has only just typed.
func dismissalKey(prefix string) string { return "@" + prefix }

// wrapIndex moves a selection cyclically, so holding a direction key never
// strands the user at an end of the list.
func wrapIndex(index, count int) int {
	if count <= 0 {
		return 0
	}
	return ((index % count) + count) % count
}

// rosterNames returns the display names seen speaking, sorted, for the mention
// completion strip.
func (s *chatState) rosterNames() []string {
	if s == nil || len(s.roster) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.roster))
	seen := make(map[string]bool, len(s.roster))
	for _, author := range s.roster {
		name := strings.TrimSpace(author.DisplayName)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

package app

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/youtube"
)

// Message search is a local, view-lane concern in exactly the way filters are:
// it decides where the viewport looks, never what is retained, and it costs no
// quota. "/" opens an input line, every edit jumps the cursor to the newest
// match, n/N walk older and newer matches, and esc clears the whole thing.
//
// Search reuses the browsing cursor (chatState.selected) rather than keeping a
// cursor of its own. One cursor means the highlight the user sees, the message
// r would reply to, and the message y would copy can never be three different
// rows.

// handleSearchKey owns the keyboard while the search input line is open. It is
// modal in the same way the moderation duration prompt is: every key belongs
// to the field, so typing "d" into a query cannot arm a deletion.
//
//nolint:unparam // every modal key handler shares the (model, cmd, consumed) shape, even when this one never issues a command
func (m shellModel) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	state := m.activeChatState()
	if state == nil || !state.searchInput {
		return m, nil, false
	}
	switch msg.Type {
	case tea.KeyEsc:
		// esc abandons the search entirely: input mode closes and the query
		// clears, so the highlight never outlives the intent.
		state.searchInput = false
		state.searchQuery = ""
		return m, nil, true
	case tea.KeyEnter:
		// enter commits: the query stays active for n/N until esc clears it.
		state.searchInput = false
		return m, nil, true
	case tea.KeyBackspace:
		state.searchQuery = trimLastGrapheme(state.searchQuery)
		m.searchJumpToNewestMatch()
		return m, nil, true
	case tea.KeyCtrlU:
		state.searchQuery = ""
		return m, nil, true
	case tea.KeySpace:
		state.searchQuery += " "
		m.searchJumpToNewestMatch()
		return m, nil, true
	case tea.KeyRunes:
		state.searchQuery += string(msg.Runes)
		m.searchJumpToNewestMatch()
		return m, nil, true
	}
	// Any other key is swallowed rather than falling through to a normal-mode
	// binding: a modal input that half-owns the keyboard would let pgup arm
	// scrolling while the user believes they are typing a query.
	return m, nil, true
}

// startSearchInput opens the input line, keeping any previous query so "/"
// then enter repeats the last search.
func (m *shellModel) startSearchInput() {
	if state := m.activeChatState(); state != nil {
		state.searchInput = true
	}
}

// clearSearch drops the query and the input mode together.
func (m *shellModel) clearSearch() {
	state := m.activeChatState()
	if state == nil {
		return
	}
	state.searchInput = false
	state.searchQuery = ""
}

// searchActive reports whether a query is highlighting matches right now.
func (m shellModel) searchActive() bool {
	state := m.activeChatState()
	return state != nil && (state.searchInput || strings.TrimSpace(state.searchQuery) != "")
}

// messageMatchesSearch is the one match predicate, shared by the jump logic,
// the row highlight, and the status label so they can never disagree.
//
// It matches the text and the author, case-insensitively: "who said this" and
// "where did that word go by" are both things a moderator scrolls back for.
func messageMatchesSearch(message youtube.Message, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return false
	}
	if strings.Contains(strings.ToLower(message.Text), query) {
		return true
	}
	return strings.Contains(strings.ToLower(message.Author.DisplayName), query)
}

// searchMatches returns the selectable messages the active query matches,
// oldest first, in the same order the viewport draws them.
func (m shellModel) searchMatches() []youtube.Message {
	state := m.activeChatState()
	if state == nil || strings.TrimSpace(state.searchQuery) == "" {
		return nil
	}
	candidates := m.selectableMessages()
	matches := make([]youtube.Message, 0, len(candidates))
	for _, message := range candidates {
		if messageMatchesSearch(message, state.searchQuery) {
			matches = append(matches, message)
		}
	}
	return matches
}

// searchJumpToNewestMatch is the incremental step: every edit of the query
// moves the cursor to the newest match and scrolls it into view, so the user
// watches the search land while still typing it.
func (m *shellModel) searchJumpToNewestMatch() {
	matches := m.searchMatches()
	if len(matches) == 0 {
		return
	}
	m.selectSearchMatch(matches[len(matches)-1])
}

// jumpSearchMatch moves the cursor delta matches through the ordered match
// list: n (-1) walks older, N (+1) walks newer, both stopping at the ends
// rather than wrapping so "how far back was that" stays answerable.
func (m *shellModel) jumpSearchMatch(delta int) {
	matches := m.searchMatches()
	if len(matches) == 0 {
		return
	}
	state := m.activeChatState()
	current := replyMessageID(state.selected)
	index := -1
	for i, match := range matches {
		if match.ID == current {
			index = i
			break
		}
	}
	if index == -1 {
		// No match is selected yet: n starts from the newest match and walks
		// back, N lands on the newest.
		index = len(matches) - 1
	} else {
		index += delta
		if index < 0 {
			index = 0
		}
		if index >= len(matches) {
			index = len(matches) - 1
		}
	}
	m.selectSearchMatch(matches[index])
}

// selectSearchMatch points the browsing cursor at a match and scrolls the
// viewport so the match is on screen.
func (m *shellModel) selectSearchMatch(message youtube.Message) {
	state := m.activeChatState()
	if state == nil {
		return
	}
	state.selected = replyContextFromMessage(message)
	m.scrollToMessage(message.ID)
}

// scrollToMessage sets the scroll offset so the named message's first row sits
// near the middle of the viewport. It renders the same blocks the view will
// draw, so the offset is exact rather than the estimate maxScrollOffset uses.
func (m *shellModel) scrollToMessage(id string) {
	layout := m.layout()
	height := layout.chatContentHeight
	if height <= 0 {
		return
	}
	blocks := m.chatRowBlocks(layout)
	total := chatRowBlockCount(blocks)
	firstRow := -1
	index := 0
	for _, block := range blocks {
		if block.separatorBefore {
			index++
		}
		if firstRow == -1 && block.message.ID == id {
			firstRow = index
		}
		index += chatRowBlockRowCount(block)
	}
	state := m.activeChatState()
	if firstRow == -1 || total <= height {
		state.scrollOffset = 0
		m.clampScroll()
		return
	}
	offset := total - firstRow - height/2
	if maxOffset := total - height; offset > maxOffset {
		offset = maxOffset
	}
	state.scrollOffset = clampMin(offset, 0)
}

// searchStatusLabel is the status-bar rendering of the search state: the query
// as typed while the input is open, and the query with its match count once
// committed.
func (m shellModel) searchStatusLabel() string {
	state := m.activeChatState()
	if state == nil {
		return ""
	}
	if state.searchInput {
		return "/" + state.searchQuery + "█"
	}
	if strings.TrimSpace(state.searchQuery) == "" {
		return ""
	}
	count := len(m.searchMatches())
	if count == 1 {
		return fmt.Sprintf("/%s · 1 match · n/N", state.searchQuery)
	}
	return fmt.Sprintf("/%s · %d matches · n/N", state.searchQuery, count)
}

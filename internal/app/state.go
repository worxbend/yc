package app

import (
	"strings"
	"time"

	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/youtube"
)

// queuedComposerSend is one outbound message awaiting dispatch.
//
// Draft is kept alongside Text so a failed send can restore exactly what the
// user typed, including the "@Name " reply prefix, rather than the normalized
// wire form.
type queuedComposerSend struct {
	ID      int
	ChatKey string
	Text    string
	Draft   string
	Reply   *composerReplyContext
}

// seenMessageRingSize bounds the per-chat message-ID memory. It comfortably
// exceeds one priming page (youtube.MaxResultsPerPoll) so a full backlog
// re-delivered after a reconnect is recognized in its entirety.
const seenMessageRingSize = 4096

// rosterRingSize bounds the per-chat speaker roster. Mention completion only
// needs people who have spoken recently, and a busy broadcast can otherwise
// accumulate an Author per distinct chatter for the whole session.
const rosterRingSize = 2048

func replyMessageID(reply *composerReplyContext) string {
	if reply == nil {
		return ""
	}
	return reply.MessageID
}

func cloneComposerReply(reply *composerReplyContext) *composerReplyContext {
	if reply == nil {
		return nil
	}
	cloned := *reply
	return &cloned
}

func replyContextFromMessage(message youtube.Message) *composerReplyContext {
	return &composerReplyContext{
		MessageID: message.ID,
		Author:    message.Author.DisplayName,
		Text:      message.Text,
	}
}

// chatState is everything the UI remembers about one open live chat.
//
// Switching chats must not lose any of it, so every per-chat concern lives
// here rather than on the model: history, scroll position, filters, the draft,
// the reply target, the send queue, and the connection state are all restored
// intact when the user comes back with [ or ].
type chatState struct {
	// key is the map key this chat was filed under and never changes.
	//
	// It is stored rather than recomputed from the target because
	// ChatTarget.Key prefers the liveChatId, which arrives later: recomputing
	// it after resolution would silently rename a chat that already holds
	// history, a draft, and a scroll position.
	key    string
	target youtube.ChatTarget
	status youtube.ConnectionState

	messages     []youtube.Message
	scrollOffset int
	filters      messageFilterSet

	// searchQuery is the active "/" search and searchInput is whether the
	// input line currently owns the keyboard. Like filters they are per-chat
	// view state only: they decide where the viewport looks, never what is
	// retained.
	searchQuery string
	searchInput bool

	// newBelow counts messages that arrived while the viewer was scrolled
	// away from the bottom. It backs the sticky "N new" indicator and resets
	// the moment the viewer is back at the bottom.
	newBelow int

	// revealQueue animates newly arrived rows. Only the active chat animates;
	// a background chat appends statically so an off-screen flood cannot grow
	// a reveal backlog.
	revealQueue *animation.Queue
	// activeOrder and activeMessages track the messages currently mid-reveal,
	// which are not yet in messages.
	activeOrder    []string
	activeMessages map[string]youtube.Message
	// localEchoes marks IDs yc printed itself so the real message arriving
	// from the poller replaces the echo instead of duplicating it.
	localEchoes map[string]struct{}

	// seenIDs and seenOrder are the bounded ring of message IDs this chat has
	// already filed, and the only defence against a re-delivered backlog.
	//
	// The poller dedupes within one session, but its ring dies with it: ctrl+r
	// and the reconnect ladder both build a fresh transport, which primes with
	// no page token and an empty ring, so YouTube re-sends the whole recent
	// history. Without this the user's own reconnect key printed every message
	// on screen a second time.
	seenIDs    map[string]struct{}
	seenOrder  []string
	seenCursor int

	// moderations retains deletions, bans, and timeouts for the activity
	// column.
	//
	// They are kept here rather than pushed into the column because the
	// column is a projection of retained state and must stay one. They cannot
	// simply be projected out of messages the way every other activity row
	// is: a moderation event is an instruction to take words off the screen,
	// so it deliberately never becomes a chat row, and without this it left
	// no trace anywhere - a ban silently blanked a channel's whole backlog
	// with nothing on screen to say who did it or why.
	moderations []youtube.ModerationEvent

	unread int

	composerText string
	// selected is the browsing cursor j/k/up/down move through history, and
	// what inspect and r act on. replyTo is the reply r actually armed.
	//
	// They are two fields rather than one because sharing one made every
	// press of j silently address the next message the user sent to whoever
	// they happened to scroll past: the composer showed "Replying to", and
	// send prepended "@name" to a line typed for the room. Browsing must not
	// put words in the user's mouth on a public broadcast, so the cursor
	// moves freely and only r arms a reply.
	selected    *composerReplyContext
	replyTo     *composerReplyContext
	inspectOpen bool

	// mentionSelected is the highlighted entry in the @mention completion
	// strip, and mentionDismissed is the one partial handle the user pressed
	// esc on. Dismissal is per word rather than per session so the next
	// mention in the same message still offers completions.
	mentionSelected  int
	mentionDismissed string

	activeSend   *queuedComposerSend
	sendQueue    []queuedComposerSend
	sendState    composerSendState
	sendFeedback string

	// roster is best-effort: YouTube reports no presence, so it is only the
	// authors seen speaking, used for @mention completion and author meta.
	// It is bounded by rosterOrder/rosterCursor, an insertion-ordered ring
	// that evicts the least recently new speaker; mention completion only
	// ever needs recent speakers, and firstSeen degrades to "unknown".
	roster       map[string]youtube.Author
	rosterOrder  []string
	rosterCursor int
	firstSeen    map[string]time.Time
	activePoll   *youtube.PollState

	live         bool
	liveKnown    bool
	liveSince    time.Time
	viewerCount  int
	viewersKnown bool
	// ended marks a chat whose broadcast closed it. The history stays
	// readable: a finished chat is a normal end state, not an error.
	ended bool
}

// chatStateSet is the ordered, keyed collection of open chats.
//
// An empty set is a normal state, not an error: `yc chat` with no --chat lands
// on the empty view and waits for the target picker.
type chatStateSet struct {
	order  []string
	active string
	states map[string]*chatState

	animationConfig animation.Config
	clock           animation.Clock
	scrollbackLimit int

	// placeholder backs the no-chats-open empty state. It is never in order
	// or states, so it cannot be switched to or listed in the sidebar; it
	// exists only so every view and key handler still has a *chatState.
	placeholder *chatState
}

func newChatStateSet(targets []youtube.ChatTarget, animationConfig animation.Config, clock animation.Clock, scrollbackLimit int) *chatStateSet {
	set := &chatStateSet{
		states:          make(map[string]*chatState),
		animationConfig: animationConfig,
		clock:           clock,
		scrollbackLimit: scrollbackLimit,
	}
	for _, target := range targets {
		set.ensure(target)
	}
	if len(set.order) > 0 {
		set.active = set.order[0]
	}
	return set
}

// empty reports whether no chat is open.
func (s *chatStateSet) empty() bool {
	return s == nil || len(s.order) == 0
}

func (s *chatStateSet) activeState() *chatState {
	if s == nil {
		return nil
	}
	if s.empty() {
		return s.emptyState()
	}
	if state, ok := s.states[s.active]; ok {
		return state
	}
	return s.emptyState()
}

// emptyState lazily builds the placeholder used while nothing is open. It is
// built on demand rather than at construction so the common case - chats
// configured up front - allocates nothing extra.
func (s *chatStateSet) emptyState() *chatState {
	if s.placeholder == nil {
		s.placeholder = s.newState(youtube.ChatTarget{})
	}
	return s.placeholder
}

func (s *chatStateSet) activeKey() string {
	if s == nil {
		return ""
	}
	return s.active
}

// activeLabel is the human-facing title of the active chat: the broadcast
// title once resolved, otherwise whatever the user typed.
func (s *chatStateSet) activeLabel() string {
	if s.empty() {
		return ""
	}
	if state := s.activeState(); state != nil {
		return state.target.Label()
	}
	return ""
}

// open makes a chat active, creating it when new. Reopening an already-open
// chat just switches to it, so the picker never creates duplicates.
func (s *chatStateSet) open(target youtube.ChatTarget) bool {
	if s == nil || target.Key() == "" {
		return false
	}
	state := s.ensure(target)
	if state == nil {
		return false
	}
	key := state.key
	if s.active == key {
		state.unread = 0
		return true
	}
	return s.setActive(key)
}

// close removes a chat. When the closed chat was active, focus moves to its
// neighbor so the sidebar selection stays where the eye already is; closing the
// last chat leaves the set empty, which is a supported state.
func (s *chatStateSet) close(key string) bool {
	if s == nil {
		return false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	index := -1
	for i, existing := range s.order {
		if existing == key {
			index = i
			break
		}
	}
	if index < 0 {
		return false
	}
	s.order = append(s.order[:index], s.order[index+1:]...)
	delete(s.states, key)
	if s.active != key {
		return true
	}
	if len(s.order) == 0 {
		s.active = ""
		return true
	}
	if index >= len(s.order) {
		index = len(s.order) - 1
	}
	s.active = s.order[index]
	if state := s.states[s.active]; state != nil {
		state.unread = 0
	}
	return true
}

// ensure returns the state for a target, creating it if needed. An empty
// target with nothing active is the empty state, not a chat to invent.
func (s *chatStateSet) ensure(target youtube.ChatTarget) *chatState {
	if s == nil {
		return nil
	}
	key := target.Key()
	if key == "" {
		if s.active == "" {
			return s.emptyState()
		}
		key = s.active
	}
	if state, ok := s.states[key]; ok {
		state.mergeTarget(target)
		return state
	}
	state := s.newState(target)
	s.states[key] = state
	s.order = append(s.order, key)
	if s.active == "" {
		s.active = key
	}
	return state
}

// ensureKey returns the state already stored under a key without creating one.
func (s *chatStateSet) ensureKey(key string) *chatState {
	if s == nil {
		return nil
	}
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return s.activeState()
	}
	if state, ok := s.states[key]; ok {
		return state
	}
	return nil
}

func (s *chatStateSet) newState(target youtube.ChatTarget) *chatState {
	return &chatState{
		key:    target.Key(),
		target: target,
		status: youtube.ConnectionState{
			Status: youtube.ConnectionDisconnected,
			ChatID: target.LiveChatID,
		},
		revealQueue:    animation.NewQueue(s.animationConfig, s.clock),
		activeMessages: make(map[string]youtube.Message),
		localEchoes:    make(map[string]struct{}),
		roster:         make(map[string]youtube.Author),
		firstSeen:      make(map[string]time.Time),
	}
}

func (s *chatStateSet) setActive(key string) bool {
	if s == nil {
		return false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	state, ok := s.states[key]
	if !ok {
		return false
	}
	if s.active == key {
		state.unread = 0
		return false
	}
	// The chat being left stops being animated the instant it stops being
	// active, so anything still mid-reveal has to land in its history now.
	if outgoing := s.states[s.active]; outgoing != nil {
		outgoing.flushActiveReveals(s.animationConfig, s.clock, s.scrollbackLimit)
	}
	s.active = key
	state.unread = 0
	return true
}

// switchBy moves the active chat by delta, wrapping in both directions.
func (s *chatStateSet) switchBy(delta int) bool {
	if s == nil || len(s.order) <= 1 || delta == 0 {
		return false
	}
	active := 0
	for i, key := range s.order {
		if key == s.active {
			active = i
			break
		}
	}
	next := (active + delta) % len(s.order)
	if next < 0 {
		next += len(s.order)
	}
	return s.setActive(s.order[next])
}

// applyMessage files a message under its chat and reports whether that chat is
// the visible one. A message for a background chat is appended immediately and
// counted as unread; the caller animates only the active chat's arrivals.
func (s *chatStateSet) applyMessage(message youtube.Message) (*chatState, bool) {
	if s == nil {
		return nil, false
	}
	state := s.stateForChatID(message.LiveChatID)
	if state == nil {
		return nil, false
	}
	if !state.markSeen(message.ID) {
		return nil, false
	}
	state.observeAuthor(message)
	if state.key != s.active {
		state.appendMessage(message)
		state.trimScrollback(s.scrollbackLimit)
		state.unread++
		return state, false
	}
	return state, true
}

// stateForChatID resolves the chat a transport event belongs to. An event with
// no liveChatId belongs to the active chat: single-chat transports do not
// bother to stamp one, and inventing a second chat for them would split the
// history in half.
func (s *chatStateSet) stateForChatID(liveChatID string) *chatState {
	if s == nil {
		return nil
	}
	id := strings.TrimSpace(liveChatID)
	if id == "" {
		return s.activeState()
	}
	key := strings.ToLower(id)
	if state, ok := s.states[key]; ok {
		return state
	}
	// The chat may have been opened by video ID and only later learned its
	// liveChatId, in which case the map key is still the video ID.
	for _, existing := range s.states {
		if strings.EqualFold(strings.TrimSpace(existing.target.LiveChatID), id) {
			return existing
		}
	}
	// An identifier that matches nothing. The live adapter stamps its stable
	// routing key on every event precisely so this cannot happen, but a
	// source that does not - a fake, or a future transport - must not have
	// its messages silently filed under whichever chat is on screen. With
	// exactly one chat open there is no ambiguity; with several, refusing is
	// the honest answer, because guessing corrupts two histories at once.
	if len(s.order) == 1 {
		return s.activeState()
	}
	return nil
}

// applyConnectionState records a lifecycle transition. A state with no chat ID
// applies to every open chat, which is how a transport-wide failure reaches the
// whole UI at once.
func (s *chatStateSet) applyConnectionState(state youtube.ConnectionState) *chatState {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(state.ChatID) == "" {
		var first *chatState
		for _, key := range s.order {
			chat := s.states[key]
			if chat == nil {
				continue
			}
			next := state
			next.ChatID = chat.target.LiveChatID
			chat.status = next
			chat.applyTerminalStatus(next)
			if first == nil {
				first = chat
			}
		}
		if first != nil {
			return first
		}
		if placeholder := s.activeState(); placeholder != nil {
			placeholder.status = state
			return placeholder
		}
		return nil
	}
	chat := s.stateForChatID(state.ChatID)
	if chat == nil {
		return nil
	}
	chat.status = state
	chat.applyTerminalStatus(state)
	return chat
}

func (s *chatStateSet) totalUnread() int {
	total := 0
	if s == nil {
		return total
	}
	for _, state := range s.states {
		total += state.unread
	}
	return total
}

// chatKeys returns the open chats in display order.
func (s *chatStateSet) chatKeys() []string {
	if s == nil {
		return nil
	}
	keys := make([]string, 0, len(s.order))
	keys = append(keys, s.order...)
	return keys
}

// chatLabels returns the open chats' display titles, in the same order as
// chatKeys, for the sidebar and tab bar.
func (s *chatStateSet) chatLabels() []string {
	if s == nil {
		return nil
	}
	labels := make([]string, 0, len(s.order))
	for _, key := range s.order {
		if state := s.states[key]; state != nil {
			labels = append(labels, state.target.Label())
		}
	}
	return labels
}

// mergeTarget folds newly resolved identifiers into an open chat without
// changing its map key, so a chat opened by video ID keeps its history once
// resolution supplies the liveChatId and the title.
func (s *chatState) mergeTarget(target youtube.ChatTarget) {
	if s == nil {
		return
	}
	if s.target.Kind == "" || s.target.Kind == youtube.TargetUnknown {
		if target.Kind != "" {
			s.target.Kind = target.Kind
		}
	}
	if strings.TrimSpace(s.target.Raw) == "" {
		s.target.Raw = target.Raw
	}
	if strings.TrimSpace(target.VideoID) != "" {
		s.target.VideoID = target.VideoID
	}
	if strings.TrimSpace(target.ChannelID) != "" {
		s.target.ChannelID = target.ChannelID
	}
	if strings.TrimSpace(target.Handle) != "" {
		s.target.Handle = target.Handle
	}
	if strings.TrimSpace(target.LiveChatID) != "" {
		s.target.LiveChatID = target.LiveChatID
	}
	if strings.TrimSpace(target.Title) != "" {
		s.target.Title = target.Title
	}
}

// appendMessage adds a message to the retained history, replacing the local
// echo it confirms rather than printing the same line twice.
func (s *chatState) appendMessage(message youtube.Message) {
	if s == nil {
		return
	}
	if s.replaceLocalEcho(message) {
		return
	}
	s.messages = append(s.messages, message)
}

// replaceLocalEcho swaps an optimistic local row for the authoritative one.
// The match is by ID when the transport echoes it back, and otherwise by the
// exact text this session sent - a repeat of someone else's identical message
// keeps its own row because only IDs yc issued are candidates.
func (s *chatState) replaceLocalEcho(message youtube.Message) bool {
	if s == nil || len(s.localEchoes) == 0 || message.LocalEcho {
		return false
	}
	for i := len(s.messages) - 1; i >= 0; i-- {
		candidate := s.messages[i]
		if !candidate.LocalEcho {
			continue
		}
		if _, ok := s.localEchoes[candidate.ID]; !ok {
			continue
		}
		if candidate.ID != message.ID && candidate.Text != message.Text {
			continue
		}
		delete(s.localEchoes, candidate.ID)
		s.messages[i] = message
		return true
	}
	return false
}

// trimScrollback drops the oldest messages once the chat exceeds limit.
//
// Retained messages are re-rendered on every repaint, so an untrimmed buffer
// makes frame time grow without bound over a long stream. Trimming from the
// head keeps the newest history, which is the part anyone is reading.
//
// A non-zero scrollOffset is measured from the bottom of the buffer, so
// dropping from the head does not shift what the viewer is looking at and the
// offset is deliberately left alone.
func (s *chatState) trimScrollback(limit int) {
	if s == nil || limit <= 0 || len(s.messages) <= limit {
		return
	}
	drop := len(s.messages) - limit
	// Reslice into a fresh backing array rather than sliding in place: the
	// old array keeps every dropped message alive otherwise, which defeats
	// the point of trimming on a long stream.
	kept := make([]youtube.Message, limit)
	copy(kept, s.messages[drop:])
	s.messages = kept
}

// observeAuthor records a speaker for @mention completion and author meta.
// YouTube reports no presence at all, so this is the only roster there is.
func (s *chatState) observeAuthor(message youtube.Message) {
	if s == nil {
		return
	}
	identity := message.Author.Identity()
	if identity == "" {
		return
	}
	if s.roster == nil {
		s.roster = make(map[string]youtube.Author, rosterRingSize)
	}
	// Guarded independently of roster: a caller may hand us a pre-populated
	// roster map, and the ring still has to exist before it is indexed.
	if s.rosterOrder == nil {
		s.rosterOrder = make([]string, rosterRingSize)
		s.rosterCursor = 0
	}
	if s.firstSeen == nil {
		s.firstSeen = make(map[string]time.Time, rosterRingSize)
	}
	if _, known := s.roster[identity]; !known {
		// New speaker: take the oldest slot. Every other per-chat structure
		// is bounded - scrollback, moderations, the seen-ID ring, the row
		// cache - and without this one a long broadcast with a large
		// distinct-chatter count grows an Author and a timestamp per person
		// for the life of the session.
		if evicted := s.rosterOrder[s.rosterCursor]; evicted != "" {
			delete(s.roster, evicted)
			delete(s.firstSeen, evicted)
		}
		s.rosterOrder[s.rosterCursor] = identity
		s.rosterCursor = (s.rosterCursor + 1) % len(s.rosterOrder)
	}
	s.roster[identity] = message.Author
	if _, ok := s.firstSeen[identity]; !ok && !message.Timestamp.IsZero() {
		s.firstSeen[identity] = message.Timestamp
	}
}

// markSeen records a message ID and reports whether it is new to this chat.
//
// A message with no ID is always new: locally generated system rows carry none
// and there is nothing to key them by. A pending local echo is also let
// through, because the authoritative copy carries the same ID and appendMessage
// has to see it in order to swap the optimistic row for the real one.
func (s *chatState) markSeen(id string) bool {
	if s == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	if s.seenIDs == nil {
		s.seenIDs = make(map[string]struct{}, seenMessageRingSize)
		s.seenOrder = make([]string, seenMessageRingSize)
	}
	if _, echoed := s.localEchoes[id]; !echoed {
		if _, ok := s.seenIDs[id]; ok {
			return false
		}
	}
	if evicted := s.seenOrder[s.seenCursor]; evicted != "" {
		delete(s.seenIDs, evicted)
	}
	s.seenOrder[s.seenCursor] = id
	s.seenCursor = (s.seenCursor + 1) % len(s.seenOrder)
	s.seenIDs[id] = struct{}{}
	return true
}

// flushActiveReveals moves every in-flight reveal straight into history.
//
// Only the active chat animates, and only the active chat's queue is advanced,
// so a chat switched away from mid-reveal would otherwise hold its newest
// messages in activeMessages forever: invisible to the filters, to j/k, and to
// the retained history the user scrolls back through.
func (s *chatState) flushActiveReveals(cfg animation.Config, clock animation.Clock, scrollbackLimit int) {
	if s == nil || len(s.activeOrder) == 0 {
		return
	}
	for _, id := range s.activeOrder {
		message, ok := s.activeMessages[id]
		if !ok {
			continue
		}
		delete(s.activeMessages, id)
		s.appendMessage(message)
	}
	s.activeOrder = nil
	s.activeMessages = make(map[string]youtube.Message)
	s.revealQueue = animation.NewQueue(cfg, clock)
	s.trimScrollback(scrollbackLimit)
}

// firstSeenAt reports when an author was first seen in this chat, for the
// grouped layout's author meta. An unknown author yields the zero time, which
// the renderer omits rather than guessing.
func (s *chatState) firstSeenAt(identity string) time.Time {
	if s == nil || identity == "" {
		return time.Time{}
	}
	return s.firstSeen[identity]
}

// applyTerminalStatus records the end of a chat. A closed live chat keeps its
// history on screen and stops asking to be reconnected, because the broadcast
// ending is a normal outcome rather than a failure.
func (s *chatState) applyTerminalStatus(state youtube.ConnectionState) {
	if s == nil {
		return
	}
	switch state.Status {
	case youtube.ConnectionClosed:
		s.ended = true
		s.live = false
		s.liveKnown = true
	case youtube.ConnectionConnected:
		// A chat that is delivering messages again is not over, whatever it
		// reported earlier. Without this a stream that ends and restarts
		// under the same live chat ID would keep a disabled composer and a
		// frozen viewer count for the rest of the session.
		s.ended = false
		// Whether the broadcast is live is the metrics lookup's verdict, not
		// the transport's. Clearing it means the badge shows nothing until
		// that lookup answers, rather than asserting a stale OFFLINE.
		s.liveKnown = false
	}
}

// markMessagesDeleted redacts every message matching the predicate in place.
//
// The text is not kept anywhere the UI can reach: the point of a deletion is
// that the words leave the screen, on a terminal that is frequently on stream.
// Returns how many rows changed so the caller can report the moderation.
func (s *chatState) markMessagesDeleted(match func(youtube.Message) bool) int {
	if s == nil || match == nil {
		return 0
	}
	count := 0
	for i := range s.messages {
		if s.messages[i].Deleted || !match(s.messages[i]) {
			continue
		}
		s.messages[i].Deleted = true
		s.messages[i].Text = ""
		s.messages[i].Fragments = nil
		count++
	}
	for id, message := range s.activeMessages {
		if message.Deleted || !match(message) {
			continue
		}
		message.Deleted = true
		message.Text = ""
		message.Fragments = nil
		s.activeMessages[id] = message
		count++
	}
	return count
}

// clearHistory drops everything retained for the chat. It is destructive and
// unbounded, which is why the key that reaches it asks for confirmation first.
func (s *chatState) clearHistory(cfg animation.Config, clock animation.Clock) {
	if s == nil {
		return
	}
	s.messages = nil
	s.scrollOffset = 0
	s.activeOrder = nil
	s.activeMessages = make(map[string]youtube.Message)
	s.localEchoes = make(map[string]struct{})
	// The dedupe ring goes with the history. Keeping it would make a clear
	// followed by a reconnect suppress the very backlog the reconnect fetched,
	// leaving the user staring at an empty chat they cannot refill.
	s.seenIDs = nil
	s.seenOrder = nil
	s.seenCursor = 0
	s.moderations = nil
	s.selected = nil
	s.replyTo = nil
	s.revealQueue = animation.NewQueue(cfg, clock)
}

// recordModeration retains one moderation event for the activity column,
// stamping it with arrival time when the source left it unset so the column
// can order it against the messages around it.
//
// The retained slice is bounded by what the column can show, because a
// moderation flood must not grow memory the way a chat flood does not.
func (s *chatState) recordModeration(event youtube.ModerationEvent) {
	if s == nil {
		return
	}
	if event.At.IsZero() {
		event.At = time.Now()
	}
	s.moderations = append(s.moderations, event)
	if len(s.moderations) > maxActivityEntries {
		s.moderations = append(s.moderations[:0:0], s.moderations[len(s.moderations)-maxActivityEntries:]...)
	}
}

// removeActiveReveal drops one in-flight reveal from the ordered list.
func (s *chatState) removeActiveReveal(id string) {
	if s == nil {
		return
	}
	for i, existing := range s.activeOrder {
		if existing == id {
			s.activeOrder = append(s.activeOrder[:i], s.activeOrder[i+1:]...)
			return
		}
	}
}

// drainUnsentComposerSends removes everything still queued behind a failed
// send and returns the drafts to restore, newest last, plus the reply context
// the failed send carried. A failure must not silently discard text the user
// typed and has no other copy of.
func (s *chatState) drainUnsentComposerSends(failed queuedComposerSend) ([]string, *composerReplyContext) {
	if s == nil {
		return nil, nil
	}
	drafts := make([]string, 0, len(s.sendQueue)+1)
	if strings.TrimSpace(failed.Draft) != "" {
		drafts = append(drafts, failed.Draft)
	}
	for _, queued := range s.sendQueue {
		if strings.TrimSpace(queued.Draft) != "" {
			drafts = append(drafts, queued.Draft)
		}
	}
	s.sendQueue = nil
	return drafts, cloneComposerReply(failed.Reply)
}

// restoreComposerText puts drafts back in the composer, ahead of anything the
// user has typed since, joined by a space so nothing is lost.
func (s *chatState) restoreComposerText(drafts ...string) {
	if s == nil || len(drafts) == 0 {
		return
	}
	parts := make([]string, 0, len(drafts)+1)
	parts = append(parts, drafts...)
	if current := strings.TrimSpace(s.composerText); current != "" {
		parts = append(parts, current)
	}
	s.composerText = strings.Join(parts, " ")
}

// configuredTargets turns the configured chat list into targets, dropping
// blanks and duplicates while preserving the order the user wrote them in.
//
// Nothing here resolves a liveChatId: resolution costs quota and belongs to a
// command, so a target starts as the raw string the user supplied and is
// upgraded in place once BroadcastResolver answers.
func configuredTargets(configured []string) []youtube.ChatTarget {
	targets := make([]youtube.ChatTarget, 0, len(configured))
	seen := make(map[string]bool, len(configured))
	for _, raw := range configured {
		target := unresolvedTarget(raw)
		key := target.Key()
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, target)
	}
	return targets
}

// unresolvedTarget wraps raw user input without classifying or resolving it.
//
// Classification lives in youtube.ParseChatTarget and resolution in the
// quota-charged ladder; the shell deliberately does neither, so opening a chat
// is instant and free and the identifiers fill in asynchronously.
func unresolvedTarget(raw string) youtube.ChatTarget {
	return youtube.ChatTarget{Raw: strings.TrimSpace(raw)}
}

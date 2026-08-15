package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// Outbound moderation: the one path in yc that removes somebody else's words
// from a live broadcast.
//
// Three properties are non-negotiable here and shape everything below.
//
//  1. It never fails silently. A key that appears to do nothing is the worst
//     possible outcome for a moderator mid-incident, so every refusal names a
//     reason the user can act on, and every reason reaches the status bar.
//  2. It never reprints what it removed. Redacted text is dropped from the
//     rendered message and survives only inside the rollback record, which no
//     surface reads. A moderation action that put the words back on screen -
//     on a terminal that is frequently being streamed - would defeat itself.
//  3. It confirms before it acts. Deleting a message and banning a viewer are
//     both a single keystroke away from keys used constantly (j, k, r), and
//     neither can be undone from here.
//
// The transport is reached through the nil-able Moderator capability rather
// than through ChatClient, so `yc chat --mock`, the deterministic fake, and a
// key-only read session all run the same shell with the keys disabled and
// explained instead of missing.

// Moderation keys, live on the chat pane's selected message. They are plain
// letters rather than ctrl chords because a moderator reaches for them with the
// same hand that is already on j/k, and because every ctrl chord that terminals
// report reliably is already spent.
const (
	moderationDeleteRune  = 'd'
	moderationTimeoutRune = 't'
	moderationBanRune     = 'b'
)

const (
	// defaultModerationTimeout pre-fills the duration prompt. It matches the
	// timeout YouTube's own moderation menu offers first, so enter alone does
	// the common thing.
	defaultModerationTimeout = 5 * time.Minute
	// maxModerationTimeout bounds what the prompt will accept. Anything
	// longer is a ban in everything but name, and the ban key says so out
	// loud; a timeout measured in weeks is nearly always a typo.
	maxModerationTimeout = 24 * time.Hour
	// moderationRequestTimeout bounds one moderation call. It is generous
	// because the alternative - reporting a failure for a request that in
	// fact succeeded - leaves the user unsure whether to try again.
	moderationRequestTimeout = 30 * time.Second
	// moderationDurationInputLimit caps the duration prompt. It exists so a
	// held key cannot grow an unbounded string in the status bar.
	moderationDurationInputLimit = 12
)

// ErrModerationUnavailable reports that a chat source accepted the Moderator
// interface but has no credential behind it. It is a typed error rather than a
// string so the shell can tell "yc cannot do this" apart from "YouTube refused".
var ErrModerationUnavailable = errors.New("moderation unavailable for this chat source")

// moderationAction is what a pending confirmation will do.
type moderationAction int

const (
	moderationActionNone moderationAction = iota
	// moderationActionDelete removes one message.
	moderationActionDelete
	// moderationActionTimeout is a temporary ban with a duration.
	moderationActionTimeout
	// moderationActionBan is a permanent ban.
	moderationActionBan
)

// verb names an action for the confirmation prompt.
func (a moderationAction) verb() string {
	switch a {
	case moderationActionDelete:
		return "delete"
	case moderationActionTimeout:
		return "time out"
	case moderationActionBan:
		return "permanently ban"
	default:
		return ""
	}
}

// key is the letter that both starts and confirms the action, so the
// confirmation is "press it again" rather than a second thing to learn.
func (a moderationAction) key() rune {
	switch a {
	case moderationActionDelete:
		return moderationDeleteRune
	case moderationActionTimeout:
		return moderationTimeoutRune
	case moderationActionBan:
		return moderationBanRune
	default:
		return 0
	}
}

// moderationStage is how far a request has got.
type moderationStage int

const (
	// moderationStageIdle is the resting state: no prompt, no confirmation.
	moderationStageIdle moderationStage = iota
	// moderationStageDuration is the timeout prompt, which owns the keyboard
	// while it is open.
	moderationStageDuration
	// moderationStageConfirm is armed and waiting for the key again.
	moderationStageConfirm
	// moderationStageInFlight is a request the transport has not answered.
	// A second action cannot start here: two overlapping optimistic
	// redactions share one rollback record and would restore the wrong rows.
	moderationStageInFlight
)

// moderationLevel is how loudly the status bar should say the current line.
type moderationLevel int

const (
	// moderationLevelInfo is a completed action or a disabled explanation.
	moderationLevelInfo moderationLevel = iota
	// moderationLevelWarn is an armed confirmation or an open prompt.
	moderationLevelWarn
	// moderationLevelError is a refusal or a failed request.
	moderationLevelError
)

// moderationRole is what yc knows about the signed-in channel's standing in one
// chat.
//
// It is deliberately four-valued. "Unknown" is not "not a moderator": YouTube
// reports isChatModerator only on authors who have actually spoken, so a
// moderator who has not typed anything is indistinguishable from a viewer until
// they do. Collapsing the two would disable moderation for exactly the person
// who most needs it.
type moderationRole string

const (
	moderationRoleUnknown   moderationRole = ""
	moderationRoleOwner     moderationRole = "owner"
	moderationRoleModerator moderationRole = "moderator"
	moderationRoleViewer    moderationRole = "viewer"
)

// canModerate reports whether the role is permitted to moderate. Unknown counts
// as permitted: see moderationRole for why, and moderationCapability for how
// the uncertainty is disclosed rather than hidden.
func (r moderationRole) canModerate() bool { return r != moderationRoleViewer }

// moderationCapability is the honest answer to "can this session moderate this
// chat right now, and if not, why not".
type moderationCapability struct {
	Available bool
	// Reason is a single line naming what to fix. It never contains
	// credential material, and never contains message text.
	Reason string
	Role   moderationRole
	// Certain is false when yc is acting on an assumption - unknown granted
	// scopes, or an unknown role - and the API is the real authority. The
	// keys stay live, and the status line says so.
	Certain bool
}

// moderationRollbackEntry is one message an optimistic redaction changed, kept
// so a failed request can put it back exactly as it was.
//
// The retained text is the reason this type exists and the reason it is
// unexported and never rendered: the words are held only long enough to learn
// whether YouTube actually removed them.
type moderationRollbackEntry struct {
	id        string
	text      string
	fragments []youtube.MessageFragment
	// active marks a message still mid-reveal, which lives in a different
	// collection than the settled backlog.
	active bool
}

// moderationState is the whole modal state of the moderation keys. It lives on
// shellModel beside pendingClearChat, because like that confirmation it is
// shell-modal rather than per-chat: only one moderation action can be armed at
// a time, in the chat the user is looking at.
type moderationState struct {
	stage  moderationStage
	action moderationAction

	// chatKey, messageID, channelID, and displayName are the target,
	// captured when the action was armed. They are captured rather than
	// re-read at commit time so a message arriving between the two
	// keystrokes cannot move the cursor onto somebody else.
	chatKey     string
	messageID   string
	channelID   string
	displayName string

	duration      time.Duration
	durationInput string

	// feedback is the status-bar line, and level is how it is colored.
	feedback string
	level    moderationLevel

	rollback []moderationRollbackEntry
}

// moderationCompletedMsg is one finished moderation request.
type moderationCompletedMsg struct {
	action      moderationAction
	chatKey     string
	displayName string
	duration    time.Duration
	result      youtube.BanResult
	err         error
}

// --- capability ------------------------------------------------------------

// moderator returns the transport's outbound moderation capability, or nil.
//
// The capability is asserted on the client rather than required by ChatClient,
// which is what lets the mock source, the deterministic fake, and a key-only
// live session drive the identical shell. A client that implements Moderator
// but was built without a credential answers through ModerationCapability, so
// "wired but unusable" is distinguishable from "not wired at all" before a key
// is pressed rather than after a request fails.
func (m shellModel) moderator() Moderator {
	if m.client == nil {
		return nil
	}
	moderator, ok := m.client.(Moderator)
	if !ok {
		return nil
	}
	if reporter, ok := m.client.(ModerationCapability); ok {
		if available, _ := reporter.ModerationAvailable(); !available {
			return nil
		}
	}
	return moderator
}

// moderationCapability answers whether the active chat can be moderated.
//
// The checks run cheapest-first and each one that fails names itself, because
// "moderation unavailable" on its own teaches the user nothing about which of
// four different things to go and fix.
func (m shellModel) moderationCapability() moderationCapability {
	if m.client == nil {
		return moderationCapability{Reason: "no chat source", Certain: true}
	}
	if _, ok := m.client.(Moderator); !ok {
		return moderationCapability{Reason: "this chat source cannot moderate", Certain: true}
	}
	if reporter, ok := m.client.(ModerationCapability); ok {
		if available, reason := reporter.ModerationAvailable(); !available {
			if strings.TrimSpace(reason) == "" {
				reason = ErrModerationUnavailable.Error()
			}
			return moderationCapability{Reason: summarizeDetail(reason), Certain: true}
		}
	}

	state := m.activeChatState()
	if state == nil || strings.TrimSpace(state.key) == "" {
		return moderationCapability{Reason: "no chat open", Certain: true}
	}

	// Granted scopes are only a reason to refuse when yc actually knows them.
	// A token supplied through the environment carries no record of its
	// grant, and refusing on a guess would disable moderation for a perfectly
	// good credential; the API is the authority in that case, and its refusal
	// is already a redacted, user-facing sentence.
	scopes := auth.Scopes(m.identity.Scopes...)
	if len(scopes) > 0 && !auth.HasScope(scopes, auth.ScopeYouTubeForceSSL) {
		return moderationCapability{
			Reason:  "moderation needs the youtube.force-ssl scope; run `yc login` again and approve it",
			Certain: true,
		}
	}

	role := m.moderationRole(state)
	if !role.canModerate() {
		return moderationCapability{
			Reason:  "you are not a moderator of this chat",
			Role:    role,
			Certain: true,
		}
	}

	capability := moderationCapability{Available: true, Role: role, Certain: true}
	switch {
	case len(scopes) == 0:
		capability.Certain = false
		capability.Reason = "granted scopes unknown; YouTube decides"
	case role == moderationRoleUnknown:
		capability.Certain = false
		capability.Reason = "your role in this chat is unknown; YouTube decides"
	}
	return capability
}

// moderationRole works out the signed-in channel's standing in one chat.
//
// Ownership is decided by identity, because the broadcast's channel ID is known
// long before the owner says anything. Moderator standing can only come from
// authorDetails on a message the user actually sent, which is why an unknown
// role is a first-class answer rather than a negative one.
func (m shellModel) moderationRole(state *chatState) moderationRole {
	if state == nil {
		return moderationRoleUnknown
	}
	self := strings.TrimSpace(m.identity.ChannelID)
	if !m.identityKnown || self == "" {
		return moderationRoleUnknown
	}
	if owner := strings.TrimSpace(state.target.ChannelID); owner != "" && strings.EqualFold(owner, self) {
		return moderationRoleOwner
	}
	author, seen := state.roster[self]
	if !seen {
		return moderationRoleUnknown
	}
	switch {
	case author.IsOwner:
		return moderationRoleOwner
	case author.IsModerator:
		return moderationRoleModerator
	}
	return moderationRoleViewer
}

// moderationStatus is the line the status bar draws, and how to color it.
//
// An idle session with a moderating credential says nothing at all: the keys
// are documented in help, and a permanent "moderation: ready" banner would only
// spend width that the quota meter needs.
func (m shellModel) moderationStatus() (string, moderationLevel) {
	if line := strings.TrimSpace(m.moderation.feedback); line != "" {
		return line, m.moderation.level
	}
	return "", moderationLevelInfo
}

// moderationStatusColor picks the palette entry for a moderation status line.
func moderationStatusColor(palette theme.Palette, level moderationLevel) string {
	switch level {
	case moderationLevelWarn:
		return palette.Warning
	case moderationLevelError:
		return palette.Error
	default:
		return palette.Success
	}
}

// --- keys ------------------------------------------------------------------

// handleModerationKey runs the moderation keys and any prompt they opened,
// reporting whether it consumed the key.
//
// While the duration prompt is open it owns the keyboard the way an overlay
// does, so a typed "5m" cannot also toggle a filter. An armed confirmation is
// the opposite: it claims only its own key and esc, and anything else cancels
// and falls through, matching what ctrl+L already does. A confirmation that
// swallowed unrelated keys would leave the user pressing j and wondering why
// the cursor stopped moving.
// It takes a pointer receiver so a cancellation survives even on the path that
// declines the key: an armed ban that the user walked away from by pressing j
// must actually disarm, not disarm in a copy that the caller throws away.
func (m *shellModel) handleModerationKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch m.moderation.stage {
	case moderationStageDuration:
		return m.handleModerationDurationKey(msg)
	case moderationStageConfirm:
		if msg.Type == tea.KeyEsc {
			m.cancelModeration("moderation canceled")
			return nil, true
		}
		if msg.Type == tea.KeyEnter || m.isModerationConfirmKey(msg) {
			return m.commitModeration(), true
		}
		// Any other key abandons the confirmation, so an armed ban cannot sit
		// waiting for an unrelated keystroke to fire it later. It then falls
		// through to the ordinary handling below, which is what lets t while
		// a ban is armed disarm the ban and open the timeout prompt rather
		// than being swallowed as a cancellation.
		m.cancelModeration("")
	case moderationStageInFlight:
		return nil, false
	}

	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return nil, false
	}
	if m.activeTab != tabChat || m.focus != focusChat || m.overlay.open() || m.leaderPending {
		return nil, false
	}
	var action moderationAction
	switch msg.Runes[0] {
	case moderationDeleteRune:
		action = moderationActionDelete
	case moderationTimeoutRune:
		action = moderationActionTimeout
	case moderationBanRune:
		action = moderationActionBan
	default:
		return nil, false
	}
	m.beginModeration(action)
	return nil, true
}

// isModerationConfirmKey reports whether this key repeats the armed action.
func (m shellModel) isModerationConfirmKey(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return false
	}
	return msg.Runes[0] == m.moderation.action.key()
}

// handleModerationDurationKey edits the timeout prompt. The prompt is modal on
// purpose: while it is open every printable key is part of the duration.
func (m *shellModel) handleModerationDurationKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.Type {
	case tea.KeyEsc:
		m.cancelModeration("timeout canceled")
		return nil, true
	case tea.KeyBackspace, tea.KeyCtrlH:
		// Graphemes, not bytes: the field is normally ASCII, but a pasted
		// character that is not must delete as one keystroke rather than
		// leaving half a sequence in the status bar.
		m.moderation.durationInput = trimLastGrapheme(m.moderation.durationInput)
		m.moderation.feedback, m.moderation.level = moderationDurationPrompt(m.moderation)
		return nil, true
	case tea.KeyCtrlU:
		m.moderation.durationInput = ""
		m.moderation.feedback, m.moderation.level = moderationDurationPrompt(m.moderation)
		return nil, true
	case tea.KeyEnter:
		duration, err := parseModerationDuration(m.moderation.durationInput)
		if err != nil {
			m.moderation.feedback = "timeout " + err.Error()
			m.moderation.level = moderationLevelError
			return nil, true
		}
		m.moderation.duration = duration
		m.moderation.stage = moderationStageConfirm
		m.moderation.feedback = moderationConfirmPrompt(m.moderation)
		m.moderation.level = moderationLevelWarn
		return nil, true
	case tea.KeyRunes:
		text := string(msg.Runes)
		if ansi.StringWidth(m.moderation.durationInput+text) > moderationDurationInputLimit {
			return nil, true
		}
		m.moderation.durationInput += text
		m.moderation.feedback, m.moderation.level = moderationDurationPrompt(m.moderation)
		return nil, true
	}
	// Every other key is swallowed rather than passed on: a modal prompt that
	// leaks pgup into the scrollback behind it is a prompt the user cannot
	// trust.
	return nil, true
}

// beginModeration validates a request and arms it, or explains why it cannot
// be armed. It never performs I/O.
func (m *shellModel) beginModeration(action moderationAction) {
	m.moderation = moderationState{}

	capability := m.moderationCapability()
	if !capability.Available {
		m.moderation.feedback = "moderation unavailable: " + capability.Reason
		m.moderation.level = moderationLevelError
		return
	}

	message, ok := m.selectedMessage()
	if !ok {
		m.moderation.feedback = "select a message with j/k first"
		m.moderation.level = moderationLevelError
		return
	}
	state := m.activeChatState()

	name := strings.TrimSpace(message.Author.DisplayName)
	if name == "" {
		name = "that author"
	}
	target := moderationState{
		action:      action,
		chatKey:     m.activeChatKey(),
		messageID:   strings.TrimSpace(message.ID),
		channelID:   strings.TrimSpace(message.Author.ChannelID),
		displayName: name,
	}

	if reason, ok := moderationTargetRefusal(action, message, m.identity.ChannelID, state); !ok {
		m.moderation.feedback = reason
		m.moderation.level = moderationLevelError
		return
	}

	m.moderation = target
	if action == moderationActionTimeout {
		m.moderation.stage = moderationStageDuration
		m.moderation.durationInput = formatModerationDuration(defaultModerationTimeout)
		m.moderation.feedback, m.moderation.level = moderationDurationPrompt(m.moderation)
		return
	}
	m.moderation.stage = moderationStageConfirm
	m.moderation.feedback = moderationConfirmPrompt(m.moderation)
	m.moderation.level = moderationLevelWarn

	// An uncertain capability is disclosed at the moment of arming rather
	// than buried in help: the user is about to spend an irreversible action
	// on an assumption, and should know that is what is happening.
	if !capability.Certain && strings.TrimSpace(capability.Reason) != "" {
		m.moderation.feedback += " (" + capability.Reason + ")"
	}
}

// moderationTargetRefusal rejects targets that cannot be acted on, before any
// confirmation is armed. Each refusal names the specific problem: "moderation
// failed" tells a moderator nothing they can use.
func moderationTargetRefusal(
	action moderationAction,
	message youtube.Message,
	selfChannelID string,
	state *chatState,
) (string, bool) {
	if message.Deleted {
		return "that message is already removed", false
	}
	switch action {
	case moderationActionDelete:
		id := strings.TrimSpace(message.ID)
		if id == "" {
			return "that message has no id to delete", false
		}
		if message.LocalEcho {
			return "waiting for YouTube to confirm that message before it can be deleted", false
		}
	case moderationActionTimeout, moderationActionBan:
		channelID := strings.TrimSpace(message.Author.ChannelID)
		if channelID == "" {
			return "no channel id for that author, so they cannot be banned", false
		}
		if self := strings.TrimSpace(selfChannelID); self != "" && strings.EqualFold(self, channelID) {
			return "that is your own channel", false
		}
		if state == nil || strings.TrimSpace(state.target.LiveChatID) == "" {
			return "this chat is not resolved yet, so bans have nowhere to go", false
		}
	}
	return "", true
}

// moderationDurationPrompt renders the timeout prompt, including the caret, so
// the field reads as an input rather than as a report.
func moderationDurationPrompt(st moderationState) (string, moderationLevel) {
	return "time out " + st.displayName + " for: " + st.durationInput +
		"_ (enter confirms, esc cancels)", moderationLevelWarn
}

// moderationConfirmPrompt renders the armed confirmation. It names the target
// and the key, and never the message text: a confirmation that quoted what it
// was about to remove would put those words back on screen.
func moderationConfirmPrompt(st moderationState) string {
	subject := st.displayName
	switch st.action {
	case moderationActionDelete:
		subject += "'s message"
	case moderationActionTimeout:
		subject += " for " + formatModerationDuration(st.duration)
	}
	return st.action.verb() + " " + subject + "? " +
		string(st.action.key()) + "/enter confirms, esc cancels"
}

// cancelModeration clears a pending action, optionally leaving a note.
func (m *shellModel) cancelModeration(note string) {
	m.moderation = moderationState{feedback: note}
	if note != "" {
		m.moderation.level = moderationLevelInfo
	}
}

// --- dispatch --------------------------------------------------------------

// commitModeration applies the optimistic redaction and dispatches the request.
//
// The local rows change first and the network answers second, because the whole
// point of the keystroke is that the words leave the screen now. The price of
// that is a rollback record, which is the only reason the removed text is kept
// anywhere at all.
func (m *shellModel) commitModeration() tea.Cmd {
	pending := m.moderation
	if pending.action == moderationActionNone {
		// Nothing was armed. Reaching here means a confirmation key arrived
		// without an action behind it, which is a bug rather than a user
		// error, and issuing a request built from a zero value would be the
		// worst possible way to find out.
		m.cancelModeration("")
		return nil
	}
	moderator := m.moderator()
	if moderator == nil {
		capability := m.moderationCapability()
		reason := capability.Reason
		if strings.TrimSpace(reason) == "" {
			reason = ErrModerationUnavailable.Error()
		}
		m.cancelModeration("moderation unavailable: " + reason)
		m.moderation.level = moderationLevelError
		return nil
	}

	state := m.chats.ensureKey(pending.chatKey)
	if state == nil {
		m.cancelModeration("that chat is no longer open")
		m.moderation.level = moderationLevelError
		return nil
	}

	m.moderation.stage = moderationStageInFlight
	m.moderation.level = moderationLevelWarn
	m.moderation.rollback = applyOptimisticModeration(state, pending)
	m.moderation.feedback = moderationInFlightLine(pending)

	liveChatID := strings.TrimSpace(state.target.LiveChatID)
	channelID := pending.channelID
	messageID := pending.messageID
	action := pending.action
	duration := pending.duration
	completed := moderationCompletedMsg{
		action:      action,
		chatKey:     pending.chatKey,
		displayName: pending.displayName,
		duration:    duration,
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), moderationRequestTimeout)
		defer cancel()
		if action == moderationActionDelete {
			completed.err = moderator.DeleteMessage(ctx, messageID)
			return completed
		}
		completed.result, completed.err = moderator.Ban(ctx, youtube.BanRequest{
			LiveChatID: liveChatID,
			ChannelID:  channelID,
			Duration:   duration,
		})
		return completed
	}
}

// moderationInFlightLine is what the status bar says while YouTube is thinking.
func moderationInFlightLine(st moderationState) string {
	switch st.action {
	case moderationActionDelete:
		return "deleting " + st.displayName + "'s message"
	case moderationActionTimeout:
		return "timing out " + st.displayName + " for " + formatModerationDuration(st.duration)
	case moderationActionBan:
		return "banning " + st.displayName
	default:
		return "moderating"
	}
}

// applyOptimisticModeration redacts the rows the action will remove and returns
// what it changed.
//
// A delete removes one message; a ban or a timeout removes everything that
// channel has said, which is what YouTube's own client does and what a
// moderator means by the word.
func applyOptimisticModeration(state *chatState, pending moderationState) []moderationRollbackEntry {
	if state == nil {
		return nil
	}
	switch pending.action {
	case moderationActionDelete:
		id := pending.messageID
		if id == "" {
			return nil
		}
		return redactWithRollback(state, func(message youtube.Message) bool {
			return message.ID == id
		})
	case moderationActionTimeout, moderationActionBan:
		channelID := pending.channelID
		if channelID == "" {
			return nil
		}
		return redactWithRollback(state, func(message youtube.Message) bool {
			return strings.EqualFold(strings.TrimSpace(message.Author.ChannelID), channelID)
		})
	}
	return nil
}

// redactWithRollback blanks every matching message and records what it blanked.
//
// It mirrors chatState.markMessagesDeleted, which discards the text outright.
// The difference is the whole reason this exists: an optimistic redaction is a
// claim about what YouTube is going to do, and a claim that turns out to be
// wrong has to be retractable.
func redactWithRollback(state *chatState, match func(youtube.Message) bool) []moderationRollbackEntry {
	if state == nil || match == nil {
		return nil
	}
	var entries []moderationRollbackEntry
	for i := range state.messages {
		if state.messages[i].Deleted || !match(state.messages[i]) {
			continue
		}
		entries = append(entries, moderationRollbackEntry{
			id:        state.messages[i].ID,
			text:      state.messages[i].Text,
			fragments: state.messages[i].Fragments,
		})
		state.messages[i].Deleted = true
		state.messages[i].Text = ""
		state.messages[i].Fragments = nil
	}
	for id, message := range state.activeMessages {
		if message.Deleted || !match(message) {
			continue
		}
		entries = append(entries, moderationRollbackEntry{
			id:        id,
			text:      message.Text,
			fragments: message.Fragments,
			active:    true,
		})
		message.Deleted = true
		message.Text = ""
		message.Fragments = nil
		state.activeMessages[id] = message
	}
	return entries
}

// rollbackModeration restores everything an optimistic redaction removed.
//
// It runs only when the request failed, which means the message is still live
// on YouTube and still visible to the audience. Leaving it blanked would hand
// the moderator a terminal that disagrees with the broadcast they are watching.
func rollbackModeration(state *chatState, entries []moderationRollbackEntry) int {
	if state == nil {
		return 0
	}
	restored := 0
	for _, entry := range entries {
		if entry.active {
			message, ok := state.activeMessages[entry.id]
			if !ok {
				continue
			}
			message.Deleted = false
			message.Text = entry.text
			message.Fragments = entry.fragments
			state.activeMessages[entry.id] = message
			restored++
			continue
		}
		for i := range state.messages {
			if state.messages[i].ID != entry.id {
				continue
			}
			state.messages[i].Deleted = false
			state.messages[i].Text = entry.text
			state.messages[i].Fragments = entry.fragments
			restored++
			break
		}
	}
	return restored
}

// completeModeration folds a finished request back into the model.
//
// A success is echoed locally as a ModerationEvent because the API does not
// reliably report yc's own actions back: messageDeletedEvent was removed from
// the reference as "not being returned by the API", so without the echo a
// deletion would leave no trace in the activity column at all.
func (m *shellModel) completeModeration(msg moderationCompletedMsg) {
	pending := m.moderation
	m.moderation = moderationState{}

	state := m.chats.ensureKey(msg.chatKey)
	if msg.err != nil {
		if state != nil {
			rollbackModeration(state, pending.rollback)
		}
		m.moderation.feedback = moderationFailureLine(msg)
		m.moderation.level = moderationLevelError
		m.clampScroll()
		return
	}

	if state != nil {
		event := youtube.ModerationEvent{
			LiveChatID:        state.target.LiveChatID,
			TargetDisplayName: msg.displayName,
			At:                m.now(),
		}
		switch msg.action {
		case moderationActionDelete:
			event.Type = youtube.ModerationMessageDeleted
			event.TargetMessageID = pending.messageID
		case moderationActionTimeout:
			event.Type = youtube.ModerationUserTimedOut
			event.TargetChannelID = pending.channelID
			event.Duration = msg.duration
		case moderationActionBan:
			event.Type = youtube.ModerationUserBanned
			event.TargetChannelID = pending.channelID
		}
		if strings.TrimSpace(event.LiveChatID) == "" {
			event.LiveChatID = msg.chatKey
		}
		m.applyModeration(event)
	}
	m.moderation.feedback = moderationSuccessLine(msg)
	m.moderation.level = moderationLevelInfo
	m.clampScroll()
}

// moderationSuccessLine reports a completed action, naming the person and never
// the words.
func moderationSuccessLine(msg moderationCompletedMsg) string {
	switch msg.action {
	case moderationActionDelete:
		return "deleted " + msg.displayName + "'s message"
	case moderationActionTimeout:
		return msg.displayName + " timed out for " + formatModerationDuration(msg.duration)
	case moderationActionBan:
		return msg.displayName + " banned"
	default:
		return "moderation applied"
	}
}

// moderationFailureLine reports a refused action and says the rows came back,
// so the user is not left wondering whether the local view is now a lie.
func moderationFailureLine(msg moderationCompletedMsg) string {
	verb := msg.action.verb()
	if verb == "" {
		verb = "moderate"
	}
	detail := credentialSafeDetail(msg.err)
	if detail == "" {
		detail = "the request was refused"
	}
	return "could not " + verb + " " + msg.displayName + ": " + detail + " (nothing was removed)"
}

// --- durations -------------------------------------------------------------

// parseModerationDuration reads the timeout prompt.
//
// A bare number is seconds, which is what the API takes and what a moderator
// typing "60" in a hurry means. Everything else goes through Go's own duration
// parser so 90s, 10m, and 1h30m all work without a bespoke grammar.
func parseModerationDuration(input string) (time.Duration, error) {
	value := strings.ToLower(strings.TrimSpace(input))
	if value == "" {
		return 0, errors.New("needs a duration, e.g. 60s, 10m, or 1h")
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return validateModerationDuration(time.Duration(seconds) * time.Second)
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, errors.New("duration not understood; try 60s, 10m, or 1h")
	}
	return validateModerationDuration(duration)
}

// validateModerationDuration bounds a parsed timeout at both ends.
func validateModerationDuration(duration time.Duration) (time.Duration, error) {
	switch {
	case duration < time.Second:
		return 0, errors.New("must be at least 1s")
	case duration > maxModerationTimeout:
		return 0, errors.New("cannot exceed 24h; use the ban key for longer")
	}
	// The API takes whole seconds, so a sub-second remainder is a rounding
	// artifact rather than an intent.
	return duration.Truncate(time.Second), nil
}

// formatModerationDuration renders a timeout the way the user typed it, without
// Go's trailing zero units.
func formatModerationDuration(duration time.Duration) string {
	if duration <= 0 {
		return "permanent"
	}
	value := duration.Truncate(time.Second)
	switch {
	case value%time.Hour == 0:
		return strconv.FormatInt(int64(value/time.Hour), 10) + "h"
	case value > time.Hour && value%time.Minute == 0:
		return strconv.FormatInt(int64(value/time.Hour), 10) + "h" +
			strconv.FormatInt(int64(value%time.Hour/time.Minute), 10) + "m"
	case value%time.Minute == 0:
		return strconv.FormatInt(int64(value/time.Minute), 10) + "m"
	default:
		return value.String()
	}
}

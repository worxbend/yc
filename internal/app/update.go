package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rivo/uniseg"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/emoji"
	"github.com/worxbend/yc/internal/render"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// The space leader chord follows the AstroNvim convention: space starts a chord
// outside the composer and the next key picks the action. Anything unbound
// simply cancels, so a stray space never leaves the shell in a mode the user
// has to escape from.
const (
	leaderSidebarRune      = 'e'
	leaderTargetPickerRune = 'c'
	leaderCloseChatRune    = 'x'
	leaderInspectRune      = 'i'
	leaderActivityRune     = 'a'
)

// Update is the whole input and event loop.
//
// The dispatch order inside tea.KeyMsg is load-bearing and is documented case
// by case: quit outranks everything, the splash swallows keys, global toggles
// work from any focus, an open overlay owns the keyboard, and the leader chord
// is consumed before any normal-mode binding so a pending leader can never be
// mistaken for one.
func (m shellModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		if !m.mouseEnabled {
			return m, nil
		}
		return m.handleMouseEvent(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.refreshActiveRevealRows()
		m.clampScroll()
		return m, nil

	case tea.FocusMsg:
		m.terminalFocused = true
		return m, nil

	case tea.BlurMsg:
		m.terminalFocused = false
		return m, nil

	case chatClientMessageMsg:
		return m.handleClientMessage(msg)

	case chatClientConnectionStateMsg:
		return m.handleConnectionState(msg)

	case chatClientModerationMsg:
		if !msg.ok {
			return m, nil
		}
		m.applyModeration(msg.event)
		m.clampScroll()
		return m, m.nextClientModerationCommand()

	case chatClientRoomEventMsg:
		if !msg.ok {
			return m, nil
		}
		m.applyRoomEvent(msg.event)
		return m, m.nextClientRoomEventCommand()

	case chatClientPollMsg:
		if !msg.ok {
			return m, nil
		}
		if state := m.chats.stateForChatID(msg.poll.LiveChatID); state != nil {
			poll := msg.poll
			state.activePoll = &poll
		}
		return m, m.nextClientPollCommand()

	case composerSendCompletedMsg:
		return m.completeComposerSend(msg)

	case reconnectCompletedMsg:
		m.completeReconnect(msg, m.now())
		return m, nil

	case moderationCompletedMsg:
		m.completeModeration(msg)
		return m, nil

	case identityResolvedMsg:
		if msg.err == nil {
			m.identity = msg.identity
			m.identityKnown = true
			if handle := strings.TrimSpace(msg.identity.Handle); handle != "" {
				m.mentionHandle = handle
			} else if name := strings.TrimSpace(msg.identity.DisplayName); name != "" {
				m.mentionHandle = name
			}
		}
		return m, nil

	case targetResolvedMsg:
		return m.applyResolvedTarget(msg)

	case chatMetricsMsg:
		m.applyChatMetrics(msg)
		return m, nil

	case chatMetricsTickMsg:
		m.metricsTickScheduled = false
		return m, batchNonNil(m.refreshChatMetricsCommand(), m.scheduleChatMetricsTick())

	case quotaTickMsg:
		m.quotaTickScheduled = false
		m.refreshQuota()
		return m, m.scheduleQuotaTick()

	case animation.FrameMsg:
		m.frameTickScheduled = false
		m.advanceFrame(msg.At)
		return m, m.scheduleFrameTick()

	case revealTickMsg:
		return m.advanceReveal()
	}
	return m, nil
}

// --- keyboard --------------------------------------------------------------

func (m shellModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Any key other than a second ctrl+L abandons a pending clear, so a stray
	// press cannot arm the confirmation and sit waiting for an unrelated
	// keystroke to trigger it later.
	if m.pendingClearChat && msg.Type != tea.KeyCtrlL {
		m.pendingClearChat = false
	}
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	if m.splashActive() {
		m.splashSkipped = true
		return m, nil
	}
	if msg.Type == tea.KeyRunes && msg.Alt && len(msg.Runes) == 1 {
		if tab, ok := tabForShortcutRune(msg.Runes[0]); ok {
			m.activeTab = tab
			m.clampScroll()
			return m, nil
		}
	}

	// The search input line is modal for the same reason the moderation
	// prompt below is: while it is open every key is query text, so typing a
	// word that happens to contain "d" cannot arm a deletion.
	if model, cmd, handled := m.handleSearchKey(msg); handled {
		return model, cmd
	}

	// Moderation is consumed before the global toggles because its duration
	// prompt is modal: while it is open every key belongs to the field, and a
	// stray ctrl+T that swapped the theme mid-prompt would leave a confirmed
	// ban armed behind a picker. An armed confirmation claims only its own
	// key, esc, and enter, and lets everything else through after cancelling.
	if cmd, handled := m.handleModerationKey(msg); handled {
		return m, cmd
	}

	// Global toggles work from any focus, including from inside an overlay,
	// because they are how the user gets back out of one.
	switch msg.Type {
	case tea.KeyCtrlP:
		m.toggleOverlay(overlayPalette)
		return m, nil
	case tea.KeyCtrlE:
		m.toggleOverlay(overlayEmojiPicker)
		return m, nil
	case tea.KeyCtrlT:
		m.toggleOverlay(overlayThemePicker)
		return m, nil
	}
	if handled := m.handleDisplayToggleKey(msg); handled {
		return m, nil
	}
	if m.overlay.open() {
		return m.handleOverlayKey(msg)
	}

	// The mention completion strip claims tab, up, down, and esc, but only
	// while it is showing something. Claiming them unconditionally would
	// break the bindings those keys carry everywhere else in the composer.
	if model, cmd, handled := m.handleMentionKey(msg); handled {
		return model, cmd
	}

	// The space leader chord is consumed before every other binding. It is
	// only ever armed outside the composer, where space is literal text.
	if m.leaderPending {
		return m.handleLeaderKey(msg)
	}
	if msg.Type == tea.KeySpace && m.focus != focusComposer {
		m.leaderPending = true
		return m, nil
	}
	if m.focus == focusSidebar {
		if model, cmd, handled := m.handleSidebarKey(msg); handled {
			return model, cmd
		}
	}

	switch msg.Type {
	case tea.KeyTab:
		m.cycleFocus()
	case tea.KeyPgUp:
		m.scrollBy(m.chatViewportHeight())
	case tea.KeyPgDown:
		m.scrollBy(-m.chatViewportHeight())
	case tea.KeyHome:
		if m.focus != focusComposer {
			m.activeChatState().scrollOffset = m.maxScrollOffset()
		}
	case tea.KeyEnd:
		if m.focus != focusComposer {
			m.activeChatState().scrollOffset = 0
			m.clampScroll()
		}
	case tea.KeyCtrlD:
		// Half-page scrolling mirrors vim: ctrl+d moves toward the newest
		// messages, ctrl+u back into history. Outside the composer only -
		// inside it ctrl+u keeps its "clear the line" meaning below.
		if m.focus != focusComposer {
			m.scrollBy(-clampMin(m.chatViewportHeight()/2, 1))
		}
	case tea.KeyCtrlL:
		// Clearing discards the whole retained backlog and cannot be undone.
		// It is one keystroke, next to keys used constantly, on a tool that
		// is often running during a live broadcast - so it asks once.
		if m.pendingClearChat {
			m.pendingClearChat = false
			m.clearLocalChat()
			break
		}
		m.pendingClearChat = true
	case tea.KeyCtrlR:
		return m, m.requestReconnect(m.now())
	case tea.KeyBackspace:
		if m.focus == focusComposer {
			m.deleteComposerGrapheme()
		}
	case tea.KeyCtrlU:
		if m.focus == focusComposer {
			m.activeChatState().composerText = ""
			break
		}
		m.scrollBy(clampMin(m.chatViewportHeight()/2, 1))
	case tea.KeyEsc:
		// esc is "leave insert mode" first: from the composer it always
		// returns to the chat view, keeping the draft intact.
		if m.focus == focusComposer {
			m.focus = focusChat
			return m, nil
		}
		state := m.activeChatState()
		if state.inspectOpen {
			state.inspectOpen = false
			m.clampScroll()
			return m, nil
		}
		// An active search is the next-most-recent mode, so esc clears it
		// before touching the reply or the cursor beneath it.
		if m.searchActive() {
			m.clearSearch()
			return m, nil
		}
		// Then unwind one step at a time: cancel the armed reply first and
		// keep the cursor where it was, so changing your mind about replying
		// does not also lose your place in the backlog.
		if state.replyTo != nil {
			state.replyTo = nil
			return m, nil
		}
		state.selected = nil
	case tea.KeyUp:
		if m.focus == focusChat {
			m.selectMessage(-1)
		}
	case tea.KeyDown:
		if m.focus == focusChat {
			m.selectMessage(1)
		}
	case tea.KeyEnter:
		if m.focus == focusComposer {
			return m.queueComposerSend()
		}
	case tea.KeySpace:
		if m.focus == focusComposer {
			m.insertComposerText(" ")
		}
	case tea.KeyRunes:
		return m.handleRuneKey(msg)
	}
	return m, nil
}

func (m shellModel) handleRuneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(msg.Runes) != 1 {
		if m.focus == focusComposer {
			m.insertComposerText(string(msg.Runes))
		}
		return m, nil
	}
	r := msg.Runes[0]

	// "?" toggles help everywhere except in the composer, where it is an
	// ordinary character in the message being typed.
	if m.focus != focusComposer && r == '?' {
		m.helpExpanded = !m.helpExpanded
		m.clampScroll()
		return m, nil
	}
	// Pane resizing works from the chat view and from the sidebar, because
	// the sidebar is the one pane whose own width these keys adjust.
	if m.focus == focusChat || m.focus == focusSidebar {
		switch r {
		case '<':
			m.resizeFocusedPane(-paneResizeStep)
			m.clampScroll()
			return m, nil
		case '>':
			m.resizeFocusedPane(paneResizeStep)
			m.clampScroll()
			return m, nil
		case '=':
			m.sidebarWidthOverride = 0
			m.activityWidthOverride = 0
			m.clampScroll()
			return m, nil
		}
	}
	if m.focus != focusChat {
		if m.focus == focusComposer {
			m.insertComposerText(string(r))
		}
		return m, nil
	}

	switch {
	case r == ']':
		if m.chats.switchBy(1) {
			m.clampScroll()
		}
	case r == '[':
		if m.chats.switchBy(-1) {
			m.clampScroll()
		}
	case r == '0':
		m.activeChatState().filters.reset()
		m.clampScroll()
	case r == 'q':
		return m, tea.Quit
	case r == 'r':
		m.startReplyMode()
	case r == 'K':
		m.toggleInspect()
	case r == 'j':
		m.selectMessage(1)
	case r == 'k':
		m.selectMessage(-1)
	case r == 'g':
		// g and G are vim's jumps: g to the oldest retained row, G back to
		// the live bottom. The offset counts rows hidden below the viewport,
		// so the top is the maximum offset and the bottom is zero.
		m.activeChatState().scrollOffset = m.maxScrollOffset()
	case r == 'G':
		m.activeChatState().scrollOffset = 0
		m.clampScroll()
	case r == '/':
		m.startSearchInput()
	case r == 'n':
		m.jumpSearchMatch(-1)
	case r == 'N':
		m.jumpSearchMatch(1)
	case isInsertRune(r):
		// i/o/a all enter the composer, matching vim's insert keys; the
		// composer appends, so they differ only in muscle memory.
		m.focus = focusComposer
	default:
		if filter, ok := messageFilterForShortcutRune(r); ok {
			state := m.activeChatState()
			state.filters.toggle(filter)
			state.sendFeedback = filterFeedback(state.filters, filter)
			m.clampScroll()
		}
	}
	return m, nil
}

func filterFeedback(set messageFilterSet, filter messageFilter) string {
	label := messageFilterLabel(filter)
	if set.enabled(filter) {
		return "filter " + label + " on"
	}
	return "filter " + label + " off"
}

// isInsertRune reports whether a normal-mode key should move focus to the
// composer. vim's i/a/o all begin inserting text; the composer appends, so they
// are equivalent here.
func isInsertRune(r rune) bool {
	return r == 'i' || r == 'o' || r == 'a'
}

// handleLeaderKey consumes the key following a pending space leader. It always
// clears the pending flag: a chord is exactly two keystrokes, so an unbound
// second key cancels rather than trapping the user in a mode.
func (m shellModel) handleLeaderKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.leaderPending = false
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return m, nil
	}
	switch msg.Runes[0] {
	case leaderSidebarRune:
		m.toggleSidebarVisibility()
		m.clampScroll()
	case leaderTargetPickerRune:
		m.toggleOverlay(overlayTargetPicker)
	case leaderCloseChatRune:
		m.closeActiveChat()
	case leaderInspectRune:
		m.toggleInspect()
	case leaderActivityRune:
		m.toggleActivityVisibility()
		m.clampScroll()
	}
	return m, nil
}

// toggleSidebarVisibility and toggleActivityVisibility flip a pane between
// shown and hidden. The first press always does the visible thing regardless of
// what "auto" had decided, which is what a user pressing a toggle expects.
func (m *shellModel) toggleSidebarVisibility() {
	if m.sidebarVisibility == paneVisibilityShown {
		m.sidebarVisibility = paneVisibilityHidden
		return
	}
	m.sidebarVisibility = paneVisibilityShown
}

func (m *shellModel) toggleActivityVisibility() {
	if m.activityVisibility == paneVisibilityShown {
		m.activityVisibility = paneVisibilityHidden
		return
	}
	m.activityVisibility = paneVisibilityShown
}

// handleDisplayToggleKey applies the four display cycles. Each takes effect
// immediately; persisting the choice is the CLI lane's job, so a failed write
// can never undo what the user just saw happen.
func (m *shellModel) handleDisplayToggleKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyCtrlG:
		m.messageLayout = nextLayoutMode(m.messageLayout)
		m.effectiveConfig.Features.MessageLayout = string(m.messageLayout)
		m.refreshActiveRevealRows()
		return true
	case tea.KeyCtrlB:
		m.badgeMode = nextBadgeMode(m.badgeMode)
		m.effectiveConfig.Features.BadgeMode = string(m.badgeMode)
		m.refreshActiveRevealRows()
		return true
	case tea.KeyCtrlY:
		m.highlightEmoji = !m.highlightEmoji
		m.effectiveConfig.Features.HighlightEmoji = m.highlightEmoji
		m.refreshActiveRevealRows()
		return true
	case tea.KeyCtrlN:
		m.fullUsername = !m.fullUsername
		m.effectiveConfig.Features.FullUsername = m.fullUsername
		m.refreshActiveRevealRows()
		return true
	}
	return false
}

func nextLayoutMode(mode render.LayoutMode) render.LayoutMode {
	switch mode {
	case render.LayoutInline:
		return render.LayoutGrouped
	case render.LayoutGrouped:
		return render.LayoutCompact
	default:
		return render.LayoutInline
	}
}

func nextBadgeMode(mode render.BadgeMode) render.BadgeMode {
	switch mode {
	case render.BadgeModeGlyph:
		return render.BadgeModeText
	case render.BadgeModeText:
		return render.BadgeModeOff
	default:
		return render.BadgeModeGlyph
	}
}

// handleSidebarKey handles the chats sidebar while it has focus. It reports
// whether the key was consumed so anything it does not claim keeps its usual
// meaning.
func (m shellModel) handleSidebarKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.Type {
	case tea.KeyUp:
		m.moveSidebarSelection(-1)
		return m, nil, true
	case tea.KeyDown:
		m.moveSidebarSelection(1)
		return m, nil, true
	case tea.KeyEnter, tea.KeyRight:
		m.activateSidebarSelection()
		return m, nil, true
	case tea.KeyLeft, tea.KeyEsc:
		m.focus = focusChat
		return m, nil, true
	case tea.KeyDelete:
		m.closeSidebarSelection()
		return m, nil, true
	case tea.KeyRunes:
		if len(msg.Runes) != 1 {
			return m, nil, false
		}
		switch msg.Runes[0] {
		case 'j':
			m.moveSidebarSelection(1)
		case 'k':
			m.moveSidebarSelection(-1)
		case 'l':
			m.activateSidebarSelection()
		case 'h':
			m.focus = focusChat
		case 'x', 'd':
			m.closeSidebarSelection()
		default:
			if isInsertRune(msg.Runes[0]) {
				m.focus = focusComposer
				return m, nil, true
			}
			return m, nil, false
		}
		return m, nil, true
	}
	return m, nil, false
}

func (m *shellModel) moveSidebarSelection(delta int) {
	count := m.chatCount()
	if count == 0 {
		m.sidebarSelected = 0
		return
	}
	next := m.sidebarSelected + delta
	if next < 0 {
		next = 0
	}
	if next >= count {
		next = count - 1
	}
	m.sidebarSelected = next
}

func (m *shellModel) activateSidebarSelection() {
	keys := m.chats.chatKeys()
	if m.sidebarSelected < 0 || m.sidebarSelected >= len(keys) {
		return
	}
	if m.chats.setActive(keys[m.sidebarSelected]) {
		m.clampScroll()
	}
	m.focus = focusChat
}

func (m *shellModel) closeSidebarSelection() {
	keys := m.chats.chatKeys()
	if m.sidebarSelected < 0 || m.sidebarSelected >= len(keys) {
		return
	}
	m.closeChat(keys[m.sidebarSelected])
}

// --- overlays --------------------------------------------------------------

// toggleOverlay opens one overlay and closes any other. Overlays are mutually
// exclusive by construction so esc always closes the thing the user can see.
func (m *shellModel) toggleOverlay(kind overlayKind) {
	if m.overlay.kind == kind {
		m.overlay = overlayState{}
		return
	}
	m.overlay = overlayState{kind: kind}
	m.overlay.itemCount = len(m.overlayItems())
	if kind == overlayThemePicker {
		// The theme picker previews live, so it opens on the active theme
		// rather than at the top of an alphabetical list.
		m.overlay.selected = indexOfString(themePickerNames(), m.effectiveConfig.Features.ThemeName)
	}
}

func (m *shellModel) toggleInspect() {
	state := m.activeChatState()
	state.inspectOpen = !state.inspectOpen
	if state.inspectOpen && state.selected == nil {
		m.selectMessage(-1)
	}
	m.clampScroll()
}

// handleOverlayKey drives every docked overlay from one handler: a query, a
// selection, and a commit. Sharing the handler is what keeps the four overlays
// behaving identically under keys a user has already learned.
func (m shellModel) handleOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		if m.overlay.kind == overlayThemePicker {
			// Cancelling a live preview must restore what was on screen when
			// it opened, not leave the last previewed palette applied.
			m.theme = m.effectiveConfig.ResolveTheme()
		}
		m.overlay = overlayState{}
		return m, nil
	case tea.KeyEnter:
		return m.commitOverlay()
	case tea.KeyUp:
		m.moveOverlaySelection(-1)
		return m, nil
	case tea.KeyDown, tea.KeyTab:
		m.moveOverlaySelection(1)
		return m, nil
	case tea.KeyHome:
		m.overlay.selected = 0
		m.previewOverlaySelection()
		return m, nil
	case tea.KeyEnd:
		m.overlay.selected = clampMin(m.overlay.itemCount-1, 0)
		m.previewOverlaySelection()
		return m, nil
	case tea.KeyBackspace:
		m.overlay.query = trimLastGrapheme(m.overlay.query)
		m.refreshOverlayItems()
		return m, nil
	case tea.KeyCtrlU:
		m.overlay.query = ""
		m.refreshOverlayItems()
		return m, nil
	case tea.KeySpace:
		m.overlay.query += " "
		m.refreshOverlayItems()
		return m, nil
	case tea.KeyRunes:
		m.overlay.query += string(msg.Runes)
		m.refreshOverlayItems()
		return m, nil
	}
	return m, nil
}

func (m *shellModel) moveOverlaySelection(delta int) {
	if m.overlay.itemCount <= 0 {
		m.overlay.selected = 0
		return
	}
	next := (m.overlay.selected + delta) % m.overlay.itemCount
	if next < 0 {
		next += m.overlay.itemCount
	}
	m.overlay.selected = next
	m.previewOverlaySelection()
}

func (m *shellModel) refreshOverlayItems() {
	items := m.overlayItems()
	m.overlay.itemCount = len(items)
	if m.overlay.selected >= m.overlay.itemCount {
		m.overlay.selected = clampMin(m.overlay.itemCount-1, 0)
	}
	m.previewOverlaySelection()
}

// previewOverlaySelection applies the effect of moving the selection where the
// overlay has one. The theme picker is the only overlay that previews, because
// a palette can only be judged against the whole terminal.
func (m *shellModel) previewOverlaySelection() {
	if m.overlay.kind != overlayThemePicker {
		return
	}
	items := m.overlayItems()
	if m.overlay.selected < 0 || m.overlay.selected >= len(items) {
		return
	}
	if palette, ok := theme.ResolvePalette(items[m.overlay.selected], m.effectiveConfig.Features.ThemeCustom); ok {
		m.theme = palette
	}
}

// overlayItems is the filtered item list for the open overlay.
//
// It is computed on demand rather than stored so a catalog that grows - a new
// theme preset, an added palette command - cannot leave the model stale, and so
// the view and the key handler can never disagree about what is on screen.
func (m shellModel) overlayItems() []string {
	query := strings.ToLower(strings.TrimSpace(m.overlay.query))
	var candidates []string
	switch m.overlay.kind {
	case overlayPalette:
		candidates = paletteCommandTitles()
	case overlayThemePicker:
		candidates = themePickerNames()
	case overlayTargetPicker:
		candidates = m.targetPickerCandidates()
	case overlayEmojiPicker:
		candidates = emojiPickerCandidates()
	default:
		return nil
	}
	if query == "" {
		return candidates
	}
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.Contains(strings.ToLower(candidate), query) {
			filtered = append(filtered, candidate)
		}
	}
	// A target picker always offers the literal typed value: a chat can be
	// opened by an ID that is in no list yet.
	if m.overlay.kind == overlayTargetPicker && len(filtered) == 0 {
		return []string{strings.TrimSpace(m.overlay.query)}
	}
	return filtered
}

// overlaySelectedItem returns the item enter would commit.
func (m shellModel) overlaySelectedItem() (string, bool) {
	items := m.overlayItems()
	if m.overlay.selected < 0 || m.overlay.selected >= len(items) {
		return "", false
	}
	return items[m.overlay.selected], true
}

func (m shellModel) commitOverlay() (tea.Model, tea.Cmd) {
	item, ok := m.overlaySelectedItem()
	kind := m.overlay.kind
	m.overlay = overlayState{}
	if !ok {
		return m, nil
	}
	switch kind {
	case overlayThemePicker:
		if palette, resolved := theme.ResolvePalette(item, m.effectiveConfig.Features.ThemeCustom); resolved {
			m.theme = palette
			m.effectiveConfig.Features.ThemeName = item
		}
		return m, nil
	case overlayTargetPicker:
		return m.openChat(item)
	case overlayEmojiPicker:
		m.insertComposerText(emojiClusterFromCandidate(item))
		m.focus = focusComposer
		return m, nil
	case overlayPalette:
		return m.runPaletteCommand(item)
	}
	return m, nil
}

// paletteCommand is one entry in the command palette. The shortcut column is
// generated from the keymap table so a command cannot advertise a key the
// keymap no longer has.
type paletteCommand struct {
	title    string
	shortcut string
	run      func(m shellModel) (tea.Model, tea.Cmd)
}

// paletteCommands is the palette's whole vocabulary. Every display key has an
// entry: a command palette exists precisely so a binding is not discoverable
// only by already knowing it.
func paletteCommands() []paletteCommand {
	return []paletteCommand{
		{title: "Open chat", shortcut: "space c", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.toggleOverlay(overlayTargetPicker)
			return m, nil
		}},
		{title: "Close chat", shortcut: "space x", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.closeActiveChat()
			return m, nil
		}},
		{title: "Next chat", shortcut: "[/]", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.chats.switchBy(1)
			m.clampScroll()
			return m, nil
		}},
		{title: "Toggle chats sidebar", shortcut: "space e", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.toggleSidebarVisibility()
			return m, nil
		}},
		{title: "Toggle activity column", shortcut: "space a", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.toggleActivityVisibility()
			return m, nil
		}},
		{title: "Emoji picker", shortcut: "ctrl+e", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.toggleOverlay(overlayEmojiPicker)
			return m, nil
		}},
		{title: "Theme", shortcut: "ctrl+t", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.toggleOverlay(overlayThemePicker)
			return m, nil
		}},
		{title: "Cycle message layout", shortcut: "ctrl+g", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.messageLayout = nextLayoutMode(m.messageLayout)
			m.effectiveConfig.Features.MessageLayout = string(m.messageLayout)
			return m, nil
		}},
		{title: "Cycle badge mode", shortcut: "ctrl+b", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.badgeMode = nextBadgeMode(m.badgeMode)
			m.effectiveConfig.Features.BadgeMode = string(m.badgeMode)
			return m, nil
		}},
		{title: "Toggle emoji highlight", shortcut: "ctrl+y", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.highlightEmoji = !m.highlightEmoji
			m.effectiveConfig.Features.HighlightEmoji = m.highlightEmoji
			return m, nil
		}},
		{title: "Toggle full usernames", shortcut: "ctrl+n", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.fullUsername = !m.fullUsername
			m.effectiveConfig.Features.FullUsername = m.fullUsername
			return m, nil
		}},
		{title: "Reconnect", shortcut: "ctrl+r", run: func(m shellModel) (tea.Model, tea.Cmd) {
			return m, m.requestReconnect(m.now())
		}},
		{title: "Reset filters", shortcut: "0", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.activeChatState().filters.reset()
			m.clampScroll()
			return m, nil
		}},
		{title: "Inspect message", shortcut: "K", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.toggleInspect()
			return m, nil
		}},
		{title: "Search messages", shortcut: "/", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.startSearchInput()
			return m, nil
		}},
		{title: "Help", shortcut: "?", run: func(m shellModel) (tea.Model, tea.Cmd) {
			m.helpExpanded = !m.helpExpanded
			return m, nil
		}},
		{title: "Quit", shortcut: "ctrl+c", run: func(m shellModel) (tea.Model, tea.Cmd) {
			return m, tea.Quit
		}},
	}
}

func paletteCommandTitles() []string {
	commands := paletteCommands()
	titles := make([]string, 0, len(commands))
	for _, command := range commands {
		titles = append(titles, command.title)
	}
	return titles
}

// paletteCommandShortcut is the shortcut column the view draws beside a title.
func paletteCommandShortcut(title string) string {
	for _, command := range paletteCommands() {
		if command.title == title {
			return command.shortcut
		}
	}
	return ""
}

func (m shellModel) runPaletteCommand(title string) (tea.Model, tea.Cmd) {
	for _, command := range paletteCommands() {
		if command.title == title {
			return command.run(m)
		}
	}
	return m, nil
}

// themePickerNames lists every selectable palette, with "custom" last so a
// configured custom palette is always reachable.
func themePickerNames() []string {
	names := theme.PresetNames()
	out := make([]string, 0, len(names)+1)
	out = append(out, names...)
	return append(out, "custom")
}

// targetPickerCandidates offers what the user can plausibly open: the chats
// already open, then the configured defaults. Subscriptions are fetched by the
// CLI lane's collaborator and folded in by the view when available.
func (m shellModel) targetPickerCandidates() []string {
	seen := make(map[string]bool)
	candidates := make([]string, 0, m.chatCount()+len(m.effectiveConfig.DefaultChats)+1)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[strings.ToLower(value)] {
			return
		}
		seen[strings.ToLower(value)] = true
		candidates = append(candidates, value)
	}
	if typed := strings.TrimSpace(m.overlay.query); typed != "" {
		add(typed)
	}
	for _, label := range m.chats.chatLabels() {
		add(label)
	}
	for _, configured := range m.effectiveConfig.DefaultChats {
		add(configured)
	}
	return candidates
}

// emojiPickerCandidates renders the built-in Unicode catalog as searchable
// "cluster name" rows. The picker never needs credentials: YouTube exposes no
// per-message emote metadata, so Unicode is the whole graphical vocabulary.
func emojiPickerCandidates() []string {
	entries := emoji.Catalog()
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Cluster+" "+entry.Name)
	}
	return out
}

func emojiClusterFromCandidate(candidate string) string {
	if cluster, _, ok := strings.Cut(candidate, " "); ok {
		return cluster
	}
	return candidate
}

func indexOfString(values []string, want string) int {
	for i, value := range values {
		if strings.EqualFold(value, want) {
			return i
		}
	}
	return 0
}

// --- mouse -----------------------------------------------------------------

// handleMouseEvent implements the one mouse behavior the model owns: scrolling.
// Hit-testing panes belongs to the view, which is the only layer that knows
// where anything was drawn.
func (m shellModel) handleMouseEvent(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.overlay.open() {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.scrollBy(3)
	case tea.MouseButtonWheelDown:
		m.scrollBy(-3)
	}
	return m, nil
}

// --- focus, scrolling, selection -------------------------------------------

func (m *shellModel) cycleFocus() {
	switch m.focus {
	case focusChat:
		m.focus = focusComposer
	case focusComposer:
		// The sidebar joins the cycle only while chats exist to select, so
		// tab never stops on something invisible.
		if m.chatCount() > 0 && m.sidebarVisibility != paneVisibilityHidden {
			m.syncSidebarSelectionToActive()
			m.focus = focusSidebar
			return
		}
		m.focus = focusChat
	default:
		m.focus = focusChat
	}
}

func (m *shellModel) syncSidebarSelectionToActive() {
	keys := m.chats.chatKeys()
	for i, key := range keys {
		if key == m.chats.activeKey() {
			m.sidebarSelected = i
			return
		}
	}
	m.sidebarSelected = 0
}

// scrollBy moves the viewport. A positive delta scrolls back into history,
// matching the offset's own "rows hidden below the viewport" meaning.
func (m *shellModel) scrollBy(delta int) {
	if delta == 0 {
		delta = 1
	}
	m.activeChatState().scrollOffset += delta
	m.clampScroll()
}

// clampScroll bounds the offset. The view re-clamps against the exact rendered
// row count when it paints; this only stops the offset growing without limit.
func (m *shellModel) clampScroll() {
	state := m.activeChatState()
	if state == nil {
		return
	}
	if maxOffset := m.maxScrollOffset(); state.scrollOffset > maxOffset {
		state.scrollOffset = maxOffset
	}
	if state.scrollOffset < 0 {
		state.scrollOffset = 0
	}
	if state.scrollOffset == 0 {
		// Back at the bottom: everything that arrived while scrolled away is
		// now on screen, so the "N new" indicator has nothing left to say.
		state.newBelow = 0
	}
}

// selectMessage moves the browsing cursor through the visible history. It
// moves the cursor only: arming a reply is r's job, not j/k's.
func (m *shellModel) selectMessage(delta int) {
	messages := m.selectableMessages()
	if len(messages) == 0 {
		return
	}
	state := m.activeChatState()
	current := replyMessageID(state.selected)
	index := -1
	for i, message := range messages {
		if message.ID == current {
			index = i
			break
		}
	}
	switch {
	case index == -1 && delta < 0:
		index = len(messages) - 1
	case index == -1:
		index = 0
	default:
		index += delta
		if index < 0 {
			index = 0
		}
		if index >= len(messages) {
			index = len(messages) - 1
		}
	}
	state.selected = replyContextFromMessage(messages[index])
}

// selectableMessages are the visible messages a reply or inspect can target.
// Deleted rows are excluded: their text is gone, so quoting one would put the
// removed words back on a terminal that is often on stream.
func (m shellModel) selectableMessages() []youtube.Message {
	visible := m.visibleMessages()
	out := make([]youtube.Message, 0, len(visible))
	for _, message := range visible {
		if message.Deleted || strings.TrimSpace(message.ID) == "" {
			continue
		}
		out = append(out, message)
	}
	return out
}

// startReplyMode arms a reply to the browsing cursor, defaulting to the newest
// message when nothing is selected yet. This is the only place a reply is
// armed, so "Replying to" on screen always means the user asked for it.
func (m *shellModel) startReplyMode() {
	state := m.activeChatState()
	if state.selected == nil {
		m.selectMessage(-1)
	}
	if state.selected == nil {
		return
	}
	state.replyTo = cloneComposerReply(state.selected)
	m.focus = focusComposer
}

// resizeFocusedPane widens or narrows the pane the keys currently act on: the
// sidebar while it has focus, the activity column otherwise. Clamping to what
// the terminal can afford is the view's job at layout time.
func (m *shellModel) resizeFocusedPane(delta int) {
	if m.focus == focusSidebar {
		m.sidebarWidthOverride = clampMin(m.sidebarWidth()+delta, 0)
		return
	}
	m.activityWidthOverride = clampMin(m.activityWidth()+delta, 0)
}

// sidebarWidth and activityWidth report the configured override, or the
// responsive default the view will use when none is set.
func (m shellModel) sidebarWidth() int {
	if m.sidebarWidthOverride > 0 {
		return m.sidebarWidthOverride
	}
	if m.width >= 112 {
		return 24
	}
	return 18
}

func (m shellModel) activityWidth() int {
	if m.activityWidthOverride > 0 {
		return m.activityWidthOverride
	}
	if m.width >= 140 {
		return 34
	}
	return 28
}

// --- chats -----------------------------------------------------------------

// openChat opens a chat by whatever the user typed. It is instant and free:
// nothing is classified or resolved here, so the chat appears immediately and
// its identifiers fill in when the quota-charged resolver answers.
func (m shellModel) openChat(raw string) (tea.Model, tea.Cmd) {
	target := unresolvedTarget(raw)
	if target.Key() == "" {
		return m, nil
	}
	m.chats.open(target)
	m.focus = focusChat
	m.syncSidebarSelectionToActive()
	m.clampScroll()

	var cmds []tea.Cmd
	if joiner, ok := m.client.(ChatJoiner); ok {
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resolved, err := joiner.OpenChat(ctx, target)
			return targetResolvedMsg{chatKey: target.Key(), target: resolved, err: err}
		})
	}
	cmds = append(cmds, m.resolveTargetsCommand())
	return m, batchNonNil(cmds...)
}

func (m *shellModel) closeActiveChat() {
	m.closeChat(m.activeChatKey())
}

// closeChat removes a chat from the UI and, when the transport supports it,
// stops polling it so the closed chat stops spending quota.
func (m *shellModel) closeChat(key string) {
	if strings.TrimSpace(key) == "" {
		return
	}
	if joiner, ok := m.client.(ChatJoiner); ok {
		_ = joiner.CloseChat(key)
	}
	if m.chats.close(key) {
		m.syncSidebarSelectionToActive()
		m.clampScroll()
	}
}

func (m shellModel) applyResolvedTarget(msg targetResolvedMsg) (tea.Model, tea.Cmd) {
	state := m.chats.ensureKey(msg.chatKey)
	if state == nil {
		return m, nil
	}
	if msg.err != nil {
		state.status = youtube.ConnectionState{
			Status: youtube.ConnectionFailed,
			ChatID: state.target.LiveChatID,
			Detail: credentialSafeDetail(msg.err),
			Err:    msg.err,
			At:     m.now(),
		}
		return m, nil
	}
	state.mergeTarget(msg.target)
	return m, m.refreshChatMetricsCommand()
}

func (m *shellModel) applyChatMetrics(msg chatMetricsMsg) {
	if msg.err != nil {
		return
	}
	state := m.chats.ensureKey(msg.chatKey)
	if state == nil {
		return
	}
	state.mergeTarget(youtube.ChatTarget{
		VideoID:    msg.broadcast.VideoID,
		ChannelID:  msg.broadcast.ChannelID,
		LiveChatID: msg.broadcast.LiveChatID,
		Title:      msg.broadcast.Title,
	})
	state.live = msg.broadcast.Live
	state.liveKnown = true
	state.liveSince = msg.broadcast.ActualStartTime
	if msg.broadcast.ViewersKnown {
		state.viewerCount = msg.broadcast.ConcurrentViewers
		state.viewersKnown = true
	}
}

func (m *shellModel) clearLocalChat() {
	state := m.activeChatState()
	if state == nil || m.chats == nil {
		return
	}
	state.clearHistory(m.chats.animationConfig, m.chats.clock)
}

// --- inbound events --------------------------------------------------------

func (m shellModel) handleClientMessage(msg chatClientMessageMsg) (tea.Model, tea.Cmd) {
	if !msg.ok {
		// A closed message stream is a disconnect, not a crash: the model
		// records it and keeps every message already on screen.
		m.applyConnectionState(youtube.ConnectionState{
			Status: youtube.ConnectionDisconnected,
			Detail: "chat message stream closed",
			At:     m.now(),
		})
		return m, nil
	}
	var cmds []tea.Cmd
	if cmd := m.enqueueMessage(msg.message); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.maybeNotify(msg.message, m.now()); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if msg.message.Kind == youtube.EventKindChatEnded {
		m.applyConnectionState(youtube.ConnectionState{
			Status: youtube.ConnectionClosed,
			ChatID: msg.message.LiveChatID,
			Detail: "live chat ended",
			At:     m.now(),
		})
	}
	cmds = append(cmds, m.nextClientMessageCommand())
	m.clampScroll()
	return m, batchNonNil(cmds...)
}

func (m shellModel) handleConnectionState(msg chatClientConnectionStateMsg) (tea.Model, tea.Cmd) {
	if !msg.ok {
		if m.activeChatState().status.Status != youtube.ConnectionClosed {
			m.applyConnectionState(youtube.ConnectionState{
				Status: youtube.ConnectionDisconnected,
				Detail: "connection state stream closed",
				At:     m.now(),
			})
		}
		return m, nil
	}
	m.applyConnectionState(msg.state)
	if msg.state.Status == youtube.ConnectionConnected {
		// A session that just came up needs its viewer count now, not at the
		// next two-minute tick: after a reconnect the old figure is the one
		// thing on screen most likely to be wrong.
		return m, batchNonNil(m.nextConnectionStateCommand(), m.refreshChatMetricsCommand())
	}
	return m, m.nextConnectionStateCommand()
}

// applyModeration folds a deletion, ban, or timeout into history already on
// screen.
//
// A moderation event is an instruction to remove text the viewer can see, so it
// is never rendered as another chat line: doing that would put the removed
// words back in front of the audience.
func (m *shellModel) applyModeration(event youtube.ModerationEvent) {
	state := m.chats.stateForChatID(event.LiveChatID)
	if state == nil {
		return
	}
	state.recordModeration(event)
	switch event.Type {
	case youtube.ModerationMessageDeleted, youtube.ModerationTombstone:
		target := strings.TrimSpace(event.TargetMessageID)
		if target == "" {
			return
		}
		state.markMessagesDeleted(func(message youtube.Message) bool {
			return message.ID == target
		})
	case youtube.ModerationUserBanned, youtube.ModerationUserTimedOut:
		// A ban retroactively removes everything that channel said, which is
		// what YouTube's own client shows and what a moderator means by it.
		target := strings.TrimSpace(event.TargetChannelID)
		if target == "" {
			return
		}
		state.markMessagesDeleted(func(message youtube.Message) bool {
			return strings.EqualFold(strings.TrimSpace(message.Author.ChannelID), target)
		})
	}
}

// applyRoomEvent records a chat-wide state change. A chat that ends stays
// readable: it is a normal end state rather than an error.
func (m *shellModel) applyRoomEvent(event youtube.RoomEvent) {
	state := m.chats.stateForChatID(event.LiveChatID)
	if state == nil {
		return
	}
	switch event.Type {
	case youtube.RoomChatEnded:
		m.applyConnectionState(youtube.ConnectionState{
			Status: youtube.ConnectionClosed,
			ChatID: event.LiveChatID,
			Detail: "live chat ended",
			At:     event.At,
		})
	case youtube.RoomStreamOffline:
		state.live = false
		state.liveKnown = true
		state.status.Detail = "stream offline"
	case youtube.RoomSponsorOnlyStarted:
		state.status.Detail = "members-only mode on"
	case youtube.RoomSponsorOnlyEnded:
		state.status.Detail = "members-only mode off"
	}
}

// enqueueMessage files an arriving message and, for the active chat at the
// bottom of the buffer, starts its reveal animation.
//
// A message that arrives while the viewer has scrolled away is appended
// statically: animating it would grow a queue nobody is watching and shift the
// page under the eye.
func (m *shellModel) enqueueMessage(message youtube.Message) tea.Cmd {
	if m.chats == nil {
		return nil
	}
	// The echo bookkeeping is deliberately left alone here. appendMessage runs
	// replaceLocalEcho, which needs the arriving ID to still be registered as
	// one of this session's echoes in order to swap the optimistic row for the
	// authoritative one - and which clears the entry itself once it has. A
	// successful send returns YouTube's own message ID, so the echo is stored
	// under exactly the ID the poller later delivers; dropping it first made
	// the replacement miss and printed every message yc sent twice.
	state, isActive := m.chats.applyMessage(message)
	if state == nil || !isActive {
		return nil
	}
	if !m.animationEnabled() || message.Historical || m.scrolledAway() {
		// The priming page is a backlog of up to 2000 rows. Typing it in one
		// grapheme at a time would take minutes and tell the viewer nothing.
		if m.scrolledAway() && !message.Historical {
			// The viewer is reading history, so the arrival lands off screen.
			// Counting it feeds the sticky "N new" indicator; clampScroll
			// zeroes the count the moment they are back at the bottom.
			state.newBelow++
		}
		state.appendMessage(message)
		state.trimScrollback(m.chats.scrollbackLimit)
		return nil
	}

	revealID := m.nextRevealID(message)
	rows := render.Rows(message, m.revealRenderOptions(message))
	result := state.revealQueue.Enqueue(revealID, rows)
	m.completeReveals(result.Completed)
	if result.Immediate {
		state.appendMessage(message)
		state.trimScrollback(m.chats.scrollbackLimit)
		return nil
	}
	state.activeOrder = append(state.activeOrder, revealID)
	state.activeMessages[revealID] = message
	return m.scheduleRevealTick()
}

func (m *shellModel) nextRevealID(message youtube.Message) string {
	m.nextReveal++
	return fmt.Sprintf("%s#%d", message.ID, m.nextReveal)
}

// revealRenderOptions builds the render options the reveal animation measures
// against. It deliberately mirrors what the view will draw: a reveal rendered
// at a different width would jump when it completes.
func (m shellModel) revealRenderOptions(message youtube.Message) render.Options {
	opts := render.DefaultOptions(m.revealRowWidth())
	opts.Palette = m.theme
	opts.Layout = m.messageLayout
	opts.Badges = m.badgeMode
	opts.FullUsername = m.fullUsername
	opts.HighlightEmoji = m.highlightEmoji
	opts.Assets.ShowAvatars = m.avatarMode != "off"
	opts.Meta = render.AuthorMeta{
		Role:            authorRole(message.Author),
		MemberMonths:    message.Author.MemberMonths,
		MemberLevelName: message.Author.MemberLevelName,
		FirstSeen:       m.activeChatState().firstSeenAt(message.Author.Identity()),
		// Truncated to the minute so a per-frame clock cannot invalidate a
		// cached row every tick.
		Now: m.metricsNow().Truncate(time.Minute),
	}
	return opts
}

// revealRowWidth is the content width a chat row is wrapped to. It matches the
// view's own budget: the pane borders, its padding, and any visible side pane.
func (m shellModel) revealRowWidth() int {
	width := m.width - 4
	if m.sidebarVisibility == paneVisibilityShown || (m.sidebarVisibility == paneVisibilityAuto && m.width >= 72 && m.chatCount() > 1) {
		width -= m.sidebarWidth()
	}
	if m.activityVisibility == paneVisibilityShown || (m.activityVisibility == paneVisibilityAuto && m.width >= 100) {
		width -= m.activityWidth()
	}
	return clampMin(width, render.MinimumRenderWidth)
}

func authorRole(author youtube.Author) string {
	switch {
	case author.IsOwner:
		return "owner"
	case author.IsModerator:
		return "moderator"
	case author.IsMember:
		return "member"
	default:
		return ""
	}
}

// --- animation -------------------------------------------------------------

func (m *shellModel) scheduleFrameTick() tea.Cmd {
	if m.frameTickScheduled {
		return nil
	}
	m.frameTickScheduled = true
	return animation.ScheduleFrame(m.frameInterval())
}

// frameInterval is how often the shared clock ticks.
//
// Animation off drops to the clock-only rate rather than to no tick at all.
// View may read no clock but m.metricsNow, so a stopped ticker does not freeze
// the animation - it freezes the on-air timer, the roster's "seen" ages, and
// every other elapsed figure at zero for the session. See
// animation.ClockOnlyFrameInterval.
func (m shellModel) frameInterval() time.Duration {
	if !m.animationEnabled() {
		return animation.ClockOnlyFrameInterval
	}
	return animation.DefaultFrameInterval
}

func (m *shellModel) scheduleRevealTick() tea.Cmd {
	state := m.activeChatState()
	if m.revealTickScheduled || state == nil || state.revealQueue == nil || state.revealQueue.Len() == 0 {
		return nil
	}
	m.revealTickScheduled = true
	return tea.Tick(revealTickInterval, func(time.Time) tea.Msg { return revealTickMsg{} })
}

// advanceFrame records one repaint. Everything time-derived in the view reads
// what this stores, so View never calls time.Now and stays pure.
func (m *shellModel) advanceFrame(now time.Time) {
	m.lastFrameAt = now
	m.frameTimestamps = append(m.frameTimestamps, now)
	cutoff := now.Add(-time.Second)
	trimmed := m.frameTimestamps[:0]
	for _, ts := range m.frameTimestamps {
		if ts.After(cutoff) {
			trimmed = append(trimmed, ts)
		}
	}
	m.frameTimestamps = trimmed
}

func (m shellModel) advanceReveal() (tea.Model, tea.Cmd) {
	m.revealTickScheduled = false
	state := m.activeChatState()
	if state == nil || state.revealQueue == nil {
		return m, nil
	}
	result := state.revealQueue.Advance()
	m.completeReveals(result.Completed)
	m.clampScroll()
	return m, m.scheduleRevealTick()
}

// completeReveals moves finished reveals into the retained history.
//
// When the viewer has scrolled away, the offset is corrected by the change in
// row count so the page they are reading does not slide.
func (m *shellModel) completeReveals(completed []animation.CompletedReveal) {
	if len(completed) == 0 {
		return
	}
	state := m.activeChatState()
	if state == nil {
		return
	}
	for _, reveal := range completed {
		message, ok := state.activeMessages[reveal.ID]
		if !ok {
			continue
		}
		delete(state.activeMessages, reveal.ID)
		state.removeActiveReveal(reveal.ID)
		state.appendMessage(message)
		state.trimScrollback(m.chats.scrollbackLimit)
	}
}

// refreshActiveRevealRows re-renders in-flight reveals after a resize or a
// display toggle, preserving how far each has revealed so the animation does
// not restart under the user.
func (m *shellModel) refreshActiveRevealRows() {
	state := m.activeChatState()
	if state == nil || state.revealQueue == nil || state.revealQueue.Len() == 0 {
		return
	}
	for _, id := range state.activeOrder {
		message, ok := state.activeMessages[id]
		if !ok {
			continue
		}
		state.revealQueue.ReplaceRows(id, render.Rows(message, m.revealRenderOptions(message)))
	}
}

// --- composer --------------------------------------------------------------

// insertComposerText appends text, stripping control characters and enforcing
// YouTube's 200-character limit.
//
// Control characters are dropped rather than escaped: they have no meaning in a
// chat message and are interpreted by whatever terminal renders it later.
func (m *shellModel) insertComposerText(text string) {
	if text == "" {
		return
	}
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\r' || r == '\n':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			// dropped
		default:
			b.WriteRune(r)
		}
	}

	state := m.activeChatState()
	combined := state.composerText + b.String()
	// The cap is applied on grapheme-cluster boundaries. Cutting at rune 200
	// splits a family emoji or a flag mid-sequence and leaves a dangling
	// zero-width joiner in the draft, which is then what gets sent: the
	// transport's own cap sees 200 runes and passes the broken cluster through.
	if capped := capComposerGraphemes(combined, youtube.MaxChatMessageRunes); capped != combined {
		combined = capped
		state.sendFeedback = fmt.Sprintf("message capped at %d characters", youtube.MaxChatMessageRunes)
	}
	state.composerText = combined
}

// capComposerGraphemes truncates to at most limit runes without ever splitting
// a grapheme cluster. It mirrors the transport's own cap so the draft on screen
// is exactly what will be dispatched.
func capComposerGraphemes(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	graphemes := uniseg.NewGraphemes(value)
	runes := 0
	end := 0
	for graphemes.Next() {
		if runes+len(graphemes.Runes()) > limit {
			return value[:end]
		}
		runes += len(graphemes.Runes())
		_, end = graphemes.Positions()
	}
	return value
}

// deleteComposerGrapheme removes one user-perceived character.
//
// Backspace has to delete what looks like one character, so it walks grapheme
// clusters: a family emoji or a flag is several runes and deleting one of them
// leaves a broken sequence on screen.
func (m *shellModel) deleteComposerGrapheme() {
	state := m.activeChatState()
	if state.composerText == "" {
		return
	}
	state.composerText = trimLastGrapheme(state.composerText)
}

func trimLastGrapheme(value string) string {
	if value == "" {
		return ""
	}
	graphemes := uniseg.NewGraphemes(value)
	last := 0
	offset := 0
	for graphemes.Next() {
		last = offset
		offset += len(graphemes.Str())
	}
	return value[:last]
}

// queueComposerSend validates and enqueues the draft.
//
// A reply is an "@DisplayName " prefix on the text: YouTube live chat has no
// parent-message field, so the reply context is a UI affordance and the wire
// form is plain text.
func (m shellModel) queueComposerSend() (tea.Model, tea.Cmd) {
	state := m.activeChatState()
	draft := strings.TrimSpace(state.composerText)

	// /chats never reaches YouTube: bare it opens the picker, and with a
	// value it opens that chat directly.
	if target, isChatCommand := composerChatCommand(draft); isChatCommand {
		state.composerText = ""
		if target == "" {
			m.toggleOverlay(overlayTargetPicker)
			return m, nil
		}
		return m.openChat(target)
	}
	if m.noChatsOpen() {
		state.sendState = composerSendFailed
		state.sendFeedback = "no chat open: /chats or space c"
		return m, nil
	}
	if draft == "" {
		return m, nil
	}
	if m.client == nil {
		state.sendState = composerSendFailed
		state.sendFeedback = "send unavailable for this chat source"
		return m, nil
	}

	text := draft
	if state.replyTo != nil {
		if name := strings.TrimSpace(state.replyTo.Author); name != "" && !strings.HasPrefix(text, "@") {
			text = "@" + name + " " + text
		}
	}
	// The "@Name " prefix can push a draft that fit over the cap, so the cap is
	// re-applied here - again on cluster boundaries, never by rune.
	text = capComposerGraphemes(text, youtube.MaxChatMessageRunes)

	m.nextSend++
	queued := queuedComposerSend{
		ID:      m.nextSend,
		ChatKey: m.activeChatKey(),
		Text:    text,
		Draft:   draft,
		Reply:   cloneComposerReply(state.replyTo),
	}
	state.sendQueue = append(state.sendQueue, queued)
	state.composerText = ""
	state.replyTo = nil
	state.sendState = composerSendQueued
	state.sendFeedback = "queued"
	return m, m.startNextComposerSend(state)
}

// composerChatCommand recognizes the one composer command yc has. It is the
// only text that never reaches the API.
func composerChatCommand(draft string) (string, bool) {
	trimmed := strings.TrimSpace(draft)
	for _, prefix := range []string{"/chats", "/chat"} {
		if trimmed == prefix {
			return "", true
		}
		if rest, ok := strings.CutPrefix(trimmed, prefix+" "); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

func (m shellModel) completeComposerSend(msg composerSendCompletedMsg) (tea.Model, tea.Cmd) {
	state := m.chatStateForActiveSend(msg.id)
	if state == nil || state.activeSend == nil {
		return m, nil
	}
	sent := *state.activeSend
	state.activeSend = nil

	switch {
	case msg.err != nil:
		state.sendState = composerSendFailed
		state.sendFeedback = "failed: " + credentialSafeDetail(msg.err)
		drafts, reply := state.drainUnsentComposerSends(sent)
		state.restoreComposerText(drafts...)
		state.replyTo = reply
		return m, nil
	case msg.result.RateLimited:
		state.sendState = composerSendRateLimited
		state.sendFeedback = "rate limited: " + sendResultDetail(msg.result)
		drafts, reply := state.drainUnsentComposerSends(sent)
		state.restoreComposerText(drafts...)
		state.replyTo = reply
		return m, nil
	}

	state.sendState = composerSendSucceeded
	state.sendFeedback = sendResultDetail(msg.result)
	m.appendLocalEcho(sent, msg.result, m.now())
	m.clampScroll()
	return m, m.startNextComposerSend(state)
}

// now is the model's clock for state transitions. Update may read the wall
// clock; View may not, which is why this exists here and not on the view side.
func (m shellModel) now() time.Time {
	return time.Now()
}

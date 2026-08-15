package app

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/render"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// This file owns the whole frame: the layout solver, the region composition,
// the chat viewport, the splash, and the empty state. It is also the only place
// that reads shellModel fields on behalf of a widget - every widget below takes
// an explicit state value instead, so each one can be rendered and golden
// tested without a Bubble Tea model in scope.
//
// View is pure. Every time-derived value comes from the last
// animation.FrameMsg via m.metricsNow, never from time.Now, so a frame is
// reproducible in a test and no repaint performs network, filesystem, or
// blocking work of any kind.

const (
	// Empty-state and splash vocabulary. These strings are shared between the
	// animated and the static frames: animation_mode=off changes the motion,
	// never the wording or the geometry.
	noChatHeadline         = "No live chat open."
	emptyStateIndent       = "  "
	emptyStateScannerGlyph = "◆"
	emptyStateScannerWidth = 16

	splashTagline    = "yc // YouTube live chat in your terminal"
	splashStrapline  = "✦  quota-aware, keyboard-first, zero browser chrome  ✦"
	splashProgressW  = 28
	splashTaglineLag = 240 * time.Millisecond

	// messageGroupTint is how far a message group's surface is pulled toward
	// its author's identity color. It is small on purpose: the tint has to
	// stay a background message text remains readable on, while still making
	// one person's block recognizable at a glance.
	messageGroupTint = 0.10
	// messageRailTint is stronger because the rail is a solid glyph rather
	// than a text background, so it can carry far more of the author's color
	// without hurting legibility.
	messageRailTint = 0.65
)

// splashLogo is the block wordmark, dropped for a one-line form on a terminal
// too small to hold it rather than being clipped.
var splashLogo = []string{
	"██╗   ██╗ ██████╗",
	"╚██╗ ██╔╝██╔════╝",
	" ╚████╔╝ ██║     ",
	"  ╚██╔╝  ██║     ",
	"   ██║   ╚██████╗",
	"   ╚═╝    ╚═════╝",
}

// chatRowBlock is one message's contribution to the viewport: its rendered rows
// plus the grouping decisions made around them.
type chatRowBlock struct {
	message    youtube.Message
	rows       []render.Row
	groupIndex int
	// separatorBefore marks the first block of a new author group.
	separatorBefore bool
	// continuesGroup marks a message following another by the same author, so
	// LayoutGrouped can omit the repeated header.
	continuesGroup bool
	animating      bool
	// revealed marks a block whose rows are an in-progress animation frame and
	// must not be re-rendered from the message.
	revealed bool
}

// View renders one complete frame.
func (m shellModel) View() string {
	// The clipboard sequence rides in the frame for the same reason the
	// background override does: only the renderer may write to the terminal.
	background := m.themeBackgroundSequence() + m.clipboardSequence()
	if m.splashActive() {
		return background + m.splashView()
	}
	// The theme picker is a full-screen page, not a docked strip: it replaces
	// the dashboard the same way the splash does, because a palette can only
	// be judged on the whole terminal.
	if m.overlay.kind == overlayThemePicker {
		return background + renderThemePicker(clampMin(m.width, 1), clampMin(m.height, 1), m.themePickerState())
	}

	layout := m.layout()
	regions := make([]string, 0, 8)
	if layout.tabBarHeight > 0 {
		regions = append(regions, m.tabBarLine(layout.width))
	}
	if layout.statusHeight > 0 {
		regions = append(regions, renderStatusBar(layout.width, m.statusBarState()))
	}
	if layout.chatHeight > 0 {
		chat := m.chatView(layout)
		if layout.sidebarWidth > 0 {
			chat = lipgloss.JoinHorizontal(lipgloss.Top, m.sidebarView(layout), chat)
		}
		if layout.activityWidth > 0 {
			chat = lipgloss.JoinHorizontal(lipgloss.Top, chat, m.activityView(layout))
		}
		regions = append(regions, chat)
	}
	if layout.streamInfoHeight > 0 {
		regions = append(regions, m.linesPane("🎛", "Stream Info", m.streamInfoLines(),
			layout.width, layout.streamInfoHeight, layout.streamInfoContentHeight, layout.streamInfoFramed))
	}
	if layout.miscHeight > 0 {
		regions = append(regions, m.linesPane("⟳", "Quota · estimated", m.quotaLedgerLines(),
			layout.width, layout.miscHeight, layout.miscContentHeight, layout.miscFramed))
	}
	if layout.overlayHeight > 0 {
		regions = append(regions, renderListOverlay(layout.width, layout.overlayHeight,
			layout.overlayContentHeight, layout.overlayFramed, m.listOverlayState()))
	}
	if layout.inspectHeight > 0 {
		regions = append(regions, renderInspect(layout.width, layout.inspectHeight,
			layout.inspectContentHeight, layout.inspectFramed, m.inspectState()))
	}
	if layout.composerHeight > 0 {
		regions = append(regions, renderComposer(layout.width, layout.composerHeight, layout.composerFramed, m.composerState()))
	}
	if layout.helpHeight > 0 {
		regions = append(regions, renderHelp(layout.width, layout.helpHeight, m.helpState()))
	}

	rendered := lipgloss.NewStyle().
		Width(layout.width).
		Height(clampMin(m.height, 1)).
		Background(lipgloss.Color(m.canvas())).
		Foreground(lipgloss.Color(m.theme.Foreground)).
		Render(lipgloss.JoinVertical(lipgloss.Left, regions...))
	return background + rendered
}

// --- shared derived state --------------------------------------------------

func (m shellModel) canvas() string { return canvasBackground(m.theme) }

func (m shellModel) animationModeValue() animation.Mode {
	return animation.NormalizeMode(m.animationMode)
}

// gradientPhase is the shared rotation offset for a width-long color cycle.
func (m shellModel) gradientPhase(width int) int {
	return gradientPhase(m.animationModeValue(), m.lastFrameAt, width)
}

// paneFocusPhase is the phase a focused pane's rail and title rotate through.
func (m shellModel) paneFocusPhase() int {
	return m.gradientPhase(paneRailGradientSteps)
}

// anyOverlayOpen reports whether a modal surface covers the chat view.
func (m shellModel) anyOverlayOpen() bool {
	return m.overlay.open() || m.activeChatState().inspectOpen
}

// --- layout ----------------------------------------------------------------

// shellLayout is the solved geometry for one frame. Every region's height and
// framing is decided once, here, so no widget has to guess how much room it
// was given.
type shellLayout struct {
	width int

	tabBarHeight int
	statusHeight int

	chatHeight        int
	chatContentHeight int
	chatWidth         int
	chatFramed        bool

	sidebarWidth          int
	sidebarContentHeight  int
	activityWidth         int
	activityContentHeight int

	// overlay* is whichever docked list overlay is open: the command palette,
	// the target picker, or the emoji picker. They are mutually exclusive in
	// the model, so one set of fields is enough.
	overlayHeight        int
	overlayContentHeight int
	overlayFramed        bool

	inspectHeight        int
	inspectContentHeight int
	inspectFramed        bool

	streamInfoHeight        int
	streamInfoContentHeight int
	streamInfoFramed        bool

	miscHeight        int
	miscContentHeight int
	miscFramed        bool

	composerHeight int
	composerFramed bool
	helpHeight     int
}

const (
	dockedOverlayHeight     = 5
	dockedOverlayTallHeight = 7
	// targetPickerTallHeight is taller than the other overlays because its
	// rows are the thing being chosen from rather than a reminder of a key.
	targetPickerTallHeight   = 9
	dockedOverlayTallAt      = 18
	dockedOverlayMinHeight   = 3
	dockedOverlayMinWidth    = 5
	composerFramedMinWidth   = 8
	composerShortTerminal    = 10
	chatFramedMinHeight      = 3
	chatFramedMinWidth       = 5
	messageGutterWideWidth   = 24
	messageGutterNarrowWidth = 12
)

// layout solves the frame geometry.
//
// The order is the priority order: the tab bar, the status line, and the
// composer are claimed first because they are how the user knows what they are
// looking at and how they act on it; chat takes the remainder; and every
// overlay is capped so it can never leave chat with nothing.
func (m shellModel) layout() shellLayout {
	width := clampMin(m.width, 1)
	height := clampMin(m.height, 1)
	layout := shellLayout{
		width:        width,
		chatWidth:    width,
		tabBarHeight: 1,
		statusHeight: 1,
		helpHeight:   1,
	}
	if height == 1 {
		// One row is only enough for the status line, which is the row that
		// answers "is this thing still connected".
		layout.tabBarHeight = 0
		layout.helpHeight = 0
		return layout
	}

	if m.helpExpanded {
		// The ladder runs as far as there are key groups, so a terminal tall
		// enough to show every section does. Truncating help is a reasonable
		// answer on a short terminal and a poor one on a tall one: a group
		// that never renders is a group nobody can discover.
		switch {
		case height >= 26:
			layout.helpHeight = len(keyGroupOrder)
		case height >= 22:
			layout.helpHeight = 5
		case height >= 18:
			layout.helpHeight = 4
		case height >= 14:
			layout.helpHeight = 3
		case height >= 10:
			layout.helpHeight = 2
		}
	}

	if m.activeTab == tabChat {
		layout.composerHeight = 4
		if m.activeChatState().replyTo != nil {
			layout.composerHeight++
		}
		if height < composerShortTerminal {
			layout.composerHeight = 3
		}
		layout.composerFramed = width >= composerFramedMinWidth
	}

	remaining := height - layout.tabBarHeight - layout.statusHeight - layout.helpHeight - layout.composerHeight
	if remaining < 3 && layout.composerHeight > 3 {
		layout.composerHeight = 3
		remaining = height - layout.tabBarHeight - layout.statusHeight - layout.helpHeight - layout.composerHeight
	}
	if remaining < 1 && layout.helpHeight > 0 {
		layout.helpHeight = 0
		remaining = height - layout.tabBarHeight - layout.statusHeight - layout.composerHeight
	}
	if remaining < 1 && layout.composerHeight > 0 {
		// Everything else has already been reduced; the composer absorbs
		// whatever is left rather than the frame overflowing.
		layout.composerHeight = clampMin(height-layout.tabBarHeight-layout.statusHeight, 0)
		layout.composerFramed = layout.composerHeight >= 3 && width >= composerFramedMinWidth
		remaining = height - layout.tabBarHeight - layout.statusHeight - layout.composerHeight
	}

	// Exactly one docked surface gets height. The list overlays outrank
	// inspect because the user just asked for one; inspect is a standing
	// toggle that can wait a frame.
	switch {
	case m.overlay.open():
		tall := dockedOverlayTallHeight
		if m.overlay.kind == overlayTargetPicker {
			tall = targetPickerTallHeight
		}
		layout.overlayHeight, layout.overlayContentHeight, layout.overlayFramed, remaining =
			dockOverlay(dockedOverlayHeight, tall, width, height, remaining)
	case m.activeChatState().inspectOpen:
		layout.inspectHeight, layout.inspectContentHeight, layout.inspectFramed, remaining =
			dockOverlay(dockedOverlayHeight, dockedOverlayTallHeight, width, height, remaining)
	}

	layout.chatHeight = clampMin(remaining, 0)
	// The activity column is measured first and handed to the sidebar as the
	// competing pane, so the two can never together starve chat.
	layout.activityWidth = activityWidthFor(m.activityVisibility, m.activityWidthOverride, width, layout.chatHeight)
	layout.sidebarWidth = sidebarWidthFor(m.sidebarVisibility, m.sidebarWidthOverride, width, layout.chatHeight, m.chatCount(), layout.activityWidth)
	layout.chatWidth = clampMin(width-layout.sidebarWidth-layout.activityWidth, 1)
	layout.chatFramed = layout.chatHeight >= chatFramedMinHeight && width >= chatFramedMinWidth
	layout.applyChatHeights()

	// Any row left over from the reductions above goes back to chat rather
	// than leaving a gap the canvas has to fill.
	used := layout.tabBarHeight + layout.statusHeight + layout.chatHeight +
		layout.overlayHeight + layout.inspectHeight + layout.composerHeight + layout.helpHeight
	if used < height {
		layout.chatHeight += height - used
		layout.applyChatHeights()
	}

	// The non-chat tabs take the whole chat block, side panes included: they
	// are pages, not a third column.
	switch m.activeTab {
	case tabStreamInfo:
		layout.streamInfoHeight, layout.streamInfoContentHeight, layout.streamInfoFramed =
			layout.chatHeight, layout.chatContentHeight, layout.chatFramed
		layout.clearChatRegion()
	case tabMisc:
		layout.miscHeight, layout.miscContentHeight, layout.miscFramed =
			layout.chatHeight, layout.chatContentHeight, layout.chatFramed
		layout.clearChatRegion()
	}
	return layout
}

// applyChatHeights derives the chat and side-pane content heights from the
// chat block's height, which several paths adjust.
func (l *shellLayout) applyChatHeights() {
	l.chatContentHeight = l.chatHeight
	if l.chatFramed {
		l.chatContentHeight = l.chatHeight - 2
	}
	l.chatContentHeight = clampMin(l.chatContentHeight, 0)
	l.sidebarContentHeight = clampMin(l.chatHeight-2, 0)
	l.activityContentHeight = l.sidebarContentHeight
}

// clearChatRegion hands the chat block's geometry to a full-width page.
func (l *shellLayout) clearChatRegion() {
	l.sidebarWidth = 0
	l.activityWidth = 0
	l.sidebarContentHeight = 0
	l.activityContentHeight = 0
	l.chatWidth = l.width
	l.chatHeight = 0
	l.chatContentHeight = 0
	l.chatFramed = false
}

// dockOverlay sizes one docked surface against the rows chat can spare,
// returning its height, its content height, whether it is framed, and what is
// left. It always leaves at least one row for chat: an overlay that hid the
// thing it operates on would be worse than one that does not open.
func dockOverlay(normal, tall, width, height, remaining int) (int, int, bool, int) {
	if remaining < 4 {
		return 0, 0, false, remaining
	}
	overlay := normal
	if height >= dockedOverlayTallAt {
		overlay = tall
	}
	overlay = min(overlay, remaining-1)
	if overlay < dockedOverlayMinHeight {
		return 0, 0, false, remaining
	}
	framed := width >= dockedOverlayMinWidth
	content := overlay
	if framed {
		content = overlay - 2
	}
	return overlay, content, framed, remaining - overlay
}

// --- tab bar ---------------------------------------------------------------

// tabBarLine renders the one-row tab strip: one label per tab tagged with its
// alt+digit shortcut, the active tab marked, and the signed-in handle plus the
// active chat aligned right when there is room.
func (m shellModel) tabBarLine(width int) string {
	handle, chat := m.tabBarContextParts()
	context := strings.TrimSpace(strings.Join(nonEmpty(handle, chat), "  "))
	tabs := m.tabBarTabs(false)
	if ansi.StringWidth(tabs)+2+ansi.StringWidth(context) > width {
		tabs = m.tabBarTabs(true)
	}
	if ansi.StringWidth(tabs)+2+ansi.StringWidth(context) > width {
		tabs = m.activeTabLabel()
	}

	line := tabs
	if available := width - ansi.StringWidth(tabs); context != "" && available > 0 {
		contextWidth := available
		if available > 2 {
			contextWidth -= 2
		}
		visible := truncateDisplayWidth(context, contextWidth)
		line += strings.Repeat(" ", available-ansi.StringWidth(visible)) + visible
	}
	return gradientBackgroundLine(
		fitLine(line, width), width,
		m.theme.Accent, gradientEndColor(m.theme),
		m.theme.Foreground, m.theme.Background,
		m.gradientPhase(width), true,
	)
}

func (m shellModel) tabBarTabs(compact bool) string {
	parts := make([]string, 0, len(shellTabs))
	for i, entry := range shellTabs {
		marker := " "
		if entry.tab == m.activeTab {
			marker = "*"
		}
		if compact && entry.tab != m.activeTab {
			marker = ""
		}
		label := fmt.Sprintf("%s%d", marker, i+1)
		if !compact {
			label += ":" + entry.label
		}
		parts = append(parts, label)
	}
	return " " + strings.Join(parts, "  ")
}

func (m shellModel) activeTabLabel() string {
	for i, entry := range shellTabs {
		if entry.tab == m.activeTab {
			return fmt.Sprintf(" *%d", i+1)
		}
	}
	return ""
}

func (m shellModel) tabBarContextParts() (string, string) {
	handle := sanitizeContextValue(m.identity.Handle)
	if handle != "" && !strings.HasPrefix(handle, "@") {
		handle = "@" + handle
	}
	return handle, sanitizeContextValue(m.activeChatLabel())
}

// sanitizeContextValue neutralizes control characters in a value that came from
// an API response or a config file, so a hostile display name cannot emit its
// own escape sequences into the tab bar.
//
// Controls become a visible replacement character rather than a space: a name
// that tried to smuggle an escape should look tampered with, not quietly
// shortened. That covers the C1 range too - a raw U+009B is a one-byte CSI on
// some terminals and needs no ESC to introduce it. Bidirectional overrides are
// dropped instead, matching flattenControlRunes: they are zero width, so
// removal cannot disturb the width math, while replacing them would widen the
// name by a cell per control.
func sanitizeContextValue(value string) string {
	// Trimming runs after the mapping: a bidi control dropped from the end of
	// the value can expose fresh trailing whitespace, which a pre-map trim
	// would miss.
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case isBidiOverride(r):
			return -1
		case unicode.IsControl(r):
			return '�'
		default:
			return r
		}
	}, value))
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

// --- chat viewport ---------------------------------------------------------

// chatView draws the message viewport.
func (m shellModel) chatView(layout shellLayout) string {
	rows := m.visibleChatRows(layout)
	for len(rows) < layout.chatContentHeight {
		rows = append(rows, "")
	}
	content := strings.Join(rows, "\n")
	if !layout.chatFramed {
		return backgroundStyledLine(fitBlock(content, layout.chatWidth, layout.chatHeight), m.theme.Background)
	}

	title := "Chat"
	if !m.noChatsOpen() {
		title = "Chat · " + m.activeChatLabel() + " · " + m.viewerLabel()
	}
	return renderPane(paneSpec{
		palette: m.theme,
		icon:    "💬",
		title:   title,
		content: content,
		width:   layout.chatWidth,
		// The chat frame and title are deliberately never animated, even when
		// the pane has focus. Everything else in the terminal may shimmer; the
		// surface people read for hours should not.
		contentHeight: layout.chatContentHeight,
		padding:       1,
		accent:        m.theme.Accent,
	})
}

// viewerLabel renders the concurrent viewer count. YouTube omits it entirely
// when the broadcaster hides it and after a stream ends, so an unknown count
// draws as an em dash rather than as zero, which would be a claim yc cannot
// support.
func (m shellModel) viewerLabel() string {
	state := m.activeChatState()
	if state == nil || !state.viewersKnown {
		return "👥 —"
	}
	return "👥 " + formatUnits(state.viewerCount)
}

// visibleChatRows styles exactly the rows the viewport will draw.
//
// Styling is not free and only a screenful is ever shown, so this asks for just
// the window it will draw rather than styling the whole backlog and slicing
// afterwards.
func (m shellModel) visibleChatRows(layout shellLayout) []string {
	height := layout.chatContentHeight
	if height <= 0 {
		return nil
	}
	state := m.activeChatState()
	rowWidth := m.chatRowWidth(layout)
	if m.noChatsOpen() {
		return visibleRows(m.emptyStateRows(rowWidth), height, state.scrollOffset)
	}

	blocks := m.chatRowBlocks(layout)
	total := chatRowBlockCount(blocks)
	if total == 0 && state.filters.active() {
		return []string{backgroundStyledLine(m.emptyFilterRow(rowWidth), m.theme.Background)}
	}
	if total <= height {
		return m.styleChatRowWindow(blocks, rowWidth, 0, -1)
	}
	// scrollOffset counts rows hidden below the viewport, so the window ends
	// that far from the bottom.
	offset := min(clampMin(state.scrollOffset, 0), total-height)
	rows := m.styleChatRowWindow(blocks, rowWidth, clampMin(total-offset-height, 0), height)
	// While the viewer reads history, arrivals land below the viewport where
	// they are invisible. The sticky indicator borrows the bottom row so
	// off-screen traffic is never silent; clampScroll clears the count the
	// moment the viewer is back at the bottom.
	if offset > 0 && state.newBelow > 0 && len(rows) > 0 {
		rows[len(rows)-1] = m.newMessagesIndicatorRow(rowWidth, state.newBelow)
	}
	return rows
}

// newMessagesIndicatorRow is the sticky "N new" strip drawn over the bottom
// viewport row while the viewer is scrolled away and messages keep arriving.
func (m shellModel) newMessagesIndicatorRow(rowWidth, count int) string {
	label := fmt.Sprintf(" ↓ %d new ", count)
	if count == 1 {
		label = " ↓ 1 new "
	}
	hint := "G to jump to newest "
	if ansi.StringWidth(label)+len(hint) <= rowWidth {
		label += "· " + hint
	}
	foreground := theme.ContrastCorrectedForeground(m.theme.Foreground, m.theme.Accent, canvasBackground(m.theme))
	return lipgloss.NewStyle().
		Width(clampMin(rowWidth, 1)).
		Background(lipgloss.Color(m.theme.Accent)).
		Foreground(lipgloss.Color(foreground)).
		Bold(true).
		Render(truncateDisplayWidth(label, clampMin(rowWidth, 1)))
}

// chatRowCount reports how many rows the viewport would produce without paying
// to style them. Scroll clamping needs only the count.
func (m shellModel) chatRowCount(layout shellLayout) int {
	if m.noChatsOpen() {
		return len(m.emptyStateRows(m.chatRowWidth(layout)))
	}
	blocks := m.chatRowBlocks(layout)
	count := chatRowBlockCount(blocks)
	if count == 0 && m.activeChatState().filters.active() {
		return 1
	}
	return count
}

// chatRowBlocks renders the visible backlog into grouped blocks.
//
// Grouping runs before rendering because LayoutGrouped needs to know whether a
// message continues the previous author's block, and it runs after filtering so
// the groups reflect what is on screen rather than hidden history.
func (m shellModel) chatRowBlocks(layout shellLayout) []chatRowBlock {
	contentWidth := m.chatMessageContentWidth(layout)
	messages := m.visibleMessages()
	order, frames := m.revealFrames()

	// The block slice is rebuilt on every repaint and never outlives the
	// frame, so it reuses one package-level scratch buffer instead of
	// allocating a backlog-sized slice per frame. Reuse is safe for the
	// same reason sharedRowCache is: Update and View run on one goroutine,
	// and no caller retains the returned slice past the frame it styled.
	sharedChatRowBlocks = sharedChatRowBlocks[:0]
	blocks := sharedChatRowBlocks
	for _, message := range messages {
		blocks = append(blocks, chatRowBlock{message: message})
	}
	for _, id := range order {
		message, ok := m.revealMessage(id)
		if !ok || !m.activeChatState().filters.matches(message, m.mentionHandle) {
			continue
		}
		blocks = append(blocks, chatRowBlock{
			message:   message,
			rows:      frames[id],
			animating: true,
			revealed:  true,
		})
	}
	assignChatAuthorGroups(blocks)

	cache := m.rowCache()
	params := m.chatRenderParams(contentWidth)
	for index := range blocks {
		if blocks[index].revealed {
			continue
		}
		message := blocks[index].message
		continuesGroup := blocks[index].continuesGroup
		meta := m.authorMeta(message)
		blocks[index].rows = cache.rows(message, continuesGroup, meta, params, func() []render.Row {
			opts := m.renderOptions(contentWidth)
			opts.ContinuesGroup = continuesGroup
			opts.Meta = meta
			return render.Rows(message, opts)
		})
	}
	sharedChatRowBlocks = blocks
	return blocks
}

// sharedChatRowBlocks is the frame-scoped scratch buffer chatRowBlocks fills.
// Its capacity settles at one backlog's worth of blocks and stays there.
var sharedChatRowBlocks []chatRowBlock

// sharedRowCache is the per-process memo for rendered chat rows.
//
// shellModel is copied by value on every Update, so a cache held as a field
// would be discarded on each keystroke unless it were a pointer; keeping the
// pointer here rather than on the model means the state lane does not have to
// carry a view-only field. Bubble Tea runs Update and View on one goroutine, so
// unsynchronized sharing is safe.
var sharedRowCache = newChatRowCache()

func (m shellModel) rowCache() *chatRowCache { return sharedRowCache }

// authorMeta resolves the per-author context rendered beside a username.
//
// Every field is optional and an unknown one is omitted rather than guessed: an
// absent membership tenure means yc has not seen a milestone event for this
// author, not that they are not a member.
func (m shellModel) authorMeta(message youtube.Message) render.AuthorMeta {
	state := m.activeChatState()
	identity := message.Author.Identity()
	meta := render.AuthorMeta{
		Role:            authorRole(message.Author),
		MemberMonths:    message.Author.MemberMonths,
		MemberLevelName: message.Author.MemberLevelName,
		// Truncating to the minute is what stops a 10fps frame clock
		// invalidating every cached row on every tick.
		Now: m.metricsNow().Truncate(time.Minute),
	}
	if state != nil {
		meta.FirstSeen = state.firstSeenAt(identity)
	}
	return meta
}

func (m shellModel) renderOptions(width int) render.Options {
	opts := render.DefaultOptions(width)
	opts.Palette = m.theme
	opts.Layout = m.messageLayout
	opts.Badges = m.badgeMode
	opts.HighlightEmoji = m.highlightEmoji
	opts.FullUsername = m.fullUsername
	opts.Assets.ShowAvatars = !strings.EqualFold(strings.TrimSpace(m.avatarMode), "off")
	return opts
}

// chatRenderParams mirrors the non-message inputs of renderOptions so the row
// cache can detect a change that invalidates every cached message.
func (m shellModel) chatRenderParams(width int) chatRenderParams {
	opts := m.renderOptions(width)
	return chatRenderParams{
		width:        opts.Width,
		layout:       opts.Layout,
		badges:       opts.Badges,
		highlight:    opts.HighlightEmoji,
		fullUsername: opts.FullUsername,
		showAvatars:  opts.Assets.ShowAvatars,
		palette:      opts.Palette,
	}
}

// assignChatAuthorGroups joins adjacent messages from the same author into one
// visual block.
func assignChatAuthorGroups(blocks []chatRowBlock) {
	previousKey := ""
	groupIndex := -1
	for index := range blocks {
		key := chatAuthorGroupKey(blocks[index].message, index)
		if index == 0 || key != previousKey {
			groupIndex++
			blocks[index].separatorBefore = index > 0
		} else {
			blocks[index].continuesGroup = true
		}
		blocks[index].groupIndex = groupIndex
		previousKey = key
	}
}

// chatAuthorGroupKey identifies an author for grouping.
//
// Authorless events fall back to a per-block identity rather than to a shared
// empty key: two unrelated Super Chats, or two system notices, must not merge
// into one block just because neither carries an author.
func chatAuthorGroupKey(message youtube.Message, blockIndex int) string {
	if identity := strings.TrimSpace(message.Author.Identity()); identity != "" {
		return "author:" + strings.ToLower(identity)
	}
	if id := strings.TrimSpace(message.ID); id != "" {
		return "message:" + strings.ToLower(id)
	}
	return fmt.Sprintf("anonymous:%s:%d", strings.ToLower(string(message.Kind)), blockIndex)
}

func chatRowBlockCount(blocks []chatRowBlock) int {
	total := 0
	for _, block := range blocks {
		if block.separatorBefore {
			total++
		}
		total += chatRowBlockRowCount(block)
	}
	return total
}

// chatRowBlockRowCount counts a block with no rendered rows as one row: an
// empty render is still a message that happened, and silently occupying zero
// rows would desynchronize the scroll math from what is on screen.
func chatRowBlockRowCount(block chatRowBlock) int {
	if len(block.rows) == 0 {
		return 1
	}
	return len(block.rows)
}

// styleChatRowWindow converts blocks into styled terminal rows, producing only
// the rows in [start, start+count). A negative count means "to the end".
func (m shellModel) styleChatRowWindow(blocks []chatRowBlock, rowWidth, start, count int) []string {
	start = clampMin(start, 0)
	capacity := count
	if capacity < 0 {
		capacity = chatRowBlockCount(blocks)
	}
	rows := make([]string, 0, clampMin(capacity, 0))
	index := 0
	// want reports whether the row at the current global index falls inside
	// the requested window, and stops the walk once it is past the end.
	want := func() (keep, done bool) {
		switch {
		case index < start:
			return false, false
		case count >= 0 && len(rows) >= count:
			return false, true
		default:
			return true, false
		}
	}

	selectedID := replyMessageID(m.activeChatState().selected)
	searchQuery := strings.TrimSpace(m.activeChatState().searchQuery)
	for _, block := range blocks {
		selected := selectedID != "" && block.message.ID == selectedID
		matched := searchQuery != "" && !block.message.Deleted &&
			messageMatchesSearch(block.message, searchQuery)
		if block.separatorBefore {
			keep, done := want()
			if done {
				return rows
			}
			if keep {
				rows = append(rows, m.messageGroupSeparator(rowWidth))
			}
			index++
		}
		if len(block.rows) == 0 {
			keep, done := want()
			if done {
				return rows
			}
			if keep {
				rows = append(rows, m.messageRowString(block, 0, render.Row{}, rowWidth, selected, matched))
			}
			index++
			continue
		}
		for rowIndex, row := range block.rows {
			keep, done := want()
			if done {
				return rows
			}
			if keep {
				rows = append(rows, m.messageRowString(block, rowIndex, row, rowWidth, selected, matched))
			}
			index++
		}
	}
	return rows
}

// messageRowString draws one chat row: the group gutter rail, the event glyph
// on the group's first row, and the message content on the group's surface.
func (m shellModel) messageRowString(block chatRowBlock, rowIndex int, row render.Row, rowWidth int, selected, searchMatched bool) string {
	gutterWidth := messageGutterWidth(rowWidth)
	background := m.messageGroupBackground(block)
	content := terminalRowString(row, clampMin(rowWidth-gutterWidth, 1), background)
	if gutterWidth == 0 {
		return content
	}

	railColor := m.messageRailColor(block)
	// The cursor thickens the rail rather than only recoloring it, so j/k is
	// legible on a monochrome palette and to anyone who cannot separate the
	// author tints. The heavy glyph is one cell wide, like the light one, so
	// selecting a message shifts nothing.
	railGlyph := "│ "
	if searchMatched {
		// A search match thickens the rail in the warning role, so every
		// match is scannable down the gutter while the accent stays reserved
		// for the one row the cursor is on.
		railGlyph, railColor = "┃ ", m.theme.Warning
	}
	if selected {
		railGlyph, railColor = "┃ ", m.theme.Accent
	}
	rail := lipgloss.NewStyle().
		Foreground(lipgloss.Color(railColor)).
		Background(lipgloss.Color(background)).
		Bold(block.animating || selected || searchMatched).
		Render(railGlyph)
	if gutterWidth == 2 {
		return rail + content
	}

	// The glyph marks a row that opens a message as rendered, which is not
	// the same as a row that opens a message. Only LayoutGrouped folds a
	// repeat author into the block above it, so only there does a continuing
	// message lack a header - keying off the message alone stamped a mark
	// halfway down a grouped block, beside a body line and out of line with
	// the row above. Inline and compact repeat the header, so they repeat the
	// glyph with it.
	icon, iconColor := "  ", m.theme.Muted
	if rowIndex == 0 && (!block.continuesGroup || m.messageLayout != render.LayoutGrouped) {
		glyph, color := messageKindGlyph(block.message, m.theme)
		icon, iconColor = glyph+" ", color
	}
	if block.animating {
		iconColor = railColor
	}
	return rail + lipgloss.NewStyle().
		Foreground(lipgloss.Color(iconColor)).
		Background(lipgloss.Color(background)).
		Bold(block.animating).
		Render(icon) + content
}

// messageKindGlyph is the gutter mark for a message's event class, paired with
// the palette role that colors it.
//
// It shares the activity column's vocabulary on purpose: the same event shows
// up in both places, so one symbol per class means the eye learns it once. An
// envelope on a $500 Super Chat, on a new membership, or on a row a ban just
// emptied reads as "somebody typed a sentence", which is the one thing those
// rows are not. Every glyph is display width one, so the content column starts
// at the same cell whatever arrived.
func messageKindGlyph(message youtube.Message, palette theme.Palette) (string, string) {
	if message.Deleted {
		return "⊘", palette.Error
	}
	switch message.Type {
	case youtube.MessageTypePaid:
		return "◈", palette.Warning
	case youtube.MessageTypeMembership:
		switch message.Kind {
		case youtube.EventKindMembershipGifting, youtube.EventKindGiftMembershipReceived:
			return "♥", palette.Success
		}
		return "★", palette.Accent
	case youtube.MessageTypeNotice, youtube.MessageTypeSystem:
		return "●", palette.Success
	case youtube.MessageTypeUnknown:
		return "·", palette.Muted
	default:
		return "✉", palette.Muted
	}
}

// messageGroupBackground returns the surface a group is drawn on: the
// alternating base/surface stripe, tinted toward the author's stable identity
// color so consecutive messages from one person read as one block.
func (m shellModel) messageGroupBackground(block chatRowBlock) string {
	background := m.theme.Background
	if block.groupIndex%2 == 1 {
		background = m.theme.Surface
	}
	color := m.messageAuthorColor(block.message)
	if color == "" {
		return background
	}
	return theme.Mix(background, color, messageGroupTint)
}

// messageRailColor returns a group's left rail color. The rail carries the
// author's identity color so one person is recognizable down the gutter;
// animating messages keep the shared-clock shimmer that marks a row as still
// arriving.
func (m shellModel) messageRailColor(block chatRowBlock) string {
	if block.animating {
		colors := theme.SeamlessGradient(m.theme.Accent, gradientEndColor(m.theme), 8)
		if len(colors) > 0 {
			return colors[(block.groupIndex+m.gradientPhase(len(colors)))%len(colors)]
		}
	}
	if color := m.messageAuthorColor(block.message); color != "" {
		return theme.Mix(m.theme.Border, color, messageRailTint)
	}
	colors := theme.Gradient(m.theme.Accent, gradientEndColor(m.theme), 8)
	if len(colors) == 0 {
		return m.theme.Accent
	}
	return colors[block.groupIndex%len(colors)]
}

// messageAuthorColor returns an author's stable identity color.
//
// YouTube publishes no author color at all, so it is derived from the channel
// ID: the same person is the same color in every session and on every machine,
// with no mutable per-user color state anywhere. Rows with no real author
// return "" and keep the neutral accent treatment rather than being tinted by a
// fabricated identity.
func (m shellModel) messageAuthorColor(message youtube.Message) string {
	switch message.Type {
	case youtube.MessageTypeNotice, youtube.MessageTypeSystem:
		return ""
	}
	identity := strings.TrimSpace(message.Author.Identity())
	if identity == "" {
		return ""
	}
	return theme.IdentityColor(identity, []string{m.theme.Background, m.theme.Surface}, m.theme.Foreground)
}

func (m shellModel) messageGroupSeparator(rowWidth int) string {
	if rowWidth <= 0 {
		return ""
	}
	line := strings.Repeat("─", rowWidth)
	if rowWidth >= 5 {
		line = "  " + strings.Repeat("─", rowWidth-4) + "  "
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.theme.Border)).
		Background(lipgloss.Color(m.theme.Surface)).
		Render(line)
}

// terminalRowString renders a row to an exact-width line.
//
// The background is stamped on every fragment and on the trailing padding
// rather than applied as an outer wrap: fragments each end in their own ANSI
// reset, so an outer background would only color text up to the row's first
// reset, and terminals then paint their own - frequently transparent - default
// for the remainder.
func terminalRowString(row render.Row, width int, background string) string {
	if width <= 0 {
		return ""
	}
	if row.Width() > width {
		return backgroundStyledLine(fitLine(row.Plain(), width), background)
	}
	out := row.TerminalStringWithBackground(background)
	if padding := width - row.Width(); padding > 0 {
		out += backgroundStyledLine(strings.Repeat(" ", padding), background)
	}
	return out
}

func (m shellModel) chatRowWidth(layout shellLayout) int {
	rowWidth := layout.chatWidth
	if layout.chatFramed {
		rowWidth = layout.chatWidth - 4
	}
	return clampMin(rowWidth, 1)
}

func (m shellModel) chatMessageContentWidth(layout shellLayout) int {
	rowWidth := m.chatRowWidth(layout)
	return clampMin(rowWidth-messageGutterWidth(rowWidth), 1)
}

// messageGutterWidth budgets the rail and envelope column. It shrinks to two
// cells and then to nothing rather than eating a narrow pane's text.
func messageGutterWidth(rowWidth int) int {
	switch {
	case rowWidth >= messageGutterWideWidth:
		return 4
	case rowWidth >= messageGutterNarrowWidth:
		return 2
	default:
		return 0
	}
}

// emptyFilterRow explains an empty viewport that a filter caused, so "nothing
// is happening" and "you are hiding it" cannot be confused.
func (m shellModel) emptyFilterRow(width int) string {
	state := m.activeChatState()
	hidden := len(state.messages) + len(state.activeOrder)
	detail := "no messages yet"
	if hidden > 0 {
		detail = fmt.Sprintf("no matching messages (%d hidden)", hidden)
	}
	return fitLine(" filter: "+state.filters.summary()+" — "+detail, width)
}

// --- empty state -----------------------------------------------------------

// emptyStateRows is where `yc chat` lands with no target configured. It names
// the two ways out rather than leaving a blank pane.
//
// The headline and the scanner run on the shared frame clock so an idle pane
// still reads as a live app. Both degrade to their static frame under
// animation=off, leaving the wording and the layout untouched.
func (m shellModel) emptyStateRows(width int) []string {
	width = clampMin(width, 1)
	elapsed := m.frameElapsed()
	rows := []string{
		m.emptyStateLine("", width),
		m.emptyStateHeadline(noChatHeadline, width, elapsed),
		m.emptyStateLine("", width),
		m.emptyStateLine("/chats or "+targetPickerKeyHint+" to open one", width),
		m.emptyStateLine("ctrl+p for the command palette", width),
	}
	if scanner := m.emptyStateScanner(width, elapsed); scanner != "" {
		rows = append(rows, m.emptyStateLine("", width), scanner)
	}
	return rows
}

func (m shellModel) emptyStateLine(text string, width int) string {
	return paneStyledText(fitLine(emptyStateIndent+text, width), m.theme.Muted, m.theme.Surface, false)
}

// emptyStateHeadline drifts an accent gradient through the headline. Muted is
// the near end so the resting color stays the empty state's own tone instead of
// turning the pane into an accent banner.
func (m shellModel) emptyStateHeadline(text string, width int, elapsed time.Duration) string {
	cfg := textEffectConfig(m.theme, m.animationModeValue(), animation.TextEffectGradientWave)
	cfg.Base = m.theme.Muted
	cfg.Bold = true
	frame := animatedText(revealDisplayCells(emptyStateIndent+text, width), cfg, elapsed, m.theme.Surface)
	return paddedEffectLine(frame, width, m.theme.Surface)
}

// emptyStateScanner bounces a marker across a narrow track as an idle
// indicator: nothing is arriving, but the app is still ticking.
//
// The row is dropped entirely rather than frozen when animation is off. A
// stationary marker carries no meaning, unlike the headline, whose wording has
// to be there either way.
func (m shellModel) emptyStateScanner(width int, elapsed time.Duration) string {
	indentWidth := ansi.StringWidth(emptyStateIndent)
	track := min(width-indentWidth-2, emptyStateScannerWidth)
	if track < 4 || !m.animationEnabled() {
		return ""
	}
	cfg := textEffectConfig(m.theme, m.animationModeValue(), animation.TextEffectBounce)
	cfg.Base = m.theme.Accent
	cfg.Trail = m.theme.Muted
	cfg.Width = track
	frame := backgroundSpaces(indentWidth, m.theme.Surface) +
		animatedText(emptyStateScannerGlyph, cfg, elapsed, m.theme.Surface)
	return paddedEffectLine(frame, width, m.theme.Surface)
}

// --- side panes ------------------------------------------------------------

func (m shellModel) sidebarView(layout shellLayout) string {
	return renderSidebar(layout.sidebarWidth, layout.sidebarContentHeight, m.sidebarState())
}

func (m shellModel) sidebarState() sidebarState {
	entries := make([]sidebarEntry, 0, m.chatCount())
	if m.chats != nil {
		activeKey := m.chats.activeKey()
		for _, key := range m.chats.order {
			state := m.chats.states[key]
			if state == nil {
				continue
			}
			entries = append(entries, sidebarEntry{
				Label:    state.target.Label(),
				Status:   state.status.Status,
				Unread:   state.unread,
				Filtered: state.filters.active(),
				Live:     state.live,
				Active:   key == activeKey,
			})
		}
	}
	return sidebarState{
		Palette:  m.theme,
		Entries:  entries,
		Selected: m.sidebarSelected,
		Focused:  m.focus == focusSidebar && !m.anyOverlayOpen(),
		Phase:    m.paneFocusPhase(),
	}
}

func (m shellModel) activityView(layout shellLayout) string {
	return renderActivityLog(layout.activityWidth, layout.activityContentHeight, activityLogState{
		Palette:        m.theme,
		Entries:        activityEntriesForChats(m.chats, maxActivityEntries),
		ShowChatLabels: m.chatCount() > 1,
		Phase:          m.paneFocusPhase(),
	})
}

// --- widget state adapters -------------------------------------------------

func (m shellModel) statusBarState() statusBarState {
	state := m.activeChatState()
	quota, quotaKnown := m.quotaSnapshot()
	if quota.EffectiveInterval <= 0 {
		quota.EffectiveInterval = m.effectivePollInterval()
	}
	st := statusBarState{
		Palette:       m.theme,
		AnimationMode: m.animationModeValue(),
		Now:           m.metricsNow(),
		ChatCount:     m.chatCount(),
		Dropped:       m.droppedMessageCount(),
		PendingClear:  m.pendingClearChat,
		Focus:         m.focusName(),
		Layout:        string(m.messageLayout),
		Quota:         quota,
		QuotaKnown:    quotaKnown,
		CostPerPoll:   m.estimatedPollCost(),
		FPS:           float64(m.framesPerSecond()),
	}
	if m.chats != nil {
		st.Unread = m.chats.totalUnread()
	}
	if !m.noChatsOpen() {
		st.ChatTitle = m.activeChatLabel()
	}
	if state != nil {
		st.Status = state.status.Status
		st.StatusDetail = summarizeDetail(state.status.Detail)
		st.Filter = state.filters.summary()
		st.Search = m.searchStatusLabel()
		st.SendFeedback = state.sendFeedback
		st.Live = state.live
		st.LiveSince = state.liveSince
		st.Viewers = state.viewerCount
		st.ViewersKnown = state.viewersKnown
	}
	if m.identityKnown {
		st.Subscribers = m.identity.SubscriberCount
		st.SubscribersKnown = m.identity.SubscriberCountKnown
	}
	if m.lastNotification != nil {
		st.Notification = notificationSummary(*m.lastNotification)
	}
	st.Moderation, st.ModerationLevel = m.moderationStatus()
	st.DebugRecording = m.debugRecording
	return st
}

// estimatedPollCost is the configured estimate for one liveChatMessages.list
// call, which is what the projected-exhaustion figure divides by.
//
// It is an estimate by construction: Google's published quota table contains no
// live-chat rows at all, so the cost is a config-overridable community
// observation and every figure derived from it is labeled as such.
func (m shellModel) estimatedPollCost() int {
	if cost := m.effectiveConfig.Quota.Costs.List; cost > 0 {
		return cost
	}
	return 5
}

func (m shellModel) composerState() composerState {
	state := m.activeChatState()
	st := composerState{
		Palette:       m.theme,
		AnimationMode: m.animationModeValue(),
		Now:           m.metricsNow(),
		Focused:       m.focus == focusComposer && !m.anyOverlayOpen(),
		HasChat:       !m.noChatsOpen(),
		TargetLabel:   m.activeChatLabel(),
	}
	if state != nil {
		st.Draft = state.composerText
		st.Reply = state.replyTo
		st.SendState = state.sendState
	}
	st.DisabledReason = m.composerDisabledReason()
	st.Suggestions = m.mentionSuggestions()
	st.SelectedSuggestion = m.mentionSelection(len(st.Suggestions))
	return st
}

// composerDisabledReason explains why sending is unavailable.
//
// yc degrades by capability rather than by hiding controls: an API-key-only
// session can read a public chat but cannot send, and a token without
// youtube.force-ssl cannot either. A composer that simply vanished would teach
// the user nothing about how to get it back.
func (m shellModel) composerDisabledReason() string {
	if m.client == nil {
		return "no chat source"
	}
	if state := m.activeChatState(); state != nil && state.ended {
		return "live chat ended"
	}
	if m.effectiveConfig.Google.AccessToken == "" && m.effectiveConfig.Google.RefreshToken == "" &&
		m.effectiveConfig.YouTube.APIKey != "" {
		return "read-only (API key); run `yc login` to send"
	}
	return ""
}

func (m shellModel) helpState() helpState {
	groups := make([]string, 0, len(keyGroupOrder))
	for _, group := range keyGroupOrder {
		groups = append(groups, helpGroupLine(group))
	}
	source := m.sourceDetail
	if source == "" {
		source = "chat source"
	}
	return helpState{
		Palette:  m.theme,
		Expanded: m.helpExpanded,
		Compact:  compactHelpLine(),
		Groups:   groups,
		NarrowGroups: []string{
			" ctrl+p: commands",
			" i/esc | tab | jk",
			" ?: help | ctrl+c: quit",
		},
		Source: source,
	}
}

func (m shellModel) inspectState() inspectState {
	message, ok := m.selectedMessage()
	return inspectState{
		Palette:  m.theme,
		Message:  message,
		Selected: ok,
		Phase:    m.paneFocusPhase(),
	}
}

func (m shellModel) themePickerState() themePickerState {
	return themePickerState{
		Palette:    m.theme,
		Names:      m.overlayItems(),
		ActiveName: m.effectiveConfig.Features.ThemeName,
		Custom:     m.effectiveConfig.Features.ThemeCustom,
		Selected:   m.overlay.selected,
		Phase:      m.paneFocusPhase(),
	}
}

// listOverlayState builds whichever docked list overlay is open. The item list
// comes from the model's own overlayItems, so the view and the key handler can
// never disagree about what enter will commit.
func (m shellModel) listOverlayState() listOverlayState {
	st := listOverlayState{
		Palette:  m.theme,
		Items:    m.overlayItems(),
		Selected: m.overlay.selected,
		Phase:    m.paneFocusPhase(),
	}
	switch m.overlay.kind {
	case overlayPalette:
		st.Icon, st.Title, st.Accent = "⌘", "Command Palette", m.theme.Accent
		st.Header = commandPaletteHeader(m.overlay.query)
		st.Detail = paletteCommandShortcut
	case overlayTargetPicker:
		st.Icon, st.Title, st.Accent = "📺", "Open Live Chat", m.theme.Success
		st.Header = targetPickerHeader(m.overlay.query, "", false)
		openLabels := []string(nil)
		if m.chats != nil {
			openLabels = m.chats.chatLabels()
		}
		configured := m.effectiveConfig.DefaultChats
		st.Detail = func(item string) string {
			return targetPickerDetail(item, openLabels, configured)
		}
	case overlayEmojiPicker:
		st.Icon, st.Title, st.Accent = "😀", "Emoji", m.theme.Accent
		st.Header = emojiPickerHeader(m.overlay.query)
	}
	return st
}

// --- non-chat tabs ---------------------------------------------------------

// quotaLedgerLines renders the Quota tab: the estimated ledger, the cadence it
// implies, and the per-endpoint tally.
//
// Twitch's equivalent tab held stream markers, which YouTube has no analog
// for. Quota does not merely fill the gap - it is the constraint that decides
// whether a session survives the broadcast, so it earns a whole screen.
func (m shellModel) quotaLedgerLines() []string {
	quota, known := m.quotaSnapshot()
	if !known {
		return []string{
			" No quota ledger yet.",
			"",
			" This chat source does not report one - mock and fake sources",
			" perform no API calls, so there is nothing to account for.",
		}
	}
	cost := m.estimatedPollCost()
	lines := []string{
		fmt.Sprintf(" Estimated usage   %s / %s units   (%.0f%% left)",
			formatUnits(quota.UsedUnits), formatUnits(quota.LimitUnits), quota.RemainingPercent()),
		fmt.Sprintf(" Daily searches    %d / %d", quota.SearchUsed, quota.SearchLimit),
		"",
		fmt.Sprintf(" Poll cadence      %s   (server floor %s, budget floor %s)",
			formatPollInterval(quota.EffectiveInterval),
			formatPollInterval(quota.ServerFloor),
			formatPollInterval(quota.BudgetFloor)),
		fmt.Sprintf(" Cadence mode      %s", quotaModeDescription(quota.Mode)),
	}
	if remaining, ok := quota.ProjectedExhaustion(cost); ok {
		lines = append(lines, fmt.Sprintf(" Projected budget  %s at this cadence", formatCompactDuration(remaining)))
	}
	if !quota.ResetAt.IsZero() {
		lines = append(lines, fmt.Sprintf(" Resets            %s (midnight America/Los_Angeles)",
			quota.ResetAt.Local().Format("2006-01-02 15:04")))
	}
	lines = append(lines, "", " By endpoint (estimated cost per call)")
	for _, endpoint := range sortedEndpoints(quota.ByEndpoint) {
		lines = append(lines, fmt.Sprintf("   %-34s %s", endpoint, formatUnits(quota.ByEndpoint[endpoint])))
	}
	// The disclaimer is not decoration. Google publishes no quota costs for
	// any live-chat method, so a figure presented as fact would be one the
	// user could act on and be wrong about.
	lines = append(lines, "",
		" Every figure here is an estimate. Google publishes no quota cost",
		" for any live-chat method; the Cloud Console is the authority.")
	return lines
}

func sortedEndpoints(byEndpoint map[string]int) []string {
	endpoints := make([]string, 0, len(byEndpoint))
	for endpoint := range byEndpoint {
		endpoints = append(endpoints, endpoint)
	}
	sort.Strings(endpoints)
	return endpoints
}

// streamInfoLines renders the Stream Info tab from what the session already
// knows. It is read-only unless a StreamInfoManager was wired in, and it says
// which of the two it is rather than presenting inert fields.
func (m shellModel) streamInfoLines() []string {
	state := m.activeChatState()
	if state == nil || m.noChatsOpen() {
		return []string{" No live chat open.", "", " Open one with " + targetPickerKeyHint + " to see its broadcast details."}
	}
	target := state.target
	lines := []string{
		" Title        " + emptyOr(target.Title, "(unresolved)"),
		" Video        " + emptyOr(target.VideoID, "—"),
		" Channel      " + emptyOr(firstNonEmpty(target.Handle, target.ChannelID), "—"),
		" Live chat    " + liveChatIDLabel(target),
		"",
		" Status       " + string(state.status.Status) + detailSuffix(state.status.Detail),
		" Viewers      " + formatViewerCount(state.viewerCount, state.viewersKnown),
	}
	if !state.liveSince.IsZero() {
		lines = append(lines, " On air       "+formatElapsed(liveElapsed(m.metricsNow(), state.liveSince)))
	}
	if state.activePoll != nil {
		lines = append(lines, "", " Poll         "+state.activePoll.Question)
		for _, option := range state.activePoll.Options {
			lines = append(lines, fmt.Sprintf("   %-30s %d", truncateDisplayWidth(option.Text, 30), option.Tally))
		}
	}
	lines = append(lines, "")
	if m.streamInfoManager == nil {
		lines = append(lines, " Editing needs the youtube.force-ssl scope. Run `yc login` to grant it.")
	} else {
		lines = append(lines, " Editing is available for broadcasts you own.")
	}
	return lines
}

// liveChatIDLabel reports whether a chat is resolved without printing the ID.
// The liveChatId is not a credential, but it is a long opaque token that adds
// nothing a person can act on, and printing it invites it into screenshots.
func liveChatIDLabel(target youtube.ChatTarget) string {
	if target.Resolved() {
		return "resolved"
	}
	return "unresolved"
}

func detailSuffix(detail string) string {
	if summary := summarizeDetail(detail); summary != "" {
		return " — " + summary
	}
	return ""
}

// linesPane frames a block of plain lines, which is how the non-chat tabs are
// drawn without this file knowing their content.
func (m shellModel) linesPane(icon, title string, lines []string, width, height, contentHeight int, framed bool) string {
	if !framed {
		return backgroundStyledLine(fitBlock(strings.Join(lines, "\n"), width, height), m.theme.Surface)
	}
	contentWidth := clampMin(width-4, 1)
	body := make([]string, 0, contentHeight)
	for _, line := range lines {
		if len(body) >= contentHeight {
			break
		}
		body = append(body, paneStyledText(fitLine(line, contentWidth), m.theme.Foreground, m.theme.Surface, false))
	}
	body = padLines(body, contentWidth, contentHeight, m.theme.Surface)
	return renderPane(paneSpec{
		palette:       m.theme,
		icon:          icon,
		title:         title,
		content:       strings.Join(body, "\n"),
		width:         width,
		contentHeight: contentHeight,
		padding:       1,
		accent:        m.theme.Accent,
	})
}

// --- terminal background ---------------------------------------------------

// themeBackgroundSequence returns the OSC 11 sequence that overrides the
// terminal's own default background, so the darker canvas covers the whole
// window rather than only the cells lipgloss paints.
//
// It is part of the returned frame rather than written to the output writer:
// Bubble Tea's renderer owns that writer from its own goroutine, and writing to
// it independently races that goroutine and can drop or interleave the
// sequence. Embedding is safe because View runs on the renderer's goroutine,
// and the ANSI parser Bubble Tea and lipgloss share treats OSC as zero-width,
// so it perturbs no width math. It is empty outside an interactive session so
// piped and test output stays clean.
func (m shellModel) themeBackgroundSequence() string {
	canvas := m.canvas()
	if m.terminalOutput == nil || strings.TrimSpace(canvas) == "" {
		return ""
	}
	return ansi.SetBackgroundColor(canvas)
}

// --- splash ----------------------------------------------------------------

// splashView renders the staged boot sequence: a gradient block wordmark, a
// tagline that types itself in, a shimmering strapline, a progress bar, and a
// named initialization phase.
//
// Any key skips it and animation_mode=off suppresses it entirely, so nobody is
// ever made to wait for decoration.
func (m shellModel) splashView() string {
	width, height := clampMin(m.width, 1), clampMin(m.height, 1)
	canvas := m.canvas()
	contentWidth := splashContentWidth(width)
	elapsed, fraction := m.splashProgress()

	logo := splashLogo
	if width < 38 || height < 13 {
		logo = []string{"╭── ✦ yc ✦ ──╮"}
	}
	phase := m.gradientPhase(clampMin(widestLine(logo), 1))
	logoLines := make([]string, 0, len(logo))
	for row, line := range centeredBlockLines(logo, contentWidth) {
		logoLines = append(logoLines, gradientForegroundText(
			line,
			m.theme.Accent, gradientEndColor(m.theme), canvas,
			phase+row*2, true,
		))
	}

	blank := paneStyledText(centeredFittedLine("", contentWidth), m.theme.Muted, canvas, false)
	progress := gradientForegroundText(
		centeredFittedLine(splashProgressBar(fraction, min(splashProgressW, clampMin(contentWidth-2, 0))), contentWidth),
		m.theme.Accent, gradientEndColor(m.theme), canvas, phase, true,
	)
	phaseLabel := splashPhaseLabel(fraction, m.activeChatLabel())
	if hint := splashSkipHint(fraction); hint != "" &&
		contentWidth >= ansi.StringWidth(phaseLabel)+ansi.StringWidth(hint)+5 {
		phaseLabel += "   ·   " + hint
	}

	lines := splashLinesForHeight(height, logoLines,
		m.splashTaglineLine(contentWidth, elapsed, canvas),
		m.splashStraplineLine(contentWidth, elapsed, canvas),
		blank, progress,
		paneStyledText(centeredFittedLine(phaseLabel, contentWidth), m.theme.Muted, canvas, false),
	)

	// Every line is already exactly contentWidth cells, so the block is
	// placed with one shared offset and lipgloss is left to do the vertical
	// centering only. Asking it to center horizontally as well would right-
	// trim each line first and re-center it on its own visible width, which
	// shifts each row of the wordmark by its own indent and shears the logo
	// apart.
	leading := strings.Repeat(" ", max((width-contentWidth)/2, 0))
	for index, line := range lines {
		lines[index] = leading + line
	}

	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		Align(lipgloss.Left, lipgloss.Center).
		Background(lipgloss.Color(canvas)).
		Render(strings.Join(lines, "\n"))
}

// splashProgress reports how far the splash has run, from the frame clock
// rather than the wall clock so the sequence is reproducible in a test.
func (m shellModel) splashProgress() (time.Duration, float64) {
	if m.splashUntil.IsZero() || m.lastFrameAt.IsZero() {
		return 0, 0
	}
	elapsed := splashDuration - m.splashUntil.Sub(m.lastFrameAt)
	return elapsed, clampFraction(float64(elapsed) / float64(splashDuration))
}

// splashTaglineLine types the tagline in behind a caret. The label is animated
// at its own width and centered afterwards, so the words settle into their
// final position instead of sliding left as characters land.
func (m shellModel) splashTaglineLine(contentWidth int, elapsed time.Duration, canvas string) string {
	cfg := textEffectConfig(m.theme, m.animationModeValue(), animation.TextEffectTypewriter)
	cfg.Bold = true
	// The step is set explicitly rather than taken from the effect default,
	// because the tagline has to finish inside the splash window in every
	// animation mode; a reduced-motion default would still be typing when the
	// splash lifts.
	cfg.Step = 30 * time.Millisecond
	if m.animationModeValue() == animation.ModeReduced {
		cfg.Step = 55 * time.Millisecond
	}
	frame := animatedText(revealDisplayCells(splashTagline, contentWidth), cfg, elapsed-splashTaglineLag, canvas)
	return centeredEffectLine(frame, contentWidth, canvas)
}

// splashStraplineLine sweeps a highlight across the strapline while the tagline
// types, so the sequence has motion on both lines without two competing
// reveals.
func (m shellModel) splashStraplineLine(contentWidth int, elapsed time.Duration, canvas string) string {
	cfg := textEffectConfig(m.theme, m.animationModeValue(), animation.TextEffectShimmer)
	cfg.Base = m.theme.Muted
	frame := animatedText(revealDisplayCells(splashStrapline, contentWidth), cfg, elapsed, canvas)
	return centeredEffectLine(frame, contentWidth, canvas)
}

// splashLinesForHeight drops elements from the least informative upward as the
// terminal shortens, so even a three-row window shows the wordmark and what yc
// is currently doing.
func splashLinesForHeight(height int, logo []string, tagline, strapline, blank, progress, phase string) []string {
	switch {
	case height <= 1:
		return logo[:1]
	case height == 2:
		return []string{logo[0], phase}
	case height == 3:
		return []string{logo[0], progress, phase}
	case height == 4:
		return []string{logo[0], tagline, progress, phase}
	case height == 5:
		return []string{logo[0], tagline, strapline, progress, phase}
	}
	lines := make([]string, 0, len(logo)+5)
	lines = append(lines, logo...)
	return append(lines, tagline, strapline, blank, progress, phase)
}

func splashContentWidth(width int) int {
	if width <= 2 {
		return width
	}
	return min(width-2, 54)
}

func splashProgressBar(fraction float64, width int) string {
	if width <= 0 {
		return "◆"
	}
	filled := int(clampFraction(fraction) * float64(width))
	if filled >= width {
		return "[" + strings.Repeat("━", width) + "]"
	}
	return "[" + strings.Repeat("━", filled) + "◆" + strings.Repeat("·", width-filled-1) + "]"
}

// splashSkipHint tells the user the splash is optional. A skippable wait nobody
// knows is skippable is just a wait.
func splashSkipHint(fraction float64) string {
	if fraction < 0.12 {
		return ""
	}
	return "press any key to skip"
}

// splashPhaseLabel names what startup is doing. The phases are real yc concerns
// - the palette, the frame clock, the quota ledger - rather than invented
// progress, so the label stays informative even though the bar is decoration.
func splashPhaseLabel(fraction float64, target string) string {
	switch {
	case fraction < 0.22:
		return "◌ yc · loading palette"
	case fraction < 0.48:
		return "◐ warming animation clock"
	case fraction < 0.76:
		return "◓ reading quota ledger"
	case strings.TrimSpace(target) == "":
		return "● ready"
	default:
		return "● ready for " + target
	}
}

func clampFraction(value float64) float64 {
	return min(max(value, 0), 1)
}

// formatViewerCount is the "N viewers" phrasing used outside the status bar,
// where an unknown count must not read as zero.
func formatViewerCount(count int, known bool) string {
	if !known {
		return "unknown (hidden by the broadcaster, or the stream has ended)"
	}
	if count == 1 {
		return "1 viewer"
	}
	return formatUnits(count) + " viewers"
}

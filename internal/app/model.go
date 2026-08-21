package app

import (
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/render"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
	"golang.org/x/term"
)

// Cadence constants for the shell's own timers. None of them poll YouTube:
// the transport owns the quota-bearing schedule, and these only drive the
// terminal.
const (
	// revealTickInterval paces the per-row reveal animation. It runs only
	// while a reveal queue is non-empty, so an idle chat costs nothing.
	revealTickInterval = 20 * time.Millisecond
	// chatMetricsInterval refreshes viewer and subscriber counts. Two
	// minutes is deliberately slow: each refresh is a quota-charged call and
	// a viewer count that lags by a minute misleads nobody.
	chatMetricsInterval = 2 * time.Minute
	// quotaPollInterval mirrors the transport's estimated ledger onto the
	// model. Reading it is free; it is a local accessor, not a request.
	quotaPollInterval = time.Second
	// splashDuration bounds the startup splash. Any key skips it, so the
	// wait is never something a user is stuck with once they know it exists.
	splashDuration = 10 * time.Second
)

// shellFocus is which region receives ordinary keys.
type shellFocus int

const (
	focusChat shellFocus = iota
	focusComposer
	focusSidebar
)

func (f shellFocus) String() string {
	switch f {
	case focusComposer:
		return "composer"
	case focusSidebar:
		return "chats"
	default:
		return "chat"
	}
}

// shellTab is a top-level screen selectable from the tab bar. tabChat is the
// zero value so a freshly constructed model always starts on Chat.
type shellTab int

const (
	tabChat shellTab = iota
	tabStreamInfo
	tabMisc
)

// shellTabs lists every tab in display and shortcut order.
var shellTabs = []struct {
	tab   shellTab
	label string
}{
	{tabChat, "Chat"},
	{tabStreamInfo, "Stream Info"},
	// Labeled for what the pane actually is. It used to read "Misc" while
	// the pane it renders is titled "Quota - estimated", so the one screen
	// devoted to yc's defining constraint was the one screen no user could
	// find from the tab bar.
	{tabMisc, "Quota"},
}

// tabForShortcutRune maps an alt+<digit> keypress to the tab it selects.
//
// Bubble Tea, and most terminals, cannot distinguish ctrl+1 from a plain "1",
// so tab switching uses alt+<digit> - the combination terminals do report as a
// distinct key.
func tabForShortcutRune(r rune) (shellTab, bool) {
	if r < '1' || r > '9' {
		return 0, false
	}
	index := int(r - '1')
	if index >= len(shellTabs) {
		return 0, false
	}
	return shellTabs[index].tab, true
}

// paneVisibility is a standing show/hide choice for a side pane. "auto" lets
// the terminal width decide; an explicit choice outranks it in both directions,
// so a narrow terminal cannot un-hide a pane the user hid and a wide one cannot
// hide a pane they pinned.
type paneVisibility int

const (
	paneVisibilityAuto paneVisibility = iota
	paneVisibilityShown
	paneVisibilityHidden
)

// overlayKind identifies the single docked overlay that can be open.
//
// Overlays are mutually exclusive by construction rather than by convention:
// one field means a second overlay cannot open behind the first and leave the
// user with an esc that appears to do nothing.
type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayPalette
	overlayThemePicker
	overlayTargetPicker
	overlayEmojiPicker
)

// overlayState is the shared state of every docked overlay: a query, a
// selection, and the kind that decides which item list the view draws.
//
// The item lists themselves are built on demand by the view rather than stored,
// so a theme list or emoji catalog that grows cannot make the model stale.
type overlayState struct {
	kind     overlayKind
	query    string
	selected int
	// itemCount is refreshed by the model whenever the query changes, so
	// selection movement can clamp without the view having to report back.
	itemCount int
}

func (o overlayState) open() bool { return o.kind != overlayNone }

// shellModel is the Bubble Tea model: every piece of UI state, and nothing
// that performs I/O.
//
// It is copied by value on every Update, so anything that must survive the copy
// - the shared row cache, the per-chat states - is held behind a pointer.
type shellModel struct {
	chats *chatStateSet

	theme           theme.Palette
	effectiveConfig config.Config
	// terminalOutput is non-nil only in interactive mode. It gates the OSC 11
	// canvas override, so piped and test output stays free of escape codes.
	terminalOutput io.Writer

	client         ChatClient
	systemNotifier SystemNotifier
	debugLogger    debuglog.Logger

	identityLookup     IdentityLookup
	broadcastResolver  BroadcastResolver
	subscriptionLookup SubscriptionLookup
	streamInfoManager  StreamInfoManager
	categoryLookup     CategoryLookup

	// chatLogger is the opt-in chat log. chatLogFailed latches the first
	// write failure so a full disk degrades to one status notice rather
	// than an error per message.
	chatLogger    ChatLogger
	chatLogFailed bool

	// Auto-follow re-resolves a chat's channel after its stream ends. The
	// interval and check cap come from config; per-chat progress lives on
	// chatState so two chats can follow independently.
	autoFollowEnabled   bool
	autoFollowInterval  time.Duration
	autoFollowMaxChecks int

	// identity is the authenticated user's own channel. The local echo has no
	// other source for the sender's display name and badges.
	identity      youtube.Identity
	identityKnown bool
	mentionHandle string

	// clipboardText is a pending OSC 52 clipboard payload, embedded in the
	// next rendered frame and retired by clipboardClearMsg. clipboardSeq
	// numbers the payloads so a stale timer cannot drop a newer one.
	clipboardText string
	clipboardSeq  int

	sourceDetail  string
	animationMode string
	avatarMode    string
	mouseEnabled  bool

	width           int
	height          int
	focus           shellFocus
	terminalFocused bool
	activeTab       shellTab

	leaderPending    bool
	helpExpanded     bool
	pendingClearChat bool
	overlay          overlayState
	// moderation is the armed-or-in-flight moderation action. It sits beside
	// pendingClearChat rather than on a chatState because, like that
	// confirmation, only one can exist at a time and it belongs to the chat
	// the user is looking at. See internal/app/moderation.go.
	moderation moderationState
	// moderationNote is the last moderation status line. It outlives the
	// action that produced it, which is why it is a separate field.
	moderationNote moderationNote

	sidebarVisibility     paneVisibility
	activityVisibility    paneVisibility
	sidebarSelected       int
	sidebarWidthOverride  int
	activityWidthOverride int

	messageLayout  render.LayoutMode
	badgeMode      render.BadgeMode
	highlightEmoji bool
	fullUsername   bool

	// quota mirrors the transport's estimated ledger. Every figure derived
	// from it is an estimate and every surface that shows one says so.
	quota        quota.Snapshot
	quotaKnown   bool
	pollInterval time.Duration

	reconnectInFlight bool
	nextSend          int
	nextReveal        int

	frameTickScheduled   bool
	revealTickScheduled  bool
	metricsTickScheduled bool
	quotaTickScheduled   bool
	lastFrameAt          time.Time
	frameTimestamps      []time.Time
	splashUntil          time.Time
	splashSkipped        bool

	lastNotification *SystemNotification
	// giftBurstAt and giftBurstCount coalesce a gift-membership flood into
	// one notification. Gifted memberships arrive as one event per recipient,
	// so a fifty-gift drop is fifty desktop notifications without this.
	giftBurstAt    time.Time
	giftBurstCount int

	debugRecording bool
}

// newShellModel builds everything both the mock and live sources share.
//
// It exists because twi maintained the two constructors by hand and they
// drifted: four display settings that config parsed, doctor validated, and the
// toggles persisted did nothing in the only mode that talked to the network.
// Callers add only their source-specific tail.
func newShellModel(cfg config.Config, clock animation.Clock) shellModel {
	animationConfig := animationConfigFor(cfg.Features.AnimationMode)
	chats := newChatStateSet(
		configuredTargets(cfg.DefaultChats),
		animationConfig,
		clock,
		cfg.Features.ScrollbackLimit,
	)
	return shellModel{
		chats:          chats,
		theme:          cfg.ResolveTheme(),
		animationMode:  string(animationConfig.Mode),
		avatarMode:     cfg.Features.AvatarMode,
		mouseEnabled:   cfg.Features.EnableMouse,
		messageLayout:  render.NormalizeLayoutMode(cfg.Features.MessageLayout),
		badgeMode:      render.NormalizeBadgeMode(cfg.Features.BadgeMode),
		highlightEmoji: cfg.Features.HighlightEmoji,
		fullUsername:   cfg.Features.FullUsername,
		mentionHandle:  cfg.YouTube.ChannelID,
		// Zero means "size from the terminal width". A configured value is
		// clamped at layout time rather than here, because what a terminal
		// can afford is not knowable until it reports its size.
		sidebarWidthOverride:  cfg.Features.SidebarWidth,
		activityWidthOverride: cfg.Features.ActivityWidth,
		debugRecording:        cfg.Debug.Enabled,
		autoFollowEnabled:     cfg.Features.AutoFollow,
		autoFollowInterval:    autoFollowIntervalFor(cfg.Features.AutoFollowPollSeconds),
		autoFollowMaxChecks:   autoFollowMaxChecksFor(cfg.Features.AutoFollowMaxChecks),
		effectiveConfig:       cfg,
		width:                 defaultShellWidth,
		height:                defaultShellHeight,
		focus:                 focusChat,
		terminalFocused:       true,
	}
}

// newLiveModel builds the model for a real chat client plus its optional
// collaborators. Every collaborator is nil-able: a credential-free run drives
// the whole UI with absent data rather than failing.
func newLiveModel(cfg config.Config, client ChatClient, clock animation.Clock, opts ClientOptions) shellModel {
	model := newShellModel(cfg, clock)
	model.client = client
	model.systemNotifier = opts.SystemNotifier
	model.debugLogger = opts.DebugLogger
	model.identityLookup = opts.IdentityLookup
	model.broadcastResolver = opts.BroadcastResolver
	model.subscriptionLookup = opts.SubscriptionLookup
	model.streamInfoManager = opts.StreamInfoManager
	model.categoryLookup = opts.CategoryLookup
	model.chatLogger = opts.ChatLogger
	model.sourceDetail = "youtube live chat"

	// Every open chat starts in "connecting": the transport has not reported
	// anything yet, and a blank status reads as a stalled app.
	for _, key := range model.chats.chatKeys() {
		state := model.chats.stateForKey(key)
		if state == nil {
			continue
		}
		state.status = youtube.ConnectionState{
			Status: youtube.ConnectionConnecting,
			ChatID: state.target.LiveChatID,
			Detail: "resolving live chat",
			At:     time.Now(),
		}
	}
	return model
}

// animationConfigFor turns the configured mode into a reveal configuration.
func animationConfigFor(mode string) animation.Config {
	cfg := animation.DefaultConfig()
	cfg.Mode = animation.NormalizeMode(mode)
	return cfg
}

// Init arms one blocking receive per transport stream plus the shared clocks.
//
// Each stream command re-arms itself on every delivery, so a closed channel
// arrives as an ordinary message with ok=false rather than as a panic.
func (m shellModel) Init() tea.Cmd {
	return batchNonNil(
		m.nextClientMessageCommand(),
		m.nextConnectionStateCommand(),
		m.nextClientModerationCommand(),
		m.nextClientRoomEventCommand(),
		m.nextClientPollCommand(),
		m.scheduleFrameTick(),
		m.resolveIdentityCommand(),
		m.resolveTargetsCommand(),
		m.refreshChatMetricsCommand(),
		m.scheduleChatMetricsTick(),
		m.scheduleQuotaTick(),
	)
}

// --- accessors -------------------------------------------------------------
//
// Everything below is read-only state for the view lane. The view never mutates
// the model, so these return values and copies rather than pointers wherever
// that is affordable.

func (m shellModel) activeChatState() *chatState {
	if m.chats == nil {
		return (&chatStateSet{states: map[string]*chatState{}}).emptyState()
	}
	return m.chats.activeState()
}

// activeChatLabel is the human-facing title of the active chat.
func (m shellModel) activeChatLabel() string {
	if m.chats == nil {
		return ""
	}
	return m.chats.activeLabel()
}

func (m shellModel) activeChatKey() string {
	if m.chats == nil {
		return ""
	}
	return m.chats.activeKey()
}

// chatCount reports how many chats are open. Zero is a supported state.
func (m shellModel) chatCount() int {
	if m.chats == nil {
		return 0
	}
	return len(m.chats.order)
}

func (m shellModel) noChatsOpen() bool { return m.chats.empty() }

// visibleMessages returns the active chat's history after its local filters,
// which is exactly what the chat pane should draw.
func (m shellModel) visibleMessages() []youtube.Message {
	return m.activeChatState().visibleMessages(m.mentionHandle)
}

// revealFrames returns the in-progress reveal rows for the active chat, keyed
// by reveal ID, alongside the order they were enqueued in.
func (m shellModel) revealFrames() ([]string, map[string][]render.Row) {
	state := m.activeChatState()
	if state == nil || state.revealQueue == nil {
		return nil, nil
	}
	return state.active.ids(), state.revealQueue.Frames()
}

// revealMessage returns the message behind an in-progress reveal.
func (m shellModel) revealMessage(id string) (youtube.Message, bool) {
	state := m.activeChatState()
	if state == nil {
		return youtube.Message{}, false
	}
	return state.active.messageFor(id)
}

// selectedMessage returns the message under the browsing cursor, which is what
// inspect shows and what r replies to.
func (m shellModel) selectedMessage() (youtube.Message, bool) {
	state := m.activeChatState()
	id := replyMessageID(state.selected)
	if id == "" {
		return youtube.Message{}, false
	}
	for i := len(state.messages) - 1; i >= 0; i-- {
		if state.messages[i].ID == id {
			return state.messages[i], true
		}
	}
	return youtube.Message{}, false
}

// droppedMessageCount reports messages lost to a full buffer, or zero for a
// source that cannot drop. The status bar shows it at every width: chat quietly
// losing messages is the one thing a moderator must not have to discover for
// themselves.
func (m shellModel) droppedMessageCount() uint64 {
	counter, ok := m.client.(MessageDropCounter)
	if !ok {
		return 0
	}
	return counter.DroppedMessages()
}

// quotaSnapshot returns the last mirrored ledger and whether one has arrived.
// Every figure in it is an estimate: Google publishes no quota costs for live
// chat methods.
func (m shellModel) quotaSnapshot() (quota.Snapshot, bool) {
	return m.quota, m.quotaKnown
}

// effectivePollInterval is the cadence the transport is actually polling at.
func (m shellModel) effectivePollInterval() time.Duration {
	if m.quotaKnown && m.quota.EffectiveInterval > 0 {
		return m.quota.EffectiveInterval
	}
	return m.pollInterval
}

func (m shellModel) focusName() string { return m.focus.String() }

// metricsNow is the only clock the view is allowed to read. It returns the last
// frame's timestamp so View stays a pure function of already-ticked state, and
// the zero time renders the deterministic static frame.
func (m shellModel) metricsNow() time.Time { return m.lastFrameAt }

// frameElapsed is the phase every continuous chrome effect derives from.
func (m shellModel) frameElapsed() time.Duration {
	return animation.FrameElapsed(m.lastFrameAt)
}

// framesPerSecond reports the observed repaint rate over the last second.
func (m shellModel) framesPerSecond() int { return len(m.frameTimestamps) }

func (m shellModel) animationEnabled() bool {
	return m.animationMode != string(animation.ModeOff)
}

// splashActive reports whether the startup splash still owns the screen.
func (m shellModel) splashActive() bool {
	if m.splashSkipped || m.splashUntil.IsZero() {
		return false
	}
	if m.lastFrameAt.IsZero() {
		return true
	}
	return m.lastFrameAt.Before(m.splashUntil)
}

// splashDeadline returns when the startup splash should end, or the zero time
// when animation is disabled - splashActive treats that as "no splash".
func splashDeadline(animationMode string) time.Time {
	if animationMode == string(animation.ModeOff) {
		return time.Time{}
	}
	return time.Now().Add(splashDuration)
}

// chatViewportHeight is the state lane's estimate of how many chat rows fit,
// used for page scrolling and for bounding the scroll offset.
//
// The view lane owns the authoritative layout and re-clamps the offset when it
// paints, so this deliberately errs on the generous side: over-scrolling is
// corrected on the next frame, under-scrolling would strand the user.
func (m shellModel) chatViewportHeight() int {
	// The layout solver in view.go is the authority on these; the constants
	// are shared with it so a taller composer cannot silently skew the page
	// keys while leaving the frame correct.
	chrome := chatChromeRows + composerBlockHeight
	if m.overlay.open() {
		chrome += dockedOverlayHeight
	}
	if m.activeChatState().inspectOpen {
		chrome += dockedOverlayHeight
	}
	height := m.height - chrome
	if height < 1 {
		return 1
	}
	return height
}

// maxScrollOffset bounds the scroll offset without rendering.
//
// A message occupies at least one row, so message count is a lower bound on
// rows; the multiplier covers wrapping. The paint-time clamp in the view is the
// authoritative one - this only stops the offset growing without limit while
// the user holds page-up on a short buffer.
func (m shellModel) maxScrollOffset() int {
	visible := len(m.visibleMessages()) + m.activeChatState().active.len()
	bound := visible*scrollRowsPerMessageBound - m.chatViewportHeight()
	if bound < 0 {
		return 0
	}
	return bound
}

// scrolledAway reports whether the viewer has scrolled off the bottom. New
// messages are appended statically in that case, so off-screen traffic cannot
// grow a reveal backlog or shift the page under the eye.
func (m shellModel) scrolledAway() bool {
	return m.activeChatState().scrollOffset > 0
}

func batchNonNil(cmds ...tea.Cmd) tea.Cmd {
	filtered := make([]tea.Cmd, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd != nil {
			filtered = append(filtered, cmd)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return tea.Batch(filtered...)
	}
}

// isInteractiveTerminal reports whether w is a terminal yc can take over.
// A piped writer renders one static frame instead, which is what the CI smoke
// test exercises.
func isInteractiveTerminal(w io.Writer) bool {
	fd, ok := w.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	return term.IsTerminal(int(fd.Fd()))
}

// summarizeDetail trims a transport detail to something a status line can hold
// without letting a multi-line error take over the screen.
func summarizeDetail(detail string) string {
	detail = strings.TrimSpace(strings.ReplaceAll(detail, "\n", " "))
	return strings.Join(strings.Fields(detail), " ")
}

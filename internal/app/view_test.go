package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/youtube"
)

// newViewModel builds a fully populated, deterministic model for the layout and
// golden tests: a fixed frame time, animation off, and one chat with history,
// so every assertion is about geometry rather than about motion.
func newViewModel(t *testing.T, width, height int) shellModel {
	t.Helper()
	model := newModelForTest(t, "launch-day")
	model.width, model.height = width, height
	model.lastFrameAt = time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	model.sourceDetail = "mock chat source"
	model.quotaKnown = true
	model.quota = quota.Snapshot{
		UsedUnits:         3240,
		LimitUnits:        10000,
		RemainingUnits:    6760,
		SearchLimit:       100,
		EffectiveInterval: 5 * time.Second,
		ServerFloor:       5 * time.Second,
		Mode:              quota.ModeLive,
		Estimated:         true,
		ByEndpoint:        map[string]int{"liveChatMessages.list": 3200, "videos.list": 40},
	}

	state := model.activeChatState()
	state.target.Title = "Launch Day Stream"
	state.status = youtube.ConnectionState{Status: youtube.ConnectionConnected, Detail: "polling"}
	state.live = true
	state.liveSince = model.lastFrameAt.Add(-2 * time.Hour)
	state.viewerCount, state.viewersKnown = 12345, true
	for _, sample := range []struct{ id, author, text string }{
		{"m1", "alice", "first message in the backlog"},
		{"m2", "alice", "and a follow-up from the same author"},
		{"m3", "bob", "a reply from someone else"},
	} {
		state.messages = append(state.messages, testMessage(t, sample.id, "", sample.author, sample.text))
	}
	return model
}

// frameLines renders one frame and returns its plain rows.
func frameLines(t *testing.T, model shellModel) []string {
	t.Helper()
	return strings.Split(ansi.Strip(model.View()), "\n")
}

// Every frame must be exactly the terminal's size. A row too many scrolls the
// user's scrollback on every repaint; a column too many wraps the whole frame.
func TestFrameIsExactlyTheTerminalSize(t *testing.T) {
	sizes := []struct{ width, height int }{
		{1, 1}, {8, 3}, {20, 6}, {40, 12}, {80, 24}, {100, 30}, {160, 50}, {200, 60},
	}
	for _, size := range sizes {
		model := newViewModel(t, size.width, size.height)
		lines := frameLines(t, model)
		if len(lines) != size.height {
			t.Errorf("%dx%d: rendered %d rows", size.width, size.height, len(lines))
			continue
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got != size.width {
				t.Errorf("%dx%d: row %d is %d cells (%q)", size.width, size.height, i, got, line)
				break
			}
		}
	}
}

// The layout solver must account for every row it was given: a region set that
// does not add up is what produces the one-row drift that scrolls a terminal.
func TestLayoutRegionsSumToTheTerminalHeight(t *testing.T) {
	for height := 1; height <= 60; height++ {
		model := newViewModel(t, 100, height)
		layout := model.layout()
		total := layout.tabBarHeight + layout.statusHeight + layout.chatHeight +
			layout.streamInfo.height + layout.misc.height + layout.overlay.height +
			layout.inspect.height + layout.composerHeight + layout.helpHeight
		if total != height {
			t.Fatalf("height %d: regions sum to %d", height, total)
		}
	}
}

// Side panes exist to help chat, never to starve it.
func TestSidePanesNeverStarveChat(t *testing.T) {
	for width := 1; width <= 220; width++ {
		model := newViewModel(t, width, 30)
		model.sidebarVisibility = paneVisibilityShown
		model.activityVisibility = paneVisibilityShown
		layout := model.layout()
		if layout.sidebarWidth+layout.activityWidth >= width && width > 4 {
			t.Fatalf("width %d: panes consumed the whole frame", width)
		}
		if layout.chatWidth < 1 {
			t.Fatalf("width %d: chat width collapsed to %d", width, layout.chatWidth)
		}
	}
}

func TestSidebarAutoShowsOnlyWithASecondChat(t *testing.T) {
	single := newViewModel(t, 120, 30)
	if got := single.layout().sidebarWidth; got != 0 {
		t.Fatalf("one chat auto-showed the sidebar at width %d", got)
	}
	pair := newModelForTest(t, "first", "second")
	pair.width, pair.height = 120, 30
	if got := pair.layout().sidebarWidth; got == 0 {
		t.Fatal("two chats did not auto-show the sidebar")
	}
	// An explicit hide outranks the auto rule in both directions.
	pair.sidebarVisibility = paneVisibilityHidden
	if got := pair.layout().sidebarWidth; got != 0 {
		t.Fatalf("an explicit hide was overridden: width %d", got)
	}
}

// A docked overlay must never take the last row of chat: an overlay that hid
// the thing it operates on would be worse than one that did not open.
func TestDockedOverlayLeavesChatOnScreen(t *testing.T) {
	for height := 6; height <= 40; height++ {
		model := newViewModel(t, 100, height)
		model.overlay = overlayState{kind: overlayPalette}
		layout := model.layout()
		if layout.overlay.height > 0 && layout.chatHeight < 1 {
			t.Fatalf("height %d: overlay %d left chat with %d rows", height, layout.overlay.height, layout.chatHeight)
		}
	}
}

// The theme picker replaces the dashboard rather than docking under it: a
// palette can only be judged on the whole terminal.
func TestThemePickerTakesTheWholeScreen(t *testing.T) {
	model := newViewModel(t, 100, 30)
	model.overlay = overlayState{kind: overlayThemePicker}
	lines := frameLines(t, model)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Themes") {
		t.Fatalf("theme page not rendered:\n%s", joined)
	}
	if strings.Contains(joined, "Launch Day Stream") {
		t.Fatal("the dashboard is still visible behind the theme page")
	}
}

// View must be a pure function of already-ticked state. Rendering twice
// without an intervening Update has to produce the same bytes, or the row cache
// and the golden tests are both meaningless.
func TestViewIsPure(t *testing.T) {
	model := newViewModel(t, 100, 30)
	first := model.View()
	time.Sleep(2 * time.Millisecond)
	if second := model.View(); first != second {
		t.Fatal("two renders of the same state differed, so View reads a live clock")
	}
}

// animation_mode=off must collapse every effect to its static frame without
// changing a word or a cell of the layout.
func TestAnimationOffPreservesWordingAndGeometry(t *testing.T) {
	animated := newViewModel(t, 100, 30)
	static := newViewModel(t, 100, 30)
	static.animationMode = "off"

	animatedLines := frameLines(t, animated)
	staticLines := frameLines(t, static)
	if len(animatedLines) != len(staticLines) {
		t.Fatalf("row counts differ: %d vs %d", len(animatedLines), len(staticLines))
	}
	for i := range animatedLines {
		if ansi.StringWidth(animatedLines[i]) != ansi.StringWidth(staticLines[i]) {
			t.Fatalf("row %d changed width when animation was disabled", i)
		}
	}
}

// The empty state has to name the two ways out. A blank pane teaches nothing.
func TestEmptyStateNamesTheWayOut(t *testing.T) {
	model := newModelForTest(t)
	model.width, model.height = 100, 30
	model.lastFrameAt = time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)
	joined := strings.Join(frameLines(t, model), "\n")
	for _, want := range []string{noChatHeadline, targetPickerKeyHint, "ctrl+p"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("empty state omits %q:\n%s", want, joined)
		}
	}
}

// The idle scanner is dropped rather than frozen when animation is off: a
// stationary marker carries no meaning, unlike the headline's wording.
func TestEmptyStateScannerIsDroppedWhenAnimationIsOff(t *testing.T) {
	model := newModelForTest(t)
	model.width, model.height = 100, 30
	model.animationMode = "off"
	if got := model.emptyStateScanner(60, 0); got != "" {
		t.Fatalf("animation=off still rendered a scanner: %q", got)
	}
	model.animationMode = "fast"
	if got := model.emptyStateScanner(60, time.Second); got == "" {
		t.Fatal("animation=fast rendered no scanner")
	}
}

// Consecutive messages from one author form one visual block; a different
// author starts a new one with a separator between them.
func TestAuthorGroupingJoinsAdjacentMessages(t *testing.T) {
	blocks := []chatRowBlock{
		{message: youtube.Message{ID: "1", Author: youtube.Author{ChannelID: "UC-a"}}},
		{message: youtube.Message{ID: "2", Author: youtube.Author{ChannelID: "UC-a"}}},
		{message: youtube.Message{ID: "3", Author: youtube.Author{ChannelID: "UC-b"}}},
	}
	assignChatAuthorGroups(blocks)
	if blocks[0].separatorBefore {
		t.Fatal("the first block has a separator above it")
	}
	if !blocks[1].continuesGroup {
		t.Fatal("a second message from the same author started a new group")
	}
	if !blocks[2].separatorBefore || blocks[2].continuesGroup {
		t.Fatal("a different author did not start a new group")
	}
}

// Authorless events must not merge: two unrelated Super Chats are two events,
// not one block, even though neither carries an author.
func TestAuthorlessEventsDoNotMerge(t *testing.T) {
	blocks := []chatRowBlock{
		{message: youtube.Message{Kind: youtube.EventKindSuperChat}},
		{message: youtube.Message{Kind: youtube.EventKindSuperChat}},
	}
	assignChatAuthorGroups(blocks)
	if blocks[1].continuesGroup {
		t.Fatal("two authorless events merged into one group")
	}
}

// chatRowCount is the authoritative row count the update lane clamps scrolling
// against, and it is computed without styling anything. If it disagreed with
// what the viewport actually draws, page-up would either stop short of the
// oldest message or scroll past it into blank rows.
func TestChatRowCountMatchesWhatTheViewportDraws(t *testing.T) {
	model := newViewModel(t, 100, 40)
	layout := model.layout()
	drawn := len(model.styleChatRowWindow(model.chatRowBlocks(layout), model.chatRowWidth(layout), 0, -1))
	if got := model.chatRowCount(layout); got != drawn {
		t.Fatalf("chatRowCount = %d, viewport drew %d rows", got, drawn)
	}

	// A filter that hides everything still occupies the one row that explains
	// why the pane is empty.
	model.activeChatState().filters.toggle(messageFilterMentions)
	layout = model.layout()
	if got := model.chatRowCount(layout); got != 1 {
		t.Fatalf("a fully filtered chat counted %d rows, want the explanation row", got)
	}
}

func TestMessageGutterDegradesWithWidth(t *testing.T) {
	tests := map[int]int{40: 4, 24: 4, 23: 2, 12: 2, 11: 0, 1: 0}
	for rowWidth, want := range tests {
		if got := messageGutterWidth(rowWidth); got != want {
			t.Errorf("messageGutterWidth(%d) = %d, want %d", rowWidth, got, want)
		}
	}
}

// A notice has no real author, so it must not be tinted by a fabricated
// identity color.
func TestSystemRowsHaveNoIdentityColor(t *testing.T) {
	model := newViewModel(t, 100, 30)
	notice := youtube.Message{Type: youtube.MessageTypeNotice, Author: youtube.Author{DisplayName: "system"}}
	if got := model.messageAuthorColor(notice); got != "" {
		t.Fatalf("a notice was given the identity color %q", got)
	}
	chat := youtube.Message{Type: youtube.MessageTypeChat, Author: youtube.Author{ChannelID: "UC-alice"}}
	if got := model.messageAuthorColor(chat); got == "" {
		t.Fatal("a chat message got no identity color")
	}
}

// The OSC 11 canvas override belongs in the frame, and only in an interactive
// session: piped output has to stay free of escape codes.
func TestTerminalBackgroundSequenceIsGatedOnInteractivity(t *testing.T) {
	model := newViewModel(t, 100, 30)
	if got := model.themeBackgroundSequence(); got != "" {
		t.Fatalf("non-interactive output emitted %q", got)
	}
	model.terminalOutput = &strings.Builder{}
	if got := model.themeBackgroundSequence(); got == "" {
		t.Fatal("interactive output emitted no background override")
	}
	// The sequence must be zero-width so it perturbs no layout math.
	if got := ansi.StringWidth(ansi.Strip(model.themeBackgroundSequence())); got != 0 {
		t.Fatalf("background override measured %d cells", got)
	}
}

// The quota tab must never present an estimate as a fact.
func TestQuotaTabLabelsItsFiguresAsEstimates(t *testing.T) {
	model := newViewModel(t, 100, 30)
	joined := strings.Join(model.quotaLedgerLines(), "\n")
	if !strings.Contains(joined, "estimate") {
		t.Fatalf("quota tab omits the estimate disclaimer:\n%s", joined)
	}
	if !strings.Contains(joined, "3,240 / 10,000") {
		t.Fatalf("quota tab omits the ledger:\n%s", joined)
	}
	if !strings.Contains(joined, "liveChatMessages.list") {
		t.Fatalf("quota tab omits the per-endpoint tally:\n%s", joined)
	}
}

func TestQuotaTabWithoutALedgerExplainsItself(t *testing.T) {
	model := newViewModel(t, 100, 30)
	model.quotaKnown = false
	joined := strings.Join(model.quotaLedgerLines(), "\n")
	if !strings.Contains(joined, "No quota ledger yet") {
		t.Fatalf("missing ledger is unexplained:\n%s", joined)
	}
}

// An unresolved liveChatId is reported as a state, not printed: the ID adds
// nothing a person can act on and only invites itself into screenshots.
func TestStreamInfoTabDoesNotPrintTheLiveChatID(t *testing.T) {
	model := newViewModel(t, 100, 30)
	model.activeChatState().target.LiveChatID = "Cg0KC3NlY3JldC1pZC0x"
	joined := strings.Join(model.streamInfoLines(), "\n")
	if strings.Contains(joined, "Cg0KC3NlY3JldC1pZC0x") {
		t.Fatalf("stream info printed the live chat ID:\n%s", joined)
	}
	if !strings.Contains(joined, "resolved") {
		t.Fatalf("stream info does not report resolution state:\n%s", joined)
	}
}

// Golden frames. They are asserted as substrings of specific rows rather than
// byte-for-byte, so a palette change does not rewrite the file, but the shape
// of the frame - which region is on which row, and what each one says - is
// pinned.
func TestGoldenWideFrame(t *testing.T) {
	model := newViewModel(t, 120, 32)
	lines := frameLines(t, model)

	wantRow := map[int][]string{
		0: {"*1:Chat", "2:Stream Info", "3:Quota"},
		1: {"⟳ 5.0s", "10,000", "est", "LIVE", "Launch Day Stream", "connected"},
		2: {"┌─ 💬 Chat · Launch Day Stream · 👥 12,345"},
	}
	for row, wants := range wantRow {
		for _, want := range wants {
			if !strings.Contains(lines[row], want) {
				t.Errorf("row %d missing %q:\n%q", row, want, lines[row])
			}
		}
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "⌨") || !strings.Contains(last, "ctrl+p") {
		t.Errorf("help strip is not the last row: %q", last)
	}
}

func TestGoldenNarrowFrame(t *testing.T) {
	model := newViewModel(t, 34, 12)
	lines := frameLines(t, model)

	if len(lines) != 12 {
		t.Fatalf("narrow frame has %d rows", len(lines))
	}
	joined := strings.Join(lines, "\n")
	// Identity and budget survive; the wide decorations do not.
	if !strings.Contains(joined, "Launch Day Stream") {
		t.Errorf("narrow frame lost the chat title:\n%s", joined)
	}
	if !strings.Contains(lines[1], "68%") {
		t.Errorf("narrow frame lost the quota meter: %q", lines[1])
	}
	if strings.Contains(lines[1], "fps=") {
		t.Errorf("narrow frame kept the process metrics: %q", lines[1])
	}
	// Both side panes are gone well before this width.
	layout := model.layout()
	if layout.sidebarWidth != 0 || layout.activityWidth != 0 {
		t.Errorf("narrow frame kept a side pane: sidebar=%d activity=%d", layout.sidebarWidth, layout.activityWidth)
	}
}

func TestGoldenSplashFrame(t *testing.T) {
	model := newViewModel(t, 80, 24)
	model.splashUntil = model.lastFrameAt.Add(5 * time.Second)
	if !model.splashActive() {
		t.Fatal("splash is not active")
	}
	lines := frameLines(t, model)
	if len(lines) != 24 {
		t.Fatalf("splash frame has %d rows", len(lines))
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"██╗   ██╗", "press any key to skip"} {
		if !strings.Contains(joined, want) {
			t.Errorf("splash omits %q:\n%s", want, joined)
		}
	}
}

// Even a terminal too small for the wordmark still shows what yc is doing.
func TestSplashDegradesToOneLine(t *testing.T) {
	model := newViewModel(t, 20, 3)
	model.splashUntil = model.lastFrameAt.Add(5 * time.Second)
	lines := frameLines(t, model)
	if len(lines) != 3 {
		t.Fatalf("tiny splash has %d rows", len(lines))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "yc") {
		t.Fatalf("tiny splash lost the wordmark:\n%s", strings.Join(lines, "\n"))
	}
}

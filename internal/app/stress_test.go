package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/render"
	"github.com/worxbend/yc/internal/youtube"
)

// stressClock is the deterministic time source for the throughput harness.
//
// Every reveal deadline, frame timestamp, and metrics read in the burst derives
// from it, so the whole test advances by arithmetic. A wall-clock sleep here
// would make the reveal queue's depth a function of how loaded the CI box is,
// which is precisely the property the harness exists to pin down.
type stressClock struct {
	now time.Time
}

func (c *stressClock) Now() time.Time { return c.now }

func (c *stressClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// stressStart is a fixed instant so message timestamps, "member for N months"
// meta, and the live-since duration render identically on every machine.
var stressStart = time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)

// stressBurst builds a deterministic, credential-free flood covering every
// normalized event kind plus the text shapes that break naive slicing: ZWJ
// emoji, skin-tone sequences, combining marks, CJK wide runs, RTL, and a body
// far longer than any terminal is wide.
//
// It is generated rather than fixtured because the point is throughput: the
// same twelve rows repeated would exercise the row cache instead of the layout.
func stressBurst(count int) []youtube.Message {
	kinds := []youtube.EventKind{
		youtube.EventKindText,
		youtube.EventKindSuperChat,
		youtube.EventKindSuperSticker,
		youtube.EventKindGift,
		youtube.EventKindNewSponsor,
		youtube.EventKindMemberMilestone,
		youtube.EventKindMembershipGifting,
		youtube.EventKindGiftMembershipReceived,
		youtube.EventKindFanFunding,
		youtube.EventKindPoll,
		youtube.EventKindUserBanned,
		youtube.EventKindMessageDeleted,
		youtube.EventKindMessageRetracted,
		youtube.EventKindSponsorOnlyModeStarted,
		youtube.EventKindSponsorOnlyModeEnded,
		youtube.EventKindTombstone,
		youtube.EventKindInvalidType,
		youtube.EventKindUnknown,
	}
	bodies := []string{
		"plain ascii chat line",
		"family emoji \U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466 stays one grapheme",
		"skin tone 👋🏽 and a flag 🇯🇵",
		"combining marks: ééé must not split",
		"日本語のテキストは全角です、折り返しに注意",
		"مرحبا بالعالم من الطرفية",
		"@you did you catch that play",
		strings.Repeat("a very long body that has to wrap many times ", 8),
		"", // an event whose only content is its structured detail
		"trailing spaces and\ttabs\tinside",
	}
	authors := []youtube.Author{
		{ChannelID: "UC-alice-000000000000001", DisplayName: "Alice Lovelace"},
		{ChannelID: "UC-bob-0000000000000002", DisplayName: "Bob", IsMember: true, MemberLevelName: "Comet Crew", MemberMonths: 14},
		{ChannelID: "UC-carol-000000000000003", DisplayName: "carol_mod", IsModerator: true},
		{ChannelID: "UC-studio-00000000000004", DisplayName: "Studio Nine", IsOwner: true, IsVerified: true},
		{ChannelID: "UC-dave-0000000000000005", DisplayName: "デイブ", IsVerified: true},
	}

	burst := make([]youtube.Message, 0, count)
	for i := 0; i < count; i++ {
		kind := kinds[i%len(kinds)]
		author := authors[i%len(authors)]
		message := youtube.Message{
			ID:         fmt.Sprintf("stress-%04d", i),
			LiveChatID: "chat-stress",
			// Spread across an hour so grouped layout exercises both the
			// same-author continuation and the timestamp-break path.
			Timestamp: stressStart.Add(time.Duration(i) * time.Second),
			Author:    author,
			Badges:    youtube.BadgesForAuthor(author),
			Text:      bodies[i%len(bodies)],
			Kind:      kind,
			Type:      youtube.MessageTypeForKind(kind),
			RawType:   string(kind),
		}
		switch kind {
		case youtube.EventKindSuperChat, youtube.EventKindFanFunding:
			message.SuperChat = &youtube.SuperChatDetails{
				Amount:  youtube.Money{Micros: int64(i+1) * 1_000_000, Currency: "USD"},
				Tier:    i % 12,
				Comment: message.Text,
			}
		case youtube.EventKindSuperSticker:
			message.SuperSticker = &youtube.SuperStickerDetails{
				Amount:    youtube.Money{Micros: 2_000_000, Currency: "EUR"},
				Tier:      2,
				StickerID: "sticker-42",
				AltText:   "a cat waving a tiny flag 🐈",
			}
		case youtube.EventKindGift:
			message.Gift = &youtube.GiftDetails{Name: "Star Shower", Jewels: 250, ComboCount: i % 5}
		case youtube.EventKindNewSponsor:
			message.Membership = &youtube.MembershipDetails{Kind: youtube.MembershipNew, LevelName: "Comet Crew"}
		case youtube.EventKindMemberMilestone:
			message.Membership = &youtube.MembershipDetails{Kind: youtube.MembershipMilestone, LevelName: "Comet Crew", Months: i%36 + 1, Comment: message.Text}
		case youtube.EventKindMembershipGifting:
			message.Membership = &youtube.MembershipDetails{Kind: youtube.MembershipGifting, LevelName: "Comet Crew", GiftCount: i%50 + 1}
		case youtube.EventKindGiftMembershipReceived:
			message.Membership = &youtube.MembershipDetails{Kind: youtube.MembershipGiftReceived, LevelName: "Comet Crew", GifterChannelID: authors[0].ChannelID}
		case youtube.EventKindMessageDeleted, youtube.EventKindMessageRetracted:
			message.Deleted = true
		case youtube.EventKindUnknown:
			message.RawType = "hologramEvent"
		}
		burst = append(burst, message)
	}
	return burst
}

// newStressModel builds a live shell wired to the deterministic fake client.
func newStressModel(t *testing.T, clock *stressClock, animationMode string, width, height int) (shellModel, *FakeChatClient) {
	t.Helper()
	cfg := config.Default()
	cfg.Features.AnimationMode = animationMode
	cfg.Features.ScrollbackLimit = stressScrollback
	cfg.Features.HighlightEmoji = true
	cfg.DefaultChats = []string{"chat-stress"}

	client := NewFakeChatClient()
	t.Cleanup(func() { _ = client.Close() })

	model := newLiveModel(cfg, client, clock, ClientOptions{})
	model.width, model.height = width, height
	model.lastFrameAt = clock.now
	// The burst is stamped with this chat ID; without it every message routes
	// through the "unstamped event belongs to the active chat" fallback and
	// the multi-chat paths never run.
	state := model.activeChatState()
	state.target.LiveChatID = "chat-stress"
	state.status = youtube.ConnectionState{Status: youtube.ConnectionConnected, Detail: "polling"}
	return model, client
}

// stressScrollback is small enough that trimming actually runs during the burst
// and large enough that a page of history survives it.
const stressScrollback = 200

// feed drives one transport message through Update exactly as the runtime would.
func feedStress(t *testing.T, model shellModel, message youtube.Message) shellModel {
	t.Helper()
	next, _ := model.Update(chatClientMessageMsg{message: message, ok: true})
	updated, ok := next.(shellModel)
	if !ok {
		t.Fatalf("Update returned %T, want shellModel", next)
	}
	return updated
}

// TestLiveShellHighThroughputChatStressHarness floods the live shell with a
// deterministic burst covering every event kind and every text shape that
// breaks naive slicing, while resizing, scrolling, typing, and sending.
//
// It is the one test that asserts the properties which only fail under load:
// the reveal queue staying bounded, the frame staying rectangular at every
// width the burst passes through, no message being silently lost, and the
// composer still accepting input while the flood is arriving. It runs on
// arithmetic alone - a fake clock, no goroutines, no wall-clock sleeps - so a
// failure is a real regression rather than a slow machine.
func TestLiveShellHighThroughputChatStressHarness(t *testing.T) {
	clock := &stressClock{now: stressStart}
	model, client := newStressModel(t, clock, "fast", 100, 30)

	// Widths the burst passes through, including the minimum yc supports and a
	// width narrower than the sidebar would like.
	widths := []int{100, 40, render.MinimumRenderWidth + 2, 72, 160}

	// --- phase one: undisturbed flood ---------------------------------------
	//
	// The queue bound is only meaningful while messages are actually animating,
	// so this phase does nothing but deliver. Scrolling away, which phase two
	// does, legitimately routes messages down the static path and would hide
	// whether the bound holds at all.
	maxQueue := 0
	for i, message := range stressBurst(300) {
		model = feedStress(t, model, message)

		state := model.activeChatState()
		depth := state.revealQueue.Len()
		if depth > maxQueue {
			maxQueue = depth
		}
		// The bound is the whole contract: overflow completes the oldest
		// reveal rather than growing the queue or dropping the message.
		if depth > animation.DefaultMaxQueued {
			t.Fatalf("message %d: reveal queue grew to %d, above the bound of %d", i, depth, animation.DefaultMaxQueued)
		}
		if depth != len(state.activeOrder) || depth != len(state.activeMessages) {
			t.Fatalf("message %d: queue holds %d but the model tracks %d ordered / %d messages",
				i, depth, len(state.activeOrder), len(state.activeMessages))
		}
		// Rendering during the burst is what catches an over-wide row: a frame
		// is only correct if it is correct while the queue is full.
		if i%13 == 0 {
			assertRectangularFrame(t, model, fmt.Sprintf("during flood at message %d", i))
		}
	}

	if maxQueue != animation.DefaultMaxQueued {
		t.Fatalf("reveal queue peaked at %d of %d; the flood never saturated it and the bound went unasserted",
			maxQueue, animation.DefaultMaxQueued)
	}
	if model.activeChatState().revealQueue.OverflowCount() == 0 {
		t.Fatal("300 messages through a 32-slot queue completed nothing early; the overflow path never ran")
	}

	// --- phase two: flood plus interaction ----------------------------------
	//
	// Resizes, scrolling, and typing interleaved with delivery. Scrolling away
	// deliberately sends messages down the static-append path, so this phase
	// asserts responsiveness and geometry rather than queue depth.
	typed := 0
	scrolledAwayDeliveries := 0
	for i, message := range stressBurst(300) {
		message.ID = "phase2-" + message.ID
		if model.scrolledAway() {
			scrolledAwayDeliveries++
		}
		model = feedStress(t, model, message)

		if got := model.activeChatState().revealQueue.Len(); got > animation.DefaultMaxQueued {
			t.Fatalf("message %d: reveal queue grew to %d during interaction", i, got)
		}

		switch {
		case i%37 == 0:
			// Resize mid-flood. Rows already revealing must be re-measured
			// against the new width rather than restarted or left over-wide.
			size := tea.WindowSizeMsg{Width: widths[(i/37)%len(widths)], Height: 24 + (i/37)%12}
			next, _ := model.Update(size)
			model = next.(shellModel)
		case i%23 == 0:
			// Advance the reveal animation by a real tick's worth of clock.
			clock.advance(2 * animation.DefaultFrameInterval)
			next, _ := model.Update(revealTickMsg{})
			model = next.(shellModel)
		case i%17 == 0:
			next, _ := model.Update(key(tea.KeyPgUp))
			model = next.(shellModel)
		case i%11 == 0:
			next, _ := model.Update(key(tea.KeyEnd))
			model = next.(shellModel)
		case i%7 == 0:
			// Input responsiveness: a keystroke during the flood must land in
			// the composer, not be swallowed by the message path.
			if model.focus != focusComposer {
				next, _ := model.Update(runeKey('i'))
				model = next.(shellModel)
			}
			next, _ := model.Update(runeKey('x'))
			model = next.(shellModel)
			typed++
		}

		if i%13 == 0 {
			assertRectangularFrame(t, model, fmt.Sprintf("during interaction at message %d", i))
		}
	}

	if typed == 0 {
		t.Fatal("the harness never typed anything; input responsiveness went unasserted")
	}
	if scrolledAwayDeliveries == 0 {
		t.Fatal("no message arrived while scrolled away; the static-append path went unasserted")
	}

	// --- accounting ---------------------------------------------------------
	//
	// Every fed message must be either in history, mid-reveal, or deliberately
	// trimmed. Anything else is a silent loss, which is the failure mode a
	// moderator cannot detect for themselves.
	state := model.activeChatState()
	if got := len(state.messages); got > stressScrollback {
		t.Errorf("history holds %d messages, above the scrollback limit of %d", got, stressScrollback)
	}
	if len(state.activeMessages) != len(state.activeOrder) {
		t.Errorf("reveal bookkeeping diverged: %d messages vs %d ordered IDs",
			len(state.activeMessages), len(state.activeOrder))
	}
	if got := state.revealQueue.Len(); got != len(state.activeOrder) {
		t.Errorf("queue holds %d reveals but the model tracks %d", got, len(state.activeOrder))
	}

	// --- composer -----------------------------------------------------------
	if got := model.activeChatState().composerText; len(got) != typed || strings.Trim(got, "x") != "" {
		t.Errorf("composer holds %q after %d keystrokes; input was dropped during the flood", got, typed)
	}

	// --- send during the flood ---------------------------------------------
	client.QueueSendResult(youtube.SendResult{MessageID: "sent-1", AcceptedAt: clock.now}, nil)
	next, cmd := model.Update(key(tea.KeyEnter))
	model = next.(shellModel)
	if cmd == nil {
		t.Fatal("enter produced no send command while the burst was in flight")
	}
	completion, ok := cmd().(composerSendCompletedMsg)
	if !ok {
		t.Fatalf("send command produced %T, want a completion", cmd())
	}
	next, _ = model.Update(completion)
	model = next.(shellModel)
	if model.activeChatState().sendState != composerSendSucceeded {
		t.Errorf("send state = %v, want accepted", model.activeChatState().sendState)
	}
	if got := len(client.SentRequests()); got != 1 {
		t.Errorf("dispatched %d sends, want 1", got)
	}

	// --- layout stability at every width -----------------------------------
	//
	// The model is still loaded here - full history, live reveals, a send
	// result in the status line - so this renders a working shell rather than a
	// quiescent one.
	for _, width := range append(widths, 8, 200) {
		for _, height := range []int{3, 24, 60} {
			resized, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
			assertRectangularFrame(t, resized.(shellModel), fmt.Sprintf("post-burst at %dx%d", width, height))
		}
	}
}

// A tab is measured as zero cells by every width-aware API yc uses and expanded
// to four spaces by lipgloss at render time. Chat text is fully API-supplied, so
// one tab in a message that reaches the status line's notification summary, or a
// broadcast title in the sidebar, silently makes the drawn frame wider than the
// frame the layout budgeted - which wraps it, adds a row, and scrolls the user's
// terminal on every repaint.
func TestControlCharactersInAPITextCannotWidenAFrame(t *testing.T) {
	hostile := []string{
		"tabs\there\tand\there",
		"a newline\nin one line",
		"a vertical tab \v and a form feed \f",
		"bidi override \u202eevil\u202c text",
		"carriage\rreturn",
	}
	for _, text := range hostile {
		t.Run(strings.Fields(text + " x")[0], func(t *testing.T) {
			clock := &stressClock{now: stressStart}
			model, _ := newStressModel(t, clock, "off", 120, 12)

			// Every app-drawn surface that quotes API text at once.
			state := model.activeChatState()
			state.target.Title = text
			state.status = youtube.ConnectionState{Status: youtube.ConnectionConnected, Detail: text}
			state.sendFeedback = text
			model.lastNotification = &SystemNotification{Title: text, Body: text}
			model.sidebarVisibility = paneVisibilityShown
			model.activityVisibility = paneVisibilityShown

			message := stressBurst(1)[0]
			message.Text = text
			message.Author.DisplayName = text
			model = feedStress(t, model, message)

			for _, width := range []int{40, 80, 120, 200} {
				for _, height := range []int{3, 12, 40} {
					resized, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
					assertRectangularFrame(t, resized.(shellModel), fmt.Sprintf("%q at %dx%d", text, width, height))
				}
			}
		})
	}
}

// The single-line fitters are the choke point that guarantee the above, so they
// are pinned directly: a tab must occupy the cells it will be drawn with.
func TestSingleLineFittersFlattenControlCharacters(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"tab", "a\tb", "a b"},
		{"newline", "a\nb", "a b"},
		{"carriage return", "a\rb", "a b"},
		{"bidi override dropped", "a\u202eb", "ab"},
		{"clean text untouched", "héllo 👋🏽", "héllo 👋🏽"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := revealDisplayCells(tc.value, 64); got != tc.want {
				t.Errorf("revealDisplayCells = %q, want %q", got, tc.want)
			}
			if got := strings.TrimRight(fitLine(tc.value, 64), " "); got != strings.TrimRight(tc.want, " ") {
				t.Errorf("fitLine = %q, want %q", got, tc.want)
			}
			if got := tailDisplayCells(tc.value, 64); got != tc.want {
				t.Errorf("tailDisplayCells = %q, want %q", got, tc.want)
			}
			// fitLine's contract is an exact width; a control character that
			// survived would make the drawn row wider than the measured one.
			if got := ansi.StringWidth(fitLine(tc.value, 20)); got != 20 {
				t.Errorf("fitLine width = %d, want 20", got)
			}
		})
	}
}

// assertRectangularFrame checks that a frame is exactly the terminal's size.
//
// A row too many scrolls the user's scrollback on every repaint; a column too
// many wraps the whole frame and desynchronizes scroll arithmetic from what is
// actually on screen. Under load is the only time this breaks.
func assertRectangularFrame(t *testing.T, model shellModel, context string) {
	t.Helper()
	lines := strings.Split(ansi.Strip(model.View()), "\n")
	if len(lines) != model.height {
		t.Fatalf("%s: frame is %d rows, want %d", context, len(lines), model.height)
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != model.width {
			t.Fatalf("%s: row %d is %d cells, want %d: %q", context, i, got, model.width, line)
		}
	}
}

// Reduced and off modes must survive the same flood. "off" in particular takes
// the static-append path for every message, so a bound that only holds because
// the queue is being drained would show up here as an unbounded history.
func TestHighThroughputBurstIsBoundedInEveryAnimationMode(t *testing.T) {
	for _, mode := range []string{"off", "reduced", "fast"} {
		t.Run(mode, func(t *testing.T) {
			clock := &stressClock{now: stressStart}
			model, _ := newStressModel(t, clock, mode, 96, 28)

			for _, message := range stressBurst(400) {
				model = feedStress(t, model, message)
				clock.advance(5 * time.Millisecond)

				state := model.activeChatState()
				if got := state.revealQueue.Len(); got > animation.DefaultMaxQueued {
					t.Fatalf("reveal queue grew to %d in %s mode", got, mode)
				}
				if got := len(state.messages); got > stressScrollback {
					t.Fatalf("history grew to %d in %s mode, above the limit of %d", got, mode, stressScrollback)
				}
			}

			if mode == "off" && model.activeChatState().revealQueue.Len() != 0 {
				t.Error("animation off must never hold a reveal")
			}
			assertRectangularFrame(t, model, mode+" mode after the burst")
		})
	}
}

// A burst aimed at a background chat must not animate at all: a reveal queue
// growing for a chat nobody is looking at is a backlog that only becomes
// visible when the user switches to it.
func TestBackgroundChatFloodNeverAnimates(t *testing.T) {
	clock := &stressClock{now: stressStart}
	cfg := config.Default()
	cfg.Features.AnimationMode = "fast"
	cfg.Features.ScrollbackLimit = stressScrollback
	cfg.DefaultChats = []string{"foreground", "background"}

	client := NewFakeChatClient()
	t.Cleanup(func() { _ = client.Close() })
	model := newLiveModel(cfg, client, clock, ClientOptions{})
	model.width, model.height = 120, 40

	keys := model.chats.chatKeys()
	if len(keys) != 2 {
		t.Fatalf("opened %d chats, want 2", len(keys))
	}
	foreground := model.chats.ensureKey(keys[0])
	background := model.chats.ensureKey(keys[1])
	foreground.target.LiveChatID = "chat-foreground"
	background.target.LiveChatID = "chat-background"

	for _, message := range stressBurst(300) {
		message.LiveChatID = "chat-background"
		model = feedStress(t, model, message)
	}

	if got := background.revealQueue.Len(); got != 0 {
		t.Errorf("background chat holds %d reveals; an off-screen flood must append statically", got)
	}
	if got := background.unread; got != 300 {
		t.Errorf("background unread = %d, want 300", got)
	}
	if got := len(foreground.messages); got != 0 {
		t.Errorf("foreground chat received %d messages meant for the background chat", got)
	}
	assertRectangularFrame(t, model, "after a background flood")
}

// Reveal frames are grapheme units, never bytes or runes. Under a flood of
// emoji and combining marks a partial frame is where a split cluster would
// first appear as a replacement character or a broken width.
func TestPartialRevealFramesStayGraphemeSafeUnderLoad(t *testing.T) {
	clock := &stressClock{now: stressStart}
	model, _ := newStressModel(t, clock, "fast", 80, 24)

	for _, message := range stressBurst(120) {
		model = feedStress(t, model, message)
		clock.advance(animation.DefaultFrameInterval)
		next, _ := model.Update(revealTickMsg{})
		model = next.(shellModel)

		order, frames := model.revealFrames()
		for _, id := range order {
			for _, row := range frames[id] {
				plain := row.Plain()
				if strings.ContainsRune(plain, '�') {
					t.Fatalf("partial reveal %q split a grapheme cluster: %q", id, plain)
				}
				if got, want := ansi.StringWidth(plain), row.Width(); got != want {
					t.Fatalf("partial reveal %q measures %d cells but reports %d: %q", id, got, want, plain)
				}
			}
		}
	}
}

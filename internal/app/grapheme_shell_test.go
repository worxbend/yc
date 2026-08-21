package app

import (
	"strconv"
	"strings"
	"testing"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/worxbend/yc/internal/render"
	"github.com/worxbend/yc/internal/youtube"
)

// Grapheme and width safety for the whole shell, not just the rows.
//
// internal/render pins the message rows. The shell puts those rows inside
// panes and then draws the same attacker-supplied display names in five more
// places that measure and truncate independently: the sidebar chat list, the
// activity column, the mention completion strip, the composer's reply context,
// and the inspect panel. Each of those is its own width budget, and each of
// them is a place a CJK name or a family emoji can push a frame one cell wide -
// which wraps the terminal and desynchronizes the scroll arithmetic from what
// the user is looking at.

// hostileNames are display names chosen to break naive measurement. They are
// duplicated from the render package's list on purpose: these have to travel
// through the app's own budgets, and a shared fixture would hide the day the
// two packages stop agreeing about what a hard name is.
//
// The U+FE0F emoji-presentation class is in the list - keycaps like "1️⃣",
// and symbols that are narrow until a variation selector widens them. It used
// to be excluded, because the tree held two width primitives that disagreed
// about it: internal/render measured with uniseg.StringWidth (one cell) while
// lipgloss composed the panes with ansi.StringWidth (two cells), so the shell
// budgeted a row by one measurement and drew it with the other, and any frame
// carrying such a name doubled in height and scrolled the user's terminal on
// every repaint. Everything measures with ansi.StringWidth now, so these are
// ordinary hard names, and their absence would hide a regression back to the
// split.
var hostileNames = []string{
	"山田太郎のチャンネルです",
	"\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466",
	"\U0001F3F3\ufe0f\u200d\U0001F308 Pride",
	"\U0001F44B\U0001F3FD Waver",
	"\U0001F1EF\U0001F1F5 Japan",
	"éléonore",
	"ź̧̖́ stacked",
	"☺\ufe0f Smiler",
	"1\ufe0f\u20e3 First",
	"#\ufe0f\u20e3 Hash",
	"\u26a0\ufe0f Warned",
	"مرحبا بالعالم",
	"中",
	"\u200b\u200b\u200b",
	"a name that is very much longer than any author column will ever be",
}

// hostileBodies are message bodies with the same properties.
var hostileBodies = []string{
	"\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466 the whole family said hello",
	strings.Repeat("日本語のテキスト", 12),
	strings.Repeat("é", 80),
	strings.Repeat("\U0001F1EF\U0001F1F5", 40),
	"mixed 中文 and \U0001F44B\U0001F3FD and é in one line",
	"@you 山田さん did you see that",
	"",
}

// assertNoBrokenCluster fails on the two signatures a split grapheme leaves in
// a drawn frame: a replacement character, and a cluster that begins with an
// orphaned combining mark or ends in a dangling zero-width joiner.
func assertNoBrokenCluster(t *testing.T, context, frame string) {
	t.Helper()
	if strings.ContainsRune(frame, '�') {
		t.Fatalf("%s: the frame contains a replacement character:\n%s", context, frame)
	}
	graphemes := uniseg.NewGraphemes(frame)
	for graphemes.Next() {
		runes := []rune(graphemes.Str())
		if len(runes) == 0 {
			continue
		}
		if unicode.Is(unicode.Mn, runes[0]) {
			t.Fatalf("%s: cluster %q starts with an orphaned combining mark:\n%s",
				context, graphemes.Str(), frame)
		}
		if runes[len(runes)-1] == '\u200d' {
			t.Fatalf("%s: cluster %q ends in a dangling zero-width joiner:\n%s",
				context, graphemes.Str(), frame)
		}
	}
}

// hostileShellModel is a live-shaped shell carrying every hostile name and body
// at once, with the side panes populated so every independent width budget is
// exercised in the same frame.
func hostileShellModel(t *testing.T, width, height int) shellModel {
	t.Helper()
	model := newModelForTest(t, "hostile-a", "hostile-b")
	model.width, model.height = width, height
	model.mentionHandle = "you"
	model.activityVisibility = paneVisibilityShown
	model.sidebarVisibility = paneVisibilityShown

	keys := model.chats.chatKeys()
	// A hostile broadcast title on a chat the sidebar has to label.
	model.chats.stateForKey(keys[1]).target.Title = hostileNames[0]
	model.chats.stateForKey(keys[1]).target.LiveChatID = keys[1]

	state := model.chats.stateForKey(keys[0])
	state.target.LiveChatID = keys[0]
	state.target.Title = "\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466 Launch 日本語 Stream"
	state.status = youtube.ConnectionState{Status: youtube.ConnectionConnected, Detail: "polling"}

	for index, name := range hostileNames {
		body := hostileBodies[index%len(hostileBodies)]
		message := testMessage(t, "hostile-"+strconv.Itoa(index), keys[0], name, body)
		message.Author.ChannelID = "UC-hostile-" + strconv.Itoa(index)
		message.Author.DisplayName = name
		message.Badges = youtube.BadgesForAuthor(message.Author)
		// Spread the paid and membership kinds across the corpus so the
		// activity column, which has its own budget, is populated too.
		switch index % 4 {
		case 1:
			message.Kind = youtube.EventKindSuperChat
			message.Type = youtube.MessageTypeForKind(message.Kind)
			message.SuperChat = &youtube.SuperChatDetails{
				Amount:  youtube.Money{Micros: int64(index+1) * 1_000_000, Currency: "JPY"},
				Tier:    index % 12,
				Comment: body,
			}
		case 2:
			message.Kind = youtube.EventKindNewSponsor
			message.Type = youtube.MessageTypeForKind(message.Kind)
			message.Membership = &youtube.MembershipDetails{
				Kind: youtube.MembershipNew, LevelName: "コメット隊",
			}
		}
		state.messages = append(state.messages, message)
		state.roster.observe(message)
	}
	state.recordModeration(youtube.ModerationEvent{
		LiveChatID:        keys[0],
		Type:              youtube.ModerationUserBanned,
		TargetDisplayName: hostileNames[1],
	})
	state.selected = replyContextFromMessage(state.messages[len(state.messages)-1])
	return model
}

// The whole shell, every layout, every badge mode, every width from the
// narrowest supported terminal upward, with hostile names in every pane at
// once. The frame must be exactly the terminal's size and must contain no
// evidence of a cluster cut in half.
func TestHostileNamesNeverBreakTheFrame(t *testing.T) {
	for _, layout := range allLayouts {
		for _, badges := range []render.BadgeMode{render.BadgeModeGlyph, render.BadgeModeText, render.BadgeModeOff} {
			for width := render.MinimumRenderWidth; width <= 140; width += 3 {
				for _, height := range []int{6, 20, 36} {
					model := hostileShellModel(t, width, height)
					model.messageLayout = layout
					model.badgeMode = badges
					model.fullUsername = true

					context := string(layout) + "/" + string(badges) + "/" +
						strconv.Itoa(width) + "x" + strconv.Itoa(height)
					assertRectangularFrame(t, model, context)
					assertNoBrokenCluster(t, context, ansi.Strip(model.View()))
				}
			}
		}
	}
}

// Every pane draws its own share of the same hostile names, so each is opened
// in turn and the frame re-checked. A pane that is never on screen during the
// sweep above is a pane whose budget is never asserted.
func TestEveryPaneSurvivesHostileNames(t *testing.T) {
	panes := []struct {
		name string
		open func(*shellModel)
	}{
		{"inspect", func(m *shellModel) { m.activeChatState().inspectOpen = true }},
		{"reply", func(m *shellModel) {
			state := m.activeChatState()
			state.replyTo = state.selected
		}},
		{"palette", func(m *shellModel) { m.toggleOverlay(overlayPalette) }},
		{"emoji picker", func(m *shellModel) { m.toggleOverlay(overlayEmojiPicker) }},
		{"theme picker", func(m *shellModel) { m.toggleOverlay(overlayThemePicker) }},
		{"stream info", func(m *shellModel) { m.activeTab = tabStreamInfo }},
		{"quota", func(m *shellModel) { m.activeTab = tabMisc }},
		{"expanded help", func(m *shellModel) { m.helpExpanded = true }},
	}

	for _, pane := range panes {
		for _, size := range []struct{ width, height int }{
			{render.MinimumRenderWidth, 6}, {40, 12}, {62, 20}, {100, 30}, {160, 44},
		} {
			model := hostileShellModel(t, size.width, size.height)
			pane.open(&model)
			context := pane.name + " at " + strconv.Itoa(size.width) + "x" + strconv.Itoa(size.height)
			assertRectangularFrame(t, model, context)
			assertNoBrokenCluster(t, context, ansi.Strip(model.View()))
		}
	}
}

// Mention completion inserts somebody else's display name into the user's own
// draft. It is the one path where a hostile name stops being read-only text and
// becomes something yc types on the user's behalf, so a name inserted half a
// cluster at a time would corrupt the message that is actually sent.
func TestMentionCompletionInsertsWholeClusters(t *testing.T) {
	for _, name := range hostileNames {
		if strings.TrimSpace(name) == "" {
			continue
		}
		model := newModelForTest(t, "mentions")
		model.width, model.height = 100, 30
		state := model.activeChatState()
		state.target.LiveChatID = "mentions"
		message := testMessage(t, "m1", "mentions", name, "hello")
		message.Author.DisplayName = name
		message.Author.ChannelID = "UC-hostile"
		state.messages = append(state.messages, message)
		state.roster.observe(message)

		// Open the composer and type the trigger plus the name's first
		// cluster, which is what a user reaching for the completion does.
		model = press(t, model, runeKey('i'))
		model = press(t, model, runeKey('@'))
		first := firstCluster(name)
		for _, r := range first {
			model = press(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}

		draft := model.activeChatState().composerText
		if !strings.HasPrefix(draft, "@"+first) {
			t.Fatalf("%q: the composer holds %q, want it to start with %q", name, draft, "@"+first)
		}
		assertNoBrokenCluster(t, "composer draft for "+name, draft)

		// Whatever the completion strip decides to show, the frame stays exact
		// and prints no half cluster.
		for _, width := range []int{render.MinimumRenderWidth, 40, 62, 100, 160} {
			model.width = width
			context := "mention strip for " + name + " at width " + strconv.Itoa(width)
			assertRectangularFrame(t, model, context)
			assertNoBrokenCluster(t, context, ansi.Strip(model.View()))
		}

		// Accepting the completion must leave a draft made of whole clusters
		// that still contains the trigger it replaced.
		accepted := press(t, model, key(tea.KeyTab))
		completed := accepted.activeChatState().composerText
		assertNoBrokenCluster(t, "accepted mention for "+name, completed)
		if !strings.HasPrefix(completed, "@") {
			t.Fatalf("%q: accepting the completion left %q, which lost the trigger", name, completed)
		}
	}
}

// firstCluster is the first grapheme of a string, which is what a user types
// before reaching for completion.
func firstCluster(value string) string {
	graphemes := uniseg.NewGraphemes(value)
	if graphemes.Next() {
		return graphemes.Str()
	}
	return ""
}

// Typing a hostile name into the composer and deleting it again must be
// reversible one keystroke per cluster. Backspace that removes a byte or a rune
// leaves half a sequence in the draft, which is then what gets sent.
func TestComposerEditingOfHostileTextIsClusterAtATime(t *testing.T) {
	for _, name := range hostileNames {
		if strings.TrimSpace(name) == "" {
			continue
		}
		model := newModelForTest(t, "composer")
		model.width, model.height = 100, 30
		model = press(t, model, runeKey('i'))

		clusters := 0
		graphemes := uniseg.NewGraphemes(name)
		for graphemes.Next() {
			cluster := graphemes.Str()
			model = press(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(cluster)})
			clusters++
		}
		if got := model.activeChatState().composerText; got != name {
			t.Fatalf("typing %q produced %q", name, got)
		}

		for i := 0; i < clusters; i++ {
			model = press(t, model, key(tea.KeyBackspace))
			assertNoBrokenCluster(t, "draft while deleting "+name,
				model.activeChatState().composerText)
		}
		if got := model.activeChatState().composerText; got != "" {
			t.Fatalf("%d backspaces over %d clusters left %q", clusters, clusters, got)
		}
	}
}

// A hostile name in a system notification is text yc hands to another program.
// It must be bounded and free of half clusters whatever the name turns out to
// be, since the notification daemon does its own measuring.
func TestNotificationTextFromHostileNamesIsWholeAndBounded(t *testing.T) {
	model := hostileShellModel(t, 100, 30)
	state := model.activeChatState()

	var built int
	for _, message := range state.messages {
		notification, ok := notificationFromMessage(message, model.activeChatLabel())
		if !ok {
			continue
		}
		built++
		context := "notification for " + message.Author.DisplayName
		assertNoBrokenCluster(t, context+" title", sanitizeNotificationText(notification.Title, 96))
		assertNoBrokenCluster(t, context+" body", sanitizeNotificationText(notification.Body, 320))
		assertNoBrokenCluster(t, context+" summary", notificationSummary(notification))
	}
	if built == 0 {
		t.Fatal("no hostile message produced a notification; the corpus no longer covers the path")
	}
}

// The notification limits are applied to text that is very often a long run of
// emoji, so the cut has to land between clusters. A rune-sliced limit leaves a
// dangling zero-width joiner in a string yc then hands to another program.
func TestNotificationTruncationCutsBetweenClusters(t *testing.T) {
	values := []string{
		strings.Repeat("\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466", 40),
		strings.Repeat("\U0001F3F3\ufe0f\u200d\U0001F308", 60),
		strings.Repeat("é", 400),
		strings.Repeat("1\ufe0f⃣", 200),
		strings.Repeat("中文", 300),
	}
	for _, value := range values {
		for _, limit := range []int{4, 12, 32, 96, 320} {
			got := sanitizeNotificationText(value, limit)
			assertNoBrokenCluster(t, "sanitizeNotificationText at limit "+strconv.Itoa(limit), got)
			if ansi.StringWidth(got) > 2*limit {
				t.Fatalf("limit %d produced %d cells", limit, ansi.StringWidth(got))
			}
		}
	}
}

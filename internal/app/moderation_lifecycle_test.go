package app

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/youtube"
)

// The moderation lifecycle: what happens between arming an action and the
// transport answering, and what happens when the world moves underneath it.
//
// moderation_test.go covers the happy paths and the refusals. These are the
// states in between - a message still mid-reveal, a request already on the
// wire, a chat that closed while yc was waiting - where the optimistic
// redaction has already changed the screen and something has to reconcile it.

// arm walks the model up to an armed confirmation for one action, asserting on
// the way that the confirmation really is armed. Tests that then press the key
// again are exercising the commit, not the arming.
func armModeration(t *testing.T, model shellModel, key rune) shellModel {
	t.Helper()
	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	if model.moderation.stage != moderationStageConfirm {
		t.Fatalf("pressing %q left stage %v, want an armed confirmation (feedback %q)",
			key, model.moderation.stage, model.moderationNote.text)
	}
	return model
}

// commitArmed confirms an armed action and returns the model plus the command
// the confirmation produced, without running it. Holding the command back is
// what makes the in-flight window observable.
func commitArmed(t *testing.T, model shellModel, key rune) (shellModel, tea.Cmd) {
	t.Helper()
	model, cmd := pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	if cmd == nil {
		t.Fatalf("confirming %q produced no command (feedback %q)", key, model.moderationNote.text)
	}
	if model.moderation.stage != moderationStageInFlight {
		t.Fatalf("after confirming %q stage is %v, want in-flight", key, model.moderation.stage)
	}
	return model, cmd
}

// midRevealMessage files a message into the reveal queue's collection rather
// than into settled history, which is where a row lives during the fraction of
// a second it is animating in.
//
// The reveal key is the "<id>#<seq>" form the shell actually synthesizes, not
// the bare message ID: the collection is keyed by reveal, and a redaction that
// matched on the map key instead of on the message would silently miss every
// real animating row while passing against a bare-ID fixture.
func midRevealMessage(t *testing.T, state *chatState, message youtube.Message) {
	t.Helper()
	revealID := message.ID + "#" + strconv.Itoa(len(state.activeOrder)+1)
	state.activeOrder = append(state.activeOrder, revealID)
	state.activeMessages[revealID] = message
}

// midRevealByID finds an animating row by the message ID it carries, since the
// collection is keyed by reveal rather than by message.
func midRevealByID(state *chatState, id string) (youtube.Message, bool) {
	for _, message := range state.activeMessages {
		if message.ID == id {
			return message, true
		}
	}
	return youtube.Message{}, false
}

// A ban removes everything that channel said, and "everything" has to include
// the row that is still animating in. Redacting only settled history would
// leave the banned chatter's newest line finishing its reveal on screen - on a
// terminal that is very likely being streamed - seconds after the ban landed.
func TestBanRedactsAMessageThatIsStillRevealing(t *testing.T) {
	model, client := moderationModel(t)
	state := model.activeChatState()
	revealing := testMessage(t, "m4", "launch-day", "badactor", "still animating in")
	midRevealMessage(t, state, revealing)

	model = armModeration(t, model, moderationBanRune)
	model, cmd := commitArmed(t, model, moderationBanRune)

	live, ok := midRevealByID(state, "m4")
	if !ok {
		t.Fatal("the mid-reveal message vanished instead of being redacted")
	}
	if !live.Deleted || live.Text != "" || live.Fragments != nil {
		t.Fatalf("mid-reveal message was not redacted: %+v", live)
	}
	// The words must be gone from the collection the renderer reads, not
	// merely flagged.
	if strings.Contains(live.Text, "animating") {
		t.Fatalf("mid-reveal text survived the ban: %q", live.Text)
	}
	var restored bool
	for _, entry := range model.moderation.rollback {
		if strings.HasPrefix(entry.id, "m4#") {
			restored = entry.active
		}
	}
	if !restored {
		t.Fatalf("the mid-reveal row was not recorded as rollback-able: %+v", model.moderation.rollback)
	}

	model = runModerationCmd(t, model, cmd)
	if len(client.bans) != 1 {
		t.Fatalf("bans = %d, want 1", len(client.bans))
	}
	if live, _ := midRevealByID(state, "m4"); !live.Deleted || live.Text != "" {
		t.Fatalf("a successful ban un-redacted the mid-reveal row: %+v", live)
	}
}

// And when the ban is refused, the animating row has to come back exactly as it
// was: the message is still live on YouTube and still visible to the audience,
// so a terminal that keeps it blanked is a terminal that disagrees with the
// broadcast the moderator is watching.
func TestRollbackRestoresAMessageThatWasStillRevealing(t *testing.T) {
	model, client := moderationModel(t)
	client.banErr = errors.New("insufficient permissions")
	state := model.activeChatState()
	revealing := testMessage(t, "m4", "launch-day", "badactor", "still animating in")
	revealing.Fragments = []youtube.MessageFragment{{Type: youtube.FragmentText, Text: "still animating in"}}
	midRevealMessage(t, state, revealing)

	model = armModeration(t, model, moderationBanRune)
	model, cmd := commitArmed(t, model, moderationBanRune)
	model = runModerationCmd(t, model, cmd)

	live, ok := midRevealByID(state, "m4")
	if !ok {
		t.Fatal("the mid-reveal message was dropped by the rollback")
	}
	if live.Deleted {
		t.Fatal("the mid-reveal message is still marked deleted after a failed ban")
	}
	if live.Text != "still animating in" {
		t.Fatalf("restored text = %q, want it byte-identical", live.Text)
	}
	if len(live.Fragments) != 1 || live.Fragments[0].Text != "still animating in" {
		t.Fatalf("restored fragments = %+v, want the originals", live.Fragments)
	}
	// The settled rows come back too, and the failure line neither reprints
	// the words nor claims a removal happened.
	for _, message := range state.messages {
		if message.Author.DisplayName == "badactor" && message.Deleted {
			t.Fatalf("settled row %q stayed redacted after a failed ban", message.ID)
		}
	}
	line := model.moderationNote.text
	if !strings.Contains(line, "nothing was removed") {
		t.Fatalf("failure line does not say the rows came back: %q", line)
	}
	if strings.Contains(line, "animating") {
		t.Fatalf("failure line reprinted the removed text: %q", line)
	}
	if model.moderationNote.level != moderationLevelError {
		t.Fatalf("failure level = %v, want error", model.moderationNote.level)
	}
}

// Two overlapping optimistic redactions would share one rollback record and
// restore the wrong rows, so a request already on the wire blocks a second one.
// It must block by refusing to arm, not by silently swallowing the key.
func TestASecondActionCannotArmWhileOneIsInFlight(t *testing.T) {
	model, client := moderationModel(t)
	model = armModeration(t, model, moderationDeleteRune)
	model, cmd := commitArmed(t, model, moderationDeleteRune)

	before := model.moderation
	model, second := pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{moderationBanRune}})
	if second != nil {
		t.Fatal("a ban was dispatched while a delete was in flight")
	}
	if model.moderation.stage != moderationStageInFlight {
		t.Fatalf("stage = %v, want the in-flight delete undisturbed", model.moderation.stage)
	}
	if model.moderation.action != before.action || len(model.moderation.rollback) != len(before.rollback) {
		t.Fatalf("the in-flight record was overwritten: %+v", model.moderation)
	}
	if len(client.bans) != 0 {
		t.Fatalf("bans = %d, want none dispatched", len(client.bans))
	}

	model = runModerationCmd(t, model, cmd)
	if model.moderation.stage != moderationStageIdle {
		t.Fatalf("stage after completion = %v, want idle", model.moderation.stage)
	}
}

// twoChatModerationModel is a moderating shell with two open chats, the second
// carrying a message from the same channel as the first.
func twoChatModerationModel(t *testing.T) (shellModel, *fakeModeratingClient, []string) {
	t.Helper()
	model := newModelForTest(t, "launch-day", "after-party")
	client := newFakeModeratingClient()
	model.client = client
	model.identity = youtube.Identity{
		ChannelID:   "UC-owner",
		DisplayName: "you",
		Scopes:      []string{"https://www.googleapis.com/auth/youtube.force-ssl"},
	}
	model.identityKnown = true

	keys := model.chats.chatKeys()
	for index, key := range keys {
		state := model.chats.stateForKey(key)
		state.target.ChannelID = "UC-owner"
		state.target.LiveChatID = key
		state.messages = []youtube.Message{
			testMessage(t, key+"-a", key, "alice", "hello from "+key),
			testMessage(t, key+"-b", key, "badactor", "an abusive line in "+key),
		}
		state.selected = replyContextFromMessage(state.messages[1])
		_ = index
	}
	model.chats.setActive(keys[0])
	return model, client, keys
}

// A completion belongs to the chat the action was armed in, not to whatever
// the user happens to be looking at when the answer arrives. Routing it to the
// active chat would blank an innocent channel's backlog in a chat nobody
// moderated, and leave the real target's rows redacted with no trace of why.
func TestModerationCompletionLandsInTheArmedChatNotTheActiveOne(t *testing.T) {
	model, client, keys := twoChatModerationModel(t)
	armed, other := keys[0], keys[1]

	model = armModeration(t, model, moderationBanRune)
	model, cmd := commitArmed(t, model, moderationBanRune)

	// Switch away while the request is on the wire. The moderation keys are
	// inert in flight, so ] reaches the ordinary handler.
	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	if got := model.activeChatKey(); got != other {
		t.Fatalf("active chat = %q, want the switch to have landed on %q", got, other)
	}

	model = runModerationCmd(t, model, cmd)

	armedState := model.chats.stateForKey(armed)
	otherState := model.chats.stateForKey(other)
	if len(armedState.moderations) != 1 {
		t.Fatalf("armed chat recorded %d moderations, want 1", len(armedState.moderations))
	}
	if len(otherState.moderations) != 0 {
		t.Fatalf("the chat the user switched to recorded %d moderations, want none",
			len(otherState.moderations))
	}
	for _, message := range otherState.messages {
		if message.Deleted {
			t.Fatalf("a ban in %q redacted %q in %q", armed, message.ID, other)
		}
	}
	if len(client.bans) != 1 || client.bans[0].LiveChatID != armed {
		t.Fatalf("ban went to %+v, want liveChatId %q", client.bans, armed)
	}
}

// Closing the chat while a request is in flight must not resurrect it. The
// completion has a chat key in hand and a naive lookup that creates on miss
// would put an empty chat back in the sidebar with one moderation entry and no
// history.
func TestModerationCompletionDoesNotResurrectAClosedChat(t *testing.T) {
	model, client, keys := twoChatModerationModel(t)
	client.banErr = errors.New("the request was refused")
	armed := keys[0]

	model = armModeration(t, model, moderationBanRune)
	model, cmd := commitArmed(t, model, moderationBanRune)

	if !model.chats.close(armed) {
		t.Fatal("closing the armed chat reported no change")
	}
	before := model.chatCount()

	model = runModerationCmd(t, model, cmd)

	if got := model.chatCount(); got != before {
		t.Fatalf("chatCount = %d after a completion for a closed chat, want %d", got, before)
	}
	for _, key := range model.chats.chatKeys() {
		if key == armed {
			t.Fatalf("the closed chat %q came back", armed)
		}
	}
	// The user still learns the request failed, without a rollback target.
	if !strings.Contains(model.moderationNote.text, "nothing was removed") {
		t.Fatalf("feedback = %q, want the failure still reported", model.moderationNote.text)
	}
}

// Switching chats with an armed confirmation must disarm it. An armed ban that
// survived a chat switch would fire on a message the user is no longer looking
// at the moment they press b again for a different reason.
func TestSwitchingChatsDisarmsAnArmedConfirmation(t *testing.T) {
	model, client, keys := twoChatModerationModel(t)
	model = armModeration(t, model, moderationBanRune)

	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	if model.moderation.stage != moderationStageIdle {
		t.Fatalf("stage after a chat switch = %v, want idle", model.moderation.stage)
	}
	if got := model.activeChatKey(); got != keys[1] {
		t.Fatalf("active chat = %q, want %q: the disarm swallowed the switch", got, keys[1])
	}

	// Pressing b again now arms against the new chat's selection rather than
	// firing the old one.
	model, cmd := pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{moderationBanRune}})
	if cmd != nil {
		t.Fatal("the second b fired the stale confirmation instead of arming a new one")
	}
	if model.moderation.chatKey != keys[1] {
		t.Fatalf("armed chatKey = %q, want the newly active %q", model.moderation.chatKey, keys[1])
	}
	if len(client.bans) != 0 {
		t.Fatalf("bans = %d, want none", len(client.bans))
	}
}

// Tab, esc, and the arrow keys are not runes, so they take a different route
// out of the confirmation. Each must still disarm rather than sit waiting.
func TestEveryNonConfirmingKeyShapeDisarms(t *testing.T) {
	shapes := []struct {
		name string
		key  tea.KeyMsg
	}{
		{"tab", tea.KeyMsg{Type: tea.KeyTab}},
		{"down", tea.KeyMsg{Type: tea.KeyDown}},
		{"pgup", tea.KeyMsg{Type: tea.KeyPgUp}},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}},
		{"ctrl+p", tea.KeyMsg{Type: tea.KeyCtrlP}},
		{"unrelated rune", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			model, client := moderationModel(t)
			model = armModeration(t, model, moderationBanRune)
			model, cmd := pressModeration(t, model, shape.key)
			if cmd != nil {
				// A command is fine as long as it is not the ban.
				if _, isModeration := cmd().(moderationCompletedMsg); isModeration {
					t.Fatalf("%s fired the armed ban", shape.name)
				}
			}
			if model.moderation.stage != moderationStageIdle {
				t.Fatalf("%s left stage %v, want idle", shape.name, model.moderation.stage)
			}
			if len(client.bans) != 0 {
				t.Fatalf("%s dispatched %d bans", shape.name, len(client.bans))
			}
		})
	}
}

// The capability is read at commit time as well as at arm time, because it can
// genuinely disappear in between: the transport drops its moderation capability
// the moment a credential stops working, and the confirmation is the user's
// second keystroke, not their first. Committing anyway would send a request yc
// already knows will fail after blanking the rows locally.
func TestCapabilityLostBetweenArmingAndConfirmingRefusesWithoutRedacting(t *testing.T) {
	model, client := moderationModel(t)
	model = armModeration(t, model, moderationDeleteRune)

	client.available = false
	client.reason = "the sign-in expired; run `yc login` to sign in again"

	model, cmd := pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{moderationDeleteRune}})
	if cmd != nil {
		t.Fatal("a delete was dispatched after the capability was withdrawn")
	}
	if len(client.deletedIDs) != 0 {
		t.Fatalf("deletes = %v, want none", client.deletedIDs)
	}
	if model.moderationNote.level != moderationLevelError {
		t.Fatalf("level = %v, want error", model.moderationNote.level)
	}
	if !strings.Contains(model.moderationNote.text, "moderation unavailable") {
		t.Fatalf("feedback = %q, want it to name the withdrawn capability", model.moderationNote.text)
	}
	// Nothing was blanked on the way out.
	for _, message := range model.activeChatState().messages {
		if message.Deleted {
			t.Fatalf("row %q was redacted by a refused commit", message.ID)
		}
	}
}

// A chat source with no Moderator methods at all is a different answer from a
// Moderator whose credential is missing, and both are different from no source.
// moderator() is the gate every dispatch runs through, so its three nil cases
// are pinned directly.
func TestModeratorGateRefusesEverySourceItCannotUse(t *testing.T) {
	base, _ := moderationModel(t)

	noSource := base
	noSource.client = nil
	if noSource.moderator() != nil {
		t.Fatal("a model with no chat source produced a moderator")
	}

	readOnly := base
	readOnly.client = &fakeReadOnlyClient{}
	if readOnly.moderator() != nil {
		t.Fatal("a source without the Moderator methods produced a moderator")
	}

	unwired := newFakeModeratingClient()
	unwired.available = false
	unwired.reason = "no credential"
	withoutCredential := base
	withoutCredential.client = unwired
	if withoutCredential.moderator() != nil {
		t.Fatal("a Moderator reporting itself unavailable produced a moderator")
	}

	if base.moderator() == nil {
		t.Fatal("a wired, available moderating source produced no moderator")
	}
}

// The three levels have to be visually distinct: an armed confirmation, a
// refusal, and a completed action are three different things for the user to
// do next, and rendering all three in one color makes the status line noise.
func TestModerationStatusLevelsAreVisuallyDistinct(t *testing.T) {
	model, _ := moderationModel(t)
	palette := model.theme

	seen := make(map[string]moderationLevel, 3)
	for _, level := range []moderationLevel{moderationLevelInfo, moderationLevelWarn, moderationLevelError} {
		color := moderationStatusColor(palette, level)
		if strings.TrimSpace(color) == "" {
			t.Fatalf("level %v has no color", level)
		}
		if previous, clash := seen[color]; clash {
			t.Fatalf("levels %v and %v share the color %q", previous, level, color)
		}
		seen[color] = level
	}
}

// The moderation line is the only feedback the keys produce, so it must survive
// the status bar's width budget everywhere - including the widths where the
// quota meter and the process metrics have already been dropped.
func TestModerationLineSurvivesTheStatusBarBudget(t *testing.T) {
	model, _ := moderationModel(t)
	model = armModeration(t, model, moderationBanRune)
	line := model.moderationNote.text
	if strings.TrimSpace(line) == "" {
		t.Fatal("arming produced no status line")
	}

	for _, width := range []int{20, 34, 62, 80, 130, 200} {
		model.width = width
		st := model.statusBarState()
		if st.Moderation != line {
			t.Fatalf("width %d: status bar carries %q, want %q", width, st.Moderation, line)
		}
		if st.ModerationLevel != moderationLevelWarn {
			t.Fatalf("width %d: level = %v, want warn", width, st.ModerationLevel)
		}
		rendered := ansi.Strip(renderStatusBar(width, st))
		if !strings.Contains(rendered, "ban") {
			t.Fatalf("width %d dropped the moderation line entirely: %q", width, rendered)
		}
		if got := ansi.StringWidth(renderStatusBar(width, st)); got != width {
			t.Fatalf("width %d: status bar rendered %d cells", width, got)
		}
	}
}

// The duration prompt is a text field in a status bar, which means every
// editing key has to be handled explicitly or it leaks into the shell behind
// it. Backspace deletes a cluster, ctrl+u clears, and the field refuses to grow
// past its budget so a held key cannot push the rest of the bar off screen.
func TestDurationPromptEditingIsBoundedAndClusterSafe(t *testing.T) {
	model, _ := moderationModel(t)
	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{moderationTimeoutRune}})
	if model.moderation.stage != moderationStageDuration {
		t.Fatalf("t did not open the duration prompt: stage %v", model.moderation.stage)
	}
	if model.moderation.durationInput != "5m" {
		t.Fatalf("prompt pre-fill = %q, want %q", model.moderation.durationInput, "5m")
	}

	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyBackspace})
	if model.moderation.durationInput != "5" {
		t.Fatalf("backspace left %q, want %q", model.moderation.durationInput, "5")
	}

	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyCtrlU})
	if model.moderation.durationInput != "" {
		t.Fatalf("ctrl+u left %q, want an empty field", model.moderation.durationInput)
	}

	// A held key cannot grow the field without bound.
	for i := 0; i < 60; i++ {
		model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'9'}})
	}
	if got := len(model.moderation.durationInput); got > moderationDurationInputLimit {
		t.Fatalf("duration field grew to %d cells, want at most %d", got, moderationDurationInputLimit)
	}

	// A multi-rune cluster deletes as one keystroke rather than leaving half a
	// sequence in the status bar.
	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyCtrlU})
	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("é")})
	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyBackspace})
	if model.moderation.durationInput != "" {
		t.Fatalf("backspace left %q of a combining cluster", model.moderation.durationInput)
	}
}

// A timeout the user typed and confirmed must reach the transport as the exact
// duration they asked for, in whole seconds. A confirmation that rounded, or
// that quietly reused the pre-filled default, would ban somebody for the wrong
// length of time with the status bar saying otherwise.
func TestConfirmedTimeoutReachesTheTransportUnchanged(t *testing.T) {
	model, client := moderationModel(t)
	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{moderationTimeoutRune}})
	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyCtrlU})
	for _, r := range "90s" {
		model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.moderation.stage != moderationStageConfirm {
		t.Fatalf("enter left stage %v, want an armed confirmation", model.moderation.stage)
	}
	if !strings.Contains(model.moderationNote.text, "1m30s") {
		t.Fatalf("confirmation = %q, want it to name the parsed duration", model.moderationNote.text)
	}

	model, cmd := commitArmed(t, model, moderationTimeoutRune)
	model = runModerationCmd(t, model, cmd)

	if len(client.bans) != 1 {
		t.Fatalf("bans = %d, want 1", len(client.bans))
	}
	if got := client.bans[0].Duration; got != 90*time.Second {
		t.Fatalf("ban duration = %v, want 90s", got)
	}
	if !strings.Contains(model.moderationNote.text, "1m30s") {
		t.Fatalf("success line = %q, want it to name the duration served", model.moderationNote.text)
	}
}

// d, t, and b are letters, which is only safe because moderation claims them
// in exactly one place. Everywhere else they must reach whatever normally owns
// them - most importantly the composer, where they are just text.
func TestModerationLettersAreInertOutsideTheChatPane(t *testing.T) {
	t.Run("composer", func(t *testing.T) {
		model, client := moderationModel(t)
		model.focus = focusComposer
		for _, r := range []rune{moderationDeleteRune, moderationTimeoutRune, moderationBanRune} {
			model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		if got := model.activeChatState().composerText; got != "dtb" {
			t.Fatalf("composer text = %q, want %q: moderation stole the keystrokes", got, "dtb")
		}
		if model.moderation.stage != moderationStageIdle {
			t.Fatalf("stage = %v, want idle", model.moderation.stage)
		}
		if len(client.bans)+len(client.deletedIDs) != 0 {
			t.Fatal("the composer dispatched a moderation request")
		}
	})

	t.Run("overlay open", func(t *testing.T) {
		model, _ := moderationModel(t)
		model.toggleOverlay(overlayPalette)
		if !model.overlay.open() {
			t.Fatal("the palette did not open")
		}
		model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{moderationBanRune}})
		if model.moderation.stage != moderationStageIdle {
			t.Fatalf("an overlay let b arm a ban: stage %v", model.moderation.stage)
		}
	})

	t.Run("leader pending", func(t *testing.T) {
		model, _ := moderationModel(t)
		model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeySpace})
		if !model.leaderPending {
			t.Fatal("space did not arm the leader")
		}
		model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{moderationDeleteRune}})
		if model.moderation.stage != moderationStageIdle {
			t.Fatalf("a pending leader let d arm a delete: stage %v", model.moderation.stage)
		}
	})

	t.Run("other tab", func(t *testing.T) {
		model, _ := moderationModel(t)
		model.activeTab = tabMisc
		model, _ = pressModeration(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{moderationBanRune}})
		if model.moderation.stage != moderationStageIdle {
			t.Fatalf("the quota tab let b arm a ban: stage %v", model.moderation.stage)
		}
	})
}

// The moderation keys stay in the help overlay whether or not they work, and
// the expanded help has to actually reach them on a real terminal. A group in
// the keymap that the height ladder truncates away is a group the user never
// discovers, and d/t/b are the three keys nobody will guess.
func TestExpandedHelpDrawsTheModerationGroupOnRealTerminals(t *testing.T) {
	model, _ := moderationModel(t)
	model.helpExpanded = true

	for _, size := range []struct{ width, height int }{
		{100, 30},
		{130, 36},
		{80, 26},
		{72, 22},
	} {
		model.width, model.height = size.width, size.height
		layout := model.layout()
		if layout.helpHeight <= 0 {
			t.Fatalf("%dx%d: expanded help got no rows", size.width, size.height)
		}
		// The group must survive the height ladder, which is what decides how
		// many groups get a row at all.
		lines := helpLines(layout.width, layout.helpHeight, model.helpState())
		want := helpGroupLine(keyGroupModeration)
		var reserved bool
		for _, line := range lines {
			if strings.HasPrefix(line, want) {
				reserved = true
			}
		}
		if !reserved {
			t.Fatalf("%dx%d: the height ladder truncated the moderation group away:\n%s",
				size.width, size.height, strings.Join(lines, "\n"))
		}

		// And the drawn row must at least reach the first key, so a moderator
		// on a narrow terminal still learns the group exists. Help rows
		// truncate at the width like every other group, so the whole row is
		// only asserted where there is room for it.
		rendered := ansi.Strip(renderHelp(layout.width, layout.helpHeight, model.helpState()))
		first := keyBindingsInGroup(keyGroupModeration)[0]
		if !strings.Contains(rendered, first.Keys+": "+strings.Fields(first.Description)[0]) {
			t.Fatalf("%dx%d: the moderation row was truncated to nothing:\n%s",
				size.width, size.height, rendered)
		}
		if size.width >= 130 {
			for _, binding := range keyBindingsInGroup(keyGroupModeration) {
				prefix := binding.Keys + ": " + strings.Fields(binding.Description)[0]
				if !strings.Contains(rendered, prefix) {
					t.Fatalf("%dx%d: expanded help omits %q:\n%s",
						size.width, size.height, prefix, rendered)
				}
			}
		}
	}

	// At the tallest rung every group renders, so a sixth group cannot be
	// added without the ladder being extended to match.
	model.width, model.height = 120, 40
	if got := model.layout().helpHeight; got != len(keyGroupOrder) {
		t.Fatalf("a tall terminal reserved %d help rows for %d key groups", got, len(keyGroupOrder))
	}
}

package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/youtube"
)

// fakeModeratingClient is a ChatClient that also moderates, so the shell's
// moderation path can be driven with no network and no credentials.
//
// It records what it was asked to do and answers with whatever the test set,
// which is the only way to assert the two halves that matter: that a success
// leaves the rows redacted, and that a failure puts them back.
type fakeModeratingClient struct {
	messages chan youtube.Message
	states   chan youtube.ConnectionState

	available bool
	reason    string

	deleteErr error
	banErr    error

	deletedIDs []string
	bans       []youtube.BanRequest
	unbanned   []string
}

var (
	_ ChatClient           = (*fakeModeratingClient)(nil)
	_ Moderator            = (*fakeModeratingClient)(nil)
	_ ModerationCapability = (*fakeModeratingClient)(nil)
)

func newFakeModeratingClient() *fakeModeratingClient {
	return &fakeModeratingClient{
		messages:  make(chan youtube.Message),
		states:    make(chan youtube.ConnectionState),
		available: true,
	}
}

func (c *fakeModeratingClient) Messages() <-chan youtube.Message { return c.messages }

func (c *fakeModeratingClient) ConnectionStates() <-chan youtube.ConnectionState { return c.states }

func (c *fakeModeratingClient) Send(context.Context, youtube.SendRequest) (youtube.SendResult, error) {
	return youtube.SendResult{}, nil
}

func (c *fakeModeratingClient) Close() error { return nil }

func (c *fakeModeratingClient) ModerationAvailable() (bool, string) { return c.available, c.reason }

func (c *fakeModeratingClient) DeleteMessage(_ context.Context, messageID string) error {
	c.deletedIDs = append(c.deletedIDs, messageID)
	return c.deleteErr
}

func (c *fakeModeratingClient) Ban(_ context.Context, request youtube.BanRequest) (youtube.BanResult, error) {
	c.bans = append(c.bans, request)
	if c.banErr != nil {
		return youtube.BanResult{}, c.banErr
	}
	return youtube.BanResult{BanID: "ban-1", Permanent: request.Duration <= 0}, nil
}

func (c *fakeModeratingClient) Unban(_ context.Context, banID string) error {
	c.unbanned = append(c.unbanned, banID)
	return nil
}

// moderationModel builds a shell whose signed-in channel owns the chat, with
// three messages from two authors on screen and the cursor on the one the
// tests act against.
func moderationModel(t *testing.T) (shellModel, *fakeModeratingClient) {
	t.Helper()
	model := newModelForTest(t, "launch-day")
	client := newFakeModeratingClient()
	model.client = client
	model.identity = youtube.Identity{
		ChannelID:   "UC-owner",
		DisplayName: "you",
		Scopes:      []string{"https://www.googleapis.com/auth/youtube.force-ssl"},
	}
	model.identityKnown = true

	state := model.activeChatState()
	state.target.ChannelID = "UC-owner"
	state.target.LiveChatID = "live-chat-1"
	state.messages = []youtube.Message{
		testMessage(t, "m1", "launch-day", "alice", "hello everyone"),
		testMessage(t, "m2", "launch-day", "badactor", "an abusive line goes here"),
		testMessage(t, "m3", "launch-day", "badactor", "and another one"),
	}
	state.selected = replyContextFromMessage(state.messages[1])
	return model, client
}

// pressModeration feeds one key straight through Update, so the tests exercise
// the same dispatch order the shell uses rather than calling the handler.
func pressModeration(t *testing.T, model shellModel, msg tea.KeyMsg) (shellModel, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(msg)
	updated, ok := next.(shellModel)
	if !ok {
		t.Fatalf("Update returned %T, want shellModel", next)
	}
	return updated, cmd
}

// runModerationCmd runs the command a confirmation produced and feeds its
// message back, which is what the Bubble Tea runtime would do.
func runModerationCmd(t *testing.T, model shellModel, cmd tea.Cmd) shellModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("confirming a moderation action produced no command")
	}
	msg := cmd()
	completed, ok := msg.(moderationCompletedMsg)
	if !ok {
		t.Fatalf("moderation command returned %T, want moderationCompletedMsg", msg)
	}
	next, _ := model.Update(completed)
	updated, ok := next.(shellModel)
	if !ok {
		t.Fatalf("Update returned %T, want shellModel", next)
	}
	return updated
}

func TestModerationDeleteAsksBeforeItActs(t *testing.T) {
	model, client := moderationModel(t)

	model, cmd := pressModeration(t, model, runeKey(moderationDeleteRune))
	if cmd != nil {
		t.Fatal("the first press dispatched a delete; it must only arm a confirmation")
	}
	if len(client.deletedIDs) != 0 {
		t.Fatalf("delete reached the transport before confirmation: %v", client.deletedIDs)
	}
	if model.moderation.stage != moderationStageConfirm {
		t.Fatalf("stage = %v, want confirm", model.moderation.stage)
	}
	line, level := model.moderationStatus()
	if !strings.Contains(line, "delete") || !strings.Contains(line, "esc cancels") {
		t.Fatalf("confirmation line = %q, want it to name the action and the way out", line)
	}
	if level != moderationLevelWarn {
		t.Fatalf("confirmation level = %v, want warn", level)
	}

	model, cmd = pressModeration(t, model, runeKey(moderationDeleteRune))
	model = runModerationCmd(t, model, cmd)
	if len(client.deletedIDs) != 1 || client.deletedIDs[0] != "m2" {
		t.Fatalf("deleted ids = %v, want [m2]", client.deletedIDs)
	}
	if model.moderation.stage != moderationStageIdle {
		t.Fatalf("stage = %v, want idle after completion", model.moderation.stage)
	}
}

func TestModerationEscCancelsWithoutActing(t *testing.T) {
	model, client := moderationModel(t)
	model, _ = pressModeration(t, model, runeKey(moderationBanRune))
	model, _ = pressModeration(t, model, key(tea.KeyEsc))

	if model.moderation.stage != moderationStageIdle {
		t.Fatalf("stage = %v, want idle after esc", model.moderation.stage)
	}
	if len(client.bans) != 0 {
		t.Fatalf("esc still issued a ban: %v", client.bans)
	}
	if line, _ := model.moderationStatus(); !strings.Contains(line, "cancel") {
		t.Fatalf("status = %q, want it to say the action was canceled", line)
	}
}

// TestModerationUnrelatedKeyDisarmsAndStillWorks is the ctrl+L rule applied to
// a far more destructive key: a confirmation that lingers behind an unrelated
// keystroke is a ban waiting to be issued by accident.
func TestModerationUnrelatedKeyDisarmsAndStillWorks(t *testing.T) {
	model, client := moderationModel(t)
	model, _ = pressModeration(t, model, runeKey(moderationBanRune))

	model, _ = pressModeration(t, model, runeKey('k'))
	if model.moderation.stage != moderationStageIdle {
		t.Fatalf("stage = %v, want idle; an unrelated key must disarm", model.moderation.stage)
	}
	if got := replyMessageID(model.activeChatState().selected); got != "m1" {
		t.Fatalf("selected = %q, want m1; the disarming key must still do its own job", got)
	}
	if len(client.bans) != 0 {
		t.Fatalf("an unrelated key issued a ban: %v", client.bans)
	}
}

func TestModerationDeleteRedactsOptimisticallyAndKeepsTheTextOffScreen(t *testing.T) {
	model, _ := moderationModel(t)
	const removed = "an abusive line goes here"

	model, _ = pressModeration(t, model, runeKey(moderationDeleteRune))
	model, cmd := pressModeration(t, model, runeKey(moderationDeleteRune))
	if cmd == nil {
		t.Fatal("confirming produced no command")
	}

	// The rows are redacted before the transport has answered: that is the
	// point of an optimistic update.
	state := model.activeChatState()
	for _, message := range state.messages {
		if message.ID != "m2" {
			continue
		}
		if !message.Deleted {
			t.Fatal("the targeted message was not redacted before the request completed")
		}
		if message.Text != "" {
			t.Fatalf("redacted message still carries text %q", message.Text)
		}
	}
	rendered := ansi.Strip(strings.Join(model.visibleChatRows(model.layout()), "\n"))
	if strings.Contains(rendered, removed) {
		t.Fatal("the removed words are still on screen after an optimistic delete")
	}
	if !strings.Contains(rendered, "hello everyone") {
		t.Fatal("deleting one message removed unrelated messages from the view")
	}
	if strings.Contains(ansi.Strip(model.View()), removed) {
		t.Fatal("the removed words reached the rendered frame")
	}
}

// TestModerationRollsBackWhenTheRequestFails is the other half of an optimistic
// update. A refused delete means the message is still live on YouTube and still
// in front of the audience; leaving the row blank would hand the moderator a
// terminal that disagrees with their own broadcast.
func TestModerationRollsBackWhenTheRequestFails(t *testing.T) {
	model, client := moderationModel(t)
	client.deleteErr = errors.New("the caller does not have permission")
	const removed = "an abusive line goes here"

	model, _ = pressModeration(t, model, runeKey(moderationDeleteRune))
	model, cmd := pressModeration(t, model, runeKey(moderationDeleteRune))
	model = runModerationCmd(t, model, cmd)

	state := model.activeChatState()
	for _, message := range state.messages {
		if message.ID != "m2" {
			continue
		}
		if message.Deleted {
			t.Fatal("a failed delete left the message redacted")
		}
		if message.Text != removed {
			t.Fatalf("rolled-back text = %q, want %q", message.Text, removed)
		}
	}
	line, level := model.moderationStatus()
	if level != moderationLevelError {
		t.Fatalf("failure level = %v, want error", level)
	}
	if !strings.Contains(line, "nothing was removed") {
		t.Fatalf("failure line = %q, want it to say the rows came back", line)
	}
	if strings.Contains(line, removed) {
		t.Fatal("the failure line reprinted the message text")
	}
}

func TestModerationBanRedactsEveryMessageFromTheTarget(t *testing.T) {
	model, client := moderationModel(t)

	model, _ = pressModeration(t, model, runeKey(moderationBanRune))
	model, cmd := pressModeration(t, model, runeKey(moderationBanRune))
	model = runModerationCmd(t, model, cmd)

	if len(client.bans) != 1 {
		t.Fatalf("bans = %d, want 1", len(client.bans))
	}
	if client.bans[0].Duration != 0 {
		t.Fatalf("ban duration = %s, want 0 (permanent)", client.bans[0].Duration)
	}
	if client.bans[0].ChannelID != "UC-badactor" {
		t.Fatalf("ban channel = %q, want UC-badactor", client.bans[0].ChannelID)
	}
	if client.bans[0].LiveChatID != "live-chat-1" {
		t.Fatalf("ban live chat = %q, want live-chat-1", client.bans[0].LiveChatID)
	}
	for _, message := range model.activeChatState().messages {
		want := message.Author.ChannelID == "UC-badactor"
		if message.Deleted != want {
			t.Fatalf("message %s Deleted = %v, want %v", message.ID, message.Deleted, want)
		}
	}
}

// TestModerationSuccessLeavesATraceInTheActivityColumn closes the gap the
// transport cannot: the API does not report yc's own deletions back, so without
// a local echo a moderation action would leave no record anywhere.
func TestModerationSuccessLeavesATraceInTheActivityColumn(t *testing.T) {
	model, _ := moderationModel(t)
	model, _ = pressModeration(t, model, runeKey(moderationBanRune))
	model, cmd := pressModeration(t, model, runeKey(moderationBanRune))
	model = runModerationCmd(t, model, cmd)

	moderations := model.activeChatState().moderations
	if len(moderations) != 1 {
		t.Fatalf("recorded moderations = %d, want 1", len(moderations))
	}
	if moderations[0].Type != youtube.ModerationUserBanned {
		t.Fatalf("recorded type = %v, want user_banned", moderations[0].Type)
	}
	entries := activityEntriesForChats(model.chats, maxActivityEntries)
	found := false
	for _, entry := range entries {
		if entry.Kind == activityModeration {
			found = true
			if strings.Contains(entry.Text, "an abusive line goes here") {
				t.Fatal("the activity column reprinted the removed message text")
			}
		}
	}
	if !found {
		t.Fatal("a completed ban left no entry in the activity column")
	}
}

func TestModerationTimeoutPromptsForADuration(t *testing.T) {
	model, client := moderationModel(t)

	model, cmd := pressModeration(t, model, runeKey(moderationTimeoutRune))
	if cmd != nil {
		t.Fatal("the timeout key dispatched immediately; it must ask for a duration")
	}
	if model.moderation.stage != moderationStageDuration {
		t.Fatalf("stage = %v, want duration", model.moderation.stage)
	}
	line, _ := model.moderationStatus()
	if !strings.Contains(line, "time out") || !strings.Contains(line, "_") {
		t.Fatalf("prompt = %q, want it to read as an input", line)
	}

	// Replace the pre-filled default by hand, one keystroke at a time.
	model, _ = pressModeration(t, model, key(tea.KeyCtrlU))
	for _, r := range "90s" {
		model, _ = pressModeration(t, model, runeKey(r))
	}
	model, _ = pressModeration(t, model, key(tea.KeyEnter))
	if model.moderation.stage != moderationStageConfirm {
		t.Fatalf("stage = %v, want confirm after entering a duration", model.moderation.stage)
	}
	if model.moderation.duration != 90*time.Second {
		t.Fatalf("duration = %s, want 1m30s", model.moderation.duration)
	}

	model, cmd = pressModeration(t, model, key(tea.KeyEnter))
	model = runModerationCmd(t, model, cmd)
	if len(client.bans) != 1 {
		t.Fatalf("bans = %d, want 1", len(client.bans))
	}
	if client.bans[0].Duration != 90*time.Second {
		t.Fatalf("ban duration = %s, want 1m30s", client.bans[0].Duration)
	}
}

// TestModerationDurationPromptIsModal keeps the prompt from leaking keystrokes
// into the chat behind it: "5m" must not also toggle a filter or move the
// cursor.
func TestModerationDurationPromptIsModal(t *testing.T) {
	model, _ := moderationModel(t)
	model, _ = pressModeration(t, model, runeKey(moderationTimeoutRune))
	model, _ = pressModeration(t, model, key(tea.KeyCtrlU))

	before := replyMessageID(model.activeChatState().selected)
	beforeFilters := model.activeChatState().filters
	for _, r := range "1k" {
		model, _ = pressModeration(t, model, runeKey(r))
	}
	if got := replyMessageID(model.activeChatState().selected); got != before {
		t.Fatalf("selected = %q, want %q; the prompt leaked a key into the chat", got, before)
	}
	if model.activeChatState().filters != beforeFilters {
		t.Fatal("typing into the duration prompt toggled a filter")
	}
	if model.moderation.durationInput != "1k" {
		t.Fatalf("durationInput = %q, want \"1k\"", model.moderation.durationInput)
	}

	// A duration that cannot be parsed says so and keeps the prompt open
	// rather than acting on a guess.
	model, _ = pressModeration(t, model, key(tea.KeyEnter))
	if model.moderation.stage == moderationStageConfirm {
		t.Fatal("an unparseable duration armed a confirmation")
	}
	if line, level := model.moderationStatus(); level != moderationLevelError || !strings.Contains(line, "duration") {
		t.Fatalf("status = %q level = %v, want a duration error", line, level)
	}
}

func TestParseModerationDuration(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{input: "60", want: time.Minute},
		{input: "90s", want: 90 * time.Second},
		{input: " 10M ", want: 10 * time.Minute},
		{input: "1h30m", want: 90 * time.Minute},
		{input: "", wantErr: true},
		{input: "0", wantErr: true},
		{input: "-5m", wantErr: true},
		{input: "500ms", wantErr: true},
		{input: "48h", wantErr: true},
		{input: "soon", wantErr: true},
	}
	for _, test := range tests {
		got, err := parseModerationDuration(test.input)
		if test.wantErr {
			if err == nil {
				t.Errorf("parseModerationDuration(%q) = %s, want an error", test.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseModerationDuration(%q) errored: %v", test.input, err)
			continue
		}
		if got != test.want {
			t.Errorf("parseModerationDuration(%q) = %s, want %s", test.input, got, test.want)
		}
	}
}

// --- capability ------------------------------------------------------------

// TestModerationDisabledStatesAreExplainedNotSilent is the property the whole
// feature is judged on. Every way moderation can be unavailable must produce a
// sentence naming what to fix, and none of them may act.
func TestModerationDisabledStatesAreExplainedNotSilent(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*shellModel, *fakeModeratingClient)
		want    string
	}{
		{
			name:    "no chat source",
			arrange: func(m *shellModel, _ *fakeModeratingClient) { m.client = nil },
			want:    "no chat source",
		},
		{
			name: "transport cannot moderate",
			arrange: func(m *shellModel, _ *fakeModeratingClient) {
				m.client = &fakeReadOnlyClient{}
			},
			want: "cannot moderate",
		},
		{
			name: "no moderating credential",
			arrange: func(_ *shellModel, c *fakeModeratingClient) {
				c.available = false
				c.reason = "this session has no credential that can moderate"
			},
			want: "no credential",
		},
		{
			name: "missing scope",
			arrange: func(m *shellModel, _ *fakeModeratingClient) {
				m.identity.Scopes = []string{"https://www.googleapis.com/auth/youtube.readonly"}
			},
			want: "force-ssl",
		},
		{
			name: "not a moderator",
			arrange: func(m *shellModel, _ *fakeModeratingClient) {
				m.identity.ChannelID = "UC-bystander"
				state := m.activeChatState()
				state.roster = map[string]youtube.Author{
					"UC-bystander": {ChannelID: "UC-bystander", DisplayName: "you"},
				}
			},
			want: "not a moderator",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, client := moderationModel(t)
			test.arrange(&model, client)

			model, cmd := pressModeration(t, model, runeKey(moderationDeleteRune))
			if cmd != nil {
				t.Fatal("a disabled moderation key dispatched a request")
			}
			if model.moderation.stage != moderationStageIdle {
				t.Fatalf("stage = %v, want idle", model.moderation.stage)
			}
			line, level := model.moderationStatus()
			if line == "" {
				t.Fatal("a disabled moderation key produced no explanation, which is a silent no-op")
			}
			if level != moderationLevelError {
				t.Fatalf("level = %v, want error", level)
			}
			if !strings.Contains(line, test.want) {
				t.Fatalf("status = %q, want it to mention %q", line, test.want)
			}
			if len(client.deletedIDs) != 0 {
				t.Fatalf("a disabled key still reached the transport: %v", client.deletedIDs)
			}
		})
	}
}

// fakeReadOnlyClient is a ChatClient with no moderation surface at all, which is
// what --mock and a key-only session look like.
type fakeReadOnlyClient struct{}

var _ ChatClient = (*fakeReadOnlyClient)(nil)

func (c *fakeReadOnlyClient) Messages() <-chan youtube.Message { return nil }

func (c *fakeReadOnlyClient) ConnectionStates() <-chan youtube.ConnectionState { return nil }

func (c *fakeReadOnlyClient) Send(context.Context, youtube.SendRequest) (youtube.SendResult, error) {
	return youtube.SendResult{}, nil
}

func (c *fakeReadOnlyClient) Close() error { return nil }

// TestModerationRoleIsFourValued pins the distinction the capability check
// rests on: an unknown role is not a negative one.
func TestModerationRoleIsFourValued(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*shellModel)
		want    moderationRole
	}{
		{
			name:    "broadcast owner",
			arrange: func(*shellModel) {},
			want:    moderationRoleOwner,
		},
		{
			name: "identity not resolved",
			arrange: func(m *shellModel) {
				m.identityKnown = false
			},
			want: moderationRoleUnknown,
		},
		{
			name: "silent moderator is unknown, not a viewer",
			arrange: func(m *shellModel) {
				m.identity.ChannelID = "UC-quiet"
			},
			want: moderationRoleUnknown,
		},
		{
			name: "moderator badge from a message they sent",
			arrange: func(m *shellModel) {
				m.identity.ChannelID = "UC-mod"
				m.activeChatState().roster = map[string]youtube.Author{
					"UC-mod": {ChannelID: "UC-mod", IsModerator: true},
				}
			},
			want: moderationRoleModerator,
		},
		{
			name: "plain viewer",
			arrange: func(m *shellModel) {
				m.identity.ChannelID = "UC-nobody"
				m.activeChatState().roster = map[string]youtube.Author{
					"UC-nobody": {ChannelID: "UC-nobody"},
				}
			},
			want: moderationRoleViewer,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, _ := moderationModel(t)
			test.arrange(&model)
			if got := model.moderationRole(model.activeChatState()); got != test.want {
				t.Fatalf("moderationRole = %q, want %q", got, test.want)
			}
		})
	}
}

// TestModerationDisclosesAnUncertainCapability keeps yc from pretending to know
// more than it does: with no record of the granted scopes the keys stay live,
// and the confirmation says the API is the authority.
func TestModerationDisclosesAnUncertainCapability(t *testing.T) {
	model, _ := moderationModel(t)
	model.identity.Scopes = nil

	capability := model.moderationCapability()
	if !capability.Available {
		t.Fatalf("unknown scopes disabled moderation: %q", capability.Reason)
	}
	if capability.Certain {
		t.Fatal("unknown scopes were reported as certain")
	}

	model, _ = pressModeration(t, model, runeKey(moderationDeleteRune))
	if line, _ := model.moderationStatus(); !strings.Contains(line, "unknown") {
		t.Fatalf("confirmation = %q, want it to disclose the uncertainty", line)
	}
}

// TestModerationRefusesTargetsItCannotAddress covers the per-target refusals,
// each of which must name its own reason rather than a generic failure.
func TestModerationRefusesTargetsItCannotAddress(t *testing.T) {
	tests := []struct {
		name    string
		action  rune
		arrange func(*shellModel)
		want    string
	}{
		{
			name:   "nothing selected",
			action: moderationDeleteRune,
			arrange: func(m *shellModel) {
				m.activeChatState().selected = nil
			},
			want: "select a message",
		},
		{
			name:   "local echo has no id yet",
			action: moderationDeleteRune,
			arrange: func(m *shellModel) {
				state := m.activeChatState()
				state.messages[1].LocalEcho = true
			},
			want: "waiting for YouTube",
		},
		{
			name:   "author has no channel id",
			action: moderationBanRune,
			arrange: func(m *shellModel) {
				state := m.activeChatState()
				state.messages[1].Author.ChannelID = ""
			},
			want: "no channel id",
		},
		{
			name:   "banning yourself",
			action: moderationBanRune,
			arrange: func(m *shellModel) {
				m.identity.ChannelID = "UC-badactor"
			},
			want: "your own channel",
		},
		{
			name:   "chat not resolved",
			action: moderationTimeoutRune,
			arrange: func(m *shellModel) {
				m.activeChatState().target.LiveChatID = ""
			},
			want: "not resolved",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, client := moderationModel(t)
			test.arrange(&model)

			model, cmd := pressModeration(t, model, runeKey(test.action))
			if cmd != nil {
				t.Fatal("a refused target still dispatched a request")
			}
			if model.moderation.stage != moderationStageIdle {
				t.Fatalf("stage = %v, want idle", model.moderation.stage)
			}
			line, level := model.moderationStatus()
			if level != moderationLevelError || !strings.Contains(line, test.want) {
				t.Fatalf("status = %q level = %v, want it to mention %q", line, level, test.want)
			}
			if len(client.bans)+len(client.deletedIDs) != 0 {
				t.Fatal("a refused target reached the transport")
			}
		})
	}
}

// TestModerationStatusReachesEveryTerminalWidth mirrors the dropped-message
// rule: a destructive confirmation that a narrow terminal hides is a
// confirmation the user answers blind.
func TestModerationStatusReachesEveryTerminalWidth(t *testing.T) {
	model, _ := moderationModel(t)
	model, _ = pressModeration(t, model, runeKey(moderationBanRune))
	state := model.statusBarState()
	if state.Moderation == "" {
		t.Fatal("the armed confirmation did not reach the status bar")
	}
	for _, width := range []int{20, 40, 62, 96, 130} {
		rendered := ansi.Strip(renderStatusBar(width, state))
		if !strings.Contains(rendered, "ban") {
			t.Fatalf("width %d dropped the moderation confirmation: %q", width, rendered)
		}
	}
}

// --- transport wiring ------------------------------------------------------

// TestLiveChatClientReportsModerationUnavailableWithoutACredential pins the
// reason ModerationCapability exists: the methods are on the type either way,
// so only an explicit report can tell the two apart before a key is pressed.
func TestLiveChatClientReportsModerationUnavailableWithoutACredential(t *testing.T) {
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory: func(youtube.ChatTarget) (LiveChatTransport, error) { return nil, errors.New("unused") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	if available, reason := client.ModerationAvailable(); available {
		t.Fatal("a client with no moderator reported moderation as available")
	} else if !strings.Contains(reason, "credential") {
		t.Fatalf("reason = %q, want it to name the missing credential", reason)
	}
	if err := client.DeleteMessage(context.Background(), "m1"); !errors.Is(err, ErrModerationUnavailable) {
		t.Fatalf("DeleteMessage error = %v, want ErrModerationUnavailable", err)
	}
	if _, err := client.Ban(context.Background(), youtube.BanRequest{}); !errors.Is(err, ErrModerationUnavailable) {
		t.Fatalf("Ban error = %v, want ErrModerationUnavailable", err)
	}
	if err := client.Unban(context.Background(), "b1"); !errors.Is(err, ErrModerationUnavailable) {
		t.Fatalf("Unban error = %v, want ErrModerationUnavailable", err)
	}
}

func TestLiveChatClientDelegatesModerationToTheConfiguredCredential(t *testing.T) {
	moderator := newFakeModeratingClient()
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory:   func(youtube.ChatTarget) (LiveChatTransport, error) { return nil, errors.New("unused") },
		Moderator: moderator,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	if available, _ := client.ModerationAvailable(); !available {
		t.Fatal("a configured moderator was not reported as available")
	}
	if err := client.DeleteMessage(context.Background(), "m1"); err != nil {
		t.Fatal(err)
	}
	if len(moderator.deletedIDs) != 1 || moderator.deletedIDs[0] != "m1" {
		t.Fatalf("deleted ids = %v, want [m1]", moderator.deletedIDs)
	}
	if _, err := client.Ban(context.Background(), youtube.BanRequest{
		LiveChatID: "live-chat-1",
		ChannelID:  "UC-badactor",
		Duration:   time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if len(moderator.bans) != 1 || moderator.bans[0].ChannelID != "UC-badactor" {
		t.Fatalf("bans = %v, want one for UC-badactor", moderator.bans)
	}
}

// TestLiveChatClientBanUsesTheResolvedLiveChatID guards the identifier mismatch
// that would make every ban fail on a chat opened by video ID: the shell knows
// the routing key, and liveChatBans.insert accepts nothing but a liveChatId.
func TestLiveChatClientBanUsesTheResolvedLiveChatID(t *testing.T) {
	moderator := newFakeModeratingClient()
	transport := newFakeTransport()
	transport.target = youtube.ChatTarget{VideoID: "vid123", LiveChatID: "resolved-chat-id"}
	client, err := NewLiveChatClient(LiveChatConfig{
		Factory:   func(youtube.ChatTarget) (LiveChatTransport, error) { return transport, nil },
		Targets:   []youtube.ChatTarget{{Raw: "vid123", VideoID: "vid123"}},
		Moderator: moderator,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	waitFor(t, func() bool { return client.resolveLiveChatID("vid123") == "resolved-chat-id" })

	if _, err := client.Ban(context.Background(), youtube.BanRequest{
		LiveChatID: "vid123",
		ChannelID:  "UC-badactor",
	}); err != nil {
		t.Fatal(err)
	}
	if len(moderator.bans) != 1 {
		t.Fatalf("bans = %d, want 1", len(moderator.bans))
	}
	if got := moderator.bans[0].LiveChatID; got != "resolved-chat-id" {
		t.Fatalf("ban live chat id = %q, want the resolved id", got)
	}
}

func TestFormatModerationDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{in: 0, want: "permanent"},
		{in: -time.Minute, want: "permanent"},
		{in: 30 * time.Second, want: "30s"},
		{in: 5 * time.Minute, want: "5m"},
		{in: 90 * time.Second, want: "1m30s"},
		{in: time.Hour, want: "1h"},
		{in: 90 * time.Minute, want: "1h30m"},
	}
	for _, test := range tests {
		if got := formatModerationDuration(test.in); got != test.want {
			t.Errorf("formatModerationDuration(%s) = %q, want %q", test.in, got, test.want)
		}
	}
}

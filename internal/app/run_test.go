package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/worxbend/yc/internal/config"
)

// runConfig is a deterministic config for the non-interactive frame.
func runConfig() config.Config {
	cfg := config.Default()
	cfg.Features.AnimationMode = "off"
	return cfg
}

func TestRunMockRendersOneFrameToANonTerminal(t *testing.T) {
	var out strings.Builder
	if err := RunMock(&out, runConfig()); err != nil {
		t.Fatalf("RunMock error = %v", err)
	}

	frame := out.String()
	if strings.TrimSpace(frame) == "" {
		t.Fatal("RunMock wrote nothing")
	}
	// A piped writer must never receive the alt-screen sequence: that is a
	// log file or a CI transcript, not a terminal yc may take over.
	if strings.Contains(frame, "\x1b[?1049h") {
		t.Fatal("alt screen sequence written to a non-terminal")
	}
	for _, want := range []string{"1:Chat", "2:Stream Info", mockChatTitle} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame is missing %q:\n%s", want, frame)
		}
	}
}

func TestNonInteractiveFrameIsRectangular(t *testing.T) {
	var out strings.Builder
	if err := RunMock(&out, runConfig()); err != nil {
		t.Fatalf("RunMock error = %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != defaultShellHeight {
		t.Fatalf("frame has %d rows, want %d", len(lines), defaultShellHeight)
	}
	for index, line := range lines {
		if got := ansi.StringWidth(ansi.Strip(line)); got != defaultShellWidth {
			t.Errorf("row %d is %d cells wide, want %d: %q", index, got, defaultShellWidth, ansi.Strip(line))
		}
	}
}

func TestMockRunOpensExactlyTheScriptedChat(t *testing.T) {
	// The scripted source speaks for one chat. If none is open the demo
	// lands on the empty state and shows nothing, which is the one outcome
	// the primary demo path may not produce.
	cfg := runConfig()
	client := NewMockChatClient(mockChatTitle)
	t.Cleanup(func() { _ = client.Close() })

	cfg.DefaultChats = []string{mockChatTitle}
	model := newLiveModel(cfg, client, nil, withMockCollaborators(ClientOptions{}, mockChatTitle))
	if model.chatCount() != 1 {
		t.Fatalf("chatCount = %d, want 1", model.chatCount())
	}
	if got := model.activeChatLabel(); got != mockChatTitle {
		t.Fatalf("activeChatLabel = %q, want %q", got, mockChatTitle)
	}
}

func TestMockCollaboratorsPopulateEverySurface(t *testing.T) {
	source := mockCollaborators{title: mockChatTitle}
	ctx := t.Context()

	identity, err := source.Identity(ctx)
	if err != nil {
		t.Fatalf("Identity error = %v", err)
	}
	if identity.ChannelID == "" || !identity.SubscriberCountKnown {
		t.Fatalf("identity is not populated: %+v", identity)
	}

	target, err := source.ResolveTarget(ctx, unresolvedTarget(mockChatTitle))
	if err != nil {
		t.Fatalf("ResolveTarget error = %v", err)
	}
	if !target.Resolved() || target.VideoID == "" {
		t.Fatalf("target is not resolved: %+v", target)
	}

	broadcast, err := source.Broadcast(ctx, target.VideoID)
	if err != nil {
		t.Fatalf("Broadcast error = %v", err)
	}
	if !broadcast.Live || !broadcast.ViewersKnown {
		t.Fatalf("broadcast is not live: %+v", broadcast)
	}

	subscriptions, err := source.Subscriptions(ctx)
	if err != nil || len(subscriptions) == 0 {
		t.Fatalf("Subscriptions = %v, %v", subscriptions, err)
	}
	categories, err := source.Categories(ctx)
	if err != nil || len(categories) == 0 {
		t.Fatalf("Categories = %v, %v", categories, err)
	}
}

func TestSuppliedCollaboratorsSurviveTheMockDefaults(t *testing.T) {
	provided := &recordingIdentityLookup{}
	opts := withMockCollaborators(ClientOptions{IdentityLookup: provided}, mockChatTitle)
	if opts.IdentityLookup != provided {
		t.Fatal("the mock replaced a collaborator the caller supplied")
	}
	if opts.BroadcastResolver == nil {
		t.Fatal("an unsupplied collaborator was left nil")
	}
}

func TestRunClientClosesTheClient(t *testing.T) {
	client := NewFakeChatClient()
	var out strings.Builder
	if err := RunClient(&out, runConfig(), client); err != nil {
		t.Fatalf("RunClient error = %v", err)
	}
	// The client owns a poll schedule and a session context. A run that
	// returns without closing it leaks both and keeps spending quota.
	if !client.Closed() {
		t.Fatal("RunClient returned without closing the client")
	}
}

func TestCenteredBlockKeepsArtRowsAligned(t *testing.T) {
	lines := centeredBlockLines(splashLogo, 60)
	if len(lines) != len(splashLogo) {
		t.Fatalf("got %d rows, want %d", len(lines), len(splashLogo))
	}
	// Every row of a picture has to be displaced by the same amount, so the
	// art keeps its own internal indentation. Centering each row on its own
	// trimmed width instead adds its indent twice and shears the wordmark
	// apart, which is what this guards against.
	want := leadingSpaces(lines[0]) - leadingSpaces(splashLogo[0])
	for index, line := range lines {
		if got := leadingSpaces(line) - leadingSpaces(splashLogo[index]); got != want {
			t.Errorf("row %d displaced by %d columns, want %d: %q", index, got, want, line)
		}
		if got := ansi.StringWidth(line); got != 60 {
			t.Errorf("row %d is %d cells wide, want 60", index, got)
		}
	}
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// recordingIdentityLookup is a stand-in the mock defaults must not replace.
type recordingIdentityLookup struct{ IdentityLookup }

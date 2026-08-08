package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/youtube"
)

const (
	// defaultShellWidth and defaultShellHeight are the dimensions used
	// before the first WindowSizeMsg arrives, so the very first frame and
	// the non-interactive smoke output are both deterministic.
	defaultShellWidth  = 88
	defaultShellHeight = 22
)

// mockChatTitle is the demo broadcast's name. It is obviously fake so a
// screenshot of a mock run can never be mistaken for a real stream.
const mockChatTitle = "yc demo stream"

// mockMentionHandle is the handle the scripted chat mentions, wired into the
// model so the mention filter and the notification path are both exercised.
const mockMentionHandle = "you"

// RunMock runs the shell against the credential-free scripted chat source.
//
// This is the primary demo path, the bug-report path, and the CI smoke path:
// `yc chat --mock` must drive the full UI with zero credentials and zero
// network, and it must work before any transport code exists.
func RunMock(w io.Writer, cfg config.Config) error {
	return RunMockWithOptions(w, cfg, ClientOptions{})
}

// RunMockWithOptions is RunMock with explicit collaborators.
//
// Any collaborator the caller left nil is filled with a simulated one. The
// point of the mock is that every surface is populated - a demo that shows an
// empty sidebar and an unknown viewer count is not exercising the parts most
// likely to be wrong.
func RunMockWithOptions(w io.Writer, cfg config.Config, opts ClientOptions) error {
	title := mockChatTitle
	if len(cfg.DefaultChats) > 0 {
		if named := strings.TrimSpace(cfg.DefaultChats[0]); named != "" {
			title = named
		}
	}
	// The scripted source speaks for exactly one chat, so the model must have
	// one open. An empty chat list would land on the empty state and the demo
	// would show nothing.
	cfg.DefaultChats = []string{title}
	if strings.TrimSpace(cfg.YouTube.ChannelID) == "" {
		cfg.YouTube.ChannelID = mockMentionHandle
	}

	client := NewMockChatClient(title)
	opts = withMockCollaborators(opts, title)

	model := newLiveModel(cfg, client, nil, opts)
	model.sourceDetail = "mock source"
	// The chat is left deliberately unresolved so the demo drives the real
	// resolution path: the simulated resolver supplies the live chat ID, the
	// video ID, and the title, and resolving is what triggers the first
	// metrics fetch. Pre-filling them here would skip both.
	if state := model.chats.activeState(); state != nil {
		state.mergeTarget(youtube.ChatTarget{Title: title})
	}
	return runModel(w, cfg, model, client)
}

// RunClient runs the shell against a live chat client.
func RunClient(w io.Writer, cfg config.Config, client ChatClient) error {
	return RunClientWithOptions(w, cfg, client, ClientOptions{})
}

// RunClientWithOptions runs the shell against a live chat client with explicit
// collaborators.
//
// It closes the client on return, starts with the first configured chat (an
// empty set is a legal start that lands on the empty state), and renders one
// static frame when w is not an interactive terminal - which is what the CI
// smoke test exercises. In interactive mode it primes the terminal background
// before the program starts, to avoid a flash, and resets it afterward.
func RunClientWithOptions(w io.Writer, cfg config.Config, client ChatClient, opts ClientOptions) error {
	return runModel(w, cfg, newLiveModel(cfg, client, nil, opts), client)
}

// runModel is the shared shell lifecycle for every source.
func runModel(w io.Writer, cfg config.Config, model shellModel, client ChatClient) error {
	if client != nil {
		defer client.Close()
	}

	if !isInteractiveTerminal(w) {
		// A piped writer gets exactly one deterministic frame. Taking over a
		// terminal that is not one would emit an alt-screen sequence into a
		// log file and block forever on a keyboard that will never arrive.
		//
		// The frame is stripped of escape sequences because lipgloss has
		// already dropped every color for a non-terminal profile: what is
		// left is no-op resets around each border cell, which double the size
		// of a smoke-test artifact and make a pasted frame harder to read for
		// no gain at all.
		_, err := fmt.Fprintln(w, ansi.Strip(model.View()))
		return err
	}

	model.terminalOutput = w
	model.splashUntil = splashDeadline(model.animationMode)

	// Priming before the program starts avoids a frame of the terminal's own
	// background flashing behind the first paint.
	primeTerminalBackground(w, canvasBackground(model.theme))
	defer resetTerminalBackground(w)

	options := []tea.ProgramOption{
		tea.WithOutput(w),
		tea.WithAltScreen(),
		tea.WithReportFocus(),
	}
	if cfg.Features.EnableMouse {
		options = append(options, tea.WithMouseCellMotion())
	}

	if _, err := tea.NewProgram(model, options...).Run(); err != nil {
		return fmt.Errorf("run terminal program: %w", err)
	}
	return nil
}

// primeTerminalBackground writes the canvas color once, synchronously, before
// bubbletea takes ownership of the writer.
//
// After the program starts, the OSC sequence travels inside View's return value
// instead: bubbletea's renderer owns the writer from its own goroutine, and
// writing to it directly from here would interleave with a frame.
func primeTerminalBackground(w io.Writer, canvas string) {
	if w == nil || strings.TrimSpace(canvas) == "" {
		return
	}
	_, _ = io.WriteString(w, ansi.SetBackgroundColor(canvas))
}

// resetTerminalBackground hands the terminal's own background back on exit. A
// client that leaves the user's terminal recolored has not really exited.
func resetTerminalBackground(w io.Writer) {
	if w == nil {
		return
	}
	_, _ = io.WriteString(w, ansi.ResetBackgroundColor)
}

// withMockCollaborators fills in simulated collaborators for anything the
// caller did not supply, so the demo exercises every surface.
func withMockCollaborators(opts ClientOptions, title string) ClientOptions {
	source := mockCollaborators{title: title}
	if opts.IdentityLookup == nil {
		opts.IdentityLookup = source
	}
	if opts.BroadcastResolver == nil {
		opts.BroadcastResolver = source
	}
	if opts.SubscriptionLookup == nil {
		opts.SubscriptionLookup = source
	}
	if opts.CategoryLookup == nil {
		opts.CategoryLookup = source
	}
	return opts
}

// mockCollaborators answers every optional lookup with fixed, obviously
// simulated data. It performs no network and no filesystem work.
type mockCollaborators struct {
	title string
}

var (
	_ IdentityLookup     = mockCollaborators{}
	_ BroadcastResolver  = mockCollaborators{}
	_ SubscriptionLookup = mockCollaborators{}
	_ CategoryLookup     = mockCollaborators{}
)

// Identity returns the simulated signed-in channel the local echo renders from.
func (m mockCollaborators) Identity(ctx context.Context) (youtube.Identity, error) {
	if err := ctx.Err(); err != nil {
		return youtube.Identity{}, err
	}
	return youtube.Identity{
		ChannelID:            "UC-mock-self",
		DisplayName:          "you",
		Handle:               "@" + mockMentionHandle,
		SubscriberCount:      12_400,
		SubscriberCountKnown: true,
		Scopes:               []string{"mock"},
	}, nil
}

// ResolveTarget marks the demo chat resolved without any lookup.
func (m mockCollaborators) ResolveTarget(ctx context.Context, target youtube.ChatTarget) (youtube.ChatTarget, error) {
	if err := ctx.Err(); err != nil {
		return target, err
	}
	target.Kind = youtube.TargetLiveChatID
	target.LiveChatID = mockLiveChatID
	target.VideoID = "mockVideo01"
	if strings.TrimSpace(target.Title) == "" {
		target.Title = m.title
	}
	return target, nil
}

// Broadcast returns simulated stream metadata for the LIVE badge and the
// viewer count.
func (m mockCollaborators) Broadcast(ctx context.Context, videoID string) (youtube.BroadcastInfo, error) {
	if err := ctx.Err(); err != nil {
		return youtube.BroadcastInfo{}, err
	}
	return youtube.BroadcastInfo{
		VideoID:           "mockVideo01",
		ChannelID:         "UC-mock-channel",
		Title:             m.title,
		Description:       "A scripted chat for demos, bug reports, and CI. No credentials, no network.",
		LiveChatID:        mockLiveChatID,
		Live:              true,
		ConcurrentViewers: 1337,
		ViewersKnown:      true,
		ActualStartTime:   time.Now().Add(-42 * time.Minute),
	}, nil
}

// Subscriptions feeds the chat picker's autocomplete.
func (m mockCollaborators) Subscriptions(ctx context.Context) ([]youtube.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []youtube.Subscription{
		{ChannelID: "UC-mock-1", Title: "Terminal Renaissance", Handle: "@terminalrenaissance"},
		{ChannelID: "UC-mock-2", Title: "Late Night Compiling", Handle: "@latenightcompiling"},
		{ChannelID: "UC-mock-3", Title: "Ship It Radio", Handle: "@shipitradio"},
	}, nil
}

// Categories feeds the Stream Info tab's category picker.
func (m mockCollaborators) Categories(ctx context.Context) ([]youtube.Category, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []youtube.Category{
		{ID: "20", Title: "Gaming"},
		{ID: "28", Title: "Science & Technology"},
		{ID: "24", Title: "Entertainment"},
	}, nil
}

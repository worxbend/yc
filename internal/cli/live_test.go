package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/app"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/storage"
	"github.com/worxbend/yc/internal/youtube"
)

// A run with nothing to authenticate with must fail before a socket is opened:
// an error that arrives after the alt screen is up is invisible.
func TestValidateLiveChatCredentialsNamesTheThreeWaysForward(t *testing.T) {
	err := validateLiveChatCredentials(credentialCapability{Mode: credentialModeNone})
	if err == nil {
		t.Fatal("a run with no credentials was allowed to start")
	}
	for _, want := range []string{"yc login", "YC_YOUTUBE_API_KEY", "yc chat --mock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message is missing %q:\n%s", want, err)
		}
	}

	for _, mode := range []credentialMode{credentialModeAPIKey, credentialModeOAuth} {
		if err := validateLiveChatCredentials(credentialCapability{Mode: mode}); err != nil {
			t.Errorf("%s mode was refused: %v", mode, err)
		}
	}
}

// A Google access token lasts about an hour and a stream lasts longer. Saying
// so at startup costs one line and turns a mystery disconnect into a known
// limitation.
func TestRefreshCapabilityWarningNamesTheGapThatEndsALongSession(t *testing.T) {
	cfg := config.Default()
	cfg.Google.AccessToken = fakeToken

	warning := refreshCapabilityWarning(cfg, credentialCapability{Mode: credentialModeOAuth})
	if warning == "" {
		t.Fatal("a token with no refresh token produced no warning")
	}
	for _, want := range []string{"cannot be renewed", "about an hour", "yc login"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning is missing %q:\n%s", want, warning)
		}
	}

	withRefresh := cfg
	withRefresh.Google.RefreshToken = "refresh-" + fakeToken
	if got := refreshCapabilityWarning(withRefresh, credentialCapability{Mode: credentialModeOAuth}); got != "" {
		t.Errorf("a refreshable session produced a warning: %q", got)
	}
	// API-key mode has no token to renew, so the warning would be noise.
	if got := refreshCapabilityWarning(cfg, credentialCapability{Mode: credentialModeAPIKey}); got != "" {
		t.Errorf("api-key mode produced a refresh warning: %q", got)
	}
}

// An installed app cannot keep a secret, so the desktop flow authenticates with
// PKCE alone; the note only applies to a client that was issued one.
func TestClientSecretWarningOnlyAppliesWhenARefreshCouldFail(t *testing.T) {
	base := config.Default()
	base.Google.ClientID = "client-abc"
	base.Google.RefreshToken = "refresh-" + fakeToken

	warning := clientSecretWarning(base)
	if warning == "" {
		t.Fatal("a refreshable client with no secret produced no note")
	}
	if !strings.Contains(warning, "YC_GOOGLE_CLIENT_SECRET") {
		t.Errorf("note does not name the variable to set:\n%s", warning)
	}

	withSecret := base
	withSecret.Google.ClientSecret = "secret-" + fakeToken
	if got := clientSecretWarning(withSecret); got != "" {
		t.Errorf("a configured secret produced a note: %q", got)
	}
	noRefresh := base
	noRefresh.Google.RefreshToken = ""
	if got := clientSecretWarning(noRefresh); got != "" {
		t.Errorf("a session with nothing to refresh produced a note: %q", got)
	}
	noClient := base
	noClient.Google.ClientID = ""
	if got := clientSecretWarning(noClient); got != "" {
		t.Errorf("a session with no OAuth client produced a note: %q", got)
	}
}

// The arithmetic that defines this client is stated once, at startup, and only
// when the user has actually opted out of the budget floor.
func TestQuotaWarningOnlyFiresForFollowServerCadence(t *testing.T) {
	cfg := config.Default()
	if got := quotaWarning(cfg); got != "" {
		t.Errorf("the default cadence produced a warning: %q", got)
	}

	cfg.Quota.FollowServerCadence = true
	warning := quotaWarning(cfg)
	for _, want := range []string{"follow_server_cadence", "estimated 5 units per poll", "2000 polls", "daily allowance"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning is missing %q:\n%s", want, warning)
		}
	}

	// An unset cost falls back to the estimate table rather than dividing by
	// zero.
	zeroed := cfg
	zeroed.Quota.Costs.List = 0
	zeroed.Quota.DailyQuotaUnits = 0
	if got := quotaWarning(zeroed); got == "" {
		t.Error("unset figures produced no warning; the defaults should still yield one")
	}
}

// An explicit live chat ID skips resolution entirely, which is the only
// zero-quota way to open a chat.
func TestChatTargetsClassifiesEverySupportedSpelling(t *testing.T) {
	cfg := config.Default()
	cfg.DefaultChats = []string{
		"dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://www.youtube.com/live/dQw4w9WgXcQ",
		"@handle",
		"UCuAXFkgsw1L7xaCfnd5JJOw",
	}

	targets, err := chatTargets(cfg, "Cg0KC2FiYw")
	if err != nil {
		t.Fatalf("chatTargets: %v", err)
	}
	if len(targets) != len(cfg.DefaultChats)+1 {
		t.Fatalf("classified %d targets, want %d", len(targets), len(cfg.DefaultChats)+1)
	}
	if targets[0].Kind != youtube.TargetLiveChatID {
		t.Errorf("the explicit live chat ID is not first: %+v", targets[0])
	}
	for i, target := range targets[1:] {
		if target.Kind == "" {
			t.Errorf("target %d (%q) was not classified", i, target.Raw)
		}
	}
}

// A target that cannot be classified is reported rather than dropped, and the
// classified ones are still returned so the run is not all-or-nothing.
func TestChatTargetsReportsUnreadableTargetsWithoutDroppingTheRest(t *testing.T) {
	cfg := config.Default()
	cfg.DefaultChats = []string{"dQw4w9WgXcQ", "  ", "https://example.com/not-youtube"}

	targets, err := chatTargets(cfg, "")
	if err == nil {
		t.Fatal("unreadable targets produced no error")
	}
	for _, want := range []string{"video ID", "@handle", "--live-chat-id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name the accepted spellings (%q):\n%s", want, err)
		}
	}
	if len(targets) == 0 {
		t.Error("a single bad target discarded the good ones")
	}
}

// The lookups that need a user token are gated on having one: with an API key
// alone they would fail on every call and turn one missing credential into
// several unexplained errors on unrelated surfaces.
func TestLiveClientOptionsGateUserTokenLookups(t *testing.T) {
	client := &youtube.Client{}
	notifier := app.NewDefaultSystemNotifier(&bytes.Buffer{})

	keyOnly := newLiveClientOptions(config.Default(), client, credentialCapability{Mode: credentialModeAPIKey}, debuglog.Logger{}, notifier)
	if keyOnly.IdentityLookup != nil || keyOnly.SubscriptionLookup != nil || keyOnly.StreamInfoManager != nil {
		t.Errorf("api-key mode wired a user-token lookup: %+v", keyOnly)
	}
	// The ones a key can answer stay wired: read-only mode should still label
	// chats by title and show a viewer count.
	if keyOnly.BroadcastResolver == nil || keyOnly.CategoryLookup == nil {
		t.Error("api-key mode dropped a lookup an API key can answer")
	}
	if keyOnly.SystemNotifier == nil {
		t.Error("the notifier was not wired")
	}

	oauth := newLiveClientOptions(config.Default(), client, credentialCapability{Mode: credentialModeOAuth}, debuglog.Logger{}, notifier)
	if oauth.IdentityLookup == nil || oauth.SubscriptionLookup == nil || oauth.StreamInfoManager == nil {
		t.Errorf("oauth mode did not wire the user-token lookups: %+v", oauth)
	}
}

// The server's pollingIntervalMillis is an absolute floor beneath these bounds;
// they only ever slow yc down, never speed it up.
func TestPollIntervalBoundsNeverSpeedYcUp(t *testing.T) {
	cases := []struct {
		name  string
		quota config.QuotaConfig
	}{
		{"auto", config.QuotaConfig{PollIntervalMode: "auto", PollIntervalFloorMS: 1000}},
		{"economy", config.QuotaConfig{PollIntervalMode: "economy", PollIntervalFloorMS: 1000}},
		{"off", config.QuotaConfig{PollIntervalMode: "off", PollIntervalFloorMS: 1000}},
		{"unknown mode", config.QuotaConfig{PollIntervalMode: "turbo", PollIntervalFloorMS: 1000}},
		{"ceiling below floor", config.QuotaConfig{PollIntervalMode: "economy", PollIntervalFloorMS: 8000, PollIntervalCeilingMS: 2000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			minInterval, maxInterval := pollIntervalBounds(tc.quota)
			if minInterval <= 0 {
				t.Fatalf("min = %s, want a positive floor", minInterval)
			}
			if maxInterval != 0 && maxInterval < minInterval {
				t.Errorf("ceiling %s is below the floor %s", maxInterval, minInterval)
			}
		})
	}
}

// The cost table is data precisely so a corrected figure is a config line
// rather than a release, and zero means "not overridden" rather than "free".
func TestCostTableTreatsZeroAsNotOverridden(t *testing.T) {
	defaults := quota.DefaultCostTable()
	table := costTable(config.QuotaCostConfig{})
	for _, endpoint := range []string{
		quota.EndpointMessagesList, quota.EndpointMessagesInsert, quota.EndpointMessagesDelete,
		quota.EndpointBansInsert, quota.EndpointBansDelete,
		quota.EndpointVideosList, quota.EndpointChannelsList, quota.EndpointSearchList,
	} {
		if got, want := table.Cost(endpoint), defaults.Cost(endpoint); got != want {
			t.Errorf("%s cost = %d with no overrides, want the default %d", endpoint, got, want)
		}
	}

	overridden := costTable(config.QuotaCostConfig{List: 7, SearchList: 250})
	if got := overridden.Cost(quota.EndpointMessagesList); got != 7 {
		t.Errorf("list cost = %d, want the override", got)
	}
	if got := overridden.Cost(quota.EndpointSearchList); got != 250 {
		t.Errorf("search cost = %d, want the override", got)
	}
	if got := overridden.Cost(quota.EndpointMessagesInsert); got != defaults.Cost(quota.EndpointMessagesInsert) {
		t.Errorf("insert cost = %d, want it untouched", got)
	}
}

// Startup failures print one redacted, actionable line and exit, because once
// the alt screen is up an error message is invisible.
func TestLiveChatStartupFailuresExitWithoutOpeningTheUI(t *testing.T) {
	clearCredentialEnv(t)
	withMemoryCredentialStore(t, storage.CredentialRecord{})

	originalTransport := newLiveChatClient
	originalRun := runLiveChat
	originalValidator := newTokenValidator
	t.Cleanup(func() {
		newLiveChatClient = originalTransport
		runLiveChat = originalRun
		newTokenValidator = originalValidator
	})
	// Validation must not reach the network in a test.
	newTokenValidator = func(config.Config) tokenValidator { return nil }

	var uiStarted bool
	runLiveChat = func(_ io.Writer, cfg config.Config, client app.ChatClient, opts app.ClientOptions) error {
		uiStarted = true
		return nil
	}
	newLiveChatClient = func(*youtube.Client, config.Config, []youtube.ChatTarget, debuglog.Logger) (app.ChatClient, error) {
		return nil, errors.New("transport unavailable: " + fakeToken)
	}

	t.Setenv("YC_GOOGLE_ACCESS_TOKEN", fakeToken)
	path := writeTempConfig(t, "")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"chat", "--config", path}, &stdout, &stderr); code != ExitFailure {
		t.Fatalf("chat = %d, want %d; stderr=%s", code, ExitFailure, stderr.String())
	}
	if uiStarted {
		t.Error("the UI was started despite a transport failure")
	}
	if !strings.Contains(stderr.String(), "start live chat") {
		t.Errorf("stderr does not name the failed step:\n%s", stderr.String())
	}
	assertNoLeak(t, "a transport failure", stderr.String())
	if strings.Contains(stderr.String(), fakeToken) {
		t.Errorf("the transport error leaked a token:\n%s", stderr.String())
	}
}

// A one-off `--theme nord` must not silently become the user's saved
// preference, so per-run flags are applied to the in-memory config only.
func TestChatFlagOverridesOutrankConfigWithoutBeingWrittenBack(t *testing.T) {
	cfg := config.Default()
	cfg.Features.ThemeName = "claude"
	cfg.Features.MessageLayout = "inline"
	cfg.Features.AnimationMode = "fast"
	cfg.Features.EnableMouse = true

	opts := chatFlagOptions{
		themeName:     optionalTextFlag{value: "NORD", set: true},
		layout:        enumFlag{value: "compact", set: true},
		animation:     enumFlag{value: "off", set: true},
		noMouse:       true,
		sessionHrs:    6,
		followCadence: true,
	}
	applyChatFlagOverrides(&cfg, opts)

	if cfg.Features.ThemeName != "nord" {
		t.Errorf("theme = %q, want the lowercased flag value", cfg.Features.ThemeName)
	}
	if cfg.Features.MessageLayout != "compact" || cfg.Features.AnimationMode != "off" {
		t.Errorf("display flags did not land: %+v", cfg.Features)
	}
	if cfg.Features.EnableMouse {
		t.Error("--no-mouse did not land")
	}
	if cfg.Quota.SessionHours != 6 || !cfg.Quota.FollowServerCadence {
		t.Errorf("quota flags did not land: %+v", cfg.Quota)
	}

	// An absent flag leaves the config value in place.
	untouched := config.Default()
	untouched.Features.ThemeName = "nord"
	applyChatFlagOverrides(&untouched, chatFlagOptions{})
	if untouched.Features.ThemeName != "nord" {
		t.Errorf("theme = %q; an absent flag must not override", untouched.Features.ThemeName)
	}
	if !untouched.Features.EnableMouse {
		t.Error("an absent --no-mouse disabled the mouse")
	}
}

func TestChatRejectsAPositionalArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"chat", "somechannel"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("chat = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "--chat") {
		t.Errorf("stderr does not point at the flag:\n%s", stderr.String())
	}
}

func TestChatRejectsAnUnknownMode(t *testing.T) {
	for _, args := range [][]string{
		{"chat", "--layout", "waterfall"},
		{"chat", "--animation", "sideways"},
		{"chat", "--theme", ""},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != ExitUsage {
			t.Errorf("Run(%v) = %d, want %d", args, code, ExitUsage)
		}
	}
}

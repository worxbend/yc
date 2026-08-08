package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
)

// scriptSetupInput feeds the wizard a canned transcript for one test.
func scriptSetupInput(t *testing.T, answers ...string) {
	t.Helper()
	original := setupInput
	t.Cleanup(func() { setupInput = original })
	setupInput = strings.NewReader(strings.Join(answers, "\n") + "\n")
}

// readConfigFile returns the raw TOML setup wrote, so secrecy can be asserted
// against the bytes on disk rather than against a re-parsed struct.
func readConfigFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(data)
}

func TestSetupNonInteractiveWritesOnlyNonSecretValues(t *testing.T) {
	clearCredentialEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"setup", "--non-interactive",
		"--config", path,
		"--client-id", "client-abc.apps.googleusercontent.com",
		"--channel-id", "UCabcdefghijklmnopqrstuv",
		"--chats", "dQw4w9WgXcQ,@handle",
		"--chat", "https://youtu.be/xyz",
		"--enable-mouse=false",
		"--avatar-mode", "off",
		"--animation-mode", "reduced",
		"--theme", "NORD",
		"--layout", "compact",
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("setup = %d, stderr=%s", code, stderr.String())
	}

	cfg, err := config.Load(nil, config.Overrides{ConfigPath: path})
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Google.ClientID != "client-abc.apps.googleusercontent.com" {
		t.Errorf("client ID = %q", cfg.Google.ClientID)
	}
	if cfg.YouTube.ChannelID != "UCabcdefghijklmnopqrstuv" {
		t.Errorf("channel ID = %q", cfg.YouTube.ChannelID)
	}
	if got := strings.Join(cfg.DefaultChats, "|"); got != "dQw4w9WgXcQ|@handle|https://youtu.be/xyz" {
		t.Errorf("chats = %q; both spellings must accumulate and the syntax must survive", got)
	}
	if cfg.Features.EnableMouse {
		t.Error("--enable-mouse=false did not land")
	}
	if cfg.Features.AvatarMode != "off" || cfg.Features.AnimationMode != "reduced" ||
		cfg.Features.MessageLayout != "compact" || cfg.Features.ThemeName != "nord" {
		t.Errorf("display settings = %+v", cfg.Features)
	}

	// Setup never asks for or writes a secret. Asserting on the file's bytes
	// is the only form that catches a key written with an empty value.
	raw := readConfigFile(t, path)
	for _, forbidden := range []string{
		"google_client_secret", "google_access_token", "google_refresh_token", "youtube_api_key",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("setup wrote the secret key %q into config.toml:\n%s", forbidden, raw)
		}
	}
	if !strings.Contains(stdout.String(), "No credential values were written") {
		t.Errorf("setup did not say it wrote no credentials:\n%s", stdout.String())
	}
}

func TestSetupRejectsAnUnusableModeBeforeWritingAnything(t *testing.T) {
	clearCredentialEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"setup", "--non-interactive", "--config", path, "--avatar-mode", "hologram"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("setup = %d, want %d", code, ExitUsage)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a rejected mode still wrote a config file")
	}
	if !strings.Contains(stderr.String(), "off") || !strings.Contains(stderr.String(), "initials") {
		t.Errorf("stderr does not name the accepted modes:\n%s", stderr.String())
	}
}

func TestSetupRefusesBothLoginSpellings(t *testing.T) {
	var stdout, stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "config.toml")
	if code := Run([]string{"setup", "--config", path, "--login", "--login-dry-run"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("setup = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "only one of") {
		t.Errorf("stderr = %q, want it to say the two flags conflict", stderr.String())
	}
}

func TestSetupRejectsAPositionalArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"setup", "somechannel"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("setup = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "unexpected setup argument") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestSetupHelpGoesToStdoutAndExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"setup", "--help"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("setup --help = %d, want %d", code, ExitOK)
	}
	if stderr.Len() != 0 {
		t.Errorf("help wrote to stderr: %s", stderr.String())
	}
	for _, want := range []string{"never asks for or writes a client secret", "--non-interactive", "-client-id"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help is missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestSetupWizardWalksEveryPromptAndKeepsCurrentValuesOnEmptyAnswers(t *testing.T) {
	clearCredentialEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	scriptSetupInput(t,
		"client-xyz.apps.googleusercontent.com", // client ID
		"UCzzzzzzzzzzzzzzzzzzzzzz",              // channel ID
		"@one, @two",                            // default chats
		"",                                      // avatar mode: keep current
		"reduced",                               // animation mode
		"grouped",                               // layout
		"y",                                     // mouse
		"skip",                                  // credential action
	)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"setup", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("setup = %d, stderr=%s", code, stderr.String())
	}

	cfg, err := config.Load(nil, config.Overrides{ConfigPath: path})
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Google.ClientID != "client-xyz.apps.googleusercontent.com" {
		t.Errorf("client ID = %q", cfg.Google.ClientID)
	}
	if cfg.YouTube.ChannelID != "UCzzzzzzzzzzzzzzzzzzzzzz" {
		t.Errorf("channel ID = %q", cfg.YouTube.ChannelID)
	}
	if got := strings.Join(cfg.DefaultChats, "|"); got != "@one|@two" {
		t.Errorf("chats = %q, want the comma-split, trimmed list", got)
	}
	if cfg.Features.AvatarMode != config.Default().Features.AvatarMode {
		t.Errorf("avatar mode = %q; an empty answer must keep the current value", cfg.Features.AvatarMode)
	}
	if cfg.Features.AnimationMode != "reduced" || cfg.Features.MessageLayout != "grouped" || !cfg.Features.EnableMouse {
		t.Errorf("wizard answers did not land: %+v", cfg.Features)
	}
	if !strings.Contains(stdout.String(), "never asks for tokens") {
		t.Errorf("the wizard did not state its secrecy contract:\n%s", stdout.String())
	}
}

// A wizard that accepted an unusable answer would write a config the app
// silently ignores, so it re-asks instead.
func TestSetupWizardReasksRatherThanAcceptingAnUnknownMode(t *testing.T) {
	clearCredentialEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	scriptSetupInput(t,
		"client", "", "",
		"hologram", "initials", // avatar mode: rejected, then accepted
		"sideways", "fast", // animation mode: rejected, then accepted
		"grouped",
		"maybe", "n", // mouse: rejected, then accepted
		"nonsense", "dry-run", // credential action: rejected, then accepted
	)

	// The dry-run handoff must not touch the network, a listener, or a browser.
	restoreLogin := refuseLoginSideEffects(t)
	defer restoreLogin()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"setup", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("setup = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if strings.Count(out, "Choose one of") < 3 {
		t.Errorf("the wizard did not re-ask on bad answers:\n%s", out)
	}
	if !strings.Contains(out, "Choose y or n.") {
		t.Errorf("the boolean prompt accepted an unparseable answer:\n%s", out)
	}
	if !strings.Contains(out, "Starting login dry run.") {
		t.Errorf("the chosen credential action did not run:\n%s", out)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("the dry run did not explain itself:\n%s", out)
	}
}

// A transcript that ends early is a cancellation, not a config write.
func TestSetupWizardTreatsEOFAsCancellation(t *testing.T) {
	clearCredentialEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	scriptSetupInput(t, "client-id-only")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"setup", "--config", path}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("setup = %d, want %d", code, ExitUsage)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a canceled wizard still wrote a config file")
	}
	if !strings.Contains(stderr.String(), "--non-interactive") {
		t.Errorf("stderr = %q, want it to point at the scripted path", stderr.String())
	}
}

func TestSetupAPIKeyOnlyExplainsTheReadOnlyPath(t *testing.T) {
	clearCredentialEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"setup", "--non-interactive", "--api-key-only", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("setup = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"YC_YOUTUBE_API_KEY", "cannot send messages or moderate", "yc login"} {
		if !strings.Contains(out, want) {
			t.Errorf("api-key guidance is missing %q:\n%s", want, out)
		}
	}
}

// The default tail has to name every way forward, including the one that needs
// no credentials at all.
func TestSetupDefaultTailNamesEveryWayForward(t *testing.T) {
	clearCredentialEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"setup", "--non-interactive", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("setup = %d, stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"yc login", "YC_YOUTUBE_API_KEY", "--mock", "Quota budget"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("setup tail is missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestNormalizeSetupChatsDropsEmptiesWithoutAlteringTargets(t *testing.T) {
	got := normalizeSetupChats([]string{" @handle ", "", "   ", "https://youtu.be/x?t=30"})
	if len(got) != 2 || got[0] != "@handle" || got[1] != "https://youtu.be/x?t=30" {
		t.Errorf("chats = %#v, want the trimmed pair with their syntax intact", got)
	}
	if normalizeSetupChats(nil) != nil {
		t.Error("an empty list must normalize to nil, not to an empty slice the writer would emit")
	}
	if normalizeSetupChats([]string{"  ", ""}) != nil {
		t.Error("an all-empty list must normalize to nil")
	}
}

// refuseLoginSideEffects makes any real login side effect a test failure.
func refuseLoginSideEffects(t *testing.T) func() {
	t.Helper()
	originalWaiter := newLoginCallbackWaiter
	originalBrowser := openLoginBrowser
	newLoginCallbackWaiter = func(string, auth.Secret) (loginCallbackWaiter, error) {
		t.Error("a listener was bound when none should have been")
		return nil, nil
	}
	openLoginBrowser = func(context.Context, string) error {
		t.Error("a browser was opened when none should have been")
		return nil
	}
	return func() {
		newLoginCallbackWaiter = originalWaiter
		openLoginBrowser = originalBrowser
	}
}

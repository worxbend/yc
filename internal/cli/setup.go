package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/worxbend/yc/internal/config"
)

const setupUsage = `Usage:
  yc setup [--config path] [--non-interactive] [--client-id id] [--channel-id id]
           [--chat target]... [--chats a,b] [--api-key-only]
           [--enable-mouse bool] [--avatar-mode mode] [--animation-mode mode]
           [--theme name] [--layout mode] [--login | --login-dry-run]

Creates or updates the flat config file with non-secret settings: OAuth client
ID, your own channel ID, default chats, theme, layout, avatar mode, mouse mode,
and animation mode.

Setup never asks for or writes a client secret, an access token, a refresh
token, an authorization code, OAuth state, or an API key. Use the login handoff
to save tokens through the private credential store, or set YC_YOUTUBE_API_KEY
in your environment for read-only mode.

Flags:
`

// setupInput is the wizard's input source, indirected so tests can script it.
var setupInput io.Reader = os.Stdin

// setupCredentialAction is what setup does once the non-secret config is
// written.
type setupCredentialAction string

const (
	setupCredentialSkip   setupCredentialAction = "skip"
	setupCredentialLogin  setupCredentialAction = "login"
	setupCredentialDryRun setupCredentialAction = "dry-run"
)

// setupFlagOptions is everything `yc setup` accepts.
type setupFlagOptions struct {
	cfgPath        string
	nonInteractive bool
	clientID       optionalTextFlag
	channelID      optionalTextFlag
	chats          chatFlags
	apiKeyOnly     bool
	enableMouse    optionalBoolFlag
	avatarMode     enumFlag
	animationMode  enumFlag
	themeName      optionalTextFlag
	layout         enumFlag
	login          bool
	loginDryRun    bool
}

// runSetup writes non-secret config and optionally hands off to login.
func runSetup(args []string, stdout, stderr io.Writer) int {
	opts := setupFlagOptions{
		avatarMode:    newEnumFlag("avatar mode", avatarModes),
		animationMode: newEnumFlag("animation mode", animationModes),
		layout:        newEnumFlag("layout", layoutModes),
	}
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.cfgPath, "config", "", "config file path")
	fs.BoolVar(&opts.nonInteractive, "non-interactive", false, "write config from flags and current values without prompting")
	fs.Var(&opts.clientID, "client-id", "Google OAuth client ID to write to config")
	fs.Var(&opts.channelID, "channel-id", "your own YouTube channel ID")
	fs.Var(&opts.chats, "chat", "default chat to open; repeat for multiple chats")
	fs.Var(&opts.chats, "chats", "comma-separated default chats (adds to --chat)")
	fs.BoolVar(&opts.apiKeyOnly, "api-key-only", false, "explain the API-key read-only path instead of the login handoff")
	fs.Var(&opts.enableMouse, "enable-mouse", "enable terminal mouse support")
	fs.Var(&opts.avatarMode, "avatar-mode", "avatar mode: off or initials")
	fs.Var(&opts.animationMode, "animation-mode", "animation mode: off, reduced, or fast")
	fs.Var(&opts.themeName, "theme", "theme preset name")
	fs.Var(&opts.layout, "layout", "message layout: inline, grouped, or compact")
	fs.BoolVar(&opts.login, "login", false, "run `yc login` after writing non-secret config")
	fs.BoolVar(&opts.loginDryRun, "login-dry-run", false, "run `yc login --dry-run` after writing non-secret config")
	fs.Usage = func() {
		fmt.Fprint(stderr, setupUsage)
		fs.PrintDefaults()
	}

	if code, ok := parseCommandFlags(fs, args, setupUsage, "setup", stdout, stderr); !ok {
		return code
	}
	if opts.login && opts.loginDryRun {
		fmt.Fprintln(stderr, "choose only one of --login or --login-dry-run")
		return ExitUsage
	}

	cfg, code := loadCommandConfig(config.Overrides{ConfigPath: opts.cfgPath}, stderr)
	if code != ExitOK {
		return code
	}
	applySetupFlagOptions(&cfg, opts)

	action := setupCredentialSkip
	switch {
	case opts.login:
		action = setupCredentialLogin
	case opts.loginDryRun:
		action = setupCredentialDryRun
	}

	var err error
	if opts.nonInteractive {
		cfg, err = runSetupNonInteractive(cfg, stdout, stderr)
	} else {
		cfg, action, err = runSetupWizard(cfg, action, stdout)
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(stderr, "setup canceled before all prompts were answered; rerun with --non-interactive for scripted installs")
			return ExitUsage
		}
		fmt.Fprintf(stderr, "setup: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitUsage
	}

	if err := config.WriteNonSecretFile(cfg.Path, cfg); err != nil {
		fmt.Fprintf(stderr, "write config: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	displayPath := config.RedactDisplayValue(cfg.Path)
	fmt.Fprintf(stdout, "Updated non-secret config: %s\n", displayPath)
	fmt.Fprintln(stdout, "No credential values were written. Tokens live in the private credential store; the API key lives in your environment.")
	if summary := quotaEnvironmentSummary(cfg); summary != "" {
		fmt.Fprintf(stdout, "Quota budget: %s.\n", summary)
	}

	switch {
	case action == setupCredentialLogin:
		fmt.Fprintln(stdout, "Starting login handoff.")
		return runLogin([]string{"--config", cfg.Path}, stdout, stderr)
	case action == setupCredentialDryRun:
		fmt.Fprintln(stdout, "Starting login dry run.")
		return runLogin([]string{"--config", cfg.Path, "--dry-run"}, stdout, stderr)
	case opts.apiKeyOnly:
		fmt.Fprintln(stdout, "Next: set YC_YOUTUBE_API_KEY to a Google API key to read public live chats without logging in.")
		fmt.Fprintln(stdout, "Key-only mode cannot send messages or moderate; run `yc login` when you want those.")
		return ExitOK
	default:
		fmt.Fprintf(stdout, "Next: run `yc login --config %s` once your Google OAuth client ID is ready,\n", displayPath)
		fmt.Fprintln(stdout, "or set YC_YOUTUBE_API_KEY for read-only mode. `yc chat --mock` needs neither.")
		return ExitOK
	}
}

// applySetupFlagOptions folds flag values into the loaded config.
func applySetupFlagOptions(cfg *config.Config, opts setupFlagOptions) {
	if opts.clientID.set {
		cfg.Google.ClientID = strings.TrimSpace(opts.clientID.value)
	}
	if opts.channelID.set {
		cfg.YouTube.ChannelID = strings.TrimSpace(opts.channelID.value)
	}
	if len(opts.chats) > 0 {
		cfg.DefaultChats = append([]string(nil), opts.chats...)
	}
	if opts.enableMouse.set {
		cfg.Features.EnableMouse = opts.enableMouse.value
	}
	if opts.avatarMode.set {
		cfg.Features.AvatarMode = opts.avatarMode.value
	}
	if opts.animationMode.set {
		cfg.Features.AnimationMode = opts.animationMode.value
	}
	if opts.themeName.set {
		cfg.Features.ThemeName = strings.ToLower(opts.themeName.value)
	}
	if opts.layout.set {
		cfg.Features.MessageLayout = opts.layout.value
	}
}

// runSetupNonInteractive applies flag-supplied values without prompting, for
// scripted installs.
func runSetupNonInteractive(cfg config.Config, stdout, stderr io.Writer) (config.Config, error) {
	if err := validateAndNormalizeSetupConfig(&cfg); err != nil {
		return cfg, err
	}
	_ = stderr
	fmt.Fprintf(stdout, "Writing non-secret config from flags and current values.\n")
	return cfg, nil
}

// runSetupWizard is the prompting half of setup, shared by the interactive
// entrypoint and the command dispatcher.
func runSetupWizard(cfg config.Config, defaultAction setupCredentialAction, stdout io.Writer) (config.Config, setupCredentialAction, error) {
	wizard := setupWizard{reader: bufio.NewReader(setupInput), stdout: stdout}
	action, err := wizard.run(&cfg, defaultAction)
	if err != nil {
		return cfg, defaultAction, err
	}
	if err := validateAndNormalizeSetupConfig(&cfg); err != nil {
		return cfg, action, err
	}
	return cfg, action, nil
}

// validateAndNormalizeSetupConfig lowercases and checks the enumerated modes,
// so an unusable value is rejected before it reaches the config file rather
// than degrading silently at the next startup.
func validateAndNormalizeSetupConfig(cfg *config.Config) error {
	var err error
	if cfg.Features.AvatarMode, err = normalizeSetupEnum("avatar mode", cfg.Features.AvatarMode, avatarModes); err != nil {
		return err
	}
	if cfg.Features.AnimationMode, err = normalizeSetupEnum("animation mode", cfg.Features.AnimationMode, animationModes); err != nil {
		return err
	}
	if cfg.Features.MessageLayout, err = normalizeSetupEnum("message layout", cfg.Features.MessageLayout, layoutModes); err != nil {
		return err
	}
	if cfg.Features.BadgeMode, err = normalizeSetupEnum("badge mode", cfg.Features.BadgeMode, badgeModes); err != nil {
		return err
	}
	cfg.DefaultChats = normalizeSetupChats(cfg.DefaultChats)
	return nil
}

// normalizeSetupEnum lowercases a mode and rejects an unknown one.
func normalizeSetupEnum(name, value string, allowed []string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if stringIn(value, allowed) {
		return value, nil
	}
	return "", fmt.Errorf("%s must be one of: %s", name, strings.Join(allowed, ", "))
}

// normalizeSetupChats trims and drops empty entries without altering the value,
// because a chat may legitimately be a URL or an @handle.
func normalizeSetupChats(values []string) []string {
	chats := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			chats = append(chats, trimmed)
		}
	}
	if len(chats) == 0 {
		return nil
	}
	return chats
}

// setupWizard prompts for the non-secret settings.
type setupWizard struct {
	reader *bufio.Reader
	stdout io.Writer
}

// run walks every prompt and returns the credential handoff the user chose.
func (w setupWizard) run(cfg *config.Config, defaultAction setupCredentialAction) (setupCredentialAction, error) {
	fmt.Fprintln(w.stdout, "yc setup")
	fmt.Fprintf(w.stdout, "Config file: %s\n", config.RedactDisplayValue(cfg.Path))
	fmt.Fprintln(w.stdout, "Setup writes non-secret settings only. It never asks for tokens, a client secret, or an API key.")

	// The wizard is a list of questions, so it is written as one. Each step
	// asks for a setting and writes the answer straight back into cfg; the
	// loop below is the only place the error check lives, instead of once
	// per question.
	steps := []func() error{
		promptStep(&cfg.Google.ClientID, func() (string, error) {
			return w.promptText("Google OAuth client ID", cfg.Google.ClientID)
		}),
		promptStep(&cfg.YouTube.ChannelID, func() (string, error) {
			return w.promptText("Your own YouTube channel ID (optional)", cfg.YouTube.ChannelID)
		}),
		promptStep(&cfg.DefaultChats, func() ([]string, error) {
			return w.promptChats("Default chats (video ID, URL, @handle, or channel ID)", cfg.DefaultChats)
		}),
		promptStep(&cfg.Features.AvatarMode, func() (string, error) {
			return w.promptEnum("Avatar mode", cfg.Features.AvatarMode, avatarModes)
		}),
		promptStep(&cfg.Features.AnimationMode, func() (string, error) {
			return w.promptEnum("Animation mode", cfg.Features.AnimationMode, animationModes)
		}),
		promptStep(&cfg.Features.MessageLayout, func() (string, error) {
			return w.promptEnum("Message layout", cfg.Features.MessageLayout, layoutModes)
		}),
		promptStep(&cfg.Features.EnableMouse, func() (bool, error) {
			return w.promptBool("Enable mouse", cfg.Features.EnableMouse)
		}),
	}
	for _, ask := range steps {
		if err := ask(); err != nil {
			return "", err
		}
	}

	return w.promptCredentialAction(defaultAction)
}

// promptStep pairs one question with the setting it answers: it runs ask and,
// unless the user cut the wizard short, stores the answer in target.
func promptStep[T any](target *T, ask func() (T, error)) func() error {
	return func() error {
		answer, err := ask()
		if err != nil {
			return err
		}
		*target = answer
		return nil
	}
}

// promptText asks for a single value, keeping the current one on an empty
// answer.
func (w setupWizard) promptText(label, current string) (string, error) {
	fmt.Fprintf(w.stdout, "%s [%s]: ", label, current)
	value, err := w.readLine()
	if err != nil {
		return "", err
	}
	if value == "" {
		return strings.TrimSpace(current), nil
	}
	return strings.TrimSpace(value), nil
}

// promptChats asks for the default chat list.
func (w setupWizard) promptChats(label string, current []string) ([]string, error) {
	fmt.Fprintf(w.stdout, "%s [%s]: ", label, strings.Join(current, ","))
	value, err := w.readLine()
	if err != nil {
		return nil, err
	}
	if value == "" {
		return normalizeSetupChats(current), nil
	}
	return normalizeSetupChats(strings.Split(value, ",")), nil
}

// promptBool asks a yes/no question.
func (w setupWizard) promptBool(label string, current bool) (bool, error) {
	defaultValue := "n"
	if current {
		defaultValue = "y"
	}
	for {
		fmt.Fprintf(w.stdout, "%s (y/n) [%s]: ", label, defaultValue)
		value, err := w.readLine()
		if err != nil {
			return false, err
		}
		if value == "" {
			return current, nil
		}
		switch strings.ToLower(value) {
		case "y", "yes", "true", "1", "on":
			return true, nil
		case "n", "no", "false", "0", "off":
			return false, nil
		default:
			fmt.Fprintln(w.stdout, "Choose y or n.")
		}
	}
}

// promptEnum asks for one of a fixed set, re-asking rather than accepting a
// value the app would silently ignore.
func (w setupWizard) promptEnum(label, current string, allowed []string) (string, error) {
	current = strings.ToLower(strings.TrimSpace(current))
	currentAllowed := stringIn(current, allowed)
	for {
		if currentAllowed {
			fmt.Fprintf(w.stdout, "%s (%s) [%s]: ", label, strings.Join(allowed, "/"), current)
		} else {
			fmt.Fprintf(w.stdout, "%s (%s): ", label, strings.Join(allowed, "/"))
		}
		value, err := w.readLine()
		if err != nil {
			return "", err
		}
		if value == "" {
			if currentAllowed {
				return current, nil
			}
			fmt.Fprintf(w.stdout, "Choose one of: %s.\n", strings.Join(allowed, ", "))
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if stringIn(value, allowed) {
			return value, nil
		}
		fmt.Fprintf(w.stdout, "Choose one of: %s.\n", strings.Join(allowed, ", "))
	}
}

// promptCredentialAction asks what to do about credentials once the config is
// written.
func (w setupWizard) promptCredentialAction(current setupCredentialAction) (setupCredentialAction, error) {
	if current == "" {
		current = setupCredentialSkip
	}
	allowed := []string{string(setupCredentialSkip), string(setupCredentialLogin), string(setupCredentialDryRun)}
	for {
		fmt.Fprintf(w.stdout, "Credential setup (%s) [%s]: ", strings.Join(allowed, "/"), current)
		value, err := w.readLine()
		if err != nil {
			return "", err
		}
		if value == "" {
			return current, nil
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if stringIn(value, allowed) {
			return setupCredentialAction(value), nil
		}
		fmt.Fprintf(w.stdout, "Choose one of: %s.\n", strings.Join(allowed, ", "))
	}
}

// readLine reads one answer, treating a final line without a newline as an
// answer rather than as a cancellation.
func (w setupWizard) readLine() (string, error) {
	line, err := w.reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && line != "" {
			return strings.TrimSpace(line), nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

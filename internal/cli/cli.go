package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/app"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/theme"
)

// Exit codes. They are part of the CLI contract: scripts distinguish a runtime
// failure from a usage error.
const (
	// ExitOK is a successful run.
	ExitOK = 0
	// ExitFailure is a runtime failure.
	ExitFailure = 1
	// ExitUsage is a usage or validation failure.
	ExitUsage = 2
)

// Version is the build identity reported by `yc --version`. Release builds
// override it through -ldflags.
var Version = "dev"

// Mode vocabularies shared by the flags, the setup wizard, and validation, so
// an accepted value on one surface is an accepted value on all of them.
var (
	avatarModes    = []string{"off", "initials"}
	animationModes = []string{"off", "reduced", "fast"}
	layoutModes    = []string{"inline", "grouped", "compact"}
	badgeModes     = []string{"glyph", "text", "off"}
)

const usage = `yc is a terminal YouTube live chat client.

Usage:
  yc chat [--chat ID] [--chats a,b] [--video ID] [--channel @handle]
          [--live-chat-id ID] [--mock] [--theme NAME] [--layout MODE]
          [--animation MODE] [--no-mouse] [--session-hours N] [--follow-server-cadence]
          [--debug-log] [--debug-log-path PATH] [--config PATH]
  yc config show [--config PATH]
  yc config path
  yc doctor [--config PATH] [--debug-log] [--debug-log-path PATH]
  yc export superchats [--dir DIR] [--out FILE] [--config PATH]
  yc login [--redirect-uri URL] [--timeout DURATION] [--dry-run] [--read-only]
           [--write-default-config] [--debug-log] [--debug-log-path PATH]
           [--config PATH]
  yc logout [--config PATH] [--keep-remote]
  yc profile list|show|set <name> [--background '#rrggbb' ... --success '#rrggbb'] [--config PATH]
  yc quota [--config PATH]
  yc setup [--non-interactive] [--client-id ID] [--api-key-only] [--channel-id ID]
           [--chat ID] [--chats a,b] [--enable-mouse BOOL] [--avatar-mode MODE]
           [--animation-mode MODE] [--theme NAME] [--layout MODE]
           [--login|--login-dry-run] [--config PATH]
  yc --version

Chats:
  A chat is named by a video ID, a watch/live/shorts URL, a youtu.be link, an
  @handle, a channel ID, or an explicit --live-chat-id (which skips resolution
  and spends no quota). --chat repeats and --chats is comma-separated; the two
  accumulate. --video and --channel are spellings of the same accumulating list.
  Opening no chats is a supported start.

Quota:
  The YouTube Data API grants 10,000 units per day, resetting at midnight
  Pacific. Reading chat at the cadence YouTube advises exhausts that in under
  three hours, so yc stretches its poll interval to make the budget last and
  shows the estimate in the status bar. Google publishes no per-method cost for
  any live chat method, so the per-poll cost yc budgets against is an estimate;
  yc doctor prints the whole table and marks which figures are published.

Environment:
  YC_GOOGLE_CLIENT_ID, GOOGLE_CLIENT_ID
  YC_GOOGLE_CLIENT_SECRET, GOOGLE_CLIENT_SECRET
  YC_GOOGLE_ACCESS_TOKEN, GOOGLE_ACCESS_TOKEN
  YC_GOOGLE_REFRESH_TOKEN, GOOGLE_REFRESH_TOKEN
  YC_GOOGLE_REDIRECT_URL, GOOGLE_REDIRECT_URL
  YC_YOUTUBE_API_KEY
  YC_YOUTUBE_CHANNEL_ID
  YC_DEFAULT_CHATS
  YC_ENABLE_MOUSE, YC_AVATAR_MODE, YC_ANIMATION_MODE, YC_THEME_NAME
  YC_THEME_BACKGROUND, YC_THEME_FOREGROUND, YC_THEME_ACCENT, YC_THEME_MUTED,
  YC_THEME_BORDER, YC_THEME_SURFACE, YC_THEME_WARNING, YC_THEME_ERROR,
  YC_THEME_SUCCESS
  YC_MESSAGE_LAYOUT, YC_BADGE_MODE, YC_HIGHLIGHT_EMOJI, YC_FULL_USERNAME
  YC_SIDEBAR_WIDTH, YC_ACTIVITY_WIDTH, YC_SCROLLBACK_LIMIT
  YC_STREAM_STATUS_MODE, YC_EMOJI_AUTOCOMPLETE_MODE
  YC_AUTO_FOLLOW, YC_AUTO_FOLLOW_POLL_SECONDS, YC_AUTO_FOLLOW_MAX_CHECKS
  YC_POLL_INTERVAL_MODE, YC_POLL_INTERVAL_FLOOR_MS, YC_POLL_INTERVAL_CEILING_MS
  YC_DAILY_QUOTA_UNITS, YC_SEARCH_QUOTA_CALLS, YC_QUOTA_RESERVE_PERCENT
  YC_SESSION_HOURS, YC_FOLLOW_SERVER_CADENCE, YC_ALLOW_SEARCH
  YC_QUOTA_COST_LIST, YC_QUOTA_COST_INSERT, YC_QUOTA_COST_DELETE,
  YC_QUOTA_COST_BANS_INSERT, YC_QUOTA_COST_BANS_DELETE,
  YC_QUOTA_COST_VIDEOS_LIST, YC_QUOTA_COST_CHANNELS_LIST,
  YC_QUOTA_COST_SEARCH_LIST
  YC_CHAT_LOG, YC_CHAT_LOG_DIR, YC_CHAT_LOG_MAX_BYTES, YC_CHAT_LOG_MAX_FILES
  YC_DEBUG_LOG, YC_DEBUG_LOG_PATH
`

// Run dispatches a command line and returns the process exit code. It never
// panics on user input and never prints a credential.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return ExitOK
	}

	switch command := strings.TrimSpace(args[0]); command {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return ExitOK
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "yc %s\n", Version)
		return ExitOK
	case "chat":
		return runChat(args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "export":
		return runExport(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "login":
		return runLogin(args[1:], stdout, stderr)
	case "logout":
		return runLogout(args[1:], stdout, stderr)
	case "profile":
		return runProfile(args[1:], stdout, stderr)
	case "quota":
		return runQuota(args[1:], stdout, stderr)
	case "setup":
		return runSetup(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", command)
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
}

// chatFlagOptions is everything `yc chat` accepts.
type chatFlagOptions struct {
	chats         chatFlags
	liveChatID    string
	cfgPath       string
	mock          bool
	noMouse       bool
	themeName     optionalTextFlag
	layout        enumFlag
	animation     enumFlag
	sessionHrs    int
	followCadence bool
	debug         debugFlagOptions
}

// runChat starts the TUI. With --mock it needs no credentials and no network.
func runChat(args []string, stdout, stderr io.Writer) int {
	opts := chatFlagOptions{
		layout:    newEnumFlag("layout", layoutModes),
		animation: newEnumFlag("animation mode", animationModes),
	}
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&opts.chats, "chat", "live chat to open; repeat for multiple chats")
	fs.Var(&opts.chats, "chats", "comma-separated live chats to open (adds to --chat)")
	fs.Var(&opts.chats, "video", "video ID or watch URL to open (adds to --chat)")
	fs.Var(&opts.chats, "channel", "@handle or channel ID to open (adds to --chat)")
	fs.StringVar(&opts.liveChatID, "live-chat-id", "", "open an explicit liveChatId, skipping resolution and spending no quota")
	fs.StringVar(&opts.cfgPath, "config", "", "config file path")
	fs.BoolVar(&opts.mock, "mock", false, "run against the built-in scripted chat source with no credentials and no network")
	fs.BoolVar(&opts.noMouse, "no-mouse", false, "disable terminal mouse reporting for this run")
	fs.Var(&opts.themeName, "theme", "theme preset name for this run")
	fs.Var(&opts.layout, "layout", "message layout: inline, grouped, or compact")
	fs.Var(&opts.animation, "animation", "animation mode: off, reduced, or fast")
	fs.IntVar(&opts.sessionHrs, "session-hours", 0, "budget quota to the next N hours instead of the Pacific reset")
	fs.BoolVar(&opts.followCadence, "follow-server-cadence", false, "poll at YouTube's advised interval, ignoring the daily budget floor")
	addDebugFlags(fs, &opts.debug)

	// The positional check is kept here rather than handed to the harness:
	// a stray argument to `yc` is nearly always a chat name, and the reply
	// says so instead of printing the whole usage page.
	if code, ok := parseCommandFlags(fs, args, usage, "", stdout, stderr); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected chat argument %q; name a chat with --chat rather than positionally\n", fs.Arg(0))
		return ExitUsage
	}

	overrides := config.Overrides{
		ConfigPath: opts.cfgPath,
		Chats:      []string(opts.chats),
		LiveChatID: strings.TrimSpace(opts.liveChatID),
	}
	applyDebugFlagOverrides(&overrides, opts.debug)
	cfg, code := loadCommandConfig(overrides, stderr)
	if code != ExitOK {
		return code
	}
	applyChatFlagOverrides(&cfg, opts)

	if opts.mock {
		return runMockChat(cfg, stdout, stderr)
	}
	return runLiveChatSession(cfg, overrides.LiveChatID, stdout, stderr)
}

// applyChatFlagOverrides folds the per-run display flags into the config.
//
// They outrank environment and file because they were typed for this run, and
// they are not written back: a one-off `--theme nord` must not silently become
// the user's saved preference.
func applyChatFlagOverrides(cfg *config.Config, opts chatFlagOptions) {
	if opts.themeName.set {
		cfg.Features.ThemeName = strings.ToLower(opts.themeName.value)
	}
	if opts.layout.set {
		cfg.Features.MessageLayout = opts.layout.value
	}
	if opts.animation.set {
		cfg.Features.AnimationMode = opts.animation.value
	}
	if opts.noMouse {
		cfg.Features.EnableMouse = false
	}
	if opts.sessionHrs > 0 {
		cfg.Quota.SessionHours = opts.sessionHrs
	}
	if opts.followCadence {
		cfg.Quota.FollowServerCadence = true
	}
}

// runMockChat drives the full UI with no credentials and no network. It is the
// demo path, the bug-report path, and the CI smoke path.
func runMockChat(cfg config.Config, stdout, stderr io.Writer) int {
	logger, closeLog, err := openDebugLogger(cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "open debug log: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	defer func() { _ = closeLog.Close() }()

	ctx := context.Background()
	logger.Log(ctx, "cli.chat.start",
		slog.Bool("mock", true),
		slog.Int("chat_count", len(cfg.DefaultChats)),
	)
	warnUnknownTheme(cfg, stderr)

	opts := app.ClientOptions{
		DebugLogger:    logger,
		SystemNotifier: app.NewDefaultSystemNotifier(stdout),
	}
	if err := app.RunMockWithOptions(stdout, cfg, opts); err != nil {
		logger.Log(ctx, "cli.chat.failed", debuglog.Err("error", err))
		fmt.Fprintf(stderr, "mock chat: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	logger.Log(ctx, "cli.chat.complete", slog.Bool("mock", true))
	return ExitOK
}

// runLiveChatSession is the credentialed startup path: resolve credentials,
// validate the token when Google is reachable, build the transport, and hand
// off to the app.
//
// Every failure before the handoff prints one redacted, actionable line and
// exits, because once the alt screen is up an error message is invisible.
func runLiveChatSession(cfg config.Config, liveChatID string, stdout, stderr io.Writer) int {
	ctx := context.Background()

	status, err := applyStoredCredentials(ctx, &cfg)
	if err != nil {
		fmt.Fprintf(stderr, "load credentials: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	logger, closeLog, err := openDebugLogger(cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "open debug log: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	defer func() { _ = closeLog.Close() }()

	capability := describeCapability(cfg, status)
	logger.Log(ctx, "cli.chat.start",
		slog.Bool("mock", false),
		slog.String("credential_mode", string(capability.Mode)),
		slog.Int("chat_count", len(cfg.DefaultChats)),
	)

	// Refuse before any socket is opened: a run with nothing to authenticate
	// with must never produce a network error as its first symptom.
	if err := validateLiveChatCredentials(capability); err != nil {
		logger.Log(ctx, "cli.chat.no_credentials")
		fmt.Fprintln(stderr, err)
		if hint := credentialPrecedenceHint(status); hint != "" {
			fmt.Fprintln(stderr, hint)
		}
		return ExitUsage
	}

	targets, err := chatTargets(cfg, liveChatID)
	if err != nil {
		fmt.Fprintf(stderr, "read chats: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitUsage
	}

	redactor := startupRedactor(cfg)
	record := credentialRecordFromConfig(cfg, status.Record)
	holder := newCredentialHolder(record, status.Store, newTokenRefresher(cfg))

	if !validateOAuthToken(ctx, cfg, capability, holder, status, logger, stderr) {
		return ExitUsage
	}

	printStartupWarnings(cfg, capability, stderr)

	// One holder, one ledger, one REST client: a refresh reaches every
	// feature, and the quota meter counts every call yc makes.
	refreshCtx, cancelRefresh := context.WithCancel(ctx)
	defer cancelRefresh()
	holder.startRefreshLoop(refreshCtx, func(err error) {
		logger.Log(refreshCtx, "cli.chat.refresh_failed", debuglog.Err("error", err))
	})

	ledger := newQuotaLedger(cfg)
	// Yesterday's records are dead weight - the meter only reads today's -
	// and nothing else ever deletes them. Startup is the natural moment to
	// sweep, and the sweep is detached and deadlined so it cannot delay or
	// fail the launch it is riding on.
	startLedgerPrune(ctx, logger)

	client, err := newYouTubeClient(cfg, holder, ledger, logger)
	if err != nil {
		logger.Log(ctx, "cli.chat.client_failed", debuglog.Err("error", err))
		fmt.Fprintf(stderr, "build YouTube client: %s\n", safeStartupError(redactor, err))
		return ExitFailure
	}

	chatClient, err := newLiveChatClient(client, cfg, targets, logger)
	if err != nil {
		logger.Log(ctx, "cli.chat.transport_failed", debuglog.Err("error", err))
		fmt.Fprintf(stderr, "start live chat: %s\n", safeStartupError(redactor, err))
		return ExitFailure
	}

	options := newLiveClientOptions(cfg, client, capability, logger, app.NewDefaultSystemNotifier(stdout))
	// The typed-nil check matters: assigning a nil *chatlog.Writer into the
	// interface field would make it non-nil and send every message into a
	// writer that no longer exists.
	if chatLog := openChatLogWriter(cfg, logger); chatLog != nil {
		defer func() { _ = chatLog.Close() }()
		options.ChatLogger = chatLog
	}
	if err := runLiveChat(stdout, cfg, chatClient, options); err != nil {
		logger.Log(ctx, "cli.chat.failed", debuglog.Err("error", err))
		fmt.Fprintf(stderr, "live chat: %s\n", safeStartupError(redactor, err))
		return ExitFailure
	}
	logger.Log(ctx, "cli.chat.complete", slog.Bool("mock", false))
	return ExitOK
}

// validateOAuthToken checks a stored OAuth access token with Google before the
// first API call, and reports whether startup should continue.
//
// The policy: a token Google actively rejects gets one refresh attempt, because
// an expired access token is the ordinary case and sending someone back to the
// browser for it would be rude. If the refresh also fails, startup stops - the
// alternative is a wall of 401s once the interface is up. If Google cannot be
// reached at all, that is not evidence against the token, so yc warns and
// carries on. A token that is valid but thin on scopes only earns a warning.
func validateOAuthToken(
	ctx context.Context,
	cfg config.Config,
	capability credentialCapability,
	holder *credentialHolder,
	status credentialLoadStatus,
	logger debuglog.Logger,
	stderr io.Writer,
) bool {
	if capability.Mode != credentialModeOAuth {
		return true
	}
	validation := validateAccessToken(ctx, cfg, newTokenValidator(cfg))
	switch {
	case validation.Reachable && !validation.Valid:
		if refreshErr := holder.Refresh(ctx); refreshErr != nil {
			logger.Log(ctx, "cli.chat.token_rejected", debuglog.Err("error", refreshErr))
			fmt.Fprintln(stderr, tokenValidationError(validation))
			if hint := credentialPrecedenceHint(status); hint != "" {
				fmt.Fprintln(stderr, hint)
			}
			return false
		}
		logger.Log(ctx, "cli.chat.token_refreshed")
	case !validation.Reachable:
		logger.Log(ctx, "cli.chat.token_validation_skipped")
		fmt.Fprintf(stderr, "warning: could not validate the access token (%s); continuing to the API\n", validation.Detail)
	default:
		if warning := tokenScopeWarning(validation); warning != "" {
			fmt.Fprintln(stderr, warning)
		}
	}
	return true
}

// printStartupWarnings prints every non-fatal thing worth saying before the
// interface takes over the screen. Each of these describes a session that will
// run but is missing something - a refresh that cannot happen, a quota that is
// close to the edge, a theme name nothing recognizes.
func printStartupWarnings(cfg config.Config, capability credentialCapability, stderr io.Writer) {
	for _, warning := range []string{
		refreshCapabilityWarning(cfg, capability),
		clientSecretWarning(cfg),
		quotaWarning(cfg),
	} {
		if warning != "" {
			fmt.Fprintln(stderr, warning)
		}
	}
	warnUnknownTheme(cfg, stderr)
}

// warnUnknownTheme reports a theme name yc does not recognize.
//
// An unknown name degrades to the default palette rather than failing startup,
// but silently ignoring it means a typo looks like a broken theme system.
func warnUnknownTheme(cfg config.Config, stderr io.Writer) {
	name := strings.TrimSpace(cfg.Features.ThemeName)
	if name == "" {
		return
	}
	if _, ok := theme.ResolvePalette(name, cfg.Features.ThemeCustom); ok {
		return
	}
	fmt.Fprintf(stderr, "warning: unknown theme %q; using the default palette. Run `yc profile list` for available names.\n", name)
}

// runConfig implements `yc config show` and `yc config path`. Secret values are
// printed as placeholders, never as values.
func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: yc config show|path")
		return ExitUsage
	}

	switch strings.TrimSpace(args[0]) {
	case "path":
		path, err := config.DefaultPath()
		if err != nil {
			fmt.Fprintf(stderr, "config path: %s\n", config.RedactDisplayValue(err.Error()))
			return ExitFailure
		}
		fmt.Fprintln(stdout, config.RedactDisplayValue(path))
		return ExitOK
	case "show":
		fs := flag.NewFlagSet("config show", flag.ContinueOnError)
		fs.SetOutput(stderr)
		var cfgPath string
		fs.StringVar(&cfgPath, "config", "", "config file path")
		if err := fs.Parse(args[1:]); err != nil {
			return ExitUsage
		}
		cfg, _, err := loadConfigWithStoredCredentials(context.Background(), os.Environ(), config.Overrides{ConfigPath: cfgPath})
		if err != nil {
			fmt.Fprintf(stderr, "load config: %s\n", config.RedactDisplayValue(err.Error()))
			return ExitFailure
		}
		fmt.Fprint(stdout, cfg.RedactedString())
		return ExitOK
	default:
		fmt.Fprintf(stderr, "unknown config command %q\n", args[0])
		return ExitUsage
	}
}

// doctorReachabilityProbe is indirected so diagnostics can be exercised with no
// network at all.
var doctorReachabilityProbe = app.ProbeYouTubeReachability

// buildDoctorReport assembles the diagnostic report. It is a variable so tests
// can supply a deterministic report.
var buildDoctorReport = func(ctx context.Context, cfg config.Config, cfgErr error, opts app.DoctorOptions) app.DoctorReport {
	opts.ConfigLoadError = cfgErr
	opts.ReachabilityProbe = doctorReachabilityProbe
	return app.DoctorWithOptions(ctx, cfg, opts)
}

// runDoctor prints the diagnostic report.
//
// Doctor is the command someone runs when something is already wrong, so it
// never aborts on a bad config: a load failure becomes a check line and the run
// continues on environment values and defaults.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgPath string
	var debugFlags debugFlagOptions
	fs.StringVar(&cfgPath, "config", "", "config file path")
	addDebugFlags(fs, &debugFlags)
	if code, ok := parseCommandFlags(fs, args, "", "", stdout, stderr); !ok {
		return code
	}

	ctx := context.Background()
	environ := os.Environ()
	overrides := config.Overrides{ConfigPath: cfgPath}
	applyDebugFlagOverrides(&overrides, debugFlags)

	cfg, loadErr := config.Load(environ, overrides)
	if loadErr != nil {
		fallback, err := config.LoadEnvOnly(environ, overrides)
		if err != nil {
			fmt.Fprintf(stderr, "load config: %s\n", config.RedactDisplayValue(err.Error()))
			return ExitFailure
		}
		cfg = fallback
	}

	status, credentialErr := applyStoredCredentials(ctx, &cfg)
	if credentialErr != nil {
		status.Err = credentialErr
	}
	logger, closeLog, err := openDebugLogger(cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "open debug log: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	defer func() { _ = closeLog.Close() }()
	logger.Log(ctx, "cli.doctor.start")

	capability := describeCapability(cfg, status)
	targets, targetErr := chatTargets(cfg, "")
	ledger := newQuotaLedger(cfg)

	opts := app.DoctorOptions{
		Environ:       environ,
		CacheDir:      doctorCacheDir(),
		QuotaReporter: ledgerQuotaReporter{ledger: ledger},
		Targets:       targets,
	}
	// The identity lookup is only wired when a user token exists: probing it
	// with an API key would report a credential failure that is really a
	// mode, and doctor's whole job is telling those apart.
	if capability.Mode == credentialModeOAuth {
		if client, err := newYouTubeClient(cfg, staticCredentials(cfg), ledger, logger); err == nil {
			opts.IdentityLookup = client
		}
	}

	// The locally derived checks print first, because they are the ones that
	// are true even when nothing is reachable.
	checks := []app.DoctorCheck{
		credentialFileDoctorCheck(status),
		capabilityDoctorCheck(capability),
		quotaBudgetDoctorCheck(cfg),
		debugLogPlatformDoctorCheck(),
	}
	if targetErr != nil {
		checks = append(checks, app.DoctorCheck{
			Name:   "chats",
			Status: app.DoctorStatusWarn,
			Detail: config.RedactDisplayValue(targetErr.Error()),
		})
	}
	for _, check := range checks {
		fmt.Fprintf(stdout, "[%s] %s: %s\n", check.Status, check.Name, check.Detail)
	}

	report := buildDoctorReport(ctx, cfg, loadErr, opts)
	for _, check := range report.Checks {
		fmt.Fprintf(stdout, "[%s] %s: %s\n", check.Status, check.Name, check.Detail)
	}
	logger.Log(ctx, "cli.doctor.complete", slog.Int("check_count", len(report.Checks)+len(checks)))
	return ExitOK
}

// doctorCacheDir reports the cache directory diagnostics should probe.
func doctorCacheDir() string {
	dir, err := config.DefaultCacheDir()
	if err != nil {
		return ""
	}
	return dir
}

// quotaBudgetDoctorCheck states the arithmetic that governs poll cadence, so
// the number in the status bar has an explanation behind it.
func quotaBudgetDoctorCheck(cfg config.Config) app.DoctorCheck {
	summary := quotaEnvironmentSummary(cfg)
	if summary == "" {
		return app.DoctorCheck{Name: "quota budget", Status: app.DoctorStatusWarn, Detail: "daily quota budget is not configured"}
	}
	return app.DoctorCheck{Name: "quota budget", Status: app.DoctorStatusOK, Detail: summary + " (all unit figures are estimates)"}
}

// debugLogPlatformDoctorCheck states the guarantee this build makes when
// opening the debug log, so the guarantee is reported rather than assumed.
func debugLogPlatformDoctorCheck() app.DoctorCheck {
	return app.DoctorCheck{
		Name:   "debug log hardening",
		Status: app.DoctorStatusOK,
		Detail: debugLogOpenPlatformNote,
	}
}

// runProfile implements `yc profile list|show|set`.
func runProfile(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: yc profile list|show|set <name>")
		return ExitUsage
	}

	switch strings.TrimSpace(args[0]) {
	case "list":
		return runProfileList(args[1:], stdout, stderr)
	case "show":
		return runProfileShow(args[1:], stdout, stderr)
	case "set":
		return runProfileSet(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown profile command %q\n", args[0])
		return ExitUsage
	}
}

// runProfileList prints every palette name, marking the active one.
func runProfileList(args []string, stdout, stderr io.Writer) int {
	cfg, code := loadConfigForFlags("profile list", args, stderr)
	if code != ExitOK {
		return code
	}
	active := strings.ToLower(strings.TrimSpace(cfg.Features.ThemeName))
	for _, name := range append(theme.PresetNames(), "custom") {
		marker := "  "
		if name == active {
			marker = "> "
		}
		fmt.Fprintf(stdout, "%s%s\n", marker, name)
	}
	return ExitOK
}

// paletteRoles is the one list of the nine color roles a theme carries.
//
// `yc profile show` prints it and `yc profile set` registers a flag for each
// entry, both by walking this table. Before, the nine roles were spelled out
// separately in the show table, the flag variables, the flag registrations and
// the assignments back into the config, so adding a role meant four edits that
// had to agree and nothing complained when one was missed.
var paletteRoles = []struct {
	name string
	// usage is the flag's help text on `yc profile set`.
	usage string
	// field points at this role's slot in a palette, for both reading and
	// writing.
	field func(*theme.Palette) *string
}{
	{"background", "custom theme background hex (only used with the 'custom' profile)", func(p *theme.Palette) *string { return &p.Background }},
	{"foreground", "custom theme foreground hex", func(p *theme.Palette) *string { return &p.Foreground }},
	{"accent", "custom theme accent hex", func(p *theme.Palette) *string { return &p.Accent }},
	{"muted", "custom theme muted hex", func(p *theme.Palette) *string { return &p.Muted }},
	{"border", "custom theme border hex", func(p *theme.Palette) *string { return &p.Border }},
	{"surface", "custom theme surface hex", func(p *theme.Palette) *string { return &p.Surface }},
	{"warning", "custom theme warning hex", func(p *theme.Palette) *string { return &p.Warning }},
	{"error", "custom theme error hex", func(p *theme.Palette) *string { return &p.Error }},
	{"success", "custom theme success hex", func(p *theme.Palette) *string { return &p.Success }},
}

// paletteRoleFlagList renders the per-role flags for the `yc profile set` usage
// line, so the help text cannot drift from the flags actually registered.
func paletteRoleFlagList() string {
	flags := make([]string, 0, len(paletteRoles))
	for _, role := range paletteRoles {
		flags = append(flags, "--"+role.name+" '#rrggbb'")
	}
	return strings.Join(flags, " ")
}

// runProfileShow prints the resolved palette's nine roles.
func runProfileShow(args []string, stdout, stderr io.Writer) int {
	cfg, code := loadConfigForFlags("profile show", args, stderr)
	if code != ExitOK {
		return code
	}
	palette := cfg.ResolveTheme()
	fmt.Fprintf(stdout, "theme_name = %s\n", cfg.Features.ThemeName)
	for _, role := range paletteRoles {
		fmt.Fprintf(stdout, "%s = %s\n", role.name, *role.field(&palette))
	}
	return ExitOK
}

// runProfileSet selects a palette and persists it as a non-secret setting.
func runProfileSet(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "usage: yc profile set <name> [%s]\n", paletteRoleFlagList())
		return ExitUsage
	}
	name := strings.ToLower(strings.TrimSpace(args[0]))

	fs := flag.NewFlagSet("profile set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgPath string
	fs.StringVar(&cfgPath, "config", "", "config file path")
	// One flag per role, holding whatever the user passed. An empty value
	// means the flag was not given and the stored color is left alone.
	overrides := make([]string, len(paletteRoles))
	for i, role := range paletteRoles {
		fs.StringVar(&overrides[i], role.name, "", role.usage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return ExitUsage
	}

	if name != "custom" {
		if _, ok := theme.Preset(name); !ok {
			fmt.Fprintf(stderr, "unknown theme %q; run `yc profile list` for available names\n", name)
			return ExitUsage
		}
	}

	cfg, err := config.Load(os.Environ(), config.Overrides{ConfigPath: cfgPath})
	if err != nil {
		fmt.Fprintf(stderr, "load config: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	cfg.Features.ThemeName = name
	if name == "custom" {
		for i, role := range paletteRoles {
			setIfNonEmpty(role.field(&cfg.Features.ThemeCustom), overrides[i])
		}
	}

	if err := config.WriteNonSecretFile(cfg.Path, cfg); err != nil {
		fmt.Fprintf(stderr, "save theme: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	fmt.Fprintf(stdout, "theme set to %s\n", name)
	return ExitOK
}

// loadConfigForFlags parses a bare --config flag set and loads the config,
// which is the shape most read-only subcommands need.
func loadConfigForFlags(name string, args []string, stderr io.Writer) (config.Config, int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgPath string
	fs.StringVar(&cfgPath, "config", "", "config file path")
	// No usage text and no argument noun: these subcommands take flags only
	// and have no help page of their own.
	if code, ok := parseCommandFlags(fs, args, "", "", nil, stderr); !ok {
		return config.Config{}, code
	}
	return loadCommandConfig(config.Overrides{ConfigPath: cfgPath}, stderr)
}

// parseCommandFlags runs the opening moves every subcommand makes: answer
// --help, parse the flags, then reject leftover positional arguments. It
// reports whether the command should carry on; when it should not, the returned
// code is what the command returns.
//
// An empty usage skips the help shortcut, for subcommands with no help page.
// An empty argNoun skips the positional check, for the two that either accept
// positional arguments or word the complaint themselves.
//
// Help goes to stdout because someone asked for it, while errors go to stderr,
// so `yc login --help | less` shows the page and a mistyped flag still reaches
// the terminal.
func parseCommandFlags(fs *flag.FlagSet, args []string, usage, argNoun string, stdout, stderr io.Writer) (int, bool) {
	if usage != "" && hasHelpArg(args) {
		fmt.Fprint(stdout, usage)
		fs.SetOutput(stdout)
		fs.PrintDefaults()
		return ExitOK, false
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage, false
	}
	if argNoun != "" && fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected %s argument %q\n\n", argNoun, fs.Arg(0))
		fs.Usage()
		return ExitUsage, false
	}
	return ExitOK, true
}

// loadCommandConfig loads the config and, on failure, prints the single
// redacted line a subcommand exits on. Redaction matters here because a config
// error can quote the file's contents, and that file holds an API key.
func loadCommandConfig(overrides config.Overrides, stderr io.Writer) (config.Config, int) {
	cfg, err := config.Load(os.Environ(), overrides)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %s\n", config.RedactDisplayValue(err.Error()))
		return config.Config{}, ExitFailure
	}
	return cfg, ExitOK
}

// loginTimeoutDefault bounds how long `yc login` waits for the browser round
// trip before giving the terminal back.
const loginTimeoutDefault = 5 * time.Minute

// chatFlags collects repeated --chat values and comma-separated --chats values
// into one accumulating list, so both spellings can be bound to the same
// variable.
type chatFlags []string

// String renders the accumulated chats for flag defaults.
func (f *chatFlags) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

// Set appends one or more comma-separated chats.
//
// Nothing is stripped from a value: a chat may be named by a video ID, a watch
// URL, an @handle, or a channel ID, and the leading "@" is meaningful.
func (f *chatFlags) Set(value string) error {
	added := false
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		*f = append(*f, part)
		added = true
	}
	if !added {
		return fmt.Errorf("chat cannot be empty")
	}
	return nil
}

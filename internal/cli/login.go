package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/storage"
)

const loginUsage = `Usage:
  yc login [--config path] [--redirect-uri url] [--timeout duration] [--dry-run]
           [--write-default-config] [--read-only]

Starts Google OAuth login for:
  .../auth/youtube.readonly    read live chat, broadcast metadata, your own
                               channel, and your subscriptions
  .../auth/youtube.force-ssl   send chat messages, delete messages, and manage
                               live chat bans

Required for a real login:
  YC_GOOGLE_CLIENT_ID or GOOGLE_CLIENT_ID, from an OAuth client of type
  "Desktop app" in the Google Cloud console.

  A client secret is optional. An installed app cannot keep a secret, so the
  desktop flow authenticates with PKCE alone; set YC_GOOGLE_CLIENT_SECRET only
  if your OAuth client was issued one and refresh would otherwise be rejected.

Behavior:
  yc binds a loopback listener on 127.0.0.1, opens a browser, waits for the
  callback, and saves the tokens through the private credential store. Access
  tokens, refresh tokens, the authorization code, OAuth state, the PKCE
  verifier, and the authorization URL are never printed and never logged.
  Environment variables and flat config credentials still take precedence over
  saved credentials when they are set.

  --dry-run explains exactly what a real login would do and touches no network,
  binds no listener, and opens no browser.

Flags:
`

const logoutUsage = `Usage:
  yc logout [--config path] [--keep-remote]

Deletes the saved credentials and, unless --keep-remote is given, asks Google to
revoke the token so it stops working immediately rather than at its next expiry.

Credentials supplied through environment variables or the config file are not
touched: yc did not write them and will not remove them.

Flags:
`

// defaultLoginTimeout bounds how long `yc login` waits for the browser round
// trip before giving the terminal back - how long the terminal is held hostage
// by a tab the user may have already closed.
const defaultLoginTimeout = 5 * time.Minute

// loginCallbackWaiter is the loopback listener the browser redirects to.
type loginCallbackWaiter interface {
	// RedirectURI is the address actually bound, including the ephemeral
	// port, so the authorization request can name it exactly.
	RedirectURI() string
	Wait(ctx context.Context, expectedState auth.Secret) (auth.LoginCallback, error)
	Close() error
}

// Indirections so the login flow can be driven end to end in tests without a
// browser, a listener, or a network.
var (
	newLoginCallbackWaiter = func(redirectURI string, expectedState auth.Secret) (loginCallbackWaiter, error) {
		return newLoopbackCallbackWaiter(redirectURI, expectedState)
	}
	openLoginBrowser = openBrowser
	newLoginContext  = func(timeout time.Duration) (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), timeout)
	}
)

// newOAuthFlow builds the Google installed-app flow from config.
func newOAuthFlow(cfg config.Config) *auth.GoogleOAuthLoginFlow {
	return auth.NewGoogleOAuthLoginFlow(auth.GoogleOAuthConfig{
		ClientID:     strings.TrimSpace(cfg.Google.ClientID),
		ClientSecret: authSecret(cfg.Google.ClientSecret),
		RedirectURI:  strings.TrimSpace(cfg.Google.RedirectURL),
		Scopes:       auth.LoginScopes(),
		HTTPClient:   auth.NewOAuthHTTPClient(metadataHTTPTimeout),
		Timeout:      metadataHTTPTimeout,
	})
}

// newTokenRefresher returns the refresher a session should use, or nil when no
// refresh is possible at all.
//
// Returning a typed nil pointer as an interface would give the holder a
// non-nil refresher that fails on every call, so the check happens here.
func newTokenRefresher(cfg config.Config) auth.TokenRefresher {
	if strings.TrimSpace(cfg.Google.ClientID) == "" {
		return nil
	}
	return newOAuthFlow(cfg)
}

// newLoginFlow builds the OAuth flow from config.
func newLoginFlow(cfg config.Config) (auth.LoginFlow, error) {
	if strings.TrimSpace(cfg.Google.ClientID) == "" {
		return nil, errors.New("login requires YC_GOOGLE_CLIENT_ID or GOOGLE_CLIENT_ID; create an OAuth client of type \"Desktop app\" in the Google Cloud console")
	}
	return newOAuthFlow(cfg), nil
}

// runLogin performs the Google installed-app OAuth flow and saves the result
// through the credential store.
//
// The authorization URL is opened in a browser and printed only through the
// deliberate reveal path; it must never reach a log line or an error. --dry-run
// exercises everything except the exchange, so the flow can be validated
// without spending a real authorization.
func runLogin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgPath, redirectURI string
	var timeout time.Duration
	var dryRun, writeDefaultConfig, readOnly bool
	var debugFlags debugFlagOptions
	fs.StringVar(&cfgPath, "config", "", "config file path")
	fs.StringVar(&redirectURI, "redirect-uri", "", "loopback OAuth callback URL; empty binds 127.0.0.1 on an ephemeral port")
	fs.DurationVar(&timeout, "timeout", defaultLoginTimeout, "maximum time to wait for browser authorization and the callback")
	fs.BoolVar(&dryRun, "dry-run", false, "explain the login without opening a browser, binding a listener, or contacting Google")
	fs.BoolVar(&writeDefaultConfig, "write-default-config", false, "write a starter config.toml at the effective config path first, only if it does not already exist (skipped during --dry-run)")
	fs.BoolVar(&readOnly, "read-only", false, "request only the readonly scope; chat will be read-only until you log in again")
	addDebugFlags(fs, &debugFlags)
	fs.Usage = func() {
		fmt.Fprint(stderr, loginUsage)
		fs.PrintDefaults()
	}

	if code, ok := parseCommandFlags(fs, args, loginUsage, "login", stdout, stderr); !ok {
		return code
	}
	if timeout <= 0 {
		fmt.Fprintln(stderr, "login timeout must be greater than zero")
		return ExitUsage
	}

	redirectExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "redirect-uri" {
			redirectExplicit = true
		}
	})

	overrides := config.Overrides{ConfigPath: cfgPath}
	applyDebugFlagOverrides(&overrides, debugFlags)
	cfg, code := loadCommandConfig(overrides, stderr)
	if code != ExitOK {
		return code
	}
	if !redirectExplicit {
		redirectURI = strings.TrimSpace(cfg.Google.RedirectURL)
	}

	if writeDefaultConfig && !dryRun {
		wrote, err := writeDefaultConfigIfMissing(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "write default config: %s\n", config.RedactDisplayValue(err.Error()))
			return ExitFailure
		}
		if wrote {
			fmt.Fprintf(stdout, "Default config written to %s.\n", config.RedactDisplayValue(cfg.Path))
		} else {
			fmt.Fprintf(stdout, "Config already exists at %s; left unchanged.\n", config.RedactDisplayValue(cfg.Path))
		}
	}

	logger, closeLog, err := openDebugLogger(cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "open debug log: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	defer func() { _ = closeLog.Close() }()

	scopes := auth.LoginScopes()
	if readOnly {
		scopes = auth.ReadScopes()
	}

	ctx := context.Background()
	logger.Log(ctx, "cli.login.start",
		slog.Bool("dry_run", dryRun),
		slog.Int("scope_count", len(scopes)),
		slog.Int64("timeout_ms", int64(timeout/time.Millisecond)),
	)

	if dryRun {
		printLoginDryRun(stdout, cfg, redirectURI, scopes, timeout)
		logger.Log(ctx, "cli.login.complete", slog.Bool("dry_run", true))
		return ExitOK
	}
	return performLogin(cfg, redirectURI, scopes, timeout, logger, stdout, stderr)
}

// failLogin ends a login attempt: it records the step that failed in the debug
// log, prints one redacted line naming the step, and returns the exit code
// performLogin should hand back. Passing an empty event skips the log entry,
// for the few steps that have nothing worth recording.
//
// Every failure in performLogin goes through here so they all report the same
// way. They used to be written out one by one, and had already drifted: some
// printed a bare redacted error while others went through printLoginError,
// which is the one that turns a cancellation or a timeout into plain words
// instead of a Go error string.
func failLogin(
	ctx context.Context,
	logger debuglog.Logger,
	stderr io.Writer,
	event, action string,
	err error,
	redactor auth.Redactor,
	code int,
) int {
	if event != "" {
		logger.Log(ctx, event, debuglog.Err("error", err))
	}
	printLoginError(stderr, action, err, redactor)
	return code
}

// performLogin runs the interactive half of `yc login`.
//
// The order matters: the listener is bound before the authorization request is
// built, because a loopback redirect on an ephemeral port cannot be named until
// the port is known, and an authorization URL naming a port yc failed to bind
// would send the user to a page whose callback goes nowhere.
func performLogin(cfg config.Config, redirectURI string, scopes []auth.Scope, timeout time.Duration, logger debuglog.Logger, stdout, stderr io.Writer) int {
	baseRedactor := configRedactor(cfg)

	flow, err := newLoginFlow(cfg)
	if err != nil {
		logger.Log(context.Background(), "cli.login.config_invalid")
		fmt.Fprintln(stderr, err)
		return ExitUsage
	}

	store, err := newCredentialStore()
	if err != nil || store == nil {
		if err == nil {
			err = errors.New("credential store unavailable")
		}
		return failLogin(context.Background(), logger, stderr, "cli.login.storage_failed", "prepare credential storage", err, baseRedactor, ExitFailure)
	}

	// State and the PKCE verifier are generated here and carried through the
	// callback, so the flow never has to hold cross-request state and a test
	// double sees exactly the same values a real exchange would.
	//
	// They are generated before the listener binds because the listener is
	// given the expected state up front: it compares it in constant time and
	// refuses to deliver a mismatched callback at all, so a forged redirect
	// cannot end the wait.
	state, err := auth.NewState()
	if err != nil {
		return failLogin(context.Background(), logger, stderr, "", "start login", err, baseRedactor, ExitFailure)
	}
	verifier, err := auth.NewCodeVerifier()
	if err != nil {
		return failLogin(context.Background(), logger, stderr, "", "start login", err, baseRedactor, ExitFailure)
	}

	waiter, err := newLoginCallbackWaiter(redirectURI, state)
	if err != nil {
		return failLogin(context.Background(), logger, stderr, "cli.login.callback_unavailable", "login callback unavailable", err, baseRedactor, ExitUsage)
	}
	defer func() { _ = waiter.Close() }()

	ctx, cancel := newLoginContext(timeout)
	defer cancel()

	request := auth.LoginRequest{
		ClientID:      strings.TrimSpace(cfg.Google.ClientID),
		ClientSecret:  authSecret(cfg.Google.ClientSecret),
		RedirectURI:   waiter.RedirectURI(),
		Scopes:        scopes,
		State:         state,
		CodeVerifier:  verifier,
		CodeChallenge: auth.CodeChallenge(verifier),
		// Google issues a refresh token by default for an installed app, but
		// only on the first grant; prompt=consent is how a later login gets
		// one re-issued rather than silently coming back without one.
		ForceConsent: true,
	}
	logger = logger.WithSecrets(state, verifier)

	challenge, err := flow.BeginLogin(ctx, request)
	if err != nil {
		return failLogin(ctx, logger, stderr, "cli.login.begin_failed", "start login", err, request.Redactor(), ExitFailure)
	}
	logger = logger.WithSecrets(challenge.AuthorizationURL, challenge.State)
	logger.Log(ctx, "cli.login.begin_succeeded", slog.Int("scope_count", len(challenge.Scopes)))

	fmt.Fprintf(stdout, "Starting Google OAuth login for scopes: %s\n", strings.Join(auth.ScopeValues(challenge.Scopes), ", "))
	fmt.Fprintln(stdout, "A browser window will open. Approve the requested access, then return to this terminal.")
	fmt.Fprintln(stdout, "Tokens are saved privately and are never printed.")

	if err := openLoginBrowser(ctx, challenge.AuthorizationURL.Reveal()); err != nil {
		return failLogin(ctx, logger, stderr, "cli.login.browser_failed", "open browser", err, challenge.Redactor(), ExitFailure)
	}
	logger.Log(ctx, "cli.login.browser_opened")

	callback, err := waiter.Wait(ctx, challenge.State)
	if err != nil {
		return failLogin(ctx, logger, stderr, "cli.login.callback_failed", "wait for the OAuth callback", err, challenge.Redactor(), ExitFailure)
	}
	callback.CodeVerifier = verifier
	callback.RedirectURI = request.RedirectURI
	logger = logger.WithSecrets(callback.Code, callback.State)
	logger.Log(ctx, "cli.login.callback_received", slog.Bool("has_code", callback.Code.Present()))

	if callback.Denied() {
		fmt.Fprintf(stderr, "login denied by the authorization server: %s\n", config.RedactDisplayValue(callback.Error))
		return ExitFailure
	}

	result, err := flow.CompleteLogin(ctx, callback)
	if err != nil {
		return failLogin(ctx, logger, stderr, "cli.login.complete_failed", "complete login", err, callback.Redactor(), ExitFailure)
	}
	logger = logger.WithSecrets(result.Tokens.AccessToken, result.Tokens.RefreshToken)
	logger.Log(ctx, "cli.login.complete_succeeded",
		slog.Int("scope_count", len(result.Scopes)),
		slog.Bool("refresh_available", result.Tokens.RefreshAvailable()),
	)

	if err := persistLogin(ctx, cfg, result); err != nil {
		return failLogin(ctx, logger, stderr, "cli.login.save_failed", "save credentials", err, result.Redactor(), ExitFailure)
	}
	logger.Log(ctx, "cli.login.save_succeeded")
	printLoginSuccess(stdout, result)
	return ExitOK
}

// persistLogin writes a completed login through the credential store and
// reports, with redaction, when persistence is unavailable rather than failing
// the login outright.
func persistLogin(ctx context.Context, cfg config.Config, result auth.LoginResult) error {
	store, err := newCredentialStore()
	if err != nil {
		if errors.Is(err, storage.ErrCredentialsUnsupported) {
			return fmt.Errorf("%w; set YC_GOOGLE_ACCESS_TOKEN and YC_GOOGLE_REFRESH_TOKEN in the environment instead", err)
		}
		return err
	}
	if store == nil {
		return errors.New("credential store unavailable")
	}

	record := storage.CredentialRecordFromLoginResult(result, strings.TrimSpace(cfg.Google.ClientID), time.Now().UTC())
	// The API key is a separate credential from the OAuth grant and a login
	// must not discard one the user configured for read-only mode.
	if key := strings.TrimSpace(cfg.YouTube.APIKey); key != "" {
		record.APIKey = auth.NewSecret(key)
	}
	return store.SaveCredentials(ctx, record)
}

// runLogout deletes the stored credentials and revokes the token when possible.
func runLogout(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgPath string
	var keepRemote bool
	fs.StringVar(&cfgPath, "config", "", "config file path")
	fs.BoolVar(&keepRemote, "keep-remote", false, "delete the local credentials without asking Google to revoke the token")
	fs.Usage = func() {
		fmt.Fprint(stderr, logoutUsage)
		fs.PrintDefaults()
	}
	// No argument noun: logout takes no positional arguments and never
	// checked for them, and starting to now would turn a stray word into a
	// failure for someone trying to log out.
	if code, ok := parseCommandFlags(fs, args, logoutUsage, "", stdout, stderr); !ok {
		return code
	}

	ctx := context.Background()
	cfg, code := loadCommandConfig(config.Overrides{ConfigPath: cfgPath}, stderr)
	if code != ExitOK {
		return code
	}

	store, err := newCredentialStore()
	if err != nil {
		if errors.Is(err, storage.ErrCredentialsUnsupported) {
			fmt.Fprintln(stdout, "Saved credentials are unsupported on this platform; nothing to remove.")
			return ExitOK
		}
		fmt.Fprintf(stderr, "open credential store: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	if store == nil {
		fmt.Fprintln(stdout, "No credential store is available; nothing to remove.")
		return ExitOK
	}

	record, present, err := store.LoadCredentials(ctx)
	if err != nil {
		// A malformed or unreadable file is exactly what logout should be
		// able to clear, so the delete still runs.
		fmt.Fprintf(stderr, "read saved credentials: %s\n", config.RedactDisplayValue(err.Error()))
	}

	// Revoking first means a failure to reach Google leaves the credentials
	// on disk, where the user can retry, rather than deleting them locally
	// and leaving a live token in Google's hands with no way to name it.
	if present && !keepRemote && record.RefreshToken.Present() {
		if err := revokeToken(ctx, cfg, record.RefreshToken); err != nil {
			fmt.Fprintf(stderr, "warning: could not revoke the token with Google (%s); the local credentials will still be removed\n",
				safeStartupError(record.Redactor(), err))
		} else {
			fmt.Fprintln(stdout, "Token revoked with Google.")
		}
	}

	if err := store.DeleteCredentials(ctx); err != nil {
		fmt.Fprintf(stderr, "delete saved credentials: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	fmt.Fprintln(stdout, "Saved credentials removed.")
	if strings.TrimSpace(cfg.Google.AccessToken) != "" || strings.TrimSpace(cfg.YouTube.APIKey) != "" {
		fmt.Fprintln(stdout, "Note: credentials are still set through the environment or the config file; yc did not write those and has not removed them.")
	}
	return ExitOK
}

// tokenRevoker is the revoke capability `yc logout` needs from a login flow.
//
// The assertion below is what enforces it. Asking for the method at runtime
// instead would turn a removed or renamed Revoke into a logout that quietly
// reports "revocation is unavailable" and leaves a live token on Google's side,
// where the build should have failed instead.
type tokenRevoker interface {
	Revoke(ctx context.Context, token auth.Secret) error
}

var _ tokenRevoker = (*auth.GoogleOAuthLoginFlow)(nil)

// revokeToken asks Google to invalidate a token immediately.
func revokeToken(ctx context.Context, cfg config.Config, token auth.Secret) error {
	return newOAuthFlow(cfg).Revoke(ctx, token)
}

// writeDefaultConfigIfMissing writes the effective non-secret config to
// cfg.Path, but only when no file exists there yet, so a user can inspect and
// edit a starter config.toml before authenticating. It never overwrites.
func writeDefaultConfigIfMissing(cfg config.Config) (wrote bool, err error) {
	if strings.TrimSpace(cfg.Path) == "" {
		return false, errors.New("config path is empty")
	}
	switch _, statErr := os.Stat(cfg.Path); {
	case statErr == nil:
		return false, nil
	case !errors.Is(statErr, os.ErrNotExist):
		return false, statErr
	}
	if err := config.WriteNonSecretFile(cfg.Path, cfg); err != nil {
		return false, err
	}
	return true, nil
}

// printLoginDryRun explains a real login without performing any part of it.
func printLoginDryRun(stdout io.Writer, cfg config.Config, redirectURI string, scopes []auth.Scope, timeout time.Duration) {
	fmt.Fprintln(stdout, "Google OAuth login dry run (no network, no listener, no browser)")
	fmt.Fprintf(stdout, "Requested scopes: %s\n", strings.Join(auth.ScopeValues(scopes), ", "))
	if strings.TrimSpace(redirectURI) == "" {
		fmt.Fprintln(stdout, "Redirect URI: http://127.0.0.1:<ephemeral>/ (bound at login time)")
	} else {
		fmt.Fprintf(stdout, "Redirect URI: %s\n", config.RedactDisplayValue(redirectURI))
	}
	fmt.Fprintf(stdout, "Timeout: %s\n", timeout)
	fmt.Fprintf(stdout, "Client ID: %s\n", presentMissing(cfg.Google.ClientID))
	fmt.Fprintf(stdout, "Client secret: %s (optional for a desktop app)\n", presentMissing(cfg.Google.ClientSecret))
	fmt.Fprintf(stdout, "Config file: %s\n", config.RedactDisplayValue(cfg.Path))
	fmt.Fprintln(stdout, "A real login binds a loopback listener, opens your browser, exchanges the code with PKCE,")
	fmt.Fprintln(stdout, "and saves the tokens through the private credential store. Nothing sensitive is printed.")
	fmt.Fprintln(stdout, "Environment variables and flat config credentials still take precedence over saved credentials.")
}

// presentMissing reports whether a credential is configured without revealing
// anything about it.
func presentMissing(value string) string {
	if strings.TrimSpace(value) == "" {
		return "missing"
	}
	return "present"
}

// printLoginSuccess reports the outcome in terms of identity and capability,
// never in terms of token material.
func printLoginSuccess(stdout io.Writer, result auth.LoginResult) {
	name := strings.TrimSpace(result.Identity.DisplayName)
	if name == "" {
		name = strings.TrimSpace(result.Identity.Handle)
	}
	if name == "" {
		name = strings.TrimSpace(result.Identity.ChannelID)
	}
	if name == "" {
		name = "unknown channel"
	}
	fmt.Fprintf(stdout, "Login succeeded for: %s\n", config.RedactDisplayValue(name))
	fmt.Fprintf(stdout, "Granted scopes: %s\n", strings.Join(auth.ScopeValues(result.Scopes), ", "))
	if result.Tokens.RefreshAvailable() {
		fmt.Fprintln(stdout, "Refresh token: received; yc can renew this session on its own.")
	} else {
		fmt.Fprintln(stdout, "Refresh token: not returned; chat will stop when this token expires. Re-run `yc login` if that happens.")
	}
	if missing := result.MissingRequiredScopes(); len(missing) > 0 {
		fmt.Fprintf(stdout, "Warning: missing %s; some features will be unavailable.\n", strings.Join(auth.ScopeValues(missing), ", "))
	}
	fmt.Fprintln(stdout, "Credentials saved to the private credential store.")
}

// printLoginError reports a login failure, naming a cancellation or a timeout
// as itself rather than as an opaque transport error.
func printLoginError(stderr io.Writer, action string, err error, redactor auth.Redactor) {
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintf(stderr, "%s: login canceled\n", action)
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintf(stderr, "%s: login timed out waiting for the browser\n", action)
	default:
		fmt.Fprintf(stderr, "%s: %s\n", action, config.RedactDisplayValue(redactor.Redact(err.Error())))
	}
}

// loopbackCallbackWaiter adapts auth.LoopbackServer to the CLI's waiter
// interface.
//
// The listener itself deliberately lives in internal/auth and not here. There
// used to be a second, separately written implementation in this file, which
// meant one security boundary - the process that receives Google's redirect -
// had two implementations that had already drifted apart on request filtering
// and response headers. One audited listener, used everywhere, is the point.
type loopbackCallbackWaiter struct {
	server *auth.LoopbackServer
}

// newLoopbackCallbackWaiter binds the loopback listener for one login attempt.
//
// The expected state is supplied at bind time because the listener compares it
// in constant time and refuses to deliver a callback that does not match, so
// the check happens before an attacker-supplied callback can end the wait.
//
// An empty redirect URI binds 127.0.0.1 on an ephemeral port, which is what
// Google recommends for an installed app: no port has to be reserved in the
// OAuth client, and a port already taken by something else cannot break login.
func newLoopbackCallbackWaiter(rawRedirectURI string, expectedState auth.Secret) (*loopbackCallbackWaiter, error) {
	address := ""
	path := ""
	if trimmedURI := strings.TrimSpace(rawRedirectURI); trimmedURI != "" {
		parsed, err := validateLoopbackRedirectURI(trimmedURI)
		if err != nil {
			return nil, err
		}
		port := parsed.Port()
		if port == "" {
			port = "0"
		}
		address = net.JoinHostPort(parsed.Hostname(), port)
		path = parsed.EscapedPath()
	}

	server, err := auth.NewLoopbackServer(auth.LoopbackConfig{
		Address:       address,
		Path:          path,
		ExpectedState: expectedState,
		Timeout:       auth.DefaultCallbackTimeout,
		SuccessMessage: "yc received the Google login callback. " +
			"You can close this tab and return to the terminal.",
	})
	if err != nil {
		return nil, err
	}
	return &loopbackCallbackWaiter{server: server}, nil
}

// RedirectURI returns the address actually bound.
func (w *loopbackCallbackWaiter) RedirectURI() string {
	return w.server.RedirectURI()
}

// Wait blocks for the callback, the listener timing out, or the context
// expiring. The listener has already rejected any callback whose state does not
// match; stamping the expectation here is what lets CompleteLogin re-check it.
func (w *loopbackCallbackWaiter) Wait(ctx context.Context, expectedState auth.Secret) (auth.LoginCallback, error) {
	callback, err := w.server.Wait(ctx)
	if err != nil {
		return auth.LoginCallback{}, err
	}
	callback.ExpectedState = expectedState
	return callback, nil
}

// Close shuts the listener down. It is safe to call more than once.
func (w *loopbackCallbackWaiter) Close() error {
	return w.server.Close()
}

// validateLoopbackRedirectURI rejects anything that is not a loopback HTTP
// callback.
//
// The loopback-redirect deprecation Google announced applies to mobile apps
// only; it remains the recommended desktop mechanism, and both 127.0.0.1 and
// [::1] are permitted.
func validateLoopbackRedirectURI(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("redirect URI could not be parsed")
	}
	if parsed.Scheme != "http" {
		return nil, errors.New("redirect URI must use http with 127.0.0.1, localhost, or ::1")
	}
	if parsed.User != nil {
		return nil, errors.New("redirect URI must not include user info")
	}
	switch strings.ToLower(strings.Trim(parsed.Hostname(), "[]")) {
	case "127.0.0.1", "localhost", "::1":
	default:
		return nil, errors.New("redirect URI host must be 127.0.0.1, localhost, or ::1")
	}
	return parsed, nil
}

// openBrowser launches the user's browser at the authorization URL.
//
// The URL is passed as a separate argument rather than through a shell, so a
// crafted URL cannot become a command, and the process is not waited on
// synchronously because some launchers block for the lifetime of the browser.
func openBrowser(ctx context.Context, targetURL string) error {
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return errors.New("authorization URL is empty")
	}

	var candidates [][]string
	if browser := strings.TrimSpace(os.Getenv("BROWSER")); browser != "" {
		if parts := strings.Fields(browser); len(parts) > 0 {
			candidates = append(candidates, append(parts, targetURL))
		}
	}
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates, []string{"open", targetURL})
	case "windows":
		candidates = append(candidates, []string{"rundll32", "url.dll,FileProtocolHandler", targetURL})
	default:
		candidates = append(candidates, []string{"xdg-open", targetURL})
	}

	var attempted []string
	for _, candidate := range candidates {
		if len(candidate) == 0 {
			continue
		}
		path, err := exec.LookPath(candidate[0])
		if err != nil {
			attempted = append(attempted, candidate[0])
			continue
		}
		cmd := exec.CommandContext(ctx, path, candidate[1:]...) //nolint:gosec // candidates are a fixed table of local browser openers, resolved via LookPath
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	if len(attempted) == 0 {
		return errors.New("automatic browser opening is not supported in this environment")
	}
	return fmt.Errorf("automatic browser opening is not supported in this environment; tried %s", strings.Join(attempted, ", "))
}

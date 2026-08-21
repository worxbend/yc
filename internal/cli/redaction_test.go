package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/storage"
)

// Distinctive per-kind markers. Each is unmistakable in a diff and none of them
// looks like anything yc would legitimately print, so a hit is always a leak
// and never a coincidence.
const (
	leakClientSecret  = "LEAK-client-secret-aaaaaaaaaaaaaaaaaaaa"
	leakAccessToken   = "LEAK-access-token-bbbbbbbbbbbbbbbbbbbb"
	leakRefreshToken  = "LEAK-refresh-token-cccccccccccccccccccc"
	leakAPIKey        = "AIzaLEAKdddddddddddddddddddddddddddddddddd"
	leakAuthCode      = "LEAK-auth-code-eeeeeeeeeeeeeeeeeeeeeeee"
	leakOAuthState    = "LEAK-oauth-state-ffffffffffffffffffffff"
	leakCodeVerifier  = "LEAK-pkce-verifier-gggggggggggggggggggg"
	leakAuthURL       = "https://accounts.google.com/o/oauth2/v2/auth?client_id=LEAK&state=" + leakOAuthState
	leakChannelSecret = "LEAK-bearer-header-hhhhhhhhhhhhhhhhhhhh"
)

// everyCredentialMarker is the full set nothing may ever print.
func everyCredentialMarker() []string {
	return []string{
		leakClientSecret, leakAccessToken, leakRefreshToken, leakAPIKey,
		leakAuthCode, leakOAuthState, leakCodeVerifier, leakAuthURL, leakChannelSecret,
	}
}

// assertNoLeak fails naming the exact credential kind that escaped, because
// "output contained a secret" is not actionable and "the refresh token reached
// stderr" is.
func assertNoLeak(t *testing.T, where, output string) {
	t.Helper()
	for _, marker := range everyCredentialMarker() {
		if strings.Contains(output, marker) {
			t.Errorf("%s leaked %s:\n%s", where, credentialKind(marker), output)
		}
	}
}

func credentialKind(marker string) string {
	switch marker {
	case leakClientSecret:
		return "the client secret"
	case leakAccessToken:
		return "the access token"
	case leakRefreshToken:
		return "the refresh token"
	case leakAPIKey:
		return "the API key"
	case leakAuthCode:
		return "the authorization code"
	case leakOAuthState:
		return "the OAuth state"
	case leakCodeVerifier:
		return "the PKCE verifier"
	case leakAuthURL:
		return "the authorization URL"
	default:
		return "a bearer header"
	}
}

// seedEveryCredential exports every credential yc reads, saves a record holding
// the same values, and points the cache and config at scratch directories.
func seedEveryCredential(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	t.Setenv("YC_GOOGLE_CLIENT_ID", "client-id.apps.googleusercontent.com")
	t.Setenv("YC_GOOGLE_CLIENT_SECRET", leakClientSecret)
	t.Setenv("YC_GOOGLE_ACCESS_TOKEN", leakAccessToken)
	t.Setenv("YC_GOOGLE_REFRESH_TOKEN", leakRefreshToken)
	t.Setenv("YC_YOUTUBE_API_KEY", leakAPIKey)

	withMemoryCredentialStore(t, storage.CredentialRecord{
		ClientID:     "client-id.apps.googleusercontent.com",
		AccessToken:  auth.NewSecret(leakAccessToken),
		RefreshToken: auth.NewSecret(leakRefreshToken),
		APIKey:       auth.NewSecret(leakAPIKey),
		Scopes:       auth.LoginScopes(),
		ExpiresAt:    time.Now().Add(time.Hour),
	})

	// A config file carrying the same secrets, so the file path is exercised
	// alongside the environment one.
	return writeTempConfig(t, strings.Join([]string{
		`google_client_id = "client-id.apps.googleusercontent.com"`,
		`google_client_secret = "` + leakClientSecret + `"`,
		`google_access_token = "` + leakAccessToken + `"`,
		`google_refresh_token = "` + leakRefreshToken + `"`,
		`youtube_api_key = "` + leakAPIKey + `"`,
	}, "\n")+"\n")
}

// TestNoCommandCanPrintACredential runs every read-only command with every
// credential populated and asserts that none of them reaches stdout, stderr, or
// the debug log.
//
// This is the guarantee yc makes at its loudest: the terminal it runs in is
// frequently on stream, so a credential printed once is a credential published.
func TestNoCommandCanPrintACredential(t *testing.T) {
	commands := [][]string{
		{"--help"},
		{"config", "show"},
		{"config", "path"},
		{"doctor"},
		{"quota"},
		{"profile", "list"},
		{"profile", "show"},
		{"login", "--dry-run"},
		{"login", "--help"},
		{"logout", "--help"},
		{"setup", "--help"},
		{"chat", "--help"},
	}

	for _, args := range commands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cfgPath := seedEveryCredential(t)
			debugPath := filepath.Join(t.TempDir(), "debug.log")

			// Everything that accepts --config and --debug-log gets them, so
			// the redaction covers the logging path too.
			full := append([]string(nil), args...)
			if args[0] != "--help" && (args[0] != "config" || args[1] != "path") {
				full = append(full, "--config", cfgPath)
			}
			switch args[0] {
			case "doctor", "login", "chat":
				full = append(full, "--debug-log", "--debug-log-path", debugPath)
			}

			var stdout, stderr bytes.Buffer
			Run(full, &stdout, &stderr)

			assertNoLeak(t, "stdout of `yc "+strings.Join(full, " ")+"`", stdout.String())
			assertNoLeak(t, "stderr of `yc "+strings.Join(full, " ")+"`", stderr.String())
			if contents, err := os.ReadFile(debugPath); err == nil {
				assertNoLeak(t, "the debug log of `yc "+strings.Join(full, " ")+"`", string(contents))
			}
		})
	}
}

// A struct printed with %v or %+v is the accident this guards against: a
// developer adding one debug print to a startup path would otherwise dump every
// token in the process.
//
// The check walks each value with reflection and asserts that every field
// holding a secret is a type that refuses to format itself, rather than trusting
// that no caller will ever print the struct.
func TestNoExportedStartupTypeFormatsACredential(t *testing.T) {
	cfg := config.Default()
	cfg.Google.ClientID = "client-id.apps.googleusercontent.com"
	cfg.Google.ClientSecret = leakClientSecret
	cfg.Google.AccessToken = leakAccessToken
	cfg.Google.RefreshToken = leakRefreshToken
	cfg.YouTube.APIKey = leakAPIKey

	record := storage.CredentialRecord{
		ClientID:     "client-id.apps.googleusercontent.com",
		AccessToken:  auth.NewSecret(leakAccessToken),
		RefreshToken: auth.NewSecret(leakRefreshToken),
		APIKey:       auth.NewSecret(leakAPIKey),
		Scopes:       auth.LoginScopes(),
	}
	tokens := auth.TokenSet{
		AccessToken:  auth.NewSecret(leakAccessToken),
		RefreshToken: auth.NewSecret(leakRefreshToken),
		TokenType:    "Bearer",
	}
	callback := auth.LoginCallback{
		Code:         auth.NewSecret(leakAuthCode),
		State:        auth.NewSecret(leakOAuthState),
		CodeVerifier: auth.NewSecret(leakCodeVerifier),
	}
	challenge := auth.LoginChallenge{
		AuthorizationURL: auth.NewSecret(leakAuthURL),
		State:            auth.NewSecret(leakOAuthState),
		Scopes:           auth.LoginScopes(),
	}
	result := auth.LoginResult{Tokens: tokens, Scopes: auth.LoginScopes()}

	holder := newCredentialHolder(record, nil, nil)
	status := credentialLoadStatus{Record: record, Present: true, Path: "/tmp/credentials.json"}
	capability := describeCapability(cfg, status)
	validation := tokenValidation{Reachable: true, Valid: true, Scopes: auth.LoginScopes()}

	values := map[string]any{
		"auth.Secret":                auth.NewSecret(leakAccessToken),
		"auth.TokenSet":              tokens,
		"auth.LoginCallback":         callback,
		"auth.LoginChallenge":        challenge,
		"auth.LoginResult":           result,
		"storage.CredentialRecord":   record,
		"cli.credentialHolder":       holder,
		"cli.credentialLoadStatus":   status,
		"cli.credentialCapability":   capability,
		"cli.tokenValidation":        validation,
		"cli.chatFlagOptions":        chatFlagOptions{cfgPath: "/tmp/config.toml"},
		"cli.setupFlagOptions":       setupFlagOptions{cfgPath: "/tmp/config.toml"},
		"cli.debugFlagOptions":       debugFlagOptions{path: "/tmp/debug.log"},
		"config.Config.Redacted":     cfg.RedactedString(),
		"auth.Redactor":              configRedactor(cfg),
		"cli.ledgerQuotaReporter":    ledgerQuotaReporter{},
		"storage.CredentialRecord.R": record.Redactor(),
	}

	// Every verb a careless caller might reach for.
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q"}
	for name, value := range values {
		for _, verb := range verbs {
			rendered := fmt.Sprintf(verb, value)
			for _, marker := range everyCredentialMarker() {
				if strings.Contains(rendered, marker) {
					t.Errorf("fmt.Sprintf(%q, %s) leaked %s:\n%s", verb, name, credentialKind(marker), rendered)
				}
			}
		}
	}
}

// The reflective half: any field on these types whose name says it holds a
// credential must be a type that cannot print itself. A plain string field
// named AccessToken is the bug this catches before anyone formats it.
func TestCredentialBearingFieldsUseANonPrintingType(t *testing.T) {
	secretish := []string{"secret", "token", "apikey", "key", "verifier", "state", "code", "authorizationurl", "password"}
	// Fields that name a credential but hold no material: a scope list, a
	// boolean, a timestamp, an ID, or the config's own plain-string fields,
	// which are protected by RedactedString and WriteNonSecretFile instead.
	exempt := map[string]bool{
		"auth.TokenSet.TokenType":              true,
		"auth.LoginCallback.Error":             true,
		"auth.LoginCallback.ErrorDescription":  true,
		"auth.LoginCallback.RedirectURI":       true,
		"auth.LoginCallback.StateMismatch":     true,
		"auth.LoginChallenge.CodeChallengeAlg": true,
		"storage.CredentialRecord.TokenType":   true,
		"storage.CredentialRecord.ClientID":    true,
		"storage.CredentialRecord.ChannelID":   true,
	}

	types := map[string]reflect.Type{
		"auth.TokenSet":            reflect.TypeOf(auth.TokenSet{}),
		"auth.LoginCallback":       reflect.TypeOf(auth.LoginCallback{}),
		"auth.LoginChallenge":      reflect.TypeOf(auth.LoginChallenge{}),
		"storage.CredentialRecord": reflect.TypeOf(storage.CredentialRecord{}),
	}
	secretType := reflect.TypeOf(auth.Secret(""))

	for typeName, typ := range types {
		for i := range typ.NumField() {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue // unexported: unreachable by a caller's fmt verb
			}
			qualified := typeName + "." + field.Name
			if exempt[qualified] {
				continue
			}
			lowered := strings.ToLower(field.Name)
			credentialish := false
			for _, marker := range secretish {
				if strings.Contains(lowered, marker) {
					credentialish = true
					break
				}
			}
			if !credentialish {
				continue
			}
			if field.Type != secretType {
				t.Errorf("%s is a %s; a credential-bearing field must be auth.Secret so fmt cannot print it",
					qualified, field.Type)
			}
		}
	}
}

// Startup errors carry two layers of defense: the config-level display
// redaction and every secret the process actually holds.
func TestSafeStartupErrorRemovesEveryConfiguredCredential(t *testing.T) {
	cfg := config.Default()
	cfg.Google.ClientSecret = leakClientSecret
	cfg.Google.AccessToken = leakAccessToken
	cfg.Google.RefreshToken = leakRefreshToken
	cfg.YouTube.APIKey = leakAPIKey
	redactor := configRedactor(cfg)

	message := fmt.Sprintf("request failed: token=%s refresh=%s secret=%s key=%s",
		leakAccessToken, leakRefreshToken, leakClientSecret, leakAPIKey)
	assertNoLeak(t, "safeStartupError", safeStartupError(redactor, errNamed(message)))

	if got := safeStartupError(redactor, nil); got != "" {
		t.Errorf("safeStartupError(nil) = %q, want empty", got)
	}
}

// `yc config show` is the command whose entire job is printing the config, so
// it is the one place a secret is most likely to escape.
func TestConfigShowPrintsPlaceholdersForEverySecret(t *testing.T) {
	cfgPath := seedEveryCredential(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"config", "show", "--config", cfgPath}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("config show = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	assertNoLeak(t, "config show", out)

	// The keys must still be listed, with a placeholder, so the user can see
	// that a value is configured without seeing the value.
	for _, key := range []string{
		"google_client_secret", "google_access_token", "google_refresh_token", "youtube_api_key",
	} {
		if !strings.Contains(out, key) {
			t.Errorf("config show omitted the key %q entirely:\n%s", key, out)
		}
	}
	if strings.Count(out, config.Redacted) < 4 {
		t.Errorf("config show printed %d placeholders, want one per secret:\n%s", strings.Count(out, config.Redacted), out)
	}
	// A non-secret must still be readable, or the command is useless.
	if !strings.Contains(out, "client-id.apps.googleusercontent.com") {
		t.Errorf("config show redacted the client ID, which is not a secret:\n%s", out)
	}
}

// The credential-precedence hint is printed after a failure, when the user is
// about to retry, and must name the problem without quoting any of it.
func TestCredentialPrecedenceHintExplainsShadowingWithoutQuotingAToken(t *testing.T) {
	if got := credentialPrecedenceHint(credentialLoadStatus{}); got != "" {
		t.Errorf("hint = %q, want nothing when no credential is shadowed", got)
	}

	hint := credentialPrecedenceHint(credentialLoadStatus{
		TokenShadowed: true,
		Record:        storage.CredentialRecord{AccessToken: auth.NewSecret(leakAccessToken)},
	})
	assertNoLeak(t, "the precedence hint", hint)
	for _, want := range []string{"YC_GOOGLE_ACCESS_TOKEN", "config.toml", "takes precedence"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint is missing %q:\n%s", want, hint)
		}
	}
}

// Doctor prints a line per credential source, which makes it the diagnostic
// most likely to quote one.
func TestCredentialDoctorChecksDescribeWithoutRevealing(t *testing.T) {
	record := storage.CredentialRecord{
		AccessToken:  auth.NewSecret(leakAccessToken),
		RefreshToken: auth.NewSecret(leakRefreshToken),
		APIKey:       auth.NewSecret(leakAPIKey),
		Scopes:       auth.LoginScopes(),
	}
	// runDoctor sets status.Err on a status that may already carry a record, so
	// the "loaded, then failed" shape is the reachable one and the record's own
	// secrets are what the check can redact by value. A bare opaque token in an
	// error from a store that loaded nothing is not redactable by anyone here;
	// that guarantee belongs to internal/storage and is asserted there.
	statuses := map[string]credentialLoadStatus{
		"absent":   {},
		"present":  {Present: true, Record: record, Path: "/home/u/.config/yc/credentials.json", Label: "credential file"},
		"shadowed": {Present: true, TokenShadowed: true, Record: record, Label: "credential file"},
		"failed with a loaded record": {
			Present: true, Record: record, Label: "credential file",
			Err: errNamed("refresh rejected for " + leakAccessToken),
		},
		"failed with a key-shaped value": {
			Label: "credential file",
			Err:   errNamed("decode failed near " + leakAPIKey),
		},
		"unsupported": {Err: storage.ErrCredentialsUnsupported, Label: "credential file"},
	}
	for name, status := range statuses {
		check := credentialFileDoctorCheck(status)
		assertNoLeak(t, "the "+name+" credential doctor check", check.Name+" "+check.Detail)
		if strings.TrimSpace(check.Detail) == "" {
			t.Errorf("the %s check has no detail", name)
		}
	}

	cfg := config.Default()
	cfg.Google.AccessToken = leakAccessToken
	cfg.YouTube.APIKey = leakAPIKey
	for name, status := range statuses {
		check := capabilityDoctorCheck(describeCapability(cfg, status))
		assertNoLeak(t, "the "+name+" capability doctor check", check.Name+" "+check.Detail)
	}
}

// applyStoredCredentials fills only what is still empty, so a token exported for
// one run wins without the user deleting their saved login.
func TestApplyStoredCredentialsFillsOnlyEmptyFields(t *testing.T) {
	stored := storage.CredentialRecord{
		ClientID:     "stored-client",
		AccessToken:  auth.NewSecret("stored-" + leakAccessToken),
		RefreshToken: auth.NewSecret(leakRefreshToken),
		APIKey:       auth.NewSecret(leakAPIKey),
		ChannelID:    "UC-stored",
	}
	withMemoryCredentialStore(t, stored)

	cfg := baseConfigWithoutCredentials()
	cfg.Google.AccessToken = "exported-" + leakAccessToken

	status, err := applyStoredCredentials(context.Background(), &cfg)
	if err != nil {
		t.Fatalf("applyStoredCredentials: %v", err)
	}
	if !status.Present {
		t.Fatal("the seeded record was not found")
	}
	if !status.TokenShadowed {
		t.Error("an exported token over a saved one must be reported as shadowing")
	}
	if cfg.Google.AccessToken != "exported-"+leakAccessToken {
		t.Errorf("access token = %q; the exported value must win", cfg.Google.AccessToken)
	}
	for label, got := range map[string]string{
		"refresh token": cfg.Google.RefreshToken,
		"api key":       cfg.YouTube.APIKey,
		"client ID":     cfg.Google.ClientID,
		"channel ID":    cfg.YouTube.ChannelID,
	} {
		if got == "" {
			t.Errorf("%s was not filled from the store", label)
		}
	}

	// A nil config must be a no-op rather than a panic.
	applyCredentialRecord(nil, stored)
}

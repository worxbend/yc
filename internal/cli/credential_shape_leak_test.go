package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/storage"
)

// TestNoCommandCanPrintACredential proves that a value yc was *handed* does not
// come back out. These tests prove something narrower and, on the day it
// matters, more important: a value that merely *looks* like a Google credential
// does not come back out either.
//
// The distinction is the whole reason auth has a pattern redactor as well as a
// value redactor. The value redactor only removes what it was constructed with,
// and there are paths where that set is incomplete by construction: a token
// read from an environment variable yc does not know the name of, a key pasted
// into the endpoint override, a refresh token echoed back inside an OAuth error
// body, a client secret in a config key that a future release renames. On those
// paths the only thing standing between the credential and a terminal that is
// very often on stream is its shape.
//
// So each marker below is written in the exact form Google issues:
//
//	AIza...    a Data API key
//	ya29....   an OAuth 2.0 access token
//	1//...     an installed-app refresh token
//	GOCSPX-... an OAuth client secret
//
// A test using "fake-token-1" would pass without ever exercising the shape
// rules, which is precisely the test that would let a real one through.

const (
	shapedKey     = "AIzaSyFAKE00000000000000000000000000000"
	shapedAccess  = "ya29.a0AfFAKEnotarealaccesstokenatallxxxxxxxxxxxx"
	shapedRefresh = "1//0gFAKEnotarealrefreshtokenatallxxxxxxxxxxx"
	shapedSecret  = "GOCSPX-FAKEnotarealclientsecret00"
	// A bearer header and an authorization URL are the two composite shapes,
	// and both have leaked from real CLIs.
	shapedBearer  = "Bearer " + shapedAccess
	shapedAuthURL = "https://accounts.google.com/o/oauth2/v2/auth?client_id=fake.apps.googleusercontent.com&code_challenge=FAKEpkcechallenge0000&state=FAKEstate0000"
)

// credentialShapes is what no command may print, with the name of the thing
// that escaped - "output contained a secret" is not actionable, "the refresh
// token reached stderr" is.
func credentialShapes() map[string]string {
	return map[string]string{
		shapedKey:     "a Google API key (AIza...)",
		shapedAccess:  "an OAuth access token (ya29....)",
		shapedRefresh: "an OAuth refresh token (1//...)",
		shapedSecret:  "an OAuth client secret (GOCSPX-...)",
	}
}

// assertNoShapeLeak fails naming the credential kind and the surface.
func assertNoShapeLeak(t *testing.T, where, output string) {
	t.Helper()
	for marker, kind := range credentialShapes() {
		if strings.Contains(output, marker) {
			t.Errorf("%s leaked %s:\n%s", where, kind, output)
		}
	}
	// The composite shapes: a bearer header carries the access token, and an
	// authorization URL carries the PKCE challenge and the state parameter,
	// neither of which may be shown even though neither is a token.
	if strings.Contains(output, shapedBearer) {
		t.Errorf("%s leaked an Authorization header:\n%s", where, output)
	}
	for _, fragment := range []string{shapedAuthURL, "code_challenge=FAKEpkcechallenge0000", "state=FAKEstate0000"} {
		if strings.Contains(output, fragment) {
			t.Errorf("%s leaked an OAuth flow parameter (%s):\n%s", where, fragment, output)
		}
	}
}

// seedShapedCredentials populates every route a credential reaches yc by:
// the environment, a config file, and the saved credential record. All three
// carry the same shaped values, so a leak from any one of them is caught.
func seedShapedCredentials(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	clearCredentialEnv(t)
	t.Setenv("YC_GOOGLE_CLIENT_ID", "fake.apps.googleusercontent.com")
	t.Setenv("YC_GOOGLE_CLIENT_SECRET", shapedSecret)
	t.Setenv("YC_GOOGLE_ACCESS_TOKEN", shapedAccess)
	t.Setenv("YC_GOOGLE_REFRESH_TOKEN", shapedRefresh)
	t.Setenv("YC_YOUTUBE_API_KEY", shapedKey)

	withMemoryCredentialStore(t, storage.CredentialRecord{
		ClientID:     "fake.apps.googleusercontent.com",
		AccessToken:  auth.NewSecret(shapedAccess),
		RefreshToken: auth.NewSecret(shapedRefresh),
		APIKey:       auth.NewSecret(shapedKey),
		Scopes:       auth.LoginScopes(),
		ExpiresAt:    time.Now().Add(time.Hour),
	})

	return writeTempConfig(t, strings.Join([]string{
		`google_client_id = "fake.apps.googleusercontent.com"`,
		`google_client_secret = "` + shapedSecret + `"`,
		`google_access_token = "` + shapedAccess + `"`,
		`google_refresh_token = "` + shapedRefresh + `"`,
		`youtube_api_key = "` + shapedKey + `"`,
	}, "\n")+"\n")
}

// TestNoCommandPrintsAnythingShapedLikeACredential runs every command yc can
// complete without a network, with every credential populated in its real
// shape, and asserts that none of them reaches stdout, stderr, or the debug log.
func TestNoCommandPrintsAnythingShapedLikeACredential(t *testing.T) {
	commands := [][]string{
		{"--help"},
		{"help"},
		{"version"},
		{"--version"},
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
		{"quota", "--help"},
		{"doctor", "--help"},
		{"config", "--help"},
		{"profile", "--help"},
	}

	for _, args := range commands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cfgPath := seedShapedCredentials(t)
			debugPath := filepath.Join(t.TempDir(), "debug.log")

			full := append([]string(nil), args...)
			// Everything that accepts --config gets it, so the file route is
			// exercised alongside the environment one.
			switch args[0] {
			case "--help", "help", "version", "--version":
			default:
				if !(args[0] == "config" && len(args) > 1 && args[1] == "path") {
					full = append(full, "--config", cfgPath)
				}
			}
			// And everything that logs gets a log, so the redaction covers
			// the debug path and not only the terminal.
			switch args[0] {
			case "doctor", "login", "chat", "quota":
				full = append(full, "--debug-log", "--debug-log-path", debugPath)
			}

			var stdout, stderr bytes.Buffer
			Run(full, &stdout, &stderr)

			label := "`yc " + strings.Join(full, " ") + "`"
			assertNoShapeLeak(t, "stdout of "+label, stdout.String())
			assertNoShapeLeak(t, "stderr of "+label, stderr.String())
			if contents, err := os.ReadFile(debugPath); err == nil {
				assertNoShapeLeak(t, "the debug log of "+label, string(contents))
			}
		})
	}
}

// A command that fails must not describe its failure by quoting what it was
// given. An unparseable flag, an unknown subcommand, and a missing file are the
// three ways a user most often gets an error out of yc, and each one is a place
// where "invalid value: <what you typed>" is the obvious implementation.
func TestNoFailingCommandEchoesACredentialBack(t *testing.T) {
	commands := [][]string{
		{"chat", "--video", shapedKey, "--poll-interval", "not-a-duration"},
		{"config", "set", "youtube_api_key", shapedKey},
		{"profile", "show", shapedAccess},
		{"quota", "--limit", shapedRefresh},
		{"nonsense-subcommand", shapedSecret},
		{"chat", "--config", "/nonexistent/" + shapedKey + "/config.toml"},
		{"login", "--dry-run", "--client-secret", shapedSecret},
	}

	for _, args := range commands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			seedShapedCredentials(t)
			var stdout, stderr bytes.Buffer
			Run(args, &stdout, &stderr)

			label := "`yc " + strings.Join(args, " ") + "`"
			assertNoShapeLeak(t, "stdout of "+label, stdout.String())
			assertNoShapeLeak(t, "stderr of "+label, stderr.String())
		})
	}
}

// The endpoint override is operator-supplied configuration rather than a
// credential, which is exactly why it is dangerous: a user debugging against a
// proxy pastes a whole URL into it, and a whole URL is where an API key lives.
func TestAnEndpointOverrideCarryingAKeyIsNotEchoed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	withMemoryCredentialStore(t, storage.CredentialRecord{})

	cfgPath := writeTempConfig(t, strings.Join([]string{
		`youtube_api_key = "` + shapedKey + `"`,
		`[youtube]`,
		`endpoint = "https://proxy.invalid/youtube/v3?key=` + shapedKey + `"`,
	}, "\n")+"\n")

	for _, args := range [][]string{
		{"config", "show", "--config", cfgPath},
		{"doctor", "--config", cfgPath},
		{"quota", "--config", cfgPath},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			Run(args, &stdout, &stderr)
			assertNoShapeLeak(t, "stdout of `yc "+strings.Join(args, " ")+"`", stdout.String())
			assertNoShapeLeak(t, "stderr of `yc "+strings.Join(args, " ")+"`", stderr.String())
		})
	}
}

// Redaction must not be achieved by printing nothing. `config show` is how a
// user checks whether a credential is configured at all, so it has to keep
// saying "yes, and it is hidden" rather than going silent - otherwise the
// safest implementation is also the least useful one, and somebody will
// eventually relax it.
func TestConfigShowStillReportsThatEachCredentialIsSet(t *testing.T) {
	cfgPath := seedShapedCredentials(t)

	var stdout, stderr bytes.Buffer
	Run([]string{"config", "show", "--config", cfgPath}, &stdout, &stderr)
	output := stdout.String()

	assertNoShapeLeak(t, "stdout of `yc config show`", output)
	for _, key := range []string{
		"google_client_secret",
		"google_access_token",
		"google_refresh_token",
		"youtube_api_key",
	} {
		if !strings.Contains(output, key) {
			t.Errorf("`yc config show` no longer names %s; a user cannot tell whether it is configured", key)
		}
	}
	// The placeholder is config.Redacted here rather than auth.RedactedSecret:
	// `config show` renders TOML, and the two placeholders are deliberately
	// different so a value that arrived through the config formatter is
	// distinguishable from one a Secret redacted itself.
	if !strings.Contains(output, config.Redacted) {
		t.Errorf("`yc config show` printed no %s placeholder:\n%s", config.Redacted, output)
	}
	if strings.Contains(output, auth.RedactedSecret) {
		t.Errorf("`yc config show` used the wrong placeholder; TOML output uses %s:\n%s", config.Redacted, output)
	}
}

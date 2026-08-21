package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/app"
	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/storage"
	"github.com/worxbend/yc/internal/storage/storagetest"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// fakeToken is an obvious non-credential, so a leak in test output is
// unmistakable.
const fakeToken = auth.FakeTokenMarker

func TestRunPrintsUsageAndVersion(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"bare", nil, ExitOK},
		{"help", []string{"help"}, ExitOK},
		{"help flag", []string{"--help"}, ExitOK},
		{"version", []string{"--version"}, ExitOK},
		{"unknown", []string{"nope"}, ExitUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := Run(tc.args, &stdout, &stderr); got != tc.want {
				t.Fatalf("Run(%v) = %d, want %d", tc.args, got, tc.want)
			}
			if tc.want == ExitUsage && stderr.Len() == 0 {
				t.Error("a usage failure must explain itself on stderr")
			}
		})
	}
}

func TestChatFlagsAccumulateAcrossSpellings(t *testing.T) {
	var flags chatFlags
	for _, value := range []string{"abc", "d,e", " @handle ", "UC123"} {
		if err := flags.Set(value); err != nil {
			t.Fatalf("Set(%q): %v", value, err)
		}
	}
	if got, want := strings.Join(flags, "|"), "abc|d|e|@handle|UC123"; got != want {
		t.Errorf("chats = %q, want %q", got, want)
	}
	if err := flags.Set("  ,  "); err == nil {
		t.Error("an all-empty value must be rejected rather than silently dropped")
	}
}

func TestDebugLogFlagIsTriState(t *testing.T) {
	var absent optionalBoolFlag
	if absent.set {
		t.Error("an unset flag must not report as set")
	}

	var explicit optionalBoolFlag
	if err := explicit.Set("false"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !explicit.set || explicit.value {
		t.Error("--debug-log=false must record an explicit false")
	}

	overrides := config.Overrides{}
	applyDebugFlagOverrides(&overrides, debugFlagOptions{enabled: explicit})
	if !overrides.DebugLogSet || overrides.DebugLogEnabled {
		t.Errorf("overrides = %+v, want an explicit disable", overrides)
	}
}

func TestConfigShowRedactsSecrets(t *testing.T) {
	path := writeTempConfig(t, "google_access_token = \""+fakeToken+"\"\n")
	withMemoryCredentialStore(t, storage.CredentialRecord{})

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"config", "show", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("config show = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), fakeToken) {
		t.Fatalf("config show leaked a token:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), config.Redacted) {
		t.Errorf("expected a redaction placeholder:\n%s", stdout.String())
	}
}

func TestChatWithoutCredentialsFailsWithoutNetworking(t *testing.T) {
	clearCredentialEnv(t)
	path := writeTempConfig(t, "")
	withMemoryCredentialStore(t, storage.CredentialRecord{})

	// Any attempt to build a transport would be a bug: refusing must happen
	// before a socket exists.
	restore := swapLiveChatClient(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"chat", "--config", path}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("chat = %d, want %d; stderr=%s", code, ExitUsage, stderr.String())
	}
	message := stderr.String()
	for _, want := range []string{"yc login", "YC_YOUTUBE_API_KEY", "--mock"} {
		if !strings.Contains(message, want) {
			t.Errorf("guidance is missing %q:\n%s", want, message)
		}
	}
}

func TestLoginDryRunTouchesNothing(t *testing.T) {
	path := writeTempConfig(t, "google_client_id = \"client\"\n")

	// A dry run that reached any of these would not be a dry run.
	originalWaiter := newLoginCallbackWaiter
	originalBrowser := openLoginBrowser
	t.Cleanup(func() {
		newLoginCallbackWaiter = originalWaiter
		openLoginBrowser = originalBrowser
	})
	newLoginCallbackWaiter = func(string, auth.Secret) (loginCallbackWaiter, error) {
		t.Fatal("dry run bound a listener")
		return nil, nil
	}
	openLoginBrowser = func(context.Context, string) error {
		t.Fatal("dry run opened a browser")
		return nil
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"login", "--dry-run", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("login --dry-run = %d, stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{"dry run", "Requested scopes", "youtube.force-ssl", "Client ID: present"} {
		if !strings.Contains(output, want) {
			t.Errorf("dry run output is missing %q:\n%s", want, output)
		}
	}
}

func TestPollIntervalBounds(t *testing.T) {
	cases := []struct {
		name        string
		quota       config.QuotaConfig
		wantMin     time.Duration
		wantMaxZero bool
	}{
		{"auto floor", config.QuotaConfig{PollIntervalMode: "auto", PollIntervalFloorMS: 1000}, time.Second, true},
		{"economy raises the floor", config.QuotaConfig{PollIntervalMode: "economy", PollIntervalFloorMS: 1000}, 5 * time.Second, true},
		{"economy keeps a higher floor", config.QuotaConfig{PollIntervalMode: "economy", PollIntervalFloorMS: 9000}, 9 * time.Second, true},
		{"off is manual only", config.QuotaConfig{PollIntervalMode: "off", PollIntervalFloorMS: 1000}, manualPollInterval, false},
		{"unset floor defaults", config.QuotaConfig{PollIntervalMode: "auto"}, time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMin, gotMax := pollIntervalBounds(tc.quota)
			if gotMin != tc.wantMin {
				t.Errorf("min = %s, want %s", gotMin, tc.wantMin)
			}
			if tc.wantMaxZero && gotMax != 0 {
				t.Errorf("max = %s, want no ceiling", gotMax)
			}
		})
	}
}

func TestPollIntervalCeilingNeverUndercutsTheFloor(t *testing.T) {
	minInterval, maxInterval := pollIntervalBounds(config.QuotaConfig{
		PollIntervalMode:      "economy",
		PollIntervalFloorMS:   1000,
		PollIntervalCeilingMS: 2000,
	})
	if maxInterval < minInterval {
		t.Fatalf("ceiling %s is below the floor %s", maxInterval, minInterval)
	}
}

func TestCostTableAppliesOverridesOnly(t *testing.T) {
	table := costTable(config.QuotaCostConfig{List: 9, Insert: 0})
	if got, want := table.Cost(quota.EndpointMessagesList), 9; got != want {
		t.Errorf("list cost = %d, want the override %d", got, want)
	}
	if got, want := table.Cost(quota.EndpointMessagesInsert), quota.DefaultCostTable().Cost(quota.EndpointMessagesInsert); got != want {
		t.Errorf("insert cost = %d, want the default %d; zero means 'not overridden'", got, want)
	}
}

func TestDescribeCapability(t *testing.T) {
	base := config.Default()

	none := describeCapability(base, credentialLoadStatus{})
	if none.Mode != credentialModeNone || none.CanRead {
		t.Errorf("empty credentials = %+v, want an unusable mode", none)
	}

	keyOnly := base
	keyOnly.YouTube.APIKey = fakeToken
	key := describeCapability(keyOnly, credentialLoadStatus{})
	if key.Mode != credentialModeAPIKey || !key.CanRead || key.CanSend {
		t.Errorf("api key = %+v, want read-only", key)
	}
	if key.Reason == "" {
		t.Error("a degraded capability must carry a reason")
	}

	readOnlyOAuth := base
	readOnlyOAuth.Google.AccessToken = fakeToken
	readOnly := describeCapability(readOnlyOAuth, credentialLoadStatus{
		Present: true,
		Record:  storage.CredentialRecord{Scopes: auth.ReadScopes()},
	})
	if !readOnly.ScopesKnown || readOnly.CanSend {
		t.Errorf("readonly scopes = %+v, want send disabled with known scopes", readOnly)
	}

	full := describeCapability(readOnlyOAuth, credentialLoadStatus{
		Present: true,
		Record:  storage.CredentialRecord{Scopes: auth.LoginScopes()},
	})
	if !full.CanSend || !full.CanModerate {
		t.Errorf("full scopes = %+v, want send and moderate", full)
	}

	unknown := describeCapability(readOnlyOAuth, credentialLoadStatus{})
	if unknown.ScopesKnown {
		t.Error("a token from the environment carries no scope record")
	}
	if !strings.Contains(unknown.Reason, "unknown") {
		t.Errorf("reason = %q, want it to admit the scopes are unknown", unknown.Reason)
	}
}

func TestCredentialRecordFromConfigDropsStaleExpiryForAnExternalToken(t *testing.T) {
	stored := storage.CredentialRecord{
		AccessToken: auth.NewSecret("stored-" + fakeToken),
		ExpiresAt:   time.Now().Add(time.Hour),
		Scopes:      auth.LoginScopes(),
		ChannelID:   "UC123",
	}
	cfg := config.Default()
	cfg.Google.AccessToken = "env-" + fakeToken

	record := credentialRecordFromConfig(cfg, stored)
	if record.AccessToken.Reveal() != "env-"+fakeToken {
		t.Error("the config token must win over the stored one")
	}
	if !record.ExpiresAt.IsZero() || len(record.Scopes) != 0 {
		t.Error("an externally supplied token must not inherit the stored expiry or scopes")
	}
	if record.ChannelID != "UC123" {
		t.Error("the cached identity should survive")
	}
}

func TestCredentialHolderRefreshIsSingleFlightAndPersists(t *testing.T) {
	store := storagetest.NewMemoryCredentialStore()
	refresher := &countingRefresher{
		release: make(chan struct{}),
		token:   auth.NewSecret("fresh-" + fakeToken),
	}
	holder := newCredentialHolder(storage.CredentialRecord{
		AccessToken:  auth.NewSecret("stale-" + fakeToken),
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
	}, store, refresher)

	const callers = 8
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { errs <- holder.Refresh(context.Background()) }()
	}
	// Let every caller arrive before the single exchange completes.
	time.Sleep(20 * time.Millisecond)
	close(refresher.release)
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("Refresh: %v", err)
		}
	}

	if got := refresher.calls.Load(); got != 1 {
		t.Errorf("refresh exchanges = %d, want exactly 1", got)
	}
	if got := holder.AccessToken().Reveal(); got != "fresh-"+fakeToken {
		t.Errorf("access token = %q, want the refreshed one", got)
	}
	saved, ok, err := store.LoadCredentials(context.Background())
	if err != nil || !ok {
		t.Fatalf("LoadCredentials: ok=%v err=%v", ok, err)
	}
	if saved.AccessToken.Reveal() != "fresh-"+fakeToken {
		t.Error("the rotated credentials must be persisted")
	}
	if saved.RefreshToken.Reveal() != "refresh-"+fakeToken {
		t.Error("an omitted refresh token means 'keep the one you have'")
	}
}

func TestChatTargetsPutAnExplicitLiveChatIDFirst(t *testing.T) {
	cfg := config.Default()
	targets, err := chatTargets(cfg, " Cg0KC2FiYw ")
	if err != nil {
		t.Fatalf("chatTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	if targets[0].Kind != youtube.TargetLiveChatID || targets[0].LiveChatID != "Cg0KC2FiYw" {
		t.Errorf("target = %+v, want a trimmed explicit live chat ID", targets[0])
	}
	if !targets[0].Resolved() {
		t.Error("an explicit live chat ID must need no resolution")
	}
}

func TestSafeStartupErrorRedactsEverything(t *testing.T) {
	cfg := config.Default()
	cfg.Google.AccessToken = fakeToken
	redactor := configRedactor(cfg)
	got := safeStartupError(redactor, errNamed("request failed with "+fakeToken))
	if strings.Contains(got, fakeToken) {
		t.Fatalf("startup error leaked a token: %q", got)
	}
}

// errNamed is a tiny error carrying an exact message.
type errNamed string

func (e errNamed) Error() string { return string(e) }

// countingRefresher records how many exchanges actually happened and blocks
// until released, so a single-flight violation is observable.
type countingRefresher struct {
	calls   atomic.Int64
	release chan struct{}
	token   auth.Secret
}

func (r *countingRefresher) Refresh(ctx context.Context, _ auth.Secret) (auth.TokenSet, error) {
	r.calls.Add(1)
	select {
	case <-r.release:
	case <-ctx.Done():
		return auth.TokenSet{}, ctx.Err()
	}
	return auth.TokenSet{AccessToken: r.token, TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

// writeTempConfig writes a config file for one test.
func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// withMemoryCredentialStore swaps the credential store for an in-memory one, so
// no test can read or write a real credential file.
func withMemoryCredentialStore(t *testing.T, record storage.CredentialRecord) *storagetest.MemoryCredentialStore {
	t.Helper()
	store := storagetest.NewMemoryCredentialStore()
	if !record.Empty() {
		if err := store.SaveCredentials(context.Background(), record); err != nil {
			t.Fatalf("seed credential store: %v", err)
		}
	}
	original := newCredentialStore
	t.Cleanup(func() { newCredentialStore = original })
	newCredentialStore = func() (storage.CredentialStore, error) { return store, nil }
	return store
}

// swapLiveChatClient makes any attempt to build a transport a test failure.
func swapLiveChatClient(t *testing.T) func() {
	t.Helper()
	original := newLiveChatClient
	newLiveChatClient = func(*youtube.Client, config.Config, []youtube.ChatTarget, debuglog.Logger) (app.ChatClient, error) {
		t.Fatal("a transport was built for a run with no credentials")
		return nil, nil
	}
	return func() { newLiveChatClient = original }
}

// clearCredentialEnv neutralizes any credential the developer's shell happens
// to export, so the test asserts yc's behavior and not the machine's.
func clearCredentialEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"YC_GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_ID",
		"YC_GOOGLE_CLIENT_SECRET", "GOOGLE_CLIENT_SECRET",
		"YC_GOOGLE_ACCESS_TOKEN", "GOOGLE_ACCESS_TOKEN",
		"YC_GOOGLE_REFRESH_TOKEN", "GOOGLE_REFRESH_TOKEN",
		"YC_YOUTUBE_API_KEY", "YC_DEFAULT_CHATS", "YC_DEBUG_LOG",
	} {
		t.Setenv(key, "")
	}
}

// TestUsageListsEveryEnvironmentVariable walks the config structs and checks
// that every YC_* variable yc actually reads is named in the usage text.
//
// The usage text is hand-written, so it can only drift one way: a setting gets
// added and the help page keeps quiet about it. Reading the names off the
// struct tags - the same tags the loader reads - means the test learns about a
// new variable at the same moment the loader does.
func TestUsageListsEveryEnvironmentVariable(t *testing.T) {
	for _, name := range configEnvKeys(reflect.TypeOf(config.Config{})) {
		if !strings.HasPrefix(name, "YC_") {
			// GOOGLE_* aliases are listed beside their YC_ spellings, but
			// they belong to Google's own conventions rather than yc's.
			continue
		}
		if !strings.Contains(usage, name) {
			t.Errorf("usage text does not mention %s; add it to the Environment section", name)
		}
	}
}

// configEnvKeys collects the environment variable names from the `env` struct
// tags of a config struct and everything nested inside it. Palette roles carry
// no tag - the loader derives YC_THEME_<ROLE> from the field name - so they are
// derived the same way here.
func configEnvKeys(structType reflect.Type) []string {
	var keys []string
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if field.Type == reflect.TypeOf(theme.Palette{}) {
			for j := 0; j < field.Type.NumField(); j++ {
				keys = append(keys, "YC_THEME_"+strings.ToUpper(field.Type.Field(j).Name))
			}
			continue
		}
		if field.Type.Kind() == reflect.Struct {
			keys = append(keys, configEnvKeys(field.Type)...)
			continue
		}
		for _, name := range strings.Split(field.Tag.Get("env"), ",") {
			if name = strings.TrimSpace(name); name != "" {
				keys = append(keys, name)
			}
		}
	}
	return keys
}

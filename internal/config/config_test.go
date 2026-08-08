package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeToken is an obvious non-credential used wherever a test needs a secret
// shaped value.
const fakeToken = "test-not-a-real-token"

func TestLoadAppliesFileThenEnvThenOverrides(t *testing.T) {
	path := writeConfigFile(t, `
# a comment
google_client_id = "file-client"
default_chats = "file-a, file-b"
animation_mode = "reduced"
theme_name = "nord"
theme_accent = "#101010"
poll_interval_floor_ms = 2500
follow_server_cadence = true
enable_mouse = false
`)

	cfg, err := Load([]string{
		"YC_ANIMATION_MODE=fast",
		"YC_GOOGLE_ACCESS_TOKEN=" + fakeToken,
		"YC_THEME_ACCENT=#abcdef",
	}, Overrides{ConfigPath: path, Chats: []string{"flag-chat"}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got, want := cfg.Google.ClientID, "file-client"; got != want {
		t.Errorf("client id = %q, want %q", got, want)
	}
	if got, want := cfg.Features.AnimationMode, "fast"; got != want {
		t.Errorf("environment did not beat file: animation mode = %q, want %q", got, want)
	}
	if got, want := strings.Join(cfg.DefaultChats, ","), "flag-chat"; got != want {
		t.Errorf("override did not beat file: chats = %q, want %q", got, want)
	}
	if got, want := cfg.Features.ThemeCustom.Accent, "#abcdef"; got != want {
		t.Errorf("palette env key not applied: accent = %q, want %q", got, want)
	}
	if got, want := cfg.Quota.PollIntervalFloorMS, 2500; got != want {
		t.Errorf("poll floor = %d, want %d", got, want)
	}
	if !cfg.Quota.FollowServerCadence {
		t.Error("follow_server_cadence should be true")
	}
	if cfg.Features.EnableMouse {
		t.Error("enable_mouse should be false")
	}
	if got, want := cfg.Google.AccessToken, fakeToken; got != want {
		t.Errorf("access token = %q, want %q", got, want)
	}
}

func TestLoadPrefersPrefixedEnvAlias(t *testing.T) {
	path := writeConfigFile(t, "")
	cfg, err := Load([]string{
		"GOOGLE_CLIENT_ID=unprefixed",
		"YC_GOOGLE_CLIENT_ID=prefixed",
	}, Overrides{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Google.ClientID, "prefixed"; got != want {
		t.Errorf("client id = %q, want %q; the YC_ prefixed name must win", got, want)
	}
}

func TestLoadIgnoresEmptyEnvValues(t *testing.T) {
	path := writeConfigFile(t, `google_client_id = "file-client"`)
	cfg, err := Load([]string{"YC_GOOGLE_CLIENT_ID=   "}, Overrides{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Google.ClientID, "file-client"; got != want {
		t.Errorf("client id = %q, want %q; a blank env var must not mask the file", got, want)
	}
}

func TestLoadRejectsMalformedLine(t *testing.T) {
	path := writeConfigFile(t, "this is not an assignment\n")
	if _, err := Load(nil, Overrides{ConfigPath: path}); err == nil {
		t.Fatal("expected a parse error for a line without =")
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.toml")
	cfg, err := Load(nil, Overrides{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Features.AnimationMode, Default().Features.AnimationMode; got != want {
		t.Errorf("animation mode = %q, want the default %q", got, want)
	}
}

func TestRedactedStringHidesSecretsAndKeepsEmptiesEmpty(t *testing.T) {
	cfg := Default()
	cfg.Path = "/home/user/.config/yc/config.toml"
	cfg.Google.ClientID = "public-client"
	cfg.Google.AccessToken = fakeToken
	cfg.YouTube.APIKey = "AIza" + strings.Repeat("x", 35)

	output := cfg.RedactedString()
	if strings.Contains(output, fakeToken) {
		t.Fatal("redacted output leaked the access token")
	}
	if strings.Contains(output, "AIza") {
		t.Fatal("redacted output leaked the API key")
	}
	if !strings.Contains(output, `google_access_token = "`+Redacted+`"`) {
		t.Errorf("expected a redacted access token placeholder in:\n%s", output)
	}
	if !strings.Contains(output, `google_refresh_token = ""`) {
		t.Errorf("an unset secret must stay empty so it reads as unconfigured:\n%s", output)
	}
	if !strings.Contains(output, `google_client_id = "public-client"`) {
		t.Errorf("a non-secret value must be shown:\n%s", output)
	}
	for _, key := range []string{"poll_interval_mode", "quota_cost_list", "theme_success", "debug_logging"} {
		if !strings.Contains(output, key+" = ") {
			t.Errorf("key %q missing from redacted output:\n%s", key, output)
		}
	}
}

func TestRedactDisplayValue(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		masked bool
	}{
		{"plain path", "/home/user/.cache/yc/debug.log", false},
		{"loopback redirect", "http://127.0.0.1:8080/", false},
		{"access token query", "http://host/cb?access_token=" + fakeToken, true},
		{"api key query", "https://host/v3/videos?key=AIza" + strings.Repeat("y", 35), true},
		{"bare google key", "AIza" + strings.Repeat("z", 35), true},
		{"url userinfo", "http://user:pass@127.0.0.1:9/", true},
		{"oauth state", "http://127.0.0.1:9/cb?state=abc", true},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactDisplayValue(tc.value)
			if tc.masked && got != Redacted {
				t.Errorf("RedactDisplayValue(%q) = %q, want %q", tc.value, got, Redacted)
			}
			if !tc.masked && got != tc.value {
				t.Errorf("RedactDisplayValue(%q) = %q, want it unchanged", tc.value, got)
			}
		})
	}
}

func TestWriteNonSecretFileNeverWritesSecretsAndPreservesUnknownLines(t *testing.T) {
	path := writeConfigFile(t, `# keep me
google_access_token = "`+fakeToken+`"
future_key_from_a_newer_build = "keep"
theme_name = "nord"
`)

	cfg := Default()
	cfg.Path = path
	cfg.Google.ClientID = "written-client"
	cfg.Google.ClientSecret = fakeToken
	cfg.YouTube.APIKey = fakeToken
	cfg.Features.ThemeName = "dracula"
	cfg.DefaultChats = []string{"a", "b"}

	if err := WriteNonSecretFile(path, cfg); err != nil {
		t.Fatalf("WriteNonSecretFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	written := string(data)

	if strings.Count(written, fakeToken) != 1 {
		t.Errorf("the pre-existing secret line must be preserved exactly once and no secret written:\n%s", written)
	}
	if strings.Contains(written, "google_client_secret") || strings.Contains(written, "youtube_api_key") {
		t.Errorf("a secret key must never be created:\n%s", written)
	}
	if !strings.Contains(written, "# keep me") || !strings.Contains(written, "future_key_from_a_newer_build") {
		t.Errorf("unknown lines and comments must survive:\n%s", written)
	}
	if !strings.Contains(written, `theme_name = "dracula"`) {
		t.Errorf("a known key must be rewritten in place:\n%s", written)
	}
	if !strings.Contains(written, `default_chats = "a,b"`) {
		t.Errorf("a missing key must be appended:\n%s", written)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config file permissions %04o allow group/other access", perm)
	}
}

func TestWriteNonSecretFileRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := Default()
	cfg.Path = path
	cfg.Features.MessageLayout = "grouped"
	cfg.Features.SidebarWidth = 24
	cfg.Quota.SessionHours = 6
	cfg.Quota.Costs.List = 7
	cfg.DefaultChats = []string{"@handle", "https://youtu.be/abc"}

	if err := WriteNonSecretFile(path, cfg); err != nil {
		t.Fatalf("WriteNonSecretFile: %v", err)
	}
	loaded, err := Load(nil, Overrides{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := loaded.Features.MessageLayout, "grouped"; got != want {
		t.Errorf("layout = %q, want %q", got, want)
	}
	if got, want := loaded.Features.SidebarWidth, 24; got != want {
		t.Errorf("sidebar width = %d, want %d", got, want)
	}
	if got, want := loaded.Quota.SessionHours, 6; got != want {
		t.Errorf("session hours = %d, want %d", got, want)
	}
	if got, want := loaded.Quota.Costs.List, 7; got != want {
		t.Errorf("list cost = %d, want %d", got, want)
	}
	if got, want := strings.Join(loaded.DefaultChats, ","), "@handle,https://youtu.be/abc"; got != want {
		t.Errorf("chats = %q, want %q; nothing may be stripped from a target", got, want)
	}
}

func TestLoadEnvOnlyIgnoresFile(t *testing.T) {
	path := writeConfigFile(t, `theme_name = "nord"`)
	cfg, err := LoadEnvOnly([]string{"YC_THEME_NAME=dracula"}, Overrides{ConfigPath: path})
	if err != nil {
		t.Fatalf("LoadEnvOnly: %v", err)
	}
	if got, want := cfg.Features.ThemeName, "dracula"; got != want {
		t.Errorf("theme = %q, want %q", got, want)
	}
	if cfg.Path != path {
		t.Errorf("path = %q, want %q", cfg.Path, path)
	}
}

func TestDebugLogOverridesAreTriState(t *testing.T) {
	path := writeConfigFile(t, "debug_logging = true\n")

	on, err := Load(nil, Overrides{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !on.Debug.Enabled {
		t.Error("file value should enable debug logging")
	}

	off, err := Load(nil, Overrides{ConfigPath: path, DebugLogSet: true, DebugLogEnabled: false})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if off.Debug.Enabled {
		t.Error("--debug-log=false must override debug_logging = true")
	}
}

func TestBindingsCoverEveryDocumentedKey(t *testing.T) {
	cfg := Default()
	seen := map[string]bool{}
	for _, b := range bindingsFor(&cfg) {
		if seen[b.TOMLKey] {
			t.Errorf("duplicate config key %q", b.TOMLKey)
		}
		seen[b.TOMLKey] = true
	}
	for _, key := range []string{
		"google_client_id", "google_client_secret", "google_access_token",
		"google_refresh_token", "google_redirect_url", "youtube_api_key",
		"youtube_channel_id", "default_chats", "enable_mouse", "avatar_mode",
		"animation_mode", "theme_name", "theme_background", "theme_success",
		"message_layout", "badge_mode", "highlight_emoji", "full_username",
		"sidebar_width", "activity_width", "scrollback_limit",
		"stream_status_mode", "emoji_autocomplete_mode", "poll_interval_mode",
		"poll_interval_floor_ms", "poll_interval_ceiling_ms", "daily_quota_units",
		"search_quota_calls", "quota_reserve_percent", "session_hours",
		"follow_server_cadence", "allow_search", "quota_cost_list",
		"quota_cost_search_list", "debug_logging", "debug_log_path",
	} {
		if !seen[key] {
			t.Errorf("config key %q has no binding", key)
		}
	}
}

// writeConfigFile writes a temporary config file and returns its path.
func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

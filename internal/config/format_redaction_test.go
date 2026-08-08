package config

import (
	"fmt"
	"strings"
	"testing"
)

// Config's credential fields are plain strings, so fmt reaches them by
// reflection and auth.Secret's guard does not apply. Config is also passed by
// value into most of internal/cli and internal/app, which makes it the value
// most likely to meet a fmt verb by accident. These tests pin the guard that
// makes that harmless.

const (
	formatTestClientSecret = "GOCSPX-test-not-a-real-client-secret"
	formatTestAccessToken  = "ya29.test-not-a-real-access-token"
	formatTestRefreshToken = "1//test-not-a-real-refresh-token"
	formatTestAPIKey       = "AIzaSyTESTNOTAREALKEY0123456789012345"
)

func credentialConfig() Config {
	cfg := Default()
	cfg.Google.ClientID = "test-client-id.apps.googleusercontent.com"
	cfg.Google.ClientSecret = formatTestClientSecret
	cfg.Google.AccessToken = formatTestAccessToken
	cfg.Google.RefreshToken = formatTestRefreshToken
	cfg.YouTube.APIKey = formatTestAPIKey
	return cfg
}

func assertNoSecrets(t *testing.T, label, rendered string) {
	t.Helper()
	for _, secret := range []string{
		formatTestClientSecret,
		formatTestAccessToken,
		formatTestRefreshToken,
		formatTestAPIKey,
	} {
		if strings.Contains(rendered, secret) {
			t.Errorf("%s leaked a credential: %s", label, rendered)
		}
	}
}

func TestConfigFormattingNeverPrintsCredentials(t *testing.T) {
	cfg := credentialConfig()
	for label, rendered := range map[string]string{
		"config %v":   fmt.Sprintf("%v", cfg),
		"config %+v":  fmt.Sprintf("%+v", cfg),
		"config %#v":  fmt.Sprintf("%#v", cfg),
		"google %v":   fmt.Sprintf("%v", cfg.Google),
		"google %+v":  fmt.Sprintf("%+v", cfg.Google),
		"google %#v":  fmt.Sprintf("%#v", cfg.Google),
		"youtube %v":  fmt.Sprintf("%v", cfg.YouTube),
		"youtube %+v": fmt.Sprintf("%+v", cfg.YouTube),
		"youtube %#v": fmt.Sprintf("%#v", cfg.YouTube),
		"pointer %v":  fmt.Sprintf("%v", &cfg),
		"pointer %+v": fmt.Sprintf("%+v", &cfg),
	} {
		assertNoSecrets(t, label, rendered)
	}
}

// The summary still has to be worth printing, or the guard will be worked
// around the first time someone needs to see which config is loaded.
func TestConfigStringNamesTheRunWithoutSecrets(t *testing.T) {
	cfg := credentialConfig()
	cfg.Path = "/tmp/yc/config.toml"
	cfg.DefaultChats = []string{"dQw4w9WgXcQ", "@someone"}

	summary := cfg.String()
	assertNoSecrets(t, "String()", summary)
	for _, want := range []string{"/tmp/yc/config.toml", cfg.Features.ThemeName, "2"} {
		if !strings.Contains(summary, want) {
			t.Errorf("String() = %q, want it to mention %q", summary, want)
		}
	}
	if got := cfg.Google.String(); !strings.Contains(got, "present") {
		t.Errorf("GoogleConfig.String() = %q, want it to report presence", got)
	}
	if got := (GoogleConfig{}).String(); !strings.Contains(got, "missing") {
		t.Errorf("empty GoogleConfig.String() = %q, want it to report absence", got)
	}
}

func TestRedactedStringStillReportsEverySecretAsConfigured(t *testing.T) {
	rendered := credentialConfig().RedactedString()
	assertNoSecrets(t, "RedactedString", rendered)
	for _, key := range []string{
		"google_client_secret",
		"google_access_token",
		"google_refresh_token",
		"youtube_api_key",
	} {
		if !strings.Contains(rendered, key+" = "+`"`+Redacted+`"`) {
			t.Errorf("RedactedString did not mark %s as configured:\n%s", key, rendered)
		}
	}
}

// default_chats holds user-supplied targets, and a watch URL is somewhere an
// API key can be pasted. `yc config show` prints it and WriteNonSecretFile
// writes it back, so it needs the same display redaction every other string
// key gets.
func TestListValuedKeysGoThroughTheDisplayRedactor(t *testing.T) {
	cfg := Default()
	cfg.DefaultChats = []string{
		"dQw4w9WgXcQ",
		"https://youtube.com/live/x?key=" + formatTestAPIKey,
		"@someone",
	}

	rendered := cfg.RedactedString()
	assertNoSecrets(t, "RedactedString default_chats", rendered)
	// One poisoned entry must not blank the rest of the list.
	for _, keep := range []string{"dQw4w9WgXcQ", "@someone"} {
		if !strings.Contains(rendered, keep) {
			t.Errorf("RedactedString dropped the safe chat %q:\n%s", keep, rendered)
		}
	}
}

package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/theme"
)

func TestProfileListMarksTheActivePalette(t *testing.T) {
	clearCredentialEnv(t)
	path := writeTempConfig(t, "theme_name = \"nord\"\n")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"profile", "list", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("profile list = %d, stderr=%s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")

	marked := 0
	names := make(map[string]bool, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "> "), "  "))
		names[name] = true
		if strings.HasPrefix(line, "> ") {
			marked++
			if name != "nord" {
				t.Errorf("marked %q as active, want nord", name)
			}
		}
	}
	if marked != 1 {
		t.Errorf("%d palettes marked active, want exactly 1", marked)
	}
	// Every preset plus the custom slot must be reachable from the listing, or
	// `yc profile set` can name something the user cannot discover.
	for _, preset := range theme.PresetNames() {
		if !names[preset] {
			t.Errorf("preset %q is missing from the listing", preset)
		}
	}
	if !names["custom"] {
		t.Error("the custom slot is missing from the listing")
	}
}

func TestProfileShowPrintsEveryRole(t *testing.T) {
	clearCredentialEnv(t)
	path := writeTempConfig(t, "theme_name = \"nord\"\n")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"profile", "show", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("profile show = %d, stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	for _, role := range []string{
		"theme_name", "background", "foreground", "accent", "muted",
		"border", "surface", "warning", "error", "success",
	} {
		if !strings.Contains(got, role+" = ") {
			t.Errorf("profile show is missing the %q role:\n%s", role, got)
		}
	}
}

func TestProfileSetPersistsAPresetAsANonSecretSetting(t *testing.T) {
	clearCredentialEnv(t)
	path := writeTempConfig(t, "google_client_id = \"client\"\n")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"profile", "set", "NORD", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("profile set = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "theme set to nord") {
		t.Errorf("stdout = %q", stdout.String())
	}

	cfg, err := config.Load(nil, config.Overrides{ConfigPath: path})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Features.ThemeName != "nord" {
		t.Errorf("theme = %q, want the lowercased preset", cfg.Features.ThemeName)
	}
	// Setting a theme must not lose an unrelated non-secret value.
	if cfg.Google.ClientID != "client" {
		t.Errorf("client ID = %q; the write dropped an unrelated setting", cfg.Google.ClientID)
	}
	raw := readConfigFile(t, path)
	for _, forbidden := range []string{"google_client_secret", "google_access_token", "youtube_api_key"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("profile set wrote the secret key %q:\n%s", forbidden, raw)
		}
	}
}

func TestProfileSetCustomStoresOnlyTheRolesGiven(t *testing.T) {
	clearCredentialEnv(t)
	path := writeTempConfig(t, "")

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"profile", "set", "custom", "--config", path,
		"--background", "#101018",
		"--accent", "#7aa2f7",
		"--success", "#9ece6a",
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("profile set custom = %d, stderr=%s", code, stderr.String())
	}

	cfg, err := config.Load(nil, config.Overrides{ConfigPath: path})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	custom := cfg.Features.ThemeCustom
	if custom.Background != "#101018" || custom.Accent != "#7aa2f7" || custom.Success != "#9ece6a" {
		t.Errorf("custom palette = %+v, want the three supplied roles", custom)
	}
	// theme.ResolvePalette takes "custom" verbatim, so an unsupplied role stays
	// unset rather than inheriting from a preset. Writing an empty value would
	// be worse: it would pin the role to empty in the file too.
	if custom.Foreground != "" || custom.Muted != "" {
		t.Errorf("custom palette = %+v; an unsupplied role must not be written", custom)
	}
	if cfg.Features.ThemeName != "custom" {
		t.Errorf("theme = %q, want custom", cfg.Features.ThemeName)
	}
}

// A typo in a theme name must be a usage error naming where to look, not a
// silent write of a palette that does not exist.
func TestProfileSetRejectsAnUnknownPreset(t *testing.T) {
	clearCredentialEnv(t)
	path := writeTempConfig(t, "")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"profile", "set", "definitely-not-a-theme", "--config", path}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("profile set = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "yc profile list") {
		t.Errorf("stderr = %q, want it to point at the listing", stderr.String())
	}
	if got := readConfigFile(t, path); strings.Contains(got, "definitely-not-a-theme") {
		t.Errorf("a rejected theme was written anyway:\n%s", got)
	}
}

func TestProfileUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no subcommand", []string{"profile"}},
		{"unknown subcommand", []string{"profile", "delete"}},
		{"set with no name", []string{"profile", "set"}},
		{"bad flag", []string{"profile", "list", "--nope"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(tc.args, &stdout, &stderr); code != ExitUsage {
				t.Fatalf("Run(%v) = %d, want %d", tc.args, code, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Error("a usage failure must explain itself on stderr")
			}
		})
	}
}

// An unknown theme degrades to the default palette rather than failing startup,
// but ignoring it silently makes a typo look like a broken theme system.
func TestWarnUnknownTheme(t *testing.T) {
	cfg := config.Default()

	var quiet bytes.Buffer
	warnUnknownTheme(cfg, &quiet)
	if quiet.Len() != 0 {
		t.Errorf("the default theme produced a warning: %s", quiet.String())
	}

	cfg.Features.ThemeName = ""
	quiet.Reset()
	warnUnknownTheme(cfg, &quiet)
	if quiet.Len() != 0 {
		t.Errorf("an empty theme name produced a warning: %s", quiet.String())
	}

	cfg.Features.ThemeName = "hologram"
	var loud bytes.Buffer
	warnUnknownTheme(cfg, &loud)
	for _, want := range []string{"unknown theme", "hologram", "yc profile list"} {
		if !strings.Contains(loud.String(), want) {
			t.Errorf("warning is missing %q: %s", want, loud.String())
		}
	}
}

func TestConfigPathAndUnknownSubcommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"config", "path"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("config path = %d, stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); !strings.HasSuffix(got, filepath.Join("yc", "config.toml")) {
		t.Errorf("config path = %q, want a yc config.toml", got)
	}

	for _, args := range [][]string{{"config"}, {"config", "reset"}} {
		var out, errOut bytes.Buffer
		if code := Run(args, &out, &errOut); code != ExitUsage {
			t.Errorf("Run(%v) = %d, want %d", args, code, ExitUsage)
		}
	}
}

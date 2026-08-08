package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/theme"
)

// The config and cache paths are what every command resolves before it does
// anything else, and what the smoke suite isolates with XDG_* overrides. If
// they stopped honoring those, a test run would read and write the developer's
// real credentials.
func TestDefaultPathsHonorTheXDGOverrides(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("XDG_* is not the platform convention here")
	}
	configHome := t.TempDir()
	cacheHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(configHome, "yc", "config.toml"); path != want {
		t.Errorf("DefaultPath() = %q, want %q", path, want)
	}

	cache, err := DefaultCacheDir()
	if err != nil {
		t.Fatalf("DefaultCacheDir: %v", err)
	}
	if want := filepath.Join(cacheHome, "yc"); cache != want {
		t.Errorf("DefaultCacheDir() = %q, want %q", cache, want)
	}

	// Both must be namespaced under "yc" so yc never writes into the bare
	// config or cache root.
	for name, got := range map[string]string{"config": path, "cache": cache} {
		if !strings.Contains(got, string(filepath.Separator)+"yc") {
			t.Errorf("the %s path %q is not namespaced under yc", name, got)
		}
	}
}

// Load with no explicit path falls back to the default, which is what makes
// `yc config show` and `yc doctor` agree about which file is in play.
func TestLoadWithoutAnOverrideUsesTheDefaultPath(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("XDG_* is not the platform convention here")
	}
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	cfg, err := Load(nil, Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(configHome, "yc", "config.toml")
	if cfg.Path != want {
		t.Errorf("cfg.Path = %q, want %q", cfg.Path, want)
	}
	// A missing file is the normal first experience, not an error.
	if cfg.Features.AnimationMode != Default().Features.AnimationMode {
		t.Errorf("defaults did not apply: %+v", cfg.Features)
	}
}

// An unrecognized theme name degrades to a usable palette rather than failing
// startup or handing the renderer empty colors, which would draw an invisible
// UI on the user's terminal.
func TestResolveThemeAlwaysReturnsAUsablePalette(t *testing.T) {
	cases := []struct {
		name   string
		themed string
	}{
		{"default", Default().Features.ThemeName},
		{"unknown name", "definitely-not-a-theme"},
		{"empty name", ""},
		{"mixed case preset", "NORD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Features.ThemeName = tc.themed
			palette := cfg.ResolveTheme()

			for role, value := range map[string]string{
				"background": palette.Background,
				"foreground": palette.Foreground,
				"accent":     palette.Accent,
				"muted":      palette.Muted,
				"border":     palette.Border,
				"surface":    palette.Surface,
				"warning":    palette.Warning,
				"error":      palette.Error,
				"success":    palette.Success,
			} {
				if strings.TrimSpace(value) == "" {
					t.Errorf("theme %q left the %s role empty", tc.themed, role)
				}
			}
		})
	}
}

// The "custom" palette is taken verbatim: it replaces the preset rather than
// overlaying it, which theme.ResolvePalette pins deliberately.
//
// The consequence is worth stating, because `yc profile set custom --accent X`
// writes exactly one role and leaves the other eight empty. Empty roles are not
// a crash - lipgloss treats an empty color as "no color" and the layout is
// unaffected - but they do drop out of contrast correction, so a partially
// filled custom palette is a partially themed UI rather than a recolored preset.
// A test that asserted an overlay would be asserting a feature that does not
// exist; this asserts what the code actually promises.
func TestResolveThemeTakesTheCustomPaletteVerbatim(t *testing.T) {
	base := Default()
	base.Features.ThemeName = "custom"
	base.Features.ThemeCustom = theme.Palette{Accent: "#ff00ff"}

	palette := base.ResolveTheme()
	if palette.Accent != "#ff00ff" {
		t.Errorf("accent = %q, want the custom value", palette.Accent)
	}
	if palette.Background != "" || palette.Foreground != "" {
		t.Errorf("palette = %+v; custom is verbatim, so unset roles stay unset", palette)
	}

	// A fully specified custom palette is the supported shape, and it must
	// survive resolution untouched.
	full := theme.Palette{
		Background: "#0b0b10", Foreground: "#e6e6ef", Accent: "#7aa2f7",
		Muted: "#6f7285", Border: "#2a2b3a", Surface: "#15161f",
		Warning: "#e0af68", Error: "#f7768e", Success: "#9ece6a",
	}
	base.Features.ThemeCustom = full
	if got := base.ResolveTheme(); got != full {
		t.Errorf("ResolveTheme() = %+v, want the custom palette unchanged", got)
	}
}

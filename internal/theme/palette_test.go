package theme

import (
	"math"
	"reflect"
	"testing"
)

func TestPresetNamesListsEveryBuiltInInStableOrder(t *testing.T) {
	want := []string{
		"abyss", "amber-crt", "arctic-neon", "ayu-dark", "ayu-mirage",
		"blood-moon", "btop", "bullion", "carbon", "catppuccin-frappe",
		"catppuccin-latte", "catppuccin-macchiato", "catppuccin-mocha",
		"claude", "cobalt2", "codex", "cyberpunk", "deep-ocean", "dracula",
		"emerald-noir", "everforest", "github-light", "gruvbox",
		"gruvbox-light", "horizon", "hotline", "kanagawa", "magma",
		"matrix", "midnight-ember", "mint-noir", "mono", "monokai",
		"neon-tokyo", "night-owl", "nightfox", "nord", "obsidian",
		"oceanic-next", "one-dark", "orchid", "palenight", "plasma",
		"rose-pine", "rose-pine-dawn", "rose-pine-moon", "ruby", "sapphire",
		"solarized-dark", "solarized-light", "spectre", "synthwave-84",
		"tokyo-night", "toxic", "ultraviolet", "vaporwave", "yc", "zenburn",
	}
	got := PresetNames()
	if len(got) != len(want) {
		t.Fatalf("PresetNames() = %v (%d), want %v (%d)", got, len(got), want, len(want))
	}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("PresetNames()[%d] = %q, want %q", i, got[i], name)
		}
	}
}

// Every role must be a parseable hex color: an unset or malformed field would
// reach lipgloss as an unknown color and render as the terminal's default,
// silently dropping that role's meaning instead of failing loudly.
func TestPresetPalettesFillEveryRoleWithValidHex(t *testing.T) {
	for _, name := range PresetNames() {
		palette := Presets()[name]
		if palette == (Palette{}) {
			t.Fatalf("preset %q has zero-value palette", name)
		}
		for role, color := range paletteRoles(palette) {
			if _, ok := parseHexColor(color); !ok {
				t.Fatalf("preset %q has invalid %s %q", name, role, color)
			}
		}
	}
}

// publishedSurfaceExceptions records presets whose own upstream scheme pairs
// its body text with its panel color below the text bar. Solarized Dark's
// base0 on base02 measures 4.11 by the scheme's definition, so yc keeps the
// published pairing rather than inventing a different Solarized. New presets
// must not join this list.
var publishedSurfaceExceptions = map[string]float64{
	"solarized-dark": 4.0,
}

// Body text has to clear the same contrast bar ContrastCorrectedForeground
// enforces for derived colors, on the canvas and on raised panes alike.
// Muted is deliberately excluded: several upstream schemes publish a
// low-contrast comment color, and yc keeps their identity rather than
// substituting its own.
func TestPresetForegroundsAreReadableOnBackgroundAndSurface(t *testing.T) {
	for _, name := range PresetNames() {
		palette := Presets()[name]
		foreground, _ := parseHexColor(palette.Foreground)
		for role, color := range map[string]string{
			"background": palette.Background,
			"surface":    palette.Surface,
		} {
			want := MinimumTextContrast
			if floor, ok := publishedSurfaceExceptions[name]; ok && role == "surface" {
				want = floor
			}
			behind, _ := parseHexColor(color)
			if got := contrastRatio(foreground, behind); got < want {
				t.Fatalf("preset %q foreground contrast on %s = %.2f, want >= %.2f", name, role, got, want)
			}
		}
	}
}

// The accent paints the status bar and every focus rail, so it has to be
// visible against the canvas rather than blending into it. 3.0 is the
// large-text/graphical-object bar, which is what an accent fill is.
func TestPresetAccentsStandOutFromTheirBackground(t *testing.T) {
	const minimumAccentContrast = 3.0
	for _, name := range PresetNames() {
		palette := Presets()[name]
		accent, _ := parseHexColor(palette.Accent)
		background, _ := parseHexColor(palette.Background)
		if got := contrastRatio(accent, background); got < minimumAccentContrast {
			t.Fatalf("preset %q accent contrast = %.2f, want >= %.2f", name, got, minimumAccentContrast)
		}
	}
}

func TestPresetPalettesAreDistinct(t *testing.T) {
	seen := make(map[Palette]string, len(presets))
	for _, name := range PresetNames() {
		palette := Presets()[name]
		if other, ok := seen[palette]; ok {
			t.Fatalf("presets %q and %q are the same palette", other, name)
		}
		seen[palette] = name
	}
}

func paletteRoles(palette Palette) map[string]string {
	return map[string]string{
		"background": palette.Background,
		"foreground": palette.Foreground,
		"accent":     palette.Accent,
		"muted":      palette.Muted,
		"border":     palette.Border,
		"surface":    palette.Surface,
		"warning":    palette.Warning,
		"error":      palette.Error,
		"success":    palette.Success,
	}
}

func TestDefaultPaletteMatchesDefaultPaletteName(t *testing.T) {
	if got, want := DefaultPalette(), Presets()[DefaultPaletteName]; got != want {
		t.Fatalf("DefaultPalette() = %+v, want %+v", got, want)
	}
}

// The YouTube-flavored house palette is what "yc" in the config means; it must
// stay a real preset rather than resolving to the fallback.
func TestYouTubePresetResolves(t *testing.T) {
	got, ok := ResolvePalette("yc", Palette{})
	if !ok {
		t.Fatal("ResolvePalette(\"yc\", ...) ok = false, want true")
	}
	if got == DefaultPalette() {
		t.Fatal("ResolvePalette(\"yc\", ...) returned the default palette, want the yc preset")
	}
}

func TestResolvePaletteKnownPreset(t *testing.T) {
	got, ok := ResolvePalette("Nord", Palette{})
	if !ok {
		t.Fatal("ResolvePalette(\"Nord\", ...) ok = false, want true")
	}
	if want := Presets()["nord"]; got != want {
		t.Fatalf("ResolvePalette(\"Nord\", ...) = %+v, want %+v", got, want)
	}
}

func TestResolvePaletteCustom(t *testing.T) {
	custom := Palette{Background: "#010101", Foreground: "#fefefe"}
	got, ok := ResolvePalette("custom", custom)
	if !ok || got != custom {
		t.Fatalf("ResolvePalette(\"custom\", %+v) = (%+v, %v), want (%+v, true)", custom, got, ok, custom)
	}
}

func TestResolvePaletteUnknownFallsBackToDefault(t *testing.T) {
	got, ok := ResolvePalette("not-a-theme", Palette{})
	if ok {
		t.Fatal("ResolvePalette(\"not-a-theme\", ...) ok = true, want false")
	}
	if want := DefaultPalette(); got != want {
		t.Fatalf("ResolvePalette(\"not-a-theme\", ...) = %+v, want %+v", got, want)
	}
}

func TestContrastCorrectedForegroundKeepsReadableColor(t *testing.T) {
	got := ContrastCorrectedForeground("#00d1ff", "#111018", "#f6f2ff")
	if want := "#00d1ff"; got != want {
		t.Fatalf("color = %q, want %q", got, want)
	}
}

func TestContrastCorrectedForegroundUsesFallbackForLowContrastColor(t *testing.T) {
	got := ContrastCorrectedForeground("#111111", "#111018", "#f6f2ff")
	if want := "#f6f2ff"; got != want {
		t.Fatalf("color = %q, want %q", got, want)
	}
}

func TestContrastCorrectedForegroundHandlesInvalidInput(t *testing.T) {
	got := ContrastCorrectedForeground("not-a-color", "#111018", "#f6f2ff")
	if want := "#f6f2ff"; got != want {
		t.Fatalf("color = %q, want %q", got, want)
	}
}

func TestGradientInterpolatesEndpointsAndMidpoint(t *testing.T) {
	got := Gradient("#ff8000", "#00c0ff", 3)
	want := []string{"#ff8000", "#80a080", "#00c0ff"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Gradient() = %v, want %v", got, want)
	}
}

func TestGradientDegradesSafelyForInvalidCustomColors(t *testing.T) {
	got := Gradient("accent", "#ffffff", 2)
	want := []string{"accent", "accent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Gradient() = %v, want %v", got, want)
	}
}

func TestSeamlessGradientMirrorsBothSidesAndClosesItsLoop(t *testing.T) {
	for _, steps := range []int{1, 2, 3, 8, 9} {
		colors := SeamlessGradient("#ff0000", "#0000ff", steps)
		if len(colors) != steps {
			t.Fatalf("SeamlessGradient(..., %d) length = %d, want %d", steps, len(colors), steps)
		}
		if colors[0] != colors[len(colors)-1] {
			t.Fatalf("SeamlessGradient(..., %d) endpoints = %q and %q, want a closed loop", steps, colors[0], colors[len(colors)-1])
		}
		for left, right := 0, len(colors)-1; left < right; left, right = left+1, right-1 {
			if colors[left] != colors[right] {
				t.Fatalf("SeamlessGradient(..., %d) is not mirrored at %d/%d: %q != %q", steps, left, right, colors[left], colors[right])
			}
		}
	}

	colors := SeamlessGradient("#ff0000", "#0000ff", 8)
	if colors[3] != "#0000ff" || colors[4] != "#0000ff" {
		t.Fatalf("even seamless gradient center = %q/%q, want the end color at both center cells", colors[3], colors[4])
	}
}

func TestDarkenAdjustsValidColorsAndPreservesInvalidValues(t *testing.T) {
	if got, want := Darken("#204060", 0.25), "#183048"; got != want {
		t.Fatalf("Darken() = %q, want %q", got, want)
	}
	if got, want := Darken("#204060", -1), "#204060"; got != want {
		t.Fatalf("Darken() with negative amount = %q, want %q", got, want)
	}
	if got, want := Darken("custom-background", 0.25), "custom-background"; got != want {
		t.Fatalf("Darken() invalid value = %q, want %q", got, want)
	}
}

func TestMixBlendsTowardOverlayAndPreservesInvalidValues(t *testing.T) {
	if got, want := Mix("#000000", "#ffffff", 0.5), "#808080"; got != want {
		t.Fatalf("Mix() = %q, want %q", got, want)
	}
	if got, want := Mix("#204060", "#ffffff", 0), "#204060"; got != want {
		t.Fatalf("Mix() with amount 0 = %q, want %q", got, want)
	}
	if got, want := Mix("surface", "#ffffff", 0.5), "surface"; got != want {
		t.Fatalf("Mix() invalid base = %q, want %q", got, want)
	}
}

func TestIdentityColorIsStableDistinctAndReadable(t *testing.T) {
	palette := DefaultPalette()
	backgrounds := []string{palette.Background, palette.Surface}
	alice := IdentityColor("Alice", backgrounds, palette.Foreground)
	if got := IdentityColor("alice", backgrounds, palette.Foreground); got != alice {
		t.Fatalf("case-normalized identity color = %q, want stable %q", got, alice)
	}
	bob := IdentityColor("bob", backgrounds, palette.Foreground)
	if bob == alice {
		t.Fatalf("different identities shared color %q", alice)
	}
	color, ok := parseHexColor(alice)
	if !ok {
		t.Fatalf("identity color %q is not valid hex", alice)
	}
	for _, value := range backgrounds {
		background, ok := parseHexColor(value)
		if !ok {
			t.Fatalf("test background %q is not valid hex", value)
		}
		if ratio := contrastRatio(color, background); ratio < MinimumTextContrast {
			t.Fatalf("identity color contrast against %q = %.2f, want >= %.2f", value, ratio, MinimumTextContrast)
		}
	}
	if got := IdentityColor("", backgrounds, palette.Foreground); got != palette.Foreground {
		t.Fatalf("empty identity color = %q, want fallback %q", got, palette.Foreground)
	}
}

// A YouTube channel ID is the identity yc hashes, and every preset is a canvas
// it may be drawn on, so the readable-color guarantee has to hold across the
// whole preset set rather than only the default.
func TestIdentityColorStaysReadableOnEveryPreset(t *testing.T) {
	identities := []string{"UC_x5XG1OV2P6uZZ5FSM9Ttw", "streamer", "chat-regular-42"}
	for _, name := range PresetNames() {
		palette := Presets()[name]
		backgrounds := []string{palette.Background, palette.Surface}
		for _, identity := range identities {
			color := IdentityColor(identity, backgrounds, palette.Foreground)
			parsed, ok := parseHexColor(color)
			if !ok {
				t.Fatalf("preset %q identity color %q is not valid hex", name, color)
			}
			for _, value := range backgrounds {
				behind, _ := parseHexColor(value)
				if ratio := contrastRatio(parsed, behind); ratio < MinimumTextContrast {
					t.Fatalf("preset %q identity color for %q on %q = %.2f, want >= %.2f",
						name, identity, value, ratio, MinimumTextContrast)
				}
			}
		}
	}
}

func TestReadableOnKeepsAColorThatAlreadyPasses(t *testing.T) {
	got := ReadableOn("#ffffff", "#000000", "#888888")
	if got != "#ffffff" {
		t.Fatalf("ReadableOn = %q, want the color returned untouched", got)
	}
}

// achromaticAccentExceptions records presets whose accent cannot carry a
// colored signal at all, so their role colors legitimately collapse onto one
// neutral and the status bar signals by wording alone.
//
// mono is achromatic on purpose. plasma's #6c5cff sits within a hair of the
// midpoint - pure white reaches 4.55 against it and pure black 4.61 - so
// nothing but an extreme clears MinimumTextContrast, and an extreme has no
// hue. Both are palette properties, not correction failures. A new preset
// must not join this list: pick an accent with headroom on one side.
var achromaticAccentExceptions = map[string]bool{
	"mono":   true,
	"plasma": true,
}

// A signal color must survive the correction as a signal. Every built-in
// palette paints the status bar in its accent, and the role colors that land
// on it are what the quota meter, the cadence label, and the dropped counter
// are made of: if the correction returns the same value for Success, Warning,
// and Error, the meter cannot report anything.
func TestReadableOnKeepsRoleColorsDistinctOnEveryPresetAccent(t *testing.T) {
	for _, name := range PresetNames() {
		if achromaticAccentExceptions[name] {
			continue
		}
		palette, ok := ResolvePalette(name, Palette{})
		if !ok {
			t.Fatalf("ResolvePalette(%q) not ok", name)
		}
		background := palette.Accent
		fallback := ContrastCorrectedForeground(palette.Foreground, background, palette.Background)

		roles := map[string]string{
			"success": palette.Success,
			"warning": palette.Warning,
			"error":   palette.Error,
		}
		sources := make(map[string]string, 3)
		seen := make(map[string]string, 3)
		for role, color := range roles {
			got := ReadableOn(color, background, fallback)
			corrected, valid := parseHexColor(got)
			if !valid {
				t.Fatalf("%s: ReadableOn(%s) = %q, not a color", name, role, got)
			}
			accent, _ := parseHexColor(background)
			if ratio := contrastRatio(corrected, accent); ratio < MinimumTextContrast {
				t.Fatalf("%s: %s corrected to %s, contrast %.2f against accent %s", name, role, got, ratio, background)
			}
			// A deliberately monochrome preset signals by wording alone, and
			// the correction must not invent a hue the palette withholds -
			// mono's silver warning and white error legitimately land on the
			// same neutral. Only role colors that carry chroma to begin with
			// are required to stay apart.
			if !chromatic(color) {
				continue
			}
			if other, clash := seen[got]; clash {
				t.Fatalf("%s: %s (%s) and %s (%s) both corrected to %s, so the meter cannot signal",
					name, role, color, other, sources[other], got)
			}
			seen[got] = role
			sources[role] = color
		}
	}
}

// A near-neutral must not acquire a hue on the way to legibility. Relighting a
// cream foreground far enough to clear 4.5:1 against a mid-tone accent
// concentrates its trace of warmth into a visible brown, which is why the gate
// is on chroma rather than on HSL saturation.
func TestReadableOnLeavesNearNeutralsNeutral(t *testing.T) {
	got := ReadableOn("#f2ede3", "#d97757", "#16121e")
	if got != "#16121e" {
		t.Fatalf("ReadableOn(cream) = %q, want the neutral fallback", got)
	}
}

func TestReadableOnFallsBackForInvalidInput(t *testing.T) {
	if got := ReadableOn("not-a-color", "#000000", "#abcdef"); got != "#abcdef" {
		t.Fatalf("ReadableOn = %q, want the fallback", got)
	}
}

// chromatic reports whether a color carries enough hue for ReadableOn to
// preserve, matching the gate the correction itself applies.
func chromatic(value string) bool {
	color, ok := parseHexColor(value)
	if !ok {
		return false
	}
	_, saturation, lightness := hueSaturationLightness(color)
	return (1-math.Abs(2*lightness-1))*saturation >= readableMinimumChroma
}

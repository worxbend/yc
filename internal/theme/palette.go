package theme

import (
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
)

// Palette is the nine-role color scheme every widget derives from. Values are
// hex strings ("#rrggbb" or "#rgb"); an empty or unparseable value must degrade
// the decoration rather than fail the app.
type Palette struct {
	Background string
	Foreground string
	Accent     string
	Muted      string
	Border     string
	Surface    string
	Warning    string
	Error      string
	Success    string
}

// DefaultPaletteName is the palette used when no theme_name is configured.
const DefaultPaletteName = "claude"

// MinimumTextContrast is the WCAG AA ratio every derived foreground must reach
// against the surfaces it will be drawn on.
const MinimumTextContrast = 4.5

// DefaultPalette returns the built-in default palette.
func DefaultPalette() Palette {
	palette, ok := presets[DefaultPaletteName]
	if !ok {
		panic("theme: " + DefaultPaletteName + " preset missing")
	}
	return palette
}

// ResolvePalette maps a config theme name onto a palette. The name "custom"
// returns custom. An unrecognized name returns the default palette with ok
// false, so startup can warn instead of failing.
func ResolvePalette(name string, custom Palette) (Palette, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "custom" {
		return custom, true
	}
	if palette, ok := presets[name]; ok {
		return palette, true
	}
	return DefaultPalette(), false
}

// ContrastCorrectedForeground returns foreground when it already meets
// MinimumTextContrast against background, then fallback if that passes, and
// otherwise pure black or white - whichever wins against background. An
// unparseable foreground returns fallback unchanged.
func ContrastCorrectedForeground(foreground, background, fallback string) string {
	fg, ok := parseHexColor(foreground)
	if !ok {
		return fallback
	}
	bg, ok := parseHexColor(background)
	if !ok {
		return canonicalHex(fg)
	}
	if contrastRatio(fg, bg) >= MinimumTextContrast {
		return canonicalHex(fg)
	}

	if candidate, ok := parseHexColor(fallback); ok && contrastRatio(candidate, bg) >= MinimumTextContrast {
		return canonicalHex(candidate)
	}

	white := rgb{r: 255, g: 255, b: 255}
	black := rgb{}
	if contrastRatio(black, bg) > contrastRatio(white, bg) {
		return canonicalHex(black)
	}
	return canonicalHex(white)
}

const (
	// readableMinimumChroma is how much color a value must carry before
	// preserving its hue is worth anything. Below it the value is effectively
	// a gray, has no signal to protect, and the neutral answer is the right
	// one.
	//
	// It is chroma rather than HSL saturation because saturation lies at the
	// ends of the lightness range: a cream white reports a saturation near
	// 0.37 while carrying almost no color, and relighting it to reach 4.5:1
	// concentrates that trace of warmth into a visible brown. Chroma folds
	// the lightness back in and calls the cream what it is.
	readableMinimumChroma = 0.15
	// readableLightnessStep and readableLightnessSteps bound the walk away
	// from the background. Sixteen steps of 0.05 cover the whole lightness
	// range from either end, so a hue that can be made readable at all is
	// found, and one that cannot costs sixteen cheap comparisons.
	readableLightnessStep  = 0.05
	readableLightnessSteps = 16
)

// ReadableOn returns color adjusted to meet MinimumTextContrast against
// background while keeping its hue.
//
// It answers a different question from ContrastCorrectedForeground, which asks
// "is this exact color legible here" and, when it is not, discards it for a
// neutral. That is right for body text and wrong for a signal. The status bar
// is filled with the palette's accent, and against a saturated accent every
// role color fails: Success, Warning, Error, and Muted all collapsed onto the
// one neutral, so the quota meter's three-stage escalation, the STRETCHED
// cadence label, and the dropped-message counter rendered identically to the
// ordinary text beside them on every built-in theme. A meter that cannot turn
// red is not a meter.
//
// So the hue is kept and only the lightness moves, away from the background
// until the ratio clears. Red still reads as red; it just reads. A color that
// cannot be rescued at any lightness - a gray, or a hue the background sits in
// the middle of - falls back to the neutral answer, because an illegible
// signal is worse than a legible non-signal.
func ReadableOn(color, background, fallback string) string {
	fg, ok := parseHexColor(color)
	if !ok {
		return fallback
	}
	bg, ok := parseHexColor(background)
	if !ok {
		return canonicalHex(fg)
	}
	if contrastRatio(fg, bg) >= MinimumTextContrast {
		return canonicalHex(fg)
	}

	hue, saturation, lightness := hueSaturationLightness(fg)
	if (1-math.Abs(2*lightness-1))*saturation < readableMinimumChroma {
		return ContrastCorrectedForeground(color, background, fallback)
	}

	// Walk toward whichever end of the range the background leaves room at.
	//
	// "Away from the background" is not the same as "lighter on a dark bar":
	// against a mid-tone accent, lightening cannot reach 4.5 at any lightness
	// - pure white only manages about 3.5 - while darkening reaches it
	// easily. So the direction is chosen by comparing the contrast pure black
	// and pure white would achieve, which is the same test the neutral
	// fallback uses, and the walk then stops at the first shade that clears
	// rather than going all the way to an extreme.
	//
	// Saturation is nudged up as lightness moves, because a hue washes out
	// toward either end and a washed-out warning is the thing being fixed.
	step := readableLightnessStep
	if contrastRatio(rgb{}, bg) > contrastRatio(rgb{r: 255, g: 255, b: 255}, bg) {
		step = -step
	}
	for i := 1; i <= readableLightnessSteps; i++ {
		candidate := hslColor(
			hue,
			math.Min(saturation+float64(i)*0.03, 1),
			clampUnit(lightness+float64(i)*step),
		)
		if contrastRatio(candidate, bg) >= MinimumTextContrast {
			return canonicalHex(candidate)
		}
	}
	return ContrastCorrectedForeground(color, background, fallback)
}

// Gradient returns steps colors interpolated from start to end.
//
// An unparseable endpoint yields steps copies of start rather than an error:
// a gradient is decoration, and a broken custom theme must cost the decoration
// and nothing else.
func Gradient(start, end string, steps int) []string {
	if steps <= 0 {
		return nil
	}
	from, fromOK := parseHexColor(start)
	to, toOK := parseHexColor(end)
	colors := make([]string, steps)
	if !fromOK || !toOK {
		for i := range colors {
			colors[i] = start
		}
		return colors
	}
	if steps == 1 {
		colors[0] = canonicalHex(from)
		return colors
	}
	for i := range colors {
		fraction := float64(i) / float64(steps-1)
		colors[i] = canonicalHex(rgb{
			r: interpolateComponent(from.r, to.r, fraction),
			g: interpolateComponent(from.g, to.g, fraction),
			b: interpolateComponent(from.b, to.b, fraction),
		})
	}
	return colors
}

// SeamlessGradient returns a mirrored start-end-start ramp whose first and last
// colors match, so a rotating phase shows no hard seam where it wraps.
func SeamlessGradient(start, end string, steps int) []string {
	if steps <= 0 {
		return nil
	}
	forwardLength := steps/2 + steps%2
	forward := Gradient(start, end, forwardLength)
	colors := make([]string, 0, steps)
	colors = append(colors, forward...)

	reverseStart := len(forward) - 1
	if steps%2 == 1 {
		reverseStart--
	}
	for index := reverseStart; index >= 0; index-- {
		colors = append(colors, forward[index])
	}
	return colors
}

// Darken returns color moved toward black by amount in [0,1].
func Darken(color string, amount float64) string {
	parsed, ok := parseHexColor(color)
	if !ok {
		return color
	}
	amount = clampUnit(amount)
	factor := 1 - amount
	return canonicalHex(rgb{
		r: uint8(math.Round(float64(parsed.r) * factor)),
		g: uint8(math.Round(float64(parsed.g) * factor)),
		b: uint8(math.Round(float64(parsed.b) * factor)),
	})
}

// Mix returns base blended toward overlay by amount in [0,1].
//
// It exists for tinting a surface with a chatter's identity color: at a low
// amount the result stays a background - text drawn on it keeps its contrast -
// while still carrying enough hue to read as that person's row.
func Mix(base, overlay string, amount float64) string {
	baseRGB, ok := parseHexColor(base)
	if !ok {
		return base
	}
	overlayRGB, ok := parseHexColor(overlay)
	if !ok {
		return base
	}
	amount = clampUnit(amount)
	blend := func(a, b uint8) uint8 {
		return uint8(math.Round(float64(a)*(1-amount) + float64(b)*amount))
	}
	return canonicalHex(rgb{
		r: blend(baseRGB.r, overlayRGB.r),
		g: blend(baseRGB.g, overlayRGB.g),
		b: blend(baseRGB.b, overlayRGB.b),
	})
}

// IdentityColor returns a deterministic, readable color for an identity string.
//
// YouTube supplies no author color, so this is how a chatter keeps one stable
// distinct color across sessions without any mutable per-user state: hash the
// identity, derive a hue and saturation, then pick the first candidate
// lightness that meets MinimumTextContrast against every listed background.
// Falls back to fallback when no candidate passes.
func IdentityColor(identity string, backgrounds []string, fallback string) string {
	identity = strings.ToLower(strings.TrimSpace(identity))
	if identity == "" {
		return fallback
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(identity))
	hash := hasher.Sum64()
	hue := float64(hash%360) / 360
	saturation := 0.62 + float64((hash>>12)%18)/100

	parsedBackgrounds := make([]rgb, 0, len(backgrounds))
	lightCanvas := false
	for _, value := range backgrounds {
		background, ok := parseHexColor(value)
		if !ok {
			continue
		}
		parsedBackgrounds = append(parsedBackgrounds, background)
		if relativeLuminance(background) > 0.45 {
			lightCanvas = true
		}
	}
	if len(parsedBackgrounds) == 0 {
		return fallback
	}

	// Dark canvases want a light identity color and light canvases a dark
	// one; each list walks away from the canvas until one candidate clears
	// the bar against every background it will be drawn on.
	lightnesses := []float64{0.68, 0.74, 0.80, 0.86}
	if lightCanvas {
		lightnesses = []float64{0.34, 0.28, 0.22, 0.16}
	}
	for _, lightness := range lightnesses {
		candidate := hslColor(hue, saturation, lightness)
		readable := true
		for _, background := range parsedBackgrounds {
			if contrastRatio(candidate, background) < MinimumTextContrast {
				readable = false
				break
			}
		}
		if readable {
			return canonicalHex(candidate)
		}
	}
	return fallback
}

// rgb is an 8-bit-per-channel color. Every public helper parses to this type,
// works in it, and formats back out, so no caller has to care about the input
// spelling.
type rgb struct {
	r uint8
	g uint8
	b uint8
}

func parseHexColor(value string) (rgb, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "#")
	if len(value) == 3 {
		value = string([]byte{
			value[0], value[0],
			value[1], value[1],
			value[2], value[2],
		})
	}
	if len(value) != 6 {
		return rgb{}, false
	}

	parsed, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return rgb{}, false
	}
	return rgb{
		// The input is exactly six hex digits, so parsed fits 24 bits and
		// each shifted byte is in range.
		r: uint8(parsed >> 16), //nolint:gosec
		g: uint8(parsed >> 8),  //nolint:gosec // see above

		b: uint8(parsed), //nolint:gosec // see above
	}, true
}

func canonicalHex(color rgb) string {
	return fmt.Sprintf("#%02x%02x%02x", color.r, color.g, color.b)
}

func clampUnit(amount float64) float64 {
	if amount < 0 {
		return 0
	}
	if amount > 1 {
		return 1
	}
	return amount
}

// hueSaturationLightness is hslColor's inverse, so a color can be moved along
// one axis and rebuilt without losing the other two.
func hueSaturationLightness(color rgb) (hue, saturation, lightness float64) {
	r := float64(color.r) / 255
	g := float64(color.g) / 255
	b := float64(color.b) / 255
	high := math.Max(r, math.Max(g, b))
	low := math.Min(r, math.Min(g, b))
	lightness = (high + low) / 2
	if high == low {
		return 0, 0, lightness
	}

	span := high - low
	if lightness > 0.5 {
		saturation = span / (2 - high - low)
	} else {
		saturation = span / (high + low)
	}
	switch high {
	case r:
		hue = math.Mod((g-b)/span+6, 6)
	case g:
		hue = (b-r)/span + 2
	default:
		hue = (r-g)/span + 4
	}
	return hue / 6, saturation, lightness
}

func hslColor(hue, saturation, lightness float64) rgb {
	c := (1 - math.Abs(2*lightness-1)) * saturation
	h := hue * 6
	x := c * (1 - math.Abs(math.Mod(h, 2)-1))
	var r, g, b float64
	switch {
	case h < 1:
		r, g = c, x
	case h < 2:
		r, g = x, c
	case h < 3:
		g, b = c, x
	case h < 4:
		g, b = x, c
	case h < 5:
		r, b = x, c
	default:
		r, b = c, x
	}
	m := lightness - c/2
	return rgb{
		r: uint8(math.Round((r + m) * 255)),
		g: uint8(math.Round((g + m) * 255)),
		b: uint8(math.Round((b + m) * 255)),
	}
}

func interpolateComponent(start, end uint8, fraction float64) uint8 {
	return uint8(math.Round(float64(start) + (float64(end)-float64(start))*fraction))
}

func contrastRatio(a, b rgb) float64 {
	la := relativeLuminance(a)
	lb := relativeLuminance(b)
	light := math.Max(la, lb)
	dark := math.Min(la, lb)
	return (light + 0.05) / (dark + 0.05)
}

func relativeLuminance(color rgb) float64 {
	return 0.2126*linearized(color.r) + 0.7152*linearized(color.g) + 0.0722*linearized(color.b)
}

func linearized(component uint8) float64 {
	value := float64(component) / 255
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

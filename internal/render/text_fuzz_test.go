package render

import (
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// FuzzSanitizeUserText pins the safety contract of the one function standing
// between attacker-controlled chat text and the terminal. Whatever bytes come
// in, the output must satisfy three invariants:
//
//  1. No C0 control except the newline the wrapper treats as a row break, no
//     DEL, and no C1 control - a raw U+009B is a one-byte CSI introducer on
//     some terminals, no ESC required.
//  2. No bidirectional formatting character, so one chatter cannot visually
//     reorder the rows around their own.
//  3. Nothing left for ansi.Strip to remove: stripping the sanitized output
//     must be the identity, proving no recognizable escape sequence survived
//     or was reassembled by the sanitizer's own replacements.
func FuzzSanitizeUserText(f *testing.F) {
	seeds := []string{
		"",
		"plain chat message",
		"\x1b[31;1mred\x1b[0m",
		"\x1b]0;owned\x07title",
		"\x1b]8;;http://attacker.invalid\x07link\x1b]8;;\x07",
		"\x1bP+q544e\x1b\\",
		"\u009b31mone-byte CSI",
		"\u009d0;ownedone-byte OSC",
		"bidi \u202egnihtemos\u202c order",
		"isolate \u2066right\u2069 text",
		"marks \u200eltr\u200f rtl",
		"line\none\nbreaks",
		"bell\a tab\t del\x7f",
		"lone escape \x1b at large",
		"truncated \x1b[ sequence",
		"\xff\xfe invalid utf-8 \x80",
		"emoji \U0001F468\u200d\U0001F469\u200d\U0001F467 family",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got := sanitizeUserText(input)
		for _, r := range got {
			if r == '\n' {
				continue
			}
			if unicode.IsControl(r) {
				t.Errorf("control rune %U survived in %q (input %q)", r, got, input)
			}
			if isBidiControl(r) {
				t.Errorf("bidi control %U survived in %q (input %q)", r, got, input)
			}
		}
		if stripped := ansi.Strip(got); stripped != got {
			t.Errorf("ansi.Strip is not the identity on sanitized output: %q -> %q (input %q)", got, stripped, input)
		}
		// Sanitizing twice must change nothing: a fixed point guarantees the
		// replacements themselves never manufacture new unsafe content.
		if again := sanitizeUserText(got); again != got {
			t.Errorf("sanitizeUserText is not idempotent: %q -> %q (input %q)", got, again, input)
		}
		if input == "" && got != "" {
			t.Errorf("empty input produced %q", got)
		}
		if !strings.ContainsFunc(input, isUnsafeRune) && !strings.Contains(input, "\x1b") && ansi.Strip(input) == input && got != input {
			t.Errorf("safe input was altered: %q -> %q", input, got)
		}
	})
}

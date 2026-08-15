package app

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// fuzzChromeSeeds is the shared seed corpus for the two chrome sanitizers:
// one representative per escape family, plus shapes that have bitten pane
// width math before (tabs, newlines, invalid UTF-8, ZWJ emoji).
var fuzzChromeSeeds = []string{
	"",
	"plain broadcast title",
	"\x1b[31;1mred\x1b[0m",
	"\x1b]0;owned\x07title",
	"\x1b]8;;http://attacker.invalid\x07link\x1b]8;;\x07",
	"\u009b31m one-byte CSI",
	"\u009d0;owned one-byte OSC",
	"bidi ‮gnihtemos‬ order",
	"isolate ⁦right⁩ text",
	"marks ‎ltr‏ rtl",
	"tab\there newline\nthere",
	"bell\a del\x7f",
	"\xff\xfe invalid utf-8 \x80",
	"emoji \U0001F468‍\U0001F469‍\U0001F467 family",
}

// FuzzFlattenControlRunes pins the guard every app-drawn line relies on: the
// output must fit a single line (no newline survives), carry no control
// character of any range, and hold no bidirectional override. Safe input must
// pass through untouched, and the function must be a fixed point so its own
// replacements can never manufacture new unsafe content.
func FuzzFlattenControlRunes(f *testing.F) {
	for _, seed := range fuzzChromeSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got := flattenControlRunes(input)
		for _, r := range got {
			if unicode.IsControl(r) {
				t.Errorf("control rune %U survived in %q (input %q)", r, got, input)
			}
			if isBidiOverride(r) {
				t.Errorf("bidi control %U survived in %q (input %q)", r, got, input)
			}
		}
		if again := flattenControlRunes(got); again != got {
			t.Errorf("not idempotent: %q -> %q (input %q)", got, again, input)
		}
		if !strings.ContainsFunc(input, isUnsafeLineRune) && got != input {
			t.Errorf("safe input was altered: %q -> %q", input, got)
		}
	})
}

// FuzzSanitizeContextValue pins the tab bar's sanitizer to the same contract,
// with its own two twists: the value is trimmed, and controls become a
// visible replacement character instead of a space.
func FuzzSanitizeContextValue(f *testing.F) {
	for _, seed := range fuzzChromeSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got := sanitizeContextValue(input)
		for _, r := range got {
			if unicode.IsControl(r) {
				t.Errorf("control rune %U survived in %q (input %q)", r, got, input)
			}
			if isBidiOverride(r) {
				t.Errorf("bidi control %U survived in %q (input %q)", r, got, input)
			}
		}
		if again := sanitizeContextValue(got); again != got {
			t.Errorf("not idempotent: %q -> %q (input %q)", got, again, input)
		}
		// strings.Map rewrites invalid UTF-8 to U+FFFD as a side effect, so
		// the pass-through guarantee only holds for well-formed input. It is
		// checked on the whole input, not the trimmed value: a control inside
		// the surrounding whitespace is still replaced, not trimmed away.
		if utf8.ValidString(input) && !strings.ContainsFunc(input, isUnsafeLineRune) && got != strings.TrimSpace(input) {
			t.Errorf("safe input was altered beyond trimming: %q -> %q", input, got)
		}
	})
}

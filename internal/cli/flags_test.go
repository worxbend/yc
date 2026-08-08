package cli

import (
	"strings"
	"testing"
)

// "Not supplied" and "supplied as empty" mean different things at this layer:
// an absent flag must leave the environment or config value in place, and an
// empty one is a mistake rather than a way to clear a setting.
func TestOptionalTextFlagDistinguishesAbsentFromEmpty(t *testing.T) {
	var flag optionalTextFlag
	if flag.set {
		t.Error("a fresh flag must not report as set")
	}
	if got := flag.String(); got != "" {
		t.Errorf("String() = %q, want empty", got)
	}

	if err := flag.Set("  nord  "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !flag.set || flag.value != "nord" {
		t.Errorf("flag = %+v, want a trimmed, set value", flag)
	}
	if got := flag.String(); got != "nord" {
		t.Errorf("String() = %q, want %q", got, "nord")
	}

	if err := flag.Set("   "); err == nil {
		t.Error("an all-whitespace value must be rejected, not silently accepted as a clear")
	}

	var nilFlag *optionalTextFlag
	if got := nilFlag.String(); got != "" {
		t.Errorf("nil String() = %q, want empty", got)
	}
}

func TestOptionalBoolFlagIsTriState(t *testing.T) {
	var flag optionalBoolFlag
	if flag.set || flag.String() != "" {
		t.Errorf("unset flag = %+v with String %q, want absent", flag, flag.String())
	}
	if !flag.IsBoolFlag() {
		t.Error("the flag package must be allowed to accept the bare spelling")
	}

	for _, tc := range []struct {
		in   string
		want bool
	}{{"true", true}, {"1", true}, {"  false  ", false}, {"0", false}} {
		var parsed optionalBoolFlag
		if err := parsed.Set(tc.in); err != nil {
			t.Fatalf("Set(%q): %v", tc.in, err)
		}
		if !parsed.set || parsed.value != tc.want {
			t.Errorf("Set(%q) = %+v, want value %v and set", tc.in, parsed, tc.want)
		}
	}

	var bad optionalBoolFlag
	if err := bad.Set("maybe"); err == nil {
		t.Error("an unparseable boolean must be a usage error")
	}

	var explicitFalse optionalBoolFlag
	_ = explicitFalse.Set("false")
	if got := explicitFalse.String(); got != "false" {
		t.Errorf("String() = %q, want %q so --flag=false is distinguishable from absent", got, "false")
	}

	var nilFlag *optionalBoolFlag
	if got := nilFlag.String(); got != "" {
		t.Errorf("nil String() = %q, want empty", got)
	}
}

// An unknown mode must be a usage error naming the alternatives, not a silent
// fallback discovered three screens later.
func TestEnumFlagRejectsUnknownValuesAndNamesTheAlternatives(t *testing.T) {
	flag := newEnumFlag("layout", layoutModes)
	if err := flag.Set("  GROUPED "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if flag.value != "grouped" || !flag.set {
		t.Errorf("flag = %+v, want a lowercased, set value", flag)
	}
	if got := flag.String(); got != "grouped" {
		t.Errorf("String() = %q, want %q", got, "grouped")
	}

	err := flag.Set("waterfall")
	if err == nil {
		t.Fatal("an unknown layout must be rejected")
	}
	for _, want := range layoutModes {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the accepted mode %q", err, want)
		}
	}
	// A rejected value must not have overwritten the accepted one.
	if flag.value != "grouped" {
		t.Errorf("value = %q; a rejected Set must leave the flag untouched", flag.value)
	}

	var nilFlag *enumFlag
	if got := nilFlag.String(); got != "" {
		t.Errorf("nil String() = %q, want empty", got)
	}
}

// Every enum vocabulary is shared by the flags, the setup wizard, and
// validation, so an accepted value on one surface is accepted on all of them.
func TestEveryEnumVocabularyIsAcceptedByItsFlagAndBySetupValidation(t *testing.T) {
	vocabularies := map[string][]string{
		"avatar mode":    avatarModes,
		"animation mode": animationModes,
		"layout":         layoutModes,
		"badge mode":     badgeModes,
	}
	for name, allowed := range vocabularies {
		if len(allowed) == 0 {
			t.Errorf("%s has no accepted values", name)
			continue
		}
		for _, value := range allowed {
			flag := newEnumFlag(name, allowed)
			if err := flag.Set(strings.ToUpper(value)); err != nil {
				t.Errorf("%s flag rejected its own vocabulary entry %q: %v", name, value, err)
			}
			if got, err := normalizeSetupEnum(name, strings.ToUpper(value), allowed); err != nil || got != value {
				t.Errorf("setup normalization of %s %q = (%q, %v), want (%q, nil)", name, value, got, err, value)
			}
		}
		if _, err := normalizeSetupEnum(name, "definitely-not-a-mode", allowed); err == nil {
			t.Errorf("%s setup validation accepted an unusable value", name)
		}
	}
}

func TestStringIn(t *testing.T) {
	if !stringIn("b", []string{"a", "b", "c"}) {
		t.Error("stringIn missed a present value")
	}
	if stringIn("d", []string{"a", "b", "c"}) {
		t.Error("stringIn matched an absent value")
	}
	if stringIn("a", nil) {
		t.Error("stringIn matched against an empty set")
	}
}

// `yc login --help` must print the long-form explanation and exit zero rather
// than erroring out, wherever the caller put the flag.
func TestHasHelpArg(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--config", "x"}, false},
		{[]string{"-h"}, true},
		{[]string{"--help"}, true},
		{[]string{"help"}, true},
		{[]string{"--config", "x", "--help"}, true},
		{[]string{"--helpful"}, false},
	}
	for _, tc := range cases {
		if got := hasHelpArg(tc.args); got != tc.want {
			t.Errorf("hasHelpArg(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestSetIfNonEmpty(t *testing.T) {
	value := "original"
	setIfNonEmpty(&value, "   ")
	if value != "original" {
		t.Errorf("value = %q; whitespace must not overwrite", value)
	}
	setIfNonEmpty(&value, "#ff00ff")
	if value != "#ff00ff" {
		t.Errorf("value = %q, want the assignment to land", value)
	}
}

// Both spellings of every chat flag bind to one accumulating list, and a value
// is never stripped: a chat may be a URL or an @handle, and the leading "@" is
// meaningful.
func TestChatFlagsPreserveTargetSyntax(t *testing.T) {
	var flags chatFlags
	if got := flags.String(); got != "" {
		t.Errorf("empty String() = %q", got)
	}
	for _, value := range []string{
		"dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=abc,https://youtu.be/def",
		"@handle",
		"UCabcdefghijklmnopqrstuv",
	} {
		if err := flags.Set(value); err != nil {
			t.Fatalf("Set(%q): %v", value, err)
		}
	}
	want := "dQw4w9WgXcQ,https://www.youtube.com/watch?v=abc,https://youtu.be/def,@handle,UCabcdefghijklmnopqrstuv"
	if got := flags.String(); got != want {
		t.Errorf("chats = %q, want %q", got, want)
	}

	var nilFlags *chatFlags
	if got := nilFlags.String(); got != "" {
		t.Errorf("nil String() = %q, want empty", got)
	}
}

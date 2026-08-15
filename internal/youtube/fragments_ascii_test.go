package youtube

import (
	"reflect"
	"strings"
	"testing"
)

// The ASCII fast path exists only as an optimization; its entire contract is
// producing byte-for-byte the same fragments the grapheme-segmented path
// produces. This test pins that equivalence over every token kind, boundary
// rule, and pathological shape the splitter distinguishes.
func TestSplitFragmentsASCIIFastPathMatchesSegmenter(t *testing.T) {
	inputs := []string{
		"plain ascii chat line",
		"@you did you catch that play",
		"hello @mod.name-1 and @other_user!",
		"see https://example.com/a?b=c and www.example.org now",
		"custom :_channelemoji: and standard :smile: chips",
		"colon: not a shortcode, ::empty:: maybe",
		"email-like not@mention because mid-word",
		"Hurl and Wurl start with url letters but are words",
		"(@paren) \"@quote\" ,@comma <h@ttp",
		"trailing spaces and\ttabs\tinside",
		"\r\nleading CRLF and mid\r\nline",
		"@",
		":",
		"h",
		strings.Repeat("a very long body that has to wrap many times ", 8),
	}
	for _, input := range inputs {
		if !isASCIIOnly(input) {
			t.Fatalf("corpus entry is not ASCII: %q", input)
		}
		fast := splitFragmentsASCII(input)
		slow := splitFragmentsUnicode(input)
		if !reflect.DeepEqual(fast, slow) {
			t.Errorf("paths diverge for %q:\n fast = %#v\n slow = %#v", input, fast, slow)
		}
	}
}

func BenchmarkSplitFragmentsASCII(b *testing.B) {
	text := "hey @someone check https://example.com and :smile: this plain tail of a message"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = SplitFragments(text)
	}
}

func BenchmarkSplitFragmentsUnicode(b *testing.B) {
	text := "hey @someone 😀 check https://example.com and :smile: 日本語 tail of a message"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = SplitFragments(text)
	}
}

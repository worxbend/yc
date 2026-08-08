package youtube

import (
	"strings"
	"testing"
)

func TestSplitFragments(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		want  []MessageFragment
		plain string
	}{
		{
			name: "plain text",
			text: "hello chat",
			want: []MessageFragment{{Type: FragmentText, Text: "hello chat"}},
		},
		{
			name: "mention at a word boundary",
			text: "hey @Streamer nice",
			want: []MessageFragment{
				{Type: FragmentText, Text: "hey "},
				{Type: FragmentMention, Text: "@Streamer"},
				{Type: FragmentText, Text: " nice"},
			},
		},
		{
			name: "mention leading the message",
			text: "@Streamer hi",
			want: []MessageFragment{
				{Type: FragmentMention, Text: "@Streamer"},
				{Type: FragmentText, Text: " hi"},
			},
		},
		{
			name: "an address inside a word is not a mention",
			text: "mail me@example.com",
			want: []MessageFragment{{Type: FragmentText, Text: "mail me@example.com"}},
		},
		{
			name: "custom channel emoji shortcode",
			text: "nice :_wave: work",
			want: []MessageFragment{
				{Type: FragmentText, Text: "nice "},
				{Type: FragmentShortcode, Text: ":_wave:"},
				{Type: FragmentText, Text: " work"},
			},
		},
		{
			name: "standard shortcode flush against text",
			text: "wow:tada:!",
			want: []MessageFragment{
				{Type: FragmentText, Text: "wow"},
				{Type: FragmentShortcode, Text: ":tada:"},
				{Type: FragmentText, Text: "!"},
			},
		},
		{
			name: "a bare colon is text",
			text: "time: 12:00",
			want: []MessageFragment{{Type: FragmentText, Text: "time: 12:00"}},
		},
		{
			name: "https url",
			text: "see https://example.com/a?b=c now",
			want: []MessageFragment{
				{Type: FragmentText, Text: "see "},
				{Type: FragmentURL, Text: "https://example.com/a?b=c"},
				{Type: FragmentText, Text: " now"},
			},
		},
		{
			name: "bare www host",
			text: "www.example.com",
			want: []MessageFragment{{Type: FragmentURL, Text: "www.example.com"}},
		},
		{
			name: "unicode emoji is its own fragment",
			text: "yes 🎉 ok",
			want: []MessageFragment{
				{Type: FragmentText, Text: "yes "},
				{Type: FragmentEmoji, Text: "🎉"},
				{Type: FragmentText, Text: " ok"},
			},
		},
		{
			name: "a zwj sequence stays one fragment",
			text: "\U0001F468\u200d\U0001F469\u200d\U0001F467",
			want: []MessageFragment{{Type: FragmentEmoji, Text: "\U0001F468\u200d\U0001F469\u200d\U0001F467"}},
		},
		{
			name: "a flag stays one fragment",
			text: "🇬🇧",
			want: []MessageFragment{{Type: FragmentEmoji, Text: "🇬🇧"}},
		},
		{
			name: "a keycap stays one fragment",
			text: "1\ufe0f⃣",
			want: []MessageFragment{{Type: FragmentEmoji, Text: "1\ufe0f⃣"}},
		},
		{
			name: "everything at once",
			text: "hey @Streamer :_wave: 🎉 https://example.com",
			want: []MessageFragment{
				{Type: FragmentText, Text: "hey "},
				{Type: FragmentMention, Text: "@Streamer"},
				{Type: FragmentText, Text: " "},
				{Type: FragmentShortcode, Text: ":_wave:"},
				{Type: FragmentText, Text: " "},
				{Type: FragmentEmoji, Text: "🎉"},
				{Type: FragmentText, Text: " "},
				{Type: FragmentURL, Text: "https://example.com"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := SplitFragments(test.text)
			if len(got) != len(test.want) {
				t.Fatalf("fragments = %#v, want %#v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("fragment %d = %#v, want %#v", i, got[i], test.want[i])
				}
			}
		})
	}
}

func TestSplitFragmentsIsLossless(t *testing.T) {
	// Concatenating the fragments must reproduce the message exactly. A
	// renderer that drops a byte here drops it from someone's chat.
	texts := []string{
		"",
		"plain",
		"hey @a :_b: 🎉 https://c.example/d",
		"::",
		":_:",
		"@",
		"@@name",
		"🎉🎉🎉",
		"mixed 🇬🇧 flags 🇩🇪 and 1\ufe0f⃣ keycaps",
		"trailing @",
	}
	for _, text := range texts {
		var rebuilt strings.Builder
		for _, fragment := range SplitFragments(text) {
			rebuilt.WriteString(fragment.Text)
		}
		if rebuilt.String() != text {
			t.Fatalf("SplitFragments(%q) rebuilt as %q", text, rebuilt.String())
		}
	}
}

func TestSplitFragmentsEmptyTextHasNoFragments(t *testing.T) {
	if got := SplitFragments(""); got != nil {
		t.Fatalf("SplitFragments(\"\") = %#v, want nil", got)
	}
}

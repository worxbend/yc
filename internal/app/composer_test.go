package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

func testComposerState() composerState {
	return composerState{
		Palette:       theme.DefaultPalette(),
		AnimationMode: animation.ModeFast,
		Now:           time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC),
		HasChat:       true,
		TargetLabel:   "Launch Day Stream",
	}
}

func TestComposerHasExactDimensionsInEveryForm(t *testing.T) {
	st := testComposerState()
	st.Focused = true
	st.Draft = "hello chat"
	cases := []struct {
		name          string
		width, height int
		framed        bool
	}{
		{name: "framed", width: 80, height: 4, framed: true},
		{name: "framed with reply", width: 80, height: 5, framed: true},
		{name: "short framed", width: 80, height: 3, framed: true},
		{name: "plain", width: 80, height: 3, framed: false},
		{name: "very narrow", width: 6, height: 3, framed: true},
		{name: "single row", width: 40, height: 1, framed: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			lines := plainLines(renderComposer(test.width, test.height, test.framed, st))
			if len(lines) != test.height {
				t.Fatalf("rendered %d rows, want %d", len(lines), test.height)
			}
			for i, line := range lines {
				if got := ansi.StringWidth(line); got != test.width {
					t.Fatalf("row %d is %d cells, want %d (%q)", i, got, test.width, line)
				}
			}
		})
	}
}

func TestComposerPlaceholderPointsSomewhereUseful(t *testing.T) {
	st := testComposerState()
	st.HasChat = false
	if got := composerPlaceholder(st, ""); !strings.Contains(got, "/chats") {
		// With nothing open the composer is still usable - the picker is
		// reachable from it - so the prompt must point there rather than
		// naming a chat that does not exist.
		t.Fatalf("empty-session placeholder = %q", got)
	}
	st.HasChat = true
	if got := composerPlaceholder(st, ""); !strings.Contains(got, "Launch Day Stream") {
		t.Fatalf("placeholder does not name the target: %q", got)
	}
}

// yc degrades by capability rather than by hiding controls: an API-key-only
// session can read but not send, and the composer has to say which.
func TestComposerReportsWhySendingIsUnavailable(t *testing.T) {
	st := testComposerState()
	st.DisabledReason = "read-only (API key); run `yc login` to send"
	label, color := composerStateLabel(st)
	if label != st.DisabledReason {
		t.Fatalf("state label = %q, want the disabled reason", label)
	}
	if color != st.Palette.Error {
		t.Fatalf("disabled reason is not rendered as an error color")
	}
	rendered := ansi.Strip(renderComposer(90, 4, true, st))
	if !strings.Contains(rendered, "yc login") {
		t.Fatalf("the reason never reaches the surface:\n%s", rendered)
	}
}

// The cursor must be solid on a static frame: a blank caret reads as a
// rendering bug rather than as "animation is off".
func TestComposerCursorIsSolidWithoutAnimation(t *testing.T) {
	st := testComposerState()
	st.Focused = true
	st.AnimationMode = animation.ModeOff
	if !composerCursorVisible(st) {
		t.Fatal("animation=off produced an invisible cursor")
	}
	st.Focused = false
	if composerCursorVisible(st) {
		t.Fatal("an unfocused composer drew a cursor")
	}
}

// The blink must not change the row's geometry.
func TestComposerCursorGlyphIsAlwaysOneCell(t *testing.T) {
	st := testComposerState()
	st.Focused = true
	for _, mode := range []animation.Mode{animation.ModeOff, animation.ModeFast, animation.ModeReduced} {
		st.AnimationMode = mode
		for _, offset := range []time.Duration{0, 400 * time.Millisecond, 700 * time.Millisecond} {
			st.Now = time.Unix(0, 0).Add(offset)
			if got := ansi.StringWidth(composerCursorGlyph(st)); got != 1 {
				t.Fatalf("mode %v offset %v: cursor is %d cells", mode, offset, got)
			}
		}
	}
}

// The reply row says "Replying to" because that is what the user meant. The
// wire form is an "@Name " prefix, since YouTube live chat has no parent-message
// field - a fact the docs state plainly rather than implying a thread.
func TestComposerShowsReplyContext(t *testing.T) {
	st := testComposerState()
	st.Reply = &composerReplyContext{MessageID: "m1", Author: "alice", Text: "the original\nmessage"}
	rendered := ansi.Strip(renderComposer(90, 5, true, st))
	if !strings.Contains(rendered, "Replying to alice") {
		t.Fatalf("reply context missing:\n%s", rendered)
	}
	if strings.Contains(rendered, "\noriginal") {
		t.Fatal("the quoted parent text was not collapsed onto one row")
	}
}

// A credential pasted into a message must not reappear in the reply row.
func TestComposerRedactsTheQuotedParent(t *testing.T) {
	st := testComposerState()
	st.Reply = &composerReplyContext{Author: "alice", Text: "token: access_token=test-not-a-real-token"}
	rendered := ansi.Strip(renderComposer(120, 5, true, st))
	if strings.Contains(rendered, "test-not-a-real-token") {
		t.Fatalf("the composer echoed a credential-shaped value:\n%s", rendered)
	}
}

// The remaining-character warning appears only as the 200-character cap comes
// into view, so it reads as a warning rather than as an always-on counter.
func TestComposerCharacterBudgetWarnsLate(t *testing.T) {
	if _, show := composerRemainingRunes("short"); show {
		t.Fatal("a short draft showed the character budget")
	}
	long := strings.Repeat("x", youtube.MaxChatMessageRunes-10)
	remaining, show := composerRemainingRunes(long)
	if !show || remaining != 10 {
		t.Fatalf("remaining = %d, show = %v", remaining, show)
	}
}

func TestComposerSendStates(t *testing.T) {
	st := testComposerState()
	tests := map[composerSendState]string{
		composerSendIdle:        "ready",
		composerSendQueued:      "queued",
		composerSendSending:     "sending",
		composerSendSucceeded:   "sent",
		composerSendFailed:      "failed",
		composerSendRateLimited: "rate limited",
	}
	for state, want := range tests {
		st.SendState = state
		if got, _ := composerStateLabel(st); got != want {
			t.Errorf("state %q rendered %q, want %q", state, got, want)
		}
	}
}

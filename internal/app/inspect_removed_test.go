package app

import (
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/youtube"
)

// "yc never reprints what was removed" is a whole-app invariant, not a
// per-widget one. The row renderer draws "[message deleted]", the activity
// column names the moderator without quoting the words, and moderation itself
// keeps the text only in an unexported rollback record. Inspect is the panel
// that deliberately shows raw-ish values, which makes it the one place where
// the invariant has to be enforced rather than assumed - and the one place a
// moderator would reach for immediately after removing something.
//
// The renderer already refuses to reprint a Deleted message's Text without
// assuming somebody upstream blanked it. These pin the same refusal here.

func TestInspectNeverReprintsARemovedBody(t *testing.T) {
	const secret = "unrepeatable abusive phrasing"

	for _, kind := range []youtube.EventKind{
		youtube.EventKindText,
		youtube.EventKindSuperChat,
		youtube.EventKindMessageDeleted,
		youtube.EventKindMessageRetracted,
		youtube.EventKindTombstone,
	} {
		st := testInspectState()
		st.Message.Kind = kind
		st.Message.Type = youtube.MessageTypeForKind(kind)
		st.Message.Deleted = true
		st.Message.Text = secret
		st.Message.Fragments = []youtube.MessageFragment{
			{Type: youtube.FragmentText, Text: secret},
		}

		for _, width := range []int{40, 80, 200} {
			rendered := strings.Join(plainLines(renderInspect(width, dockedPane{height: 14, contentHeight: 12, framed: true}, st)), "\n")
			if strings.Contains(rendered, secret) {
				t.Fatalf("%s at width %d reprinted the removed body:\n%s", kind, width, rendered)
			}
			// Even a prefix of it is a leak: the panel truncates, and half a
			// slur is still a slur on a streamed terminal.
			for _, word := range strings.Fields(secret) {
				if len(word) < 5 {
					continue
				}
				if strings.Contains(rendered, word) {
					t.Fatalf("%s at width %d leaked the word %q from the removed body:\n%s",
						kind, width, word, rendered)
				}
			}
			// The panel stays honest about what it is withholding rather than
			// quietly dropping the field, which would read as "no text".
			if !strings.Contains(rendered, "text: removed") {
				t.Fatalf("%s at width %d does not say the body was removed:\n%s", kind, width, rendered)
			}
			// The deleted flag lives on the message line, which truncates like
			// every other line, so it is only asserted where there is room.
			if width >= 200 && !strings.Contains(rendered, "deleted=true") {
				t.Fatalf("%s at width %d does not report the message as deleted:\n%s", kind, width, rendered)
			}
		}
	}
}

// The withholding is conditional on removal, not on the event kind: an ordinary
// message that is still live must still show its body, or inspect stops being
// able to answer "why did that row render like that".
func TestInspectStillShowsTheBodyOfALiveMessage(t *testing.T) {
	st := testInspectState()
	st.Message.Deleted = false
	st.Message.Text = "a perfectly ordinary line"
	st.Message.Fragments = []youtube.MessageFragment{
		{Type: youtube.FragmentText, Text: "a perfectly ordinary line"},
	}

	rendered := strings.Join(plainLines(renderInspect(200, dockedPane{height: 14, contentHeight: 12, framed: true}, st)), "\n")
	if !strings.Contains(rendered, "a perfectly ordinary line") {
		t.Fatalf("inspect withheld a live message's body:\n%s", rendered)
	}
	if strings.Contains(rendered, "text: removed") {
		t.Fatalf("inspect reported a live message as removed:\n%s", rendered)
	}
}

// The end-to-end path: select a row, moderate it, and press K. The row the
// moderator is looking at is the row whose text must not come back.
func TestInspectingAJustModeratedRowShowsNothingItRemoved(t *testing.T) {
	model, _ := moderationModel(t)
	target := model.activeChatState().messages[1]
	secret := target.Text
	if strings.TrimSpace(secret) == "" {
		t.Fatal("the fixture message has no body to remove")
	}

	model = armModeration(t, model, moderationDeleteRune)
	model, cmd := commitArmed(t, model, moderationDeleteRune)
	model = runModerationCmd(t, model, cmd)

	model.activeChatState().inspectOpen = true
	model.activeChatState().selected = replyContextFromMessage(target)

	for _, size := range []struct{ width, height int }{{62, 20}, {100, 30}, {130, 36}} {
		model.width, model.height = size.width, size.height
		frame := strings.Join(frameLines(t, model), "\n")
		if strings.Contains(frame, secret) {
			t.Fatalf("%dx%d: inspecting the deleted row reprinted %q:\n%s",
				size.width, size.height, secret, frame)
		}
	}
}

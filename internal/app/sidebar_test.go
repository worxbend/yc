package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

func testSidebarState() sidebarState {
	return sidebarState{
		Palette: theme.DefaultPalette(),
		Entries: []sidebarEntry{
			{Label: "Launch Day Stream", Status: youtube.ConnectionConnected, Live: true, Active: true},
			{Label: "A Very Long Broadcast Title That Will Not Fit", Status: youtube.ConnectionReconnecting, Unread: 12},
			{Label: "Ended Broadcast", Status: youtube.ConnectionClosed, Filtered: true},
		},
	}
}

func TestSidebarHasExactDimensions(t *testing.T) {
	st := testSidebarState()
	for _, width := range []int{12, 14, 18, 24, 40} {
		lines := plainLines(renderSidebar(width, 6, st))
		if len(lines) != 8 { // title row + 6 content rows + bottom border
			t.Fatalf("width %d rendered %d rows", width, len(lines))
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got != width {
				t.Fatalf("width %d row %d is %d cells (%q)", width, i, got, line)
			}
		}
	}
}

// The collapsed form drops the title before the unread count: knowing that
// something unread arrived matters more in a narrow column than knowing which
// broadcast it was.
func TestSidebarCollapsesToMarkersWhenNarrow(t *testing.T) {
	st := testSidebarState()
	wide := strings.Join(plainLines(renderSidebar(30, 4, st)), "\n")
	if !strings.Contains(wide, "Launch Day Stream") {
		t.Fatalf("wide sidebar lost the title:\n%s", wide)
	}
	narrow := strings.Join(plainLines(renderSidebar(sidebarCollapsedWidth, 4, st)), "\n")
	if strings.Contains(narrow, "Launch Day") {
		t.Fatalf("collapsed sidebar kept the title:\n%s", narrow)
	}
	if !strings.Contains(narrow, "12") {
		t.Fatalf("collapsed sidebar lost the unread count:\n%s", narrow)
	}
}

// The close affordance is drawn only on the highlighted row and only while the
// sidebar has focus, so it reads as "x closes this" rather than as decoration
// hanging off every chat.
func TestSidebarCloseAffordanceFollowsFocus(t *testing.T) {
	st := testSidebarState()
	unfocused := strings.Join(plainLines(renderSidebar(30, 4, st)), "\n")
	if strings.Contains(unfocused, "✕") {
		t.Fatalf("an unfocused sidebar drew the close affordance:\n%s", unfocused)
	}
	st.Focused, st.Selected = true, 1
	focused := plainLines(renderSidebar(30, 4, st))
	if !strings.Contains(focused[2], "✕") {
		t.Fatalf("the highlighted row has no close affordance: %q", focused[2])
	}
	if strings.Contains(focused[1], "✕") {
		t.Fatalf("a non-highlighted row drew the close affordance: %q", focused[1])
	}
}

func TestSidebarWithNothingOpenSaysSo(t *testing.T) {
	st := testSidebarState()
	st.Entries = nil
	rendered := strings.Join(plainLines(renderSidebar(20, 3, st)), "\n")
	if !strings.Contains(rendered, "(none open)") {
		t.Fatalf("empty sidebar is blank:\n%s", rendered)
	}
}

// Every connection indicator must be exactly one cell, or the column of titles
// below them stops aligning.
func TestConnectionIndicatorsAreOneCell(t *testing.T) {
	palette := theme.DefaultPalette()
	statuses := []youtube.ConnectionStatus{
		youtube.ConnectionConnecting, youtube.ConnectionConnected, youtube.ConnectionReconnecting,
		youtube.ConnectionDisconnected, youtube.ConnectionClosed, youtube.ConnectionFailed,
		youtube.ConnectionPaused, youtube.ConnectionStatus("something-new"),
	}
	for _, status := range statuses {
		glyph, color := connectionIndicator(palette, status)
		if got := ansi.StringWidth(glyph); got != 1 {
			t.Errorf("status %q glyph %q is %d cells", status, glyph, got)
		}
		if color == "" {
			t.Errorf("status %q has no color", status)
		}
	}
}

func TestPaneWidthClampsAgainstTheCompetingPane(t *testing.T) {
	// A pane can never take so much that chat drops below its floor.
	got := clampPaneWidth(60, activityMinSize, activityMaxSize, 100, 20)
	if want := 100 - 20 - minChatWidthAfterPanes; got != want {
		t.Fatalf("clampPaneWidth = %d, want %d", got, want)
	}
	// A terminal that cannot afford even the minimum leaves the pane at its
	// floor and lets the layout decide to drop it.
	if got := clampPaneWidth(30, activityMinSize, activityMaxSize, 40, 0); got != activityMinSize {
		t.Fatalf("clampPaneWidth on a tiny terminal = %d, want %d", got, activityMinSize)
	}
}

func TestPaneVisibilityRules(t *testing.T) {
	// Auto shows the sidebar only once a second chat earns its width.
	if sidebarVisibleFor(paneVisibilityAuto, 120, 20, 1) {
		t.Error("auto showed the sidebar with one chat open")
	}
	if !sidebarVisibleFor(paneVisibilityAuto, 120, 20, 2) {
		t.Error("auto hid the sidebar with two chats open")
	}
	// An explicit show cannot conjure space that is not there.
	if sidebarVisibleFor(paneVisibilityShown, 40, 20, 3) {
		t.Error("an explicit show overrode the width floor")
	}
	// The activity column does not need a second chat, only room.
	if !activityVisibleFor(paneVisibilityAuto, activityMinTerminalWidth, 20) {
		t.Error("auto hid the activity column on a wide terminal")
	}
	if activityVisibleFor(paneVisibilityAuto, activityMinTerminalWidth-1, 20) {
		t.Error("auto showed the activity column below its threshold")
	}
	// A deliberate show works below the auto threshold, given room for chat.
	if !activityVisibleFor(paneVisibilityShown, minChatWidthAfterPanes+activityMinSize, 20) {
		t.Error("an explicit show was refused with room available")
	}
}

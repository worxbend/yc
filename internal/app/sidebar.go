package app

import (
	"fmt"
	"strings"

	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// The two side columns - the chat sidebar on the left and the activity log on
// the right - both borrow width from chat, which is the pane that matters most.
// So both are sized responsively by default and both can be overridden: the
// defaults suit a terminal nobody has thought about, and the overrides exist
// because a streamer with a fixed layout has usually thought about it a lot.

const (
	// sidebarMinWidth is the terminal width at which the sidebar starts
	// paying for itself.
	sidebarMinWidth   = 72
	sidebarNormalSize = 18
	sidebarWideSize   = 24
	sidebarWideAt     = 112

	activityMinTerminalWidth = 100
	activityNormalSize       = 28
	activityWideSize         = 34
	activityWideAt           = 140

	// Side panes are clamped rather than rejected: a configured or resized
	// width the terminal cannot afford degrades to the nearest usable value
	// instead of producing a layout with no chat column in it.
	sidebarMinSize  = 12
	sidebarMaxSize  = 40
	activityMinSize = 16
	activityMaxSize = 60

	// paneResizeStep is how far one keypress moves a pane edge. Two cells is
	// small enough to land on a specific width without feeling slow.
	paneResizeStep = 2

	// minChatWidthAfterPanes is the chat column the side panes must leave
	// behind. Below this, messages wrap into an unreadable ribbon, so a
	// resize stops here rather than honoring the request.
	minChatWidthAfterPanes = 32

	// sidebarCollapsedWidth is where the sidebar stops having room for a
	// title and shows only the marker, the connection indicator, and the
	// unread count.
	sidebarCollapsedWidth = 16

	// sidebarCloseAffordance marks the highlighted row as closable with x
	// (keyboard) or a click on the glyph itself (mouse).
	sidebarCloseAffordance = " ✕"
)

// sidebarEntry is one row of the chat sidebar. It is a flat display value, so
// the sidebar can be rendered and golden-tested without per-chat model state.
type sidebarEntry struct {
	// Label is the broadcast title, or whatever the user typed while the
	// target is still unresolved. yc never labels a chat by its ID.
	Label    string
	Status   youtube.ConnectionStatus
	Unread   int
	Filtered bool
	Live     bool
	Active   bool
}

// sidebarState is everything the sidebar draws.
type sidebarState struct {
	Palette theme.Palette
	Entries []sidebarEntry
	// Selected is the highlighted row, which is only distinct from the
	// active chat while the sidebar holds focus.
	Selected int
	Focused  bool
	Phase    int
}

// renderSidebar draws the chat list column.
//
// The close affordance is drawn only on the highlighted row and only while the
// sidebar has focus, so it reads as "x closes this" rather than as decoration
// hanging off every chat.
func renderSidebar(width, contentHeight int, st sidebarState) string {
	if width <= 0 {
		return ""
	}
	contentWidth := clampMin(width-2, 1)
	collapsed := contentWidth < sidebarCollapsedWidth
	lines := make([]string, 0, clampMin(contentHeight, 0))

	start := windowStart(st.Selected, len(st.Entries), contentHeight)
	for index := start; index < len(st.Entries) && len(lines) < contentHeight; index++ {
		entry := st.Entries[index]
		highlighted := st.Focused && index == st.Selected
		lines = append(lines, sidebarEntryLine(contentWidth, collapsed, highlighted, entry, st.Palette))
	}
	if len(st.Entries) == 0 && contentHeight > 0 {
		lines = append(lines, paneStyledText(fitLine(" (none open)", contentWidth), st.Palette.Muted, st.Palette.Surface, false))
	}
	lines = padLines(lines, contentWidth, contentHeight, st.Palette.Surface)

	return renderPane(paneSpec{
		palette:       st.Palette,
		icon:          "📺",
		title:         fmt.Sprintf("Chats · %02d", len(st.Entries)),
		content:       strings.Join(lines, "\n"),
		width:         width,
		contentHeight: contentHeight,
		accent:        st.Palette.Success,
		focused:       st.Focused,
		phase:         st.Phase,
	})
}

// sidebarEntryLine draws one chat row, dropping the title before the unread
// count as width shrinks: knowing something unread arrived matters more in a
// narrow column than knowing which broadcast it was.
func sidebarEntryLine(width int, collapsed, highlighted bool, entry sidebarEntry, palette theme.Palette) string {
	writer := newPaneLineWriter(width, palette.Surface)

	marker, markerColor := " ", palette.Muted
	if entry.Active {
		marker, markerColor = "▸", palette.Accent
	}
	writer.write(marker, markerColor, entry.Active)

	indicator, indicatorColor := connectionIndicator(palette, entry.Status)
	writer.write(" "+indicator, indicatorColor, false)

	// The close glyph is reserved out of the budget before the label so the
	// title truncates instead of the affordance disappearing.
	reserved := 0
	if highlighted {
		reserved = 2
	}
	suffix := sidebarEntrySuffix(entry)
	if !collapsed {
		labelWidth := clampMin(writer.remaining()-reserved-len(suffix), 0)
		if labelWidth > 0 {
			writer.write(" "+truncateDisplayWidth(entry.Label, clampMin(labelWidth-1, 0)), palette.Foreground, entry.Active)
		}
	}
	if entry.Live {
		writer.write(" ●", palette.Success, true)
	}
	if entry.Unread > 0 {
		writer.write(fmt.Sprintf(" %d", entry.Unread), palette.Warning, true)
	}
	if entry.Filtered {
		writer.write(" ƒ", palette.Muted, false)
	}
	if highlighted {
		pad := clampMin(writer.remaining()-2, 0)
		writer.write(strings.Repeat(" ", pad), palette.Muted, false)
		writer.write(sidebarCloseAffordance, palette.Error, true)
	}
	return writer.String()
}

// sidebarEntrySuffix reports the cells the trailing markers will need, so the
// label can be truncated before they are written rather than after.
func sidebarEntrySuffix(entry sidebarEntry) string {
	suffix := ""
	if entry.Live {
		suffix += " ●"
	}
	if entry.Unread > 0 {
		suffix += fmt.Sprintf(" %d", entry.Unread)
	}
	if entry.Filtered {
		suffix += " f"
	}
	return suffix
}

// connectionIndicator is the one-cell poll-session badge. It is a plain ASCII
// glyph so it occupies exactly one cell under every terminal's width rules and
// the column below it stays aligned.
func connectionIndicator(palette theme.Palette, status youtube.ConnectionStatus) (string, string) {
	switch status {
	case youtube.ConnectionConnected:
		return "*", palette.Success
	case youtube.ConnectionConnecting, youtube.ConnectionReconnecting:
		return "~", palette.Warning
	case youtube.ConnectionPaused:
		return "=", palette.Warning
	case youtube.ConnectionFailed:
		return "!", palette.Error
	case youtube.ConnectionDisconnected, youtube.ConnectionClosed:
		return "-", palette.Muted
	default:
		return "-", palette.Muted
	}
}

// sidebarVisibleFor decides whether the sidebar has room and reason to be
// drawn. Width and chat height are hard constraints in every mode: a sidebar
// that would leave no usable chat column helps nobody, and an explicit "show"
// cannot conjure space that is not there.
func sidebarVisibleFor(visibility paneVisibility, width, chatHeight, openChats int) bool {
	if width < sidebarMinWidth || chatHeight < 3 {
		return false
	}
	switch visibility {
	case paneVisibilityShown:
		return true
	case paneVisibilityHidden:
		return false
	default:
		return openChats >= 2
	}
}

// activityVisibleFor mirrors sidebarVisibleFor for the activity column. A
// deliberate "show" still needs enough width to leave chat readable, but does
// not require the auto threshold: someone who asked for the column on a
// 90-column terminal should get it.
func activityVisibleFor(visibility paneVisibility, width, chatHeight int) bool {
	if chatHeight < 3 {
		return false
	}
	switch visibility {
	case paneVisibilityShown:
		return width >= minChatWidthAfterPanes+activityMinSize
	case paneVisibilityHidden:
		return false
	default:
		return width >= activityMinTerminalWidth
	}
}

// sidebarWidthFor resolves the sidebar's effective width. The activity column
// is measured first and passed in as the competing pane, so the two can never
// together starve chat.
func sidebarWidthFor(visibility paneVisibility, override, width, chatHeight, openChats, activityWidth int) int {
	if !sidebarVisibleFor(visibility, width, chatHeight, openChats) {
		return 0
	}
	fallback := sidebarNormalSize
	if width >= sidebarWideAt {
		fallback = sidebarWideSize
	}
	return paneWidthOrDefault(override, fallback, sidebarMinSize, sidebarMaxSize, width, activityWidth)
}

// activityWidthFor resolves the activity column's effective width.
func activityWidthFor(visibility paneVisibility, override, width, chatHeight int) int {
	if !activityVisibleFor(visibility, width, chatHeight) {
		return 0
	}
	fallback := activityNormalSize
	if width >= activityWideAt {
		fallback = activityWideSize
	}
	return paneWidthOrDefault(override, fallback, activityMinSize, activityMaxSize, width, 0)
}

// clampPaneWidth keeps a pane inside its own bounds and inside what the
// terminal can spare once the other side pane has taken its share.
func clampPaneWidth(want, minimum, maximum, totalWidth, otherPaneWidth int) int {
	want = min(max(want, minimum), maximum)
	if affordable := totalWidth - otherPaneWidth - minChatWidthAfterPanes; want > affordable {
		want = affordable
	}
	if want < minimum {
		// The terminal cannot afford even the minimum; leave the pane at its
		// floor and let the layout decide to drop it entirely.
		return minimum
	}
	return want
}

// paneWidthOrDefault resolves an override against the responsive default,
// applying the clamps a manual resize would.
func paneWidthOrDefault(override, fallback, minimum, maximum, totalWidth, otherPaneWidth int) int {
	if override <= 0 {
		return fallback
	}
	return clampPaneWidth(override, minimum, maximum, totalWidth, otherPaneWidth)
}

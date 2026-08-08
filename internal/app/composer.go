package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// The composer is a borderless inset panel rather than another framed pane: one
// focus rail, a block cursor, and a compact metadata footer. Keeping it
// frameless holds the chat hierarchy light while send state stays visible, and
// it degrades to a three-row surface and then to plain text as the terminal
// shrinks, so a 6-row window still has somewhere to type.

// composerSendState is the lifecycle of the draft the user last submitted.
type composerSendState string

const (
	composerSendIdle        composerSendState = ""
	composerSendQueued      composerSendState = "queued"
	composerSendSending     composerSendState = "sending"
	composerSendSucceeded   composerSendState = "sent"
	composerSendFailed      composerSendState = "failed"
	composerSendRateLimited composerSendState = "rate limited"
)

// composerReplyContext is the message a draft is answering.
//
// YouTube live chat has no parent-message field, so this is yc's own construct:
// the send prepends "@DisplayName " to the text. The UI says "Replying to"
// because that is what the user meant, and the docs say plainly that the wire
// form is a mention, not a thread.
type composerReplyContext struct {
	MessageID string
	Author    string
	Text      string
}

const (
	// composerCursorInterval is the block cursor's blink period, driven off
	// the shared frame clock rather than a timer of its own.
	composerCursorInterval        = 500 * time.Millisecond
	composerReducedCursorInterval = time.Second

	// composerNarrowFrameWidth is the width below which the panel drops its
	// rail and inset and becomes plain text.
	composerNarrowFrameWidth = 8

	// composerRail is the one-cell focus indicator down the panel's left
	// edge, which is the only chrome the composer has.
	composerRail = "▌"
)

// composerSegment is one styled run of a composer row.
type composerSegment struct {
	text       string
	foreground string
	bold       bool
	italic     bool
}

// composerState is everything the composer draws.
type composerState struct {
	Palette       theme.Palette
	AnimationMode animation.Mode
	// Now is the shared frame clock's last tick. The zero time renders a
	// solid cursor rather than reading the wall clock.
	Now time.Time

	Draft   string
	Focused bool
	// HasChat is false before any chat is open. The composer stays usable in
	// that state because the target picker is reachable from it, so the
	// placeholder points there instead of naming a chat that does not exist.
	HasChat     bool
	TargetLabel string

	Reply     *composerReplyContext
	SendState composerSendState

	// DisabledReason explains why sending is unavailable - an API-key-only
	// session, or an OAuth token without youtube.force-ssl. The composer is
	// disabled with a reason rather than hidden: a control that vanishes
	// teaches the user nothing about how to get it back.
	DisabledReason string

	// Suggestions is the @mention completion strip, already ranked.
	Suggestions        []string
	SelectedSuggestion int
}

// renderComposer draws the composer at the height the layout reserved.
func renderComposer(width, height int, framed bool, st composerState) string {
	if height <= 0 || width <= 0 {
		return ""
	}
	if !framed || width < composerNarrowFrameWidth {
		return renderPlainComposer(width, height, st)
	}

	canvas := canvasBackground(st.Palette)
	panelWidth := clampMin(width-2, 1)
	bodyWidth := clampMin(panelWidth-1, 0)
	bodyPadding := 1
	if bodyWidth >= 12 {
		bodyPadding = 2
	}
	contentWidth := clampMin(bodyWidth-bodyPadding*2, 0)

	railColor := st.Palette.Border
	if st.Focused {
		railColor = st.Palette.Accent
	}

	lines := make([]string, 0, height)
	for _, segments := range composerPanelLines(height, contentWidth, st) {
		body := renderComposerSegments(segments, contentWidth, st.Palette.Surface)
		body = backgroundSpaces(bodyPadding, st.Palette.Surface) + body
		body += backgroundSpaces(bodyWidth-lipgloss.Width(body), st.Palette.Surface)
		panel := lipgloss.NewStyle().
			Foreground(lipgloss.Color(railColor)).
			Background(lipgloss.Color(st.Palette.Surface)).
			Bold(st.Focused).
			Render(composerRail) + body
		line := backgroundSpaces(1, canvas) + panel
		line += backgroundSpaces(width-lipgloss.Width(line), canvas)
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// renderPlainComposer is the last-resort form for a terminal too narrow or too
// short for the panel: the draft, a cursor, and the reply context if there is
// room for a second row. No inset, no rail, no footer.
func renderPlainComposer(width, height int, st composerState) string {
	input := st.Draft
	switch {
	case st.Focused:
		cursorWidth := 0
		if width >= 2 {
			cursorWidth = 1
		}
		input = tailDisplayCells(input, clampMin(width-cursorWidth, 0))
		if cursorWidth > 0 {
			input += composerCursorGlyph(st)
		}
	case input == "":
		input = composerPlaceholder(st, "")
	}
	if st.Reply != nil && height >= 2 {
		input = composerReplyLine(st, width) + "\n" + input
	}
	return backgroundStyledLine(fitBlock(input, width, height), canvasBackground(st.Palette))
}

// composerPanelLines lays the panel out top to bottom: reply context, mention
// strip, the draft itself, filler, and the metadata footer.
//
// The reply row and the mention strip are skipped rather than truncated when
// the panel is too short to hold them without evicting the input row: the one
// row that must always exist is the one being typed into.
func composerPanelLines(height, width int, st composerState) [][]composerSegment {
	if height <= 0 {
		return nil
	}
	lines := make([][]composerSegment, 0, height)
	if st.Reply != nil && height >= 3 {
		lines = append(lines, composerReplySegments(st))
	}
	if strip := composerSuggestionSegments(st); len(strip) > 0 && height >= len(lines)+3 {
		lines = append(lines, strip)
	}
	lines = append(lines, composerInputSegments(width, st))
	for len(lines) < height-1 {
		lines = append(lines, nil)
	}
	if len(lines) < height {
		lines = append(lines, composerFooterSegments(st))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// composerInputSegments renders the draft row.
//
// A focused composer truncates from the front so the caret stays on screen; an
// unfocused one truncates from the back so the start of the draft is what the
// user sees when they look away.
func composerInputSegments(width int, st composerState) []composerSegment {
	cursorWidth := 0
	if st.Focused && width > 0 {
		cursorWidth = 1
	}
	available := clampMin(width-cursorWidth, 0)

	input := st.Draft
	placeholder := false
	if input == "" && !st.Focused {
		input = composerPlaceholder(st, "…")
		placeholder = true
	}
	if st.Focused {
		input = tailDisplayCells(input, available)
	} else {
		input = revealDisplayCells(input, available)
	}

	foreground := st.Palette.Foreground
	if placeholder {
		foreground = st.Palette.Muted
	}
	segments := []composerSegment{{text: input, foreground: foreground}}
	if st.Focused && width > 0 {
		segments = append(segments, composerSegment{
			text:       composerCursorGlyph(st),
			foreground: st.Palette.Foreground,
			bold:       true,
		})
	}
	return segments
}

// composerReplySegments renders the "↳ Replying to <author> · <text>" row.
func composerReplySegments(st composerState) []composerSegment {
	if st.Reply == nil {
		return nil
	}
	author := strings.TrimSpace(st.Reply.Author)
	if author == "" {
		author = "someone"
	}
	segments := []composerSegment{
		{text: "↳ Replying to", foreground: st.Palette.Accent, bold: true},
		{text: " " + redactDiagnosticText(author), foreground: st.Palette.Foreground},
	}
	if text := compactDiagnosticText(st.Reply.Text); text != "" {
		segments = append(segments,
			composerSegment{text: " · ", foreground: st.Palette.Muted},
			composerSegment{text: redactDiagnosticText(text), foreground: st.Palette.Muted, italic: true},
		)
	}
	return segments
}

// composerSuggestionSegments renders the @mention completion strip, marking the
// selected candidate. It sits directly above the draft so the list is adjacent
// to the word being completed.
func composerSuggestionSegments(st composerState) []composerSegment {
	if len(st.Suggestions) == 0 {
		return nil
	}
	segments := make([]composerSegment, 0, len(st.Suggestions)*2+1)
	segments = append(segments, composerSegment{text: "@", foreground: st.Palette.Muted, bold: true})
	for index, suggestion := range st.Suggestions {
		if index > 0 {
			segments = append(segments, composerSegment{text: " · ", foreground: st.Palette.Muted})
		} else {
			segments = append(segments, composerSegment{text: " ", foreground: st.Palette.Muted})
		}
		color := st.Palette.Muted
		bold := false
		if index == st.SelectedSuggestion {
			color, bold = st.Palette.Accent, true
		}
		segments = append(segments, composerSegment{text: suggestion, foreground: color, bold: bold})
	}
	return segments
}

// composerFooterSegments is the compact target / send-state / budget line.
//
// The remaining character count only appears once a draft is close to
// YouTube's 200-character cap, so it reads as a warning rather than as a
// counter that is always there.
func composerFooterSegments(st composerState) []composerSegment {
	state, color := composerStateLabel(st)
	segments := []composerSegment{
		{text: "✉ Chat", foreground: st.Palette.Accent, bold: true},
		{text: " · ", foreground: st.Palette.Muted},
		{text: composerTargetLabel(st), foreground: st.Palette.Foreground, bold: true},
		{text: " · ", foreground: st.Palette.Muted},
		{text: state, foreground: color, bold: state != "ready"},
	}
	if remaining, show := composerRemainingRunes(st.Draft); show {
		color := st.Palette.Warning
		if remaining <= 0 {
			color = st.Palette.Error
		}
		segments = append(segments,
			composerSegment{text: " · ", foreground: st.Palette.Muted},
			composerSegment{text: fmt.Sprintf("%d left", remaining), foreground: color, bold: true},
		)
	}
	return segments
}

// composerRemainingRunes reports how many of YouTube's 200 characters are left,
// and whether that is close enough to matter yet.
func composerRemainingRunes(draft string) (int, bool) {
	used := uniseg.GraphemeClusterCount(draft)
	remaining := youtube.MaxChatMessageRunes - used
	return remaining, remaining <= youtube.MaxChatMessageRunes/4
}

// composerStateLabel names what the last submitted draft is doing. A disabled
// composer reports its reason here rather than in a separate banner, because
// this is the line the user is already looking at when they try to type.
func composerStateLabel(st composerState) (string, string) {
	if reason := strings.TrimSpace(st.DisabledReason); reason != "" {
		return reason, st.Palette.Error
	}
	switch st.SendState {
	case composerSendQueued:
		return "queued", st.Palette.Warning
	case composerSendSending:
		return "sending", st.Palette.Warning
	case composerSendSucceeded:
		return "sent", st.Palette.Success
	case composerSendFailed:
		return "failed", st.Palette.Error
	case composerSendRateLimited:
		return "rate limited", st.Palette.Error
	default:
		return "ready", st.Palette.Success
	}
}

// composerPlaceholder names what typing will do.
func composerPlaceholder(st composerState, suffix string) string {
	if !st.HasChat {
		return "/chats to open a live chat" + suffix
	}
	if reason := strings.TrimSpace(st.DisabledReason); reason != "" {
		return reason + suffix
	}
	return "Message " + composerTargetLabel(st) + suffix
}

func composerTargetLabel(st composerState) string {
	if !st.HasChat {
		return "no chat"
	}
	if label := strings.TrimSpace(st.TargetLabel); label != "" {
		return label
	}
	return "live chat"
}

// composerCursorGlyph returns the block cursor or a blank of the same width, so
// the blink never changes the row's geometry.
func composerCursorGlyph(st composerState) string {
	if composerCursorVisible(st) {
		return "█"
	}
	return " "
}

// composerCursorVisible derives the blink phase from the shared frame clock.
// With animation off, or before the first tick, the cursor is solid: a static
// caret still says "type here", which a blank one does not.
func composerCursorVisible(st composerState) bool {
	if !st.Focused {
		return false
	}
	interval := composerCursorInterval
	if st.AnimationMode == animation.ModeReduced {
		interval = composerReducedCursorInterval
	}
	return pulseOn(st.AnimationMode, st.Now, interval)
}

// composerReplyLine is the plain-text reply row used by the unframed form.
func composerReplyLine(st composerState, width int) string {
	if st.Reply == nil {
		return ""
	}
	author := strings.TrimSpace(st.Reply.Author)
	if author == "" {
		author = "someone"
	}
	return fitLine("↳ "+redactDiagnosticText(author), width)
}

// renderComposerSegments paints segments into an exact-width row, truncating on
// the width budget rather than after assembly, since styled text carries ANSI
// escapes that fitLine cannot measure.
func renderComposerSegments(segments []composerSegment, width int, background string) string {
	if width <= 0 {
		return ""
	}
	var builder strings.Builder
	remaining := width
	for _, segment := range segments {
		if remaining <= 0 {
			break
		}
		text := revealDisplayCells(segment.text, remaining)
		if text == "" {
			continue
		}
		builder.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(segment.foreground)).
			Background(lipgloss.Color(background)).
			Bold(segment.bold).
			Italic(segment.italic).
			Render(text))
		remaining -= ansi.StringWidth(text)
	}
	return builder.String()
}

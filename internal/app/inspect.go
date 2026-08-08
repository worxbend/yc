package app

import (
	"fmt"
	"strings"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// The inspect panel is the "why did that row render like that" surface: the
// normalized message behind the selected row, including the original
// snippet.type that yc mapped from.
//
// Everything it prints goes through the shared redactor first. This panel is
// the one place in the UI that deliberately shows raw-ish values, so it is also
// the one place where a credential-shaped string could reach a terminal that is
// frequently on stream. Redacting on the way out, before fitting, means a
// secret cannot survive as a truncated prefix either.

// inspectState is everything the panel draws.
type inspectState struct {
	Palette theme.Palette
	// Message is the selected row. Selected is false when nothing is
	// selected, which is a normal state rather than an error.
	Message  youtube.Message
	Selected bool
	Phase    int
}

// renderInspect draws the docked diagnostics panel.
func renderInspect(width, height, contentHeight int, framed bool, st inspectState) string {
	contentWidth := width
	if framed {
		contentWidth = clampMin(width-4, 1)
	}
	lines := inspectLines(contentWidth, contentHeight, st)
	for len(lines) < contentHeight {
		lines = append(lines, fitLine("", contentWidth))
	}
	content := strings.Join(lines, "\n")
	if !framed {
		return backgroundStyledLine(fitBlock(content, width, height), st.Palette.Surface)
	}
	return renderPane(paneSpec{
		palette:       st.Palette,
		icon:          "🔎",
		title:         "Inspect",
		content:       content,
		width:         width,
		contentHeight: contentHeight,
		padding:       1,
		accent:        st.Palette.Warning,
		focused:       true,
		phase:         st.Phase,
	})
}

// inspectLines builds the panel body. Lines are ordered by how often they
// answer the question that opened the panel, because a short terminal keeps
// only the prefix.
func inspectLines(width, height int, st inspectState) []string {
	if height <= 0 {
		return nil
	}
	if !st.Selected {
		return fitInspectLines([]string{
			" Inspect: no selected message",
			" Select with j/k or click a message, then press K.",
		}, width, height)
	}

	message := st.Message
	lines := []string{
		" Inspect: selected message",
		inspectMessageLine(message),
		inspectAuthorLine(message),
		inspectBadgesLine(message.Badges),
	}
	if line, ok := inspectPaidLine(message); ok {
		lines = append(lines, line)
	}
	if line, ok := inspectMembershipLine(message); ok {
		lines = append(lines, line)
	}
	// A removed message's body never goes back on screen, on any surface.
	// The row renderer and the activity column both refuse to reprint it
	// without assuming somebody upstream already blanked it, and this panel -
	// the one place in the UI that deliberately shows raw-ish values - has to
	// hold the same line. Without this, K on a row drawn as "[message
	// deleted]" put the words the moderator just removed straight back onto a
	// terminal that is frequently being streamed. The removal is named rather
	// than silently omitted, so the diagnostic stays honest about what it is
	// withholding.
	if message.Deleted {
		return fitInspectLines(append(lines, "text: removed"), width, height)
	}
	if len(message.Fragments) > 0 {
		lines = append(lines, inspectFragmentsLine(message.Fragments))
	}
	if strings.TrimSpace(message.Text) != "" {
		lines = append(lines, "text: "+compactDiagnosticText(message.Text))
	}
	return fitInspectLines(lines, width, height)
}

// fitInspectLines redacts every line before fitting it, so a secret can never
// survive as a truncated prefix.
func fitInspectLines(lines []string, width, height int) []string {
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = fitLine(redactDiagnosticText(lines[i]), width)
	}
	return lines
}

func inspectMessageLine(message youtube.Message) string {
	parts := []string{
		"id=" + safeDiagnosticValue(message.ID),
		"kind=" + safeDiagnosticValue(string(message.Kind)),
		"type=" + safeDiagnosticValue(string(message.Type)),
	}
	// RawType is the original snippet.type. It is retained for exactly this
	// line: when YouTube adds an event kind, the row still renders and this
	// is what names what it actually was.
	if raw := strings.TrimSpace(message.RawType); raw != "" && !strings.EqualFold(raw, string(message.Kind)) {
		parts = append(parts, "snippet="+safeDiagnosticValue(raw))
	}
	if !message.Timestamp.IsZero() {
		parts = append(parts, "time="+message.Timestamp.UTC().Format("2006-01-02T15:04:05Z"))
	}
	// Declared in a fixed order rather than iterated from a map, so the line
	// is byte-identical between frames and between test runs.
	for _, flag := range []struct {
		label string
		set   bool
	}{
		{"historical", message.Historical},
		{"deleted", message.Deleted},
		{"echo", message.LocalEcho},
	} {
		if flag.set {
			parts = append(parts, flag.label+"=true")
		}
	}
	return "message: " + strings.Join(parts, " ")
}

func inspectAuthorLine(message youtube.Message) string {
	author := message.Author
	parts := []string{"display=" + safeDiagnosticValue(author.DisplayName)}
	if author.ChannelID != "" {
		parts = append(parts, "channel="+safeDiagnosticValue(author.ChannelID))
	}
	if author.MemberLevelName != "" {
		parts = append(parts, "level="+safeDiagnosticValue(author.MemberLevelName))
	}
	if author.MemberMonths > 0 {
		parts = append(parts, fmt.Sprintf("months=%d", author.MemberMonths))
	}
	// The avatar URL is recorded but never fetched: yc has no image path at
	// all, so reporting "resolved" is honest and printing the URL would be
	// noise.
	if author.AvatarURL != "" {
		parts = append(parts, "avatar=recorded")
	}
	return "author: " + strings.Join(parts, " ")
}

func inspectBadgesLine(badges []youtube.Badge) string {
	if len(badges) == 0 {
		return "badges: none"
	}
	parts := make([]string, 0, len(badges))
	for _, badge := range badges {
		label := safeDiagnosticValue(string(badge.Kind))
		if badge.Info != "" {
			label += "(" + safeDiagnosticValue(badge.Info) + ")"
		}
		parts = append(parts, label)
	}
	return "badges: " + strings.Join(parts, ", ")
}

// inspectPaidLine reports the money on a message without doing any currency
// math: Micros is an integer and Display is YouTube's own localized string.
func inspectPaidLine(message youtube.Message) (string, bool) {
	amount, ok := message.Amount()
	if !ok && message.Gift == nil {
		return "", false
	}
	parts := make([]string, 0, 5)
	if ok {
		parts = append(parts,
			"display="+safeDiagnosticValue(amount.Display),
			"currency="+safeDiagnosticValue(amount.Currency),
			fmt.Sprintf("micros=%d", amount.Micros),
		)
		if tier := message.Tier(); tier > 0 {
			parts = append(parts, fmt.Sprintf("tier=%d", tier))
		}
	}
	if message.SuperSticker != nil && message.SuperSticker.AltText != "" {
		parts = append(parts, "sticker="+safeDiagnosticValue(message.SuperSticker.AltText))
	}
	if message.Gift != nil {
		parts = append(parts, "gift="+safeDiagnosticValue(message.Gift.Name))
		if message.Gift.ComboCount > 0 {
			parts = append(parts, fmt.Sprintf("combo=%d", message.Gift.ComboCount))
		}
	}
	return "paid: " + strings.Join(parts, " "), true
}

func inspectMembershipLine(message youtube.Message) (string, bool) {
	membership := message.Membership
	if membership == nil {
		return "", false
	}
	parts := []string{"kind=" + safeDiagnosticValue(string(membership.Kind))}
	if membership.LevelName != "" {
		parts = append(parts, "level="+safeDiagnosticValue(membership.LevelName))
	}
	if membership.Months > 0 {
		parts = append(parts, fmt.Sprintf("months=%d", membership.Months))
	}
	if membership.GiftCount > 0 {
		parts = append(parts, fmt.Sprintf("gifts=%d", membership.GiftCount))
	}
	if membership.GifterChannelID != "" {
		parts = append(parts, "gifter="+safeDiagnosticValue(membership.GifterChannelID))
	}
	return "membership: " + strings.Join(parts, " "), true
}

func inspectFragmentsLine(fragments []youtube.MessageFragment) string {
	parts := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		label := string(fragment.Type)
		if fragment.Text != "" {
			label += "=" + safeDiagnosticValue(compactDiagnosticText(fragment.Text))
		}
		parts = append(parts, label)
	}
	return "fragments: " + strings.Join(parts, ", ")
}

// diagnosticRedactor is the shared redactor for every user-visible diagnostic
// surface. It carries no secrets of its own: it matches on shape, which is what
// catches a token pasted into a chat message as well as one that leaked out of
// a config value.
var diagnosticRedactor = auth.NewRedactor()

// redactDiagnosticText removes credential-shaped values from a line bound for
// the terminal.
func redactDiagnosticText(value string) string {
	return diagnosticRedactor.Redact(value)
}

// safeDiagnosticValue redacts a field value and names an empty one rather than
// rendering a gap the reader has to interpret.
func safeDiagnosticValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return redactDiagnosticText(value)
}

// compactDiagnosticText collapses whitespace so a multi-line message body
// occupies one diagnostic row.
func compactDiagnosticText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

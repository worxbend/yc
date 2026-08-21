package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/worxbend/yc/internal/animation"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// The status bar is one accent-backed row. It carries the two facts a YouTube
// chat client must show and a Twitch one never has to: how much of today's API
// quota is gone, and how often yc is actually allowed to poll.
//
// Quota is the defining constraint of this API. At the estimated 5 units per
// list call, the default 10,000-unit day is 2,000 polls - one every 43 seconds
// to last until the Pacific reset. Polling at YouTube's own advertised cadence
// exhausts the day in under three hours. A user who cannot see that coming
// finds out when the chat stops.

const (
	// statusPulseInterval is the LIVE/REC blink period.
	statusPulseInterval = 500 * time.Millisecond

	// Width tiers. Segments are added only above their threshold, so the line
	// degrades by dropping decoration rather than by truncating identity.
	statusWidthFullQuota    = 96
	statusWidthMediumQuota  = 72
	statusWidthCompactQuota = 48
	statusWidthFullMetrics  = 96
	statusWidthMetrics      = 60
	statusWidthChatCount    = 26
	statusWidthUnread       = 34
	statusWidthDetail       = 34
	statusWidthFilter       = 46
	statusWidthSearch       = 40
	statusWidthSendFeedback = 50
	statusWidthNotify       = 58
	statusWidthFocus        = 42
	statusWidthFocusMode    = 64
	statusWidthWideDetail   = 112

	// quotaWarnPercent and quotaCriticalPercent are where the meter shifts
	// from Success to Warning and from Warning to Error. They are remaining
	// percentages, not used ones: what matters is what is left.
	quotaWarnPercent     = 50.0
	quotaCriticalPercent = 20.0
)

// statusSegment is one colored run of the status line. The bar is assembled
// from segments rather than from one string because the quota meter has to
// change color independently of everything beside it, and fitLine is not
// ANSI-aware.
type statusSegment struct {
	text       string
	foreground string
	bold       bool
}

// statusBarState is everything the status line renders. It is a value snapshot
// taken in Update: nothing here is read live from a client or a clock, which is
// what keeps View pure and the golden tests deterministic.
type statusBarState struct {
	Palette       theme.Palette
	AnimationMode animation.Mode
	// Now is the shared frame clock's last tick. The zero time renders the
	// deterministic static frame rather than falling back to the wall clock.
	Now time.Time

	// ChatTitle is the active broadcast title, already truncated by the
	// caller if it needs to be. Empty means no chat is open.
	ChatTitle    string
	Status       youtube.ConnectionStatus
	StatusDetail string
	ChatCount    int
	Unread       int
	// Dropped counts messages lost because the UI could not keep up. It is
	// rendered at every width: chat quietly losing messages is the one thing
	// a moderator must not have to discover for themselves.
	Dropped      uint64
	PendingClear bool
	// Moderation is the armed confirmation, open duration prompt, or the
	// result of the last moderation action, and ModerationLevel is how
	// loudly to say it. Like Dropped it is drawn at every width: a key that
	// removes somebody's words from a live broadcast must never look like it
	// did nothing.
	Moderation      string
	ModerationLevel moderationLevel
	Notification    string
	Filter          string
	// Search is the active "/" query - the raw input line while it is open,
	// or the committed query with its match count. Empty when no search is
	// active.
	Search       string
	SendFeedback string
	Focus        string
	Layout       string

	Live             bool
	LiveSince        time.Time
	Viewers          int
	ViewersKnown     bool
	Subscribers      int
	SubscribersKnown bool

	// Quota is the estimated ledger. QuotaKnown is false for transports with
	// no ledger at all, in which case the meter is omitted rather than shown
	// as zeroes.
	Quota      quota.Snapshot
	QuotaKnown bool
	// CostPerPoll is the estimated cost of one liveChatMessages.list call,
	// used for the projected-exhaustion figure.
	CostPerPoll int

	DebugRecording bool
	// FPS is the observed repaint rate over the last second, sampled in
	// Update. It is the only process metric yc shows: a chat client's
	// frame rate is a real symptom, and CPU or heap figures would be
	// decoration competing with the quota meter for the same cells.
	FPS float64
}

// renderStatusBar draws the whole line, padded to width.
func renderStatusBar(width int, st statusBarState) string {
	if width <= 0 {
		return ""
	}
	background := st.Palette.Accent
	foreground := theme.ContrastCorrectedForeground(st.Palette.Foreground, background, canvasBackground(st.Palette))

	writer := newPaneLineWriter(width, background)
	for _, segment := range statusSegments(width, st) {
		color := segment.foreground
		if color == "" {
			color = foreground
		} else {
			// A role color chosen for a dark canvas is not guaranteed to be
			// legible on the accent-filled status bar, so every colored
			// segment is corrected against the bar's own background - but by
			// relighting the hue rather than replacing it. Discarding the
			// color made the quota meter, the cadence label, and the dropped
			// counter render in the same neutral as the text beside them.
			color = theme.ReadableOn(color, background, foreground)
		}
		writer.write(segment.text, color, segment.bold || segment.foreground == "")
	}
	line := writer.String()
	// One outer style fixes the row width even when every segment was
	// truncated away, which is what happens on a one-column terminal.
	return lipgloss.NewStyle().
		Width(width).
		Background(lipgloss.Color(background)).
		Render(line)
}

// statusSegments builds the line left to right.
//
// Order is priority order: fitting is done by dropping trailing segments, so
// anything that must survive a narrow terminal is placed ahead of anything
// decorative. The dropped-message counter and the armed clear guard come first
// for exactly that reason - a silent loss and an invisible confirmation prompt
// are both worse than losing every decoration on the line.
func statusSegments(width int, st statusBarState) []statusSegment {
	groups := make([][]statusSegment, 0, 8)
	add := func(segments ...statusSegment) {
		if len(segments) > 0 {
			groups = append(groups, segments)
		}
	}
	for _, rule := range statusSegmentRules {
		if width < rule.minWidth {
			continue
		}
		add(rule.build(width, st)...)
	}

	// Groups are joined here rather than as they are added, so a group that
	// turned out empty cannot leave a dangling " | " behind it.
	segments := make([]statusSegment, 0, len(groups)*3)
	segments = append(segments, statusSegment{text: " "})
	for index, group := range groups {
		if index > 0 {
			segments = append(segments, statusSegment{text: " | ", foreground: st.Palette.Muted})
		}
		segments = append(segments, group...)
	}
	return segments
}

// statusSegmentRule is one candidate group on the status line, with the width
// below which it is not drawn at all.
type statusSegmentRule struct {
	// minWidth is the narrowest line this group may appear on. Zero means the
	// group never drops for width - only for having nothing to say.
	minWidth int
	// build returns the group, or nil when the state has nothing to show. It
	// is handed the width too, because a few groups have their own internal
	// tiers: the quota meter, the metrics pair, and the focus readout each
	// shed detail before they disappear entirely.
	build func(width int, st statusBarState) []statusSegment
}

// statusSegmentRules is the whole drop-priority ladder, in the order the line
// is assembled left to right.
//
// It is one table rather than fifteen inline width checks so the answer to
// "what disappears first as the terminal narrows" can be read top to bottom in
// one place. Earlier entries are both further left and more important: fitting
// drops from the right, so a group placed here is also a claim about what a
// narrow terminal must keep.
var statusSegmentRules = []statusSegmentRule{
	// A silent message loss and an invisible confirmation prompt are both
	// worse than losing every decoration on the line, so these never drop.
	{minWidth: 0, build: func(_ int, st statusBarState) []statusSegment {
		if st.Dropped <= 0 {
			return nil
		}
		return statusText(fmt.Sprintf("dropped=%d", st.Dropped), st.Palette.Error, true)
	}},
	{minWidth: 0, build: func(_ int, st statusBarState) []statusSegment {
		if !st.PendingClear {
			return nil
		}
		return statusText("clear chat? ctrl+L again to confirm", st.Palette.Warning, true)
	}},
	{minWidth: 0, build: func(_ int, st statusBarState) []statusSegment {
		return statusText(st.Moderation, moderationStatusColor(st.Palette, st.ModerationLevel), true)
	}},
	// The search line sits ahead of the meters: while the user is typing a
	// query, the query is the thing they are looking for on screen.
	{minWidth: statusWidthSearch, build: func(_ int, st statusBarState) []statusSegment {
		return statusText(st.Search, st.Palette.Warning, true)
	}},
	{minWidth: 0, build: func(width int, st statusBarState) []statusSegment {
		if !st.QuotaKnown {
			return nil
		}
		return quotaSegments(width, st)
	}},
	{minWidth: statusWidthMetrics, build: func(width int, st statusBarState) []statusSegment {
		if width >= statusWidthFullMetrics {
			return fullMetricsSegments(st)
		}
		return compactMetricsSegments(st)
	}},
	{minWidth: 0, build: func(_ int, st statusBarState) []statusSegment {
		return chatIdentitySegments(st)
	}},
	{minWidth: statusWidthChatCount, build: func(_ int, st statusBarState) []statusSegment {
		if st.ChatCount <= 1 {
			return nil
		}
		return statusText(fmt.Sprintf("chats=%d", st.ChatCount), "", false)
	}},
	{minWidth: statusWidthUnread, build: func(_ int, st statusBarState) []statusSegment {
		if st.Unread <= 0 {
			return nil
		}
		return statusText(fmt.Sprintf("unread=%d", st.Unread), st.Palette.Warning, true)
	}},
	{minWidth: statusWidthNotify, build: func(_ int, st statusBarState) []statusSegment {
		if st.Notification == "" {
			return nil
		}
		return statusText("notify: "+st.Notification, "", false)
	}},
	{minWidth: statusWidthFilter, build: func(_ int, st statusBarState) []statusSegment {
		if st.Filter == "" {
			return nil
		}
		return statusText("filter="+st.Filter, st.Palette.Warning, false)
	}},
	// Send feedback and the connection detail compete for the same slot, and
	// send feedback wins where it fits: it answers a question the user asked
	// a moment ago.
	// The detail additionally waits for a wider line when several chats are
	// open, because the identity of the chat has to fit first.
	{minWidth: statusWidthDetail, build: func(width int, st statusBarState) []statusSegment {
		if st.SendFeedback != "" && width >= statusWidthSendFeedback {
			return statusText("send: "+st.SendFeedback, st.Palette.Warning, false)
		}
		// Feedback too wide to fit does not block the detail: the original
		// ladder fell through to it, and a connection detail is better than
		// an empty slot.
		if st.StatusDetail == "" || (st.ChatCount > 1 && width < statusWidthWideDetail) {
			return nil
		}
		return statusText(st.StatusDetail, st.Palette.Muted, false)
	}},
	{minWidth: statusWidthFocus, build: func(width int, st statusBarState) []statusSegment {
		if width >= statusWidthFocusMode {
			return statusText(fmt.Sprintf("focus=%s layout=%s",
				emptyOr(st.Focus, "chat"), emptyOr(st.Layout, "inline")), "", false)
		}
		return statusText("focus="+emptyOr(st.Focus, "chat"), "", false)
	}},
}

// statusText is one plain run of text, or nothing when the value is empty.
func statusText(value, foreground string, bold bool) []statusSegment {
	if value == "" {
		return nil
	}
	return []statusSegment{{text: value, foreground: foreground, bold: bold}}
}

// quotaSegments renders the quota meter and the effective poll interval, the
// pair that is unique to a YouTube client.
//
// Every numeric form carries an explicit "est" marker, and the marker is the
// last thing dropped before the numbers themselves: Google publishes no quota
// costs for any live-chat method, so a figure presented as fact would be a lie
// the user could act on.
func quotaSegments(width int, st statusBarState) []statusSegment {
	snapshot := st.Quota
	palette := st.Palette
	percent := snapshot.RemainingPercent()
	percentText := fmt.Sprintf("%.0f%%", percent)
	percentColor := quotaColor(palette, percent)
	mode, modeColor := quotaModeLabel(palette, snapshot.Mode)
	interval := "⟳ " + formatPollInterval(snapshot.EffectiveInterval)

	dot := statusSegment{text: " · ", foreground: palette.Muted}
	switch {
	case width >= statusWidthFullQuota:
		segments := []statusSegment{
			{text: interval, foreground: palette.Foreground},
			dot,
			{text: fmt.Sprintf("%s/%s", formatUnits(snapshot.UsedUnits), formatUnits(snapshot.LimitUnits)), foreground: percentColor, bold: true},
		}
		if snapshot.Estimated {
			segments = append(segments, statusSegment{text: " est", foreground: palette.Muted})
		}
		segments = append(segments, dot, statusSegment{text: percentText + " left", foreground: percentColor, bold: true})
		if remaining, ok := snapshot.ProjectedExhaustion(st.CostPerPoll); ok {
			segments = append(segments, dot, statusSegment{
				text:       "~" + formatCompactDuration(remaining),
				foreground: percentColor,
			})
		}
		if mode != "" {
			segments = append(segments, dot, statusSegment{text: mode, foreground: modeColor, bold: true})
		}
		return segments
	case width >= statusWidthMediumQuota:
		segments := []statusSegment{
			{text: interval, foreground: palette.Foreground},
			dot,
			{text: percentText, foreground: percentColor, bold: true},
		}
		if snapshot.Estimated {
			segments = append(segments, statusSegment{text: " est", foreground: palette.Muted})
		}
		if mode != "" {
			segments = append(segments, dot, statusSegment{text: mode, foreground: modeColor, bold: true})
		}
		return segments
	case width >= statusWidthCompactQuota:
		return []statusSegment{
			{text: interval, foreground: palette.Foreground},
			dot,
			{text: percentText, foreground: percentColor, bold: true},
		}
	default:
		return []statusSegment{{text: percentText, foreground: percentColor, bold: true}}
	}
}

// quotaColor shifts the meter from Success through Warning to Error as the
// remaining budget depletes. The thresholds are on remaining units because that
// is the number that decides whether the session survives the stream.
func quotaColor(palette theme.Palette, remainingPercent float64) string {
	switch {
	case remainingPercent > quotaWarnPercent:
		return palette.Success
	case remainingPercent > quotaCriticalPercent:
		return palette.Warning
	default:
		return palette.Error
	}
}

// quotaModeLabel names why the poll loop is running at its current cadence.
// STRETCHED is not a warning about yc misbehaving - it means yc deliberately
// slowed below YouTube's advertised cadence to make the budget last - so it
// reads as caution rather than as failure.
//
// The healthy cadence returns an empty label and is not drawn at all. It says
// nothing the ⟳ interval beside it has not already said, the same status bar
// already carries a pulsing LIVE for the broadcast itself - so spelling the
// word twice for two unrelated facts would be actively misleading - and the
// cells it would take are worth more to the connection detail further right.
// A label appears precisely when something is not normal.
func quotaModeLabel(palette theme.Palette, mode quota.Mode) (string, string) {
	switch mode {
	case quota.ModeStretched:
		return "STRETCHED", palette.Warning
	case quota.ModeBackoff:
		return "BACKOFF", palette.Warning
	case quota.ModePaused:
		return "PAUSED", palette.Error
	default:
		return "", palette.Muted
	}
}

// quotaModeDescription spells out a cadence mode for the Quota tab, which has
// the room the status bar does not. Each one names the reason as well as the
// state: "stretched" on its own reads like a fault rather than a decision yc
// took on the user's behalf.
func quotaModeDescription(mode quota.Mode) string {
	switch mode {
	case quota.ModeStretched:
		return "stretched — polling slower than allowed so the daily budget lasts"
	case quota.ModeBackoff:
		return "backoff — the API asked yc to slow down"
	case quota.ModePaused:
		return "paused — the allowance or the reserve threshold was reached"
	case quota.ModeLive:
		return "realtime — polling at the server-advised cadence"
	case quota.ModeIdle:
		return "idle — no chat has polled yet today"
	default:
		return string(mode)
	}
}

// fullMetricsSegments is the wide telemetry block: the pulsing broadcast badge,
// viewers, subscribers, the debug-recording marker, and process metrics.
func fullMetricsSegments(st statusBarState) []statusSegment {
	pulse := pulseOn(st.AnimationMode, st.Now, statusPulseInterval)
	segments := make([]statusSegment, 0, 10)
	add := func(text, foreground string, bold bool) {
		if len(segments) > 0 {
			segments = append(segments, statusSegment{text: " ", foreground: foreground})
		}
		segments = append(segments, statusSegment{text: text, foreground: foreground, bold: bold})
	}

	if st.Live {
		add(pulseLabel("LIVE", pulse)+" "+formatElapsed(liveElapsed(st.Now, st.LiveSince)), st.Palette.Success, true)
		if st.ViewersKnown {
			add(fmt.Sprintf("viewers=%s", formatUnits(st.Viewers)), "", false)
		}
	} else {
		add("OFFLINE", st.Palette.Muted, false)
	}
	if st.SubscribersKnown {
		add(fmt.Sprintf("subs=%s", formatUnits(st.Subscribers)), "", false)
	}
	if st.DebugRecording {
		add(pulseLabel("REC", pulse), st.Palette.Error, true)
	}
	add(fmt.Sprintf("fps=%.0f", st.FPS), "", false)
	return segments
}

// compactMetricsSegments keeps only the broadcast badge and the viewer count,
// the two figures that stay meaningful in a narrow terminal.
func compactMetricsSegments(st statusBarState) []statusSegment {
	if !st.Live {
		return []statusSegment{{text: "OFFLINE", foreground: st.Palette.Muted}}
	}
	pulse := pulseOn(st.AnimationMode, st.Now, statusPulseInterval)
	segments := []statusSegment{{
		text:       pulseLabel("LIVE", pulse) + " " + formatElapsed(liveElapsed(st.Now, st.LiveSince)),
		foreground: st.Palette.Success,
		bold:       true,
	}}
	if st.ViewersKnown {
		segments = append(segments, statusSegment{text: " 👥 " + formatUnits(st.Viewers)})
	}
	return segments
}

// chatIdentitySegments names the active chat and its poll-session state. This
// is the part of the line the user reads to answer "what am I looking at".
func chatIdentitySegments(st statusBarState) []statusSegment {
	if strings.TrimSpace(st.ChatTitle) == "" {
		return []statusSegment{{text: "no chat open", foreground: st.Palette.Muted}}
	}
	status := string(st.Status)
	if status == "" {
		status = string(youtube.ConnectionDisconnected)
	}
	return []statusSegment{
		{text: st.ChatTitle, bold: true},
		{text: " " + status, foreground: connectionColor(st.Palette, st.Status)},
	}
}

// connectionColor maps a poll session's lifecycle onto palette roles. A closed
// session is not an error: a chat that ended is the normal end of a broadcast,
// and the history stays readable.
func connectionColor(palette theme.Palette, status youtube.ConnectionStatus) string {
	switch status {
	case youtube.ConnectionConnected:
		return palette.Success
	case youtube.ConnectionConnecting, youtube.ConnectionReconnecting:
		return palette.Warning
	case youtube.ConnectionPaused:
		return palette.Warning
	case youtube.ConnectionFailed:
		return palette.Error
	default:
		return palette.Muted
	}
}

// formatPollInterval renders a cadence the way a person reads it: sub-minute
// intervals in seconds with one decimal below ten, whole seconds above, and
// minutes once yc has stretched far enough that seconds stop being useful.
func formatPollInterval(d time.Duration) string {
	switch {
	case d <= 0:
		return "--"
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// formatCompactDuration renders a projected time-to-exhaustion. It is the
// number that tells someone whether they will make it to the end of the stream,
// so it stays short enough to survive the status bar's width budget.
func formatCompactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if hours > 0 {
		return fmt.Sprintf("%dh%02dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// formatUnits groups thousands so a five-digit quota figure can be read at a
// glance rather than counted.
func formatUnits(value int) string {
	negative := value < 0
	if negative {
		value = -value
	}
	digits := fmt.Sprintf("%d", value)
	if len(digits) <= 3 {
		if negative {
			return "-" + digits
		}
		return digits
	}
	var builder strings.Builder
	lead := len(digits) % 3
	if lead > 0 {
		builder.WriteString(digits[:lead])
	}
	for i := lead; i < len(digits); i += 3 {
		if builder.Len() > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(digits[i : i+3])
	}
	if negative {
		return "-" + builder.String()
	}
	return builder.String()
}

// liveElapsed reports on-air duration as of now, or zero while either end is
// unknown. It never falls back to the wall clock: View must stay a pure
// function of already-ticked state.
func liveElapsed(now, liveSince time.Time) time.Duration {
	if now.IsZero() || liveSince.IsZero() {
		return 0
	}
	return now.Sub(liveSince)
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	hours := total / 3600
	minutes := (total % 3600) / 60
	seconds := total % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

func emptyOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

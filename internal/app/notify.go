package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rivo/uniseg"
	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/youtube"
)

const (
	terminalBell               = "\a"
	desktopNotificationTimeout = 3 * time.Second
)

// defaultSystemNotifier tries a desktop notification and falls back to the
// terminal bell. A missed notification must never take down chat, so every
// failure degrades instead of propagating.
type defaultSystemNotifier struct {
	desktop desktopNotifier
	bell    terminalBellNotifier
}

// newDefaultSystemNotifier returns the platform notifier for w. It uses no
// notification library: the three supported mechanisms are a process launch
// each, which keeps the dependency set at zero for a feature that is optional
// by nature.
func newDefaultSystemNotifier(w io.Writer) SystemNotifier {
	return defaultSystemNotifier{
		desktop: desktopNotifier{},
		bell:    terminalBellNotifier{w: w},
	}
}

func (n defaultSystemNotifier) Notify(ctx context.Context, notification SystemNotification) error {
	if err := n.desktop.Notify(ctx, notification); err == nil {
		return nil
	}
	return n.bell.Notify(ctx, notification)
}

type terminalBellNotifier struct {
	w io.Writer
}

func (n terminalBellNotifier) Notify(ctx context.Context, _ SystemNotification) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if n.w == nil {
		return nil
	}
	_, err := io.WriteString(n.w, terminalBell)
	return err
}

// desktopNotifier shells out to the platform's notification tool. Every
// dependency is injectable so the behavior is testable without a desktop.
type desktopNotifier struct {
	goos       string
	timeout    time.Duration
	lookPath   func(string) (string, error)
	runCommand func(context.Context, string, ...string) error
}

func (n desktopNotifier) Notify(ctx context.Context, notification SystemNotification) error {
	title := sanitizeNotificationText(notification.Title, 96)
	body := sanitizeNotificationText(notification.Body, 320)
	if title == "" {
		title = "yc"
	}

	goos := n.goos
	if goos == "" {
		goos = runtime.GOOS
	}
	name, args, ok := desktopNotificationCommand(goos, title, body)
	if !ok {
		return ErrDesktopNotificationUnsupported
	}

	lookPath := n.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := lookPath(name)
	if err != nil {
		return err
	}

	timeout := n.timeout
	if timeout <= 0 {
		timeout = desktopNotificationTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runCommand := n.runCommand
	if runCommand == nil {
		runCommand = runDesktopNotificationCommand
	}
	return runCommand(ctx, path, args...)
}

func runDesktopNotificationCommand(ctx context.Context, path string, args ...string) error {
	return exec.CommandContext(ctx, path, args...).Run() //nolint:gosec // path comes from exec.LookPath over a fixed notifier list, never from chat
}

func desktopNotificationCommand(goos, title, body string) (string, []string, bool) {
	switch goos {
	case "darwin":
		return "osascript", []string{
			"-e", "on run argv",
			"-e", "display notification item 2 of argv with title item 1 of argv",
			"-e", "end run",
			title,
			body,
		}, true
	case "windows":
		return "powershell.exe", []string{
			"-NoProfile",
			"-NonInteractive",
			"-ExecutionPolicy", "Bypass",
			"-EncodedCommand", windowsToastPowerShellCommand(title, body),
		}, true
	case "linux", "freebsd", "netbsd", "openbsd":
		// The "--" terminator matters: notify-send accepts options anywhere
		// on the command line, and the body starts with an attacker-chosen
		// author name, so a chatter called "--icon=/etc/passwd" would
		// otherwise be parsed as an option rather than displayed as text.
		args := []string{"--app-name=yc", "--urgency=normal", "--expire-time=8000", "--", title}
		if body != "" {
			args = append(args, body)
		}
		return "notify-send", args, true
	default:
		return "", nil, false
	}
}

func windowsToastPowerShellCommand(title, body string) string {
	script := fmt.Sprintf(`
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null
$template = @"
<toast><visual><binding template="ToastGeneric"><text>%s</text><text>%s</text></binding></visual></toast>
"@
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml($template)
$notifier = [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("yc")
$notifier.Show([Windows.UI.Notifications.ToastNotification]::new($xml))
`, escapeNotificationXML(title), escapeNotificationXML(body))
	encoded := utf16.Encode([]rune(script))
	bytes := make([]byte, 0, len(encoded)*2)
	for _, r := range encoded {
		bytes = append(bytes, byte(r), byte(r>>8)) //nolint:gosec // deliberate UTF-16LE byte split; both halves of the uint16 are kept
	}
	return base64.StdEncoding.EncodeToString(bytes)
}

func escapeNotificationXML(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch r {
		case '&':
			builder.WriteString("&amp;")
		case '<':
			builder.WriteString("&lt;")
		case '>':
			builder.WriteString("&gt;")
		case '"':
			builder.WriteString("&quot;")
		case '\'':
			builder.WriteString("&apos;")
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// sanitizeNotificationText makes a value safe to hand to a shell-launched
// notifier: redacted, control-character free, whitespace collapsed, bounded.
//
// The redaction pass is not theater. A notification body is the one piece of
// yc's output that leaves the terminal entirely and can be logged by a desktop
// environment, so it gets the same treatment as the debug log.
func sanitizeNotificationText(value string, limit int) string {
	value = debuglog.Logger{}.Redact(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 {
		return value
	}
	// Clusters, not runes. Display names and chat bodies are routinely a run
	// of ZWJ emoji, and a rune-sliced limit cuts one in half - which leaves a
	// dangling joiner in a string yc hands to a shell-launched notifier, where
	// it binds to whatever the daemon prints next. The limit is a count of
	// user-visible characters either way; only the boundary changes.
	if uniseg.GraphemeClusterCount(value) <= limit {
		return value
	}
	keep, suffix := limit, ""
	if limit > 3 {
		keep, suffix = limit-3, "..."
	}
	var truncated strings.Builder
	graphemes := uniseg.NewGraphemes(value)
	for i := 0; i < keep && graphemes.Next(); i++ {
		truncated.WriteString(graphemes.Str())
	}
	return truncated.String() + suffix
}

// notificationFromMessage builds the notification for a high-signal event, or
// reports that the message is ordinary chat.
//
// The selection is deliberately narrow. Everything here is money, a new
// supporter, or the chat itself ending: events a creator wants pulled out of a
// fast-moving chat. Ordinary messages, mode changes, and moderation are not
// notified, because a notification for every line is a notification for none.
func notificationFromMessage(message youtube.Message, chatLabel string) (SystemNotification, bool) {
	title, ok := notificationTitleForMessage(message)
	if !ok {
		return SystemNotification{}, false
	}
	if label := strings.TrimSpace(chatLabel); label != "" {
		title = title + " · " + label
	}
	return SystemNotification{
		Title:   title,
		Body:    notificationBodyForMessage(message),
		ChatKey: strings.ToLower(strings.TrimSpace(message.LiveChatID)),
		EventID: string(message.Kind),
	}, true
}

func notificationTitleForMessage(message youtube.Message) (string, bool) {
	author := strings.TrimSpace(message.Author.DisplayName)
	if author == "" {
		author = "someone"
	}
	switch message.Kind {
	case youtube.EventKindSuperChat:
		if amount, ok := message.Amount(); ok && amount.Display != "" {
			return "Super Chat " + amount.Display, true
		}
		return "Super Chat", true
	case youtube.EventKindSuperSticker:
		if amount, ok := message.Amount(); ok && amount.Display != "" {
			return "Super Sticker " + amount.Display, true
		}
		return "Super Sticker", true
	case youtube.EventKindGift:
		return "Gift from " + author, true
	case youtube.EventKindNewSponsor:
		return "New member", true
	case youtube.EventKindMemberMilestone:
		return "Member milestone", true
	case youtube.EventKindMembershipGifting:
		return "Gifted memberships", true
	case youtube.EventKindGiftMembershipReceived:
		return "Gift membership received", true
	case youtube.EventKindChatEnded:
		return "Live chat ended", true
	default:
		return "", false
	}
}

func notificationBodyForMessage(message youtube.Message) string {
	author := strings.TrimSpace(message.Author.DisplayName)
	parts := make([]string, 0, 3)
	if author != "" {
		parts = append(parts, author)
	}
	if message.Membership != nil {
		if level := strings.TrimSpace(message.Membership.LevelName); level != "" {
			parts = append(parts, level)
		}
		if message.Membership.Months > 0 {
			parts = append(parts, fmt.Sprintf("%d months", message.Membership.Months))
		}
		if message.Membership.GiftCount > 0 {
			parts = append(parts, fmt.Sprintf("%d gifts", message.Membership.GiftCount))
		}
	}
	head := strings.Join(parts, " · ")
	text := strings.TrimSpace(message.Text)
	if text == "" {
		return head
	}
	if head == "" {
		return text
	}
	return head + ": " + text
}

// notificationSummary is the status line's one-line form of the last
// notification, so the notification is visible even when the desktop swallowed
// it or the terminal only rang a bell.
func notificationSummary(notification SystemNotification) string {
	title := strings.TrimSpace(notification.Title)
	body := strings.TrimSpace(notification.Body)
	switch {
	case body == "":
		return title
	case title == "":
		return body
	case strings.EqualFold(title, body):
		return title
	default:
		return title + ": " + body
	}
}

// shouldNotify decides whether a high-signal event is worth interrupting for.
//
// The rule is focus-aware rather than unconditional: an event in the chat the
// user is already watching, in a focused terminal, with the chat pane in front
// of them, has already been seen. Everything else - a background chat, an
// unfocused terminal, an open overlay, or a focused composer - has not.
func (m shellModel) shouldNotify(message youtube.Message) bool {
	if _, ok := notificationTitleForMessage(message); !ok {
		return false
	}
	if message.Historical {
		// The priming page is a backlog, not news. Notifying for it would
		// mean a burst of alerts for events that happened before yc started.
		return false
	}
	if !m.messageTargetsActiveChat(message) {
		return true
	}
	if !m.terminalFocused {
		return true
	}
	return m.focus != focusChat || m.overlay.open() || m.activeTab != tabChat
}

func (m shellModel) messageTargetsActiveChat(message youtube.Message) bool {
	if m.chats == nil {
		return true
	}
	id := strings.TrimSpace(message.LiveChatID)
	if id == "" {
		return true
	}
	state := m.chats.stateForChatID(id)
	if state == nil {
		return true
	}
	return state.key == m.chats.active
}

// maybeNotify records and dispatches a notification for one message.
//
// Gifted memberships are coalesced inside a short window: they arrive one per
// recipient, and fifty separate desktop toasts for a single gift drop is a
// worse outcome than one that names the count.
func (m *shellModel) maybeNotify(message youtube.Message, now time.Time) tea.Cmd {
	if !m.shouldNotify(message) {
		return nil
	}
	label := ""
	if m.chats != nil {
		if state := m.chats.stateForChatID(message.LiveChatID); state != nil {
			label = state.target.Label()
		}
	}
	notification, ok := notificationFromMessage(message, label)
	if !ok {
		return nil
	}

	if message.Kind == youtube.EventKindGiftMembershipReceived {
		if !m.giftBurstAt.IsZero() && now.Sub(m.giftBurstAt) < giftBurstWindow {
			// Fold this recipient into the burst already on screen and stay
			// silent: the first notification already told the user.
			m.giftBurstCount++
			m.giftBurstAt = now
			if m.lastNotification != nil {
				summary := *m.lastNotification
				summary.Body = fmt.Sprintf("%d gift memberships", m.giftBurstCount)
				m.lastNotification = &summary
			}
			return nil
		}
		m.giftBurstAt = now
		m.giftBurstCount = 1
	}

	m.lastNotification = &notification
	if m.systemNotifier == nil {
		return nil
	}
	notifier := m.systemNotifier
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), desktopNotificationTimeout*2)
		defer cancel()
		// A notification that cannot be delivered is not a chat failure, so
		// the error is deliberately dropped rather than surfaced.
		_ = notifier.Notify(ctx, notification)
		return nil
	}
}

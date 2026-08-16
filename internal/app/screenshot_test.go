package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/rivo/uniseg"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/youtube"
)

// TestWriteDocsScreenshots regenerates the SVG terminal screenshots used by the
// README and the docs site.
//
// It is a generator, not an assertion, so it is skipped unless
// YC_WRITE_SCREENSHOTS=1 - CI must never write into the working tree. Run it
// with:
//
//	YC_WRITE_SCREENSHOTS=1 go test ./internal/app -run TestWriteDocsScreenshots
//
// Every shot is the real View() output of a real shellModel piped through an
// SGR interpreter, never a hand-drawn mockup, so a screenshot cannot drift away
// from what yc actually prints: change a glyph or a status segment and the next
// regeneration shows it.
func TestWriteDocsScreenshots(t *testing.T) {
	if os.Getenv("YC_WRITE_SCREENSHOTS") != "1" {
		t.Skip("set YC_WRITE_SCREENSHOTS=1 to regenerate docs screenshots")
	}
	// View() emits truecolor only when lipgloss believes the terminal can
	// take it, and under `go test` it believes nothing can. Forcing the
	// profile is what makes the captured stream carry color at all.
	restore := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(restore) })

	outDir := filepath.Join("..", "..", "docs", "assets", "screenshots")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", outDir, err)
	}

	for _, shot := range screenshotScenes() {
		svg := ansiToSVG(shot.render(t), shot.title)
		path := filepath.Join(outDir, shot.name+".svg")
		if err := os.WriteFile(path, []byte(svg), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
		t.Logf("wrote %s", path)
	}
}

// TestScreenshotScenesRender is the assertion half: it runs every scene on
// every test run, without writing anything, so a scene that panics or renders
// a ragged frame fails CI instead of waiting to be noticed the next time
// somebody regenerates the images.
func TestScreenshotScenesRender(t *testing.T) {
	for _, shot := range screenshotScenes() {
		t.Run(shot.name, func(t *testing.T) {
			frame := shot.render(t)
			lines := strings.Split(strings.TrimRight(stripOSC(frame), "\n"), "\n")
			if len(lines) < 10 {
				t.Fatalf("scene rendered %d rows, want a full frame", len(lines))
			}
			width := -1
			for i, line := range lines {
				got := visibleWidth(line)
				if width == -1 {
					width = got
				}
				if got != width {
					t.Fatalf("row %d is %d cells wide, want %d: the frame is not rectangular", i, got, width)
				}
			}
		})
	}
}

type screenshotScene struct {
	name   string
	title  string
	render func(t *testing.T) string
}

func screenshotScenes() []screenshotScene {
	return []screenshotScene{
		{name: "chat-grouped", title: "yc — grouped layout", render: sceneGrouped},
		{name: "chat-inline", title: "yc — inline layout", render: sceneInline},
		{name: "theme-picker", title: "yc — theme picker", render: sceneThemePicker},
		{name: "quota-status-bar", title: "yc — quota meter and cadence", render: sceneQuota},
	}
}

// screenshotModel builds a deterministic, populated chat so every screenshot
// shows the same broadcast under different settings.
//
// Everything time-derived is pinned: the frame clock is set explicitly rather
// than left to tick, so the on-air timer, the roster ages, and the quota reset
// render the same figures on every machine and in every timezone.
func screenshotModel(t *testing.T, themeName, layout string, width, height int) shellModel {
	t.Helper()
	cfg := config.Default()
	cfg.Features.AnimationMode = "off"
	cfg.Features.ThemeName = themeName
	cfg.Features.MessageLayout = layout
	cfg.Path = filepath.Join(t.TempDir(), "config.toml")
	cfg.DefaultChats = []string{"dQw4w9WgXcQ"}

	model := newShellModel(cfg, nil)
	model.width, model.height = width, height
	model.splashSkipped = true
	model.sourceDetail = "polling every 5s"
	// A composer with no client behind it reports "no chat source", which is
	// true of a bare model and misleading in a picture of a working session.
	// Nothing is ever read off this client: the scene never runs Init.
	model.client = NewFakeChatClient()
	model.effectiveConfig = cfg

	// now is the instant the frame is painted; the transcript sits behind it.
	// It is built in the local zone so the clock on screen reads the same in
	// every timezone a regeneration might run in - what has to be reproducible
	// here is the rendered text, not the absolute instant.
	now := time.Date(2026, 8, 8, 20, 14, 0, 0, time.Local)
	at := now.Add(-26 * time.Minute)
	model.lastFrameAt = now
	// framesPerSecond counts repaints in the last second, and a model that was
	// never ticked has counted none. Seeding the window keeps the status bar
	// from advertising a stalled renderer.
	for i := range 10 {
		model.frameTimestamps = append(model.frameTimestamps, now.Add(-time.Duration(i)*100*time.Millisecond))
	}
	model.identity = youtube.Identity{
		ChannelID:            "UC-you",
		DisplayName:          "you",
		Handle:               "@you",
		SubscriberCount:      12400,
		SubscriberCountKnown: true,
		Scopes:               []string{"https://www.googleapis.com/auth/youtube.force-ssl"},
	}
	model.identityKnown = true

	state := model.activeChatState()
	state.target = youtube.ChatTarget{VideoID: "dQw4w9WgXcQ", Title: "Terminal Renaissance — live build"}
	state.status = youtube.ConnectionState{Status: youtube.ConnectionConnected, Detail: "polling", At: now}
	state.live = true
	state.liveKnown = true
	state.liveSince = now.Add(-97 * time.Minute)
	state.viewerCount = 1337
	state.viewersKnown = true

	for _, message := range screenshotMessages(at) {
		state.messages = append(state.messages, message)
		state.observeAuthor(message)
	}
	state.recordModeration(youtube.ModerationEvent{
		Type:              youtube.ModerationUserTimedOut,
		TargetChannelID:   "UC-spambot",
		TargetDisplayName: "spambot",
		Duration:          5 * time.Minute,
		At:                at.Add(23 * time.Minute),
	})

	model.quota = screenshotQuota(now, quota.ModeStretched, 3240)
	model.quotaKnown = true
	model.pollInterval = 5 * time.Second
	return model
}

// screenshotQuota is a plausible mid-session ledger: enough units spent that
// the meter has something to say, and a stretched cadence, which is the state
// yc spends most of a long session in.
func screenshotQuota(at time.Time, mode quota.Mode, used int) quota.Snapshot {
	const limit = 10000
	return quota.Snapshot{
		UsedUnits:      used,
		LimitUnits:     limit,
		RemainingUnits: limit - used,
		SearchUsed:     0,
		SearchLimit:    100,
		ByEndpoint: map[string]int{
			"liveChatMessages.list": used - 2,
			"videos.list":           2,
		},
		ResetAt:           time.Date(2026, 8, 9, 0, 0, 0, 0, time.Local),
		EffectiveInterval: 5 * time.Second,
		ServerFloor:       2 * time.Second,
		BudgetFloor:       5 * time.Second,
		Mode:              mode,
		Estimated:         true,
		At:                at,
	}
}

// screenshotMessages is one of every high-signal row: ordinary chat, a mention,
// a moderator, the owner, the Super Chat tiers side by side, a sticker, a
// membership, a milestone, a gifting burst, and a row a timeout emptied.
//
// The paid tiers are shown together rather than singly on purpose: tier color
// and amount-chip width are exactly what breaks quietly, and they are only
// visible as a mistake next to each other.
func screenshotMessages(at time.Time) []youtube.Message {
	viewer := screenshotAuthor("nova_dev", "-nova")
	pixel := screenshotAuthor("pixelwitch", "-pixel")
	mod := screenshotAuthor("streammod", "-mod")
	mod.IsModerator, mod.IsMember = true, true
	mod.MemberLevelName, mod.MemberMonths = "Regulars", 18
	owner := screenshotAuthor("The Channel", "-owner")
	owner.IsOwner, owner.IsVerified = true, true
	tipper := screenshotAuthor("bigfan", "-bigfan")
	sticker := screenshotAuthor("stickerfan", "-sticker")
	member := screenshotAuthor("freshmember", "-fresh")
	longtime := screenshotAuthor("longtimer", "-long")
	longtime.IsMember = true
	longtime.MemberLevelName, longtime.MemberMonths = "Regulars", 24
	spam := screenshotAuthor("spambot", "-spambot")

	messages := []youtube.Message{
		screenshotChat(viewer, "s1", "first time catching this live 👋", at),
		screenshotChat(pixel, "s2", "the terminal setup is unreal", at.Add(time.Minute)),
		screenshotChat(mod, "s3", "keep it friendly in here please", at.Add(2*time.Minute)),
		screenshotChat(owner, "s4", "thanks for hanging out, everyone", at.Add(3*time.Minute)),
	}

	tiers := []struct {
		id      string
		display string
		micros  int64
		tier    int
		comment string
	}{
		{"s5", "$2.00", 2_000_000, 1, "small but sincere"},
		{"s6", "$10.00", 10_000_000, 4, "loving the quota meter"},
		{"s7", "$50.00", 50_000_000, 7, "ship it"},
		{"s8", "$500.00", 500_000_000, 11, "for the terminal renaissance"},
	}
	for index, tier := range tiers {
		messages = append(messages, youtube.Message{
			ID:        tier.id,
			Author:    tipper,
			Timestamp: at.Add(time.Duration(4+index) * time.Minute),
			Text:      tier.comment,
			Fragments: youtube.SplitFragments(tier.comment),
			Kind:      youtube.EventKindSuperChat,
			Type:      youtube.MessageTypePaid,
			RawType:   "superChatEvent",
			SuperChat: &youtube.SuperChatDetails{
				Amount:  youtube.Money{Micros: tier.micros, Currency: "USD", Display: tier.display},
				Tier:    tier.tier,
				Comment: tier.comment,
			},
		})
	}

	messages = append(messages,
		youtube.Message{
			ID:        "s9",
			Author:    sticker,
			Timestamp: at.Add(8 * time.Minute),
			Text:      "a cat typing furiously",
			Kind:      youtube.EventKindSuperSticker,
			Type:      youtube.MessageTypePaid,
			RawType:   "superStickerEvent",
			SuperSticker: &youtube.SuperStickerDetails{
				Amount:   youtube.Money{Micros: 5_000_000, Currency: "EUR", Display: "€5.00"},
				Tier:     3,
				AltText:  "a cat typing furiously",
				Language: "en",
			},
		},
		youtube.Message{
			ID:         "s10",
			Author:     member,
			Timestamp:  at.Add(9 * time.Minute),
			Kind:       youtube.EventKindNewSponsor,
			Type:       youtube.MessageTypeMembership,
			RawType:    "newSponsorEvent",
			Membership: &youtube.MembershipDetails{Kind: youtube.MembershipNew, LevelName: "Regulars"},
		},
		youtube.Message{
			ID:        "s11",
			Author:    longtime,
			Timestamp: at.Add(10 * time.Minute),
			Text:      "two years of this nonsense",
			Kind:      youtube.EventKindMemberMilestone,
			Type:      youtube.MessageTypeMembership,
			RawType:   "memberMilestoneChatEvent",
			Membership: &youtube.MembershipDetails{
				Kind:      youtube.MembershipMilestone,
				LevelName: "Regulars",
				Months:    24,
				Comment:   "two years of this nonsense",
			},
		},
		// A row a timeout emptied. It keeps its gutter mark and its author so
		// the moderation is legible, and never reprints the words.
		youtube.Message{
			ID:        "s12",
			Author:    spam,
			Timestamp: at.Add(11 * time.Minute),
			Kind:      youtube.EventKindText,
			Type:      youtube.MessageTypeChat,
			RawType:   "textMessageEvent",
			Deleted:   true,
		},
		screenshotChat(pixel, "s13", "@nova_dev told you the swatches would sell you ✨", at.Add(12*time.Minute)),
	)
	return messages
}

func screenshotAuthor(name, id string) youtube.Author {
	return youtube.Author{
		ChannelID:   "UC" + id,
		DisplayName: name,
		ChannelURL:  "https://www.youtube.com/channel/UC" + id,
	}
}

func screenshotChat(author youtube.Author, id, text string, at time.Time) youtube.Message {
	return youtube.Message{
		ID:        id,
		Author:    author,
		Timestamp: at,
		Text:      text,
		Fragments: youtube.SplitFragments(text),
		Kind:      youtube.EventKindText,
		Type:      youtube.MessageTypeChat,
		RawType:   "textMessageEvent",
	}
}

func sceneGrouped(t *testing.T) string {
	model := screenshotModel(t, "claude", "grouped", 132, 32)
	return model.View()
}

func sceneInline(t *testing.T) string {
	model := screenshotModel(t, "tokyo-night", "inline", 132, 32)
	return model.View()
}

func sceneThemePicker(t *testing.T) string {
	model := screenshotModel(t, "claude", "grouped", 132, 32)
	model.toggleOverlay(overlayThemePicker)
	for range 9 {
		model.moveOverlaySelection(1)
	}
	return model.View()
}

// sceneQuota shows the meter under pressure: most of the day's units spent, a
// cadence stretched well past the server floor to make the rest last, and the
// projection that follows from both. An idle ledger would show the feature
// without showing the thing it exists for.
func sceneQuota(t *testing.T) string {
	model := screenshotModel(t, "catppuccin-mocha", "inline", 132, 32)
	model.quota = screenshotQuota(model.lastFrameAt, quota.ModeStretched, 8850)
	model.quota.EffectiveInterval = 27500 * time.Millisecond
	model.quota.BudgetFloor = 27500 * time.Millisecond
	model.pollInterval = 27500 * time.Millisecond
	return model.View()
}

// --- ANSI to SVG ---------------------------------------------------------
//
// A minimal SGR interpreter: enough to reproduce what lipgloss emits (24-bit
// foreground/background, bold, italic, strikethrough, reset). Cells are laid
// out on a fixed grid using display width, so a double-width emoji occupies
// exactly the two columns it occupies in a terminal.

const (
	svgCellWidth  = 8.4
	svgLineHeight = 18.0
	svgFontSize   = 14.0
	svgPadding    = 18.0
	svgTitleBar   = 34.0
)

type svgCell struct {
	text          string
	width         int
	fg            string
	bg            string
	bold          bool
	italic        bool
	strikethrough bool
}

func ansiToSVG(rendered, title string) string {
	lines := strings.Split(strings.TrimRight(stripOSC(rendered), "\n"), "\n")
	grid := make([][]svgCell, 0, len(lines))
	columns := 0
	for _, line := range lines {
		cells := parseANSILine(line)
		width := 0
		for _, cell := range cells {
			width += cell.width
		}
		columns = max(columns, width)
		grid = append(grid, cells)
	}
	if columns == 0 {
		columns = 80
	}

	bodyWidth := float64(columns)*svgCellWidth + svgPadding*2
	bodyHeight := float64(len(grid))*svgLineHeight + svgPadding*2 + svgTitleBar

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`+"\n",
		bodyWidth, bodyHeight, bodyWidth, bodyHeight, escapeXML(title))
	b.WriteString(`<defs><filter id="s" x="-4%" y="-4%" width="108%" height="112%"><feDropShadow dx="0" dy="10" stdDeviation="14" flood-color="#000" flood-opacity="0.45"/></filter></defs>` + "\n")

	// Window chrome: a rounded frame and the three traffic-light dots, so the
	// image reads as a terminal window rather than as a wall of text.
	frame := backgroundOf(grid)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%.0f" height="%.0f" rx="12" fill="%s" filter="url(#s)"/>`+"\n", bodyWidth, bodyHeight, frame)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%.0f" height="%.0f" rx="12" fill="#000" fill-opacity="0.22"/>`+"\n", bodyWidth, svgTitleBar)
	for i, color := range []string{"#ff5f57", "#febc2e", "#28c840"} {
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="6" fill="%s"/>`+"\n", 20+float64(i)*20, svgTitleBar/2, color)
	}
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-family="ui-monospace,SFMono-Regular,Menlo,Consolas,monospace" font-size="12" fill="#ffffff" fill-opacity="0.55" text-anchor="middle">%s</text>`+"\n",
		bodyWidth/2, svgTitleBar/2+4, escapeXML(title))

	fmt.Fprintf(&b, `<g font-family="ui-monospace,SFMono-Regular,Menlo,Consolas,'DejaVu Sans Mono',monospace" font-size="%.0f">`+"\n", svgFontSize)
	for row, cells := range grid {
		y := svgTitleBar + svgPadding + float64(row)*svgLineHeight
		column := 0
		// Backgrounds first, so text always paints on top of its own chip.
		for _, cell := range cells {
			if cell.bg != "" && cell.width > 0 {
				fmt.Fprintf(&b, `<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" fill="%s"/>`,
					svgPadding+float64(column)*svgCellWidth, y, float64(cell.width)*svgCellWidth, svgLineHeight, cell.bg)
			}
			column += cell.width
		}
		column = 0
		for _, cell := range cells {
			// A whitespace-only run contributes nothing beyond the background
			// rect already drawn for it above.
			if strings.TrimSpace(cell.text) == "" {
				column += cell.width
				continue
			}
			attrs := ""
			if cell.bold {
				attrs += ` font-weight="600"`
			}
			if cell.italic {
				attrs += ` font-style="italic"`
			}
			if cell.strikethrough {
				attrs += ` text-decoration="line-through"`
			}
			fill := cell.fg
			if fill == "" {
				fill = "#e6e6e6"
			}
			// textLength pins each run to an exact cell count. Without it the
			// font's natural advance drifts from the grid over a long line and
			// the right-hand columns walk off the edge.
			fmt.Fprintf(&b, `<text x="%.2f" y="%.2f" fill="%s"%s textLength="%.2f" lengthAdjust="spacingAndGlyphs" xml:space="preserve">%s</text>`,
				svgPadding+float64(column)*svgCellWidth, y+svgFontSize, fill, attrs,
				float64(cell.width)*svgCellWidth, escapeXML(cell.text))
			column += cell.width
		}
		b.WriteString("\n")
	}
	b.WriteString("</g>\n</svg>\n")
	return b.String()
}

// backgroundOf picks the most common background color as the window fill, so
// the frame matches whatever theme the scene was rendered in.
func backgroundOf(grid [][]svgCell) string {
	counts := map[string]int{}
	for _, row := range grid {
		for _, cell := range row {
			if cell.bg != "" {
				counts[cell.bg] += cell.width
			}
		}
	}
	best, bestCount := "#12131a", 0
	for color, count := range counts {
		if count > bestCount {
			best, bestCount = color, count
		}
	}
	return best
}

// stripOSC removes OSC sequences. yc emits OSC 11 to hand the terminal its
// theme background; the sequence carries no glyphs and would otherwise be
// parsed as text.
func stripOSC(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); {
		if value[i] == 0x1b && i+1 < len(value) && value[i+1] == ']' {
			j := i + 2
			for j < len(value) {
				if value[j] == 0x07 {
					j++
					break
				}
				if value[j] == 0x1b && j+1 < len(value) && value[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
			continue
		}
		b.WriteByte(value[i])
		i++
	}
	return b.String()
}

// visibleWidth is the display width of a styled line, used by the rectangularity
// assertion.
func visibleWidth(line string) int {
	width := 0
	for _, cell := range parseANSILine(line) {
		width += cell.width
	}
	return width
}

func parseANSILine(line string) []svgCell {
	var cells []svgCell
	var current svgCell
	flush := func() {
		if current.text != "" {
			cells = append(cells, current)
		}
		current.text = ""
		current.width = 0
	}

	state := svgCell{}
	runes := []rune(line)
	for i := 0; i < len(runes); {
		if runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '[' {
			j := i + 2
			for j < len(runes) && runes[j] != 'm' && !isANSIFinal(runes[j]) {
				j++
			}
			if j < len(runes) && runes[j] == 'm' {
				flush()
				applySGR(&state, string(runes[i+2:j]))
				current.fg, current.bg = state.fg, state.bg
				current.bold, current.italic, current.strikethrough = state.bold, state.italic, state.strikethrough
			}
			i = j + 1
			continue
		}
		// Consume one grapheme cluster so an emoji or a combining sequence
		// stays whole and keeps its true display width.
		cluster, _, _, _ := uniseg.FirstGraphemeClusterInString(string(runes[i:]), -1)
		if cluster == "" {
			cluster = string(runes[i])
		}
		current.fg, current.bg = state.fg, state.bg
		current.bold, current.italic, current.strikethrough = state.bold, state.italic, state.strikethrough
		current.text += cluster
		current.width += ansi.StringWidth(cluster)
		i += len([]rune(cluster))
	}
	flush()
	return cells
}

func isANSIFinal(r rune) bool {
	return r >= 0x40 && r <= 0x7e && r != '[' && r != ';'
}

func applySGR(state *svgCell, params string) {
	if params == "" {
		params = "0"
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "", "0":
			state.fg, state.bg = "", ""
			state.bold, state.italic, state.strikethrough = false, false, false
		case "1":
			state.bold = true
		case "3":
			state.italic = true
		case "9":
			state.strikethrough = true
		case "22":
			state.bold = false
		case "23":
			state.italic = false
		case "29":
			state.strikethrough = false
		case "39":
			state.fg = ""
		case "49":
			state.bg = ""
		case "38", "48":
			if i+4 < len(parts) && parts[i+1] == "2" {
				color := rgbHex(parts[i+2], parts[i+3], parts[i+4])
				if parts[i] == "38" {
					state.fg = color
				} else {
					state.bg = color
				}
				i += 4
			}
		}
	}
}

func rgbHex(r, g, b string) string {
	value := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return 0
		}
		return min(n, 255)
	}
	return fmt.Sprintf("#%02x%02x%02x", value(r), value(g), value(b))
}

func escapeXML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}

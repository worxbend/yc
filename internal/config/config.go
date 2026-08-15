package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/worxbend/yc/internal/theme"
)

// Redacted is the placeholder printed in place of any non-empty secret value.
const Redacted = "[redacted]"

// DefaultScrollbackLimit is the per-chat message cap applied when no
// scrollback_limit is configured. Chat is unbounded upstream and every retained
// message is re-rendered on each repaint, so an uncapped buffer degrades frame
// time over a long session until the UI cannot keep up with the animation tick.
const DefaultScrollbackLimit = 2000

// DefaultDailyQuotaUnits is the YouTube Data API's default per-project daily
// allowance. It is configurable because a project can be granted more.
const DefaultDailyQuotaUnits = 10000

// DefaultSearchQuotaCalls is the separate daily allowance for search.list under
// the granular quota system introduced 2026-06-01.
const DefaultSearchQuotaCalls = 100

// Config is the effective application configuration after merging defaults,
// config file values, environment variables, and CLI overrides.
//
// The file format is flat "key = value" lines with no nested tables, parsed by
// a hand-rolled line scanner. The toml tags below are the authoritative file
// key names and the env tags the authoritative variable names; both are read by
// the parser and by RedactedString so a key can never drift from its
// documentation.
type Config struct {
	// Path is the config file this Config was loaded from. It is not itself
	// a config key.
	Path string `toml:"-" env:"-"`

	Google       GoogleConfig  `toml:",inline" env:",inline"`
	YouTube      YouTubeConfig `toml:",inline" env:",inline"`
	DefaultChats []string      `toml:"default_chats" env:"YC_DEFAULT_CHATS"`
	Features     FeatureConfig `toml:",inline" env:",inline"`
	Quota        QuotaConfig   `toml:",inline" env:",inline"`
	Logging      LoggingConfig `toml:",inline" env:",inline"`
	Debug        DebugConfig   `toml:",inline" env:",inline"`
}

// GoogleConfig holds the installed-app OAuth credentials.
//
// ClientSecret, AccessToken, and RefreshToken are secrets: they must never be
// printed, logged, or error-wrapped, and WriteNonSecretFile must never create
// or update their keys.
type GoogleConfig struct {
	ClientID string `toml:"google_client_id" env:"YC_GOOGLE_CLIENT_ID,GOOGLE_CLIENT_ID"`
	// ClientSecret is optional: an installed app cannot keep a secret, so
	// the desktop flow works with PKCE alone. Setting it enables unattended
	// refresh.
	ClientSecret string `toml:"google_client_secret" env:"YC_GOOGLE_CLIENT_SECRET,GOOGLE_CLIENT_SECRET" secret:"true"`
	AccessToken  string `toml:"google_access_token" env:"YC_GOOGLE_ACCESS_TOKEN,GOOGLE_ACCESS_TOKEN" secret:"true"`
	RefreshToken string `toml:"google_refresh_token" env:"YC_GOOGLE_REFRESH_TOKEN,GOOGLE_REFRESH_TOKEN" secret:"true"`
	// RedirectURL overrides the loopback redirect. An empty value means
	// "listen on 127.0.0.1 on an ephemeral port", which is what the
	// installed-app flow wants.
	RedirectURL string `toml:"google_redirect_url" env:"YC_GOOGLE_REDIRECT_URL,GOOGLE_REDIRECT_URL"`
}

// YouTubeConfig holds the API-key read path and the user's own channel.
type YouTubeConfig struct {
	// APIKey enables the zero-friction read-only mode: an API key is an
	// accepted caller identity for liveChatMessages.list on public chats.
	// It is a secret.
	APIKey string `toml:"youtube_api_key" env:"YC_YOUTUBE_API_KEY" secret:"true"`
	// ChannelID is the user's own channel. It is resolved from the token
	// when possible; the configured value is a stale-value fallback and is
	// warned about when it disagrees with the resolved identity.
	ChannelID string `toml:"youtube_channel_id" env:"YC_YOUTUBE_CHANNEL_ID"`
}

// FeatureConfig holds display and interaction preferences. Every value is
// validated by normalizing to a known mode rather than by rejecting input, so
// an unrecognized value degrades instead of failing startup.
type FeatureConfig struct {
	EnableMouse bool `toml:"enable_mouse" env:"YC_ENABLE_MOUSE"`
	// AvatarMode is "initials" or "off". There is no image path: YouTube
	// profile images are recorded for diagnostics and never fetched.
	AvatarMode string `toml:"avatar_mode" env:"YC_AVATAR_MODE"`
	// AnimationMode is "fast", "reduced", or "off".
	AnimationMode string `toml:"animation_mode" env:"YC_ANIMATION_MODE"`
	// ThemeName is a preset name or "custom".
	ThemeName string `toml:"theme_name" env:"YC_THEME_NAME"`
	// ThemeCustom is used only when ThemeName is "custom". Its nine fields
	// map to theme_background, theme_foreground, theme_accent, theme_muted,
	// theme_border, theme_surface, theme_warning, theme_error, and
	// theme_success (env YC_THEME_*).
	ThemeCustom theme.Palette `toml:",inline" env:",inline"`
	// MessageLayout is "inline", "grouped", or "compact".
	MessageLayout string `toml:"message_layout" env:"YC_MESSAGE_LAYOUT"`
	// BadgeMode is "glyph", "text", or "off".
	BadgeMode string `toml:"badge_mode" env:"YC_BADGE_MODE"`
	// HighlightEmoji draws emoji and channel shortcodes on a tinted chip.
	HighlightEmoji bool `toml:"highlight_emoji" env:"YC_HIGHLIGHT_EMOJI"`
	// FullUsername renders "DisplayName (handle)" when the two differ.
	FullUsername bool `toml:"full_username" env:"YC_FULL_USERNAME"`
	// SidebarWidth and ActivityWidth override the responsive default width
	// of the chats sidebar and the activity column, in terminal cells. Zero
	// means "size automatically from the terminal width". Values are clamped
	// to a usable range rather than rejected, so a config that asks for more
	// than the terminal can afford degrades instead of failing.
	SidebarWidth  int `toml:"sidebar_width" env:"YC_SIDEBAR_WIDTH"`
	ActivityWidth int `toml:"activity_width" env:"YC_ACTIVITY_WIDTH"`
	// ScrollbackLimit caps how many messages each chat retains. Zero or
	// negative disables trimming.
	ScrollbackLimit int `toml:"scrollback_limit" env:"YC_SCROLLBACK_LIMIT"`
	// StreamStatusMode is "auto" or "off" and controls the LIVE indicator
	// and viewer-count refresh.
	StreamStatusMode string `toml:"stream_status_mode" env:"YC_STREAM_STATUS_MODE"`
	// EmojiAutocompleteMode is "auto" or "off". The emoji picker is a
	// built-in Unicode set and never needs credentials.
	EmojiAutocompleteMode string `toml:"emoji_autocomplete_mode" env:"YC_EMOJI_AUTOCOMPLETE_MODE"`
}

// QuotaConfig holds the poll-cadence and quota-accounting knobs. This block has
// no twi analogue: it exists because the YouTube Data API's 10,000-unit daily
// allowance, spent at the server-advised cadence, is exhausted in under three
// hours, so cadence is a budgeting decision rather than a preference.
type QuotaConfig struct {
	// PollIntervalMode is "auto" (obey pollingIntervalMillis under the
	// budget floor), "economy" (floor at 5s), or "off" (manual refresh only).
	PollIntervalMode string `toml:"poll_interval_mode" env:"YC_POLL_INTERVAL_MODE"`
	// PollIntervalFloorMS is an absolute local floor. yc never polls faster
	// than this, and never faster than pollingIntervalMillis either.
	PollIntervalFloorMS int `toml:"poll_interval_floor_ms" env:"YC_POLL_INTERVAL_FLOOR_MS"`
	// PollIntervalCeilingMS caps the stretched cadence so a nearly exhausted
	// budget does not stall chat entirely. Zero means no ceiling.
	PollIntervalCeilingMS int `toml:"poll_interval_ceiling_ms" env:"YC_POLL_INTERVAL_CEILING_MS"`
	// DailyQuotaUnits is the project's daily allowance.
	DailyQuotaUnits int `toml:"daily_quota_units" env:"YC_DAILY_QUOTA_UNITS"`
	// SearchQuotaCalls is the separate daily search.list allowance.
	SearchQuotaCalls int `toml:"search_quota_calls" env:"YC_SEARCH_QUOTA_CALLS"`
	// ReservePercent stops polling at this remaining share so sends and
	// moderation still work after reads are cut off.
	ReservePercent int `toml:"quota_reserve_percent" env:"YC_QUOTA_RESERVE_PERCENT"`
	// SessionHours narrows the budget horizon to the next N hours instead of
	// the Pacific reset, trading tomorrow's budget for real-time cadence
	// tonight. Zero means "budget to the reset".
	SessionHours int `toml:"session_hours" env:"YC_SESSION_HOURS"`
	// FollowServerCadence disables the budget floor entirely, for users with
	// a raised quota.
	FollowServerCadence bool `toml:"follow_server_cadence" env:"YC_FOLLOW_SERVER_CADENCE"`
	// AllowSearch opts in to the search.list resolution path, which spends
	// one of the 100 daily searches.
	AllowSearch bool `toml:"allow_search" env:"YC_ALLOW_SEARCH"`

	// Costs overrides the per-method unit cost table. Google publishes no
	// costs for live chat methods, so every default here is a
	// community-observed estimate and yc labels it as one.
	Costs QuotaCostConfig `toml:",inline" env:",inline"`
}

// QuotaCostConfig is the config-overridable per-method cost table. A wrong
// published constant must be a one-line fix, not a rebuild.
type QuotaCostConfig struct {
	List         int `toml:"quota_cost_list" env:"YC_QUOTA_COST_LIST"`
	Insert       int `toml:"quota_cost_insert" env:"YC_QUOTA_COST_INSERT"`
	Delete       int `toml:"quota_cost_delete" env:"YC_QUOTA_COST_DELETE"`
	BansInsert   int `toml:"quota_cost_bans_insert" env:"YC_QUOTA_COST_BANS_INSERT"`
	BansDelete   int `toml:"quota_cost_bans_delete" env:"YC_QUOTA_COST_BANS_DELETE"`
	VideosList   int `toml:"quota_cost_videos_list" env:"YC_QUOTA_COST_VIDEOS_LIST"`
	ChannelsList int `toml:"quota_cost_channels_list" env:"YC_QUOTA_COST_CHANNELS_LIST"`
	SearchList   int `toml:"quota_cost_search_list" env:"YC_QUOTA_COST_SEARCH_LIST"`
}

// Chat log defaults. See internal/chatlog for the file format.
const (
	// DefaultChatLogMaxBytes rotates a chat log file at 10 MB, roughly a
	// full day of very busy chat, so an ordinary session is one file.
	DefaultChatLogMaxBytes = 10 << 20
	// DefaultChatLogMaxFiles keeps the five newest log files.
	DefaultChatLogMaxFiles = 5
)

// LoggingConfig controls the opt-in chat log: an append-only JSON Lines file
// of normalized chat events, written per session and rotated by size.
//
// This is separate from DebugConfig on purpose. The debug log records what yc
// did (requests, states, redacted errors) for bug reports; the chat log
// records what the chat said, for the user's own archive and for
// `yc export superchats`.
type LoggingConfig struct {
	// ChatLogEnabled turns chat logging on. Default off: writing chat to
	// disk is a privacy decision the user has to make, never a default.
	ChatLogEnabled bool `toml:"chat_logging" env:"YC_CHAT_LOG"`
	// ChatLogDir overrides where log files are created. Empty means
	// "chatlog" under the platform cache directory.
	ChatLogDir string `toml:"chat_log_dir" env:"YC_CHAT_LOG_DIR"`
	// ChatLogMaxBytes rotates the current file once it exceeds this size.
	ChatLogMaxBytes int `toml:"chat_log_max_bytes" env:"YC_CHAT_LOG_MAX_BYTES"`
	// ChatLogMaxFiles is how many rotated files are kept, newest first,
	// current file included.
	ChatLogMaxFiles int `toml:"chat_log_max_files" env:"YC_CHAT_LOG_MAX_FILES"`
}

// DebugConfig controls opt-in JSON-line diagnostics.
type DebugConfig struct {
	Enabled bool   `toml:"debug_logging" env:"YC_DEBUG_LOG"`
	LogPath string `toml:"debug_log_path" env:"YC_DEBUG_LOG_PATH"`
}

// Overrides are the CLI-supplied values that outrank environment and file.
type Overrides struct {
	ConfigPath string
	// Chats accumulates repeated --chat flags and comma-separated --chats
	// values. An empty set is a supported start.
	Chats []string
	// LiveChatID skips target resolution entirely and costs no quota.
	LiveChatID string
	// DebugLogSet distinguishes "--debug-log absent" from "--debug-log=false",
	// so the flag can override debug_logging = true.
	DebugLogSet     bool
	DebugLogEnabled bool
	DebugLogPath    string
}

// Default returns the built-in configuration before any file, environment, or
// override is applied.
func Default() Config {
	return Config{
		Features: FeatureConfig{
			EnableMouse:           true,
			AvatarMode:            "initials",
			AnimationMode:         "fast",
			ThemeName:             theme.DefaultPaletteName,
			MessageLayout:         "inline",
			BadgeMode:             "glyph",
			HighlightEmoji:        true,
			FullUsername:          false,
			ScrollbackLimit:       DefaultScrollbackLimit,
			StreamStatusMode:      "auto",
			EmojiAutocompleteMode: "auto",
		},
		Logging: LoggingConfig{
			ChatLogMaxBytes: DefaultChatLogMaxBytes,
			ChatLogMaxFiles: DefaultChatLogMaxFiles,
		},
		Quota: QuotaConfig{
			PollIntervalMode:    "auto",
			PollIntervalFloorMS: 1000,
			DailyQuotaUnits:     DefaultDailyQuotaUnits,
			SearchQuotaCalls:    DefaultSearchQuotaCalls,
			ReservePercent:      10,
			Costs: QuotaCostConfig{
				List:         5,
				Insert:       50,
				Delete:       50,
				BansInsert:   50,
				BansDelete:   50,
				VideosList:   1,
				ChannelsList: 1,
				SearchList:   1,
			},
		},
	}
}

// Load returns the effective config. Precedence, highest first, is
// flags/overrides > environment > config file > defaults. Empty environment
// values are ignored so a blank CI variable cannot mask a file value.
//
// Saved credentials sit below all of these and are applied by internal/cli
// after Load, filling only fields that are still empty: this package never
// touches the credential store, so a config load can never be the thing that
// reads a token off disk.
func Load(environ []string, overrides Overrides) (Config, error) {
	cfg := Default()
	path, err := effectiveConfigPath(overrides)
	if err != nil {
		return Config{}, err
	}
	cfg.Path = path

	if err := applyFile(&cfg, path); err != nil {
		return Config{}, err
	}
	applyEnv(&cfg, environ)
	applyOverrides(&cfg, overrides)
	return cfg, nil
}

// LoadEnvOnly is Load without reading the config file. It backs diagnostic and
// setup paths that must not depend on an existing file: `yc doctor` has to keep
// working when the config is exactly what is broken.
func LoadEnvOnly(environ []string, overrides Overrides) (Config, error) {
	cfg := Default()
	path, err := effectiveConfigPath(overrides)
	if err != nil {
		return Config{}, err
	}
	cfg.Path = path

	applyEnv(&cfg, environ)
	applyOverrides(&cfg, overrides)
	return cfg, nil
}

// effectiveConfigPath resolves the config file the run should use.
func effectiveConfigPath(overrides Overrides) (string, error) {
	if path := strings.TrimSpace(overrides.ConfigPath); path != "" {
		return path, nil
	}
	return DefaultPath()
}

// DefaultPath returns the platform config file path, <UserConfigDir>/yc/config.toml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "yc", "config.toml"), nil
}

// DefaultCacheDir returns the platform cache directory, <UserCacheDir>/yc.
func DefaultCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "yc"), nil
}

// applyFile merges the flat config file into cfg. A missing file is not an
// error: running yc before writing a config is the normal first experience.
//
// Unknown keys are ignored rather than rejected so a config written by a newer
// build still loads in an older one; a line that is not "key = value" at all is
// an error, because that is a typo rather than a version skew.
func applyFile(cfg *Config, path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	index := bindingIndex(cfg)
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected key = value", RedactDisplayValue(path), lineNo)
		}
		if b, known := index[strings.TrimSpace(key)]; known {
			b.apply(trimValue(value))
		}
	}
	return scanner.Err()
}

// applyEnv merges environment variables into cfg.
//
// Each key takes the first of its aliases that is set to a non-empty value, so
// the YC_-prefixed name always beats the unprefixed Google alias and the result
// does not depend on map iteration order. An empty value is skipped entirely
// rather than treated as "set to empty", because a blank CI variable must not
// mask a configured file value.
func applyEnv(cfg *Config, environ []string) {
	env := environMap(environ)
	for _, b := range bindingsFor(cfg) {
		for _, key := range b.EnvKeys {
			value, ok := env[key]
			if !ok || strings.TrimSpace(value) == "" {
				continue
			}
			b.apply(value)
			break
		}
	}
}

// environMap indexes an os.Environ-shaped slice.
func environMap(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, entry := range environ {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	return env
}

// applyOverrides merges the CLI-supplied values that outrank everything else.
//
// Overrides.LiveChatID is deliberately not merged here: it names one chat to
// open rather than a stored preference, and internal/cli turns it into a target
// directly so it never round-trips through the config file.
func applyOverrides(cfg *Config, overrides Overrides) {
	if len(overrides.Chats) > 0 {
		cfg.DefaultChats = normalizeChats(overrides.Chats)
	}
	if overrides.DebugLogSet {
		cfg.Debug.Enabled = overrides.DebugLogEnabled
	}
	if path := strings.TrimSpace(overrides.DebugLogPath); path != "" {
		cfg.Debug.LogPath = path
	}
}

// ResolveTheme returns the effective palette: the named preset, or ThemeCustom
// when theme_name is "custom".
func (c Config) ResolveTheme() theme.Palette {
	palette, _ := theme.ResolvePalette(c.Features.ThemeName, c.Features.ThemeCustom)
	return palette
}

// String describes the config without printing anything it holds.
//
// This is not decoration. GoogleConfig.ClientSecret, AccessToken, RefreshToken,
// and YouTubeConfig.APIKey are plain strings - the config layer stores what the
// user typed, and auth.Secret only wraps a value once it crosses into
// internal/auth - so a single %v, %+v, or %#v of a Config prints the client
// secret, both tokens, and the API key on one line. Config is passed by value
// into most of internal/cli and internal/app, which makes it the value most
// likely to reach a fmt verb by accident. youtube.ClientConfig, youtube.Client,
// and cli.credentialHolder all carry this same guard for the same reason; use
// RedactedString for the deliberate `yc config show` rendering.
func (c Config) String() string {
	return "config.Config{path: " + RedactDisplayValue(c.Path) +
		", theme: " + c.Features.ThemeName +
		", chats: " + strconv.Itoa(len(c.DefaultChats)) + "}"
}

// GoString mirrors String for %#v, which otherwise reflects into the fields.
func (c Config) GoString() string { return c.String() }

// String and GoString keep the credential-bearing sub-structs safe on their
// own, so printing one field of a Config is no more dangerous than printing all
// of it.
func (g GoogleConfig) String() string {
	return "config.GoogleConfig{client_id: " + present(g.ClientID) +
		", client_secret: " + present(g.ClientSecret) +
		", access_token: " + present(g.AccessToken) +
		", refresh_token: " + present(g.RefreshToken) + "}"
}

// GoString mirrors String for %#v.
func (g GoogleConfig) GoString() string { return g.String() }

// String reports the API key by presence only.
func (y YouTubeConfig) String() string {
	return "config.YouTubeConfig{api_key: " + present(y.APIKey) +
		", channel_id: " + y.ChannelID + "}"
}

// GoString mirrors String for %#v.
func (y YouTubeConfig) GoString() string { return y.String() }

// present reports whether a credential is configured without revealing it.
func present(value string) string {
	if strings.TrimSpace(value) == "" {
		return "missing"
	}
	return "present"
}

// RedactedString renders every configuration key for display. Secret values
// become Redacted when non-empty and stay empty when unset, so an operator can
// tell "not configured" from "configured but hidden".
func (c Config) RedactedString() string {
	lines := []string{"path = " + strconv.Quote(RedactDisplayValue(c.Path))}
	for _, b := range bindingsFor(&c) {
		lines = append(lines, b.TOMLKey+" = "+b.display())
	}
	return strings.Join(lines, "\n") + "\n"
}

// WriteNonSecretFile rewrites the known non-secret keys of path in place,
// preserving unknown lines and existing secret lines untouched, appending any
// missing keys, and writing atomically through a temp file with mode 0600 in a
// 0700 directory.
//
// It must never create or update google_client_secret, google_access_token,
// google_refresh_token, or youtube_api_key. Those are marked secret in the
// struct tags and are filtered here from the same tag walk the parser uses, so
// a newly added secret is excluded by declaring it rather than by remembering
// to update this function.
func WriteNonSecretFile(path string, cfg Config) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path is required")
	}

	updates := map[string]string{}
	order := []string{}
	for _, b := range bindingsFor(&cfg) {
		if b.Secret {
			continue
		}
		updates[b.TOMLKey] = b.literal()
		order = append(order, b.TOMLKey)
	}
	return writeFlatConfigUpdates(path, order, updates)
}

// writeFlatConfigUpdates rewrites known keys in place and appends the rest.
//
// Rewriting in place rather than regenerating the file is what lets a user keep
// comments, ordering, and any key this build does not know about - including
// the secret keys, which are read but never written.
func writeFlatConfigUpdates(path string, order []string, updates map[string]string) error {
	var existing string
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		existing = string(data)
	case errors.Is(err, os.ErrNotExist):
	default:
		return err
	}

	seen := map[string]bool{}
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(existing))
	for scanner.Scan() {
		line := scanner.Text()
		if key, ok := configLineKey(line); ok {
			if value, exists := updates[key]; exists {
				lines = append(lines, key+" = "+value)
				seen[key] = true
				continue
			}
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	if len(lines) > 0 {
		lines = append(lines, "")
	}
	for _, key := range order {
		if !seen[key] {
			lines = append(lines, key+" = "+updates[key])
		}
	}

	output := strings.Join(lines, "\n")
	if output != "" && !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	return writeFilePrivate(path, []byte(output))
}

// configLineKey returns the key a config line assigns, if it assigns one.
func configLineKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	key, _, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	return key, true
}

// writeFilePrivate replaces path atomically with owner-only permissions. The
// config file is not supposed to hold secrets, but a user may still have put a
// client secret in it, so it is written as if it does.
func writeFilePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// trimValue strips the quoting and bracket forms the flat format tolerates, so
// a value written by hand as `"fast"`, 'fast', or ["a", "b"] all parse.
func trimValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"`)
	value = strings.Trim(value, `'`)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimPrefix(strings.TrimSuffix(value, "]"), "[")
		value = strings.ReplaceAll(value, `"`, "")
		value = strings.ReplaceAll(value, `'`, "")
	}
	return value
}

// splitList parses a comma-separated list value.
func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return normalizeChats(strings.Split(value, ","))
}

// normalizeChats trims and drops empty entries.
//
// Unlike twi, nothing is stripped from the value: a yc chat may be named by a
// video ID, a watch URL, an @handle, or a channel ID, and the leading "@" of a
// handle is meaningful. Classification happens in youtube.ParseChatTarget, not
// here.
func normalizeChats(values []string) []string {
	chats := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			chats = append(chats, trimmed)
		}
	}
	if len(chats) == 0 {
		return nil
	}
	return chats
}

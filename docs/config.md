# Configuration

This document is the key-by-key reference for `yc`'s configuration. For the
credential model, read [auth.md](auth.md); for the quota block specifically,
read [quota.md](quota.md); for symptoms and fixes, read
[troubleshooting.md](troubleshooting.md).

Nothing here binds a key — the keymap is fixed and generated from source. See
[keybindings.md](keybindings.md).

## The File Format

A flat `key = value` file parsed by a hand-rolled line scanner.

- **Nested TOML tables are not supported.** Keep the file flat.
- Blank lines and `#` comments are ignored.
- Values may be bare, `"double quoted"`, `'single quoted'`, or a
  `["bracketed", "list"]` for list keys.
- **Unknown keys are ignored**, so a file written by a newer build still loads in
  an older one.
- A line that is not `key = value` at all is an **error** — that is a typo rather
  than version skew.
- `yc setup` and `yc profile set` rewrite known non-secret keys **in place**,
  preserving your comments, your ordering, and any key this build does not know
  about. Files are written atomically with mode `0600` in a `0700` directory.

## Paths

```text
$XDG_CONFIG_HOME/yc/config.toml
~/.config/yc/config.toml
```

```sh
yc config path     # the effective default path
yc config show     # the effective config, secrets redacted
yc chat --config /some/other/config.toml   # per-run override; a subcommand flag,
                                           # not a global one
```

The cache directory holds the persisted quota ledger and small diagnostic
records — `yc` never downloads asset bytes:

```text
$XDG_CACHE_HOME/yc
~/.cache/yc
        └── quota/<credential-fingerprint>-<YYYY-MM-DD>.json
```

The private credential file is separate from all of these and is Unix-only:

```text
$XDG_CONFIG_HOME/yc/credentials.json
```

## Precedence

Highest priority first:

1. CLI flags.
2. Environment variables.
3. The config file.
4. Saved credentials, filling only fields that are still empty.
5. Built-in defaults.

An **empty environment value is ignored entirely** rather than treated as "set
to empty", which keeps a blank CI variable from masking a configured file value.

Where both a canonical `YC_`-prefixed variable and an unprefixed alias exist, the
canonical name wins regardless of map ordering.

## Credentials

| Key | Env | Secret | Purpose |
| --- | --- | --- | --- |
| `google_client_id` | `YC_GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_ID` | No | OAuth client ID from a **Desktop app** client. Required by `yc login`. |
| `google_client_secret` | `YC_GOOGLE_CLIENT_SECRET`, `GOOGLE_CLIENT_SECRET` | **Yes** | Optional. An installed app authenticates with PKCE; set this only if your client was issued a secret and refresh needs it. |
| `google_access_token` | `YC_GOOGLE_ACCESS_TOKEN`, `GOOGLE_ACCESS_TOKEN` | **Yes** | OAuth access token. Normally supplied by `yc login` through the credential file instead. |
| `google_refresh_token` | `YC_GOOGLE_REFRESH_TOKEN`, `GOOGLE_REFRESH_TOKEN` | **Yes** | Refresh token, for unattended renewal. |
| `google_redirect_url` | `YC_GOOGLE_REDIRECT_URL`, `GOOGLE_REDIRECT_URL` | No | Loopback callback override. Empty means "bind 127.0.0.1 on an ephemeral port", which is what the installed-app flow wants. |
| `youtube_api_key` | `YC_YOUTUBE_API_KEY` | **Yes** | Enables read-only mode against public live chats with no OAuth at all. |
| `youtube_channel_id` | `YC_YOUTUBE_CHANNEL_ID` | No | Your own channel. Normally resolved from the token; the configured value is a stale-value fallback that `yc` warns about when it disagrees with the resolved identity. |

Secrets print as `[redacted]` in `yc config show` when set and as `""` when
unset, so "not configured" is distinguishable from "configured but hidden".
`yc setup`, `yc profile set`, and `yc login --write-default-config` never create
or update a secret key — that exclusion comes from the struct tags themselves, so
a newly added secret is excluded by declaring it rather than by remembering to.

## Chats

| Key | Env | Purpose |
| --- | --- | --- |
| `default_chats` | `YC_DEFAULT_CHATS` | Comma-separated chats to open at startup. Each may be a video ID, a watch/live/shorts URL, a `youtu.be` link, an `@handle`, or a channel ID. |

CLI equivalents, which all accumulate into the same list:

```sh
yc chat --chat <target>          # repeatable
yc chat --chats a,b              # comma-separated
yc chat --video <id-or-url>      # alias spelling
yc chat --channel @handle        # alias spelling
yc chat --live-chat-id <id>      # skips resolution; costs zero quota
```

Nothing is stripped from a value — the leading `@` of a handle is meaningful.
Classification happens in `youtube.ParseChatTarget`, not in the config parser, so
a config load can never fail on a chat name.

Chats opened at runtime from the picker are session-only and are not written back
to config.

## Display

| Key | Env | Default | Values |
| --- | --- | --- | --- |
| `enable_mouse` | `YC_ENABLE_MOUSE` | `true` | Terminal mouse reporting. Currently wheel scrolling only. `--no-mouse` disables it for one run. |
| `avatar_mode` | `YC_AVATAR_MODE` | `initials` | `off`, `initials`. There is no image mode. |
| `animation_mode` | `YC_ANIMATION_MODE` | `fast` | `off`, `reduced`, `fast`. |
| `theme_name` | `YC_THEME_NAME` | `claude` | One of 58 presets, or `custom`. `yc profile list` prints them; [themes.md](themes.md) lists every palette. |
| `theme_background` … `theme_success` | `YC_THEME_BACKGROUND` … `YC_THEME_SUCCESS` | empty | Nine hex roles: background, foreground, accent, muted, border, surface, warning, error, success. Applied only when `theme_name = "custom"`; an unset role falls back to no styling. |
| `message_layout` | `YC_MESSAGE_LAYOUT` | `inline` | `inline` (author and text on one row), `grouped` (author header with text indented beneath, repeated authors collapsed), `compact` (text only). Rendered examples per event kind: [events.md](events.md). |
| `badge_mode` | `YC_BADGE_MODE` | `glyph` | `glyph` (`◉ ⚔ ★ ✓`), `text` (`[own] [mod] [mem] [ver]`), `off`. |
| `highlight_emoji` | `YC_HIGHLIGHT_EMOJI` | `true` | Draw emoji and channel shortcodes on a tinted chip. |
| `full_username` | `YC_FULL_USERNAME` | `false` | Append the handle when it differs from the display name. |
| `sidebar_width` | `YC_SIDEBAR_WIDTH` | `0` | Chats sidebar width in cells. `0` sizes from the terminal. Clamped, then reduced further if the terminal cannot spare it. |
| `activity_width` | `YC_ACTIVITY_WIDTH` | `0` | Activity column width in cells. Same clamping. |
| `scrollback_limit` | `YC_SCROLLBACK_LIMIT` | `2000` | Messages retained per chat. `0` or negative keeps everything, at the cost of a repaint that slows as the buffer grows. |
| `stream_status_mode` | `YC_STREAM_STATUS_MODE` | `auto` | `auto` enables the LIVE badge and viewer-count refresh; `off` disables them. |
| `emoji_autocomplete_mode` | `YC_EMOJI_AUTOCOMPLETE_MODE` | `auto` | **Inert.** Parsed and printed, but no code reads it: the emoji picker is a built-in Unicode set that is always available and needs no credentials. |

An unrecognized mode value is **normalized to a known one rather than rejected**,
so a typo degrades instead of failing startup — and `yc doctor`'s `display` check
names every value it had to correct:

```text
[ok] display: layout=inline badges=glyph avatars=initials animation=fast mouse=true
[warn] display: … ; corrected message_layout "inlien" -> inline
```

`--theme`, `--layout`, and `--animation` override for one run and are **not**
written back: a one-off `--theme nord` must not silently become your saved
preference. `yc profile set` is how you persist one.

## Quota

This block has no `twi` analogue. Read [quota.md](quota.md) for what the numbers
mean; this is the reference.

| Key | Env | Default | Purpose |
| --- | --- | --- | --- |
| `poll_interval_mode` | `YC_POLL_INTERVAL_MODE` | `auto` | `auto` obeys `pollingIntervalMillis` under the budget floor; `economy` raises the local floor to 5s; `off` sets floor and ceiling to 24h, i.e. manual refresh with `ctrl+r`. |
| `poll_interval_floor_ms` | `YC_POLL_INTERVAL_FLOOR_MS` | `1000` | Absolute local floor. `yc` never polls faster than this **and** never faster than `pollingIntervalMillis`. |
| `poll_interval_ceiling_ms` | `YC_POLL_INTERVAL_CEILING_MS` | `0` | Caps the stretched cadence so a nearly-empty budget does not stall chat. `0` means no ceiling. |
| `daily_quota_units` | `YC_DAILY_QUOTA_UNITS` | `10000` | Your project's daily allowance. Raise it if Google granted you more. |
| `search_quota_calls` | `YC_SEARCH_QUOTA_CALLS` | `100` | The separate daily `search.list` allowance. |
| `quota_reserve_percent` | `YC_QUOTA_RESERVE_PERCENT` | `10` | Stop polling at this remaining share so sends and [moderation](moderation.md) still work. `ctrl+r` overrides. |
| `session_hours` | `YC_SESSION_HOURS` | `0` | Budget to the next N hours instead of to the Pacific reset. `0` budgets to the reset. Also `--session-hours N`. |
| `follow_server_cadence` | `YC_FOLLOW_SERVER_CADENCE` | `false` | Remove the budget floor entirely, for a project with a raised quota. Also `--follow-server-cadence`. |
| `allow_search` | `YC_ALLOW_SEARCH` | `false` | **Inert.** Intended to opt in to `search.list` resolution. `youtube.SearchLiveVideo` exists; nothing calls it yet. |

### Cost overrides

Google publishes a per-method cost for `videos.list`, `channels.list`, and
`search.list`, and `yc` matches it. It publishes **no** cost for any live-chat
method — the cost table lists none of them, and their reference pages carry no
*Quota impact* line — so those five values are estimates derived from Google's
own documented rule of thumb (a list read usually 1 unit, a write usually 50).
The table is data precisely so a corrected figure is a config line rather than a
release.

| Key | Env | Default | Source |
| --- | --- | --- | --- |
| `quota_cost_list` | `YC_QUOTA_COST_LIST` | `5` | *estimate* — the one figure the whole budget rests on |
| `quota_cost_insert` | `YC_QUOTA_COST_INSERT` | `50` | *estimate* (write rule) |
| `quota_cost_delete` | `YC_QUOTA_COST_DELETE` | `50` | *estimate* (write rule) |
| `quota_cost_bans_insert` | `YC_QUOTA_COST_BANS_INSERT` | `50` | *estimate* (write rule) |
| `quota_cost_bans_delete` | `YC_QUOTA_COST_BANS_DELETE` | `50` | *estimate* (write rule) |
| `quota_cost_videos_list` | `YC_QUOTA_COST_VIDEOS_LIST` | `1` | published |
| `quota_cost_channels_list` | `YC_QUOTA_COST_CHANNELS_LIST` | `1` | published |
| `quota_cost_search_list` | `YC_QUOTA_COST_SEARCH_LIST` | `1` | published — and charged to its **own** 100-call bucket |

An endpoint with no entry costs 1 unit, so an unmetered call still moves the
ledger. `yc doctor` prints the table actually in force and how many entries your
config overrode.

## Chat Logging

Opt-in archive of the chat itself, separate from debug logging: the debug log
records what `yc` did, the chat log records what the chat said. Each session
appends normalized events - one JSON object per line (JSON Lines) - so the
files work with `jq`, spreadsheet imports, and `yc export superchats` without a
custom parser. Raw YouTube API JSON never reaches disk, every free-text field
passes through the credential redactor, and files are written `0600` in a
`0700` directory. A write failure never interrupts chat: logging turns itself
off for the session and says so once in the status line.

| Key | Env | Default | Purpose |
| --- | --- | --- | --- |
| `chat_logging` | `YC_CHAT_LOG` | `false` | Enable the chat log. Off by default: writing chat to disk is a privacy decision, never a default. |
| `chat_log_dir` | `YC_CHAT_LOG_DIR` | empty | Where log files go. Empty means `chatlog` under the cache directory. |
| `chat_log_max_bytes` | `YC_CHAT_LOG_MAX_BYTES` | `10485760` | Rotate the current file once it exceeds this size (10 MB). |
| `chat_log_max_files` | `YC_CHAT_LOG_MAX_FILES` | `5` | How many log files survive rotation, newest first, current file included. |

## Debug Logging

| Key | Env | Default | Purpose |
| --- | --- | --- | --- |
| `debug_logging` | `YC_DEBUG_LOG` | `false` | Enable redacted JSON-line diagnostics. |
| `debug_log_path` | `YC_DEBUG_LOG_PATH` | empty | Where to write. Empty means `debug.log` under the cache directory. |

Flags on `chat`, `login`, and `doctor`:

```sh
yc chat --video dQw4w9WgXcQ --debug-log
yc chat --mock --debug-log --debug-log-path /tmp/yc-debug.log
yc login --debug-log
yc doctor --debug-log
```

`--debug-log=false` explicitly disables logging for that command even when
`YC_DEBUG_LOG=true` or `debug_logging = true` is configured — the flag tracks
"absent" separately from "false".

Logs are JSON lines in a file created with mode `0600`; parent directories are
created `0700`. Existing log files that are directories, symlinks, or
group/other-accessible are rejected. Unix builds open the final path with
`O_NOFOLLOW` and validate the opened descriptor; `yc doctor` reports which
guarantee this build makes:

```text
[ok] debug log hardening: Unix debug log files are opened with O_NOFOLLOW on the
     final path and validated through the opened file descriptor.
```

Records use curated fields and redact access tokens, refresh tokens, client
secrets, API keys, bearer headers, authorization codes and state, and
credential-shaped URL query values. Review a log before sharing it anyway — it
can still contain video IDs, chat IDs, channel names, hostnames, counts, and
timing.

## Example Config

The complete set of keys `yc config show` prints, with defaults:

```toml
google_client_id = ""
google_client_secret = ""
google_access_token = ""
google_refresh_token = ""
google_redirect_url = ""
youtube_api_key = ""
youtube_channel_id = ""
default_chats = ""
enable_mouse = true
avatar_mode = "initials"
animation_mode = "fast"
theme_name = "claude"
theme_background = ""
theme_foreground = ""
theme_accent = ""
theme_muted = ""
theme_border = ""
theme_surface = ""
theme_warning = ""
theme_error = ""
theme_success = ""
message_layout = "inline"
badge_mode = "glyph"
highlight_emoji = true
full_username = false
sidebar_width = 0
activity_width = 0
scrollback_limit = 2000
stream_status_mode = "auto"
emoji_autocomplete_mode = "auto"
poll_interval_mode = "auto"
poll_interval_floor_ms = 1000
poll_interval_ceiling_ms = 0
daily_quota_units = 10000
search_quota_calls = 100
quota_reserve_percent = 10
session_hours = 0
follow_server_cadence = false
allow_search = false
quota_cost_list = 5
quota_cost_insert = 50
quota_cost_delete = 50
quota_cost_bans_insert = 50
quota_cost_bans_delete = 50
quota_cost_videos_list = 1
quota_cost_channels_list = 1
quota_cost_search_list = 1
debug_logging = false
debug_log_path = ""
```

Leave secret values empty in anything you share. Prefer `yc login` for saved
tokens. If you keep real credentials in this file, keep it private to your user
account (`chmod 600`) — flat config values still take precedence over saved
credentials.

## CLI Commands And Flags

```sh
yc chat [--chat ID] [--chats a,b] [--video ID] [--channel @handle]
        [--live-chat-id ID] [--mock] [--theme NAME] [--layout MODE]
        [--animation MODE] [--no-mouse] [--session-hours N]
        [--follow-server-cadence] [--debug-log] [--debug-log-path PATH]
        [--config PATH]
yc config show [--config PATH]
yc config path
yc doctor [--config PATH] [--debug-log]
yc login [--redirect-uri URL] [--timeout D] [--dry-run] [--read-only]
         [--write-default-config] [--debug-log] [--debug-log-path PATH] [--config PATH]
yc logout [--config PATH] [--keep-remote]
yc profile list | show | set <name> [--background '#rrggbb' … --success '#rrggbb'] [--config PATH]
yc quota [--config PATH]
yc setup [--non-interactive] [--client-id ID] [--api-key-only] [--channel-id ID]
         [--chat ID] [--chats a,b] [--enable-mouse BOOL] [--avatar-mode MODE]
         [--animation-mode MODE] [--theme NAME] [--layout MODE]
         [--login | --login-dry-run] [--config PATH]
yc --version
yc --help
```

Exit codes are part of the contract:

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Runtime failure. |
| `2` | Usage or validation failure, including "no credentials configured". |

A chat named positionally is a usage error: `yc chat dQw4w9WgXcQ` is rejected
with a message telling you to use `--chat`, because a positional target would be
ambiguous with a subcommand.

## The Setup Command

`yc setup` is the guided path for non-secret configuration. It writes only:

`google_client_id`, `youtube_channel_id`, `default_chats`, `enable_mouse`,
`avatar_mode`, `animation_mode`, `theme_name`, `message_layout`.

It never asks for or writes a client secret, access token, refresh token,
authorization code, OAuth state, authorization URL, or API key. Existing secret
lines already in `config.toml` are preserved untouched.

```sh
yc setup                                     # interactive
yc setup --non-interactive --client-id "…" --chat dQw4w9WgXcQ --theme nord
yc setup --api-key-only                      # explains the key path instead of login
yc setup --login                             # write config, then run yc login
yc setup --login-dry-run                     # write config, then the bounded smoke path
```

## What `yc doctor` Checks

Fifteen checks on a default run. All but the last two are offline.

1. **credential file** — loaded, missing, failed, shadowed, or unsupported, and where.
2. **credentials** — the effective mode and what it can do, with granted scopes when known.
3. **quota budget** — the arithmetic behind the poll cadence, in one line.
4. **debug log hardening** — the guarantee this build makes when opening the log.
5. **config** — the path it loaded from, or the load error plus "running on environment values and defaults".
6. **credential mode** — token/refresh/key presence, and the expiry cliff when a token has no refresh token.
7. **theme** — whether the configured palette resolved.
8. **display** — the effective layout, badges, avatars, animation, and mouse, naming any value it corrected.
9. **cache** — writability of the directory the quota ledger lives in.
10. **debug log** — enabled or disabled, and the destination.
11. **chats** — every configured target, how it will resolve, and what that costs.
12. **cost table** — the table actually in force, with the live-chat figures marked as estimates, and an override count.
13. **quota** — today's estimated spend, the reset, search calls, the effective interval, and the projected exhaustion. Warns below 25% remaining.
14. **reachability** and **identity** — the only two network checks.

Warnings never make the command fail; `yc doctor` exits `0` even when every
check warns. It exits non-zero only when it cannot construct a report at all —
an environment-only config load failure, or an unopenable debug log. Doctor is
what you run when something is already wrong, so a broken config file becomes a
check line rather than an abort.

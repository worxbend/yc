# Quickstart

This guide assumes nothing except a terminal and either Go or Docker. It keeps
to what `yc` can do today. For the full documentation map, read
[index.md](index.md); for failure diagnosis, read
[troubleshooting.md](troubleshooting.md); for the daily unit allowance that
governs every live run, read [quota.md](quota.md).

## 1. Pick Your Path

Use Go when you are developing the project:

```sh
go run ./cmd/yc --help
```

Use Docker when you want a clean packaged run:

```sh
docker build -t yc:local .
docker run --rm yc:local --help
```

## 2. Run Mock Chat First

Mock mode is ready today and is the friendly sandbox. No Google account, no API
key, no OAuth, no network access, and — importantly — **no quota**.

```sh
go run ./cmd/yc chat --mock
```

Docker:

```sh
docker run --rm -it yc:local chat --mock
```

Compose:

```sh
docker compose run --rm mock
```

The mock plays a scripted tour: a splash, ordinary chat, a Super Chat, a
membership, a gifting burst, a moderation action, a poll, the chat ending, and a
reconnect. It exercises identity, the LIVE badge, a viewer count, subscriptions,
and categories with obviously-fake data.

Piped into a file or another process, the same command detects the missing TTY,
renders one ANSI-stripped frame, and exits 0. That is the CI smoke.

## 3. Learn The Keys

The essentials are below. [keybindings.md](keybindings.md) is the complete
reference — every binding, every context, and the input dispatch order — and it
is generated from the same table the in-app help renders from.

| Key | Action |
| --- | --- |
| `ctrl+p` | Open or close the command palette. |
| `i` / `o` / `a` | Focus the composer and start typing. |
| `esc` | Leave the composer for the chat view (draft kept). |
| `tab` | Move focus between chat, composer, and the chats sidebar. |
| `j` / `k` | Select the next/previous message, or move the sidebar highlight. |
| `pgup` / `pgdn` | Scroll chat history. |
| `r` | Reply to the selected message. |
| `K` | Open or close the selected-message inspect panel. |
| `alt+1` / `alt+2` / `alt+3` | Chat, Stream Info, and the quota ledger. |
| `space` `c` | Open the chat picker (also `/chats` in the composer). |
| `space` `x` | Close the active chat. |
| `space` `e` | Show or hide the chats sidebar. |
| `space` `a` | Show or hide the activity column. |
| `[` / `]` | Switch the active chat. |
| `<` / `>` / `=` | Resize the focused pane / reset both side panes. |
| `1`–`4` / `0` | Toggle / reset local view filters. |
| `?` | Expand or collapse help. |
| `ctrl+t` | Theme page with live preview. |
| `ctrl+e` | Emoji picker. |
| `ctrl+g` / `ctrl+b` / `ctrl+y` / `ctrl+n` | Layout / badges / emoji chip / full names. **This run only** — see below. |
| `@` + `tab` | Complete a chatter's name from the live roster. |
| `d` / `t` / `b` | Delete the selected message / time out / ban its author. Each asks first. |
| `ctrl+l` | Clear the active chat's local history. Press twice — the first press asks. |
| `ctrl+r` | Reconnect; also how you override a quota-paused or ended session. |
| `enter` | Send from the composer, or run the highlighted picker entry. |
| `q` / `ctrl+c` | Quit. |
| any key during the splash | Skip the startup animation. |

The display toggles — and the `ctrl+t` theme picker — change the **running
session** and do not write `config.toml`. Persist a choice with `yc profile set`
or by editing the file; see [config.md](config.md) and [themes.md](themes.md).

`d`, `t`, and `b` need an OAuth credential with `youtube.force-ssl` **and** the
signed-in channel owning or moderating that chat. They stay bound either way and
say exactly what is missing rather than doing nothing:
[moderation.md](moderation.md).

Mouse support is enabled by default but currently covers **wheel scrolling
only**. Disable it with `enable_mouse = false`, `YC_ENABLE_MOUSE=false`, or
`--no-mouse`.

## 4. Configure A Live Chat

> `yc` uses **your own** Google Cloud project, not a bundled one. Register it
> first — see [register-google-app.md](register-google-app.md). The API-key path
> takes about two minutes and needs no OAuth consent screen.

Pick the smallest credential that covers what you want:

| Goal | Needs | Command |
| --- | --- | --- |
| Try the UI | nothing | `yc chat --mock` |
| Read a public live chat | an API key | `YC_YOUTUBE_API_KEY=… yc chat --video <id>` |
| Read and send | a Desktop OAuth client + `yc login` | `yc chat --channel @name` |

Read-only mode:

```sh
export YC_YOUTUBE_API_KEY="<api key from Google Cloud>"
go run ./cmd/yc chat --video dQw4w9WgXcQ
```

Full mode:

```sh
export YC_GOOGLE_CLIENT_ID="<desktop-app client id>"
go run ./cmd/yc login
go run ./cmd/yc chat --channel @somechannel
```

`yc chat` refuses before opening a socket when nothing is configured, exits `2`,
and names all three ways forward.

> **Status: credentialed.** Every live path here is implemented and unit-tested
> against fakes, and has never been run against Google. See
> [manual-validation.md](manual-validation.md).

### Naming a chat

```sh
yc chat --video dQw4w9WgXcQ                              # video ID
yc chat --chat 'https://www.youtube.com/watch?v=dQw4w9WgXcQ'
yc chat --chat https://youtu.be/dQw4w9WgXcQ
yc chat --chat 'https://www.youtube.com/live/dQw4w9WgXcQ'
yc chat --channel @somechannel                           # handle
yc chat --channel UCxxxxxxxxxxxxxxxxxxxxxx               # channel ID
yc chat --live-chat-id Cg0KC2RRdzR3OVdnWGNR              # costs zero quota
yc chat --chat one --chat two                            # repeats accumulate
yc chat --chats one,two                                  # comma-separated
yc chat                                                  # start empty, then space c
```

`--video` and `--channel` are spellings of the same accumulating list as
`--chat`; mixing them is fine. An explicit `--live-chat-id` is the only way to
open a chat without spending a resolution unit.

### Guided setup

```sh
go run ./cmd/yc setup
go run ./cmd/yc setup --non-interactive --client-id "<id>" --chat dQw4w9WgXcQ --theme nord
```

Setup writes only non-secret keys. It never asks for or writes a client secret,
access token, refresh token, authorization code, OAuth state, or API key. Add
`--login` to hand off to login afterwards, or `--login-dry-run` for the bounded
smoke path.

### Check login wiring without a browser

```sh
go run ./cmd/yc login --dry-run
```

`--dry-run` explains the scopes, the redirect, the timeout, and which client
credentials are present. It contacts nothing, binds nothing, and opens nothing.

## 5. Use A Config File Instead

Ask `yc` where it expects config:

```sh
go run ./cmd/yc config path
```

Create that file with flat `key = value` lines:

```toml
google_client_id = "<desktop-app client id>"
youtube_api_key = ""
default_chats = "dQw4w9WgXcQ"
theme_name = "claude"
animation_mode = "fast"
poll_interval_mode = "auto"
quota_reserve_percent = 10
```

The parser is a flat line scanner. **Do not use nested TOML tables.** Prefer
`yc setup` for non-secret config and `yc login` for saved tokens. Leave secret
values empty in anything you share. If you do keep real credentials in the flat
config, keep the file private (`chmod 600`) — flat config values take precedence
over saved credentials.

## 6. Watch The Quota

This is the step with no Twitch equivalent, and skipping it is how a session
dies mid-stream.

```sh
go run ./cmd/yc quota
```

```text
used = 0/10000 units est.
remaining = 10000 units est. (100%)
search = 0/100 calls
mode = idle (no chat has polled yet today)
resets = Sun, 09 Aug 2026 00:00:00 PDT (America/Los_Angeles)
effective_interval = -
server_floor = -
budget_floor = -
projected_exhaustion = unknown; no cadence has been established yet
```

The status bar carries the same meter live, and `alt+3` opens a whole tab for
it. Every figure is an estimate — Google publishes no quota cost for any
live-chat method. Read [quota.md](quota.md) before a long stream.

## 7. Diagnose Before Blaming The Terminal

```sh
go run ./cmd/yc doctor
docker run --rm yc:local doctor
```

`doctor` reports the credential store, the effective capability, the quota
budget arithmetic, debug-log hardening, config load, credential mode, theme,
display modes, cache writability, debug-log destination, configured chats, the
cost table in force, today's ledger, API reachability, and your resolved
identity. It prints no token, no key, and no secret, and only two of its checks
touch the network.

## 8. Use The Dotfile Shape

For Docker Compose, copy the tracked template:

```sh
cp .env.example .env
$EDITOR .env
docker compose run --rm live
```

`.env` is ignored by git. Keep the real file local.

## 9. Build A Local Binary

```sh
go build -o bin/yc ./cmd/yc
./bin/yc chat --mock
```

## Common Fixes

**`no YouTube credentials configured`** — run `yc login`, set
`YC_YOUTUBE_API_KEY`, or run `yc chat --mock`.

**"no active broadcast for this channel"** — `channels.list` answered but the
channel is not currently live, or its broadcast is not reported there. Pass the
video ID or the watch URL directly. The `search.list` fallback is not wired up.

**Chat stops and the status bar says `PAUSED`** — the quota reserve tripped.
That is deliberate: reads stop so sends still work. `ctrl+r` overrides. See
[quota.md](quota.md).

**Avatars, badges, and emoji look like text** — that is the only rendering mode.
The live chat API supplies no badge imagery and no per-message emote metadata,
so `yc` has no image path at all.

# RECON — shared reconnaissance for the swarm improvement pass

Date: 2026-08-15. Baseline commit: `5211b69`.

All track agents read this before touching code. The headline: **this codebase is already in
very good shape** — clean build/vet/race, zero staticcheck findings, zero known vulnerabilities,
75–92% coverage per package, extensive docs and ADRs. Tracks must target the *real* gaps listed
at the bottom, not invent work. "Audited, already satisfactory" is a valid and expected outcome
for many checklist items.

## Module map

Entry point: `cmd/yc/main.go` (9 lines) → `internal/cli.Run`. Everything lives in `internal/`.

| Package | Responsibility | ~LOC |
|---|---|---|
| `internal/cli` | Subcommand dispatch, flags, config loading, wiring (`login`/`logout`/`setup`/`doctor`/`quota`/`profile`/`config`/`chat`) | 3,300 |
| `internal/app` | Bubble Tea model (`shellModel`), Update/View, chat state, panes, filters, moderation UI, notifications, `LiveChatClient` multiplexer | 13,700 |
| `internal/youtube` | Hand-rolled REST client (ADR 0002), `Poller` state machine, event normalization, quota ledger + persistence, send/moderation, deterministic fake | 6,000 |
| `internal/render` | Chat rows as width-bounded semantic fragments; badges, superchats, events, text sanitization/width | 1,900 |
| `internal/auth` | OAuth loopback login, `Secret` type, scopes, redactors | 1,000 |
| `internal/storage` | Credential file store (Unix-only), cache dir, writability probes | 1,200 |
| `internal/config` | `Config` struct + TOML/env reflection bindings, defaults, redaction | 950 |
| `internal/theme` | 58 preset palettes + custom palette resolution | small |
| `internal/animation` | Frame clock, text effects — pure functions | small |
| `internal/emoji`, `internal/debuglog` | Emoji catalog / autocomplete; JSON-lines debug logger | small |

Dependency direction: `cli → {config, auth, storage, youtube, app, debuglog}`;
`app → {youtube, render, theme, animation, emoji, config, debuglog}`;
`youtube → {auth, storage (iface), debuglog}`; `render → {theme, emoji}`.
`app` consumes normalized models via interfaces in `internal/app/contract.go` — it never sees
Google JSON shapes.

## TUI stack

Bubble Tea v1.3.10 + lipgloss v1.1.0 + charmbracelet/x/ansi + uniseg.

- Program: `internal/app/run.go:118-133` — alt screen, focus reporting, optional mouse.
  Non-TTY path prints one `ansi.Strip(View())` frame (CI smoke).
- `Init` (`model.go:313`) batches five stream-receive commands, frame tick, identity/target
  resolution, chat metrics, quota tick. `Update` is a 1,703-line switch (`update.go:37`);
  `View` (`view.go`, 1,633 lines) does no I/O. OSC (terminal background) travels inside
  `View`'s return string after startup — never written directly.
- Ingestion chain: `youtube.Poller.run` → five buffered channels →
  `app.liveChatSession.forward` (`live_chat.go:898`, one goroutine per stream, stamps routing
  key) → `LiveChatClient.emit*` merged channels → `tea.Cmd` one-blocking-receive-per-delivery
  (`chat.go:99-172`), re-armed by `Update` on each message.

## YouTube chat consumption

Official Live Streaming API `liveChatMessages.list` polling (ADR 0001). No scraping/InnerTube.
Hand-rolled REST client (ADR 0002).

- Poller: `internal/youtube/poll.go` (1,200 lines). States: idle, resolving, priming,
  streaming, backoff, stretched, ended, offline, quota_paused, closed. `maxResults=2000`;
  8000-entry dedupe ring; priming pass marks rows `Historical`.
- Auth: exactly one credential per request (`transport.go:511-521`) — OAuth Bearer if present,
  else API key query param. Loopback OAuth flow (`internal/auth/loopback.go`); scopes
  `youtube.readonly` / `youtube.force-ssl`. One shared refresh-and-retry on 401 via an epoch
  (`transport.go:271-340`).
- Credentials: `internal/storage/credentials.go` — 0600 file / 0700 dir enforced, symlinks
  rejected (Lstat + `O_NOFOLLOW`), atomic replace. Same modes for cache, quota ledger, debug log.
- Quota pacing (ADR 0004): every call charged (errors included); budget floor from remaining
  units over horizon; reserve pause at `ReservePercent`; `NextInterval` =
  clamp(max(serverFloor, budgetFloor, configMin), configMin, configMax) ± jitter; backoff caps
  120s (rate limit) / 60s (transient). Ledger persisted per credential fingerprint per Pacific
  day; pruned by a background goroutine (`cli/live.go:133-155`).

## Concurrency model

- Poller: one session goroutine per launch, ctx-derived cancel, `done` channel; `restartMu`
  serializes `Reconnect`; emitters **drop rather than block** and count drops (surfaced as
  `dropped=N` in the status bar).
- `LiveChatClient` (`app/live_chat.go`): N per-chat sessions multiplexed onto five merged
  channels; `wg` + `closeOnce`; `startSession` does closed-check/insert/`wg.Add` under one
  lock hold. `Close`: mark closed → snapshot sessions → cancel → stop each → `wg.Wait()` →
  close channels. App side spawns no goroutines of its own — everything is a `tea.Cmd`.

## Config / flags / keys / persistence

- `internal/config/config.go`: single `Config` struct; TOML keys and env names in struct tags,
  `secret:"true"` on the four credential fields. Sections: Google, YouTube, DefaultChats,
  Features, Quota (incl. 8 per-endpoint cost overrides), Debug.
- Precedence: flags > env > file > defaults, via reflection binding table
  (`config/bindings.go` — these are *config* bindings, not user keybindings).
- Flags registered in `cli/cli.go:163-177`; typed helpers in `cli/flags.go`.
- Keybindings: single source-of-truth table `keyBindings` in `app/keymap.go:57-101`; help
  footer, expanded help, and command palette generate from it; `keymap_coverage_test.go`
  fails on undocumented ctrl keys. **No user-rebindable keys today.**

## Sanitization (Track A: read this carefully)

Untrusted text is sanitized at the display boundary, not in `normalize.go` (deliberate —
stripping before measurement keeps width math honest):

- `render/text.go:105` `sanitizeUserText`: `ansi.Strip` + control-char mapping + bidi-control
  drop. Applied throughout `message.go`, `events.go`, `superchat.go`.
- `app/panes.go:238` `flattenControlRunes` for app-drawn chrome.
- `app/view.go:469` `sanitizeContextValue` (tab bar) — **weaker**: maps C0/DEL to `�` but does
  not drop bidi overrides and doesn't run `ansi.Strip` (safe today only because ESC < 0x20).
- `app/notify.go:194` `sanitizeNotificationText` + length caps before shelling to the desktop
  notifier.

**Paths to probe, not assume**: `activity.go:235` (`TargetDisplayName` into "X was banned"),
`activity.go:404` `displayNameOr`, `notify.go:250,283` author names reaching the in-TUI toast —
all `strings.TrimSpace` only, relying on downstream pane writers.

## Test/lint/vuln baseline (2026-08-15)

- `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./...`: **all pass**.
- `staticcheck ./...` (via `go tool`): **zero findings**. `govulncheck ./...`: **no vulns**.
- No `.golangci.yml`, golangci-lint not installed.
- Coverage: animation 89.3, app 80.2, auth 89.2, cli 75.1, config 86.8, debuglog 82.8,
  emoji 88.3, render 90.5, storage 77.0, theme 92.3, youtube 89.6 (%).
- ~912 test functions in 111 files (~47% of the codebase). Golden files
  (`render/testdata/event_rows.golden` with `-update`, whole-frame layout golden), rich fakes
  (deterministic transport, scripted mock source, memory stores, injectable clock), invariant
  tests (keymap coverage, credential-leak, redaction, mode matrix, stress, reconnect lifecycle).
- **No fuzz tests anywhere** (`func Fuzz` absent).

## Features already implemented — do NOT re-plan

Multi-chat tabs + panes + sidebar + activity column; filters (4 shortcut filters + mentions);
full moderation (delete/timeout/ban, confirm-first, capability-gated); 58 themes + custom +
picker + OSC 11 background; desktop notifications with bell fallback; shared 10fps animation
clock with off/reduced modes; emoji picker + autocomplete; command palette; message inspect;
reply; rate-limited send queue; quota status bar + `yc quota`; `yc doctor`; `yc setup` wizard;
mock/demo mode; layout modes; superchat/sticker/gift/membership/poll rendering; viewer count.

## Real gaps (target these)

1. No fuzz tests on sanitizers/width math (Track A).
2. Sanitization soft spots listed above (Track A — verify with targeted tests first).
3. No `.golangci.yml` / golangci-lint in CI (Track E).
4. No perf benchmark/replay harness or profiles committed (Track C).
5. Feature absences: chat logging to disk (JSONL) + superchat CSV ledger, in-buffer search,
   user-configurable keybindings, auto-follow when a channel restarts a stream (Track D —
   coordinate config keys/keybindings with Track B).
6. `sanitizeContextValue` weaker than `sanitizeUserText` (Track A).

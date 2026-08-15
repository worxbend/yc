# Track D — Features

Delivered on branch `swarm/features`. Everything here is opt-in via config;
no new keybindings were added (Track B holds `/`, `n/N`, `y`, and the vim
scroll keys; nothing in this track needed a key at all).

Note: `docs/swarm/RECON.md` and `FINDINGS.md` did not exist in this worktree
at branch time; `FINDINGS.md` is created here with this track's out-of-scope
notes.

## 1. Chat logging to disk

Opt-in JSONL append log of normalized chat events, one file per chat session,
size-rotated with a bounded retained-file count. Files are `0600` in a `0700`
directory (the internal/storage convention); every free-text field passes
through the run's credential redactor before it is written; a write failure
latches logging off for the session and surfaces once in the status line —
it can never end the chat session.

- Package: `internal/chatlog` (writer.go, event.go) — consumed by the app
  through the app-side `ChatLogger` interface in `internal/app/contract.go`
  (the `contract.go` pattern), wired at `internal/app/update.go`
  (`handleClientMessage` → `logChatMessage` in `internal/app/chatlogging.go`)
  and constructed in `internal/cli/chatlog.go` / `internal/cli/cli.go`.
- Config keys: `chat_logging` (`YC_CHAT_LOG`, default `false`),
  `chat_log_dir` (`YC_CHAT_LOG_DIR`, default `<cache>/yc/chatlog`),
  `chat_log_max_bytes` (`YC_CHAT_LOG_MAX_BYTES`, default 10485760),
  `chat_log_max_files` (`YC_CHAT_LOG_MAX_FILES`, default 5).
- Keybindings: none (config-only surface by design).
- Tests: `internal/chatlog/writer_test.go` (append format, lazy file
  creation, permissions, rotation + pruning, redaction, closed-writer,
  corrupt-tail reads), `internal/app/chatlogging_test.go` (wiring,
  one-notice failure degradation), `internal/config/config_test.go`
  (key binding + defaults-off).

## 2. Superchat ledger export

`yc export superchats` reads the JSONL chat logs and emits CSV
(`timestamp, chat_id, author, amount_value, currency, tier, message`).
No network, no quota; amounts are integer-micros arithmetic, never floats.
An empty log directory yields a header-only CSV, not an error.

- Files: `internal/cli/export.go`, dispatch in `internal/cli/cli.go`,
  shared record reader `chatlog.Records` in `internal/chatlog/event.go`.
- Config keys: reuses `chat_log_dir`; `--dir`, `--out`, `--config` flags.
- Keybindings: none (CLI subcommand).
- Tests: `internal/cli/export_test.go` (paid-event selection across files,
  foreign-file exclusion, header-only empty case, `--out`, usage errors,
  table-driven `formatMicros`).

## 3. Auto-follow (opt-in)

When a watched stream's live chat closes and the same channel goes live
again, the chat re-resolves the channel and reconnects to the new broadcast,
keeping the same UI chat (history, draft, scroll position). Quota-conscious
per docs/adr/0004: each check is one estimated `channels.list` unit, the
cadence is floored at 30s, and the check count per ended stream is capped
(default 30 ≈ 30 units worst case). Resolution runs through the shared
`BroadcastResolver`/client, so every check lands on the one quota ledger the
status bar reports. State is surfaced through the existing status-detail
vocabulary ("ended; auto-follow is watching for the next stream (check
N/M)", "auto-follow found a new stream; connecting").

- Files: `internal/app/autofollow.go`, hooks in `internal/app/update.go`
  (`handleConnectionState` ConnectionClosed, `handleClientMessage`
  chat-ended), per-chat progress on `chatState` (`internal/app/state.go`),
  clamps wired in `internal/app/model.go`.
- Config keys: `auto_follow` (`YC_AUTO_FOLLOW`, default `false`),
  `auto_follow_poll_seconds` (`YC_AUTO_FOLLOW_POLL_SECONDS`, default 60,
  floor 30), `auto_follow_max_checks` (`YC_AUTO_FOLLOW_MAX_CHECKS`,
  default 30; 0 means default, never unbounded).
- Keybindings: none.
- Tests: `internal/app/autofollow_test.go` (off by default, needs a channel,
  arms on close, bounded checks, same-chat "still offline" handling,
  new-stream adoption + routing, clamp tables).

## 4. Regex mute list

Not implemented — deliberately left out after features 1–3; see
`docs/swarm/FINDINGS.md` for the handoff sketch.

## Docs

- `docs/config.md`: new "Chat Logging" section (keys + export usage), three
  auto-follow rows in the Display table, `yc export` in the CLI command list.
- `yc --help` usage text updated with the new command and env vars.

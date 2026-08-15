# Changelog

All notable changes to `yc` are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

`yc --version` reports `dev` unless a build stamps a version through `-ldflags`.
Released binaries are stamped with their tag.

## [Unreleased]

Nothing yet.

## [0.2.0] - 2026-08-15

A five-track improvement pass: security hardening, measured performance work,
chat navigation, on-disk logging, and a lint gate. Full details live in
`docs/swarm/FINAL-REPORT.md`.

### Added

- In-buffer search: `/` opens an incremental search over the scrollback,
  `n`/`N` walk matches, `esc` unwinds. Matches are highlighted in place.
- Vim-style scrollback navigation: `g`/`G` jump to the oldest/newest message,
  `ctrl+d`/`ctrl+u` move half a page.
- A sticky `↓ N new` indicator while scrolled up, cleared on jumping to the
  bottom. Historical backlog does not count toward it.
- `y` copies the focused message to the clipboard as sanitized plain text over
  OSC 52 — no shell-out, and only in interactive sessions.
- Opt-in chat logging (`chat_logging`): normalized events appended as JSON
  Lines, one file per session, size-rotated (`chat_log_max_bytes`,
  `chat_log_max_files`), written `0600` in a `0700` directory with free-text
  fields passed through the redactor. A write failure disables logging for the
  session and says so once; it never kills the chat.
- `yc export superchats`: reads those logs and emits a CSV ledger
  (timestamp, chat, author, amount, currency, tier, message) using integer
  micros — no floating-point money.
- Opt-in auto-follow (`auto_follow`): when a watched stream ends, yc
  re-resolves the channel on a bounded, quota-charged cadence
  (`auto_follow_poll_seconds`, `auto_follow_max_checks`) and reopens the next
  broadcast into the same chat, keeping history and draft.
- The first fuzz tests in the repository, covering the three text sanitizers
  with terminal-safety invariants, plus end-to-end escape-injection probes
  through the rendered frame.
- Replay benchmarks for the chat ingestion and render paths, so performance
  claims are measured rather than asserted.
- A `.golangci.yml` lint gate (errcheck, govet, staticcheck, revive, gocritic,
  gosec, misspell, unparam) wired into CI. The initial run's 440 findings are
  at zero.

### Fixed

- The tab-bar sanitizer now neutralizes raw C1 control bytes and bidirectional
  override characters, and trims after mapping so a trailing control cannot
  shield whitespace.
- Desktop notifications on Linux pass `--` to `notify-send`, so a chatter whose
  display name looks like an option (for example `--icon=...`) is treated as
  text, not parsed.
- API response bodies are bounded on the success path, closing the client's one
  unbounded read.
- OAuth state generation could loop forever for alphabet sizes that divide 256;
  the rejection threshold no longer wraps to zero.
- `yc export superchats` reports a failed output-file close instead of
  silently dropping data on a full disk.

### Changed

- Ingesting a message at the scrollback cap is roughly ten times faster and
  allocates ~99% less (26,097 → 2,419 ns/op; 190,647 → 2,099 B/op on the flood
  benchmark): the buffer now trims in place instead of reallocating.
- Splitting pure-ASCII message bodies into fragments skips the grapheme
  segmenter (10,005 → 1,338 ns/op).
- One combined Update+View pipeline pass allocates 41% less.

## [0.1.0] - 2026-08-08

The first release. Everything below is the initial implementation.

> **No credentialed path in this release has ever been run against Google.**
> Mock chat, diagnostics, config, theming, rendering, and the quota arithmetic
> are exercised by tests and credential-free smokes. Login, live polling,
> sending, moderation, identity, and stream info are unit-tested against fakes
> and `httptest` only. See [docs/manual-validation.md](docs/manual-validation.md).

### Added

#### Chat transport

- Live chat over `liveChatMessages.list`, owned by `youtube.Poller`: priming with
  a token-less first request, retained page tokens, an 8000-entry dedupe ring,
  a 2-minute offline grace window after `offlineAt`, and five separate outbound
  streams for messages, connection state, moderation, room events, and polls.
- `maxResults=2000` on every poll. Quota is charged per call rather than per
  item, so the largest documented page costs what the smallest does.
- Chat target parsing for a video ID, a `watch`/`live`/`shorts` URL, a `youtu.be`
  link, an `@handle`, a channel ID, and an explicit `--live-chat-id` that skips
  resolution and spends nothing.
- A cheapest-first resolution ladder: explicit ID (free) → `videos.list` →
  `channels.list`. `search.list` is implemented but deliberately not wired to any
  caller.
- Sending through `liveChatMessages.insert`, behind a local 3-burst / 2-second
  token bucket that declines before dispatch, with the reply convention
  (`@DisplayName ` prefix — the API has no parent-message field) and a
  200-grapheme cap applied before the call.
- Moderation transport: `liveChatMessages.delete` and `liveChatBans.insert`/
  `.delete`, reached by the `d`/`t`/`b` keys described under *Shell*.
- Broadcast metadata, viewer counts, subscriptions, and video categories.
- Error classification across all three of Google's disagreeing channels — the
  legacy `error.errors[].reason`, the canonical `error.status`, and the
  `google.rpc.ErrorInfo` reason — onto one sentinel set, degrading an
  unrecognized combination into a sane retry policy.

#### Quota

- `QuotaLedger`: mutex-guarded, charges every dispatched call including failures,
  keyed by credential fingerprint and Pacific day, with a separate bucket for
  `search.list`.
- `FileLedgerStore`: versioned JSON under the cache directory, `0600` in `0700`,
  atomic temp-and-rename, filename components validated rather than sanitized.
- DST-correct reset: `ResetAt` constructs the next calendar midnight in
  `America/Los_Angeles` rather than adding 24 hours, with the tz database
  embedded.
- Budget-aware pacing: `NextInterval` clamps
  `max(serverFloor, budgetFloor, configMin)` into `[configMin, configMax]`,
  applies ±10% jitter (full jitter while backing off), and never returns a value
  below the server floor.
- A reserve threshold that pauses reads so sends and moderation survive, a
  backoff ladder capped at 120s for rate limiting and 60s for transient failures
  with one-step decay on success, and a hard refusal to retry an exhausted quota.
- `yc quota`, a status-bar meter with an effective-interval readout, and a whole
  tab (`alt+3`) for the ledger. Every figure carries an `est.` marker.
- A config-overridable cost table, so a corrected estimate is one line rather
  than a release.

#### Authentication

- `yc login`: Google installed-app OAuth with a loopback listener on an ephemeral
  `127.0.0.1` port, PKCE S256, and `access_type=offline`. `--dry-run`,
  `--read-only`, `--timeout`, `--redirect-uri`, and `--write-default-config`.
- `yc logout`, with remote revocation unless `--keep-remote`.
- API-key mode as a first-class read-only path with no OAuth at all.
- Capability derivation without any network access, so `yc chat` refuses early
  and `yc doctor` reports offline. Controls are disabled with a reason rather
  than hidden.
- A single credential holder shared by every client, with single-flight refresh
  fired 5 minutes before expiry and rotated tokens persisted before the new
  access token becomes visible.
- `auth.Secret` and `auth.Redactor`: credentials that render as placeholders
  under formatting and JSON encoding, revealed only at the request boundary and
  the storage marshal path.

#### Interface

- A Bubble Tea shell with three tabs, a chats sidebar, an activity column, a
  command palette, an inspect panel, local filters, reply context, a theme
  picker, an emoji picker, and `@mention` autocomplete completing from the live
  roster.
- 58 built-in themes — 53 dark including a `yc` preset and 24 vibrant near-black
  palettes, 5 light — plus a custom hex palette, contrast correction, and OSC
  11/111 terminal background override and restore.
- Three message layouts, three badge modes, an emoji highlight chip, and full
  names, all cycled at runtime and saved to config.
- One shared ~10fps animation clock driving gradients, pulsing indicators, a
  staged block-logo splash, and typewriter reveals, with every effect preserving
  its label's display width on every frame.
- A generated keymap: the footer, the expanded help, and the command palette all
  come from one table, with a coverage test that fails when a handled `ctrl` key
  is not documented.
- A mock chat source that drives the whole UI with no credentials, no network,
  and no quota, and a non-interactive path that renders one ANSI-stripped frame
  and exits 0.
- A status bar that fits by dropping trailing segments, so the dropped-message
  counter and the armed clear guard survive a narrow terminal while decoration
  does not.
- Moderation on the selected message: `d` delete, `t` time out, `b` ban, each
  armed and confirmed before it fires, with the timeout duration prompt
  defaulting to 5 minutes and bounded at 24 hours. Any unrelated key disarms a
  pending action, every refusal names an actionable reason, and redacted text is
  dropped from the rendered row rather than reprinted on a terminal that may be
  on stream. The keys stay bound without the capability and explain themselves.
- Focus-aware desktop notifications for Super Chats, new members, and a chat
  ending, dependency-free across `notify-send`, `osascript`, and PowerShell
  toast, falling back to the terminal bell. Payloads are redacted, stripped of
  control characters, and bounded.
- Deterministic SVG screenshots generated from real `View()` output, with a test
  asserting every frame is rectangular so a ragged pane fails CI.

#### Commands and configuration

- `yc chat`, `config show|path`, `doctor`, `login`, `logout`,
  `profile list|show|set`, `quota`, `setup`, `--version`, `--help`.
- A flat `key = value` config file with 49 keys, environment variables with
  `YC_`-prefixed canonical names and unprefixed Google aliases, and CLI
  overrides — resolved flags → environment → file → saved credentials → defaults.
- Non-secret config writing that rewrites known keys **in place**, preserving
  comments, ordering, and unknown keys, and excluding secrets by struct tag
  rather than by memory.
- `yc doctor`: 15 checks, 13 of them fully offline — only API reachability and
  identity touch the network.
- Opt-in redacted JSON-line debug logging on `chat`, `login`, and `doctor`, with
  `0600` files, `0700` directories, and `O_NOFOLLOW` opens on Unix.
- A Unix-only credential file with exact `0700`/`0600` modes, symlink rejection,
  no-follow opens, and atomic replacement; non-Unix builds fail closed.

#### Packaging and distribution

- `linux/amd64` and `linux/arm64` release binaries with SHA-256 checksums, and
  a POSIX `install.sh` that picks the architecture, verifies the checksum before
  installing, and adds `~/.local/bin` to `PATH` when it is missing. No macOS or
  Windows builds, no snap, no package-manager manifests, no signing,
  notarization, SBOM, or provenance.
- A credential-free CI quality gate: `go mod tidy`, `gofmt`, `go vet`,
  `go test`, `go test -race`, `govulncheck`, `staticcheck`, a build, and the
  `--help` / `--mock` / `doctor` / `config show` / `quota` smokes, all against
  isolated config and cache directories with every `YC_*` and `GOOGLE_*`
  variable emptied.
- A published documentation site and a `Dockerfile` plus `compose.yaml` for
  mock, doctor, and live runs.

### Fixed

- Terminal cell width is measured with `ansi.StringWidth` everywhere. It was
  `uniseg.StringWidth` in `internal/render`, `internal/app`, and
  `internal/animation`, and the two disagree about U+FE0F emoji-presentation
  sequences: a keycap such as `1️⃣` measures one cell to `uniseg` and two to
  `ansi`, which is what Lip Gloss uses to compose the panes. A chatter with such
  a character in their display name made the shell budget a row one cell narrower
  than it was drawn, wrapping it and **doubling the height of the entire frame**
  at every terminal width, which scrolled the user's terminal on every repaint.
- `yc doctor` no longer reports the whole cost table as estimated. Six of the
  eleven costs are published by Google; the five live chat costs are not, and
  each row now says which it is. Calling all of them estimates cast doubt on the
  documented figures and hid that `liveChatMessages.list` — the one number the
  poll budget rests on — is the guess.
- The `ctrl+t` theme picker no longer offers to "save". It never wrote
  `config.toml`; it now says `applies for this run` and `enter apply`, and a test
  pins that wording.
- `scripts/install.sh` builds its cleanup trap from a function instead of
  interpolating the temporary path into the trap string, so a `TMPDIR`
  containing a quote and a command substitution can no longer execute at exit.
  Its bash check also runs before `set -o pipefail`, so a non-bash shell now
  prints the instruction instead of `Illegal option -o pipefail`.
- The `Dockerfile` accepts a `VERSION` build argument and stamps it into the
  binary. Images built from a tagged tree reported `yc dev`; both the release
  dry run and CI now assert the stamped version and fail if it is missing.
- The documented curl-pipe install command pipes into `bash`, not `sh`. On
  Debian and Ubuntu `/bin/sh` is `dash` and the installer correctly refuses it,
  so the published one-liner could not have worked there.

### Known gaps

- **No credentialed path has ever been run against Google.** Login, polling,
  sending, moderation, identity, and stream info are unit-tested against fakes
  only. See [docs/manual-validation.md](docs/manual-validation.md).
- `search.list` resolution has no caller; `allow_search` and
  `emoji_autocomplete_mode` are parsed and displayed but never read.
- The Stream Info tab is read-only. `youtube.UpdateStreamInfo` and the category
  lookup exist in the transport; no editing UI reaches them.
- Mouse support is wheel-scroll only. There are no click targets.
- The runtime display toggles (`ctrl+g`, `ctrl+b`, `ctrl+y`, `ctrl+n`) and the
  `ctrl+t` theme picker change the running session only. Only `yc setup`,
  `yc profile set`, and `yc login --write-default-config` write `config.toml`.
- `youtube.Identity.Scopes` is never populated, so a live session takes
  moderation's "granted scopes unknown" branch: the keys stay live and the
  uncertainty is disclosed, but the disabled state is honest rather than precise.
- Releases are `linux/amd64` and `linux/arm64` only, and unsigned. No macOS or
  Windows binaries, no snap, no package-manager manifests, no signing,
  notarization, SBOM, or provenance.

[Unreleased]: https://github.com/worxbend/yc/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/worxbend/yc/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/worxbend/yc/releases/tag/v0.1.0

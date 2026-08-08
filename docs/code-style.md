# Code Style

This guide describes the local engineering style for `yc`. It is intentionally
specific to this repository: Go first, small packages, explicit errors, strong
redaction, deterministic tests, quota-aware transport, and terminal UI code that
never blocks rendering.

## Go Baseline

- Use the Go version and `toolchain` directive already pinned in [../go.mod](../go.mod).
- Use Go modules only.
- Prefer the standard library and the existing dependency set before adding
  anything new. `yc` deliberately does not depend on `golang.org/x/oauth2` or
  `charmbracelet/bubbles`; re-adding either needs a reason in a PR description.
- Keep `go.mod` and `go.sum` machine-managed through `go get`, `go mod tidy`, and
  `go mod edit`.
- Use `go tool govulncheck` and `go tool staticcheck` from the module `tool`
  directives.

## Package Boundaries

| Package | Owns | Must not own |
| --- | --- | --- |
| `cmd/yc` | Process entrypoint and CLI handoff. | Business logic. |
| `internal/cli` | Command parsing, config wiring, debug-log setup, startup orchestration. | Bubble Tea view behavior or YouTube wire-format parsing. |
| `internal/config` | Flat config, env mapping, defaults, display redaction. | Credential persistence or network clients. |
| `internal/app` | Bubble Tea model, update loop, view composition, fake/live chat boundary, per-chat UI state. | Concrete YouTube JSON types or blocking I/O in `View`. |
| `internal/youtube` | REST transport, poll scheduling, quota accounting, target resolution, moderation adapters, normalized events. | Bubble Tea components. |
| `internal/auth` | Google installed-app OAuth, scopes, capability derivation, `Secret`/`Redactor`. | UI state or credential file layout. |
| `internal/render` | Message fragments, wrapping, badge and emoji text, color decisions. | Network calls or API client ownership. |
| `internal/storage` | Disk cache, filesystem probes, Unix credential-file persistence, ledger persistence, test fakes. | UI decisions or non-Unix credential backends. |
| `internal/animation` | Grapheme-safe reveal units, bounded queues, the shared frame clock, time-pure text effects returning styled cells. | API parsing or terminal I/O. |
| `internal/debuglog` | Redacted JSON-line debug records. | Raw struct dumping. |
| `internal/theme` | Palette data and contrast correction. | App state. |
| `internal/emoji` | Grapheme-cluster emoji detection. | Rendering decisions. |

## Error Handling

Return explicit errors with enough context for an operator to act, but never
include a token, refresh token, client secret, API key, authorization code,
OAuth state, PKCE verifier, authorization URL, bearer header, credential file
content, request URL, or query string.

Use `errors.Is`/`errors.As` for sentinel behavior. `internal/youtube` classifies
every API failure onto one sentinel from all three of Google's disagreeing
channels — the legacy `error.errors[].reason`, the canonical `error.status`, and
the `google.rpc.ErrorInfo` reason — and an unrecognized combination degrades into
a sane retry policy rather than an unhandled state.

Redact the message while **preserving the chain**. `errors.New(redact(err.Error()))`
also keeps tokens out, but it throws the cause away and leaves callers with
nothing but a string to classify by. Use the `safeError` pattern: a redacted
`Error()` with a real `Unwrap()`.

Keep transient failures distinct from definitive ones. A quota-exhausted error
must never be retried; a 5xx must be.

## Context And Cancellation

Every network, cache, credential, login, and transport operation accepts a
`context.Context` or is called from a function that already owns cancellation.
Reconnect cancels the old session and drains it before the replacement exists.

Time is injectable wherever behavior depends on it: `PollerConfig.Now`/`Sleep`
and `LedgerConfig.Now` exist so the whole state machine, including the Pacific
midnight rollover across a DST boundary, is driven by a fake clock.

## Terminal UI Rules

Bubble Tea `Update` can schedule commands, mutate model state, and consume typed
messages. Bubble Tea `View` must stay pure: **no network calls, no filesystem
writes, no blocking reads, no sleeps**. If a view needs data, schedule work
through a command and render a stable fallback until the result arrives. Quota
snapshots are read in `Update` and mirrored onto the model precisely for this
reason.

Keep narrow layouts usable. Sidebar, activity column, help, status bar, composer,
inspect panel, and chat rows must degrade predictably when width or height is
small. The status bar fits by **dropping trailing segments**, so anything that
must survive a narrow terminal goes first — the dropped-message counter and the
armed clear guard lead, because a silent loss and an invisible confirmation
prompt are both worse than losing every decoration on the line.

## Rendering Rules

Normalize before rendering. Render fragments, not raw payloads. Use width-aware
and grapheme-aware APIs (`rivo/uniseg`, `charmbracelet/x/ansi`) for anything
user-visible. **Never slice a user-visible string by byte or rune** when grapheme
clusters, emoji, combining marks, or ANSI styles can be involved.

**Measure width with `ansi.StringWidth`, never `uniseg.StringWidth`.** Split by
grapheme clusters with `uniseg`; measure cells with `ansi`. The two disagree
about U+FE0F emoji-presentation sequences — a keycap like `1️⃣` is one cell to
`uniseg` and two to `ansi` — and Lip Gloss composes the panes with `ansi`. A
package that budgets a row by one measurement and draws it with the other wraps
the row, and one wrapped row doubles the height of the whole frame and scrolls
the user's terminal on every repaint. The package that draws must be the package
that measures. `internal/app/grapheme_shell_test.go` keeps that class of name in
its corpus so a regression is a test failure rather than a bug report.

Avatars, badges, and emoji always render as text. There is no image path, and
reintroducing one needs an ADR: the live chat API supplies no badge imagery and
no per-message emote metadata, so there is nothing faithful to render from.

Author color is a deterministic, contrast-corrected hash of the author's channel
ID. It must stay derived rather than stored, so one author keeps one color with
no UI-owned mutable color state.

## Quota Rules

These are specific to this project and are not optional.

- **Charge the ledger for every dispatched call, including failures.** Google
  charges at least one unit for an invalid request.
- **`pollingIntervalMillis` is an absolute floor.** No configuration, jitter,
  backoff decay, or override may produce an interval beneath it.
- **Never retry an exhausted quota.** Every attempt is charged and the allowance
  does not return until the Pacific reset.
- **Never present an estimate as a fact.** Google publishes no cost for any
  live-chat method. Every rendered unit figure carries an `est.` marker, and the
  marker is the last thing dropped when space runs out.
- **Keep costs as data.** The cost table is config-overridable so a corrected
  figure is a config line rather than a release.
- **Prefer the cheapest resolution ladder.** An explicit `liveChatId` costs
  nothing; `videos.list` and `channels.list` cost one unit; `search.list` spends a
  scarce separate allowance and must stay opt-in behind an explicit prompt.
- **Decline locally before spending.** The send limiter refuses before dispatch
  rather than learning the limit from a 429, because an insert costs an estimated
  50 units whether it is accepted or not.

## Secret And Debug Rules

Debug logging is opt-in. Records use curated fields and redaction helpers. Do not
log a raw `ConnectionState`, `Message`, API response body, request URL, query
string, HTTP header, OAuth callback query, or unfiltered error from auth,
storage, or network code.

Secrets travel as `auth.Secret`, so ordinary formatting and ordinary JSON
encoding redact them. Reveal only at the two deliberate boundaries: the HTTP
request that sends the credential to Google, and the storage-owned marshal path
that writes the credential file. Never for a log, a diagnostic, a snapshot, or
user-facing output.

Tests use obvious fake markers such as `test-not-a-real-token` and
`AIzaSyTEST-not-a-real-key`, so a leak is greppable. Never place a real
credential in a fixture.

## Comment Style

Write comments where they preserve design intent, security constraints, package
boundaries, quota reasoning, or non-obvious invariants. The comment density in
this repository is deliberately high on *why* and deliberately zero on *what*:
a comment that narrates an assignment or repeats a function name is noise, and a
comment explaining why a terminal condition parks with the streams open is the
difference between a maintainer keeping the behavior and "fixing" it.

Package comments explain the package's responsibility and its boundary. Exported
identifiers need comments when the name alone does not explain safe use,
redaction behavior, concurrency expectations, estimate status, or platform
limitations.

## Test Style

Prefer focused table tests and deterministic fakes. **Never use a wall-clock
sleep** when a fake clock or an explicit message can drive the behavior. For
high-throughput chat and animation, assert queue bounds, overflow behavior,
stable layout, and input responsiveness.

Use real files only when filesystem permissions, symlink rejection, no-follow
opens, cache paths, or atomic replacement are under test. Keep test temp
directories isolated and credential-free.

Generate user-facing surfaces from one table rather than maintaining them beside
each other, and test the coverage. The keymap is the example: the footer, the
expanded help, and the command palette are all generated from `keyBindings`, and
a coverage test fails when the update loop handles a `ctrl` key that the table
does not document. `twi` learned this the hard way — `ctrl+e` opened the emote
picker dozens of times a session and appeared in none of the three surfaces.

## Documentation Style

Docs distinguish **ready**, **partial**, **planned**, **manual**,
**credentialed**, and **out of scope**. Link related docs with relative `.md`
paths. No trailing whitespace in any file.

Never claim a credentialed YouTube path was verified.
[manual-validation.md](manual-validation.md) is the only place such evidence may
be recorded, and it currently records none.

When a feature exists in the transport but has no caller — moderation,
`search.list` resolution, ledger pruning — say so explicitly rather than
describing the transport as if it were the feature.

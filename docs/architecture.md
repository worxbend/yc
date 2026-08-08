# Architecture

`yc` keeps YouTube transport, poll scheduling, quota accounting, authentication,
rendering, storage, and Bubble Tea state separated so each lane can be tested
without a Google account, a network, or a specific terminal.

Companion references for the parts this page only sketches:
[events.md](events.md) (every event kind and how it renders),
[keybindings.md](keybindings.md) (input dispatch order),
[moderation.md](moderation.md), [themes.md](themes.md), [quota.md](quota.md),
and [security.md](security.md).

## System Shape

The runtime shape is a set of narrow boundaries around the Bubble Tea shell:

```text
        config/env/flags                 YouTube Data API v3
              |                        (REST, polled, metered)
              v                                  |
        internal/cli ------------------> internal/youtube
              |     \                    /    |         \
              |      \                  /     |          \
              |    internal/auth   Poller  QuotaLedger  Client
              |      (OAuth)          \      |         /
              v                        \     |        /
        internal/app  <----------- normalized events + quota snapshots
              |
      +-------+-------------------+
      |                           |
      v                           v
 internal/render            internal/theme
      |
      v
 width-bounded rows of semantic fragments
```

`internal/storage` is reached from `internal/cli` (credential file),
`internal/youtube` (the persisted quota ledger, through `LedgerStore`), and
`internal/app` (the writability probe in diagnostics). `internal/animation`,
`internal/emoji`, and `internal/debuglog` are leaf utilities with no lane of
their own. Verify any edge in this diagram with `go list -deps ./internal/<pkg>`
before relying on it.

The app consumes normalized `internal/youtube` models and app-facing interfaces
declared in `internal/app/contract.go`. It does not import YouTube Data API JSON
shapes, and it performs no network or filesystem I/O from `View`.

## Data Flow: Polling

This is the part that differs most from `twi`, which owns an IRC socket that
pushes. `yc` owns a loop that pulls, on a schedule it has to justify.

```text
  ChatTarget (raw string)
        |  ParseChatTarget — pure, free
        v
  kind: live-chat-id | video | channel | handle
        |
        |  ResolveTarget — the cheapest ladder that can answer
        |    live-chat-id -> 0 units
        |    video        -> videos.list      (1 unit est.)
        |    handle/id    -> channels.list    (1 unit est.)
        |    search.list  -> opt-in, separate 100-call bucket, NOT WIRED
        v
  liveChatId
        |
        v
  +----------------------- Poller.run -----------------------+
  |                                                          |
  |  priming: list without a page token -> Historical=true   |
  |     |                                                    |
  |     v                                                    |
  |  streaming: list with the retained page token            |
  |     |                                                    |
  |     |-- normalize -> Message / ModerationEvent /         |
  |     |               RoomEvent / PollState                |
  |     |-- dedupe (8000-ID ring)                            |
  |     |-- charge the QuotaLedger (every call, incl. errors) |
  |     |                                                    |
  |     v                                                    |
  |  NextInterval(serverFloor, budgetFloor, min, max, backoff)|
  |     = clamp(max(serverFloor, budgetFloor, min), min, max) |
  |       * backoff, +/- jitter                              |
  |     |                                                    |
  |     v                                                    |
  |  sleep(ctx, interval) -> loop                            |
  +----------------------------------------------------------+
        |
        v
  five buffered channels: Messages, ConnectionStates,
  Moderations, RoomEvents, Polls
        |
        v
  internal/app: one blocking receive per stream, re-armed each delivery
```

Five properties of that loop are load-bearing:

1. **`pollingIntervalMillis` is an absolute floor.** No jitter, no backoff decay,
   and no configuration can produce an interval beneath it. `NextInterval`
   enforces it after every other adjustment.
2. **The page token is retained.** An empty `nextPageToken` never clears the
   stored one, because falling back to a token-less request re-delivers the
   whole backlog and charges for the privilege.
3. **The poller drops rather than blocks.** Blocking an emitter stalls the
   goroutine that owns the poll schedule, which costs the whole session. Drops
   are counted and shown in the status bar at every width.
4. **A terminal condition parks with the streams open.** The app adapter treats a
   closed stream as "retry this chat", and retrying an ended chat or an exhausted
   quota spends units to be told the same thing again. Closing is the user's
   decision, taken with `ctrl+r`.
5. **A reconnect restarts the poller in place.** See below — a rebuild is the
   fallback, not the normal path.

## Data Flow: Reconnect

`ctrl+r` and the automatic recovery paths both go through one door. The shape
matters because the naive implementation — tear the transport down and build a
new one — loses the page token, the resolved target, and the dedupe ring, and
then pays a resolution unit and a full re-delivery to get back to where it was.

```text
  liveChatSession.restart()
        |
        |  does the transport implement app.LiveChatReconnector?
        |
        +-- yes -> Poller.Reconnect(ctx)
        |            - serialized by restartMu, so two overlapping restarts
        |              cannot each cancel one session and strand the other's
        |              goroutine still charging quota
        |            - streams stay open, the fan-in never stops
        |            - page token, resolved target, and dedupe ring survive
        |
        +-- no, or Reconnect returned an error
                   (logged as app.live_chat.restart_in_place_failed)
                     -> close the transport, rebuild through the factory,
                        mark explicitRestart, re-resolve, re-prime
```

`LiveChatReconnector` is an **optional** capability: a transport without it keeps
the old rebuild behavior, which is what lets the fake transports in the app tests
stay unchanged.

Two API answers are treated as "the continuation token was rejected" rather than
as the end of the chat: **400 or 404 while a token is held**. That check runs
*before* the terminal `ErrChatEnded` / `ErrChatDisabled` / `ErrChatNotFound` case,
and it clears the rejected token so the next call re-primes instead of presenting
a cursor the API already refused. Without a token — during priming, or on the one
retry per session — a 404 is still terminal, so a chat that really has ended
still ends, one call later.

## Data Flow: Moderation

Outbound moderation is the one path that removes somebody else's words from a
live broadcast, and its shape is dictated by that.

```text
  d / t / b on the selected message
        |
        |  capability ladder, cheapest first, each failure naming itself:
        |    no chat source -> not a Moderator -> ModerationAvailable() false
        |    -> no chat open -> scopes lack force-ssl -> role is viewer
        |
        |  per-target refusals (already removed, local echo, no channel id,
        |  your own channel, unresolved chat) - before anything is armed
        v
  armed confirmation (or the modal duration prompt for a timeout)
        |
        |  same key again, or enter. esc cancels. ANY other key disarms
        |  and then does its own job.
        v
  optimistic redaction + rollback record
        |  delete      -> the one message
        |  ban/timeout -> every message from that channel
        |  the removed text lives ONLY in the unexported rollback record
        v
  one request, 30s bound, through app.Moderator
        |
        +-- ok   -> drop the rollback record, echo a ModerationEvent locally
        |           (the API does not report yc's own deletions back)
        +-- fail -> restore every row exactly, say "(nothing was removed)"
```

Two interfaces, not one, and the split is load-bearing. Go interfaces are
satisfied by a **type**: `*LiveChatClient` has the `Moderator` methods whether or
not a credential was wired, so asserting `Moderator` alone reports the capability
as present and only fails after a destructive keystroke has been confirmed.
`ModerationCapability` is the instance-level answer, asked before anything arms.

`app.ModerationCapability`, `app.LiveChatConfig.Moderator`, and
`app.ErrModerationUnavailable` are the surface; `internal/cli` wires the one
shared `youtube.Client` in as the moderator, which keeps moderation calls on the
same quota ledger as everything else. Full treatment:
[moderation.md](moderation.md).

## Data Flow: Quota

```text
  every dispatched call
        |
        v
  QuotaLedger.Charge(endpoint)          CostTable (config-overridable;
        |  mutex-guarded                  published for the six documented
        |  Pacific-day rollover           endpoints, ESTIMATED for the five
        |  search.list -> its own bucket  liveChat methods Google does not
        v                                 publish a cost for)
  FileLedgerStore  ->  <cache>/yc/quota/<fingerprint>-<YYYY-MM-DD>.json
        |                (0600 in 0700, atomic temp+rename, validated keys)
        |                pruned to LedgerRetentionDays (7) once per start,
        |                on a context.WithoutCancel goroutine so a run that
        |                fails at startup still tidies up
        v
  QuotaSnapshot { used, limit, remaining, search, byEndpoint,
                  resetAt, serverFloor, budgetFloor,
                  effectiveInterval, mode, estimated: true }
        |
        +--> Poller.budget()  -> BudgetFloor(remaining, cost, horizon)
        |                        = horizon / (remaining / cost)
        +--> status bar meter + effective cadence
        +--> the quota tab (alt+3)
        +--> yc doctor / yc quota
```

`ResetAt` constructs the *next calendar midnight* in `America/Los_Angeles`
rather than adding 24 hours, so a DST boundary moves the reset by an hour
instead of by a day. The tz database is embedded (`_ "time/tzdata"`); a build
that somehow cannot load it falls back to a fixed `-08:00`.

Rollover reloads the new day's persisted tally rather than carrying yesterday's
forward. Two accounts on one machine keep separate meters, keyed by a truncated
SHA-256 fingerprint of the client ID and channel ID.

Full treatment: [quota.md](quota.md).

## Message Flow

Live chat starts in `internal/cli`, which loads config, applies saved
credentials beneath it, classifies the resulting capability, validates the token
when Google is reachable, builds one `youtube.Client` over one `QuotaLedger` and
one credential holder, then hands app-facing options to `internal/app`.

One holder, one ledger, one REST client is deliberate: a refresh reaches every
feature, and the meter counts every call `yc` makes.

`internal/youtube` converts `liveChatMessages.list` responses into normalized
messages, moderation events, room events, poll state, and connection state.
Every documented `snippet.type` has an `EventKind`, including the ones the API
no longer delivers; an unrecognized type is carried through with `RawType`
intact and rendered from `snippet.displayMessage`, never dropped and never a
crash.

Moderation is kept **off** the message stream on purpose. A deletion is an
instruction to take text off a screen that is frequently on stream; rendering it
as another chat row would put the removed text back in front of the viewer.
Consumers apply moderation to messages they already hold.

`internal/app` stores per-chat history, unread counts, composer drafts, reply
context, inspect state, send status, scroll offsets, local filters, and
connection state. Switching chats changes the active view without losing
per-chat state. The open set is dynamic: the picker adds and the sidebar
removes, through the optional `ChatJoiner` capability when the transport has it
and locally when it does not.

`internal/render` converts normalized messages into width-bounded rows of
semantic fragments: timestamps, badges, author names, mentions, paid-message
banners, membership notices, moderation notices, deletions, emoji, and initials
chips. Author color is a deterministic hash of the channel ID (falling back to
display name), contrast-corrected against both message surfaces, so one author
keeps one color with no UI-owned mutable color state.

## Credential Flow

Precedence, highest first:

```text
CLI flags > environment > flat config file > Unix saved credentials > defaults
```

Saved credentials sit at the bottom on purpose: an operator who exports a token
for one run must be able to do so without deleting their saved login, and the
reverse — a stale saved token quietly overriding an explicit export — is the
failure that is hard to diagnose. When both exist, `yc` reports the saved one as
*shadowed* rather than silently ignoring it.

Capability is derived without any network access, so `yc chat` can refuse early
and `yc doctor` can report offline:

```text
  no token, no key   -> mode "none":     --mock only
  key only           -> mode "api-key":  read public chats; no send, no moderate
  token              -> mode "oauth":    scopes decide
       |
       +-- scopes known (they came from the credential file yc wrote)
       |     force-ssl granted -> send + moderate
       |     readonly only     -> read, with an explicit reason on the composer
       +-- scopes unknown (token came from env or config)
             optimistic: controls enabled, the API is the authority
```

`yc` degrades **by capability, not by hiding controls**: a disabled composer
that explains itself teaches the user what to fix; a missing one does not. The
moderation keys follow the same rule — they stay bound, stay in help, and answer
with a reason.

### Refresh, and a mid-session 401

One `credentialHolder` owns the live credential set, and every API client reads
the token through it at **request** time rather than capturing it at
construction. That is what makes one refresh reach every feature at once.

```text
  background loop: refresh 5 minutes before the known expiry
        |
        |  (and, when the API rejects a bearer call anyway)
        v
  401 with a token present
        |  sample authGeneration BEFORE dispatch
        |
        +-- stale epoch (someone else's refresh already landed)
        |     -> retry immediately, exchange nothing
        |
        +-- current epoch -> join or start the single-flight exchange
                               ClientConfig.OnAuthFailure, auto-wired from a
                               CredentialSource implementing CredentialRefresher
                                |
                                +-- ok   -> re-sign from scratch (new URL, new
                                |           Authorization, fresh per-attempt
                                |           timeout, body re-read) and retry
                                |           EXACTLY ONCE - a separate statement,
                                |           not a loop, so the bound is structural
                                +-- fail -> terminal: "the sign-in expired and
                                            could not be renewed; run `yc login`",
                                            unwrapping to ErrAuthFailed
```

Only **401 with a token** is retried. 403, 429, 5xx, and a key-only 401 are
untouched and stay with the poller's backoff ladder — a refresh cannot fix any of
them. The refresh error's identity is dropped: only text that has been through
the URL strip, the URL-pattern scrub, and a redactor holding the stale token, the
new token, and the API key survives into the message.

A token that was renewed but could not be **written** is a warning, not a
failure: the pending request is retried and the write failure is reported through
the holder's error reporter, because a session that works is better than a
session that dies over a cache miss. The rotated refresh token is still persisted
*before* the new access token becomes visible on the normal path.

The credential file is Unix-only: a private `credentials.json` under the
platform config directory, with exact `0700` directory and `0600` file modes,
symlink rejection, no-follow opens, and atomic replacement. Non-Unix builds must
keep returning a redacted unsupported-platform sentinel.

## Debug Flow

Debug logs are JSON lines written only when explicitly enabled. Callers send
curated fields through `internal/debuglog`; auth, config, storage, transport,
quota, and render code must not dump raw structs, response bodies, request URLs,
or query strings. Files are created `0600` under a `0700` directory; Unix builds
open the final path with `O_NOFOLLOW` and validate the opened descriptor.

`APIError` is assembled from classified fields rather than from anything the
transport saw, so there is no path by which a request URL — which carries the
API key in its query string — can reach an error message.

## Error Classification

Google returns three parallel, disagreeing classification channels, and `yc`
reads all three: the legacy `error.errors[].reason`, the canonical
`error.status`, and the modern `google.rpc.ErrorInfo` reason. They map onto one
sentinel set, and an unrecognized combination degrades into a sane retry policy
(`5xx` transient, other `4xx` not-permitted) rather than an unhandled state.

| Sentinel | Poller reaction |
| --- | --- |
| `ErrQuotaExceeded` | Stop. Never retry — every attempt is charged and the allowance does not return until the reset. |
| `ErrChatEnded` / `ErrChatDisabled` / `ErrChatNotFound` | Stop cleanly. History stays on screen. |
| `ErrAuthFailed` / `ErrNotPermitted` / `ErrNoCredentials` | Stop. The transport has already spent its one refresh-and-retry on a 401 by the time the poller sees this. |
| `ErrRateLimited` | Back off, capped at 120s. The API asking `yc` to slow down is a statement about the next few minutes. |
| `ErrTransient` | Back off, capped at 60s. A 5xx is usually over in seconds. |
| anything else | Stop and report. |

A success decays the ladder one step rather than clearing it, so a flapping
connection does not slam straight back to full cadence.

## Extension Points

The project can grow without collapsing boundaries:

- If `liveChatMessages.streamList` ever ships in the discovery document, it can
  be substituted behind the same `Poller` type — the transport is factored for it.
- New event types add an `EventKind` and a render class; the layouts switch on
  `MessageType`, not on `EventKind`. The unknown-kind fallback means a new type
  renders readably before anyone writes that code — see [events.md](events.md).
- A corrected quota cost is a config line, not a release: the cost table is data.
- A new transport capability is an optional interface asserted on the client
  (`ChatJoiner`, `Moderator`, `ModerationCapability`, `LiveChatReconnector`,
  `QuotaReporter`), never a new method on `ChatClient`. That is what keeps the
  mock source, the deterministic fake, and the live poller satisfying one
  required surface.

Extension work keeps the same rule: the UI depends on internal interfaces and
normalized models, not on Google's types.

## Theming And Animation

`internal/theme` resolves a `Palette` (background, foreground, accent, muted,
border, surface, warning, error, success) from one of 58 preset names or a custom
hex palette — see [themes.md](themes.md). `internal/app` reads one active palette
and every widget derives its colors from it. The full viewport and the terminal's own OSC 11 background use a
slightly darker derived canvas; framed panes share a raised surface, an
icon-bearing title, a quiet frame, and a role-colored left rail.

A single shared `animation.FrameMsg` tick (~10fps, skipped when
`animation_mode = "off"`) drives every chrome effect — gradients, pulsing
indicators, the staged block-logo splash, and typewriter reveals — instead of
each effect running its own ticker. `animation.TextFrame` renders a label under
one of four effects as a pure function of elapsed time and returns styled cells
rather than escape sequences, so the package owns no terminal I/O. Every effect
preserves its label's display width on every frame, so an animated label cannot
reflow the pane around it.

Splash art is centered as a block (`centeredBlockLines`) rather than line by
line: right-trimming each row before centering is correct for a label and shears
a logo, because each row is then displaced by its own indent.

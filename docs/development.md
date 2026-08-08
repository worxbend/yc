# Development

This document summarizes the development workflow and implementation state for
`yc`. Start with [../CONTRIBUTING.md](../CONTRIBUTING.md) for the contribution
flow, [code-style.md](code-style.md) for local code rules, and
[architecture.md](architecture.md) for the runtime data-flow map.

Behavior references, each generated from or checkable against the source:
[keybindings.md](keybindings.md), [events.md](events.md),
[themes.md](themes.md), [moderation.md](moderation.md),
[security.md](security.md), and [faq.md](faq.md).

## Current State

- Module `github.com/worxbend/yc`, `go 1.26`, `toolchain go1.26.5`.
- `govulncheck` and `staticcheck` are pinned as module `tool` directives.
- Go modules only. No GOPATH workflows.
- Eleven internal packages plus `cmd/yc`. Count the tests rather than trusting a
  number in a document:

  ```sh
  find . -name '*_test.go' | wc -l          # test files
  grep -rn '^func Test' --include='*_test.go' . | wc -l   # top-level tests
  go test ./... | cat                        # per-package result
  ```

  At the time of writing: 109 test files and 910 top-level test functions, all
  passing under `-race`. `go test ./...` on Linux runs 908 of them and skips one:
  `TestWriteDocsScreenshots` is gated on `YC_WRITE_SCREENSHOTS=1` so CI never
  writes into the working tree. The two that do not appear at all are behind
  `//go:build !unix` — they assert the credential store's unsupported-platform
  sentinel, and they can only run on a platform yc does not release for.
- Dependencies are deliberately few: Bubble Tea, Lip Gloss, `charmbracelet/x/ansi`,
  `muesli/termenv`, `rivo/uniseg`, `golang.org/x/term`, and `BurntSushi/toml`
  (indirect). **Notably absent:** `golang.org/x/oauth2` and `charmbracelet/bubbles`.
  The OAuth flow is hand-rolled because the installed-app flow with PKCE, a
  loopback listener, typed `Secret` values, and strict redaction is a
  small amount of code and a large amount of control over what can reach a log
  line. The UI components are hand-rolled because every widget has to be
  width-exact and theme-driven.

### Behavior status

**Ready** — exercised by tests and credential-free smokes:

- The mock chat source and the whole Bubble Tea shell, including a
  non-interactive single-frame path that strips ANSI and exits 0.
- `yc config show` / `yc config path` / `yc profile list|show|set` / `yc quota` /
  `yc doctor` / `yc setup`.
- The quota ledger: charging, Pacific-day rollover including the DST boundary,
  per-credential isolation, persistence, refusal of unsafe filename keys.
- Poll pacing: server-floor inviolability, budget floor, jitter, backoff ladder,
  page-token retention, dedupe, the offline grace window, the reserve threshold,
  and error-class routing — all on a fake clock with no wall-clock sleeps.
- Rendering, theming, animation, emoji tokenization, mention autocomplete,
  filters, inspect, help, the command palette, and the status bar.
- The moderation **UI** — capability detection, the `d`/`t`/`b` keys, the modal
  duration prompt, confirmations, per-target refusals, optimistic redaction and
  rollback, and every disabled explanation. See [moderation.md](moderation.md).
- Reconnect: in-place `Poller.Reconnect` with token, resolved target, and dedupe
  ring preserved, and the rebuild fallback when a transport cannot restart.
- Ledger retention: `FileLedgerStore.Prune` at `LedgerRetentionDays` (7), swept
  once per `yc chat` start on a detached goroutine.
- Redaction, verified by tests that scan output for fake secret markers.

**Partial**:

- Credential storage is Unix-only.
- The Stream Info tab is read-only; `youtube.UpdateStreamInfo` exists with no UI.
- Mouse support is wheel scrolling only.
- `Moderator.Unban` is transport-only and deliberately unbound: `liveChatBans`
  has no `list`, so a ban ID is only knowable within the session that created it.
- The `ctrl+t` theme picker applies a palette for the session but does not write
  `config.toml`; `yc profile set` is what persists one. The header and footer say
  so (`applies for this run`, `enter apply`), and a test pins that wording.
- `youtube.Identity.Scopes` is declared as the granted scope list and nothing
  populates it, so live sessions take the "granted scopes unknown" branch.

**Credentialed — implemented, unit-tested against fakes and `httptest`, never
run against Google**:

- `yc login` / `yc logout`, token refresh, and identity resolution.
- Mid-session 401 recovery: `ClientConfig.OnAuthFailure`, auto-wired from a
  `CredentialSource` that implements `CredentialRefresher`, single-flight with a
  stampede guard, retrying exactly once.
- Live polling, sending, moderation requests, and every transport call.

**Planned**:

- Wiring `youtube.SearchLiveVideo` behind the `allow_search` opt-in.
- Populating `Identity.Scopes` from the credential source, so the moderation
  capability can be precise instead of merely honest.

**Out of scope**: any image rendering path; non-Unix saved credentials; slow
mode, pinning, members-only toggling, and ban listing (no API exists).

For release packaging and installation, [release.md](release.md) and
[install.md](install.md) are the authority — do not infer either from this page.

## Package Lanes

```text
cmd/yc             process entrypoint only
internal/cli       command parsing, config wiring, debug-log setup, startup orchestration
internal/config    flat TOML config, env mapping, defaults, display redaction
internal/app       Bubble Tea model/update/view, per-chat UI state, fake + live chat boundary
internal/youtube   YouTube Data API v3 transport, polling, quota accounting, normalized events
internal/auth      Google OAuth2 installed-app flow, scopes, secret redaction
internal/render    normalized message -> width-bounded rows of semantic fragments
internal/theme     palettes + contrast correction
internal/storage   hardened Unix credential file, disk cache, filesystem probes
internal/animation shared frame clock, grapheme-safe reveal, time-pure text effects
internal/debuglog  redacted JSON-line debug records
internal/emoji     emoji grapheme detection
```

Keep boundaries strict:

- The UI depends on internal interfaces and normalized models, never on YouTube
  Data API JSON types.
- Transport code never imports Bubble Tea.
- Rendering consumes normalized messages, never raw API payloads.
- Debug logging goes through `internal/debuglog` with curated fields only.
- Network work never blocks Bubble Tea `Update` or `View`.

## Core Interfaces

`internal/app/contract.go` declares one required boundary and a set of optional
capabilities asserted on the client, so the mock source, the deterministic fake,
and the live poller all satisfy the same required surface:

| Interface | Required? | Purpose |
| --- | --- | --- |
| `ChatClient` (`MessageStream` + `Sender`) | required | messages, connection state, sends |
| `ModerationSource` | optional | deletions, tombstones, bans, timeouts — kept **off** the message stream so a removal never reprints the removed text |
| `RoomEventSource` | optional | members-only mode, chat ended, broadcast offline |
| `PollSource` | optional | creator polls, from both `pollEvent` items and the response's `activePollItem` |
| `ChatJoiner` | optional | start/stop polling an additional chat without a reconnect |
| `Moderator` | optional | delete, ban, timeout, unban |
| `ModerationCapability` | optional | whether a client that *has* the `Moderator` methods can currently act on them |
| `LiveChatReconnector` | optional | restart a transport in place, keeping its page token, resolved target, and dedupe ring |
| `MessageDropCounter` | optional | how many rows were discarded; shown at every width |
| `QuotaReporter` | optional | the estimated ledger and the cadence it implies |
| `PollIntervalSource` | optional | cadence without a full ledger |

`ModerationCapability` is separate from `Moderator` because Go interfaces are
satisfied by a **type**, not by an instance: an adapter built without a
moderating credential still has the methods, so asserting `Moderator` alone
reports the capability as present and only fails after a destructive keystroke
has been confirmed.

A transport without an optional capability is not an error: the model reports the
action as unavailable instead of leaving the UI in a state it cannot leave.

Use `youtube.FakeChatClient` and `storage.MemoryCredentialStore` for
deterministic tests.

## Toolchain And Quality Gate

`.github/workflows/ci.yml` is the authority. It runs four jobs on every push to
`main` and every pull request:

| Job | Blocking? | Does |
| --- | --- | --- |
| `gate` | yes | the Go gate below, plus package-boundary, leak, and whitespace checks |
| `shell` | yes | `shellcheck -x scripts/*.sh`, plus executable-bit and shebang checks |
| `docs` | yes | every **relative** Markdown link and heading anchor resolves |
| `docker` | **no** (`continue-on-error`) | builds the image and smokes it credential-free |

The `docker` job reports rather than blocks: the image is a convenience path, not
part of the required gate, and it has never been verified on a release host.

The `docs` job deliberately does **not** fetch external URLs — a third-party
outage must not fail an unrelated pull request, and no CI step should make
outbound requests on a fork's behalf.

### Run the gate locally

```sh
export GOTOOLCHAIN=auto TERM=xterm-256color
export XDG_CONFIG_HOME="$(mktemp -d)" XDG_CACHE_HOME="$(mktemp -d)"
export XDG_STATE_HOME="$(mktemp -d)" XDG_DATA_HOME="$(mktemp -d)"
export YC_GOOGLE_CLIENT_ID= YC_GOOGLE_CLIENT_SECRET= YC_GOOGLE_ACCESS_TOKEN=
export YC_GOOGLE_REFRESH_TOKEN= YC_GOOGLE_REDIRECT_URL=
export YC_YOUTUBE_API_KEY= YC_YOUTUBE_CHANNEL_ID= YC_DEFAULT_CHATS=
export GOOGLE_CLIENT_ID= GOOGLE_CLIENT_SECRET= GOOGLE_ACCESS_TOKEN=
export GOOGLE_REFRESH_TOKEN= GOOGLE_REDIRECT_URL=

go version
go mod tidy && git diff --exit-code go.mod go.sum
gofmt -l .                      # must print nothing; CI fails on any output
go vet ./...
go test ./...
go test -race ./...
go tool govulncheck ./...
go tool staticcheck ./...

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/yc-amd64 ./cmd/yc
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /tmp/yc-arm64 ./cmd/yc
```

Then the credential-free smokes, against the built binary rather than `go run`,
because that is what CI exercises:

```sh
yc=/tmp/yc-amd64
"$yc" --help
"$yc" --version
"$yc" doctor
"$yc" config show
"$yc" config path
"$yc" quota
"$yc" profile list
"$yc" login --dry-run
timeout 60 "$yc" chat --mock < /dev/null
timeout 60 "$yc" chat --mock --chat one --chat two < /dev/null
timeout 60 "$yc" chat --mock --chats one,two < /dev/null

# a live chat with no credentials must refuse with exit 2, not exit 1
"$yc" chat --video dQw4w9WgXcQ < /dev/null; echo "exit=$?"   # expect exit=2
```

The empty credential variables plus isolated XDG directories keep every smoke
independent of a developer's local config, credential file, quota ledger, or
Google account. `chat --mock` renders one frame and exits when stdin closes; the
`timeout` is a backstop so a regression that blocks on input fails instead of
hanging.

### The checks that are not `go` commands

CI enforces these too, and they are the ones most easily missed locally.

```sh
# 1. package boundaries
go list -deps ./internal/youtube | grep -i charmbracelet          # must be empty
go list -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}' ./internal/render |
  grep bubbletea                                                  # must be empty
go list -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}' ./internal/app |
  grep -E '^(net/http|net/http/.*|google\.golang\.org/.*)$'        # must be empty
grep -rn 'json:"' internal/app --include='*.go' | grep -v '_test\.go'   # must be empty
grep -rnE 'os\.Open|os\.Create|http\.|ReadFile|WriteFile|time\.Sleep' \
  internal/app --include='*.go' | grep -v '_test\.go'              # must be empty

# 2. no trailing whitespace in any tracked file
# [[:blank:]], not [ \t]: in a bracket expression \t is a literal backslash
# and t, so '[ \t]$' also matches every line ending in "t".
git ls-files -z | xargs -0 -r grep -nIH '[[:blank:]]$'             # must be empty

# 3. no whitespace errors or conflict markers in the diff
git diff --check origin/main...HEAD

# 4. shell scripts
#    CI pins shellcheck v0.11.0. Which checks fire varies between releases, so
#    an older local shellcheck can report findings CI does not, and vice versa.
shellcheck -x scripts/*.sh

# 5. every relative doc link and anchor resolves
#    (CI runs an inline Python checker; see the `docs` job in ci.yml)
```

Replace `origin/main` with the PR base branch when needed; use plain
`git diff --check` for uncommitted local changes.

Credentialed YouTube behavior is **not** in any gate and never can be. Record it
in [manual-validation.md](manual-validation.md) or do not claim it.

Restricted-environment form, when the module cache is read-only:

```sh
GOTOOLCHAIN=local GOCACHE=/tmp/yc-gocache GOMODCACHE=/tmp/yc-gomodcache go test ./...
STATICCHECK_CACHE=/tmp/yc-staticcheck-cache go tool staticcheck ./...
```

## A Credential-Leak Smoke

The one check worth running by hand after touching auth, transport, or logging.
It uses an obviously fake key-shaped marker and asserts it reaches nothing:

```sh
tmp=$(mktemp -d)
env XDG_CONFIG_HOME="$tmp/config" XDG_CACHE_HOME="$tmp/cache" \
  YC_YOUTUBE_API_KEY='AIzaSyTEST-not-a-real-key-000000000000000' \
  go run ./cmd/yc chat --mock --debug-log --debug-log-path "$tmp/debug.log" \
  > "$tmp/out" 2> "$tmp/err"
grep -rn 'AIzaSyTEST' "$tmp" && echo LEAK && exit 1
stat -c '%a' "$tmp/debug.log"     # expect 600
```

## Focused Review Searches

```sh
# the app lane must not learn YouTube JSON shapes
rg 'liveChatMessages|snippet\.|authorDetails' internal/app --glob '!**/*_test.go'

# View must stay pure
rg 'os\.Open|http\.|ReadFile|WriteFile|time\.Sleep' internal/app --glob '!**/*_test.go'

# no byte/rune slicing of user-visible strings
rg '\[[0-9a-zA-Z_]*:[0-9a-zA-Z_]*\]' internal/render --glob '!**/*_test.go'

# trailing whitespace anywhere in docs
rg -n '[ \t]+$' README.md docs CONTRIBUTING.md SECURITY.md CHANGELOG.md
```

## Testing Strategy

Unit coverage includes:

- Config precedence, including empty-environment-value handling.
- Secret redaction, with obvious fake markers.
- OAuth flow states: state mismatch, reuse, denial, expiry, scope subsumption.
- Chat-target parsing for every accepted form.
- Error classification across all three of Google's disagreeing channels.
- Event normalization for every documented `snippet.type`, plus unknown types.
- Quota ledger arithmetic: charging, rollover, DST, per-credential isolation,
  unsafe-key refusal.
- Poll pacing: server-floor inviolability, budget floor, jitter bounds, backoff
  caps and decay, page-token retention, dedupe, the offline grace window.
- Width-aware wrapping and grapheme-safe reveal.
- Key bindings, including a coverage test that fails when a handled `ctrl` key is
  absent from the keymap table.
- Moderation: confirmation before action, `esc` cancelling, an unrelated key
  disarming and still doing its own job, the modal duration prompt, optimistic
  redaction keeping removed words out of both the row list and `View()`, rollback
  restoring the exact text without reprinting it, every disabled state producing
  an explanation and never reaching the transport, and the four role values.
- Reconnect: an in-place restart resuming on the retained page token with no
  second resolution call and no duplicate row, a rejected token re-priming after
  a 400 or 404, and a 404 with no token still closing the chat.
- Mid-session 401: refresh-once semantics, single-flight across eight concurrent
  callers (barrier-synchronised, no sleeps), body resend after a refresh, and a
  walk of `Error()`, `%v`, `%+v`, `%#v`, and the whole unwrap chain for every
  credential shape.
- Ledger retention boundaries, and a prune that completes on an unreadable
  directory and on an already-cancelled caller context.
- Resize and focus layout behavior, and frame rectangularity.

Integration coverage includes a fake chat client feeding the Bubble Tea model,
fake sends with success/failure/cancellation/rate-limit responses, and connection
state transitions.

Golden coverage includes narrow and wide frames with chat, paid messages,
memberships, notices, deletions, and partial reveal frames.

**No wall-clock sleeps.** The poller takes injectable `Now` and `Sleep`; the
ledger takes an injectable `Now`; the animation clock is driven by explicit
messages. A test that sleeps is a test that flakes.

Real files are used only where filesystem permissions, symlink rejection,
no-follow opens, or atomic replacement are themselves under test. Temp
directories stay isolated and credential-free.

## Manual Verification

Some things only a human at a terminal can confirm. Record them in
[manual-validation.md](manual-validation.md), and mark a check **skipped with the
environment reason** rather than implying it passed:

- `yc chat --mock` in a real PTY at 80×24, a wide size, and a narrow size, plus a
  live resize.
- The splash animation and `animation_mode = "off"`.
- The theme picker's live preview and OSC 11/111 background restore on exit.
- A real live chat, a sent message, a Super Chat, and an inbound moderation event.
- An **outbound** moderation action: a delete, a timeout, and a ban against a
  chat the account actually moderates, including one deliberate failure to see
  the rollback restore the rows.
- A mid-session credential expiry, to see the 401 recovery rather than a dead
  session.
- The quota meter over a multi-hour session, compared against the Cloud Console.
  This is the measurement that would settle whether `quota_cost_list = 5` is
  right.

## Quality Gates Before Handoff

```sh
go fmt ./...
go vet ./...
go test ./...
```

When relevant:

```sh
go test -race ./...
go tool govulncheck ./...
go tool staticcheck ./...
```

Also check:

- No secret leakage on any surface.
- No blocking I/O in `View`.
- No raw byte or rune slicing of user-visible Unicode.
- Every network and disk call takes a `context.Context`.
- The quota ledger is charged for every dispatched call, failures included.
- `pollingIntervalMillis` is still an absolute floor.
- Avatars, badges, and emoji stay text-only.
- Docs match actual CLI behavior, and no doc claims a credentialed path was
  verified.

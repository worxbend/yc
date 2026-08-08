# Security Model

The threat model `yc` was built against, and the guarantees the code actually
makes. This document is the *engineering* side.

**For policy — the supported scope, how to report a vulnerability, and what to do
first if you leak a credential — read [../SECURITY.md](../SECURITY.md).** It is
not duplicated here.

Related: [auth.md](auth.md) for the credential model, [config.md](config.md) for
redaction in configuration output, [quota.md](quota.md) for why spend is a
security concern.

## What `yc` Is Protecting

| Asset | Where it lives |
| --- | --- |
| OAuth access token | credential file, or environment/config |
| OAuth refresh token | credential file, or environment/config |
| OAuth client secret (optional) | environment/config |
| YouTube API key | credential file, or environment/config |
| Authorization code, OAuth `state`, PKCE verifier and challenge | in memory, during login only |
| The authorization URL | in memory, during login only |
| The daily quota allowance | Google's side, spent by `yc` |
| Private chat content | on screen, and in an opt-in debug log |

Two of those are unusual enough to be worth calling out, and
[../SECURITY.md](../SECURITY.md) opens with them:

- **The API key travels in a query string.** A YouTube Data API request URL is
  therefore itself a credential — and `net/http` errors echo the URL.
- **The PKCE verifier and the OAuth `state` are single-use secrets during login.**
  Logging either defeats the flow's protection against interception and replay.

## The Threat Model

`yc` assumes the machine and the user's shell are trusted. It does **not** assume
the terminal is private.

### 1. The terminal is on stream

This is the defining assumption, and the one that shapes the most code. A person
running a YouTube chat client is disproportionately likely to be broadcasting the
window it is in.

Consequences that are visible throughout the codebase:

- No credential value is ever drawn, at any width, in any state.
- **A deleted message is never reprinted** — not struck through, not in the
  inspect panel, not in a status line, not in a moderation confirmation prompt.
  A confirmation names the person and the action, never the text.
- A failed moderation action says `(nothing was removed)` rather than quoting
  what it tried to remove.
- `yc config show` prints `[redacted]` for a set secret and `""` for an unset one
  — no prefixes, no suffixes, no lengths, so a screenshot of a doctor run is
  shareable but "configured" is still distinguishable from "not configured".

### 2. Diagnostics get shared

Debug logs, `yc doctor` output, and error messages are pasted into issues and
chat rooms. They are treated as public by construction, which is why redaction
lives at the *type* level rather than at each call site.

### 3. `net/http` will leak the URL for you

Any error from the standard library carries the request URL, and `yc`'s API key
is in that URL. Anything that surfaces a transport error must therefore reduce it
to its class rather than pass it through. This is not a hypothetical: it is the
single most likely leak path in the whole program.

### 4. The filesystem is shared

A credential file that another account can read is a compromised credential. `yc`
refuses to use a credential file or a debug log whose permissions are wrong,
rather than repairing them — silently fixing a mode hides the fact that something
already read it.

### 5. Quota is a denial-of-service target

The allowance is finite, non-replenishing until the Pacific reset, and **per
project rather than per machine**. A bug that spends it is a DoS against the
user's own project, so [../SECURITY.md](../SECURITY.md) classifies runaway
polling as security-relevant rather than merely incorrect.

### Explicitly out of the model

`yc` does **not** defend against:

- An attacker who can read your process memory or attach a debugger.
- An attacker who already has your config file, credential file, or shell
  environment.
- Terminal scrollback and terminal-emulator logging.
- A hostile terminal emulator.
- A malicious Go module in the dependency tree beyond what `govulncheck` and a
  deliberately tiny dependency set provide.
- Anything about YouTube's own access control. What a credential is *allowed* to
  read is Google's decision; `yc` neither widens nor narrows it.

## Trust Boundaries

```text
  environment / flags / config.toml        credentials.json (0600 in 0700)
              |                                      |
              v                                      v
        internal/config  --------------------> internal/cli
        (Secret-typed fields,                 (credentialHolder: one live
         redacted display)                     credential set, single-flight
              |                                refresh, persists before swap)
              |                                       |
              |                                       v
              |                              internal/youtube
              |                              - token read at REQUEST time
              |                              - APIError built from CLASSIFIED
              |                                fields, never from raw transport
              |                                text
              |                              - every URL scrubbed from errors
              |                                       |
              v                                       v
        internal/debuglog  <---- curated fields ---- everything
        (auth.Redactor over every string attribute,
         0600 file in a 0700 dir, O_NOFOLLOW on unix)
                                     |
                                     v
                              internal/app
                              (renders only display-safe text; View does
                               no I/O and holds no credential)
```

A credential crosses **into** `internal/youtube` and never comes back out. The
app layer has no field that can hold one.

## The Redaction Guarantees

### `auth.Secret` — redaction by type, not by discipline

Every credential value is an `auth.Secret`, whose default formatting is redacted:

| Path | Result |
| --- | --- |
| `fmt.Sprintf("%v", s)` / `%s` | `<redacted>` |
| `fmt.Sprintf("%#v", s)` | `auth.Secret(<redacted>)` |
| `json.Marshal(s)` | `"<redacted>"` |
| `encoding.TextMarshaler` | `<redacted>` |
| `s.Reveal()` | the real value — the **only** way to get it |

`Reveal` is called deliberately, in a handful of places: signing an API request,
the OAuth token exchange, the tokeninfo call, the credential store's own marshal
path, and test assertions. Everywhere else the value cannot escape by accident,
because there is no accidental path.

`auth.Redactor` — which by construction holds *every* secret in the process —
refuses to format itself for the same reason: without its own `String` method,
one `%v` on a redactor, or on any struct that has one as a field, would print the
client secret, both tokens, and the API key in a single line.

### `auth.Redactor` — redaction by shape, for text `Secret` never touched

A second layer catches credential-shaped text that arrived as a plain string from
somewhere else — a Google error body, an OAuth library message, a URL.

| Pattern | Catches |
| --- | --- |
| explicit secrets | every configured `Secret`, longest first, so a longer secret cannot be partially masked by a shorter prefix of itself |
| `bearer <value>` | an `Authorization` header value that reached a string |
| `access_token=`, `refresh_token=`, `id_token=`, `client_secret=`, `api_key=`, `authorization_code=`, `code_verifier=`, `code_challenge=` (`=` or `:`) | every unambiguous credential parameter in the installed-app flow |
| `state=` / `code=` | matched by **separator**, not bare |
| `AIza[0-9A-Za-z_-]{35}` | a bare Google API key by its own shape, even with no label |

The `state` / `code` handling is the subtle one and it is worth understanding
before changing it. Matched bare, those two names destroyed real diagnostics:
`connection state: connected`, `error code: 403`, and `country code: US` all
became `<redacted>`, on every debug attribute and every user-facing error. They
are therefore split by separator:

- `=` is only ever an assignment — a query string, a form body, a redirect URI.
  English does not write `state=`, so any prefix is accepted.
- `:` is ambiguous, being both a JSON key separator and how English writes
  `error code: 403`. So the name must sit in a **parameter position**: at the
  start, or after `?`, `&`, `{`, or `,`. A bare `code: <value>` in prose is
  deliberately left alone — `Secret` is the primary defence, and an authorization
  code does not reach a string that way.

Over-redaction is tested for, not just under-redaction.

### `APIError` is assembled, never quoted

`youtube.APIError` is built from **classified fields** — status code, legacy
`reason`, canonical `status`, `ErrorInfo` reason, a bounded detail — rather than
from anything the transport saw. There is no path by which a request URL reaches
an error message.

Three defences stack on the detail text:

1. `stripRequestURL` unwraps `*url.Error` structurally, keeping only the
   operation (`Get`, `Post`) because that is the only part worth having.
2. `absoluteURLPattern` replaces **anything shaped like a URL** wholesale. A
   structural strip cannot remove a URL that another package interpolated into
   its own message — and the OAuth token exchange produces exactly that.
3. An `auth.Redactor` holding the live token, the API key, and any
   caller-supplied secrets runs last, so a value nothing else knew about is still
   caught by shape.

The response body read for error detail is bounded (8 KiB) and the decoded
message is truncated, so a hostile or broken endpoint cannot make `yc` buffer or
print an unbounded string.

`ProbeYouTubeReachability` reduces a transport error to its class for the same
reason, which is why `yc doctor` can report "the network path exists" without
ever printing what it dialled.

### `internal/debuglog` — curated fields only

Debug logging is **opt-in** (`debug_logging`, `YC_DEBUG_LOG`, or `--debug-log` on
`chat`, `login`, and `doctor`). `--debug-log=false` explicitly disables it even
when the config or environment enables it, because the flag tracks "absent"
separately from "false".

- Records are JSON lines of curated fields. Auth records carry phase names, scope
  **counts**, identity names, refresh availability, status, and sanitized errors
  — never a URL, a code, a state value, or a token.
- Every string attribute passes through the logger's `auth.Redactor`, and URLs
  found in free text are reduced further.
- Callers must not dump raw structs or response bodies. This is a
  [code-style.md](code-style.md) rule with a review search behind it.

### The one rule with no exception

> Never let a token, refresh token, client secret, API key, OAuth code or state,
> PKCE verifier, or authorization URL reach any output, error, log, or doc.

Tests enforce it by scanning output for obviously fake markers such as
`test-not-a-real-token` and `AIzaSyTEST-not-a-real-key-000000000000000`. A leak
shows up as a failing test rather than as a review comment.

## Filesystem Hardening

Both hardened paths follow the same rule: **reject rather than repair**.

| | Credential file | Debug log |
| --- | --- | --- |
| Path | `$XDG_CONFIG_HOME/yc/credentials.json` | `debug_log_path`, else `<cache>/yc/debug.log` |
| Directory mode | exactly `0700` | `0700` |
| File mode | exactly `0600` | `0600` |
| Symlink at either path | rejected | rejected |
| Existing file that is a directory | rejected | rejected |
| Group- or other-accessible existing file | rejected | rejected |
| Special mode bits | rejected | — |
| Opening an existing file | `O_NOFOLLOW`, descriptor validated | `O_NOFOLLOW`, descriptor validated |
| Writes | temp file + same-directory rename | append |

The credential file is **Unix-only**. Non-Unix builds have no saved-credential
backend and `yc login` refuses *before* starting OAuth, so a token is never
obtained without a place to put it.

`yc doctor` reports which guarantee the running build actually makes, rather than
the documentation asserting it on the build's behalf.

## Credential Handling At Runtime

- **One holder, one ledger, one REST client.** Every API client reads the token
  through the holder at *request* time rather than capturing it at construction,
  which is what makes a mid-session refresh reach every feature at once.
- **Refresh is single-flight.** Concurrent callers share one exchange; a 401
  whose epoch is stale (another request's refresh already landed) retries without
  exchanging at all.
- **A rotated refresh token is persisted before the new access token becomes
  visible.** If the write failed after the in-memory swap, the next process start
  would read a refresh token Google had already rotated away.
- **A mid-session 401 retries exactly once.** The retry is a separate statement
  rather than a loop, so "exactly once" is structural — there is no counter to
  get wrong. A second rejection is terminal and says to run `yc login`.
- **A refresh failure drops the error's identity.** Only text that has been
  through the URL strip, the URL pattern scrub, and a redactor holding the stale
  token, the new token, and the API key survives into the message.
- Only HTTP **401 with a token present** is retried. 403, 429, 5xx, and a
  key-only 401 are untouched and belong to the poller's backoff ladder.

## Migration Is Explicit Only

`yc` never silently copies secrets from the environment or the flat config file
into credential storage during setup or config loading. `yc login` saves after a
successful user-authorized login; refresh saves only newly rotated tokens.
`yc setup`, `yc profile set`, and `yc login --write-default-config` never create
or update a secret key — and that exclusion comes from the struct tags
themselves, so a newly added secret is excluded by **declaring** it rather than by
someone remembering to.

`yc logout` deletes the saved credential and asks Google to revoke it, so the
token stops working immediately rather than at its next expiry. Credentials
supplied through the environment or the config file are **not** touched: `yc` did
not write them and will not remove them.

## Quota As A Security Property

Treat these as security-relevant, not merely as correctness bugs:

- A retry loop against `quotaExceeded` or `rateLimitExceeded`.
- Any path that polls faster than `pollingIntervalMillis`.
- A resolution path that reaches `search.list` without explicit consent.
- A background or daemonized instance polling without the user's knowledge.
- A ledger that under-reports spend and hides any of the above.

`yc` charges the ledger for **every dispatched call, failures included**, because
a failed call costs units too and a meter that only counts successes
under-reports exactly when it matters most.

## Verifying The Guarantees

The credential-leak smoke — the one check worth running by hand after touching
auth, transport, or logging:

```sh
tmp=$(mktemp -d)
env XDG_CONFIG_HOME="$tmp/config" XDG_CACHE_HOME="$tmp/cache" \
  YC_YOUTUBE_API_KEY='AIzaSyTEST-not-a-real-key-000000000000000' \
  go run ./cmd/yc chat --mock --debug-log --debug-log-path "$tmp/debug.log" \
  > "$tmp/out" 2> "$tmp/err"
grep -rn 'AIzaSyTEST' "$tmp" && echo LEAK && exit 1
stat -c '%a' "$tmp/debug.log"     # expect 600
```

The automated suites:

```sh
go test ./internal/auth -run 'Secret|Redact' -v
go test ./internal/youtube -run 'Redaction|Refresh.*Credential' -v
go test ./internal/cli -run Redaction -v
go test ./internal/storage -v
go tool govulncheck ./...
```

The three surfaces that most often leak, and the searches that find them:

```sh
# a raw struct in a debug record instead of curated fields
rg 'slog\.Any|%\+?v' internal --glob '!**/*_test.go' | rg -i 'token|secret|key|cred'

# a transport error passed through instead of classified
rg 'err\.Error\(\)' internal/youtube --glob '!**/*_test.go'

# View doing I/O, which is also how a credential would reach a frame
rg 'os\.Open|http\.|ReadFile|WriteFile|time\.Sleep' internal/app --glob '!**/*_test.go'
```

## Status

| Aspect | Label |
| --- | --- |
| `auth.Secret` formatting, `auth.Redactor` patterns, over- and under-redaction | **Ready** — covered by tests that scan for fake markers |
| `APIError` construction and URL scrubbing | **Ready** — covered against `httptest` |
| Debug-log hardening, credential-file modes, symlink and no-follow behavior | **Ready** — covered with real files, since permissions are what is under test |
| Single-flight refresh, 401-retry-once, persist-before-swap | **Credentialed** — implemented and unit-tested against fakes, **never run against Google** |
| Non-Unix credential storage | **Out of scope** — must keep returning a redacted unsupported-platform error |

No credentialed YouTube path has ever been exercised; see
[manual-validation.md](manual-validation.md).

To report a suspected leak, follow [../SECURITY.md](../SECURITY.md) — rotate
first, reproduce with fake values, and never attach a real credential to an
issue.

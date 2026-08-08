# Authentication

This document describes the authentication model for `yc`: the two credential
modes, the Google installed-app OAuth flow, token refresh, the Unix credential
file, and the redaction rules that hold all of it together.

To create the Google Cloud project and get a key or a client ID, read
[register-google-app.md](register-google-app.md). For config precedence, read
[config.md](config.md); for symptoms and fixes, read
[troubleshooting.md](troubleshooting.md).

> **Status.** Everything below marked **Credentialed** is implemented and
> unit-tested against fakes and `httptest`, and has **never been run against
> Google**. `--dry-run`, `yc doctor`, `yc config show`, and every redaction rule
> are covered by credential-free tests and smokes. See
> [manual-validation.md](manual-validation.md).

## Two Modes, Both First Class

| | API key | OAuth |
| --- | --- | --- |
| Setup cost | one console click, no consent screen | consent screen + Desktop client + browser login |
| Read a **public** live chat | ✅ | ✅ |
| Read a members-only / private chat | ❌ | ✅ if the account can |
| Send a message | ❌ | ✅ with `youtube.force-ssl` |
| Delete / timeout / ban | ❌ | ✅ with `youtube.force-ssl`, if the account owns or moderates that chat |
| Resolve your own channel | ❌ | ✅ |
| Read your subscriptions | ❌ | ✅ |

An API key is an accepted caller identity for `liveChatMessages.list` on a public
live chat, and for nothing else `yc` does. It is not a fallback — it is the
zero-friction first-run path, and `yc` treats it as a mode rather than as a
partial failure.

`yc` **degrades by capability, not by hiding controls.** A composer you cannot
send from stays visible with the reason attached:

```text
API-key mode is read-only; run `yc login` to send messages
missing OAuth scope https://www.googleapis.com/auth/youtube.force-ssl;
  run `yc login` again and approve it
```

The moderation keys follow the same rule: `d`, `t`, and `b` stay bound and stay
in the help overlay even when the credential cannot use them, and answer with the
specific reason — see [moderation.md](moderation.md).

A disabled control that explains itself teaches the user what to fix. A missing
one does not.

## Scopes

| Scope | Enables |
| --- | --- |
| `https://www.googleapis.com/auth/youtube.readonly` | read live chat, broadcast metadata, your own channel, your subscriptions |
| `https://www.googleapis.com/auth/youtube.force-ssl` | send chat messages, delete messages, manage live chat bans |

`yc login` requests both. `yc login --read-only` requests only the first.

`yc` never requests the broad `https://www.googleapis.com/auth/youtube` scope —
`force-ssl` is the narrowest scope covering every write `yc` performs — but it
**honors** it when Google reports it as granted for a credential minted
elsewhere. Subsumption rules:

- `force-ssl` and the broad `youtube` scope both satisfy a `readonly` requirement.
- The broad `youtube` scope satisfies a `force-ssl` requirement.

### When scopes are known

Scopes are trustworthy only when they came from the credential file `yc` wrote at
login. A token pasted into the environment or the config file carries no record
of its grant, so `yc` reports the scopes as **unknown** and is optimistic:
send and moderation controls stay enabled and the API is the authority. `yc
doctor` says so explicitly rather than pretending to know.

> **Known gap.** `youtube.Identity.Scopes` is declared as the granted scope list
> and nothing in `internal/youtube` currently populates it. A live session
> therefore takes the "scopes unknown" branch even when the credential file
> recorded them, which means the moderation keys disclose uncertainty
> (`granted scopes unknown; YouTube decides`) rather than refusing precisely.
> Optimism is the safe direction here — the API refuses in a redacted, actionable
> sentence — but the disabled state would be sharper if `Client.Identity`
> populated `Scopes` from the credential source.

## Credential Sources And Precedence

Highest priority first:

1. CLI flags (`--config`, `--chat`/`--chats`/`--video`/`--channel`,
   `--live-chat-id`, `--debug-log`, `--debug-log-path`, and the per-run display
   and quota flags).
2. Environment variables.
3. The flat config file.
4. Saved credentials, filling only fields still empty.
5. Built-in defaults.

Saved credentials sit at the bottom **on purpose**. An operator who exports a
token for one run must be able to do so without deleting their saved login; the
reverse — a stale saved token quietly overriding an explicit export — is the
failure that is hard to diagnose.

When both exist, `yc` reports the saved credential as *shadowed*:

```text
Saved login credentials are present but were not used because an environment or
config token takes precedence; unset YC_GOOGLE_ACCESS_TOKEN/GOOGLE_ACCESS_TOKEN
or remove google_access_token from config.toml, then retry.
```

`yc doctor` shows the same state as a warning line rather than as an "ok".

## The Login Flow

**Status: credentialed.**

`yc login` runs Google's installed-app (native) authorization-code flow:

```text
  yc                                   Google
   |                                     |
   |-- bind 127.0.0.1:<ephemeral>        |
   |-- generate code_verifier (PKCE)     |
   |-- open browser to                   |
   |   accounts.google.com/o/oauth2/v2/auth
   |     ?client_id=… &redirect_uri=http://127.0.0.1:<port>/
   |     &response_type=code &scope=… &state=…
   |     &code_challenge=… &code_challenge_method=S256
   |     &access_type=offline                |
   |                                     |
   |<-- browser redirect: code + state --|
   |-- verify state (single use)         |
   |-- POST oauth2.googleapis.com/token -|
   |     code + code_verifier + client_id (+ secret if configured)
   |<-- access_token, refresh_token, expiry, scope
   |-- GET oauth2.googleapis.com/tokeninfo  (validate + read granted scopes)
   |-- GET youtube/v3/channels?mine=true    (resolve identity, 1 unit est.)
   |-- save through the credential store
```

Endpoints used:

| Purpose | URL |
| --- | --- |
| Authorization | `https://accounts.google.com/o/oauth2/v2/auth` |
| Token exchange and refresh | `https://oauth2.googleapis.com/token` |
| Token introspection | `https://oauth2.googleapis.com/tokeninfo` |
| Revocation | `https://oauth2.googleapis.com/revoke` |
| Identity | `https://www.googleapis.com/youtube/v3/channels` |

Flags:

```sh
yc login                        # full flow
yc login --read-only            # readonly scope only
yc login --dry-run              # explains everything; no network, no listener, no browser
yc login --write-default-config # write a starter config.toml first, only if none exists
yc login --timeout 2m           # bound the browser round trip (default 5m)
yc login --redirect-uri http://127.0.0.1:17643/   # pin a loopback address and port
```

Desktop OAuth clients accept loopback redirects on any port, so there is normally
nothing to register and `--redirect-uri` is for the unusual case where you must
pin one. An empty value means "bind `127.0.0.1` on an ephemeral port", which is
what the installed-app flow wants. It can also be set persistently as
`google_redirect_url` / `YC_GOOGLE_REDIRECT_URL`; an explicit flag still wins.

The flow rejects, with redacted actionable errors: missing, mismatched, expired,
or reused OAuth state; a denied authorization; an unsupported callback listener;
an unsupported browser launch; token and validation errors; missing required
scopes; a client mismatch; context cancellation; and the bounded timeout.

### The client secret is optional

An installed app cannot keep a secret, which is why the flow uses PKCE S256.
`yc` authenticates with the code verifier and does not need one. Set
`YC_GOOGLE_CLIENT_SECRET` only if your OAuth client was issued a secret *and*
Google rejects your refresh without it.

This is the opposite of `twi`, where the client secret is mandatory for refresh
and its absence is a documented four-hour cliff.

## Logout

```sh
yc logout               # delete saved credentials, then ask Google to revoke
yc logout --keep-remote # delete locally only, leave the grant in place
```

Revocation posts to `https://oauth2.googleapis.com/revoke` so the token stops
working immediately rather than at its next expiry.

Credentials supplied through environment variables or the config file are **not**
touched: `yc` did not write them and will not remove them.

## Token Refresh

**Status: credentialed.**

One `credentialHolder` owns the live credential set, and every API client reads
the token through it at request time rather than capturing it at construction.
That is what makes a mid-session refresh reach every feature at once.

- A background loop refreshes **5 minutes before expiry**, so the token is
  replaced during a quiet moment rather than in the middle of a poll.
- Refresh is **single-flight**: concurrent callers share one exchange.
- The rotated refresh token is **persisted before** the new access token becomes
  visible. If the write failed after the in-memory swap, the next process start
  would read a refresh token Google had already rotated away.
- `yc chat` also validates the access token at startup when Google is reachable.
  A rejected token triggers one refresh attempt before sending the user back to
  the browser; an unreachable validator prints a warning and continues to the
  API, so a validation outage does not block a chat the API would still serve.

### A mid-session 401 recovers

A 401 arriving mid-poll is recovered in the transport rather than ending the
session. `youtube.ClientConfig.OnAuthFailure` is auto-wired to
`credentialHolder.RefreshCredentials` — `NewClient` sets it from the credential
source whenever that source implements the new `youtube.CredentialRefresher`
interface, so no call site had to change.

- The token is exchanged **once**, under a client-wide single-flight, and the
  request is retried **exactly once**. The retry is a separate statement rather
  than a loop, so the bound is structural: there is no counter to get wrong.
- The retry re-signs from scratch — new URL, new `Authorization`, a fresh
  per-attempt timeout — and re-reads the marshalled body, so `POST` bodies
  survive.
- An epoch is sampled **before** dispatch. A 401 whose epoch is already stale
  (another request's refresh landed while this one was on the wire) retries
  without exchanging at all; concurrent same-epoch 401s join one exchange.
- Only **HTTP 401 with a token present** is retried. 403, 429, 5xx, and a
  key-only 401 are untouched and stay with the poller's backoff ladder — a
  refresh cannot fix any of them.
- A second rejection is terminal and actionable:

  ```text
  videos.list: the sign-in expired and could not be renewed; run `yc login`
  to sign in again (<scrubbed reason>)
  ```

  It unwraps to `ErrAuthFailed`, and the refresh error's *identity* is dropped:
  only text that has been through the URL strip, the absolute-URL scrub, and a
  redactor holding the stale token, the new token, and the API key survives.

A token that was renewed but could not be **written** returns success — the
pending request is retried — and reports the write failure through the holder's
error reporter. A session that works is better than a session that dies over a
cache write.

The proactive background refresh above still exists and still does the work in
the normal case; this is the net beneath it.

Access tokens issued to a project in *Testing* status carry refresh tokens that
**expire after 7 days**. Publish the OAuth consent screen, or re-run `yc login`.

## Credential Storage

**Status: partial — Unix only.**

`internal/storage` owns the credential boundary:

- `CredentialStore` — load, save, delete.
- `CredentialRecord` — the storage-owned DTO. Token values are `auth.Secret`, so
  ordinary formatting and ordinary JSON encoding stay redacted; only the
  storage-owned marshal path deliberately reveals them.
- `MemoryCredentialStore` and `FailingCredentialStore` — test fakes.
- `CredentialFileStore` — the restrictive Unix-only implementation.

Path:

```text
$XDG_CONFIG_HOME/yc/credentials.json
~/.config/yc/credentials.json
```

This is **not** the flat `config.toml`. On Unix builds the implementation:

- creates the credential directory with exactly `0700`;
- creates the credential file with exactly `0600`;
- writes through a temporary file and same-directory rename;
- rejects a symlink at the directory or the file path;
- opens an existing credential file with a no-follow open;
- **rejects** an existing file or directory whose permissions do not match those
  exact modes, rather than reusing it;
- reports every failure with a redacted error.

Non-Unix builds have no saved-credential backend. `yc chat`, `yc config show`,
and `yc doctor` still work there with environment variables and the flat config
file; `yc login` reports a redacted actionable error **before** starting OAuth,
so a token is never obtained without a place to put it.

### On-disk format

Versioned. Version 1 records Google OAuth material, the API key, and safe
identity metadata:

```json
{
  "version": 1,
  "google": {
    "client_id": "test-not-a-real-client-id.apps.googleusercontent.com",
    "access_token": "<stored access token>",
    "refresh_token": "<stored refresh token>",
    "api_key": "<stored api key>",
    "token_type": "Bearer",
    "expires_at": "2026-08-08T13:00:00Z",
    "scopes": [
      "https://www.googleapis.com/auth/youtube.readonly",
      "https://www.googleapis.com/auth/youtube.force-ssl"
    ],
    "channel_id": "UCtest000000000000000000",
    "display_name": "Test Channel",
    "updated_at": "2026-08-08T12:00:00Z"
  }
}
```

`channel_id` and `display_name` are cached so startup does not have to spend a
`channels.list` unit before the UI appears. A file with an unrecognized version
is refused with `ErrCredentialsUnsupportedVersion` rather than misparsed.

### Migration is explicit only

`yc` never silently copies secrets from the environment or the flat config file
into credential storage during setup or config loading. `yc login` saves after a
successful user-authorized login; refresh saves only newly rotated tokens.
Remove duplicates from shell profiles, `.env`, or `config.toml` if you no longer
want those sources to take precedence.

## What `yc doctor` Reports

Without printing any credential value:

- Whether the credential store loaded, is missing, failed, or is shadowed, and
  where it lives.
- The effective credential mode (`none` / `api-key` / `oauth`) and what it can do.
- Granted scopes, when they are known.
- Whether a refresh token is present — and a warning when an access token exists
  without one, because that session stops when the token expires.
- Your resolved channel and subscriber count, when a user token is configured.
- Whether the YouTube API answered at all, using an unauthenticated `HEAD` that
  spends no quota. Any HTTP response counts as reachable, including the 403 an
  unauthenticated caller gets: the question is whether the network path exists,
  not whether the credential works, and telling those apart is doctor's whole job.

The identity lookup is wired **only** when a user token exists. Probing it with
an API key would report a credential failure that is really a mode.

## Secret Redaction Rules

These are non-negotiable and are enforced by tests that scan for fake markers
such as `test-not-a-real-token`.

Never logged, printed, or error-wrapped:

- access tokens, refresh tokens, client secrets, API keys;
- OAuth authorization codes, OAuth state, PKCE verifiers and challenges;
- authorization URLs;
- `Authorization:` header values;
- credential file contents;
- request URLs and query strings from the YouTube transport — the API key lives
  in a query string, and `net/http` errors echo the URL, which is why
  `ProbeYouTubeReachability` reduces a transport error to its class and
  `APIError` is assembled from classified fields rather than from anything the
  transport saw.

Redacted from:

- `yc config show` — a non-empty secret prints as `[redacted]`, an unset one
  prints as `""`, so "not configured" is distinguishable from "configured but
  hidden". No prefixes, no suffixes, no lengths.
- `yc doctor` and `yc quota`.
- Debug logs, which use curated fields rather than raw structs.
- Error messages and status-line details.
- The message inspect panel.
- Test snapshots and golden frames.

Debug logging is opt-in through `debug_logging = true`, `YC_DEBUG_LOG=true`, or
`--debug-log` on `chat`, `login`, and `doctor`. Auth debug records carry phase
names, scope counts, identity names, refresh availability, status, and sanitized
errors — never a URL, a code, a state value, or a token.

Review a debug log before sharing it anyway: it can still contain video IDs,
live chat IDs, channel names, display names, hostnames, and timing.

## Troubleshooting Targets

User-facing errors distinguish:

- no credentials at all;
- an API key where an OAuth token is required;
- an invalid or expired token;
- a missing `force-ssl` scope;
- a disabled YouTube Data API on the project;
- an exhausted daily quota versus a rate limit;
- chat ended, chat disabled, or chat not found;
- an account with no YouTube channel, which cannot send;
- a network failure versus a credential failure.

Each stays specific enough to fix the problem while never exposing a credential.

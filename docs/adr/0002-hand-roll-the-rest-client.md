# 0002: Hand-Roll The REST And OAuth Clients

## Status

Accepted.

## Context

Two obvious dependencies present themselves for a YouTube client in Go:

- `google.golang.org/api/youtube/v3`, the generated client for the whole Data
  API.
- `golang.org/x/oauth2` and `golang.org/x/oauth2/google`, the standard OAuth
  plumbing.

Both are well maintained and both are the default answer. `yc` uses neither.

The generated YouTube client is enormous — it covers every resource in the Data
API, of which `yc` calls eight methods — and it hands the caller Google's own
struct types. Letting those into the codebase means `internal/app` and
`internal/render` can accidentally learn the wire format, which is precisely the
boundary [0003](0003-normalize-live-chat-events-before-rendering.md) exists to
protect. It also owns its own error shapes, which is a problem because error
classification is load-bearing here: Google returns three parallel, disagreeing
classification channels, and `yc` needs to read all three to distinguish
`quotaExceeded` (stop forever) from `rateLimitExceeded` (back off) from
`liveChatEnded` (clean terminal state).

`golang.org/x/oauth2` is a better fit but carries a specific liability for this
project: it holds tokens as plain strings in exported struct fields, and its
errors and transports have no notion of redaction. `yc`'s hardest requirement is
that a token, refresh token, client secret, API key, authorization code, OAuth
state, PKCE verifier, or authorization URL must never reach a log line, an error
string, or a printed value. Enforcing that through a dependency whose types are
plain strings means auditing every path a value can take out of it.

The API key adds a third consideration. It travels in a **query string**, which
means the request URL itself is a credential — and `net/http` errors echo the
URL.

## Decision

Hand-roll both.

**Transport** (`internal/youtube`): one `Client` over `net/http` with a per-call
path, query map, and body. It:

- Reads credentials through a `CredentialSource` interface at request time rather
  than capturing them at construction, so a mid-session refresh reaches every
  caller.
- Charges the quota ledger for every dispatched call, including failures.
- Decodes Google's error envelope and classifies it from all three channels —
  the legacy `error.errors[].reason`, the canonical `error.status`, and the
  `google.rpc.ErrorInfo` reason — onto one sentinel set, degrading an
  unrecognized combination into a sane retry policy rather than an unhandled
  state.
- Builds `APIError` from classified fields only. There is no path by which a
  request URL, a query string, or an `Authorization` header can reach an error
  message, because the error is never constructed from anything the transport
  saw.

**OAuth** (`internal/auth`): the installed-app authorization-code flow with a
loopback listener, PKCE S256, and `access_type=offline`, plus token exchange,
refresh, `tokeninfo` validation, and revocation. Every credential is an
`auth.Secret` whose `String`, `Format`, and `MarshalJSON` render a placeholder;
revealing it requires an explicit call, permitted at exactly two boundaries — the
HTTP request that sends it to Google, and the storage-owned marshal path that
writes the credential file.

## Consequences

- The module's direct dependency set is Bubble Tea, Lip Gloss,
  `charmbracelet/x/ansi`, `muesli/termenv`, `rivo/uniseg`, and `golang.org/x/term`.
  Nothing Google-specific.
- Redaction is a property of the type system rather than a convention: a token
  that reaches a `%v` renders as a placeholder by construction.
- Error classification is exactly as precise as the quota logic needs, because it
  was written for that purpose.
- Every call is testable against `httptest` with no network and no credential.
- The cost is real: `yc` owns the wire format. A YouTube response shape change is
  a code change here, not a dependency bump. That cost is bounded — eight methods,
  all inside one package — and is the same cost `internal/youtube` was going to
  pay anyway for normalization.
- Refresh-token rotation, PKCE, and state validation are ours to get right. They
  are covered by unit tests against `httptest`, and are the highest-risk code in
  the repository until a real login is run.

## Verification

- `httptest`-backed tests for every endpoint, both success and each error class.
- Table tests over the three classification channels, including a case where they
  disagree and the most specific one must win.
- Redaction tests that format, print, JSON-encode, and error-wrap fake secret
  markers and assert none appear.
- A test that a transport error from an unreachable host does not carry the
  request URL.
- OAuth tests for state mismatch, state reuse, denial, expiry, missing scope,
  client mismatch, cancellation, and timeout.
- `go mod tidy` must not reintroduce `golang.org/x/oauth2` or
  `charmbracelet/bubbles`.

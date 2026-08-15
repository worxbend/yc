# Contributing to yc

Thanks for helping make `yc` a sharp terminal YouTube chat client. This project
values small verifiable changes, plain Go, careful secret handling, careful quota
handling, and documentation that says exactly what the app can do today.

## Support Boundary

`yc` targets Unix-like terminals and Docker. Saved credentials are supported only
on Go `unix` builds through the restrictive credential-file store. Windows is not
a supported target, and non-Unix saved credentials must keep failing closed.

## First Local Run

Start from a clean checkout and keep credentials out of the environment while
running repository checks:

```sh
export GOTOOLCHAIN=auto TERM=xterm-256color
export XDG_CONFIG_HOME="$(mktemp -d)" XDG_CACHE_HOME="$(mktemp -d)"
export YC_GOOGLE_CLIENT_ID= YC_GOOGLE_CLIENT_SECRET= YC_GOOGLE_ACCESS_TOKEN=
export YC_GOOGLE_REFRESH_TOKEN= YC_YOUTUBE_API_KEY= YC_YOUTUBE_CHANNEL_ID=
export GOOGLE_CLIENT_ID= GOOGLE_CLIENT_SECRET= GOOGLE_ACCESS_TOKEN= GOOGLE_REFRESH_TOKEN=
go run ./cmd/yc chat --mock
```

The mock path is the safest place to inspect UI behavior: no Google account, no
token, no network, **and no quota**. That last one matters more here than it did
in `twi` — a careless live smoke spends a real, non-replenishing daily allowance.

## Contribution Flow

1. Read [README.md](README.md), [docs/index.md](docs/index.md),
   [docs/code-style.md](docs/code-style.md), [docs/quota.md](docs/quota.md), and
   [SECURITY.md](SECURITY.md).
2. Pick one coherent behavior, documentation, or test improvement.
3. Keep package boundaries intact. UI belongs in `internal/app`; transport, poll
   scheduling, and quota accounting belong in `internal/youtube`; OAuth belongs
   in `internal/auth`; rendering belongs in `internal/render`; secret and cache
   persistence belong in `internal/storage`.
4. Add or update tests near the changed package.
5. Update docs when behavior, setup, architecture, support boundaries, quota
   arithmetic, or verification changes.
6. Run the relevant focused checks, then the broad gate when practical.

## Default Verification

Run this before sending a PR when the change is not trivial:

```sh
go mod tidy
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go tool govulncheck ./...
go tool staticcheck ./...
golangci-lint run ./...
go build -o /tmp/yc-validation ./cmd/yc
go run ./cmd/yc --help
go run ./cmd/yc chat --mock
go run ./cmd/yc doctor
go run ./cmd/yc config show
go run ./cmd/yc quota
git diff --check
```

`golangci-lint` is the one tool in that list that is not a Go module `tool`
directive: install it separately (the v2 line, matching what CI pins):

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

Its configuration lives in `.golangci.yml` at the repository root, which
enables errcheck, revive, gocritic, gosec, misspell, and unparam on top of
vet and staticcheck. The file documents every deliberate exclusion inline.
If a finding is a false positive, silence it with a targeted
`//nolint:<linter> // reason` on the offending line — never a bare `//nolint`
and never without the reason.

Use isolated `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` directories and clear all
`YC_*` and `GOOGLE_*` variables. Do not let tests or smokes read your real local
config, credential file, or quota ledger by accident.

## Quota Rules For Contributors

These are specific to this project and are easy to violate by accident.

- **Never add a call that is not charged to the ledger.** Every dispatched
  request, including one that fails, moves the meter.
- **Never let anything poll faster than `pollingIntervalMillis`.** It is an
  absolute floor beneath configuration, jitter, and backoff alike.
- **Never retry an exhausted quota.** Every attempt is charged and the allowance
  does not return until the Pacific reset.
- **Never present a unit figure as a fact.** Google publishes none for live-chat
  methods. Anything you render carries an `est.` marker.
- **Never put `search.list` on a hot path.** It spends a scarce separate daily
  allowance and must stay behind an explicit opt-in prompt.
- **Prefer the cheapest resolution ladder.** An explicit `liveChatId` costs
  nothing.

If you run a live smoke against your own project, say so in the PR and mention
roughly what it cost. Nobody should have to guess where 3,000 units went.

## Manual Checks

Use [docs/manual-validation.md](docs/manual-validation.md) as the evidence log
for terminal and credentialed behavior that automated tests cannot prove. Record
terminal name and version, viewport size, the exact command, what you observed,
and every skipped check with its environment reason.

**No credentialed YouTube behavior may be claimed anywhere in this repository
until that document records it.** It currently records none.

## Secret Handling

Never commit, paste, log, screenshot, or record an access token, refresh token,
client secret, API key, authorization code, OAuth state, PKCE verifier,
authorization URL, credential file, private config file, or a debug log with
private context.

Use `auth.Secret` for every credential value, and reveal only at the two
deliberate boundaries — the HTTP request to Google, and the storage-owned
credential-file marshal. Use redaction helpers for every user-facing error,
diagnostic line, debug record, and fixture. Debug logs use curated fields; do not
dump raw structs, response bodies, request URLs, query strings, or HTTP headers.

Remember that the **API key travels in a query string**, so a request URL is
itself a credential — and `net/http` errors echo the URL.

Test fixtures use obvious fake markers such as `test-not-a-real-token` and
`AIzaSyTEST-not-a-real-key`, so a leak is greppable.

## Pull Request Checklist

- The change is scoped to one behavior or documentation improvement.
- Tests cover the changed behavior, or the manual evidence explains why
  automation is not possible.
- No wall-clock sleeps in tests. Use the injectable `Now`/`Sleep` hooks.
- `go fmt`, the relevant `go test`, and `git diff --check` pass.
- New docs link to related docs with relative `.md` paths.
- No trailing whitespace in any file.
- README, docs, and code comments do not overclaim Windows support, credentialed
  YouTube behavior, Docker verification, or a quota figure Google has not
  published.
- No real secret appears in a commit, log, fixture, screenshot, or copied output.

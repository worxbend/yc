<!--
Thanks for contributing to yc. Keep the change small and verifiable; the
checklist below is the same gate CI runs, so running it locally first turns a
red run into a green one.
-->

## What this changes

<!-- One paragraph. What behavior is different after this merges? -->

## Why

<!-- The problem, or the issue this closes: "Closes #123". -->

## How it was verified

<!--
Name the commands you actually ran and what you saw. "Ran the gate" is less
useful than "gate green; also checked the status bar at 62x20".
-->

## The gate

Run from a clean checkout with credentials cleared, exactly as in
[docs/development.md](https://github.com/worxbend/yc/blob/main/docs/development.md):

```sh
export GOTOOLCHAIN=auto TERM=xterm-256color
export XDG_CONFIG_HOME="$(mktemp -d)" XDG_CACHE_HOME="$(mktemp -d)"
export YC_GOOGLE_CLIENT_ID= YC_GOOGLE_CLIENT_SECRET= YC_GOOGLE_ACCESS_TOKEN=
export YC_GOOGLE_REFRESH_TOKEN= YC_GOOGLE_REDIRECT_URL=
export YC_YOUTUBE_API_KEY= YC_YOUTUBE_CHANNEL_ID= YC_DEFAULT_CHATS=
export GOOGLE_CLIENT_ID= GOOGLE_CLIENT_SECRET= GOOGLE_ACCESS_TOKEN=
export GOOGLE_REFRESH_TOKEN= GOOGLE_REDIRECT_URL=
```

- [ ] `go mod tidy` leaves `go.mod` and `go.sum` unchanged
- [ ] `gofmt -l .` prints nothing
- [ ] `go vet ./...`
- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `go tool govulncheck ./...`
- [ ] `go tool staticcheck ./...`
- [ ] Builds for `linux/amd64` and `linux/arm64`
- [ ] `yc --help`, `yc doctor`, `yc config show`, `yc quota`, `yc chat --mock`

## Boundaries and house rules

- [ ] No secret reaches output, an error, a log, or a doc — no token, refresh
      token, client secret, API key, OAuth code or state, PKCE verifier, or
      authorization URL. Tests use obvious fake markers.
- [ ] `View()` stays pure: no network, no filesystem, no sleeps.
- [ ] `internal/app` imports no YouTube Data API JSON types; `internal/youtube`
      imports nothing from Charm.
- [ ] Every network and disk call takes a `context.Context`.
- [ ] User-visible strings are handled grapheme- and width-aware
      (`rivo/uniseg`, `charmbracelet/x/ansi`) — no raw byte or rune slicing.
- [ ] No trailing whitespace in any tracked file.

## Quota

- [ ] This change adds no YouTube Data API call, **or** the added calls are
      described below with their estimated unit cost and the ledger accounts
      for them.

<!-- If it adds calls: which endpoint, how often, what it costs. -->

## Docs

- [ ] Docs match what the code now does (`README.md`, `docs/`, `CHANGELOG.md`).
- [ ] Anything unverified is marked as unverified rather than implied to pass.

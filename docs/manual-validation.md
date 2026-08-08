# Manual Validation Evidence

This file records manual evidence for environment-dependent behavior that
automated tests cannot prove. It intentionally avoids screenshots, terminal
recordings, debug-log contents, and credential values.

**The rule:** if a check is not recorded here, no document in this repository may
claim it. A check that could not be run is recorded as *skipped* with the exact
environment reason, never omitted and never implied to have passed.

## Credentialed YouTube Validation: NONE

**No part of `yc` has ever been run against Google.** Not one live
`liveChatMessages.list` call, not one `yc login`, not one send, not one identity
lookup, not one real quota unit. Every credentialed path is implemented and
unit-tested against fakes and `httptest` servers only.

This means the following are **unverified against the real API**, whatever the
tests say:

- That `liveChatMessages.list` accepts an API key for a public live chat in
  practice as well as in the documentation.
- That `maxResults=2000` is accepted, and that the documented 200–2000 range is
  the real one.
- That `liveChatMessages.list` really costs 5 units. Google publishes no cost for
  it; `yc`'s entire budget arithmetic rests on a community-observed estimate.
- That the three error-classification channels appear in the shapes `yc` parses.
- That the Google installed-app flow completes with an ephemeral-port loopback
  redirect and PKCE, without a client secret.
- That a `tokeninfo` response carries granted scopes in the form `yc` reads.
- That refresh, rotation, and revoke behave as coded.
- That `offlineAt` and the 2-minute grace window match how chat actually outlives
  a broadcast.
- That the reply convention (`@DisplayName ` prefix) reads as a reply to viewers.

Closing this gap needs a Google Cloud project, an API key, a Desktop OAuth
client, and a real live broadcast to point at. Until a session below records it,
treat every "Credentialed" label in the docs as *written and reviewed, not
witnessed*.

## 2026-08-08 Quota Cost Model: Documentation Review

A previous build flagged `yc`'s `search.list` model as unverified and possibly
wrong. It was checked against Google's own documentation. **The model is
correct.** This was a documentation review, not an API call — no credential was
used and no quota was spent, so it does not close the gap above.

Checked, and matching what `yc` encodes:

- [`search.list` reference](https://developers.google.com/youtube/v3/docs/search/list)
  states verbatim: *"Quota impact: 100 calls per day. A call to this method has a
  quota cost of 1 unit in the Search Queries quota bucket."* The 100-units-from-
  the-main-pool figure that most third-party references still quote describes the
  pre-2026 model.
- [Revision history](https://developers.google.com/youtube/v3/revision_history)
  carries the **2026-06-01** entry moving `search.list` and `videos.insert` into
  their own buckets. The migration `yc` cites is real and correctly dated.
- [Quota allocation](https://developers.google.com/youtube/v3/getting-started#quota)
  states the default as *"100 `search.list` calls, 100 `videos.insert` calls, and
  10,000 units per day combined for all other endpoints"* — confirming both
  `DefaultDailyUnits = 10000` and `DefaultSearchCalls = 100`.
- [Quota costs table](https://developers.google.com/youtube/v3/determine_quota_cost)
  confirms `videos.list` = 1, `channels.list` = 1, `subscriptions.list` = 1,
  `videoCategories.list` = 1, `videos.update` = 50. No endpoint cost `yc` encodes
  was found to be wrong.
- The same table lists **no** live chat method, and the reference pages for
  `liveChatMessages.list`, `liveChatMessages.insert`, and `liveChatBans.insert`
  carry no *Quota impact* line at all. The five live chat costs therefore remain
  estimates — the bullet above still stands.
- `liveChatMessages.list` `maxResults` is documented as *"200 to 2000, inclusive.
  The default value is 500"*, confirming `MaxResultsPerPoll = 2000` is in range.
  Whether the API honors it in practice is still unverified.

One caveat recorded honestly: third-party references disagree, and some 2026-
dated ones still print 100 units. The four Google pages above agree with each
other and with the bucket naming used by `videos.insert` (*"1 unit in the Video
Uploads quota bucket"*), so the disagreement is treated as staleness elsewhere
rather than ambiguity here. A real project's quota page would still be better
evidence than any of it.

## 2026-08-08 Credential-Free Build Gate

Environment:

- Host: Linux amd64, Go `go1.26.5`, module `go 1.26` / `toolchain go1.26.4`.
- Isolated `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` under `mktemp -d`, with every
  `YC_*` and `GOOGLE_*` credential variable cleared.
- YouTube mode: `--mock` and offline diagnostics only. No live chat connection
  was attempted, and no credential was configured.

Commands run:

```sh
gofmt -l .
go build ./...
go vet ./...
go test ./...
go test -race ./...
go tool staticcheck ./...
go run ./cmd/yc --help
go run ./cmd/yc doctor
go run ./cmd/yc config show
go run ./cmd/yc quota
go run ./cmd/yc chat --mock
go run ./cmd/yc chat --video dQw4w9WgXcQ
```

Results:

- Formatting clean; build, vet, `staticcheck`, and 808 tests across 11 packages
  pass, including under `-race`.
- `yc --help` exits 0.
- `yc doctor` prints 15 checks and exits 0. In the isolated environment it
  reports the credential file as not found, no credentials configured, the quota
  budget arithmetic (`10000 units/day at an estimated 5 per poll … one poll every
  43s`), the debug-log hardening guarantee, a resolved `claude` theme, the
  effective display modes, a writable cache, no configured chats, the cost table
  in force marked as estimates, a 0/10000 ledger, and — the only two network
  checks — a reachable YouTube API and a skipped identity lookup.
- `yc config show` prints the config path plus 49 keys, with every secret shown
  as `""` (unset), and exits 0.
- `yc quota` prints the empty ledger and exits 0 without spending anything.
- `yc chat --mock` piped to a non-TTY renders one ANSI-stripped rectangular frame
  and exits 0.
- `yc chat --video dQw4w9WgXcQ` with no credentials **exits 2 before opening any
  socket**, naming all three ways forward (`yc login`, `YC_YOUTUBE_API_KEY`,
  `--mock`).

Redaction check:

- A run with an obviously fake `AIzaSy…` API key marker in the environment, with
  debug logging enabled, produced no occurrence of the marker in stdout, stderr,
  or the debug log. The log file was mode `0600`.

Interactive PTY checks (driven under tmux):

- Default PTY: `yc chat --mock` walked through splash → composer → send → all
  three tabs → command palette → theme picker → emoji picker → expanded help →
  mention completion → `q`. Layout held at each step and the exit was clean.
- Chat, Stream Info, and the quota tab all rendered; the quota tab correctly
  reported that a mock source has no ledger to account for.

Skipped or environment-limited checks:

- **Every credentialed YouTube check**: skipped, no Google Cloud project or
  credential set was available in this environment. See the section above.
- **Docker**: skipped in this session; built and smoked in the release gate
  below.
- **Real pointer/mouse gestures**: not driven manually. Mouse support is
  wheel-scroll only and is covered by model-level tests; this pass covered the
  equivalent keyboard workflows.
- **Multi-hour quota drift**: not measured. Comparing `yc quota` against the Cloud
  Console's usage graph over a real stream is the only way to validate the cost
  estimates, and it has not been done.
- **Non-Unix builds**: not built. The credential store returns
  `ErrCredentialsUnsupported` there by construction and is unit-tested, but no
  Windows or Plan 9 binary has been produced.
- **Terminal matrix**: only the default validation PTY was exercised. Narrow,
  wide, and live-resize behavior is covered by tests but has not been walked
  through by hand at multiple sizes.
- **OSC 11/111 background override**: not confirmed against a specific terminal
  emulator.

## 2026-08-08 Release Gate

Environment: same isolation rule as the session above — every run under a
throwaway `HOME` plus `XDG_{CONFIG,CACHE,STATE,DATA}_HOME`, with every `YC_*`,
`YOUTUBE_*`, and `GOOGLE_*` variable cleared. Host Linux amd64, Docker available.

Toolchain gate, all clean: `go mod tidy` (a no-op — `go.mod` and `go.sum`
unchanged), `gofmt -l .` (empty), `go vet ./...`, `go test ./...`,
`go test -race ./...`, `go tool govulncheck ./...` ("No vulnerabilities found"),
`go tool staticcheck ./...`. 907 top-level tests (1,484 including subtests)
across 12 packages pass; the single skip is `TestWriteDocsScreenshots`, which is
gated on `YC_WRITE_SCREENSHOTS=1` by design so CI never writes into the tree.

Release artifacts: `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build
-trimpath -ldflags "-s -w"` both succeed. `file` reports each as
`ELF 64-bit LSB executable … statically linked … stripped` — x86-64 and ARM
aarch64 respectively — and neither binary contains a build-host path, confirming
`-trimpath`. `scripts/release-dry-run.sh --version 0.1.0` passes end to end,
including the tampered-artifact negative check, the native binary smokes, and
the Docker build.

**Docker: built and run.** `docker build` succeeds and the container passes
`--help`, `--version`, `doctor`, `config show`, and `chat --mock`. With
`--build-arg VERSION=0.1.0` the image reports `yc 0.1.0`; the dry run now asserts
that equality, so an unstamped image fails the release instead of silently
shipping as `yc dev`.

Interactive terminal checks, driven under **tmux** with a real pty:

- `yc chat --mock` at 130x36 and at 62x20: the pane is exactly the terminal's
  size, no row exceeds the terminal width when measured by display cells, and
  nothing is pushed into scrollback. `q` exits 0 from every size.
- Also walked at 80x24, and with `ctrl+g` (layout), `ctrl+t` (theme picker, then
  `esc`), and `?` (expanded help). Each held its geometry and exited cleanly.
- Ten non-interactive commands (`--help`, `--version`, `doctor`, `config show`,
  `config path`, `quota`, `profile list`, `login --dry-run`,
  `setup --non-interactive`) exit 0 with no panic; `chat --video dQw4w9WgXcQ`
  with no credentials exits **2** with an actionable message and opens no socket.
- Every one of those outputs was scanned for an `AIza…`-shaped key, a `ya29.`/
  `1//0…`-shaped token, and a full Google authorization URL. None appeared.

A note on method, because it changed the conclusion: two hand-rolled pty
harnesses (a raw `pty.fork` loop and `pexpect`) both failed to deliver keystrokes
to Bubble Tea, which made `yc` look like it could not be quit. A minimal
standalone Bubble Tea program failed identically in the same harnesses, and a
temporarily instrumented `yc` build confirmed that **zero** `tea.KeyMsg` were
arriving — the harness, not the app. Under tmux the same keys register and `q`
quits. Recorded because "the TUI ignores the keyboard" is exactly the kind of
false alarm that gets a real release blocked, or a real bug waved away.

Still not verified here: everything in *Credentialed YouTube Validation: NONE*
above, which this session does not touch, and the GitHub Actions workflows, which
cannot be executed off a runner.

## How To Add A Session

Copy the shape above. Record:

1. The date and what the session was for.
2. Host OS, architecture, Go version, terminal program and version, and PTY size.
3. Whether credentials were available, and which — never their values.
4. The exact commands run.
5. What was observed, including anything that looked wrong.
6. Every skipped check, with the environment reason.

Then update the status labels in the README and the relevant doc — and only
then.

# CLEANUP — Track E (clean code) report

Date: 2026-08-15. Base: `ceadc95` (all prior tracks merged). Branch: `swarm/cleanup`.

The baseline was already clean (zero staticcheck findings, clean vet/race). The
real gap from RECON was the missing lint gate; this track added it, drove the
tree to zero findings under it, and wired it into CI and the contributor docs.

## Lint gate

- Added `.golangci.yml` (golangci-lint v2 schema, verified against v2.12.2)
  enabling errcheck, govet, staticcheck, revive, gocritic, gosec, misspell,
  unparam, plus the gofmt formatter. Default issue caps are lifted so a run
  always shows everything.
- First full run: **440 findings** (with caps lifted). Final state: **0**.

### Fixed (code changes, all behavior-neutral)

| Category | What changed |
|---|---|
| misspell (37) | Mixed British/US prose normalized to US ("behaviour"→"behavior", "cancelled"→"canceled", ...). The `spectre` theme name is exempt via config — users reference it from config files. |
| revive `exported` (~30) | Doc comments added to enum const blocks (event kinds, badges, fragment types, poller states, doctor statuses, ...) and to the one-line stream accessors on `LiveChatClient`, `MockChatClient`, `FakeChatClient`, `UnsupportedCredentialStore`. |
| revive `unused-parameter` | `quotaLedgerLines`/`streamInfoLines` in `internal/app/view.go` dropped their never-read `width` parameter (the RECON-flagged diagnostics); unread params in interface impls renamed to `_`. |
| revive `redefines-builtin-id` | Locals named `cap` → `ceiling` (poll backoff), `real` → `realDir` (storage tests). |
| staticcheck QF/ST | Tagged switch in `moderation.go` (`moderationConfirmPrompt`), De Morgan simplifications, redundant embedded selectors, and all invisible bidi/C1/ZWJ characters in source now written as `\u` escapes (also clears gosec G116 Trojan-Source on `panes.go`). |
| errcheck (production) | Best-effort cleanup closes made explicit (`defer func() { _ = x.Close() }()`); two real fixes below. |
| unparam | `bindingForKeys` → `hasBindingForKeys` (only the bool was ever used); `safeError` lost its never-passed variadic match targets. |
| gocritic | `backoff /= backoffStep`; guarded a `strings.Index` result in `animation/text_test.go` before slicing. |
| benchmarks | `for i := 0; i < b.N; i++` → `b.Loop()` in `bench_flood_test.go` and `fragments_ascii_test.go` (per-task item). `bench_poll_test.go` keeps `b.N`: it counts received messages, which `b.Loop` cannot express. |

### Real (latent) defects found by the gate

1. `yc export superchats` never checked the output file's `Close`, which is
   where a full-disk error on the final flush surfaces. Now checked and
   reported as a failure (`internal/cli/export.go`).
2. `randomString` in `internal/auth/oauth.go` computed its rejection-sampling
   threshold as a `byte`; for any alphabet whose size divides 256 the
   threshold wraps to zero and the loop never terminates. No current caller
   hits it; the arithmetic is now `int` with an explicit >256 guard.
3. The earlier misspell/staticcheck autofix pass had doubled two C1 control
   bytes in `escape_injection_test.go` seeds (escape added, raw byte left
   behind); restored to single controls.

### nolint'd (each with an inline reason)

- `handleMentionKey` / `handleSearchKey` / `handleSidebarKey` — unparam; the
  modal key handlers share the `(model, cmd, consumed)` shape even when one
  never issues a command.
- Two `appendAssign` sites (`mention.go`, `render/message.go`) — the append
  deliberately continues a slice that is dead afterwards.
- gosec G404 (poll jitter), G204 (notify-send / browser openers resolved via
  fixed LookPath tables), G101 (public OAuth endpoint URLs pattern-matched as
  "credentials"), G115 (bounded hex→uint8 in theme, deliberate UTF-16LE byte
  split in notify), and the `RenderOptions = Options` alias stutter (both
  spellings appear in the design specs).

### Config-level policy exclusions (documented in `.golangci.yml`)

- errcheck ignores `fmt.Fprint*` — every call writes to the CLI's own
  stdout/stderr or a test writer; no channel exists to report a failed write.
- gosec G304 disabled — a local CLI opening user-named config/log paths is
  not a trust-boundary crossing.
- Tests exempt from gosec, errcheck, unparam, and revive's
  unused-parameter/empty-block — teardown closes surface as test failures,
  stubs implement fixed callback shapes, `for range ch {}` drains are the idiom.

## Architecture verification (no changes needed)

`go list` import check: `internal/youtube` imports only `auth` + `debuglog`
(no app/render); `internal/render` imports `emoji`/`theme`/`youtube` (no app);
`internal/chatlog` imports only `youtube` (no UI packages). The one non-main
panic (`theme/palette.go:37`) is an init-time invariant on a compiled-in
preset — acceptable. Error wrapping, context plumbing, and channel discipline
spot-checked; repo standard confirmed high, nothing to fix.

## CI

`.github/workflows/ci.yml` already ran gofmt, vet, unit + race tests,
govulncheck, staticcheck, cross-builds, smokes, and its own package-boundary
check — nothing was missing except lint. Added one step:
`golangci/golangci-lint-action@v8` pinned to the v2.12 linter line, right
after the staticcheck step. Nothing else touched.

## Docs

- README verified: install, config, and keybindings pointers all present.
- CONTRIBUTING.md: `golangci-lint run ./...` added to the default
  verification list, with install instructions, a pointer to `.golangci.yml`,
  and the `//nolint:<linter> // reason` suppression rule.
- docs/development.md: same step added to the clean-checkout gate.
- Every package already has doc comments (most via doc.go); none added.

## Verification

At branch tip: `go build ./...`, `go vet ./...`, `go test ./...`,
`go test -race ./...` all green; `golangci-lint run ./...` (v2.12.2): 0 issues;
`gofmt -l .` empty. Golden files untouched.

## Out of scope / handoffs

Recorded in FINDINGS.md under Track E.

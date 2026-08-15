# Security audit — Track A

Date: 2026-08-15. Baseline: `5211b69`. Branch: `swarm/security`.

Scope: credentials, network, untrusted input, terminal escape injection, local
attack surface. The codebase was already in strong shape; most items below are
"already satisfactory" with the evidence that proves it. Four small fixes were
made, each behavior-preserving for legitimate input.

## Findings

| # | Severity | Location | Finding | Resolution | Status |
|---|---|---|---|---|---|
| 1 | Info | `internal/app/activity.go` (`activityEntryForModeration`, `displayNameOr`), `internal/app/notify.go` (notification summary) | RECON flagged these paths as carrying attacker-chosen display names with only `strings.TrimSpace`. Probing proved every one is neutralized downstream by `flattenControlRunes` via the pane line writers before it reaches the frame. | Pinned with end-to-end probes (`internal/app/escape_injection_test.go`): one hostile name per escape family (CSI, OSC title/hyperlink, raw C1 introducers, bidi overrides/isolates, BEL/DEL) rendered through `shellModel.View`, asserting no sequence introducer, no control rune, no bidi control, and an exactly terminal-sized frame. | Already satisfactory, now pinned by tests |
| 2 | Low | `internal/app/view.go` `sanitizeContextValue` | Passed raw C1 controls (U+009B is a one-byte CSI introducer on some terminals) and bidi overrides through; safe only because `fitLine` happened to flatten them later on the same path. | Now mirrors `flattenControlRunes`: all Unicode controls (C0, DEL, C1) become a visible `�`, bidi controls are dropped. | Fixed (`fix(app): drop bidi and C1 controls in sanitizeContextValue`) |
| 3 | Low | `internal/app/view.go` `sanitizeContextValue` | Found by fuzzing: trimming ran before rune mapping, so a trailing bidi control shielded whitespace from the trim (`"0 ‮"` → `"0 "`), making the function non-idempotent. | Trim moved after the mapping; counterexamples checked in as fuzz corpus under `internal/app/testdata/fuzz/`. | Fixed (`fix(app): trim after mapping in sanitizeContextValue`) |
| 4 | Medium (defense in depth) | `internal/app/notify.go` `desktopNotificationCommand` | notify-send parses options anywhere on its command line; the body begins with an attacker-chosen author name, so a chatter named `--icon=/etc/passwd` would be parsed as an option, not displayed. macOS (osascript stops at the first operand, which is yc-controlled) and Windows (encoded, XML-escaped script) were fine. | `--` end-of-options marker inserted before the title; regression test added. | Fixed (`fix(app): stop notify-send parsing a chatter's name as an option`) |
| 5 | Low (defense in depth) | `internal/youtube/transport.go` `attempt` | The only unbounded body read in the client: a 200 response streamed into the JSON decoder with no ceiling. Error bodies, drains, and all OAuth reads were already capped. The endpoint URL is operator-configurable, so "it is always Google" is not a transport-level guarantee. | Success-path decode now reads through `io.LimitReader` at 64 MiB (largest legitimate page is far smaller); truncation surfaces as the existing transient decode error. | Fixed (`fix(youtube): bound the success-path response body`) |
| 6 | — | `internal/render/text.go` `sanitizeUserText`, `internal/app/panes.go` `flattenControlRunes`, `sanitizeContextValue` | No fuzz coverage existed anywhere in the module. | Three fuzz targets added with terminal-safety invariants (no controls except newline, no bidi, `ansi.Strip` identity on output, idempotence, safe-input pass-through). Each ran 60 s of coverage-guided fuzzing (2–6.6 M execs); the only failure was finding #3. | Added (`test: fuzz the three text sanitizers…`) |
| 7 | — | `internal/storage/credentials.go`, `internal/auth`, `internal/debuglog` | Credential handling: 0600/0700 enforced with symlink rejection (`Lstat` + `O_NOFOLLOW`) and atomic replace; `Secret` type with no live JSON tags; redaction walked across the whole error chain; leak tests (`credentials_leak_test.go`, `transport_redaction_test.go`, `redactor_format_test.go`, mode-matrix tests) all pass. | None needed. | Already satisfactory |
| 8 | — | `go tool govulncheck ./...` | Re-run at audit time. | "No vulnerabilities found." | Already satisfactory |
| 9 | — | `internal/youtube/client.go`, `internal/auth/oauth.go` | Network hygiene: both HTTP clients refuse redirects (API key in query string / secrets in POST bodies), set timeouts with sane defaults, derive per-attempt contexts, use default (verifying) TLS — no `InsecureSkipVerify` anywhere. OAuth reads all bounded by `maxOAuthResponseBytes`; error bodies by `maxErrorBodyBytes`. | Only gap was finding #5. | Already satisfactory |
| 10 | — | `internal/youtube/normalize.go` | JSON robustness: normalization consumes already-decoded typed structs; malformed and non-JSON bodies on the error path are table-tested (`transport_auth_coverage_test.go`); hostile event shapes covered by `testdata/events` including `invalidType.json`; unknown types degrade to `EventKindUnknown` rather than being dropped. Sanitization deliberately happens at the display boundary, which the Track A probes now verify end to end. | None needed. | Already satisfactory |
| 11 | — | `internal/config`, `internal/cli/login.go` (browser open) | Local attack surface: config values that reach the frame pass the same display sanitizers (verified by the tab-bar probe, which injects through the chat label); browser opening and the notifier both use `exec.CommandContext` with argument arrays — no shell interpolation anywhere; `$BROWSER` is the user's own environment. | None needed (notify-send argument parsing was finding #4). | Already satisfactory |

## Verification

Every commit passed `go build ./... && go vet ./... && go test ./...`.
Fuzz runs: `FuzzSanitizeUserText` 60 s / 4.4 M execs, `FuzzFlattenControlRunes`
60 s / 6.6 M execs, `FuzzSanitizeContextValue` 60 s / 2.3 M execs after the
finding-#3 fix — all clean.

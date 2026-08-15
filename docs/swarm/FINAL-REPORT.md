# FINAL REPORT — swarm improvement pass

Date: 2026-08-15. Base: `5211b69`. Merge order: Security → Performance → UI/UX →
Features → Clean Code, each gated on `go build && go vet && go test ./...` and a
final full regression (`-race`, golangci-lint, govulncheck, perf harness re-run).

## Per-track summary

**A — Security** (`docs/swarm/SECURITY-AUDIT.md`)
Four fixes: `sanitizeContextValue` now neutralizes C1 controls and bidi overrides
and trims after mapping (fuzz-found idempotence bug); notify-send gets a `--`
end-of-options terminator so a chatter named `--icon=...` can't smuggle an option;
the API client's success-path body read is bounded (64 MiB `io.LimitReader`).
First fuzz tests in the repo (3 targets, 60 s each, corpus checked in) plus
end-to-end escape-injection probes that *disproved* the suspected soft spots
(activity pane, toasts, tab bar — all already neutralized downstream, now pinned).
Credentials, TLS, redirects, exec surfaces, JSON hostility: audited, satisfactory.

**C — Performance** (`docs/swarm/PERF-REPORT.md`)
Replay benchmark harness committed first (flood Update, warm/cold View, pipeline,
poller delivery). Three profile-justified, behavior-neutral wins:
in-place scrollback trimming, frame-scoped row-block scratch slice, ASCII fast
path in fragment splitting. Backpressure, dedupe, cadence, and goroutine-leak
coverage were verified as already present.

**B — UI/UX** (`docs/swarm/UIUX-NOTES.md`)
Added: `g`/`G`, `ctrl+d`/`ctrl+u` scrollback navigation; sticky "N new" strip
while scrolled up; modal incremental `/` search with `n`/`N` and match
highlighting; `y` copy-to-clipboard via OSC 52 (sanitized plain text, no
shell-out). Verified satisfactory: CJK/ZWJ width, resize, NO_COLOR/16-color,
empty/error states. `docs/keybindings.md` updated; help overlay auto-generates
from the keymap table as before.

**D — Features** (`docs/swarm/FEATURES.md`)
New `internal/chatlog`: opt-in JSONL session logs, size-rotated, 0600/0700,
redacted, one-notice failure degradation. New `yc export superchats` CSV ledger
(integer-micros amounts, streamed reads). Opt-in auto-follow: bounded,
quota-aware channel re-resolution after a stream ends, reopening the new
broadcast into the same UI chat. Config keys: `chat_logging`, `chat_log_dir`,
`chat_log_max_bytes`, `chat_log_max_files`, `auto_follow`,
`auto_follow_poll_seconds`, `auto_follow_max_checks` — all default off,
documented in `docs/config.md`. No new keybindings.

**E — Clean Code** (`docs/swarm/CLEANUP.md`)
`.golangci.yml` added (errcheck, govet, staticcheck, revive, gocritic, gosec,
misspell, unparam) + CI lint step. 440 initial findings → 0: misspellings, doc
comments, shadowed builtins, Trojan-Source escapes, dead params — and three real
defects (unchecked export-file Close, wrap-to-zero threshold in the OAuth state
generator that could infinite-loop, two corrupted test seeds). Architecture
boundaries verified clean; deliberate patterns carry justified `//nolint`s.

## Metrics before → after

| Metric | Before (5211b69) | After |
|---|---|---|
| golangci-lint findings | no config (440 on first run) | **0** |
| staticcheck / govulncheck | 0 / 0 | 0 / 0 |
| Fuzz targets | 0 | 3 (+ corpus) |
| Benchmarks | 0 | 8+ (harness committed) |
| Flood ingest (ChatFloodUpdate) | 26,097 ns/op, 190,647 B/op | **2,419 ns/op, 2,099 B/op** |
| SplitFragments (ASCII) | 10,005 ns/op | **1,338 ns/op** |
| Pipeline Update+View B/op | 629,941 | 368,495 |
| Coverage (notable) | app 80.2, render 90.5, cli 75.1 | app 80.5, render 90.8, cli 74.1 (+chatlog 86.8) |
| `go test -race ./...` | pass | pass |

(cli coverage dipped slightly: the new export subcommand adds surface; its logic
is tested, some flag plumbing is not.)

## Deferred items

- Regex mute list — handoff sketch in FINDINGS.md (config-only `mute_patterns`
  feeding the data-driven filter table); skipped to keep Track D reviewable.
- `features.clipboard` toggle for OSC 52 (tmux/remote policy concern) — key
  sketched in FINDINGS.md; today the copy action is interactive-only.
- j/k cursor does not auto-scroll the viewport (long-standing behavior; changing
  it deserves a deliberate decision).
- Auto-follow does not arm if a stream ends while the reconnect ladder is
  exhausted (never observes `ConnectionClosed`) — acceptable v1 corner.
- ~~Flaky `TestValidateAccessTokenTreatsATransportFailureAsUnreachable`~~ —
  fixed during the release: the real cause was the rejection classifier
  substring-matching "401" inside a dial port number, not port reuse.
- Cold-frame render allocations (~2.1 MB on resize) — reusable grapheme scratch
  buffer if wrapping is ever restructured; row cache hides it in steady state.

## Suggested changelog (next tag)

### Added
- In-buffer search (`/`, `n`/`N`), vim scroll keys (`g`/`G`, `ctrl+d`/`ctrl+u`),
  sticky new-message indicator, copy message to clipboard (`y`, OSC 52).
- Opt-in JSONL chat logging with rotation, and `yc export superchats` CSV ledger.
- Opt-in auto-follow: reconnect to a channel's next broadcast after a stream ends.
- Fuzz tests for the terminal-safety sanitizers; replay benchmarks for the chat
  pipeline; golangci-lint config and CI gate.

### Fixed
- Tab-bar sanitizer now neutralizes C1 controls and bidi overrides.
- Desktop notifications can no longer have options injected via a display name.
- API response bodies are bounded on the success path.
- OAuth state generation could loop forever for some alphabet sizes.

### Performance
- ~10x faster and ~99% fewer allocations ingesting messages at the scrollback
  cap; ~7.5x faster fragment splitting for ASCII messages.

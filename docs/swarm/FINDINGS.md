# FINDINGS — cross-track handoffs and hot-zone locks

Agents: when you hit an issue that belongs to another track, record it under that track's
heading and move on. Do not fix out-of-scope issues.

## Hot-zone locks

Only one track may touch each zone at a time. Orchestrator grants/releases.

| Zone | Files | Held by |
|---|---|---|
| Render pipeline | `internal/render/*`, `internal/app/view.go`, `internal/app/panes.go` | Track B (A and C merged) |
| Message pipeline | `internal/youtube/poll.go`, `internal/app/live_chat.go`, `internal/app/chat.go` | released (Track C merged) |
| Config schema | `internal/config/*` | Track D |

## Track A — Security
Done. See docs/swarm/SECURITY-AUDIT.md. No out-of-scope handoffs: nothing
belonging to another track surfaced during the audit. Render-pipeline lock can
be released to Track C (Track A touched internal/app/view.go's
sanitizeContextValue only; internal/render gained a fuzz test, no code change).

## Track B — UI/UX
(none yet)

## Track C — Performance
Done. See docs/swarm/PERF-REPORT.md. Notes for later tracks:

- Out of scope for Track C: `render.Rows` allocates per-fragment intermediate
  strings (`graphemeStrings`, `appendFragment`, `wrapChunks` show up in the
  cold-cache alloc profile at ~2.1 MB/frame total). The row cache hides this in
  steady state; if the wrapping code is restructured anyway, a reusable
  grapheme scratch buffer would cut the cold-path (resize) cost.
- The warm frame's remaining CPU is lipgloss `Style.Render` plus ANSI width
  measurement across all panes (~1.8 ms/frame at 100x30). If frame cost ever
  matters (e.g. higher frame-clock rates), whole-pane memoization keyed on
  pane inputs is the next lever; not worth the invalidation complexity today.
- `BenchmarkChatFloodUpdate` occasionally reports ~15 us/op instead of
  ~2.4 us/op on the first `-count` repetitions (suspected CPU frequency
  scaling on the benchmark box); prefer best-of-N when comparing.

## Track B — notes from its pass

### For Track D (config)

- A `features.clipboard` (or similar) config key could let users disable the
  OSC 52 clipboard write from `y` entirely — some remote/tmux setups treat
  clipboard escapes as a policy concern. Track B deliberately did not touch
  the config schema; the key is gated on interactive terminals only today.

### Out-of-scope observations

- `selectMessage` (j/k) does not scroll the viewport to keep the cursor
  visible; only search jumps do (`scrollToMessage`). Reusing
  `scrollToMessage` from `selectMessage` would make j/k follow the cursor
  off-screen, but it changes long-standing behavior and is left for a
  deliberate decision.
- `maxScrollOffset` uses a `rows ≈ messages × 4` heuristic; the paint-time
  clamp corrects it, but `g`/`home` can momentarily overshoot on short
  buffers until the next frame. Harmless, documented in the code.

## Track D — Features
Done. See docs/swarm/FEATURES.md. Notes:

- Flaky test (pre-existing, environmental):
  `internal/cli/token_test.go` `TestValidateAccessTokenTreatsATransportFailureAsUnreachable`
  failed once because a "connection refused" dial was classified as
  Reachable=true — the supposedly dead 127.0.0.1 port appears to have been
  reused by another local listener between setup and call. Passed on rerun
  (`-count=3`). Worth making the test bind-and-close its own listener
  instead of guessing a dead port.
- `allow_search` and `emoji_autocomplete_mode` are documented as inert in
  docs/config.md; still true. `youtube.SearchLiveVideo` remains uncalled —
  auto-follow deliberately uses `channels.list` (published 1-unit cost)
  rather than `search.list`.
- Regex mute list (wishlist item 4) handoff sketch: add `mute_patterns`
  (string list) to `FeatureConfig`; compile once at model build (invalid
  patterns degrade with a startup warning, per the config layer's
  "normalize, don't reject" rule); apply either as a retention-time drop in
  `chatStateSet.applyMessage` or as a fifth `messageFilter` if view-only
  semantics are preferred. The filter table in `internal/app/filters.go` is
  data-driven and easy to extend, but shortcut digits 1-4/0 are taken; a
  config-only mute avoids the keybinding question entirely.
- Auto-follow keys off `ConnectionClosed`; a stream that ends while yc is
  disconnected (reconnect ladder exhausted) never reports Closed, so
  auto-follow does not arm in that corner. Acceptable for v1; noted for a
  follow-up.
- Chat logging is wired on the live path only (`runLiveChatSession`);
  `yc chat --mock` deliberately does not write logs even when
  `chat_logging` is set, so the demo stays filesystem-silent.


## Track E — Clean Code
(none yet)

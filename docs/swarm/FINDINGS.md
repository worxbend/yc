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

## Track D — Features
(none yet)

## Track E — Clean Code
(none yet)

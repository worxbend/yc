# Track C: Performance Report

Branch: `swarm/perf`. Machine: AMD Ryzen 7 5700X (16 threads), go1.26.5,
linux/amd64. All numbers are `go test -bench -benchtime 2s -count 3`,
best of 3, `b.ReportAllocs` enabled. All changes are behavior-neutral and
proven by the existing test suite (`go build ./... && go vet ./... &&
go test ./...` green before every commit).

Note: `docs/swarm/RECON.md` did not exist in this worktree; the
"already done" verification below is from direct code reading instead.

## Already done before this track (verified, not redone)

- **Bounded channels with drop-and-count backpressure**: transport
  streams are bounded; the status bar leads with the dropped-message
  counter (documented in `docs/code-style.md`, "Terminal UI Rules").
- **Dedupe ring**: `chatState.markSeen` in `internal/app/state.go` keeps
  a fixed-size seen-ID ring; the poller has its own dedupe.
- **`pollingIntervalMillis` honored as an absolute floor** with adaptive
  backoff: `internal/youtube/poll.go`, asserted by
  `poll_quota_pacing_test.go`.
- **Scrollback limit is a real cap**: `chatState.trimScrollback` trims
  from the head on every append; roster, seen-ID ring, reveal queue, and
  the row cache are all bounded too (`internal/app/state.go`,
  `chat_row_cache.go`).
- **Goroutine-leak coverage for reconnect cycles**:
  `TestRepeatedReconnectsLeaveNoGoroutinesBehind` in
  `internal/youtube/poll_reconnect_lifecycle_test.go` runs 25 reconnects
  and asserts the settled goroutine count returns to a warmed baseline.
  Adding `go.uber.org/goleak` would duplicate this; the dependency was
  deliberately not added.

## Harness

- `internal/app/bench_flood_test.go` — replays the deterministic stress
  burst (`stressBurst`, every event kind, grapheme-hostile text) through
  the real `Update`/`View` pipeline on the fake clock.
- `internal/youtube/bench_poll_test.go` — end-to-end Poller delivery
  against an in-process server: HTTP round trip, JSON decode,
  normalization, dedupe, channel delivery.
- `internal/youtube/fragments_ascii_test.go` — micro-benchmarks for the
  fragment splitter plus the differential test pinning the ASCII fast
  path to the segmented path.

## Baseline vs final

| Benchmark | Baseline | Final | Delta |
| --- | --- | --- | --- |
| ChatFloodUpdate (ingest 1 msg) | 26,097 ns/op, 190,647 B/op, 7 allocs | 2,450 ns/op, 2,099 B/op, 5 allocs | **-91% ns, -99% B** |
| ChatViewWarmCache (1 frame) | 1,779,075 ns/op, 408,043 B/op, 5,992 allocs | 1,805,374 ns/op, 336,190 B/op, 5,991 allocs | ns flat, **-18% B** |
| ChatViewColdCache (resize frame) | 8,014,293 ns/op, 2,131,202 B/op, 31,879 allocs | 8,285,737 ns/op, 2,093,672 B/op, 31,885 allocs | flat (noise) |
| ChatPipelineUpdateAndView | 1,902,801 ns/op, 629,941 B/op, 6,145 allocs | 1,892,158 ns/op, 368,495 B/op, 6,143 allocs | ns flat, **-41% B** |
| PollerDelivery (per msg, 50/page) | 18,664 ns/op, 4,814 B/op, 25 allocs | 19,080 ns/op, 4,820 B/op, 25 allocs | flat (pipelined; see below) |
| SplitFragments, ASCII body | 10,005 ns/op, 567 B/op, 10 allocs (old path measured directly) | 1,357 ns/op, 535 B/op, 9 allocs | **-86% ns** |

Throughput, sustained (1e9 / ns/op):

- Ingestion (Update only): ~38k msgs/s → **~410k msgs/s** per core.
- Full pipeline (one message + one full repaint): ~525 msgs/s, unchanged
  — a repaint costs ~1.9 ms and dominates; in the real app repaints are
  frame-clocked, not per-message, so ingestion headroom is what matters.
- Poller delivery: ~52k msgs/s end to end, unchanged; the poller parses
  ahead of the consumer, so the SplitFragments CPU win shows up as less
  CPU burned per page (it was >60% of normalization CPU), not as
  channel-delivery latency.

## Changes made (each commit cites its own before/after)

1. `perf(app): trim scrollback in place instead of reallocating the
   buffer` — at the cap, every message paid two full history copies
   (append regrowth + fresh trim array). Trim now slides in place and
   clears the freed tail, which releases dropped references identically.
   This was 90% of ingestion allocation.
2. `perf(app): reuse one scratch slice for per-frame chat row blocks` —
   `chatRowBlocks` rebuilt a backlog-sized slice every repaint; it now
   fills a frame-scoped package-level scratch buffer (same
   single-goroutine safety argument as `sharedRowCache`). -18% B/frame,
   ns flat.
3. `perf(youtube): skip the grapheme segmenter for pure-ASCII bodies` —
   `SplitFragments` walks bytes when the body has no multi-byte runes;
   for ASCII, bytes are grapheme clusters and nothing below 0x80 is in
   `emojiRanges`. A differential test pins both paths to identical
   output. -86% ns on typical ASCII lines.

## What was profiled and deliberately left alone

- **Warm-frame CPU (~1.8 ms)** is spread across lipgloss `Style.Render`
  (~53%) and ANSI/grapheme width measurement for the whole screen —
  chat pane, activity column, tab bar, status bar, gradient lines. There
  is no single hot spot left in `internal/app`; going further means
  memoizing entire rendered panes, whose invalidation complexity is not
  justified at a frame-clocked ~1.8 ms (≈2% of one core at 10 fps).
  `chatRowCache` already removes the per-message render cost (warm vs
  cold frame: 1.8 ms vs 8.3 ms — the cache buys ~4.6x).
- **Cold-cache frame (resize/theme change)** happens on discrete user
  actions, not in the hot loop; not worth optimizing.
- **Poller allocations (25/msg)** are mostly JSON decode of the wire
  item; at 1 request/s real-world polling this is negligible.
- **`sync.Pool`**: no profile showed a candidate that survives the wins
  above; not introduced.
- Potential further render-path wins live in `internal/render` (owned by
  Track A during this pass); noted in `docs/swarm/FINDINGS.md`.

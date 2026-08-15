# Cross-track findings

## Track C (Performance)

- `docs/swarm/RECON.md` was not present in the Track C worktree; the
  shared reconnaissance had to be re-derived from the code. If other
  tracks depend on it, make sure it is committed somewhere visible.
- Out of scope for Track C (Track A owns `internal/render` this pass):
  `render.Rows` allocates per-fragment intermediate strings
  (`graphemeStrings`, `appendFragment`, `wrapChunks` show up in the
  cold-cache alloc profile at ~2.1 MB/frame total). The row cache hides
  this in steady state; if Track A restructures wrapping anyway, a
  reusable grapheme scratch buffer would cut the cold-path (resize)
  cost.
- The warm frame's remaining CPU is lipgloss `Style.Render` plus ANSI
  width measurement across all panes (~1.8 ms/frame at 100x30). If frame
  cost ever matters (e.g. higher frame-clock rates), whole-pane
  memoization keyed on pane inputs is the next lever; not worth the
  invalidation complexity today.
- `BenchmarkChatFloodUpdate` occasionally reports ~15 us/op instead of
  ~2.4 us/op on the first `-count` repetitions (suspected CPU frequency
  scaling on the benchmark box); prefer best-of-N when comparing.

# Swarm Findings

This file was absent from the Track B worktree at start (along with RECON.md);
created by Track B to record cross-track handoffs.

## Track D (config)

- A `features.clipboard` (or similar) config key could let users disable the
  OSC 52 clipboard write from `y` entirely — some remote/tmux setups treat
  clipboard escapes as a policy concern. Track B deliberately did not touch
  the config schema; the key is gated on interactive terminals only today.

## Out of scope observations (Track B)

- `selectMessage` (j/k) does not scroll the viewport to keep the cursor
  visible; only search jumps do (`scrollToMessage`). Reusing
  `scrollToMessage` from `selectMessage` would make j/k follow the cursor
  off-screen, but it changes long-standing behavior and is left for a
  deliberate decision.
- `maxScrollOffset` uses a `rows ≈ messages × 4` heuristic; the paint-time
  clamp corrects it, but `g`/`home` can momentarily overshoot on short
  buffers until the next frame. Harmless, documented in the code.

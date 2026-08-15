# Track D — Findings (out of scope for this track)

- `docs/swarm/RECON.md` / `FINDINGS.md` did not exist in the features
  worktree at branch time; this file was created fresh by Track D.
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

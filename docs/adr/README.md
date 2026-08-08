# Architecture Decision Records

Each ADR records one decision, its context, and its consequences at the time it
was made. They are historical documents: a later change is recorded by adding a
note here or writing a new ADR, not by editing an old one into agreement with
the current code.

## Verifying an ADR against the code

Two habits are worth keeping.

**Check the dependency edges before trusting a diagram.**
`go list -deps ./internal/<pkg>` settles any question about which package
actually depends on which. The diagrams in [../architecture.md](../architecture.md)
are maintained by hand and can drift.

**Check that a described mechanism was actually built.** An ADR can record a
decision whose *intent* held while its *mechanism* was replaced by something
better during implementation. When that happens, note it here rather than
rewriting the record.

## Status notes

- **0001 — Poll `liveChatMessages.list`.** Holds. The decision rests on
  `liveChatMessages.streamList` being absent from the REST discovery document
  despite being documented as a low-latency server-streaming alternative. If it
  ever appears there, this is the ADR to supersede — the transport is factored so
  a streaming source can be substituted behind the same `Poller` type.

- **0002 — Hand-roll the REST client.** Holds. `golang.org/x/oauth2` and
  `charmbracelet/bubbles` were both removed during integration and the module
  re-tidied, which confirmed the decision rather than merely restating it.

- **0003 — Normalize live chat events before rendering.** Holds, and has already
  paid for itself: `giftEvent` (added 2026-03-26) and the removal of
  `messageDeletedEvent` from the reference (2026-06-23) were both absorbed inside
  `internal/youtube` with no change to the renderer or the app.

- **0004 — Pace polling against an estimated quota budget.** Holds, with one
  honest caveat that the ADR states in full: every unit cost it depends on is a
  community estimate, because Google publishes none for live-chat methods.

- **0005 — Render everything as text.** Holds. Unlike `twi`'s equivalent
  decision, which was superseded when its Kitty-graphics path was removed, this
  one was never a trade-off between two workable options: the API supplies no
  badge imagery and no per-message emote metadata, so there is nothing to render
  an image from.

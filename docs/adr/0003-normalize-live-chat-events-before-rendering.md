# 0003: Normalize Live Chat Events Before Rendering

## Status

Accepted.

## Context

`liveChatMessages.list` returns one item shape with a `snippet.type` discriminator
and a differently-named detail object per type: `textMessageDetails`,
`superChatDetails`, `superStickerDetails`, `newSponsorDetails`,
`memberMilestoneChatDetails`, `membershipGiftingDetails`,
`giftMembershipReceivedDetails`, `messageDeletedDetails`, `userBannedDetails`,
and more. Eighteen documented types, and the set moves: `giftEvent` was added on
2026-03-26, and `messageDeletedEvent` was removed from the reference on
2026-06-23 as "not being returned by the API".

Rendering needs stable fragments for author names, badges, timestamps, mentions,
paid banners, membership notices, moderation notices, deletions, and emoji — and
the reveal animation must operate on grapheme-safe units, not on raw string
slices, so ANSI styles and emoji clusters are not corrupted mid-frame.

If the renderer switched on `snippet.type`, every new YouTube event type would
require touching every layout, and an unrecognized one would be a crash or a
silent drop.

## Decision

Normalize every incoming API item and every locally generated event into
`internal/youtube` models before it reaches the renderer.

Two levels of classification, deliberately:

- **`EventKind`** carries the precise event, with an entry for every documented
  `snippet.type` — including the ones the API no longer delivers, decoded
  defensively. An unrecognized type maps to `EventKindUnknown`, is carried
  through with `RawType` intact, and is rendered from `snippet.displayMessage`.
  It is never dropped and never a crash.
- **`MessageType`** is the coarse render class — `chat`, `paid`, `membership`,
  `notice`, `system`, `unknown` — and is what the renderer and the local filters
  switch on. Adding an `EventKind` therefore does not require touching a layout.

Four things are kept **off** the message stream and delivered on their own
channels:

- **Moderation events** (deletions, tombstones, bans, timeouts). A deletion is an
  instruction to take text off a screen that is frequently on stream; rendering
  it as another chat row would put the removed text back in front of the viewer.
  Consumers apply it to messages they already hold.
- **Room events** (members-only mode, chat ended, broadcast offline).
- **Poll state**, fed by both `pollEvent` items and the list response's
  out-of-band `activePollItem`.
- **Connection state**.

Deletions in practice surface as **tombstones** — a previously delivered message
reappearing with `hasDisplayContent` false — plus the local echo of a successful
delete call. `messageDeletedEvent` is decoded but must not be relied on.

## Consequences

- The UI is independent of the wire format. `giftEvent` and the
  `messageDeletedEvent` removal were both absorbed inside `internal/youtube` with
  no change to `internal/render` or `internal/app`.
- Rendering and animation are testable with no network and no credential.
- A new event type degrades into a readable row rather than a gap in the chat.
- There is an up-front conversion layer, but it removes the entire class of bug
  where a layout forgets one of eighteen detail shapes.
- Moderation being a separate stream means a consumer that ignores it shows stale
  text — so the app must handle it, and the boundary makes that a visible
  omission rather than an invisible one.

## Verification

- Unit-test normalization for every documented `snippet.type`, including the
  deprecated and no-longer-delivered ones, and for an invented unknown type.
- Unit-test that an unknown type keeps `RawType` and renders its
  `displayMessage`.
- Unit-test tombstone correlation against messages the client has actually seen —
  a tombstone for a message never shown has nothing to redact.
- Unit-test paid-message currency and tier rendering, membership levels, and
  gifting-burst collapsing.
- Unit-test render fragment width accounting with width-aware helpers, and
  partial reveal frames for grapheme-cluster, ANSI, and emoji safety.
- Run `go test ./internal/youtube ./internal/render ./internal/app`.

# 0005: Render Avatars, Badges, And Emoji As Text Only

## Status

Accepted.

## Context

Terminal chat clients face a recurring temptation: render avatars and emotes as
real images using Kitty graphics, iTerm2 inline images, or sixel. `twi` built
that path and later removed it, superseding its own ADR.

For `yc` the question does not arise the same way, because the data is not there.
`liveChatMessages.list` returns, per message:

- `authorDetails.profileImageUrl` — a URL to the author's avatar;
- `authorDetails.isVerified`, `isChatOwner`, `isChatSponsor`, `isChatModerator` —
  four booleans;
- `snippet.displayMessage` and the per-type detail objects.

There is **no badge imagery** — the role is four booleans, not an image set. There
is **no per-message emote metadata** at all: YouTube channel emotes arrive inside
the message text as `:shortcode:` and the API supplies no position map, no image
URL, and no emote ID. Standard emoji arrive as ordinary Unicode.

So the only asset with an image behind it is the avatar, and rendering it would
mean: an HTTP download per author, an image cache on disk, decode and resize, a
terminal capability probe, a protocol implementation, placeholder cells that
reserve stable width so a late arrival does not reflow scrollback, and a failure
path back to text anyway. All of that, for a 1–2 cell thumbnail, on a client
whose defining constraint is that it must not spend resources it does not have to.

## Decision

Render everything as text. There is no image decode, download, cache, terminal
capability probe, or graphics protocol path anywhere in `yc`.

- **Avatars** are an `[XY]` initials chip derived from the display name, in that
  author's stable color, or nothing at all when `avatar_mode = "off"`.
  `profileImageUrl` is retained for the inspect panel and diagnostics and is
  never fetched.
- **Badges** are glyphs (`badge_mode = "glyph"`), compact labels such as `[mod]`
  and `[member]` (`"text"`), or absent (`"off"`), derived from the four booleans.
- **Emoji** are the native Unicode glyph, detected by grapheme cluster.
- **Channel shortcodes** render as their `:shortcode:` text, optionally on a
  tinted chip (`highlight_emoji`).
- **Author color** is a deterministic, contrast-corrected hash of the author's
  channel ID, carried into the name, the message surface, and the gutter rail —
  which is what actually makes a busy chat scannable, and costs nothing.

`avatar_mode` accepts only `off` and `initials`. There is no third value to add
later without revisiting this record.

## Consequences

- No network work in the render path, so `View` stays pure and rendering stays
  synchronous row construction.
- No cache of downloaded bytes, which keeps `internal/storage` to tiny records —
  the quota ledger and diagnostics — and keeps the disk footprint negligible.
- No terminal capability matrix. `yc` looks the same in every terminal that can
  do 256 colors, and degrades to fewer.
- No reflow class of bug: nothing arrives late, so no placeholder needs to
  reserve width and no scrollback shifts under the user.
- Avatars are genuinely less pretty than a real image would be. That is the whole
  cost, and it buys the four properties above.
- If YouTube ever ships per-message emote metadata, this record needs revisiting
  — for emotes specifically, and probably not for avatars.

## Verification

- Unit-test initials derivation for Latin, CJK, emoji-leading, and single-character
  display names.
- Unit-test badge rendering in all three modes against every combination of the
  four role booleans.
- Unit-test emoji detection on grapheme-cluster boundaries, including ZWJ
  sequences, skin-tone modifiers, and combining marks.
- Unit-test that author color is stable across sessions for one channel ID and
  readable against both alternating group surfaces.
- Grep guard: no image, decode, download, or graphics-protocol dependency appears
  in `go.mod`, and `internal/render` performs no I/O.

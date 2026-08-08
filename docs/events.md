# Live Chat Events

Every YouTube live-chat event `yc` handles: the API field it comes from, the
normalized shape it becomes, and exactly how it renders in all three layouts.

Everything here is produced by `internal/youtube/normalize.go` and rendered by
`internal/render`. The rendered rows on this page are copied from
`internal/render/testdata/event_rows.golden`, which is generated from real
`Rows()` output — see [How To Verify This Page](#how-to-verify-this-page).

For why events are normalized before rendering at all, read
[adr/0003-normalize-live-chat-events-before-rendering.md](adr/0003-normalize-live-chat-events-before-rendering.md).
For why none of it is an image, read
[adr/0005-render-everything-as-text.md](adr/0005-render-everything-as-text.md).

## One Endpoint, Four Streams

Every event arrives in the same place: an item in a `liveChatMessages.list`
response, identified by `snippet.type`. `yc` fans one response out into four
independent streams, because they mean four different things to a UI:

```text
  liveChatMessages.list response
        |
        |  normalizeItem, per item, switching on snippet.type
        |
        +--> Messages     []youtube.Message        rows to draw
        +--> Moderations  []youtube.ModerationEvent  content to REMOVE
        +--> RoomEvents   []youtube.RoomEvent      chat-wide state changes
        +--> Polls        []youtube.PollState      creator poll tallies
```

Splitting them is not tidiness. **A moderation event is an instruction to take
text off a screen**, so emitting it as another chat row would put the removed
words back in front of a viewer on a terminal that is frequently on stream.
Consumers apply moderation to messages they already hold. The same reasoning is
why a room event and its readable chat row are two separate values rather than
one overloaded one.

## Every Event Kind

`snippet.type` is normalized to an `EventKind` by
`youtube.EventKindFromSnippetType`, and each `EventKind` maps to a coarse
`MessageType` that the renderer and the local filters switch on. The indirection
is deliberate: a newly added `EventKind` does not require touching every layout.

| `snippet.type` | `EventKind` | `MessageType` | Stream | Body text comes from |
| --- | --- | --- | --- | --- |
| `textMessageEvent` | `text` | `chat` | Messages | `snippet.textMessageDetails.messageText`, falling back to `displayMessage` |
| `superChatEvent` | `super_chat` | `paid` | Messages | `superChatDetails.userComment` |
| `superStickerEvent` | `super_sticker` | `paid` | Messages | `superStickerDetails.superStickerMetadata.altText` |
| `giftEvent` | `gift` | `paid` | Messages | gift `altText`, else the gift name |
| `fanFundingEvent` | `fan_funding` | `paid` | Messages | `fanFundingEventDetails.userComment` |
| `newSponsorEvent` | `new_sponsor` | `membership` | Messages | `displayMessage` |
| `memberMilestoneChatEvent` | `member_milestone` | `membership` | Messages | `memberMilestoneChatDetails.userComment` |
| `membershipGiftingEvent` | `membership_gifting` | `membership` | Messages | `displayMessage` |
| `giftMembershipReceivedEvent` | `gift_membership_received` | `membership` | Messages | `displayMessage` |
| `pollEvent` | `poll` | `notice` | Messages **and** Polls | `"poll: " + questionText` |
| `sponsorOnlyModeStartedEvent` | `sponsor_only_mode_started` | `notice` | Messages **and** RoomEvents | `displayMessage`, else `"members-only mode started"` |
| `sponsorOnlyModeEndedEvent` | `sponsor_only_mode_ended` | `notice` | Messages **and** RoomEvents | `displayMessage`, else `"members-only mode ended"` |
| `chatEndedEvent` | `chat_ended` | `notice` | RoomEvents only | — (documented as carrying no `displayMessage`) |
| `tombstone` | `tombstone` | `unknown` | Moderations only | — (no display content by definition) |
| `messageDeletedEvent` | `message_deleted` | `unknown` | Moderations only | — |
| `messageRetractedEvent` | `message_retracted` | `unknown` | Moderations only | — |
| `userBannedEvent` | `user_banned` | `unknown` | Moderations only | — |
| `invalidType` | `invalid_type` | `unknown` | Messages | `displayMessage` (often empty) |
| *anything else* | `unknown` | `unknown` | Messages | `displayMessage`, with `RawType` preserved |

`MessageType` values are `chat`, `paid`, `membership`, `notice`, `system`, and
`unknown`. `system` is never produced by the transport — it is the class for rows
`yc` composes locally.

### Two events are on two streams at once

`sponsorOnlyModeStartedEvent`, `sponsorOnlyModeEndedEvent`, and `pollEvent`
produce **both** a `RoomEvent`/`PollState` *and* a readable chat row. The room
event drives state (the members-only indicator, the poll pane); the chat row is
what a viewer scrolling history later sees. Neither can be derived from the
other without losing something.

`chatEndedEvent` is the opposite: it produces a `RoomEvent` and no row at all. It
is a room transition, not a chat message; the poller turns it into
`ConnectionClosed` and the history on screen stays readable.

## Author And Badges

`authorDetails` supplies four booleans and nothing else about status. That is the
entire badge vocabulary — there is no VIP, founder, staff, or turbo equivalent to
model, and no badge imagery of any kind.

| API field | Badge | Glyph | Text mode |
| --- | --- | --- | --- |
| `isChatOwner` | owner | `◉` | `[own]` |
| `isChatModerator` | moderator | `⚔` | `[mod]` |
| `isChatSponsor` | member | `★` | `[mem]` |
| `isVerified` | verified | `✓` | `[ver]` |

Badges are derived **after** the membership details have had their say, so a
`newSponsorEvent` shows a member badge on the row that announces it. A badge kind
added to the API after this build renders as `•`, keeping the column occupied so
the rows below still line up.

Every glyph is a plain Unicode symbol of display width 1 under `uniseg`, not a
Nerd Font private-use codepoint: an emoji-presentation character would measure
two cells and silently break the badge column on every row that carried it.

Author color is a deterministic hash of `channelId` (falling back to the display
name), contrast-corrected against both message surfaces. There is no author color
in the API, and no UI-owned mutable color state.

## Body Fragments

The API supplies **no** fragment metadata: no emote ranges, no mention spans, no
link positions. `youtube.SplitFragments` produces them by scanning the text.

| Fragment | Is |
| --- | --- |
| `text` | ordinary body text |
| `mention` | an `@name` token that starts a word |
| `emoji` | a Unicode emoji grapheme cluster |
| `shortcode` | a `:name:` or `:_name:` channel-emoji literal |
| `url` | a detected link |

A `shortcode` is rendered as a fixed-width token chip because the API never
resolves it to an image — the channel emoji arrives in the message text as
literal `:name:` with no ID, no URL, and no position map.

A fragment type the renderer does not recognize falls through to a local rescan
rather than being dropped, so a span type added to the transport still renders
before `internal/render` learns it.

## The Three Layouts

`ctrl+g` cycles them; `message_layout` in [config.md](config.md) sets the
default.

| Layout | Shape |
| --- | --- |
| `inline` | avatar, clock, badges, name, and body on one row. Densest. |
| `grouped` | an author header row, body indented beneath. Consecutive messages from one author skip the header. |
| `compact` | body text with a bare `name:` prefix. No avatar, clock, or badges. |

Inline budgets its decorations before building anything and drops them **in
order — badges, then avatar, then timestamp** — until the author's name fits. The
name is the one part that is never sacrificed: a row nobody can attribute is
worse than a row with no clock on it.

Grouped indents the body by 3 cells at width ≥ 40, 2 at ≥ 20, and 0 below that,
so the indent never eats the message. An authorless row (a notice) is never
grouped — there is no name to head a group with, and grouping would spend a whole
row on a bare clock.

## Every Kind, Rendered

Copied verbatim from the golden at width 72, with `|` marking the row edges.
`[B]` and `[CD]` are initials chips; `avatar_mode = "off"` removes them.

### Ordinary chat — `textMessageEvent`

```text
inline   |[B]  20:00 Bob: hello chat, how is everyone doing tonight|
grouped  |[B]  Bob 20:00|
         |   hello chat, how is everyone doing tonight|
compact  |Bob: hello chat, how is everyone doing tonight|
```

### Super Chat — `superChatEvent`

The amount is a solid **chip** — colored ground with contrast-corrected text —
not tinted text. On a busy chat the point of a Super Chat row is that it is
impossible to scroll past, and a block reads at a glance where a colored word
does not.

```text
inline   |[B]  20:00 Bob:  $5.00  thanks for the stream|
grouped  |[B]  Bob 20:00|
         |    $5.00  thanks for the stream|
compact  |Bob:  $5.00  thanks for the stream|
```

The chip prefers YouTube's own pre-localized `amountDisplayString`. When that is
absent it is reconstructed from `amountMicros`, which is parsed as an integer and
**never** converted to a float — a float round trip would silently misprint large
amounts in low-denomination currencies, which is exactly the row a viewer
screenshots.

The chip's color comes from the purchase tier (`1`–`11`), mapped onto palette
roles rather than hex. YouTube's own ladder runs blue → cyan → green → yellow →
orange → magenta → red; eleven distinct hues do not exist in a nine-role palette,
so the ladder collapses onto six monotonic steps built from `Accent`, `Success`,
`Warning`, and `Error` with blends between them. The exact amount is on the chip
either way; the color only has to convey "more than the last one".

**A paid event with no amount is not decorated at all** and falls back to plain
text. Without the money there is nothing a chip could say that the text does not.

### Super Sticker — `superStickerEvent`

`yc` fetches no images, so the sticker's **alt text is the only renderable form**.
Dropping it would leave a bare price.

```text
inline   |[B]  20:00 Bob:  €2.00  sticker: a cat waving a tiny flag|
grouped  |[B]  Bob 20:00|
         |    €2.00  sticker: a cat waving a tiny flag|
compact  |Bob:  €2.00  sticker: a cat waving a tiny flag|
```

Unlike a Super Chat there is no user comment field on a sticker.

### Virtual gift — `giftEvent`

Gifts are denominated in **jewels**, not money, so the chip carries a jewel count
behind a `◆`. A combo is folded into the label so a burst of the same gift reads
as one escalating event rather than as repetition.

```text
inline   |[B]  20:00 Bob:  ◆ 250 jewels  Star Shower x3|
grouped  |[B]  Bob 20:00|
         |    ◆ 250 jewels  Star Shower x3|
compact  |Bob:  ◆ 250 jewels  Star Shower x3|
```

Google's two official sources disagree about the wire shape — the discovery
document places a flat `snippet.giftDetails`, the HTML reference nests the same
fields under `snippet.giftEventDetails.giftMetadata` — so the normalizer decodes
**both** into one struct.

### Fan funding — `fanFundingEvent`

The pre-Super-Chat tip event, deprecated since 2017. It is decoded into the Super
Chat shape rather than given a pointer of its own, because it is the same fact —
a tip with a comment — and `RawType` still records what actually arrived.

```text
inline   |[B]  20:00 Bob:  £3.00  an old-style tip|
compact  |Bob:  £3.00  an old-style tip|
```

### Memberships — four events, one chip family

The chip states **what happened**, never who: the author column already names the
person, and repeating the name would cost the columns the message text needs. The
level name follows the chip as a muted italic detail, because it is the one part
of a membership event a viewer cannot infer.

`newSponsorEvent` (`isUpgrade` false):

```text
inline   |[CD] 20:00 ★ Carol Danvers:  ★ new member  Comet Crew|
grouped  |[CD] Carol Danvers ★ 20:00|
         |    ★ new member  Comet Crew|
compact  |Carol Danvers:  ★ new member  Comet Crew|
```

`newSponsorEvent` with `isUpgrade` true renders `★ member upgrade`.

`memberMilestoneChatEvent` — the only membership event that carries a body, from
`userComment`:

```text
inline   |[CD] 20:00 ★ Carol Danvers:  ★ member 14 mo  Comet Crew still the best |
         |                            chat on the platform|
grouped  |[CD] Carol Danvers ★ 20:00|
         |    ★ member 14 mo  Comet Crew still the best chat on the platform|
compact  |Carol Danvers:  ★ member 14 mo  Comet Crew still the best chat on the |
         |               platform|
```

`membershipGiftingEvent` — a `♥` chip in the `Success` role, with the count:

```text
inline   |[CD] 20:00 ★ Carol Danvers:  ♥ 5 gift memberships  Comet Crew|
compact  |Carol Danvers:  ♥ 5 gift memberships  Comet Crew|
```

`giftMembershipReceivedEvent` — one recipient of that burst:

```text
inline   |[B]  20:00 Bob:  ♥ gift member  Comet Crew|
compact  |Bob:  ♥ gift member  Comet Crew|
```

The received event carries `gifterChannelId` and
`associatedMembershipGiftingMessageId`, which correlate it back to the gifting
burst that produced it.

### Room notices — members-only mode, chat ended, polls

Room-state changes describe the room rather than a person, so they carry **no
chip**: a colored block would give a mode change the same visual weight as a paid
message. They get a bold `[notice]` marker in the `Warning` role, and no author
column at all — printing `notice:` where the name goes would spend the widest
part of the row restating the marker the body already carries.

```text
inline   |20:00 [notice] members-only mode on|
inline   |20:00 [notice] members-only mode off|
inline   |20:00 [notice] live chat ended|
inline   |20:00 [notice] poll which map next?|
compact  |[notice] members-only mode on|
```

`compact` drops the clock too, since it drops every decoration.

### Moderation notices — `userBannedEvent`

A ban does have an author (the moderator who issued it, when the API reports
one), so it keeps the author column *and* takes the `[notice]` marker:

```text
inline   |[MD] 20:00 ⚔ mod_dana: [notice] Bob was timed out for 5m|
grouped  |[MD] mod_dana ⚔ 20:00|
         |   [notice] Bob was timed out for 5m|
compact  |mod_dana: [notice] Bob was timed out for 5m|
```

`banDurationSeconds` arrives as a `uint64` in a JSON string. A `banType` of
`temporary`, or any non-zero duration, makes it a timeout; otherwise it is
permanent.

### Deletions — the one rule with no exceptions

A deleted message is replaced **wholesale**. The original text is not reprinted:
not struck through, not in the inspect panel, not in a status line, not in a
confirmation prompt. It survives only in a debug log, which is opt-in.

```text
inline   |[B]  20:00 Bob: [message deleted]|
grouped  |[B]  Bob 20:00|
         |   [message deleted]|
compact  |Bob: [message deleted]|
```

A `tombstone` for a message `yc` never saw has no author to attribute:

```text
inline   |20:00 [message deleted]|
compact  |[message deleted]|
```

**Tombstones are how deletion actually reaches `yc`.** A message the viewer
already saw comes back with `hasDisplayContent` false. If `yc` holds the original
it becomes a `ModerationMessageDeleted`; if it does not, it is reported as a
`ModerationTombstone` — there is nothing on screen to redact, and saying so is
better than emitting a deletion the UI cannot act on.

`messageDeletedEvent` and `messageRetractedEvent` are handled identically but are
**not delivered in practice**: YouTube removed both from the reference on
2026-06-23 as "not being returned by the API". The enum members survive in the
discovery document, so an arriving one is handled rather than dropped.

## Moderation Events

The four values on the `Moderations` stream, none of which ever carries message
text:

| `ModerationType` | Produced by | Carries |
| --- | --- | --- |
| `message_deleted` | a tombstone for a message `yc` holds, or `yc`'s own successful delete | `TargetMessageID` |
| `tombstone` | a tombstone for a message `yc` never saw | `TargetMessageID` |
| `user_banned` | `userBannedEvent` with no duration | `TargetChannelID`, `TargetDisplayName` |
| `user_timed_out` | `userBannedEvent` with a duration | the above, plus `Duration` |

`yc`'s own successful deletions and bans are echoed onto this stream locally,
because the API does not reliably report them back — without the echo, a
completed moderation action would leave no trace in the activity column at all.
See [moderation.md](moderation.md).

## Room Events

| `RoomEventType` | From |
| --- | --- |
| `sponsor_only_started` | `sponsorOnlyModeStartedEvent` |
| `sponsor_only_ended` | `sponsorOnlyModeEndedEvent` |
| `chat_ended` | `chatEndedEvent` |
| `stream_offline` | the list response's `offlineAt`, not an item |

`stream_offline` is **not** a reason to stop polling. Chat outlives the broadcast
by a short window, so `yc` keeps polling through a 2-minute grace period and only
closes if nothing new arrives.

## Polls

`pollEvent` items and the list response's own `activePollItem` both feed
`PollState`: a question, a status (`active` / `closed` / unknown), and options
with tallies. Tallies arrive as JSON strings and are parsed as integers.

## The Activity Column

`space a` toggles a narrow column that summarizes non-chat events, so a fast
chat does not bury them. Each entry is one width-1 glyph plus a line of text.

| Glyph | Role | Entries |
| --- | --- | --- |
| `◈` | Warning | Super Chat, Super Sticker, fan funding, `giftEvent` — *"Bob sent $5.00"*, *"Bob sent Star Shower"* |
| `★` | Accent | new member, member milestone — *"Carol became a member (Comet Crew)"*, *"Carol hit 14 months"* |
| `♥` | Success | membership gifting and gifts received — *"Carol gifted 5 memberships"*, *"Bob received a gift membership"* |
| `⊘` | Error | moderation — *"a message was deleted"*, *"Bob was banned"*, *"Bob was timed out for 5m"* |
| `●` | Success | members-only mode on/off, live chat ended |
| `▤` | Accent | poll updated |
| `⟳` | Warning | quota transitions |
| `▸` | Muted | chat |

A moderation entry names the person and states the action. It never reproduces
the removed content — the same rule the chat pane follows.

A run of **gift-membership** entries inside one time window collapses past a
limit into a single *"+N more gift memberships"* summary, because a gifting burst
is one event that the API happens to deliver as many.

Ordinary `textMessageEvent` rows produce **no** activity entry: the column exists
for what the chat pane would bury.

## The Unknown-Kind Fallback Contract

This is absolute, and it is the reason the table above ends with a wildcard row.

**Any `snippet.type` `yc` does not recognize — including `invalidType`, and
including whatever YouTube ships next — becomes a `Message` with:**

- `Kind = EventKindUnknown` and `Type = MessageTypeUnknown`;
- `Text` taken from `snippet.displayMessage`;
- `RawType` set to the original `snippet.type`, for redacted diagnostics only;
- the author, badges, timestamp, and fragments built exactly as for a chat row.

**Never a crash. Never a dropped row. Never a blank line.**

```text
inline   |[B]  20:00 Bob: a brand new event nobody has shipped support for|
compact  |Bob: a brand new event nobody has shipped support for|
```

`invalidType` is the API's own enum member for an event it could not classify. It
usually carries no `displayMessage`, so it renders as an attributed row with an
empty body:

```text
inline   |[B]  20:00 Bob: |
```

That is the honest rendering: something happened, `yc` knows who and when, and
the API said nothing about what.

Rendering must switch on `Kind` or `Type`, never on `RawType`. `RawType` exists
so a debug log can say what arrived, and for nothing else.

## Layout Invariants

Enforced by tests, for every event kind, in every layout, at every badge mode,
at every width from `MinimumRenderWidth` to 96:

- **No row exceeds the width it was rendered at.** One over-wide row wraps in the
  terminal itself and desynchronizes the app's scroll arithmetic from what is on
  screen.
- **Chips and badges are atomic** — they are never split across a wrap.
- Wrapping is grapheme-cluster aware. There is no byte or rune slicing of
  user-visible text anywhere in the package.

## How To Verify This Page

```sh
# every kind, every layout, at two widths - the source of the rows above
go test ./internal/render -run TestEventRowsGolden
sed -n '1,40p' internal/render/testdata/event_rows.golden

# a new snippet.type cannot slip past both the fixture list and the golden
go test ./internal/render -run TestEveryEventKindHasAGoldenFixture

# no row may exceed its width, in any layout, at any badge mode
go test ./internal/render -run TestEventRowsNeverExceedWidth

# a deleted message never reprints its text
go test ./internal/render -run TestDeletedMessageNeverReprintsItsText

# the normalizer, including every documented snippet.type and unknown types
go test ./internal/youtube -run Normalize
```

To regenerate the golden after an intentional rendering change:

```sh
go test ./internal/render -update
```

The kind table itself is `snippetTypes` in `internal/youtube/model.go`:

```sh
sed -n '/^var snippetTypes/,/^}/p' internal/youtube/model.go
```

**Status: ready.** Normalization and rendering of every kind on this page are
exercised by unit tests and golden fixtures with no credentials, and by
`yc chat --mock`. What has **never** been verified is that Google's live API
sends these shapes as documented — no credentialed YouTube path has ever been
run. See [manual-validation.md](manual-validation.md).

# Moderation

`yc` can delete a message, time a viewer out, and ban them permanently, from the
chat pane, on the message the cursor has selected.

This is the one path in `yc` that removes somebody else's words from a live
broadcast. Three properties shape everything on this page:

1. **It never fails silently.** A key that appears to do nothing is the worst
   possible outcome for a moderator mid-incident, so every refusal names a reason
   you can act on, and every reason reaches the status bar.
2. **It never reprints what it removed.** Redacted text is dropped from the
   rendered row and survives only in an unexported rollback record that no
   surface reads. A moderation action that put the words back on screen — on a
   terminal that is frequently on stream — would defeat itself.
3. **It confirms before it acts.** Delete and ban are each one keystroke away
   from keys used constantly (`j`, `k`, `r`), and neither can be undone from
   here.

For the keys in context, read [keybindings.md](keybindings.md). For what a
moderation event looks like when it arrives *from* YouTube, read
[events.md](events.md).

## What You Need

| Requirement | Why |
| --- | --- |
| An **OAuth** credential | An API key is never an accepted identity for a write. |
| The **`youtube.force-ssl`** scope | It is the narrowest scope covering `liveChatMessages.delete` and `liveChatBans.insert`/`delete`. |
| To be the **owner or a moderator** of that chat | YouTube enforces this; `yc` checks what it can before spending a request. |

```sh
yc login          # requests youtube.readonly + youtube.force-ssl
yc login --read-only   # requests readonly only — moderation stays disabled
```

`yc` never requests the broad `https://www.googleapis.com/auth/youtube` scope,
but it **honors** it when Google reports it as granted for a credential minted
elsewhere: the broad scope satisfies a `force-ssl` requirement. See
[auth.md](auth.md).

## The Capability Ladder

Before a destructive key can arm anything, `yc` asks whether this session can
moderate this chat. The checks run cheapest-first and **each failure names
itself**, because "moderation unavailable" on its own teaches you nothing about
which of five different things to go and fix.

| Check | Status line when it fails |
| --- | --- |
| a chat source exists | `moderation unavailable: no chat source` |
| the source implements `Moderator` | `moderation unavailable: this chat source cannot moderate` |
| the source reports it can act | `moderation unavailable: this session has no credential that can moderate; run yc login and grant youtube.force-ssl` |
| a chat is open and keyed | `moderation unavailable: no chat open` |
| granted scopes include `force-ssl` | `moderation unavailable: moderation needs the youtube.force-ssl scope; run yc login again and approve it` |
| your role is not "viewer" | `moderation unavailable: you are not a moderator of this chat` |

(The app's own strings wrap `yc login` in backticks; they are dropped above so
the table cells render.)

The third check exists because Go interfaces are satisfied by a **type**, not by
an instance: a live adapter built without a moderating credential still *has* the
`Moderator` methods. Asserting `Moderator` alone would report the capability as
present and only discover otherwise **after** a destructive keystroke had been
confirmed. So a client that can moderate also answers `ModerationAvailable()`,
and "wired but unusable" is distinguishable from "not wired at all" before you
press anything.

`yc chat --mock`, the deterministic fake, and a key-only read session all run the
identical shell with the keys **disabled and explained** rather than missing.

## Roles, And Why "Unknown" Does Not Block

`yc` tracks four role values, not two:

| Role | Learned from |
| --- | --- |
| `owner` | `identity.ChannelID` equals the broadcast's `ChannelID`, or `authorDetails.isChatOwner` on a message you sent |
| `moderator` | `authorDetails.isChatModerator` on a message you sent |
| `viewer` | you have spoken and neither flag was set |
| `unknown` | you have not spoken in this chat yet |

**Unknown is not "not a moderator".** YouTube reports `isChatModerator` only on
authors who have actually spoken, so a moderator who has not typed anything is
indistinguishable from a viewer until they do. Collapsing the two would disable
moderation for exactly the person who most needs it.

So `unknown` leaves the keys live and **discloses the uncertainty at the moment
of arming**, appended to the confirmation prompt:

```text
permanently ban Bob? b/enter confirms, esc cancels (your role in this chat is unknown; YouTube decides)
```

The same rule applies to scopes. A token supplied through the environment or the
config file carries no record of its grant, so the scope list is empty rather
than known-insufficient — and refusing on a guess would disable moderation for a
perfectly good credential. The keys stay live and say so:

```text
delete Bob's message? d/enter confirms, esc cancels (granted scopes unknown; YouTube decides)
```

The API is the authority in both cases, and its refusal is already a redacted,
user-facing sentence. This mirrors the rule `yc` already follows for the composer
— see [auth.md](auth.md).

Only a **confirmed** `viewer` blocks.

## The Keys

Live only when the Chat tab is active, the chat pane has focus, no overlay is
open, and no `space` leader is pending.

| Key | Action | API call |
| --- | --- | --- |
| `d` | delete the selected message | `liveChatMessages.delete` |
| `t` | time the author out | `liveChatBans.insert` with `type=temporary` |
| `b` | ban the author permanently | `liveChatBans.insert` with `type=permanent` |

They are plain letters rather than `ctrl` chords because a moderator reaches for
them with the same hand already on `j`/`k`, and because every `ctrl` chord that
terminals report reliably is already spent.

### Confirmation

Every action confirms. The confirmation is **press the same key again**, so there
is no second thing to learn:

```text
delete Bob's message? d/enter confirms, esc cancels
time out Bob for 5m? t/enter confirms, esc cancels
permanently ban Bob? b/enter confirms, esc cancels
```

- `enter` also confirms.
- `esc` cancels, leaving `moderation cancelled` in the status bar.
- **Any other key disarms and then does its own job** — the same rule `ctrl+l`
  follows. Pressing `j` while a ban is armed moves the cursor and the ban is
  gone; pressing `t` while a ban is armed disarms the ban and opens the timeout
  prompt, rather than being swallowed as a cancellation.

An armed confirmation that quoted the message it was about to remove would put
those words back on screen, so it names **the person and the action, never the
text**.

### The timeout prompt

`t` opens a modal duration field, pre-filled with `5m` — the timeout YouTube's
own moderation menu offers first, so `enter` alone does the common thing.

```text
time out Bob for: 5m_ (enter confirms, esc cancels)
```

While it is open **every printable key is part of the duration**. Typing `1k`
moves no cursor and toggles no filter. `backspace` deletes one grapheme cluster,
`ctrl+u` clears the field, `esc` cancels, and every other key is swallowed rather
than passed through — a modal prompt that leaked `pgup` into the scrollback
behind it is a prompt you cannot trust.

Accepted input:

| You type | Means |
| --- | --- |
| `60` | 60 seconds — a bare number is seconds, which is what the API takes and what a moderator typing in a hurry means |
| `90s`, `10m`, `1h30m` | any Go duration |

Bounds: **1 second minimum, 24 hours maximum**, truncated to whole seconds
because the API takes whole seconds and a sub-second remainder is a rounding
artifact rather than an intent. The field itself is capped at 12 display cells so
a held key cannot grow an unbounded string in the status bar.

Rejections are specific:

```text
timeout needs a duration, e.g. 60s, 10m, or 1h
timeout duration not understood; try 60s, 10m, or 1h
timeout must be at least 1s
timeout cannot exceed 24h; use the ban key for longer
```

Anything longer than a day is a ban in everything but name, and the ban key says
so out loud.

## Per-Target Refusals

Some targets are refused **before** anything is armed, so you never spend a
confirmation on an action that could not work:

| Situation | Status line |
| --- | --- |
| nothing selected | `select a message with j/k first` |
| the row is already removed | `that message is already removed` |
| a delete target with no ID | `that message has no id to delete` |
| a delete target still in local echo | `waiting for YouTube to confirm that message before it can be deleted` |
| a ban target with no channel ID | `no channel id for that author, so they cannot be banned` |
| the target is you | `that is your own channel` |
| the chat has no resolved `liveChatId` | `this chat is not resolved yet, so bans have nowhere to go` |

The local-echo refusal matters: `yc` renders its own sent messages immediately
from a local echo, and that row has no YouTube-confirmed ID to delete until the
poll reconciles it.

## Optimistic Redaction, With Rollback

Confirming does two things in this order: **the rows change first, and the
network answers second.** The whole point of the keystroke is that the words
leave the screen *now*.

```text
  confirm
     |
     |-- redact matching rows locally, snapshotting text + fragments
     |     delete  -> the one message
     |     ban/timeout -> EVERY message from that channel
     |
     |-- status: "deleting Bob's message" / "banning Bob" / "timing out Bob for 5m"
     |
     |-- one request, 30s timeout
     |
     +-- success -> echo a ModerationEvent locally; the rollback record is dropped
     +-- failure -> restore every snapshotted row exactly, and say so
```

A ban or a timeout removes **everything that channel has said**, which is what
YouTube's own client does and what a moderator means by the word.

The price of acting first is the rollback record, and that record is the **only**
reason the removed text is kept anywhere at all. It is unexported, it is read by
nothing but the rollback, and it is discarded the moment the request succeeds.

On failure the rows come back exactly as they were, and the message says so —
because leaving them blanked would hand you a terminal that disagrees with the
broadcast you are watching:

```text
could not permanently ban Bob: <redacted reason> (nothing was removed)
```

The reason is passed through the same credential-safe path every other error
uses. It cannot contain a token, a key, a URL, or any message text.

### The local echo on success

A successful action is echoed locally onto the moderation stream:

```text
deleted Bob's message
Bob timed out for 5m
Bob banned
```

and the activity column gains an `⊘` entry. This echo exists because **the API
does not report `yc`'s own deletions back**: `messageDeletedEvent` was removed
from the reference on 2026-06-23 as "not being returned by the API", and the only
inbound deletion signal is a previously seen message reappearing as a tombstone.
Without the echo a completed action would leave no trace at all.

### One at a time

While a request is in flight, a second moderation action cannot start. Two
overlapping optimistic redactions would share one rollback record and restore the
wrong rows.

## What The API Can And Cannot Do

Everything `yc` could ever offer here is bounded by three methods. This is the
whole live-chat moderation surface of the YouTube Data API v3.

### Possible

| Action | Method | Notes |
| --- | --- | --- |
| Delete one message | `liveChatMessages.delete` | Takes the message ID. |
| Permanent ban | `liveChatBans.insert`, `type=permanent` | Takes `liveChatId` + the target's `channelId`. |
| Timeout | `liveChatBans.insert`, `type=temporary` | Plus `banDurationSeconds`, a `uint64` sent as a JSON string. |
| Lift a ban | `liveChatBans.delete` | Takes the **ban ID** returned by the insert. |

### Not possible — no API exists

- **Slow mode**, in either direction.
- **Turning members-only mode on or off.** `yc` can *observe* it changing
  (`sponsorOnlyModeStartedEvent`) and cannot cause it.
- **Pinning or unpinning** a message.
- **Hiding a user** without banning them.
- **Editing** a message, yours or anyone's.
- **Appointing or removing moderators.**
- **Clearing the chat** for everyone. `ctrl+l` clears *your local view* only.
- **Warnings**, or any pre-ban action.
- **Listing existing bans.** `liveChatBans` has `insert` and `delete` and no
  `list`, so there is no way to enumerate who is banned.
- **Undoing a delete.** A deletion is permanent for everyone.

### Implemented but not reachable from the UI

**`Unban` is transport-only.** `youtube.Client.Unban` and the `Moderator`
interface both have it, and no key is bound to it. That is not an oversight
waiting on UI work — `liveChatBans.delete` requires the **ban ID returned by the
insert**, and since the API offers no way to list bans, `yc` can only unban
somebody it banned in the same process. A key that worked until you restarted
would be worse than no key.

Lift a ban from YouTube Studio.

## Quota

Every moderation call is charged to the same ledger as everything else, so the
poll cadence accounts for units you spend moderating.

| Call | Estimated cost |
| --- | --- |
| `liveChatMessages.delete` | 50 units |
| `liveChatBans.insert` | 50 units |
| `liveChatBans.delete` | 50 units |

These figures are **estimates**. Google's published quota cost table lists no
live-chat method at all, and the reference pages for these methods carry no
*Quota impact* line — but Google does document the shape: *"a write operation
that creates, updates, or deletes a resource usually costs 50 units."* All four
live-chat writes follow that rule exactly. Read [quota.md](quota.md) for the full
treatment and for how to correct a cost with a config line.

The practical consequence: `yc` pauses **polling** at the configured quota
reserve (10% by default) precisely so that sending and moderation still work when
the budget is nearly gone. Moderation is what the reserve is reserved for.

## Status Lines

Every moderation message is drawn in the status bar at **every terminal width**,
immediately after the pending-clear indicator, in one of three levels:

| Level | Color role | Used for |
| --- | --- | --- |
| info | `Success` | a completed action, or a disabled explanation |
| warn | `Warning` | an armed confirmation, an open prompt, a request in flight |
| error | `Error` | a refusal or a failed request |

An idle session with a working moderating credential says **nothing at all**. The
keys are in help; a permanent "moderation: ready" banner would only spend width
the quota meter needs.

No status line ever contains message text or credential material. That is
enforced by tests, not by review.

## Safety Summary

The things worth knowing before you press a key on a live broadcast:

- Nothing happens on the first press. Every action asks.
- The confirmation names the person, never the message.
- `esc` cancels; any unrelated key disarms.
- A ban removes every message from that channel from your view, not just the
  selected one.
- A failure puts everything back and tells you it did.
- A success cannot be undone from `yc`.
- Your own channel cannot be targeted.
- The removed text is never re-displayed anywhere in the UI.

## How To Verify This Page

```sh
# the whole moderation feature, credential-free
go test ./internal/app -run Moderation -v

# the transport calls, against httptest
go test ./internal/youtube -run Moderation -v

# the keys stay documented even while the capability is unavailable
go test ./internal/app -run 'ModerationKeys' -v
```

The tests cover: confirmation before action; `esc` cancels; an unrelated key
disarms and still does its own job; the optimistic redaction keeps the removed
words out of the row list **and** out of `View()`; rollback restores the exact
text while the failure line neither reprints it nor claims a removal; a ban
redacts every message from the target; success reaches the activity column
without reprinting text; the duration prompt is modal and rejects unparseable
input; all five disabled states produce an explanation and never reach the
transport; the four role values; the uncertainty disclosure; the per-target
refusals; and the confirmation rendering at widths 20–130.

## Status

| Aspect | Label |
| --- | --- |
| Keys, confirmations, prompts, refusals, redaction, rollback | **Ready** — exercised by tests and by `yc chat --mock` |
| Capability detection and every disabled explanation | **Ready** |
| The `liveChatMessages.delete` and `liveChatBans.insert` requests themselves | **Credentialed** — implemented and unit-tested against `httptest`, **never run against Google** |
| `Unban` | **Partial** — transport only, deliberately not bound to a key |
| Slow mode, pinning, members-only toggling, ban listing | **Out of scope** — no API exists |

No credentialed YouTube path has ever been exercised. See
[manual-validation.md](manual-validation.md), which is the only place such
evidence may be recorded.

> **Known gap.** `youtube.Identity.Scopes` is documented as the granted scope
> list, and nothing in `internal/youtube` currently populates it. Live sessions
> therefore land in the "granted scopes unknown" branch: the keys stay live, the
> uncertainty is disclosed, and YouTube decides. Populating `Scopes` from the
> credential source would make the disabled state precise rather than merely
> honest.
